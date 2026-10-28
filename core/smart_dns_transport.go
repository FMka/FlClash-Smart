package core

import (
	"bytes"
	"context"
	"errors"
	"io"
	"mime"
	"net/netip"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/metacubex/http"
	"github.com/metacubex/mihomo/component/ca"
	"github.com/metacubex/mihomo/component/resolver"
	C "github.com/metacubex/mihomo/constant"
	"github.com/metacubex/mihomo/tunnel"
	"github.com/metacubex/tls"
	D "github.com/miekg/dns"
)

type smartDNSProvider struct {
	name, endpoint string
	ips            []string
}

var smartDNSProviders = []smartDNSProvider{
	{"Google", "https://dns.google/dns-query", []string{"8.8.8.8", "8.8.4.4"}},
	{"Cloudflare", "https://cloudflare-dns.com/dns-query", []string{"1.1.1.1", "1.0.0.1"}},
	{"Quad9", "https://dns10.quad9.net/dns-query", []string{"9.9.9.10", "149.112.112.10"}},
	{"AdGuard", "https://unfiltered.adguard-dns.com/dns-query", []string{"94.140.14.140", "94.140.14.141"}},
}

var smartDNSYandex = smartDNSProvider{"Yandex", "https://common.dot.dns.yandex.net/dns-query", []string{"77.88.8.8", "77.88.8.1"}}

func smartDNSProviderPool(scope string) []smartDNSProvider {
	providers := append([]smartDNSProvider(nil), smartDNSProviders...)
	if scope == "direct" {
		providers = append(providers, smartDNSYandex)
	}
	return providers
}

type smartDNSDoH struct {
	provider smartDNSProvider
	client   *http.Client
	network  string
	cacheMu  sync.Mutex
	cache    map[string]smartDNSCachedAnswer
}
type smartDNSCachedAnswer struct {
	answer    *D.Msg
	at, until time.Time
}

func newSmartDNSDoH(provider smartDNSProvider, route string, adapter C.ProxyAdapter, network string) (*smartDNSDoH, error) {
	u, err := url.Parse(provider.endpoint)
	if err != nil || u.Scheme != "https" || u.Hostname() == "" || len(provider.ips) == 0 {
		return nil, errors.New("invalid encrypted DNS provider")
	}
	bootstrap := &smartDNSBootstrap{host: u.Hostname()}
	for _, value := range provider.ips {
		ip, err := netip.ParseAddr(value)
		if err != nil || !smartDNSPublicIP(ip) {
			return nil, errors.New("invalid DNS bootstrap address")
		}
		bootstrap.ips = append(bootstrap.ips, ip)
	}
	tlsConfig, err := ca.GetTLSConfig(ca.Option{TLSConfig: &tls.Config{MinVersion: tls.VersionTLS12, ServerName: u.Hostname()}})
	if err != nil {
		return nil, err
	}
	dialer := tunnel.NewDNSDialer(bootstrap, adapter, route)
	transport := &http.Transport{
		Proxy: nil, DialContext: dialer.DialContext, TLSClientConfig: tlsConfig,
		ForceAttemptHTTP2: true, DisableCompression: true, IdleConnTimeout: time.Minute,
		TLSHandshakeTimeout: 3 * time.Second, ResponseHeaderTimeout: 3 * time.Second,
		MaxIdleConnsPerHost: 1,
	}
	return &smartDNSDoH{provider: provider, network: network, client: &http.Client{
		Transport: transport, Timeout: 4 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error { return errors.New("DNS redirect refused") },
	}}, nil
}

func (d *smartDNSDoH) ExchangeContext(ctx context.Context, query *D.Msg) (*D.Msg, error) {
	answer, _, err := d.exchange(ctx, query)
	return answer, err
}

func (d *smartDNSDoH) exchange(ctx context.Context, query *D.Msg) (*D.Msg, bool, error) {
	_, diagnostic := ctx.Value(smartDNSDiagnosticContextKey{}).(bool)
	key := ""
	if len(query.Question) == 1 {
		key = query.Question[0].String()
	}
	if !diagnostic && key != "" {
		d.cacheMu.Lock()
		cached, ok := d.cache[key]
		d.cacheMu.Unlock()
		if ok && time.Now().Before(cached.until) {
			answer := cached.answer.Copy()
			answer.Id = query.Id
			elapsed := uint32(time.Since(cached.at).Seconds())
			for _, rr := range answer.Answer {
				if rr.Header().Ttl > elapsed {
					rr.Header().Ttl -= elapsed
				} else {
					rr.Header().Ttl = 0
				}
			}
			return answer, true, nil
		}
	}
	answer, err := d.exchangeNetwork(ctx, query)
	if err == nil && answer != nil && key != "" && answer.Rcode == D.RcodeSuccess && len(answer.Answer) > 0 {
		ttl := uint32(300)
		for _, rr := range answer.Answer {
			if rr.Header().Ttl < ttl {
				ttl = rr.Header().Ttl
			}
		}
		if ttl > 0 {
			d.cacheMu.Lock()
			if d.cache == nil || len(d.cache) >= 256 {
				d.cache = map[string]smartDNSCachedAnswer{}
			}
			d.cache[key] = smartDNSCachedAnswer{answer: answer.Copy(), at: time.Now(), until: time.Now().Add(time.Duration(ttl) * time.Second)}
			d.cacheMu.Unlock()
		}
	}
	return answer, false, err
}

func (d *smartDNSDoH) exchangeNetwork(ctx context.Context, query *D.Msg) (*D.Msg, error) {
	wire, err := query.Pack()
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, d.provider.endpoint, bytes.NewReader(wire))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/dns-message")
	req.Header.Set("Accept", "application/dns-message")
	response, err := d.client.Do(req)
	smartTraffic.add(d.network, int64(len(wire)), true)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	body, readErr := io.ReadAll(io.LimitReader(response.Body, 65537))
	smartTraffic.add(d.network, int64(len(body)), true)
	contentType, _, _ := mime.ParseMediaType(response.Header.Get("Content-Type"))
	if readErr != nil || response.StatusCode != http.StatusOK || contentType != "application/dns-message" || len(body) > 65535 {
		return nil, errors.New("invalid encrypted DNS response")
	}
	answer := &D.Msg{}
	if err = answer.Unpack(body); err != nil {
		return nil, err
	}
	if !answer.Response || len(answer.Question) != 1 || len(query.Question) != 1 || !strings.EqualFold(answer.Question[0].Name, query.Question[0].Name) || answer.Question[0].Qtype != query.Question[0].Qtype || answer.Id != query.Id {
		return nil, errors.New("mismatched encrypted DNS answer")
	}
	return answer, nil
}

func (d *smartDNSDoH) close() { d.client.CloseIdleConnections() }

type smartDNSBootstrap struct {
	host string
	ips  []netip.Addr
}

func (b *smartDNSBootstrap) LookupIP(_ context.Context, host string) ([]netip.Addr, error) {
	if !strings.EqualFold(strings.TrimSuffix(host, "."), b.host) {
		return nil, resolver.ErrIPNotFound
	}
	return append([]netip.Addr(nil), b.ips...), nil
}
func (b *smartDNSBootstrap) LookupIPv4(ctx context.Context, host string) ([]netip.Addr, error) {
	ips, err := b.LookupIP(ctx, host)
	result := []netip.Addr{}
	for _, ip := range ips {
		if ip.Is4() {
			result = append(result, ip)
		}
	}
	return result, err
}
func (b *smartDNSBootstrap) LookupIPv6(ctx context.Context, host string) ([]netip.Addr, error) {
	ips, err := b.LookupIP(ctx, host)
	result := []netip.Addr{}
	for _, ip := range ips {
		if ip.Is6() {
			result = append(result, ip)
		}
	}
	return result, err
}
func (*smartDNSBootstrap) ExchangeContext(context.Context, *D.Msg) (*D.Msg, error) {
	return nil, resolver.ErrIPNotFound
}
func (*smartDNSBootstrap) ResolveECH(context.Context, string) ([]byte, error) {
	return nil, resolver.ErrIPNotFound
}
func (*smartDNSBootstrap) Invalid() bool    { return true }
func (*smartDNSBootstrap) ClearCache()      {}
func (*smartDNSBootstrap) ResetConnection() {}

func smartDNSPublicIP(ip netip.Addr) bool {
	return ip.IsValid() && ip.IsGlobalUnicast() && !ip.IsPrivate() && !ip.IsLoopback() && !ip.IsLinkLocalUnicast() && !ip.IsUnspecified() && !ip.IsMulticast() && !netip.MustParsePrefix("100.64.0.0/10").Contains(ip) && !netip.MustParsePrefix("198.18.0.0/15").Contains(ip)
}
