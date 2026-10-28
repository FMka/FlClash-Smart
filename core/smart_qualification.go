package core

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/metacubex/mihomo/constant"
	P "github.com/metacubex/mihomo/constant/provider"
	"github.com/metacubex/mihomo/tunnel"
	"golang.org/x/text/language"
)

var russianSmartToken = regexp.MustCompile(`(?i)(^|[^\pL\pN])(?:ru|russia(?:n(?: federation)?)?|россия)([^\pL\pN]|$)`)
var smartCountrySources = []string{"https://www.cloudflare.com/cdn-cgi/trace", "https://api.ip.sb/geoip"}
var smartHealthCheckSlots = make(slots, 4)
var smartHealthProbeSlots = make(slots, 2)

type smartHealthKey struct{}

func russianSmartName(name string) bool {
	name = strings.ToLower(name)
	return strings.Contains(name, "🇷🇺") || russianSmartToken.MatchString(name)
}

func validSmartCountry(country string) bool {
	if len(country) != 2 || !isAsciiUpper(country) || country == "ZZ" || country == "XX" {
		return false
	}
	region, err := language.ParseRegion(country)
	return err == nil && region.IsCountry() && region.String() == country
}

func smartCountryAdmission(settings smartSettings, country string) error {
	if settings.PreferredCountry != "" && slices.Contains(settings.ExcludedCountries, settings.PreferredCountry) {
		return errors.New("country-filter-conflict")
	}
	if !validSmartCountry(country) {
		return errors.New("exit-country-unknown")
	}
	if slices.Contains(settings.ExcludedCountries, country) {
		return fmt.Errorf("country-excluded:%s", country)
	}
	if settings.PreferredCountry != "" && settings.PreferredCountry != country {
		return fmt.Errorf("preferred-country-unavailable:%s", settings.PreferredCountry)
	}
	return nil
}

func (b *smartBackend) node(name string) constant.Proxy {
	b.nodesMu.RLock()
	defer b.nodesMu.RUnlock()
	return b.nodes[name]
}

func (b *smartBackend) identity(proxy constant.Proxy) string {
	if identity := b.spec.Identities[proxy.Name()]; identity != "" {
		return identity
	}
	providerName := proxy.ProxyInfo().ProviderName
	if provider := tunnel.ProvidersSnapshot()[providerName]; provider != nil {
		b.providerMu.Lock()
		defer b.providerMu.Unlock()
		if b.providerCache == nil {
			b.providerCache = map[string]smartProviderIdentity{}
		}
		cached := b.providerCache[providerName]
		if cached.Version != provider.Version() || cached.Hash == "" {
			hash := b.spec.ProviderIdentities[providerName]
			if source, ok := provider.(interface{ Vehicle() P.Vehicle }); ok {
				file, err := os.Open(source.Vehicle().Path())
				if err == nil {
					digest := sha256.New()
					n, readErr := io.Copy(digest, io.LimitReader(file, 64*1024*1024+1))
					_ = file.Close()
					if readErr == nil && n <= 64*1024*1024 {
						hash += hex.EncodeToString(digest.Sum(nil))
					} else {
						hash = ""
					}
				} else {
					hash = ""
				}
			}
			cached = smartProviderIdentity{Version: provider.Version(), Hash: hash}
			b.providerCache[providerName] = cached
		}
		if cached.Hash != "" {
			return smartHash(proxy.Name() + "|" + proxy.Addr() + "|" + proxy.Type().String() + "|" + cached.Hash)
		}
	}
	encoded, _ := proxy.Adapter().MarshalJSON()
	return smartHash(proxy.Name() + "|" + proxy.Addr() + "|" + proxy.Type().String() + "|" + string(encoded))
}

type smartProviderIdentity struct {
	Version uint32
	Hash    string
}

func parseSmartCountry(body string) (string, string) {
	country, ip := traceValue(body, "loc"), traceValue(body, "ip")
	if country == "" {
		var value struct {
			Country     string `json:"country"`
			CountryCode string `json:"country_code"`
			IP          string `json:"ip"`
		}
		if json.Unmarshal([]byte(body), &value) == nil {
			country = value.CountryCode
			if country == "" {
				country = value.Country
			}
			ip = value.IP
		}
	}
	country = strings.ToUpper(strings.TrimSpace(country))
	if !validSmartCountry(country) || !mentionsIp(ip) {
		return "", ""
	}
	return country, smartHash(ip)
}

func (b *smartBackend) country(ctx context.Context, name string) (string, error) {
	ctx = context.WithValue(ctx, smartDiagnosticKey{}, true)
	proxy := b.node(name)
	if proxy == nil {
		return "", errors.New("candidate missing")
	}
	key := smartHash(b.network + "|" + b.identity(proxy))
	smartTraffic.mu.Lock()
	cached := smartTraffic.Geo[key]
	smartTraffic.mu.Unlock()
	ttl := 10 * time.Minute
	if smartMobileNetwork(b.network) {
		ttl = 30 * time.Minute
	}
	if cached.Country != "" && time.Since(cached.Checked) >= 0 && time.Since(cached.Checked) < ttl {
		b.rememberCountry(name, key, cached)
		return cached.Country, nil
	}
	for _, source := range smartCountrySources {
		result := runProbe(ctx, probeRequest{url: source, proxyName: name, maxBody: outboundIpMaxBody, timeout: 3 * time.Second})
		if result == nil {
			continue
		}
		smartTraffic.add(b.network, int64(len(result.Body)), true)
		if result.Error != "" || result.StatusCode != 200 {
			continue
		}
		country, exit := parseSmartCountry(result.Body)
		if country == "" {
			continue
		}
		smartTraffic.mu.Lock()
		if smartTraffic.Geo == nil {
			smartTraffic.Geo = map[string]smartGeoEntry{}
		}
		if len(smartTraffic.Geo) >= 512 {
			oldest := ""
			for id, entry := range smartTraffic.Geo {
				if oldest == "" || entry.Checked.Before(smartTraffic.Geo[oldest].Checked) {
					oldest = id
				}
			}
			delete(smartTraffic.Geo, oldest)
		}
		entry := smartGeoEntry{Country: country, Exit: exit, Checked: time.Now()}
		smartTraffic.Geo[key] = entry
		smartTraffic.mu.Unlock()
		b.rememberCountry(name, key, entry)
		if cached.Exit != "" && cached.Exit != exit && b.runtime != nil && b.runtime.history != nil {
			historyKey := smartHash(b.network) + ":" + smartHash(b.spec.Name)
			record := b.runtime.history.Snapshot(historyKey)[name]
			record.BytesPerSec = 0
			record.SpeedAt = time.Time{}
			b.runtime.history.Put(historyKey, name, record)
		}
		return country, nil
	}
	return "", errors.New("exit-country-unknown")
}

func (b *smartBackend) Qualify(ctx context.Context, name string) error {
	country, err := b.country(ctx, name)
	if err == nil {
		err = smartCountryAdmission(b.spec.Settings, country)
	}
	if err != nil {
		b.rememberQualification(ctx, name, country, err.Error())
		return err
	}
	err = b.checkPriorityServices(ctx, name)
	reason := ""
	if err != nil {
		reason = err.Error()
	}
	b.rememberQualification(ctx, name, country, reason)
	return err
}

func (b *smartBackend) Health(ctx context.Context, name string) error {
	return b.Qualify(context.WithValue(ctx, smartHealthKey{}, true), name)
}

func (b *smartBackend) checkPriorityServices(ctx context.Context, name string) (resultErr error) {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	ttl := 30 * time.Second
	if smartMobileNetwork(b.network) {
		ttl = time.Minute
	}
	b.nodesMu.RLock()
	passed := b.servicePassed[name]
	b.nodesMu.RUnlock()
	if priority, _ := ctx.Value(smartHealthKey{}).(bool); priority && !passed.IsZero() && time.Since(passed) < ttl {
		return nil
	}
	defer func() {
		b.nodesMu.Lock()
		defer b.nodesMu.Unlock()
		if b.servicePassed == nil {
			b.servicePassed = map[string]time.Time{}
		}
		if resultErr == nil {
			b.servicePassed[name] = time.Now()
		} else {
			delete(b.servicePassed, name)
		}
	}()
	checkers := selectedCheckers(b.spec.Settings.Services)
	if len(b.spec.Settings.Services) == 0 {
		return nil
	}
	answers := make(chan ServiceCheckItem, len(checkers))
	for _, checker := range checkers {
		safeGo("smartPriorityService", func() {
			limiter := serviceCheckSlots
			if priority, _ := ctx.Value(smartHealthKey{}).(bool); priority {
				limiter = smartHealthCheckSlots
			}
			if !limiter.acquire(ctx) {
				answers <- ServiceCheckItem{Name: checker.name, Status: serviceTimeout}
				return
			}
			defer limiter.release()
			probeCtx := context.WithValue(ctx, smartDiagnosticKey{}, func(bytes int64) { smartTraffic.add(b.network, bytes, true) })
			answers <- runServiceCheck(probeCtx, checker, name, 3*time.Second)
		})
	}
	for range checkers {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case item := <-answers:
			if item.Status != serviceAvailable {
				return fmt.Errorf("service-%s:%s", item.Name, item.Status)
			}
		}
	}
	return nil
}

func (b *smartBackend) rememberQualification(ctx context.Context, name, country, reason string) {
	b.nodesMu.Lock()
	if b.qualified == nil {
		b.qualified = map[string]smartGroupStatus{}
	}
	b.qualified[name] = smartGroupStatus{Current: name, Country: country, Reason: reason, LastChecked: time.Now().UnixMilli()}
	item := b.qualified[name]
	b.nodesMu.Unlock()
	if b.runtime != nil {
		b.runtime.mu.Lock()
		if ctx.Err() != nil || b.generation != b.runtime.generation {
			b.runtime.mu.Unlock()
			return
		}
		selectMu.Lock()
		current := b.group.Now()
		selectMu.Unlock()
		if current == name {
			b.runtime.states[b.spec.Name] = item
		} else if current == "REJECT" && reason != "" {
			state := b.runtime.states[b.spec.Name]
			state.Reason = reason
			state.Current = "REJECT"
			b.runtime.states[b.spec.Name] = state
		}
		b.runtime.mu.Unlock()
	}
	if reason != "" {
		appendSmartJournal("candidate-rejected", reason)
	}
}

type smartDiagnosticKey struct{}
