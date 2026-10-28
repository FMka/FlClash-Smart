package core

import (
	"context"
	"errors"
	"net/netip"
	"strings"
	"time"

	"github.com/metacubex/mihomo/component/resolver"
	"github.com/metacubex/mihomo/component/trie"
	C "github.com/metacubex/mihomo/constant"
	"github.com/metacubex/mihomo/dns"
	D "github.com/miekg/dns"
)

type smartDNSDiagnosticContextKey struct{}
type smartDNSResolver struct {
	manager       *smartDNSManager
	base          resolver.Resolver
	scope         string
	servers       []dns.NameServer
	policies      []dns.Policy
	policyDomains *trie.DomainTrie[bool]
	protected     bool
	route         string
	adapter       C.ProxyAdapter
	overlays      map[string]smartDNSOverlay
	ipv6          bool
	ipv6Timeout   time.Duration
}

func (w *smartDNSResolver) ExchangeContext(ctx context.Context, query *D.Msg) (*D.Msg, error) {
	_, diagnostic := ctx.Value(smartDNSDiagnosticContextKey{}).(bool)
	return w.exchange(ctx, query, !diagnostic)
}
func (w *smartDNSResolver) exchange(ctx context.Context, query *D.Msg, observe bool) (*D.Msg, error) {
	if len(query.Question) != 1 {
		return w.base.ExchangeContext(ctx, query)
	}
	question := query.Question[0]
	host := strings.ToLower(strings.TrimSuffix(question.Name, "."))
	start := time.Now()
	m := w.manager
	alternate := w.overlay(host)
	var answer *D.Msg
	var err error
	if alternate != nil {
		var cached bool
		answer, cached, err = alternate.exchange(ctx, query)
		if observe && dns.DefaultQueryNotify != nil {
			dns.DefaultQueryNotify(dns.QueryRecord{Question: question, Msg: answer, Initiator: w.initiator(ctx), Upstream: alternate.provider.endpoint, Cached: cached, Start: start, Err: err})
		}
	} else {
		answer, err = w.base.ExchangeContext(ctx, query)
	}
	if observe {
		m.observeQuery(w, host, question.Qtype, err, ctx.Value(smartDiagnosticKey{}) == nil)
	}
	return answer, err
}
func (w *smartDNSResolver) initiator(ctx context.Context) string {
	if initiator := resolver.InitiatorFrom(ctx); initiator != "" {
		return initiator
	}
	if w.scope == "direct" {
		return resolver.InitiatorDirect
	}
	if w.scope == "node" {
		return resolver.InitiatorProxy
	}
	return resolver.InitiatorApp
}
func (w *smartDNSResolver) lookup(ctx context.Context, host string, qtype uint16) ([]netip.Addr, error) {
	if ip, err := netip.ParseAddr(host); err == nil {
		if (qtype == D.TypeA && ip.Unmap().Is4()) || (qtype == D.TypeAAAA && ip.Is6()) {
			return []netip.Addr{ip}, nil
		}
		return nil, resolver.ErrIPVersion
	}
	answer, err := w.ExchangeContext(ctx, smartDNSQuestion(host, qtype))
	if err != nil {
		return nil, err
	}
	if answer == nil || answer.Rcode != D.RcodeSuccess {
		return nil, resolver.ErrIPNotFound
	}
	result := []netip.Addr{}
	for _, rr := range answer.Answer {
		switch rr := rr.(type) {
		case *D.A:
			if qtype == D.TypeA {
				if ip, ok := netip.AddrFromSlice(rr.A); ok {
					result = append(result, ip.Unmap())
				}
			}
		case *D.AAAA:
			if qtype == D.TypeAAAA {
				if ip, ok := netip.AddrFromSlice(rr.AAAA); ok {
					result = append(result, ip)
				}
			}
		}
	}
	if len(result) == 0 {
		return nil, resolver.ErrIPNotFound
	}
	return result, nil
}
func (w *smartDNSResolver) LookupIPv4(ctx context.Context, host string) ([]netip.Addr, error) {
	if !w.hasOverlay(host) {
		start := time.Now()
		ips, err := w.base.LookupIPv4(ctx, host)
		w.observeLookup(ctx, host, D.TypeA, start, err)
		return ips, err
	}
	return w.lookup(ctx, host, D.TypeA)
}
func (w *smartDNSResolver) LookupIPv6(ctx context.Context, host string) ([]netip.Addr, error) {
	if !w.hasOverlay(host) {
		start := time.Now()
		ips, err := w.base.LookupIPv6(ctx, host)
		w.observeLookup(ctx, host, D.TypeAAAA, start, err)
		return ips, err
	}
	if !w.ipv6 {
		return nil, resolver.ErrIPv6Disabled
	}
	return w.lookup(ctx, host, D.TypeAAAA)
}
func (w *smartDNSResolver) LookupIP(ctx context.Context, host string) ([]netip.Addr, error) {
	if !w.hasOverlay(host) {
		start := time.Now()
		ips, err := w.base.LookupIP(ctx, host)
		w.observeLookup(ctx, host, D.TypeA, start, err)
		return ips, err
	}
	if !w.ipv6 {
		return w.LookupIPv4(ctx, host)
	}
	type result struct {
		ips []netip.Addr
		err error
	}
	ch := make(chan result, 1)
	safeGo("smartDNSLookup", func() { ips, err := w.LookupIPv6(ctx, host); ch <- result{ips, err} })
	ips, err := w.LookupIPv4(ctx, host)
	timeout := w.ipv6Timeout
	if timeout <= 0 {
		timeout = 100 * time.Millisecond
	}
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case other := <-ch:
		ips = append(ips, other.ips...)
		if len(ips) > 0 {
			return ips, nil
		}
		if err != nil {
			return nil, err
		}
		return nil, other.err
	case <-ctx.Done():
		if len(ips) > 0 {
			return ips, nil
		}
		return nil, ctx.Err()
	case <-timer.C:
		return ips, err
	}
}

func (w *smartDNSResolver) observeLookup(ctx context.Context, host string, qtype uint16, start time.Time, err error) {
	if ctx.Value(smartDNSDiagnosticContextKey{}) == nil {
		w.manager.observeLookup(ctx, w, host, qtype, start, err)
	}
}
func (w *smartDNSResolver) hasOverlay(host string) bool {
	return w.overlay(host) != nil
}
func (w *smartDNSResolver) overlay(host string) *smartDNSDoH {
	w.manager.mu.Lock()
	defer w.manager.mu.Unlock()
	host = strings.ToLower(strings.TrimSuffix(host, "."))
	if !w.manager.enabled || !isRunning.Load() || w.manager.wrappers[w.scope] != w || !smartDNSPublicName(host) || !w.compatible(host) {
		return nil
	}
	for _, key := range []string{smartDNSScopeOverlay, host} {
		overlay, ok := w.overlays[key]
		if !ok {
			continue
		}
		if time.Now().Before(overlay.until) {
			return overlay.client
		}
		delete(w.overlays, key)
		overlay.client.close()
		if w.manager.state.Scope == w.scope {
			w.manager.state.Source = "profile"
			w.manager.state.Provider = "profile"
			w.manager.state.Endpoint = ""
			w.manager.state.Phase = "observing"
			w.manager.state.Reason = "expired"
		}
	}
	return nil
}
func (w *smartDNSResolver) ResolveECH(ctx context.Context, host string) ([]byte, error) {
	if !w.hasOverlay(host) {
		return w.base.ResolveECH(ctx, host)
	}
	answer, err := w.ExchangeContext(ctx, smartDNSQuestion(host, D.TypeHTTPS))
	if err != nil {
		return nil, err
	}
	if answer != nil {
		for _, rr := range answer.Answer {
			if https, ok := rr.(*D.HTTPS); ok {
				for _, value := range https.Value {
					if ech, ok := value.(*D.SVCBECHConfig); ok {
						return ech.ECH, nil
					}
				}
			}
		}
	}
	return nil, errors.New("no ECH config found in DNS records")
}
func (w *smartDNSResolver) Invalid() bool { return w.base != nil && w.base.Invalid() }
func (w *smartDNSResolver) ClearCache()   { w.base.ClearCache() }
func (w *smartDNSResolver) ResetConnection() {
	w.base.ResetConnection()
	w.manager.mu.Lock()
	defer w.manager.mu.Unlock()
	for _, overlay := range w.overlays {
		overlay.client.close()
	}
}
