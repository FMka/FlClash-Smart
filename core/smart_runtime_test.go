package core

import (
	"context"
	"core/smart"
	"encoding/binary"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/metacubex/mihomo/config"
	"github.com/metacubex/mihomo/constant"
	"github.com/metacubex/mihomo/tunnel"
)

func isolateSmartState(t *testing.T) {
	t.Helper()
	oldHome, oldTraffic, oldRuntime := constant.Path.HomeDir(), smartTraffic, smartReconnect
	oldActivity := smartLastActivity.Swap(0)
	constant.SetHomeDir(t.TempDir())
	smartTraffic = &smartTrafficState{}
	t.Cleanup(func() {
		constant.SetHomeDir(oldHome)
		smartTraffic = oldTraffic
		smartReconnect = oldRuntime
		smartLastActivity.Store(oldActivity)
	})
}

func TestSmartSettingsDefaultAndGlobalDisableOverrideLegacyScript(t *testing.T) {
	settings, err := decodeSmartSettings([]byte("smart-reconnect-settings: {enabled: false}"))
	if err != nil {
		t.Fatal(err)
	}
	raw, err := unmarshalRawConfig([]byte("proxy-groups: [{name: Custom, type: select, smart-reconnect: true, proxies: [DIRECT]}]\nrules: [MATCH,Custom]"))
	if err != nil {
		t.Fatal(err)
	}
	var specs []smartGroupSpec
	if err = prepareSmartGroups(raw, &specs, settings); err != nil || len(specs) != 0 || raw.ProxyGroup[0]["default-selected"] != nil {
		t.Fatal("disabled global mode was overridden by legacy script")
	}
	defaults, err := decodeSmartSettings(nil)
	if err != nil || !defaults.Enabled || defaults.AllowRussian || len(defaults.Services) != 4 || defaults.MobileBudgetMiB >= defaults.WiFiBudgetMiB {
		t.Fatalf("defaults: %+v %v", defaults, err)
	}
}

func TestSmartPrimaryMatchFlattensNestedLeavesWithoutChangingRules(t *testing.T) {
	raw, err := unmarshalRawConfig([]byte(`
proxies:
  - {name: first, type: socks5, server: 127.0.0.1, port: 9}
  - {name: second, type: socks5, server: 127.0.0.1, port: 9}
proxy-groups:
  - {name: Native, type: url-test, proxies: [first]}
  - {name: Destination, type: select, proxies: [DIRECT, Native, second]}
rules: ["DOMAIN,local.test,DIRECT", "MATCH,Destination"]
`))
	if err != nil {
		t.Fatal(err)
	}
	var specs []smartGroupSpec
	if err = prepareSmartGroups(raw, &specs); err != nil || len(specs) != 1 || specs[0].Name != "Destination" {
		t.Fatalf("specs %+v %v", specs, err)
	}
	if len(raw.Rule) != 2 || raw.Rule[0] != "DOMAIN,local.test,DIRECT" || raw.ProxyGroup[0]["type"] != "url-test" {
		t.Fatalf("modified unrelated group or routing before parse: %+v %+v", raw.ProxyGroup, raw.Rule)
	}
	cfg, err := config.ParseRawConfig(raw)
	if err != nil {
		t.Fatal(err)
	}
	tunnel.UpdateProxies(cfg.Proxies, cfg.Providers)
	defer tunnel.UpdateProxies(nil, nil)
	group, err := selectableGroup("Destination")
	if err != nil {
		t.Fatal(err)
	}
	b := &smartBackend{spec: specs[0], group: group}
	if nodes := b.Candidates(); len(nodes) != 2 {
		t.Fatalf("nested candidate count %d", len(nodes))
	}
}

func TestSmartRussianNameHasCorrectTokenBoundaries(t *testing.T) {
	for _, name := range []string{"Россия Санкт-Петербург", "🇷🇺 Node", "RU-Moscow", "x [ru] x", "Russia", "Russian Federation"} {
		if !russianSmartName(name) {
			t.Errorf("missed %q", name)
		}
	}
	for _, name := range []string{"Brussels", "Prussia", "Ruby", "RUby", "Latvia"} {
		if russianSmartName(name) {
			t.Errorf("false positive %q", name)
		}
	}
}

func TestSmartExitCountryRequiresPublicAddressAndMachineCountry(t *testing.T) {
	for _, test := range []struct{ body, want string }{{"ip=1.1.1.1\nloc=RU\n", "RU"}, {`{"ip":"8.8.8.8","country_code":"DE"}`, "DE"}, {"ip=127.0.0.1\nloc=DE\n", ""}, {"ip=1.1.1.1\nloc=ZZ\n", ""}, {"<html>captcha</html>", ""}} {
		got, _ := parseSmartCountry(test.body)
		if got != test.want {
			t.Fatalf("%q => %q", test.body, got)
		}
	}
}

func TestSmartQualificationPinsExitAndFailsClosedOnUnknownAndRussianCountry(t *testing.T) {
	isolateSmartState(t)
	body := "ip=1.1.1.1\nloc=RU\n"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = io.WriteString(w, body) }))
	defer server.Close()
	oldSources := smartCountrySources
	smartCountrySources = []string{server.URL}
	defer func() { smartCountrySources = oldSources }()
	withProbeProxies(t, "foreign-label")
	b := &smartBackend{spec: smartGroupSpec{Settings: defaultSmartSettings()}, network: "cellular", nodes: map[string]constant.Proxy{"foreign-label": lookupProxy("foreign-label")}}
	b.spec.Settings.Services = []string{}
	if err := b.Qualify(context.Background(), "foreign-label"); err == nil || !strings.Contains(err.Error(), "country-excluded:RU") {
		t.Fatalf("Russian exit admitted: %v", err)
	}
	body = "captcha"
	smartTraffic.Geo = map[string]smartGeoEntry{}
	if err := b.Qualify(context.Background(), "foreign-label"); err == nil || !strings.Contains(err.Error(), "unknown") {
		t.Fatalf("unknown exit admitted: %v", err)
	}
	body = "ip=1.1.1.1\nloc=DE\n"
	smartTraffic.Geo = map[string]smartGeoEntry{}
	if err := b.Qualify(context.Background(), "foreign-label"); err != nil {
		t.Fatal(err)
	}
	tunnel.UpdateProxies(nil, nil)
	smartTraffic.Geo = map[string]smartGeoEntry{}
	if err := b.Qualify(context.Background(), "foreign-label"); err == nil {
		t.Fatal("missing pinned proxy silently used DIRECT")
	}
}

func TestSmartSpeedBudgetCountsFailedBodyAndPersistsSeparateDailyTotals(t *testing.T) {
	isolateSmartState(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/octet-stream")
		_, _ = io.WriteString(w, strings.Repeat("x", 100))
	}))
	defer server.Close()
	r := &smartRuntime{history: &smart.History{}, settings: defaultSmartSettings()}
	b := &smartBackend{spec: smartGroupSpec{SpeedURL: server.URL, Options: smart.DefaultOptions()}, runtime: r, network: "cellular", nodes: map[string]constant.Proxy{"node": namedProxy("node")}}
	if _, err := b.Speed(context.Background(), "node", 1024); err == nil {
		t.Fatal("short download succeeded")
	}
	if smartTraffic.Mobile != 100 || smartTraffic.WiFi != 0 {
		t.Fatalf("traffic %+v", smartTraffic)
	}
	smartTraffic.add("wifi", 300, false)
	smartTraffic.add("cellular", 50, true)
	saveSmartMetadata()
	smartTraffic = &smartTrafficState{}
	loadSmartMetadata()
	if smartTraffic.Mobile != 100 || smartTraffic.WiFi != 300 || smartTraffic.Diagnostic != 50 {
		t.Fatal("metadata did not survive load")
	}
	r.settings.MobileBudgetMiB = 0
	if _, err := b.Speed(context.Background(), "node", 1024); err == nil || smartTraffic.Mobile != 100 {
		t.Fatal("exhausted budget still started download")
	}
	if !smartTraffic.canDownload("wifi", defaultSmartSettings(), 1024) {
		t.Fatal("mobile exhaustion disabled Wi-Fi tests")
	}
}

func TestSmartCurrentHealthGetsReservedProbeSlotsAndFreshRetryAfterFailure(t *testing.T) {
	isolateSmartState(t)
	withServiceCheckers(t, stubChecker("fake", ServiceCheckItem{Status: serviceAvailable}))
	b := &smartBackend{spec: smartGroupSpec{Settings: smartSettings{Services: []string{"fake"}}}, network: "cellular"}
	for range cap(serviceCheckSlots) {
		serviceCheckSlots <- struct{}{}
	}
	defer func() {
		for range cap(serviceCheckSlots) {
			<-serviceCheckSlots
		}
	}()
	ctx, cancel := context.WithTimeout(context.WithValue(context.Background(), smartHealthKey{}, true), time.Second)
	defer cancel()
	if err := b.checkPriorityServices(ctx, "node"); err != nil {
		t.Fatal("current health starved by candidate slots", err)
	}
	serviceCheckers[0] = stubChecker("fake", ServiceCheckItem{Status: serviceUnavailable})
	if err := b.checkPriorityServices(ctx, "node"); err != nil {
		t.Fatal("fresh successful health was not cached")
	}
	b.servicePassed["node"] = time.Now().Add(-2 * time.Minute)
	if err := b.checkPriorityServices(ctx, "node"); err == nil {
		t.Fatal("failed service was ignored")
	}
	serviceCheckers[0] = stubChecker("fake", ServiceCheckItem{Status: serviceAvailable})
	if err := b.checkPriorityServices(ctx, "node"); err != nil {
		t.Fatal("failure incorrectly cached instead of fresh retry")
	}
}

func TestTelegramHandshakeRejectsWrongNonce(t *testing.T) {
	for _, valid := range []bool{true, false} {
		client, server := net.Pipe()
		go func() {
			defer server.Close()
			packet := make([]byte, 42)
			if _, err := io.ReadFull(server, packet); err != nil {
				return
			}
			reply := make([]byte, 81)
			reply[0] = 20
			binary.LittleEndian.PutUint32(reply[17:21], 60)
			binary.LittleEndian.PutUint32(reply[21:25], 0x05162463)
			copy(reply[25:41], packet[26:42])
			if !valid {
				reply[25] ^= 1
			}
			_, _ = server.Write(reply)
		}()
		_, err := telegramHandshake(context.Background(), client)
		_ = client.Close()
		if (err == nil) != valid {
			t.Fatalf("valid=%v err=%v", valid, err)
		}
	}
}

func TestSmartRestartRequiresTransportFailureAndStopInvalidatesRequest(t *testing.T) {
	isolateSmartState(t)
	oldRunning := isRunning.Load()
	defer isRunning.Store(oldRunning)
	isRunning.Store(true)
	r := &smartRuntime{settings: defaultSmartSettings(), network: "cellular", workers: map[string]context.CancelFunc{"Smart": func() {}}, transportFailure: time.Now().Add(-2 * time.Minute), resetAttempt: time.Now().Add(-time.Minute), transportOK: time.Now()}
	smartReconnect = r
	if TakeSmartRestartRequest() {
		t.Fatal("quiet idle requested a VPN restart")
	}
	smartRecordActivity()
	if TakeSmartRestartRequest() {
		t.Fatal("recent transport success allowed restart for service-only failure")
	}
	r.transportOK = time.Time{}
	if !TakeSmartRestartRequest() || !SmartRestartStillNeeded() || TakeSmartRestartRequest() {
		t.Fatal("restart not rate-limited or request lost")
	}
	smartLastActivity.Store(time.Now().Add(-2 * time.Minute).UnixNano())
	if SmartRestartStillNeeded() {
		t.Fatal("idle did not invalidate native restart request")
	}
	smartRecordActivity()
	isRunning.Store(false)
	if SmartRestartStillNeeded() || SmartRecheck() {
		t.Fatal("Stop was overridden")
	}
}
