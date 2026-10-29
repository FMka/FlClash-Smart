package core

import (
	"context"
	"errors"
	"io"
	"net"
	"net/netip"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/metacubex/http"
	"github.com/metacubex/mihomo/component/resolver"
	"github.com/metacubex/mihomo/config"
	C "github.com/metacubex/mihomo/constant"
	"github.com/metacubex/mihomo/dns"
	D "github.com/miekg/dns"
)

type smartDNSFakeResolver struct {
	err   error
	calls atomic.Int64
}

func (r *smartDNSFakeResolver) ExchangeContext(_ context.Context, q *D.Msg) (*D.Msg, error) {
	r.calls.Add(1)
	if r.err != nil {
		return nil, r.err
	}
	return smartDNSFakeAnswer(q), nil
}
func (r *smartDNSFakeResolver) LookupIP(context.Context, string) ([]netip.Addr, error) {
	return nil, r.err
}
func (r *smartDNSFakeResolver) LookupIPv4(context.Context, string) ([]netip.Addr, error) {
	return nil, r.err
}
func (r *smartDNSFakeResolver) LookupIPv6(context.Context, string) ([]netip.Addr, error) {
	return nil, r.err
}
func (r *smartDNSFakeResolver) ResolveECH(context.Context, string) ([]byte, error) { return nil, r.err }
func (*smartDNSFakeResolver) Invalid() bool                                        { return true }
func (*smartDNSFakeResolver) ClearCache()                                          {}
func (*smartDNSFakeResolver) ResetConnection()                                     {}

func smartDNSFakeAnswer(q *D.Msg) *D.Msg {
	answer := &D.Msg{}
	answer.SetReply(q)
	if q.Question[0].Qtype == D.TypeAAAA {
		answer.Answer = []D.RR{&D.AAAA{Hdr: D.RR_Header{Name: q.Question[0].Name, Rrtype: D.TypeAAAA, Class: D.ClassINET, Ttl: 60}, AAAA: net.ParseIP("2001:4860:4860::8888")}}
	} else {
		answer.Answer = []D.RR{&D.A{Hdr: D.RR_Header{Name: q.Question[0].Name, Rrtype: D.TypeA, Class: D.ClassINET, Ttl: 60}, A: net.ParseIP("8.8.8.8")}}
	}
	return answer
}

type smartDNSRoundTrip func(*http.Request) (*http.Response, error)

func (f smartDNSRoundTrip) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
func smartDNSFakeClient(provider smartDNSProvider, handler func(*D.Msg) *D.Msg) *smartDNSDoH {
	return &smartDNSDoH{provider: provider, client: &http.Client{Transport: smartDNSRoundTrip(func(req *http.Request) (*http.Response, error) {
		body, _ := io.ReadAll(req.Body)
		query := &D.Msg{}
		if err := query.Unpack(body); err != nil {
			return nil, err
		}
		answer := handler(query)
		wire, _ := answer.Pack()
		return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": []string{"application/dns-message"}}, Body: io.NopCloser(strings.NewReader(string(wire)))}, nil
	})}}
}
func smartDNSManagerForTest(t *testing.T) (*smartDNSManager, *smartDNSResolver) {
	t.Helper()
	running := isRunning.Load()
	isRunning.Store(true)
	smartRecordActivity()
	m := &smartDNSManager{enabled: true, network: "wifi:test", wrappers: map[string]*smartDNSResolver{}, failures: map[string]smartDNSFailure{}, remembered: map[string]smartDNSRemembered{}}
	m.makeClient = func(p smartDNSProvider, _ string, _ C.ProxyAdapter, _ string) (*smartDNSDoH, error) {
		return smartDNSFakeClient(p, smartDNSFakeAnswer), nil
	}
	w := m.wrap("direct", &smartDNSFakeResolver{err: context.DeadlineExceeded}, []dns.NameServer{{Net: "udp", Addr: "8.8.8.8:53"}}, nil)
	t.Cleanup(func() {
		m.invalidate("stopped")
		deadline := time.Now().Add(time.Second)
		for time.Now().Before(deadline) {
			m.mu.Lock()
			active := m.active
			m.mu.Unlock()
			if !active {
				break
			}
			time.Sleep(time.Millisecond)
		}
		isRunning.Store(running)
		smartEndDiagnostic("dns")
	})
	return m, w
}
func smartDNSTrigger(m *smartDNSManager, w *smartDNSResolver, host string) {
	m.observe(w, host, D.TypeA, context.DeadlineExceeded)
	m.mu.Lock()
	key := w.scope + ":" + host
	f := m.failures[key]
	f.last = time.Now().Add(-2 * time.Second)
	m.failures[key] = f
	m.mu.Unlock()
	m.observe(w, host, D.TypeA, context.DeadlineExceeded)
}
func smartDNSWait(t *testing.T, m *smartDNSManager, phase string) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if m.status().Phase == phase {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("phase=%+v, want %s", m.status(), phase)
}

func TestSmartDNSEffectiveDirectRecoveryWithoutUI(t *testing.T) {
	m, w := smartDNSManagerForTest(t)
	smartDNSTrigger(m, w, "m.vk.ru")
	smartDNSWait(t, m, "recovered")
	ips, err := w.LookupIPv4(context.Background(), "m.vk.ru")
	if err != nil || len(ips) != 1 || ips[0].String() != "8.8.8.8" {
		t.Fatalf("effective direct lookup=%v %v", ips, err)
	}
	if _, err = w.LookupIPv4(context.Background(), "unrelated.com"); err != nil {
		t.Fatal("confirmed branch outage did not restore the next public domain")
	}
	state := m.status()
	if state.Scope != "direct" || state.Source != "recovery" || state.Provider != "Google" {
		t.Fatalf("status=%+v", state)
	}
	if m.wrappers["destination"] != nil {
		t.Fatal("recovery created a global replacement")
	}
}

func TestSmartDNSStopsAndNetworkChangesCancelLateRecovery(t *testing.T) {
	for _, action := range []string{"stop", "network", "config", "disabled"} {
		t.Run(action, func(t *testing.T) {
			m, w := smartDNSManagerForTest(t)
			entered := make(chan struct{}, 1)
			release := make(chan struct{})
			var once sync.Once
			m.makeClient = func(p smartDNSProvider, _ string, _ C.ProxyAdapter, _ string) (*smartDNSDoH, error) {
				once.Do(func() { entered <- struct{}{}; <-release })
				return smartDNSFakeClient(p, smartDNSFakeAnswer), nil
			}
			smartDNSTrigger(m, w, "m.vk.ru")
			select {
			case <-entered:
			case <-time.After(time.Second):
				t.Fatal("episode did not start")
			}
			switch action {
			case "stop":
				isRunning.Store(false)
				m.invalidate("stopped")
			case "network":
				m.networkChanged("mobile:new")
			case "config":
				m.invalidate("observing")
			case "disabled":
				m.mu.Lock()
				m.enabled = false
				m.mu.Unlock()
				m.invalidate("disabled")
			}
			close(release)
			deadline := time.Now().Add(time.Second)
			for time.Now().Before(deadline) {
				m.mu.Lock()
				active := m.active
				m.mu.Unlock()
				if !active {
					break
				}
				time.Sleep(time.Millisecond)
			}
			m.mu.Lock()
			count := len(w.overlays)
			m.mu.Unlock()
			if count != 0 || m.status().Source == "recovery" {
				t.Fatal("late recovery was applied")
			}
		})
	}
}

func TestSmartDNSNoNewPollingAndBurstSharedGate(t *testing.T) {
	m, w := smartDNSManagerForTest(t)
	var calls atomic.Int64
	m.makeClient = func(p smartDNSProvider, _ string, _ C.ProxyAdapter, _ string) (*smartDNSDoH, error) {
		calls.Add(1)
		return smartDNSFakeClient(p, smartDNSFakeAnswer), nil
	}
	if m.status().DiagnosticCount != 0 || calls.Load() != 0 {
		t.Fatal("idle created diagnostic traffic")
	}
	if !smartBeginDiagnostic("transport") {
		t.Fatal("gate unavailable")
	}
	smartDNSTrigger(m, w, "m.vk.ru")
	if calls.Load() != 0 {
		t.Fatal("overlapping transport/DNS episode")
	}
	smartEndDiagnostic("transport")
	m.mu.Lock()
	f := m.failures["direct:m.vk.ru"]
	f.last = time.Now().Add(-2 * time.Second)
	m.failures["direct:m.vk.ru"] = f
	m.mu.Unlock()
	m.observe(w, "m.vk.ru", D.TypeA, context.DeadlineExceeded)
	smartDNSWait(t, m, "recovered")
	for range 30 {
		m.observe(w, "other.com", D.TypeA, context.DeadlineExceeded)
	}
	if calls.Load() != 1 || m.status().DiagnosticCount != 1 {
		t.Fatal("failure burst bypassed cooldown")
	}
}

func TestSmartDNSProtectsPrivateNegativeAndFilterPolicies(t *testing.T) {
	m, w := smartDNSManagerForTest(t)
	for _, host := range []string{"router", "printer.local", "account.corp", "1.2.3.4", "local.home.arpa"} {
		smartDNSTrigger(m, w, host)
	}
	for _, err := range []error{resolver.ErrIPNotFound, context.Canceled, errors.New("server failure: SERVFAIL"), errors.New("NXDOMAIN"), errors.New("x509: invalid certificate")} {
		m.observe(w, "m.vk.ru", D.TypeA, err)
	}
	if m.status().DiagnosticCount != 0 {
		t.Fatal("private/negative/filter failure triggered recovery")
	}
	protected := m.wrap("node", w.base, []dns.NameServer{{Net: "https", Addr: "https://dns.quad9.net/dns-query"}}, nil)
	smartDNSTrigger(m, protected, "blocked.com")
	if m.status().Reason != "policy-preserved" {
		t.Fatal("filtering resolver was bypassed")
	}
	policy := m.wrap("destination", w.base, w.servers, []dns.Policy{{Domain: "+.vk.ru"}})
	smartDNSTrigger(m, policy, "m.vk.ru")
	if m.status().DiagnosticCount != 0 {
		t.Fatal("explicit domain policy was bypassed")
	}
}

func TestSmartDNSDirectFollowPolicyPreserved(t *testing.T) {
	oldMain, oldDirect, oldNode, oldService, oldMapper := resolver.DefaultResolver, resolver.DirectHostResolver, resolver.ProxyServerHostResolver, resolver.DefaultService, resolver.DefaultHostMapper
	t.Cleanup(func() {
		resolver.DefaultResolver, resolver.DirectHostResolver, resolver.ProxyServerHostResolver, resolver.DefaultService, resolver.DefaultHostMapper = oldMain, oldDirect, oldNode, oldService, oldMapper
	})
	for _, follow := range []bool{false, true} {
		main, direct, node := &smartDNSFakeResolver{}, &smartDNSFakeResolver{}, &smartDNSFakeResolver{}
		resolver.DefaultResolver, resolver.DirectHostResolver, resolver.ProxyServerHostResolver = main, direct, node
		resolver.DefaultHostMapper = nil
		cfg, _ := config.ParseRawConfig(config.DefaultRawConfig())
		cfg.DNS.Enable = true
		cfg.DNS.DirectNameServer = []dns.NameServer{{Net: "udp", Addr: "8.8.8.8:53"}}
		cfg.DNS.NameServerPolicy = []dns.Policy{{Domain: "+.vk.ru"}}
		cfg.DNS.DirectFollowPolicy = follow
		m := &smartDNSManager{}
		m.install(cfg, true)
		wrapped, ok := resolver.DirectHostResolver.(*smartDNSResolver)
		if !ok || wrapped.base != direct {
			t.Fatal("lost effective direct resolver")
		}
		if wrapped.compatible("m.vk.ru") == follow || cfg.DNS.DirectFollowPolicy != follow {
			t.Fatalf("policy inheritance changed, follow=%v", follow)
		}
		m.invalidate("stopped")
	}
}

func TestSmartDNSDoHPinsBootstrapAndValidatesTLS(t *testing.T) {
	for _, provider := range smartDNSProviderPool("direct") {
		client, err := newSmartDNSDoH(provider, "", nil, "wifi")
		if err != nil {
			t.Fatal(err)
		}
		transport := client.client.Transport.(*http.Transport)
		if transport.TLSClientConfig.InsecureSkipVerify || transport.TLSClientConfig.ServerName == "" || transport.Proxy != nil {
			t.Fatal("unsafe TLS/bootstrap transport")
		}
		client.close()
	}
	b := &smartDNSBootstrap{host: "dns.google", ips: []netip.Addr{netip.MustParseAddr("8.8.8.8")}}
	if _, err := b.LookupIP(context.Background(), "other.com"); err == nil {
		t.Fatal("bootstrap resolved arbitrary hostname")
	}
}

func TestSmartDNSRejectsMalformedDoHAndCachesRealReplies(t *testing.T) {
	provider := smartDNSProviders[0]
	var calls atomic.Int64
	client := smartDNSFakeClient(provider, func(q *D.Msg) *D.Msg { calls.Add(1); return smartDNSFakeAnswer(q) })
	q := smartDNSQuestion("example.com", D.TypeA)
	if _, err := client.ExchangeContext(context.Background(), q); err != nil {
		t.Fatal(err)
	}
	q.Id++
	answer, cached, err := client.exchange(context.Background(), q)
	if err != nil || !cached || answer.Id != q.Id || calls.Load() != 1 {
		t.Fatal("DNS cache not reused with current request id")
	}
	bad := smartDNSFakeClient(provider, func(q *D.Msg) *D.Msg {
		answer := smartDNSFakeAnswer(q)
		answer.Question[0].Name = "different.com."
		return answer
	})
	if _, err := bad.ExchangeContext(context.Background(), q); err == nil {
		t.Fatal("mismatched response accepted")
	}
}

type smartDNSLookupResolver struct {
	smartDNSFakeResolver
	methods []string
}

func (r *smartDNSLookupResolver) LookupIP(context.Context, string) ([]netip.Addr, error) {
	r.methods = append(r.methods, "ip")
	return []netip.Addr{netip.MustParseAddr("192.0.2.44")}, nil
}
func (r *smartDNSLookupResolver) LookupIPv4(context.Context, string) ([]netip.Addr, error) {
	r.methods = append(r.methods, "v4")
	return []netip.Addr{netip.MustParseAddr("192.0.2.45")}, nil
}
func (r *smartDNSLookupResolver) LookupIPv6(context.Context, string) ([]netip.Addr, error) {
	r.methods = append(r.methods, "v6")
	return []netip.Addr{netip.MustParseAddr("2001:db8::1")}, nil
}
func TestSmartDNSPreservesOriginalLookupSemanticsAndIPv6Disable(t *testing.T) {
	m, _ := smartDNSManagerForTest(t)
	base := &smartDNSLookupResolver{}
	w := m.wrap("direct", base, []dns.NameServer{{Net: "udp", Addr: "8.8.8.8:53"}}, nil)
	ips, err := w.LookupIP(context.Background(), "m.vk.ru")
	if err != nil || ips[0].String() != "192.0.2.44" {
		t.Fatal("base LookupIP semantics changed")
	}
	_, _ = w.LookupIPv4(context.Background(), "m.vk.ru")
	_, _ = w.LookupIPv6(context.Background(), "m.vk.ru")
	if strings.Join(base.methods, ",") != "ip,v4,v6" || base.calls.Load() != 0 {
		t.Fatal("base lookup did not delegate unchanged")
	}
	var aaaa atomic.Int64
	client := smartDNSFakeClient(smartDNSProviders[0], func(q *D.Msg) *D.Msg {
		if q.Question[0].Qtype == D.TypeAAAA {
			aaaa.Add(1)
		}
		return smartDNSFakeAnswer(q)
	})
	w.overlays["m.vk.ru"] = smartDNSOverlay{client: client, until: time.Now().Add(time.Minute)}
	w.ipv6 = false
	w.ipv6Timeout = 600 * time.Millisecond
	ips, err = w.LookupIP(context.Background(), "m.vk.ru")
	if err != nil || len(ips) != 1 || !ips[0].Is4() {
		t.Fatal("overlay did not preserve disabled IPv6")
	}
	if _, err = w.LookupIPv6(context.Background(), "m.vk.ru"); !errors.Is(err, resolver.ErrIPv6Disabled) {
		t.Fatal("overlay enabled IPv6")
	}
	if aaaa.Load() != 0 {
		t.Fatal("overlay queried disabled IPv6")
	}
}

func TestSmartDNSPendingInternalFailureDoesNotCreateUserActivity(t *testing.T) {
	m, w := smartDNSManagerForTest(t)
	oldActivity := time.Now().Add(-time.Hour).UnixNano()
	smartLastActivity.Store(oldActivity)
	if !smartBeginDiagnostic("transport") {
		t.Fatal("shared gate unavailable")
	}
	ctx := context.WithValue(context.Background(), smartDiagnosticKey{}, true)
	_, _ = w.LookupIP(ctx, "m.vk.ru")
	m.mu.Lock()
	f := m.failures["direct:m.vk.ru"]
	f.last = time.Now().Add(-2 * time.Second)
	m.failures["direct:m.vk.ru"] = f
	m.mu.Unlock()
	_, _ = w.LookupIP(ctx, "m.vk.ru")
	if smartLastActivity.Load() != oldActivity {
		t.Fatal("internal health failure counted as user traffic")
	}
	m.mu.Lock()
	pending := m.pending != nil
	m.mu.Unlock()
	if !pending {
		t.Fatal("transport-owned failure was discarded")
	}
	smartEndDiagnostic("transport")
	m.retryPending()
	smartDNSWait(t, m, "recovered")
}

func TestSmartDNSAlternativesFailAndSettingsDefault(t *testing.T) {
	m, w := smartDNSManagerForTest(t)
	m.makeClient = func(p smartDNSProvider, _ string, _ C.ProxyAdapter, _ string) (*smartDNSDoH, error) {
		return smartDNSFakeClient(p, func(q *D.Msg) *D.Msg { a := &D.Msg{}; a.SetRcode(q, D.RcodeNameError); return a }), nil
	}
	smartDNSTrigger(m, w, "m.vk.ru")
	smartDNSWait(t, m, "unavailable")
	if len(w.overlays) != 0 || m.status().Reason != "alternatives-unavailable" {
		t.Fatal("failed alternatives changed active resolver")
	}
	settings, err := decodeSmartSettings([]byte("smart-reconnect-settings:\n  enabled: false\n"))
	if err != nil || settings.Enabled || !settings.DNSRecoveryEnabled {
		t.Fatal("DNS default depends on Smart automatic server mode")
	}
	settings, err = decodeSmartSettings([]byte("smart-reconnect-settings:\n  dns-recovery-enabled: false\n"))
	if err != nil || settings.DNSRecoveryEnabled {
		t.Fatal("DNS opt-out ignored")
	}
}

func TestSmartDNSRejectsMixedPrivateAlternative(t *testing.T) {
	answer := smartDNSFakeAnswer(smartDNSQuestion("example.com", D.TypeA))
	answer.Answer = append(answer.Answer, &D.A{Hdr: D.RR_Header{Name: "example.com.", Rrtype: D.TypeA, Class: D.ClassINET, Ttl: 60}, A: net.ParseIP("10.0.0.1")})
	if smartDNSUseful(answer, D.TypeA) {
		t.Fatal("mixed private answer admitted as public recovery")
	}
}

func TestSmartDNSYandexRestrictedToDirectFallback(t *testing.T) {
	for _, scope := range []string{"destination", "node", "direct"} {
		providers := smartDNSProviderPool(scope)
		found := false
		for i, p := range providers {
			if p.name == "Yandex" {
				found = true
				if i != len(providers)-1 || p.endpoint != "https://common.dot.dns.yandex.net/dns-query" {
					t.Fatal("Yandex is not an encrypted final fallback")
				}
			}
		}
		if found != (scope == "direct") {
			t.Fatalf("Yandex candidate scope=%s found=%v", scope, found)
		}
	}
}

type smartDNSNegativeResolver struct{ smartDNSFakeResolver }

func (*smartDNSNegativeResolver) ExchangeContext(_ context.Context, q *D.Msg) (*D.Msg, error) {
	answer := &D.Msg{}
	answer.SetRcode(q, D.RcodeNameError)
	return answer, nil
}
func TestSmartDNSNewNegativeAnswerCancelsFailover(t *testing.T) {
	m, w := smartDNSManagerForTest(t)
	w.base = &smartDNSNegativeResolver{}
	var calls atomic.Int64
	m.makeClient = func(p smartDNSProvider, _ string, _ C.ProxyAdapter, _ string) (*smartDNSDoH, error) {
		calls.Add(1)
		return smartDNSFakeClient(p, smartDNSFakeAnswer), nil
	}
	smartDNSTrigger(m, w, "m.vk.ru")
	smartDNSWait(t, m, "unavailable")
	if m.status().Reason != "policy-preserved" || calls.Load() != 0 || len(w.overlays) != 0 {
		t.Fatal("new valid negative answer bypassed during recovery")
	}
}

type smartDNSSelectiveResolver struct {
	smartDNSFakeResolver
	failed string
}

func (r *smartDNSSelectiveResolver) ExchangeContext(_ context.Context, q *D.Msg) (*D.Msg, error) {
	if strings.TrimSuffix(q.Question[0].Name, ".") == r.failed {
		return nil, context.DeadlineExceeded
	}
	answer := smartDNSFakeAnswer(q)
	if a, ok := answer.Answer[0].(*D.A); ok {
		a.A = net.ParseIP("9.9.9.10")
	}
	return answer, nil
}
func (r *smartDNSSelectiveResolver) LookupIPv4(_ context.Context, host string) ([]netip.Addr, error) {
	if host == r.failed {
		return nil, context.DeadlineExceeded
	}
	return []netip.Addr{netip.MustParseAddr("9.9.9.10")}, nil
}
func (r *smartDNSSelectiveResolver) LookupIP(ctx context.Context, host string) ([]netip.Addr, error) {
	return r.LookupIPv4(ctx, host)
}

func TestSmartDNSBranchRecoverySharesCompatibleRolesPreservesExceptions(t *testing.T) {
	m, w := smartDNSManagerForTest(t)
	w = m.wrap("direct", w.base, w.servers, []dns.Policy{{Domain: "+.policy.example.net"}})
	other := m.wrap("destination", w.base, w.servers, []dns.Policy{{Domain: "+.other-policy.example.net"}})
	smartDNSTrigger(m, w, "m.vk.ru")
	smartDNSWait(t, m, "recovered")
	for _, check := range []struct {
		wrapper *smartDNSResolver
		host    string
	}{{w, "m.vk.ru"}, {w, "cdn.vk.ru"}, {w, "unrelated.com"}, {other, "next-site.net"}} {
		ips, err := check.wrapper.LookupIPv4(context.Background(), check.host)
		if err != nil || len(ips) != 1 {
			t.Fatalf("branch recovery missing for %s: %v", check.host, err)
		}
	}
	for _, check := range []struct {
		wrapper *smartDNSResolver
		host    string
	}{{w, "private.local"}, {w, "account.policy.example.net"}, {other, "account.other-policy.example.net"}} {
		if _, err := check.wrapper.LookupIPv4(context.Background(), check.host); err == nil {
			t.Fatalf("branch bypassed original exception %s", check.host)
		}
	}
	if m.status().DiagnosticCount != 1 {
		t.Fatal("one dead shared branch caused multiple episodes")
	}
}

func TestSmartDNSIsolatedNameFailureKeepsExactOverlay(t *testing.T) {
	m, w := smartDNSManagerForTest(t)
	w.base = &smartDNSSelectiveResolver{failed: "m.vk.ru"}
	smartDNSTrigger(m, w, "m.vk.ru")
	smartDNSWait(t, m, "recovered")
	ips, err := w.LookupIPv4(context.Background(), "m.vk.ru")
	if err != nil || ips[0].String() != "8.8.8.8" {
		t.Fatal("isolated name was not recovered")
	}
	ips, err = w.LookupIPv4(context.Background(), "unrelated.com")
	if err != nil || ips[0].String() != "9.9.9.10" {
		t.Fatal("healthy branch was globally replaced")
	}
	if _, ok := w.overlays[smartDNSScopeOverlay]; ok {
		t.Fatal("isolated name installed branch overlay")
	}
}

func TestSmartDNSDistinctRoleCanRecoverWithoutGlobalCooldown(t *testing.T) {
	m, w := smartDNSManagerForTest(t)
	node := m.wrap("node", &smartDNSFakeResolver{err: context.DeadlineExceeded}, w.servers, nil)
	smartDNSTrigger(m, w, "m.vk.ru")
	smartDNSWait(t, m, "recovered")
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		m.mu.Lock()
		active := m.active
		m.mu.Unlock()
		if !active {
			break
		}
		time.Sleep(time.Millisecond)
	}
	smartDNSTrigger(m, node, "vpn.example.net")
	deadline = time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		state := m.status()
		if state.Phase == "recovered" && state.Scope == "node" && state.DiagnosticCount == 2 {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("independent role blocked by unrelated cooldown: %+v", m.status())
}

func TestSmartDNSSharedOriginalResolverIdentity(t *testing.T) {
	original := &dns.Resolver{}
	a := &smartDNSResolver{base: dns.Resolvers{Resolver: original}}
	b := &smartDNSResolver{base: original}
	if !smartDNSSharedBranch(a, b) {
		t.Fatal("main/direct embedding hid same original resolver")
	}
	b.route = "RULES"
	if smartDNSSharedBranch(a, b) {
		t.Fatal("different DNS transports were shared")
	}
}

func TestSmartDNSYandexBranchDoesNotLeakIntoOtherRoles(t *testing.T) {
	m, w := smartDNSManagerForTest(t)
	other := m.wrap("node", w.base, w.servers, nil)
	m.makeClient = func(p smartDNSProvider, _ string, _ C.ProxyAdapter, _ string) (*smartDNSDoH, error) {
		if p.name == "Yandex" {
			return smartDNSFakeClient(p, smartDNSFakeAnswer), nil
		}
		return nil, errors.New("unavailable")
	}
	smartDNSTrigger(m, w, "m.vk.ru")
	smartDNSWait(t, m, "recovered")
	if m.status().Provider != "Yandex" || !w.hasOverlay("next-domain.com") {
		t.Fatal("direct Yandex branch did not recover")
	}
	if other.hasOverlay("next-domain.com") {
		t.Fatal("Yandex direct fallback reached a node resolver")
	}
}

func TestSmartDNSProtectsPrivateHTTPSUpstreamHosts(t *testing.T) {
	for _, test := range []struct {
		addr      string
		protected bool
	}{
		{"https://10.0.0.1:443/dns-query", true},
		{"https://192.168.1.1/dns-query", true},
		{"https://127.0.0.1:443/dns-query", true},
		{"https://[fd00::1]:443/dns-query", true},
		{"https://[fe80::1]:443/dns-query", true},
		{"https://[::1]:443/dns-query", true},
		{"https://resolver.local:443/dns-query", true},
		{"https://dns.google:443/dns-query", false},
		{"https://8.8.8.8:443/dns-query", false},
		{"https://[2001:4860:4860::8888]:443/dns-query", false},
	} {
		t.Run(test.addr, func(t *testing.T) {
			server := dns.NameServer{Net: "https", Addr: test.addr}
			if actual := smartDNSProtectedServer(server); actual != test.protected {
				t.Fatalf("protected=%v, want %v", actual, test.protected)
			}
			if !test.protected {
				return
			}
			m, w := smartDNSManagerForTest(t)
			w = m.wrap("direct", w.base, []dns.NameServer{server}, nil)
			smartDNSTrigger(m, w, "m.vk.ru")
			if m.status().Reason != "policy-preserved" || m.status().DiagnosticCount != 0 || len(w.overlays) != 0 {
				t.Fatal("private HTTPS resolver permitted public recovery")
			}
		})
	}
}
