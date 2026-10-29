package core

import (
	"core/smart"
	"slices"
)

func smartPoolKey(network, group string) string {
	kind := "wifi"
	if smartMobileNetwork(network) {
		kind = "mobile"
	}
	return kind + ":" + smartHash(group)
}

func (b *smartBackend) KnownCountry(name string) string {
	b.nodesMu.RLock()
	defer b.nodesMu.RUnlock()
	return b.countries[name].Country
}

func (b *smartBackend) keepCommonCurrent() bool {
	if b.runtime == nil || b.runtime.history == nil {
		return false
	}
	name := b.group.Now()
	wifi := b.runtime.history.SavedPool("wifi:" + smartHash(b.spec.Name))[name]
	mobile := b.runtime.history.SavedPool("mobile:" + smartHash(b.spec.Name))[name]
	if wifi.Fingerprint == "" || wifi.Fingerprint != mobile.Fingerprint || slices.Contains(b.spec.Settings.ExcludedCountries, wifi.Country) || slices.Contains(b.spec.Settings.ExcludedCountries, mobile.Country) {
		return false
	}
	for _, node := range b.group.Proxies() {
		if node.Name() == name && b.identity(node) == wifi.Fingerprint {
			return true
		}
	}
	return false
}

func smartPoolItems(history *smart.History, key string) []smartPoolItem {
	items := []smartPoolItem{}
	for name, record := range history.SavedPool(key) {
		item := smartPoolItem{Name: name, Checked: record.Checked.UnixMilli(), LatencyMS: record.Latency.Milliseconds(), Speed: record.BytesPerSec, Country: record.Country, Available: record.Reachable || record.Alive || record.Failure == ""}
		if !record.SpeedAt.IsZero() {
			item.SpeedAt = record.SpeedAt.UnixMilli()
		}
		items = append(items, item)
	}
	slices.SortFunc(items, func(a, b smartPoolItem) int {
		if a.Available != b.Available {
			if a.Available {
				return -1
			}
			return 1
		}
		if a.LatencyMS != b.LatencyMS {
			if a.LatencyMS < b.LatencyMS {
				return -1
			}
			return 1
		}
		if a.Name < b.Name {
			return -1
		}
		if a.Name > b.Name {
			return 1
		}
		return 0
	})
	return items
}
