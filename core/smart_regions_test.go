package core

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/metacubex/mihomo/constant"
)

func TestSmartCountrySettingsDefaultsMigrationAndValidation(t *testing.T) {
	for _, test := range []struct {
		yaml, preferred string
		excluded        []string
		invalid         bool
	}{
		{"", "", []string{"RU"}, false},
		{"allow-russian: true", "", []string{}, false},
		{"allow-russian: false", "", []string{"RU"}, false},
		{"allow-russian: true, excluded-countries: [ru, us, RU], preferred-country: de", "DE", []string{"RU", "US"}, false},
		{"excluded-countries: []", "", []string{}, false},
		{"preferred-country: EU", "", nil, true},
		{"excluded-countries: [ZZ]", "", nil, true},
		{"preferred-country: garbage", "", nil, true},
	} {
		data := "smart-reconnect-settings: {" + test.yaml + "}"
		settings, err := decodeSmartSettings([]byte(data))
		if test.invalid {
			if err == nil {
				t.Errorf("accepted %s", data)
			}
			continue
		}
		if err != nil || settings.PreferredCountry != test.preferred || !reflect.DeepEqual(settings.ExcludedCountries, test.excluded) || !settings.DNSRecoveryEnabled {
			t.Errorf("%s: %+v %v", data, settings, err)
		}
	}
}

func TestSmartPreferredCountryAllowsFallbackButNeverOverridesExclusion(t *testing.T) {
	settings := defaultSmartSettings()
	settings.PreferredCountry = "DE"
	for _, test := range []struct{ country, reason string }{
		{"DE", ""}, {"RU", "country-excluded:RU"}, {"US", ""}, {"", "exit-country-unknown"},
	} {
		err := smartCountryAdmission(settings, test.country)
		if test.reason == "" {
			if err != nil {
				t.Fatal(err)
			}
			continue
		}
		if err == nil || err.Error() != test.reason {
			t.Fatalf("%s: %v", test.country, err)
		}
	}
	settings.PreferredCountry = "RU"
	if err := smartCountryAdmission(settings, "RU"); err == nil {
		t.Fatal("preferred country bypassed an exclusion")
	}
	if err := smartCountryAdmission(settings, "DE"); err != nil {
		t.Fatalf("excluded preference prevented allowed fallback: %v", err)
	}
}

func TestSmartCountryInventoryIncludesExcludedAndServiceFailedExits(t *testing.T) {
	isolateSmartState(t)
	body := "ip=1.1.1.1\nloc=RU\n"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = io.WriteString(w, body) }))
	defer server.Close()
	oldSources := smartCountrySources
	smartCountrySources = []string{server.URL}
	t.Cleanup(func() { smartCountrySources = oldSources })
	withProbeProxies(t, "foreign", "RU-labelled")
	b := &smartBackend{spec: smartGroupSpec{Settings: defaultSmartSettings()}, network: "cellular", nodes: map[string]constant.Proxy{"foreign": lookupProxy("foreign"), "RU-labelled": lookupProxy("RU-labelled")}}
	if err := b.Qualify(context.Background(), "foreign"); err == nil {
		t.Fatal("excluded RU exit admitted")
	}
	body = "ip=8.8.8.8\nloc=DE\n"
	withServiceCheckers(t, stubChecker("fake", ServiceCheckItem{Status: serviceUnavailable}))
	b.spec.Settings.Services = []string{"fake"}
	if err := b.Qualify(context.Background(), "RU-labelled"); err == nil || !strings.Contains(err.Error(), "service-fake") {
		t.Fatal("test did not reach service admission", err)
	}
	countries, unresolved := b.countryInventory()
	if !reflect.DeepEqual(countries, []string{"DE", "RU"}) || unresolved != 0 {
		t.Fatalf("inventory follows admission filter: %v %d", countries, unresolved)
	}
	b.spec.Settings.Services = []string{}
	if err := b.Qualify(context.Background(), "RU-labelled"); err != nil {
		t.Fatal("foreign verified exit rejected by node label", err)
	}
}

func TestSmartCountryInventoryDoesNotAdmitStaleIdentityResults(t *testing.T) {
	b := &smartBackend{nodes: map[string]constant.Proxy{"changed": nil, "unknown": nil}, countryKeys: map[string]string{"changed": "new"}}
	b.rememberCountry("changed", "old", smartGeoEntry{Country: "RU", Checked: time.Now()})
	if countries, unresolved := b.countryInventory(); len(countries) != 0 || unresolved != 2 {
		t.Fatal("stale identity country leaked into inventory")
	}
	b.rememberCountry("changed", "new", smartGeoEntry{Country: "DE", Checked: time.Now()})
	if countries, unresolved := b.countryInventory(); !reflect.DeepEqual(countries, []string{"DE"}) || unresolved != 1 {
		t.Fatalf("inventory %v %d", countries, unresolved)
	}
}
