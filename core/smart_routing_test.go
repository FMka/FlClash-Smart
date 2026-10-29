package core

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/metacubex/mihomo/config"
	"github.com/metacubex/mihomo/constant"
)

func routingTestHome(t *testing.T) string {
	t.Helper()
	previous := constant.Path.HomeDir()
	previousReady := smartRoutingRouteReady
	smartRoutingRouteReady = func(string) bool { return true }
	home := t.TempDir()
	constant.SetHomeDir(home)
	t.Cleanup(func() {
		setSmartRoutingRunning(false)
		setSmartRoutingInfo("none", nil, time.Time{}, "", "", "")
		constant.SetHomeDir(previous)
		smartRoutingRouteReady = previousReady
	})
	return home
}

func routingTestRaw() *config.RawConfig {
	raw := config.DefaultRawConfig()
	raw.Proxy = []map[string]any{{"name": "node", "type": "socks5", "server": "127.0.0.1", "port": 1080}}
	return raw
}

func TestSmartRoutingKeepsExplicitSubscriptionPolicy(t *testing.T) {
	routingTestHome(t)
	for _, rules := range [][]string{{"MATCH,DIRECT"}, {"MATCH,node"}, {"DOMAIN,vk.com,node", "MATCH,node"}} {
		raw := routingTestRaw()
		raw.Rule = append([]string{}, rules...)
		if err := prepareSmartRouting(raw); err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(raw.Rule, rules) || len(raw.RuleProvider) != 0 {
			t.Fatalf("explicit subscription policy replaced: %v", raw.Rule)
		}
		if smartRoutingSnapshot().Source != "subscription" {
			t.Fatal("incorrect routing source")
		}
	}
}

func TestSmartRoutingBuildsRouteForServersOnly(t *testing.T) {
	home := routingTestHome(t)
	raw := routingTestRaw()
	if err := prepareSmartRouting(raw); err != nil {
		t.Fatal(err)
	}
	if len(raw.ProxyGroup) != 1 || raw.Rule[len(raw.Rule)-1] != "MATCH,FlClash Smart" || !strings.HasSuffix(raw.Rule[0], ",DIRECT") {
		t.Fatalf("missing default routing: %v / %v", raw.Rule, raw.ProxyGroup)
	}
	if _, err := os.Stat(filepath.Join(home, "smart-routing", "category-ru.list")); err != nil {
		t.Fatal(err)
	}
	if info := smartRoutingSnapshot(); info.Source != "builtin-ru" || info.Version == "" || info.UpdatedAt == "" {
		t.Fatalf("missing provenance: %+v", info)
	}
}

func TestSmartRoutingPreservesPersonalRulesBeforeFallback(t *testing.T) {
	routingTestHome(t)
	raw := routingTestRaw()
	raw.Rule = []string{"DOMAIN,private.example,node"}
	if err := prepareSmartRouting(raw, []byte("smart-routing-source: none\n")); err != nil {
		t.Fatal(err)
	}
	if raw.Rule[0] != "DOMAIN,private.example,node" || len(raw.Rule) != 3 {
		t.Fatalf("personal rules lost or fallback missing: %v", raw.Rule)
	}
	raw = routingTestRaw()
	raw.Rule = []string{"MATCH,node"}
	if err := prepareSmartRouting(raw, []byte("smart-routing-source: none\n")); err != nil || len(raw.Rule) != 1 {
		t.Fatalf("personal MATCH overridden: %v / %v", raw.Rule, err)
	}
}

func TestSmartRoutingKeepsAnExplicitlyManualGroup(t *testing.T) {
	routingTestHome(t)
	raw := routingTestRaw()
	raw.ProxyGroup = []map[string]any{{"name": "Manual", "type": "select", "proxies": []string{"node"}, "smart-reconnect": false}}
	if err := prepareSmartRouting(raw); err != nil {
		t.Fatal(err)
	}
	var specs []smartGroupSpec
	if err := prepareSmartGroups(raw, &specs); err != nil {
		t.Fatal(err)
	}
	if len(raw.ProxyGroup) != 1 || len(specs) != 0 || raw.Rule[len(raw.Rule)-1] != "MATCH,Manual" {
		t.Fatalf("manual opt-out bypassed: %v / %v", raw.Rule, raw.ProxyGroup)
	}
}

func TestSmartRoutingRejectRouteDoesNotConsumeUpdateBudget(t *testing.T) {
	routingTestHome(t)
	smartRoutingRouteReady = func(string) bool { return false }
	if err := prepareSmartRouting(routingTestRaw()); err != nil {
		t.Fatal(err)
	}
	smartRouting.Lock()
	smartRouting.nextAttempt = time.Time{}
	smartRouting.Unlock()
	setSmartRoutingRunning(true)
	smartRouting.Lock()
	defer smartRouting.Unlock()
	if !smartRouting.nextAttempt.IsZero() || smartRouting.cancel != nil {
		t.Fatal("unavailable route consumed a rule-update attempt")
	}
}

func TestSmartRoutingRejectsMalformedUpdatesAndKeepsLastCopy(t *testing.T) {
	home := routingTestHome(t)
	path := filepath.Join(home, "smart-routing", "category-ru.list")
	good, _, err := smartRoutingData(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, bad := range [][]byte{nil, []byte("<html>denied</html>"), []byte("+.ru\n"), append(append([]byte{}, good...), []byte("MATCH,DIRECT\n")...)} {
		if _, err = validateSmartRouting(bad); err == nil {
			t.Fatal("invalid update accepted")
		}
	}
	still, _, err := smartRoutingData(path)
	if err != nil || !bytes.Equal(still, good) {
		t.Fatal("last valid data changed after validation failures")
	}
}

func TestSmartRoutingBaseWritesReloadableFallbackAndKeepsExceptions(t *testing.T) {
	home := routingTestHome(t)
	profile := writeBaseProfile(t, "proxies:\n  - {name: node, type: socks5, server: 127.0.0.1, port: 1080}\n")
	base, err := handleGetProfileBase(profile)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(home, "config.yaml")
	cfg, err := loadConfigWithBase(&SetupBase{Path: profile, Overlay: overlayOf(t, base, []string{"DOMAIN,exception.example,DIRECT"}, nil)}, path)
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.Rules) != 3 || cfg.Rules[0].Payload() != "exception.example" {
		t.Fatalf("incorrect merged rules: %v", ruleStrings(cfg))
	}
	reloaded, err := loadConfig(path)
	if err != nil || !reflect.DeepEqual(ruleStrings(cfg), ruleStrings(reloaded)) {
		t.Fatalf("reload changed fallback: %v / %v", err, ruleStrings(reloaded))
	}
}

func TestSmartRoutingBasePreservesLegacyRussianPermission(t *testing.T) {
	home := routingTestHome(t)
	profile := writeBaseProfile(t, "proxies:\n  - {name: node, type: socks5, server: 127.0.0.1, port: 1080}\n")
	base, err := handleGetProfileBase(profile)
	if err != nil {
		t.Fatal(err)
	}
	var settings smartSettings
	_, err = loadConfigWithSmartBase(&SetupBase{
		Path: profile,
		Overlay: overlayOf(t, base, nil, map[string]any{
			"smart-reconnect-settings": map[string]any{"allow-russian": true},
		}),
	}, filepath.Join(home, "config.yaml"), nil, &settings)
	if err != nil || len(settings.ExcludedCountries) != 0 {
		t.Fatalf("legacy permission lost in base path: %+v / %v", settings, err)
	}
}

func TestSmartRoutingDoesNotDownloadWhenStopped(t *testing.T) {
	routingTestHome(t)
	previous := fetchSmartRouting
	t.Cleanup(func() { fetchSmartRouting = previous })
	called := false
	fetchSmartRouting = func(context.Context, string) ([]byte, error) {
		called = true
		return nil, errors.New("unexpected download")
	}
	if err := prepareSmartRouting(routingTestRaw()); err != nil {
		t.Fatal(err)
	}
	setSmartRoutingRunning(false)
	if called {
		t.Fatal("stopped routing update used network")
	}
}

func waitSmartRouting(t *testing.T, ready func(smartRoutingInfo) bool) {
	t.Helper()
	timer := time.NewTimer(2 * time.Second)
	defer timer.Stop()
	tick := time.NewTicker(time.Millisecond)
	defer tick.Stop()
	for {
		if ready(smartRoutingSnapshot()) {
			return
		}
		select {
		case <-timer.C:
			t.Fatal("routing update did not finish")
		case <-tick.C:
		}
	}
}

func TestSmartRoutingFailedRefreshKeepsWorkingData(t *testing.T) {
	home := routingTestHome(t)
	previousFetch, previousRunning := fetchSmartRouting, isRunning.Load()
	t.Cleanup(func() { fetchSmartRouting = previousFetch; isRunning.Store(previousRunning) })
	if err := prepareSmartRouting(routingTestRaw()); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(home, "smart-routing", "category-ru.list")
	previous, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	fetchSmartRouting = func(context.Context, string) ([]byte, error) { return []byte("<html>blocked</html>"), nil }
	smartRouting.Lock()
	smartRouting.nextAttempt = time.Time{}
	smartRouting.Unlock()
	isRunning.Store(true)
	setSmartRoutingRunning(true)
	waitSmartRouting(t, func(info smartRoutingInfo) bool { return info.Error == "update-failed" })
	current, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(previous, current) {
		t.Fatal("failed update replaced working data")
	}
}

func TestSmartRoutingStopDiscardsLateRefresh(t *testing.T) {
	home := routingTestHome(t)
	previousFetch, previousRunning := fetchSmartRouting, isRunning.Load()
	t.Cleanup(func() { fetchSmartRouting = previousFetch; isRunning.Store(previousRunning) })
	if err := prepareSmartRouting(routingTestRaw()); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(home, "smart-routing", "category-ru.list")
	previous, _ := os.ReadFile(path)
	started, finished := make(chan struct{}), make(chan struct{})
	fetchSmartRouting = func(ctx context.Context, _ string) ([]byte, error) {
		close(started)
		<-ctx.Done()
		defer close(finished)
		return append(append([]byte{}, previous...), []byte("+.new-public-domain.com\n")...), nil
	}
	smartRouting.Lock()
	smartRouting.nextAttempt = time.Time{}
	smartRouting.Unlock()
	isRunning.Store(true)
	setSmartRoutingRunning(true)
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("refresh did not start")
	}
	isRunning.Store(false)
	setSmartRoutingRunning(false)
	select {
	case <-finished:
	case <-time.After(time.Second):
		t.Fatal("Stop did not cancel refresh")
	}
	current, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(previous, current) {
		t.Fatal("late refresh changed rules after Stop")
	}
}
