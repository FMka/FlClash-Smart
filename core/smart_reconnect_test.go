package core

import (
	"context"
	"core/smart"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/metacubex/mihomo/config"
	"github.com/metacubex/mihomo/constant"
	"github.com/metacubex/mihomo/tunnel"
	"github.com/metacubex/mihomo/tunnel/statistic"
)

func TestSmartGroupPreparationKeepsRoutingAndRejectsDirectFallback(t *testing.T) {
	raw, err := unmarshalRawConfig([]byte(`
proxies:
  - {name: node, type: socks5, server: 127.0.0.1, port: 9}
proxy-groups:
  - name: Smart
    type: url-test
    proxies: [DIRECT, node]
    smart-reconnect: {latency-window-ms: 15}
rules: ["DOMAIN,local.test,DIRECT", "MATCH,Smart"]
`))
	if err != nil {
		t.Fatal(err)
	}
	var specs []smartGroupSpec
	if err = prepareSmartGroups(raw, &specs); err != nil {
		t.Fatal(err)
	}
	if len(specs) != 1 || specs[0].Options.LatencyWindow.Milliseconds() != 15 {
		t.Fatalf("specs = %+v", specs)
	}
	cfg, err := config.ParseRawConfig(raw)
	if err != nil {
		t.Fatal(err)
	}
	tunnel.UpdateProxies(cfg.Proxies, cfg.Providers)
	t.Cleanup(func() { tunnel.UpdateProxies(nil, nil) })
	group, err := selectableGroup("Smart")
	if err != nil {
		t.Fatal(err)
	}
	if group.Now() != "REJECT" {
		t.Fatalf("initial route %s is not fail-closed", group.Now())
	}
	b := &smartBackend{spec: specs[0], group: group, expected: "REJECT"}
	candidates := b.Candidates()
	if len(candidates) != 1 || candidates[0].ID != "node" {
		t.Fatalf("candidates = %+v", candidates)
	}
	if len(cfg.Rules) != 2 || raw.Rule[0] != "DOMAIN,local.test,DIRECT" {
		t.Fatal("changed the user's routing rules")
	}
}

func TestSmartConfigRejectsUnsafeOptions(t *testing.T) {
	for _, options := range []string{
		"{speed-url: 'http://example.test/down'}",
		"{speed-url: 'https://user:secret@example.test/down'}",
		"{concurrency: 999}", "{health-interval-seconds: -1}", "{latency-window-ms: -1}",
	} {
		raw, err := unmarshalRawConfig([]byte("proxy-groups: [{name: Smart, type: select, smart-reconnect: " + options + "}]"))
		if err != nil {
			t.Fatal(err)
		}
		if prepareSmartGroups(raw, nil) == nil {
			t.Fatalf("accepted %s", options)
		}
	}
}

func TestSmartSpeedRejectsCaptivePortalAndShortBody(t *testing.T) {
	for _, test := range []struct {
		contentType, body string
		valid             bool
	}{
		{"text/html", strings.Repeat("x", 1024), false},
		{"application/octet-stream", "short", false},
		{"application/octet-stream", strings.Repeat("x", 1024), true},
	} {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Query().Get("bytes") != "1024" || r.Header.Get("Cache-Control") != "no-cache" {
				t.Error("unbounded or cached request")
			}
			w.Header().Set("Content-Type", test.contentType)
			_, _ = w.Write([]byte(test.body))
		}))
		b := &smartBackend{spec: smartGroupSpec{SpeedURL: server.URL, Options: smart.DefaultOptions()}, nodes: map[string]constant.Proxy{"node": namedProxy("node")}}
		speed, err := b.Speed(context.Background(), "node", 1024)
		server.Close()
		if test.valid && (err != nil || speed <= 0) {
			t.Fatalf("valid test: %v, %v", speed, err)
		}
		if !test.valid && err == nil {
			t.Fatal("portal or truncated response passed as speed")
		}
	}
}

func TestSmartPauseCancelsOnlyRequestedGroup(t *testing.T) {
	a, stopA := context.WithCancel(context.Background())
	b, stopB := context.WithCancel(context.Background())
	defer stopA()
	defer stopB()
	r := &smartRuntime{workers: map[string]context.CancelFunc{"a": stopA, "b": stopB}}
	r.pause("a")
	if a.Err() == nil || b.Err() != nil || !r.paused["a"] {
		t.Fatal("manual selection did not cancel just its group")
	}
	r.stop()
	if b.Err() == nil {
		t.Fatal("stop left background work running")
	}
}

func TestSmartDefaultsAndExplicitOptOut(t *testing.T) {
	raw, err := unmarshalRawConfig([]byte(`
proxy-groups:
  - {name: Auto, type: url-test}
  - {name: Secondary, type: url-test}
  - {name: Native, type: url-test, smart-reconnect: false}
  - {name: Manual, type: select}
`))
	if err != nil {
		t.Fatal(err)
	}
	var specs []smartGroupSpec
	if err = prepareSmartGroups(raw, &specs); err != nil {
		t.Fatal(err)
	}
	if len(specs) != 1 || specs[0].Name != "Auto" || raw.ProxyGroup[1]["type"] != "url-test" || raw.ProxyGroup[2]["type"] != "url-test" || raw.ProxyGroup[3]["type"] != "select" {
		t.Fatal("default automation changed manual or opted-out groups")
	}
}

func TestSmartStoppedAndReplacedGroupCannotCommit(t *testing.T) {
	raw, err := unmarshalRawConfig([]byte(`
proxies:
  - {name: node, type: socks5, server: 127.0.0.1, port: 9}
proxy-groups:
  - {name: Smart, type: url-test, proxies: [node]}
`))
	if err != nil {
		t.Fatal(err)
	}
	var specs []smartGroupSpec
	if err = prepareSmartGroups(raw, &specs); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.ParseRawConfig(raw)
	if err != nil {
		t.Fatal(err)
	}
	tunnel.UpdateProxies(cfg.Proxies, cfg.Providers)
	wasRunning := isRunning.Load()
	t.Cleanup(func() { isRunning.Store(wasRunning); tunnel.UpdateProxies(nil, nil) })
	group, err := selectableGroup("Smart")
	if err != nil {
		t.Fatal(err)
	}
	b := &smartBackend{spec: specs[0], group: group, expected: "REJECT"}
	b.Candidates()
	isRunning.Store(false)
	if err = b.Select(context.Background(), "node", "test"); err != context.Canceled || group.Now() != "REJECT" {
		t.Fatal("a stopped VPN accepted a late selection")
	}
	isRunning.Store(true)
	tunnel.UpdateProxies(nil, nil)
	if err = b.Select(context.Background(), "node", "test"); err != context.Canceled {
		t.Fatal("a replaced config accepted a late selection")
	}
}

func TestSmartSpeedCancelsInFlightTransfer(t *testing.T) {
	entered := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/octet-stream")
		w.WriteHeader(200)
		w.(http.Flusher).Flush()
		close(entered)
		<-r.Context().Done()
	}))
	defer server.Close()
	b := &smartBackend{spec: smartGroupSpec{SpeedURL: server.URL, Options: smart.DefaultOptions()}, nodes: map[string]constant.Proxy{"node": namedProxy("node")}}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	finished := make(chan error, 1)
	safeGo("testSmartSpeed", func() { _, err := b.Speed(ctx, "node", 1024); finished <- err })
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("probe never started")
	}
	cancel()
	select {
	case err := <-finished:
		if err == nil {
			t.Fatal("cancelled transfer passed as a speed result")
		}
	case <-time.After(time.Second):
		t.Fatal("cancelled transfer kept running")
	}
}

func TestSmartLatencyRejectsRestrictionStatusAndFake204Page(t *testing.T) {
	previous := currentTestURL()
	defer setTestURL(previous)
	for _, test := range []struct {
		path   string
		status int
		valid  bool
	}{
		{"/generate_204", 204, true},
		{"/generate_204", 200, false},
		{"/custom", 403, false},
		{"/custom", 200, true},
	} {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(test.status) }))
		setTestURL(server.URL + test.path)
		b := &smartBackend{nodes: map[string]constant.Proxy{"node": namedProxy("node")}}
		_, err := b.Latency(context.Background(), "node")
		server.Close()
		if (err == nil) != test.valid {
			t.Fatalf("status %d at %s: %v", test.status, test.path, err)
		}
	}
}

func TestSmartSwitchClosesOldRouteAndManualChoiceWins(t *testing.T) {
	savedRuntime, savedRunning := smartReconnect, isRunning.Load()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	smartReconnect = &smartRuntime{workers: map[string]context.CancelFunc{"Smart": cancel}}
	isRunning.Store(true)
	t.Cleanup(func() {
		smartReconnect = savedRuntime
		isRunning.Store(savedRunning)
		tunnel.UpdateProxies(nil, nil)
	})
	tunnel.UpdateProxies(map[string]constant.Proxy{"Smart": selectorGroup(t, "Smart", "old", "new", "manual")}, nil)
	group, err := selectableGroup("Smart")
	if err != nil {
		t.Fatal(err)
	}
	b := &smartBackend{spec: smartGroupSpec{Name: "Smart"}, group: group, expected: "old", nodes: map[string]constant.Proxy{"new": namedProxy("new")}}
	old, fresh, direct := newPipeTracker(t), newPipeTracker(t), newPipeTracker(t)
	old.Info().Chain = constant.Chain{"old", "Smart"}
	fresh.Info().Chain = constant.Chain{"new", "Smart"}
	direct.Info().Chain = constant.Chain{"DIRECT"}
	if err = b.Select(ctx, "new", "recovery"); err != nil {
		t.Fatal(err)
	}
	if statistic.DefaultManager.Get(old.ID()) != nil || statistic.DefaultManager.Get(fresh.ID()) == nil || statistic.DefaultManager.Get(direct.ID()) == nil {
		t.Fatal("switch left the old route open or closed unrelated connections")
	}
	result := handleChangeProxy(&ChangeProxyParams{GroupName: "Smart", ProxyName: "manual"})
	if !result.Changed || ctx.Err() == nil || !smartReconnect.paused["Smart"] {
		t.Fatal("manual selection did not cancel automation")
	}
	if err = b.Select(ctx, "new", "optimized"); err != context.Canceled || group.Now() != "manual" {
		t.Fatal("late optimization overwrote manual selection")
	}
}
