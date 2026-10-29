package core

import (
	"context"
	"errors"
	"net"
	"net/netip"
	"net/url"
	"reflect"
	"strings"
	"sync"
	"time"

	"github.com/metacubex/mihomo/adapter/inbound"
	"github.com/metacubex/mihomo/component/resolver"
	"github.com/metacubex/mihomo/component/trie"
	"github.com/metacubex/mihomo/config"
	C "github.com/metacubex/mihomo/constant"
	"github.com/metacubex/mihomo/dns"
	D "github.com/miekg/dns"
)

type smartDNSStatus struct {
	Enabled         bool   `json:"enabled"`
	Phase           string `json:"phase"`
	Scope           string `json:"scope"`
	Source          string `json:"source"`
	Provider        string `json:"provider"`
	Endpoint        string `json:"endpoint"`
	Reason          string `json:"reason"`
	LastChecked     int64  `json:"last-checked"`
	DiagnosticCount int64  `json:"diagnostic-count"`
}

type smartDNSFailure struct {
	count       int
	first, last time.Time
}
type smartDNSOverlay struct {
	client *smartDNSDoH
	until  time.Time
}

const smartDNSScopeOverlay = "*"

type smartDNSRemembered struct {
	provider string
	at       time.Time
}
type smartDNSPending struct {
	wrapper *smartDNSResolver
	host    string
	qtype   uint16
	epoch   uint64
}
type smartDNSManager struct {
	mu             sync.Mutex
	enabled        bool
	epoch          uint64
	network        string
	cancel         context.CancelFunc
	active         bool
	nextByScope    map[string]time.Time
	windowStart    time.Time
	windowEpisodes int
	failures       map[string]smartDNSFailure
	wrappers       map[string]*smartDNSResolver
	remembered     map[string]smartDNSRemembered
	state          smartDNSStatus
	recentFailures map[string]time.Time
	pending        *smartDNSPending
	makeClient     func(smartDNSProvider, string, C.ProxyAdapter, string) (*smartDNSDoH, error)
}

var smartDNS = &smartDNSManager{makeClient: newSmartDNSDoH}
var smartDiagnosticGate struct {
	sync.Mutex
	owner string
}

func smartBeginDiagnostic(kind string) bool {
	smartDiagnosticGate.Lock()
	defer smartDiagnosticGate.Unlock()
	if smartDiagnosticGate.owner != "" {
		return false
	}
	smartDiagnosticGate.owner = kind
	return true
}
func smartEndDiagnostic(kind string) {
	smartDiagnosticGate.Lock()
	released := false
	if smartDiagnosticGate.owner == kind {
		smartDiagnosticGate.owner = ""
		released = true
	}
	smartDiagnosticGate.Unlock()
	if released && kind == "dns" {
		setSmartRoutingRunning(isRunning.Load())
	}
	if released && kind == "rules" {
		smartDNS.retryPending()
	}
}
func smartDNSDiagnosticActive() bool {
	smartDiagnosticGate.Lock()
	defer smartDiagnosticGate.Unlock()
	return smartDiagnosticGate.owner == "dns"
}

func (m *smartDNSManager) resume() {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, wrapper := range m.wrappers {
		for _, overlay := range wrapper.overlays {
			overlay.client.close()
			overlay.client.cacheMu.Lock()
			overlay.client.cache = nil
			overlay.client.cacheMu.Unlock()
		}
	}
}

func (m *smartDNSManager) status() smartDNSStatus {
	m.mu.Lock()
	defer m.mu.Unlock()
	state := m.state
	state.Enabled = m.enabled
	if !m.enabled {
		state.Phase = "disabled"
		state.Reason = "disabled"
	} else if !isRunning.Load() {
		state.Phase = "stopped"
		state.Reason = "stopped"
	}
	return state
}

func (m *smartDNSManager) invalidate(reason string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.invalidateLocked(reason)
}
func (m *smartDNSManager) invalidateLocked(reason string) {
	m.epoch++
	if m.cancel != nil {
		m.cancel()
		m.cancel = nil
	}
	m.failures = map[string]smartDNSFailure{}
	m.recentFailures = map[string]time.Time{}
	m.pending = nil
	m.nextByScope = map[string]time.Time{}
	m.windowStart = time.Time{}
	m.windowEpisodes = 0
	for _, wrapper := range m.wrappers {
		for _, overlay := range wrapper.overlays {
			overlay.client.close()
		}
		wrapper.overlays = map[string]smartDNSOverlay{}
	}
	m.state = smartDNSStatus{Enabled: m.enabled, Phase: "observing", Source: "profile", Provider: "profile", Reason: reason}
}
func (m *smartDNSManager) networkChanged(network string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.network = network
	m.invalidateLocked("network-changed")
}

func (m *smartDNSManager) install(cfg *config.Config, enabled bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.invalidateLocked("observing")
	m.enabled = enabled
	m.wrappers = map[string]*smartDNSResolver{}
	if m.remembered == nil {
		m.remembered = map[string]smartDNSRemembered{}
	}
	if cfg == nil || !cfg.DNS.Enable || resolver.DefaultResolver == nil {
		return
	}
	main := m.wrap("destination", resolver.DefaultResolver, cfg.DNS.NameServer, cfg.DNS.NameServerPolicy)
	main.protected = main.protected || len(cfg.DNS.Fallback) > 0
	directNS, directPolicy := cfg.DNS.DirectNameServer, []dns.Policy(nil)
	if len(directNS) == 0 {
		directNS = cfg.DNS.NameServer
		directPolicy = cfg.DNS.NameServerPolicy
	} else if cfg.DNS.DirectFollowPolicy {
		directPolicy = cfg.DNS.NameServerPolicy
	}
	direct := m.wrap("direct", resolver.DirectHostResolver, directNS, directPolicy)
	if len(cfg.DNS.DirectNameServer) == 0 {
		direct.protected = direct.protected || main.protected
	}
	nodeNS, nodePolicy := cfg.DNS.ProxyServerNameserver, cfg.DNS.ProxyServerPolicy
	if len(nodeNS) == 0 {
		nodeNS = cfg.DNS.NameServer
		nodePolicy = cfg.DNS.NameServerPolicy
	}
	node := m.wrap("node", resolver.ProxyServerHostResolver, nodeNS, nodePolicy)
	if len(cfg.DNS.ProxyServerNameserver) == 0 {
		node.protected = node.protected || main.protected
	}
	for _, wrapper := range []*smartDNSResolver{main, direct, node} {
		wrapper.ipv6 = cfg.DNS.IPv6 && cfg.General.IPv6
		wrapper.ipv6Timeout = time.Duration(cfg.DNS.IPv6Timeout) * time.Millisecond
		if wrapper.ipv6Timeout <= 0 {
			wrapper.ipv6Timeout = 100 * time.Millisecond
		}
	}
	resolver.DefaultResolver, resolver.DirectHostResolver, resolver.ProxyServerHostResolver = main, direct, node
	if enhancer, ok := resolver.DefaultHostMapper.(*dns.ResolverEnhancer); ok {
		service := dns.NewService(main, enhancer)
		resolver.DefaultService = service
		listen := inbound.NewListenConfig()
		listen.SetRouteMark(cfg.DNS.ListenRoutingMark)
		dns.ReCreateServer(cfg.DNS.Listen, listen, service)
	}
}

func (m *smartDNSManager) wrap(scope string, base resolver.Resolver, servers []dns.NameServer, policies []dns.Policy) *smartDNSResolver {
	w := &smartDNSResolver{manager: m, base: base, scope: scope, servers: servers, policies: policies, overlays: map[string]smartDNSOverlay{}}
	w.policyDomains = trie.New[bool]()
	for _, policy := range policies {
		if policy.Matcher == nil && policy.Domain != "" {
			if w.policyDomains.Insert(policy.Domain, true) != nil {
				w.protected = true
			}
		}
	}
	if len(servers) == 0 {
		w.protected = true
	}
	for i, server := range servers {
		if smartDNSProtectedServer(server) {
			w.protected = true
		}
		if i == 0 {
			w.route, w.adapter = server.ProxyName, server.ProxyAdapter
		} else if server.ProxyName != w.route || server.ProxyAdapter != nil {
			w.protected = true
		}
	}
	m.wrappers[scope] = w
	return w
}

func smartDNSProtectedServer(server dns.NameServer) bool {
	if server.Net == "system" || server.Net == "dhcp" || server.Net == "rcode" || server.Net == "tailscale" || server.Net == "easytier" || len(server.Params) > 0 {
		return true
	}
	value := strings.ToLower(server.Addr)
	for _, marker := range []string{"family", "security.cloudflare", "dns.quad9.net", "9.9.9.9", "149.112.112.112", "1.1.1.2", "1.1.1.3", "1.0.0.2", "1.0.0.3", "dns.adguard-dns.com", "cleanbrowsing", "nextdns", "opendns", "94.140.14.14:", "94.140.15.15"} {
		if strings.Contains(value, marker) {
			return true
		}
	}
	addr := value
	if endpoint, err := url.Parse(value); err == nil && endpoint.Hostname() != "" {
		addr = endpoint.Hostname()
	} else if host, _, err := net.SplitHostPort(value); err == nil {
		addr = host
	}
	if ip, err := netip.ParseAddr(addr); err == nil {
		return !smartDNSPublicIP(ip)
	}
	return !smartDNSPublicName(addr)
}

func smartDNSPublicName(host string) bool {
	host = strings.ToLower(strings.TrimSuffix(host, "."))
	if len(host) > 253 || !strings.Contains(host, ".") || strings.ContainsAny(host, " /\\:@") {
		return false
	}
	if _, err := netip.ParseAddr(host); err == nil {
		return false
	}
	for _, suffix := range []string{".local", ".localhost", ".lan", ".home", ".home.arpa", ".internal", ".corp", ".onion", ".arpa", ".invalid", ".test", ".example"} {
		if strings.HasSuffix(host, suffix) {
			return false
		}
	}
	if record, ok := resolver.DefaultHosts.Search(host, false); ok || record != nil {
		return false
	}
	return true
}

func smartDNSRecoverable(err error) bool {
	if err == nil || errors.Is(err, context.Canceled) || errors.Is(err, resolver.ErrIPNotFound) || errors.Is(err, resolver.ErrIPVersion) || errors.Is(err, resolver.ErrIPv6Disabled) {
		return false
	}
	message := strings.ToLower(err.Error())
	for _, prohibited := range []string{"nxdomain", "servfail", "refused", "dnssec", "certificate", "x509", "no such host"} {
		if strings.Contains(message, prohibited) {
			return false
		}
	}
	return errors.Is(err, context.DeadlineExceeded) || strings.Contains(message, "timeout") || strings.Contains(message, "deadline exceeded") || strings.Contains(message, "all dns requests failed")
}

func (m *smartDNSManager) observeRecord(record dns.QueryRecord) {
	if smartDNSRecoverable(record.Err) {
		m.mu.Lock()
		if m.recentFailures == nil || len(m.recentFailures) >= 256 {
			m.recentFailures = map[string]time.Time{}
		}
		m.recentFailures[strings.ToLower(strings.TrimSuffix(record.Question.Name, "."))] = time.Now()
		m.mu.Unlock()
	}
	if record.Err != nil || record.Cached || record.Upstream == "" {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if !m.enabled || m.active || m.state.Source == "recovery" {
		return
	}
	if u, err := url.Parse(record.Upstream); err == nil && u.Hostname() != "" {
		m.state.Endpoint = u.Scheme + "://" + u.Host
		m.state.Provider = u.Hostname()
	}
	for _, provider := range smartDNSProviderPool("direct") {
		if strings.Contains(record.Upstream, strings.TrimPrefix(provider.endpoint, "https://")) {
			m.state.Endpoint = provider.endpoint
			m.state.Provider = provider.name
			break
		}
	}
}

func (m *smartDNSManager) observe(w *smartDNSResolver, host string, qtype uint16, err error) {
	m.observeQuery(w, host, qtype, err, true)
}

func (m *smartDNSManager) observeLookup(ctx context.Context, w *smartDNSResolver, host string, qtype uint16, start time.Time, err error) {
	if errors.Is(err, resolver.ErrIPNotFound) {
		m.mu.Lock()
		failed := m.recentFailures[strings.ToLower(strings.TrimSuffix(host, "."))]
		m.mu.Unlock()
		if !failed.Before(start) {
			err = context.DeadlineExceeded
		}
	}
	m.observeQuery(w, host, qtype, err, ctx.Value(smartDiagnosticKey{}) == nil)
}

func (m *smartDNSManager) retryPending() {
	m.mu.Lock()
	pending := m.pending
	m.pending = nil
	if pending == nil || pending.epoch != m.epoch {
		m.mu.Unlock()
		return
	}
	m.mu.Unlock()
	m.observeQuery(pending.wrapper, pending.host, pending.qtype, context.DeadlineExceeded, false)
}

func (m *smartDNSManager) observeQuery(w *smartDNSResolver, host string, qtype uint16, err error, activity bool) {
	if !smartDNSRecoverable(err) || !smartDNSPublicName(host) || (qtype != D.TypeA && qtype != D.TypeAAAA) {
		return
	}
	if activity {
		smartNotifyActivity()
	}
	host = strings.ToLower(strings.TrimSuffix(host, "."))
	m.mu.Lock()
	if !m.enabled || !isRunning.Load() || m.network == "" || m.wrappers[w.scope] != w || m.active {
		m.mu.Unlock()
		return
	}
	if !w.compatible(host) {
		m.state.Phase = "unavailable"
		m.state.Reason = "policy-preserved"
		m.state.Scope = w.scope
		m.mu.Unlock()
		return
	}
	now := time.Now()
	key := w.scope + ":" + host
	failure := m.failures[key]
	if now.Sub(failure.first) > time.Minute {
		failure = smartDNSFailure{first: now}
	}
	if now.Sub(failure.last) > time.Second {
		failure.count++
		failure.last = now
	}
	if len(m.failures) >= 128 {
		m.failures = map[string]smartDNSFailure{}
	}
	m.failures[key] = failure
	if failure.count < 2 || now.Before(m.nextByScope[w.scope]) {
		m.mu.Unlock()
		return
	}
	if now.Sub(m.windowStart) >= 2*time.Minute {
		m.windowStart = now
		m.windowEpisodes = 0
	}
	if m.windowEpisodes >= 3 {
		m.mu.Unlock()
		return
	}
	if !smartBeginDiagnostic("dns") {
		m.pending = &smartDNSPending{wrapper: w, host: host, qtype: qtype, epoch: m.epoch}
		m.mu.Unlock()
		return
	}
	m.active = true
	ctx, cancel := context.WithTimeout(context.Background(), 24*time.Second)
	m.cancel = cancel
	epoch, network := m.epoch, m.network
	if m.nextByScope == nil {
		m.nextByScope = map[string]time.Time{}
	}
	m.nextByScope[w.scope] = now.Add(2 * time.Minute)
	m.windowEpisodes++
	m.state.Phase = "diagnosing"
	m.state.Reason = "checking-resolvers"
	m.state.Scope = w.scope
	m.state.LastChecked = now.UnixMilli()
	m.state.DiagnosticCount++
	m.mu.Unlock()
	safeGo("smartDNS", func() {
		defer func() { cancel(); smartEndDiagnostic("dns"); m.mu.Lock(); m.active = false; m.mu.Unlock() }()
		m.diagnose(ctx, w, host, qtype, epoch, network)
	})
}

func (w *smartDNSResolver) compatible(host string) bool {
	if w.protected || w.policyDomains.Search(host) != nil {
		return false
	}
	for _, policy := range w.policies {
		if policy.Matcher != nil && policy.Matcher.MatchDomain(host) {
			return false
		}
	}
	return true
}

type smartDNSExchange interface {
	ExchangeContext(context.Context, *D.Msg) (*D.Msg, error)
}

func smartDNSQuestion(host string, qtype uint16) *D.Msg {
	query := &D.Msg{}
	query.SetQuestion(D.Fqdn(host), qtype)
	query.SetEdns0(1232, true)
	return query
}
func smartDNSUseful(answer *D.Msg, qtype uint16) bool {
	if answer == nil || answer.Rcode != D.RcodeSuccess || answer.Truncated {
		return false
	}
	count := 0
	for _, rr := range answer.Answer {
		switch rr := rr.(type) {
		case *D.A:
			if qtype == D.TypeA {
				ip, ok := netip.AddrFromSlice(rr.A)
				if !ok || !smartDNSPublicIP(ip.Unmap()) {
					return false
				}
				count++
			}
		case *D.AAAA:
			if qtype == D.TypeAAAA {
				ip, ok := netip.AddrFromSlice(rr.AAAA)
				if !ok || !smartDNSPublicIP(ip) {
					return false
				}
				count++
			}
		}
	}
	return count > 0
}
func smartDNSProbe(ctx context.Context, client smartDNSExchange, host string, qtype uint16) bool {
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	answer, err := client.ExchangeContext(ctx, smartDNSQuestion(host, qtype))
	return err == nil && smartDNSUseful(answer, qtype)
}

func (m *smartDNSManager) diagnose(ctx context.Context, w *smartDNSResolver, host string, qtype uint16, epoch uint64, network string) {
	ctx = context.WithValue(ctx, smartDNSDiagnosticContextKey{}, true)
	originalCtx, originalCancel := context.WithTimeout(ctx, 3*time.Second)
	original, originalErr := w.base.ExchangeContext(originalCtx, smartDNSQuestion(host, qtype))
	originalCancel()
	if originalErr == nil {
		if smartDNSUseful(original, qtype) {
			m.finish(epoch, "observing", "original-restored", nil)
		} else {
			m.finish(epoch, "unavailable", "policy-preserved", nil)
		}
		return
	}
	if original != nil && original.Rcode != D.RcodeSuccess || !smartDNSRecoverable(originalErr) {
		m.finish(epoch, "unavailable", "policy-preserved", nil)
		return
	}
	controls := make(chan bool, 2)
	for _, control := range []string{"example.com", "www.iana.org"} {
		control := control
		safeGo("smartDNSControl", func() {
			if !w.compatible(control) {
				controls <- false
				return
			}
			controlCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
			defer cancel()
			_, err := w.base.ExchangeContext(controlCtx, smartDNSQuestion(control, D.TypeA))
			controls <- smartDNSRecoverable(err)
		})
	}
	wholeScope := false
	for range 2 {
		if <-controls {
			wholeScope = true
		}
	}
	providers := smartDNSProviderPool(w.scope)
	m.mu.Lock()
	remembered := m.remembered[network+":"+w.scope]
	m.mu.Unlock()
	if time.Since(remembered.at) < 30*time.Minute {
		for i, p := range providers {
			if p.name == remembered.provider && p.name != "Yandex" {
				providers[0], providers[i] = providers[i], providers[0]
				break
			}
		}
	}
	for _, provider := range providers {
		if ctx.Err() != nil {
			break
		}
		client, err := m.makeClient(provider, w.route, w.adapter, network)
		if err != nil {
			continue
		}
		results := make(chan bool, 3)
		for _, probe := range []struct {
			host string
			kind uint16
		}{{host, qtype}, {"example.com", D.TypeA}, {"www.iana.org", D.TypeA}} {
			probe := probe
			safeGo("smartDNSProbe", func() { results <- smartDNSProbe(ctx, client, probe.host, probe.kind) })
		}
		valid := true
		for range 3 {
			if !<-results {
				valid = false
			}
		}
		if !valid {
			client.close()
			continue
		}
		m.mu.Lock()
		if ctx.Err() != nil || epoch != m.epoch || !isRunning.Load() || !m.enabled || m.wrappers[w.scope] != w {
			m.mu.Unlock()
			client.close()
			return
		}
		key := host
		affected := []*smartDNSResolver{w}
		if wholeScope {
			key = smartDNSScopeOverlay
			for _, other := range m.wrappers {
				if other != w && smartDNSSharedBranch(w, other) && !(provider.name == "Yandex" && other.scope != "direct") {
					affected = append(affected, other)
				}
			}
		}
		previous := map[*smartDNSResolver]smartDNSOverlay{}
		for _, wrapper := range affected {
			if old, ok := wrapper.overlays[key]; ok {
				previous[wrapper] = old
			}
			if len(wrapper.overlays) >= 128 {
				for oldKey, old := range wrapper.overlays {
					if oldKey != key {
						old.client.close()
						delete(wrapper.overlays, oldKey)
						break
					}
				}
			}
			wrapper.overlays[key] = smartDNSOverlay{client: client, until: time.Now().Add(30 * time.Minute)}
		}
		m.mu.Unlock()
		answer, err := w.exchange(ctx, smartDNSQuestion(host, qtype), false)
		if err != nil || !smartDNSUseful(answer, qtype) {
			m.mu.Lock()
			if epoch == m.epoch {
				for _, wrapper := range affected {
					if old, ok := previous[wrapper]; ok {
						wrapper.overlays[key] = old
					} else {
						delete(wrapper.overlays, key)
					}
				}
			}
			m.mu.Unlock()
			client.close()
			continue
		}
		for _, old := range previous {
			old.client.close()
		}
		m.mu.Lock()
		if epoch != m.epoch || ctx.Err() != nil || !isRunning.Load() {
			m.mu.Unlock()
			return
		}
		if len(m.remembered) >= 64 {
			m.remembered = map[string]smartDNSRemembered{}
		}
		for _, wrapper := range affected {
			m.remembered[network+":"+wrapper.scope] = smartDNSRemembered{provider: provider.name, at: time.Now()}
		}
		m.mu.Unlock()
		m.finish(epoch, "recovered", "recovered", &provider)
		appendSmartJournal("dns-recovered", w.scope+": "+provider.name)
		return
	}
	m.finish(epoch, "unavailable", "alternatives-unavailable", nil)
}

func smartDNSSharedBranch(a, b *smartDNSResolver) bool {
	if a.protected || b.protected || a.route != b.route || !smartDNSComparable(a.adapter, b.adapter) {
		return false
	}
	normalize := func(r resolver.Resolver) any {
		switch r := r.(type) {
		case dns.Resolvers:
			return r.Resolver
		case *dns.Resolvers:
			return r.Resolver
		default:
			return r
		}
	}
	return smartDNSComparable(normalize(a.base), normalize(b.base))
}
func smartDNSComparable(a, b any) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	t := reflect.TypeOf(a)
	return t == reflect.TypeOf(b) && t.Comparable() && a == b
}
func (m *smartDNSManager) finish(epoch uint64, phase, reason string, provider *smartDNSProvider) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if epoch != m.epoch {
		return
	}
	m.state.Phase = phase
	m.state.Reason = reason
	m.state.LastChecked = time.Now().UnixMilli()
	if provider != nil {
		m.state.Source = "recovery"
		m.state.Provider = provider.name
		m.state.Endpoint = provider.endpoint
	}
}
