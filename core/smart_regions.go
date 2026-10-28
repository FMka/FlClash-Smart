package core

import (
	"context"
	"sort"
	"strings"
	"time"
)

func (b *smartBackend) Discover(ctx context.Context, name string) {
	_, _ = b.country(ctx, name)
}

func (b *smartBackend) PolicyFailure(err error) bool {
	if err == nil {
		return false
	}
	reason := err.Error()
	return reason == "country-filter-conflict" || reason == "exit-country-unknown" || strings.HasPrefix(reason, "country-excluded:") || strings.HasPrefix(reason, "preferred-country-unavailable:")
}

func (b *smartBackend) Block(ctx context.Context, reason string) error {
	if err := b.Select(ctx, "REJECT", reason); err != nil {
		return err
	}
	b.rememberQualification(ctx, "REJECT", "", reason)
	return nil
}

func (b *smartBackend) rememberCountry(name, key string, entry smartGeoEntry) {
	b.nodesMu.Lock()
	defer b.nodesMu.Unlock()
	if expected := b.countryKeys[name]; expected != "" && expected != key {
		return
	}
	if b.countries == nil {
		b.countries = map[string]smartGeoEntry{}
	}
	b.countries[name] = entry
}

func (b *smartBackend) countryInventory() ([]string, int) {
	b.nodesMu.RLock()
	defer b.nodesMu.RUnlock()
	seen, unresolved := map[string]bool{}, 0
	for name := range b.nodes {
		entry := b.countries[name]
		age := time.Since(entry.Checked)
		if !validSmartCountry(entry.Country) || age < 0 || age >= 7*24*time.Hour {
			unresolved++
			continue
		}
		seen[entry.Country] = true
	}
	countries := make([]string, 0, len(seen))
	for country := range seen {
		countries = append(countries, country)
	}
	sort.Strings(countries)
	return countries, unresolved
}
