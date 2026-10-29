package core

import (
	"core/smart"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/metacubex/mihomo/config"
	"github.com/metacubex/mihomo/tunnel"
)

func TestSmartPoolKeysSeparateTransportsButKeepWiFiHistory(t *testing.T) {
	if smartPoolKey("wifi|dns-a", "Group") != smartPoolKey("wifi|dns-b", "Group") || smartPoolKey("wifi", "Group") == smartPoolKey("lte", "Group") {
		t.Fatal("transport pool separation is not stable")
	}
	if smartPoolKey("wifi", "First") == smartPoolKey("wifi", "Second") {
		t.Fatal("managed groups shared node identities")
	}
}

func TestSmartCommonCurrentRequiresBothPoolsAndUnchangedNode(t *testing.T) {
	settings := defaultSmartSettings()
	raw, err := unmarshalRawConfig([]byte(`
proxies: [{name: common, type: socks5, server: 127.0.0.1, port: 9}]
proxy-groups: [{name: Main, type: select, proxies: [common]}]
rules: ["MATCH,Main"]
`))
	if err != nil {
		t.Fatal(err)
	}
	var specs []smartGroupSpec
	if err = prepareSmartGroups(raw, &specs, settings); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.ParseRawConfig(raw)
	if err != nil {
		t.Fatal(err)
	}
	tunnel.UpdateProxies(cfg.Proxies, cfg.Providers)
	defer tunnel.UpdateProxies(nil, nil)
	group, err := selectableGroup("Main")
	if err != nil {
		t.Fatal(err)
	}
	if err = group.Set("common"); err != nil {
		t.Fatal(err)
	}
	r := &smartRuntime{history: &smart.History{}}
	b := &smartBackend{spec: specs[0], group: group, runtime: r, network: "lte"}
	fingerprint := specs[0].Identities["common"]
	candidate := smart.Candidate{ID: "common", Fingerprint: fingerprint, Country: "DE"}
	r.history.Succeeded(smartPoolKey("wifi", "Main"), candidate, time.Millisecond)
	if b.keepCommonCurrent() {
		t.Fatal("a Wi-Fi-only node bypassed handoff search")
	}
	r.history.Succeeded(smartPoolKey("lte", "Main"), candidate, time.Millisecond)
	if !b.keepCommonCurrent() || group.Now() != "common" {
		t.Fatal("common node was not retained")
	}
	candidate.Fingerprint = "changed"
	r.history.Succeeded(smartPoolKey("lte", "Main"), candidate, time.Millisecond)
	if b.keepCommonCurrent() {
		t.Fatal("changed definition reused another node's evidence")
	}
	candidate.Fingerprint, candidate.Country = fingerprint, "RU"
	r.history.Succeeded(smartPoolKey("lte", "Main"), candidate, time.Millisecond)
	if b.keepCommonCurrent() {
		t.Fatal("handoff ignored an excluded country")
	}
}

func TestSmartPoolStatusIncludesEverySavedResponseAndFailure(t *testing.T) {
	h := &smart.History{}
	for i := range 350 {
		candidate := smart.Candidate{ID: fmt.Sprint(i), Country: "DE"}
		h.Succeeded("wifi", candidate, time.Duration(i+1)*time.Millisecond)
	}
	h.Failed("wifi", smart.Candidate{ID: "0"}, smart.ErrUnavailable, time.Minute)
	items := smartPoolItems(h, "wifi")
	if len(items) != 350 || items[len(items)-1].Available || items[0].Name != "1" {
		t.Fatal("status truncated or hid the saved failed node")
	}
}

func TestSmartProviderRefreshKeepsUnchangedPoolNodeButDetectsCredentials(t *testing.T) {
	original := smartProviderNodeIdentities([]byte("proxies: [{name: one, type: socks5, server: example.test, port: 443, password: first}]"), "source")
	expanded := smartProviderNodeIdentities([]byte("proxies: [{name: one, type: socks5, server: example.test, port: 443, password: first}, {name: two, type: socks5, server: example.test, port: 444}]"), "source")
	changed := smartProviderNodeIdentities([]byte("proxies: [{name: one, type: socks5, server: example.test, port: 443, password: second}]"), "source")
	if original["one"] == "" || expanded["one"] != original["one"] || changed["one"] == original["one"] {
		t.Fatal("provider refresh discarded unchanged evidence or reused changed credentials")
	}
}

func TestSmartPoolStatusPollingIsSmallAndBulkReadIsExplicit(t *testing.T) {
	isolateSmartState(t)
	r := &smartRuntime{history: &smart.History{}, network: "wifi", settings: defaultSmartSettings(), specs: []smartGroupSpec{{Name: "Main"}}}
	smartReconnect = r
	for i := range 350 {
		r.history.Succeeded(smartPoolKey("wifi", "Main"), smart.Candidate{ID: fmt.Sprint(i)}, time.Millisecond)
	}
	value, err := methodHandlers[getSmartStatusMethod](&MethodCall{})
	if err != nil {
		t.Fatal(err)
	}
	summary := value.(smartStatus).Groups[0]
	if summary.WiFiPoolCount != 350 || len(summary.WiFiPool) != 0 || len(summary.Pool) != 0 {
		t.Fatal("regular polling serialized the full saved pool")
	}
	value, err = methodHandlers[getSmartStatusMethod](&MethodCall{Arguments: json.RawMessage("true")})
	if err != nil || len(value.(smartStatus).Groups[0].WiFiPool) != 350 {
		t.Fatal("explicit pool read lost nodes")
	}
	if _, err = methodHandlers[getSmartStatusMethod](&MethodCall{Arguments: json.RawMessage(`"invalid"`)}); err == nil {
		t.Fatal("invalid pool request was accepted")
	}
}
