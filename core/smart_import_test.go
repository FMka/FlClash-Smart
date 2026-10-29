package core

import (
	"context"
	"core/smart"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"testing"
	"time"

	"github.com/metacubex/mihomo/tunnel"
)

func TestSmartImportInactiveProfileUsesIsolatedProxyWithoutStartingVPN(t *testing.T) {
	isolateSmartState(t)
	oldImports, oldRunning, oldSources := smartImports, isRunning.Load(), smartCountrySources
	smartImports = &smartImportManager{}
	smartReconnect = &smartRuntime{network: "wifi", history: &smart.History{}}
	isRunning.Store(false)
	smartCountrySources = []string{"http://scan.invalid/geo"}
	t.Cleanup(func() { smartImports = oldImports; isRunning.Store(oldRunning); smartCountrySources = oldSources })
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodConnect {
			t.Errorf("expected CONNECT, got %s", request.Method)
			return
		}
		conn, reader, err := w.(http.Hijacker).Hijack()
		if err != nil {
			return
		}
		defer conn.Close()
		_, _ = io.WriteString(conn, "HTTP/1.1 200 Connection established\r\n\r\n")
		nested, err := http.ReadRequest(reader.Reader)
		if err != nil {
			return
		}
		if nested.Method == http.MethodHead {
			_, _ = io.WriteString(conn, "HTTP/1.1 204 No Content\r\nConnection: close\r\n\r\n")
		} else {
			body := "ip=8.8.8.8\nloc=DE\n"
			_, _ = fmt.Fprintf(conn, "HTTP/1.1 200 OK\r\nContent-Length: %d\r\nConnection: close\r\n\r\n%s", len(body), body)
		}
	}))
	defer proxy.Close()
	host, port, _ := net.SplitHostPort(proxy.Listener.Addr().String())
	configText := fmt.Sprintf("smart-profile-id: alpha\nproxies: [{name: inactive, type: http, server: %s, port: %s}]\nproxy-groups: [{name: PROXY, type: select, proxies: [inactive]}]\nrules: ['MATCH,PROXY']", host, port)
	before := tunnel.AllProxies()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	job := &smartImportJob{cancel: cancel, status: smartImportStatus{ProfileID: "alpha", ScanID: "1", Phase: "scanning", Countries: []string{}}}
	smartImports.jobs = map[string]*smartImportJob{"alpha": job}
	smartImports.run(ctx, job, smartImportParams{ProfileID: "alpha", Config: configText, Params: &SetupParams{TestURL: "http://scan.invalid/generate_204"}}, "wifi", smartReconnect.history)
	if job.status.Phase != "complete" || job.status.Checked != 1 || job.status.Responded != 1 || !reflect.DeepEqual(job.status.Countries, []string{"DE"}) {
		t.Fatalf("scan: %+v", job.status)
	}
	if isRunning.Load() || lookupProxy("inactive") != nil || !reflect.DeepEqual(tunnel.AllProxies(), before) {
		t.Fatal("isolated scan changed running profile or VPN state")
	}
	wifi := smartReconnect.history.SavedPool(smartPoolKey("wifi", "alpha|PROXY"))
	if wifi["inactive"].Country != "DE" || len(smartReconnect.history.SavedPool(smartPoolKey("lte", "alpha|PROXY"))) != 0 {
		t.Fatal("scan did not stay in its actual network pool")
	}
	if len(smartReconnect.history.SavedPool(smartPoolKey("wifi", "beta|PROXY"))) != 0 {
		t.Fatal("scan contaminated another subscription")
	}
	status := handleGetSmartStatus()
	if !reflect.DeepEqual(status.DiscoveredCountries, []string{"DE"}) {
		t.Fatalf("import countries missing from status: %v", status.DiscoveredCountries)
	}
}

func TestSmartImportIdentityMatchesPreparedProfileAndSkipsGlobalDependencies(t *testing.T) {
	isolateSmartState(t)
	text := "smart-profile-id: alpha\nproxies: [{name: one, type: socks5, server: 127.0.0.1, port: 9}, {name: chain, type: socks5, server: 127.0.0.1, port: 9, dialer-proxy: one}]\nproxy-groups: [{name: PROXY, type: select, proxies: [one,chain]}]\nrules: ['MATCH,PROXY']"
	raw, err := unmarshalRawConfig([]byte(text))
	if err != nil {
		t.Fatal(err)
	}
	settings, err := decodeSmartSettings([]byte(text))
	if err != nil {
		t.Fatal(err)
	}
	var specs []smartGroupSpec
	if err = prepareSmartGroups(raw, &specs, settings); err != nil {
		t.Fatal(err)
	}
	nodes, unsupported, err := smartImportNodes(raw, settings)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		for _, node := range nodes {
			_ = node.proxy.Close()
		}
	}()
	if unsupported != 1 || len(nodes) != 1 || nodes["one"].identity != specs[0].Identities["one"] || !reflect.DeepEqual(nodes["one"].groups, []string{"alpha|PROXY"}) {
		t.Fatalf("incompatible pool identity: %+v %d %v", nodes, unsupported, err)
	}
}

func TestSmartImportCachedProviderIdentitySurvivesSiblingAddition(t *testing.T) {
	isolateSmartState(t)
	path := filepath.Join(t.TempDir(), "provider.yaml")
	first := "proxies: [{name: one, type: socks5, server: 127.0.0.1, port: 9}]"
	var identity string
	for _, content := range []string{first, "proxies: [{name: one, type: socks5, server: 127.0.0.1, port: 9}, {name: two, type: socks5, server: 127.0.0.1, port: 10}]"} {
		if err := os.WriteFile(path, []byte(content), 0600); err != nil {
			t.Fatal(err)
		}
		raw, err := unmarshalRawConfig([]byte("proxy-providers: {local: {type: file, path: " + strconv.Quote(filepath.ToSlash(path)) + "}}\nproxy-groups: [{name: PROXY, type: select, use: [local]}]\nrules: ['MATCH,PROXY']"))
		if err != nil {
			t.Fatal(err)
		}
		nodes, unsupported, err := smartImportNodes(raw, defaultSmartSettings())
		if err != nil || unsupported != 0 || nodes["one"] == nil {
			t.Fatalf("provider parse: %v %d", err, unsupported)
		}
		if identity != "" && identity != nodes["one"].identity {
			t.Fatal("unchanged provider node identity changed")
		}
		identity = nodes["one"].identity
		for _, node := range nodes {
			_ = node.proxy.Close()
		}
	}
}

func TestSmartImportCancelIsScopedAndCancelledRunCannotCommitPool(t *testing.T) {
	isolateSmartState(t)
	old := smartImports
	smartImports = &smartImportManager{}
	t.Cleanup(func() { smartImports = old })
	smartReconnect = &smartRuntime{network: "lte", history: &smart.History{}}
	ctx, cancel := context.WithCancel(context.Background())
	job := &smartImportJob{cancel: cancel, status: smartImportStatus{ProfileID: "alpha", ScanID: "2", Phase: "scanning"}}
	smartImports.jobs = map[string]*smartImportJob{"alpha": job}
	if _, err := handleScanSmartProfile(smartImportParams{Operation: "cancel", ProfileID: "alpha", ScanID: "1"}); err == nil || ctx.Err() != nil {
		t.Fatal("stale cancel reached new scan")
	}
	if result, err := handleScanSmartProfile(smartImportParams{Operation: "cancel", ProfileID: "alpha", ScanID: "2"}); err != nil || result.Phase != "cancelled" || ctx.Err() == nil {
		t.Fatalf("cancel failed: %+v %v", result, err)
	}
	smartImports.run(ctx, job, smartImportParams{ProfileID: "alpha", Config: "proxies: []"}, "lte", smartReconnect.history)
	if len(smartReconnect.history.Records) != 0 || job.status.Phase != "cancelled" {
		t.Fatal("cancelled scan committed a result")
	}
}

func TestSmartImportLifecycleCancelAndSnapshotAreDetached(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	manager := &smartImportManager{jobs: map[string]*smartImportJob{"alpha": {cancel: cancel, status: smartImportStatus{Phase: "scanning", Countries: []string{"DE"}}}}}
	snapshot := manager.snapshot()
	snapshot[0].Countries[0] = "RU"
	manager.cancelAll()
	if ctx.Err() == nil || manager.jobs["alpha"].status.Phase != "cancelled" || manager.jobs["alpha"].status.Countries[0] != "DE" {
		t.Fatal("lifecycle cancellation or snapshot ownership failed")
	}
}

func TestSmartImportFirstApplyDoesNotCancelButStopDoes(t *testing.T) {
	isolateSmartState(t)
	old := smartImports
	t.Cleanup(func() { smartImports = old })
	ctx, cancel := context.WithCancel(context.Background())
	job := &smartImportJob{cancel: cancel, status: smartImportStatus{Phase: "scanning"}}
	smartImports = &smartImportManager{jobs: map[string]*smartImportJob{"alpha": job}}
	r := &smartRuntime{}
	smartReconnect = r
	r.beginConfig()
	if ctx.Err() != nil {
		t.Fatal("first imported profile apply cancelled its independent inventory")
	}
	r.stop()
	if ctx.Err() == nil || job.status.Phase != "cancelled" {
		t.Fatal("Stop left an import scan active")
	}
}

func TestSmartImportOldFingerprintCannotOverwriteNewerMeasurement(t *testing.T) {
	history := &smart.History{}
	started := time.Now().Add(-time.Second)
	current := smart.Candidate{ID: "node", Fingerprint: "new", Country: "DE"}
	history.Succeeded("wifi:alpha", current, time.Millisecond)
	old := smart.Candidate{ID: "node", Fingerprint: "old", Country: "RU"}
	if history.ObserveIfCurrent("wifi:alpha", old, 2*time.Millisecond, nil, started) {
		t.Fatal("stale profile snapshot overwrote new content")
	}
	if record := history.Snapshot("wifi:alpha")["node"]; record.Fingerprint != "new" || record.Country != "DE" {
		t.Fatal("new measurement was changed")
	}
}

func TestSmartImportInlineProvidersAndTransformedProvidersStayIsolated(t *testing.T) {
	text := `proxy-providers:
  supported: {type: inline, payload: [{name: plain, type: socks5, server: 127.0.0.1, port: 9}]}
  routed: {type: inline, dialer-proxy: ACTIVE, payload: [{name: routed, type: socks5, server: 127.0.0.1, port: 9}]}
  renamed: {type: inline, override: {additional-prefix: changed}, payload: [{name: renamed, type: socks5, server: 127.0.0.1, port: 9}]}
proxy-groups: [{name: PROXY, type: select, use: [supported,routed,renamed]}]
rules: ['MATCH,PROXY']`
	raw, err := unmarshalRawConfig([]byte(text))
	if err != nil {
		t.Fatal(err)
	}
	var specs []smartGroupSpec
	if err = prepareSmartGroups(raw, &specs); err != nil {
		t.Fatal(err)
	}
	nodes, unsupported, err := smartImportNodes(raw, defaultSmartSettings())
	if err != nil || unsupported != 2 || len(nodes) != 1 {
		t.Fatalf("provider transforms were bypassed: %v %d %d", err, unsupported, len(nodes))
	}
	node := nodes["plain"]
	defer node.proxy.Close()
	want := smartHash("plain|" + node.proxy.Addr() + "|" + node.proxy.Type().String() + "|" + specs[0].ProviderIdentities["supported"])
	if node.identity != want {
		t.Fatal("inline provider identity differs from runtime")
	}
}

func TestSmartImportResponseSurvivesSlowCountryCancellation(t *testing.T) {
	isolateSmartState(t)
	oldImports, oldSources := smartImports, smartCountrySources
	smartImports = &smartImportManager{}
	smartReconnect = &smartRuntime{network: "wifi", history: &smart.History{}}
	smartCountrySources = []string{"http://scan.invalid/geo"}
	t.Cleanup(func() { smartImports = oldImports; smartCountrySources = oldSources })
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	release := make(chan struct{})
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		conn, reader, err := w.(http.Hijacker).Hijack()
		if err != nil {
			return
		}
		defer conn.Close()
		_, _ = io.WriteString(conn, "HTTP/1.1 200 Connection established\r\n\r\n")
		nested, err := http.ReadRequest(reader.Reader)
		if err != nil {
			return
		}
		if nested.Method == http.MethodHead {
			_, _ = io.WriteString(conn, "HTTP/1.1 204 No Content\r\nConnection: close\r\n\r\n")
		} else {
			cancel()
			<-release
		}
	}))
	defer proxy.Close()
	host, port, _ := net.SplitHostPort(proxy.Listener.Addr().String())
	text := fmt.Sprintf("smart-profile-id: alpha\nproxies: [{name: fast, type: http, server: %s, port: %s}]\nproxy-groups: [{name: PROXY, type: select, proxies: [fast]}]\nrules: ['MATCH,PROXY']", host, port)
	job := &smartImportJob{cancel: cancel, status: smartImportStatus{ProfileID: "alpha", ScanID: "1", Phase: "scanning"}}
	smartImports.run(ctx, job, smartImportParams{ProfileID: "alpha", Config: text, Params: &SetupParams{TestURL: "http://scan.invalid/generate_204"}}, "wifi", smartReconnect.history)
	close(release)
	pool := smartReconnect.history.SavedPool(smartPoolKey("wifi", "alpha|PROXY"))
	if record := pool["fast"]; record.RespondedAt.IsZero() || !record.Reachable || record.Country != "" || job.status.Phase != "cancelled" {
		t.Fatalf("response lost waiting for region: %+v %+v", record, job.status)
	}
}
