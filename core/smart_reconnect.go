package core

import (
	"context"
	"core/smart"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/metacubex/http"
	"github.com/metacubex/mihomo/adapter/outboundgroup"
	"github.com/metacubex/mihomo/common/utils"
	"github.com/metacubex/mihomo/component/ca"
	"github.com/metacubex/mihomo/config"
	"github.com/metacubex/mihomo/constant"
	"github.com/metacubex/mihomo/log"
	"github.com/metacubex/mihomo/tunnel"
	"github.com/metacubex/mihomo/tunnel/statistic"
	"go.yaml.in/yaml/v3"
)

type smartGroupSpec struct {
	Name               string
	Options            smart.Options
	SpeedURL           string
	Settings           smartSettings
	Sources            []string
	Providers          []string
	Identities         map[string]string
	ProviderIdentities map[string]string
}

func (s smartGroupSpec) poolName() string {
	if s.Settings.ProfileID == "" {
		return s.Name
	}
	return s.Settings.ProfileID + "|" + s.Name
}

func prepareSmartGroups(raw *config.RawConfig, specs *[]smartGroupSpec, configured ...smartSettings) error {
	settings := defaultSmartSettings()
	if len(configured) > 0 {
		settings = configured[0]
	}
	if !settings.Enabled {
		return nil
	}
	primary := primarySmartGroup(raw)
	for _, group := range raw.ProxyGroup {
		value, exists := group["smart-reconnect"]
		kind, _ := group["type"].(string)
		if !exists && group["name"] == primary {
			value, exists = true, true
		}
		if !exists || value == false {
			continue
		}
		spec := smartGroupSpec{Options: smart.DefaultOptions(), SpeedURL: "https://speed.cloudflare.com/__down", Settings: settings, Sources: smartStrings(group["proxies"]), Providers: smartStrings(group["use"]), Identities: map[string]string{}}
		spec.ProviderIdentities = map[string]string{}
		for name, provider := range raw.ProxyProvider {
			encoded, _ := yaml.Marshal(provider)
			spec.ProviderIdentities[name] = smartHash(string(encoded))
		}
		for _, node := range raw.Proxy {
			name, _ := node["name"].(string)
			encoded, _ := yaml.Marshal(node)
			spec.Identities[name] = smartHash(string(encoded))
		}
		spec.Name, _ = group["name"].(string)
		if kind != "select" && kind != "url-test" {
			return fmt.Errorf("smart-reconnect requires a select or url-test group")
		}
		if value != true {
			data, err := yaml.Marshal(value)
			if err != nil {
				return err
			}
			var options struct {
				Window      int    `yaml:"latency-window-ms"`
				Concurrency int    `yaml:"concurrency"`
				Health      int    `yaml:"health-interval-seconds"`
				Interval    int    `yaml:"optimize-interval-seconds"`
				SpeedURL    string `yaml:"speed-url"`
			}
			if err = yaml.Unmarshal(data, &options); err != nil {
				return err
			}
			if options.Window != 0 {
				spec.Options.LatencyWindow = time.Duration(options.Window) * time.Millisecond
			}
			if options.Concurrency != 0 {
				spec.Options.Concurrency = options.Concurrency
			}
			if options.Health != 0 {
				spec.Options.HealthInterval = time.Duration(options.Health) * time.Second
			}
			if options.Interval != 0 {
				spec.Options.OptimizeInterval = time.Duration(options.Interval) * time.Second
			}
			if options.SpeedURL != "" {
				spec.SpeedURL = options.SpeedURL
			}
		}
		parsed, err := url.Parse(spec.SpeedURL)
		if err != nil || parsed.Scheme != "https" || parsed.Host == "" || parsed.User != nil {
			return errors.New("smart-reconnect speed-url must be an HTTPS URL without credentials")
		}
		if spec.Options.Concurrency < 1 || spec.Options.Concurrency > 16 || spec.Options.LatencyWindow < time.Millisecond || spec.Options.LatencyWindow > time.Second || spec.Options.HealthInterval < 3*time.Second || spec.Options.OptimizeInterval < time.Minute {
			return errors.New("smart-reconnect settings outside supported limits")
		}
		group["type"] = "select"
		group["empty-fallback"] = "REJECT"
		group["default-selected"] = "REJECT"
		flattenSmartGroup(raw, group)
		if specs != nil {
			*specs = append(*specs, spec)
		}
	}
	return nil
}

type smartRuntime struct {
	mu               sync.Mutex
	saveMu           sync.Mutex
	specs            []smartGroupSpec
	workers          map[string]context.CancelFunc
	paused           map[string]bool
	history          *smart.History
	network          string
	configuring      bool
	settings         smartSettings
	engines          map[string]*smart.Engine
	backends         map[string]*smartBackend
	generation       uint64
	phases           map[string]string
	states           map[string]smartGroupStatus
	transportOK      time.Time
	transportFailure time.Time
	resetAttempt     time.Time
	lastRestart      time.Time
}

var smartReconnect = &smartRuntime{history: &smart.History{}}
var smartSpeedSlots = make(slots, 1)

func (r *smartRuntime) cancelLocked() {
	r.generation++
	for _, cancel := range r.workers {
		cancel()
	}
	r.workers = map[string]context.CancelFunc{}
	r.engines = map[string]*smart.Engine{}
}

func (r *smartRuntime) beginConfig() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.configuring = true
	r.cancelLocked()
}

func (r *smartRuntime) configure(specs []smartGroupSpec, settings ...smartSettings) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.cancelLocked()
	r.specs, r.paused, r.configuring = specs, map[string]bool{}, false
	r.settings = defaultSmartSettings()
	if len(settings) > 0 {
		r.settings = settings[0]
	}
	r.phases, r.states = map[string]string{}, map[string]smartGroupStatus{}
	r.backends = map[string]*smartBackend{}
	r.transportFailure, r.resetAttempt = time.Time{}, time.Time{}
	if runtime.GOOS != "android" && r.network == "" {
		r.network = "desktop"
	}
	r.startLocked()
}

func (r *smartRuntime) stop() {
	r.mu.Lock()
	defer r.mu.Unlock()
	smartImports.cancelAll()
	r.cancelLocked()
	r.transportFailure, r.resetAttempt = time.Time{}, time.Time{}
}

func (r *smartRuntime) resume() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.paused = map[string]bool{}
	r.transportFailure, r.resetAttempt = time.Time{}, time.Time{}
	r.startLocked()
}

func (r *smartRuntime) pause(group string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.pauseLocked(group)
}

func (r *smartRuntime) pauseLocked(group string) {
	if cancel := r.workers[group]; cancel != nil {
		cancel()
		delete(r.workers, group)
	}
	if r.paused == nil {
		r.paused = map[string]bool{}
	}
	r.paused[group] = true
	if r.phases == nil {
		r.phases = map[string]string{}
	}
	r.phases[group] = "manual"
	appendSmartJournal("manual", "manual selection")
}

func (r *smartRuntime) startLocked() {
	if !isRunning.Load() || r.configuring || r.network == "" || !r.settings.Enabled {
		return
	}
	if r.history == nil {
		r.history = &smart.History{}
	}
	if r.workers == nil {
		r.workers = map[string]context.CancelFunc{}
	}
	for _, spec := range r.specs {
		if r.workers[spec.Name] != nil || r.paused[spec.Name] {
			continue
		}
		selectMu.Lock()
		group, err := selectableGroup(spec.Name)
		backend := &smartBackend{spec: spec, group: group, expected: "REJECT", network: r.network, runtime: r, generation: r.generation}
		keep := false
		if err == nil {
			if spec.Settings.ProfileID != "" {
				for _, transport := range []string{"wifi", "mobile"} {
					key := transport + ":" + smartHash(spec.poolName())
					if len(r.history.Snapshot(key)) == 0 {
						r.history.Import(transport+":"+smartHash(spec.Name), key)
					}
				}
			}
			if key := smartPoolKey(r.network, spec.poolName()); len(r.history.Snapshot(key)) == 0 {
				r.history.Import(smartHash(r.network)+":"+smartHash(spec.Name), key)
			}
			keep = backend.keepCommonCurrent()
			if keep {
				backend.expected = group.Now()
			} else {
				err = group.Set("REJECT")
			}
		}
		selectMu.Unlock()
		if err != nil {
			log.Warnln("[SmartReconnect] group cannot be managed: %v", err)
			continue
		}
		ctx, cancel := context.WithCancel(context.Background())
		r.workers[spec.Name] = cancel
		refreshRouteLockedSafe()
		if r.backends == nil {
			r.backends = map[string]*smartBackend{}
		}
		r.backends[spec.Name] = backend
		if smartMobileNetwork(r.network) {
			spec.Options.SpeedTTL = 30 * time.Minute
			spec.Options.PoolRefreshInterval = 5 * time.Minute
		} else {
			spec.Options.SpeedTTL = 10 * time.Minute
		}
		spec.Options.KeepCurrent = keep
		spec.Options.NetworkContext = smartHash(r.network)
		spec.Options.PreferredCountry = spec.Settings.PreferredCountry
		engine := smart.New(spec.Options, r.history)
		if r.engines == nil {
			r.engines = map[string]*smart.Engine{}
		}
		r.engines[spec.Name] = engine
		generation := r.generation
		engine.Status = func(phase string) {
			r.mu.Lock()
			valid := generation == r.generation
			if valid {
				r.phases[spec.Name] = phase
			}
			r.mu.Unlock()
			if !valid {
				return
			}
			appendSmartJournal(phase, "")
			if phase == "unavailable" || phase == "internal-error" {
				log.Warnln("[SmartReconnect] %s", phase)
			} else {
				log.Infoln("[SmartReconnect] %s", phase)
			}
		}
		engine.Save = r.save
		network := smartPoolKey(r.network, spec.poolName())
		safeGo("smartReconnect", func() {
			defer func() {
				r.mu.Lock()
				if generation == r.generation && r.engines[spec.Name] == engine {
					delete(r.workers, spec.Name)
					delete(r.engines, spec.Name)
					if ctx.Err() == nil {
						r.phases[spec.Name] = "internal-error"
					}
				}
				r.mu.Unlock()
			}()
			engine.Run(ctx, network, backend)
		})
	}
}

func smartHash(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])
}

func (r *smartRuntime) load() {
	r.mu.Lock()
	defer r.mu.Unlock()
	loadSmartMetadata()
	r.settings = defaultSmartSettings()
	r.cancelLocked()
	r.history = &smart.History{}
	data, err := os.ReadFile(filepath.Join(constant.Path.HomeDir(), "smart-history.json"))
	if err == nil && len(data) <= 64*1024*1024 {
		_ = json.Unmarshal(data, r.history)
	}
}

func (r *smartRuntime) save() {
	r.saveMu.Lock()
	defer r.saveMu.Unlock()
	saveSmartMetadata()
	r.mu.Lock()
	history := r.history
	r.mu.Unlock()
	data, err := json.Marshal(history)
	if err != nil {
		return
	}
	path := filepath.Join(constant.Path.HomeDir(), "smart-history.json")
	if os.WriteFile(path+".tmp", data, 0600) == nil {
		_ = os.Rename(path+".tmp", path)
	}
}

type smartBackend struct {
	isolated      bool
	activity      smartActivitySample
	spec          smartGroupSpec
	group         pickableGroup
	expected      string
	nodes         map[string]constant.Proxy
	nodesMu       sync.RWMutex
	qualified     map[string]smartGroupStatus
	countryKeys   map[string]string
	countries     map[string]smartGeoEntry
	servicePassed map[string]time.Time
	providerMu    sync.Mutex
	providerCache map[string]smartProviderIdentity
	network       string
	runtime       *smartRuntime
	generation    uint64
}

func (b *smartBackend) Candidates() []smart.Candidate {
	selectMu.Lock()
	defer selectMu.Unlock()
	current, err := selectableGroup(b.spec.Name)
	if err != nil || current != b.group {
		return nil
	}
	b.nodesMu.Lock()
	defer b.nodesMu.Unlock()
	b.nodes = map[string]constant.Proxy{}
	previousKeys := b.countryKeys
	b.countryKeys = map[string]string{}
	allowed := map[string]bool{}
	visited := map[string]bool{}
	var allow func(constant.Proxy)
	allow = func(proxy constant.Proxy) {
		if proxy == nil || visited[proxy.Name()] {
			return
		}
		if proxy.Type() == constant.Relay {
			return
		}
		visited[proxy.Name()] = true
		if nested, ok := adapterAs[outboundgroup.ProxyGroup](proxy); ok {
			for _, leaf := range nested.Proxies() {
				allow(leaf)
			}
		} else {
			allowed[proxy.Name()] = true
		}
	}
	for _, name := range b.spec.Sources {
		allow(lookupProxy(name))
	}
	for _, providerName := range b.spec.Providers {
		if pd := tunnel.ProvidersSnapshot()[providerName]; pd != nil {
			for _, proxy := range pd.Proxies() {
				allow(proxy)
			}
		}
	}
	var candidates []smart.Candidate
	saved := map[string]smart.Record{}
	if b.runtime != nil && b.runtime.history != nil {
		for _, kind := range []string{"mobile", "wifi"} {
			for name, record := range b.runtime.history.SavedPool(kind + ":" + smartHash(b.spec.poolName())) {
				saved[name] = record
			}
		}
	}
	for _, proxy := range b.group.Proxies() {
		if proxy.Type() < constant.Shadowsocks || isProxyGroupType(proxy.Type()) {
			continue
		}
		if (len(b.spec.Sources) > 0 || len(b.spec.Providers) > 0) && !allowed[proxy.Name()] {
			continue
		}
		b.nodes[proxy.Name()] = proxy
		identity := b.identity(proxy)
		key := smartHash(b.network + "|" + identity)
		b.countryKeys[proxy.Name()] = key
		if previousKeys[proxy.Name()] != key {
			delete(b.servicePassed, proxy.Name())
			delete(b.qualified, proxy.Name())
			delete(b.countries, proxy.Name())
		}
		smartTraffic.mu.Lock()
		cached := smartTraffic.Geo[key]
		smartTraffic.mu.Unlock()
		if cached.Country != "" {
			if b.countries == nil {
				b.countries = map[string]smartGeoEntry{}
			}
			b.countries[proxy.Name()] = cached
		}
		country := cached.Country
		if country == "" && saved[proxy.Name()].Fingerprint == identity {
			country = saved[proxy.Name()].Country
			if country != "" {
				if b.countries == nil {
					b.countries = map[string]smartGeoEntry{}
				}
				b.countries[proxy.Name()] = smartGeoEntry{Country: country, Checked: saved[proxy.Name()].Checked}
			}
		}
		candidates = append(candidates, smart.Candidate{ID: proxy.Name(), Fingerprint: identity, Country: country})
	}
	if b.runtime != nil && b.runtime.history != nil {
		for _, kind := range []string{"wifi", "mobile"} {
			b.runtime.history.Reconcile(kind+":"+smartHash(b.spec.poolName()), candidates)
		}
	}
	return candidates
}

func (b *smartBackend) Current() string {
	selectMu.Lock()
	defer selectMu.Unlock()
	return b.group.Now()
}

func (b *smartBackend) Latency(ctx context.Context, name string) (time.Duration, error) {
	ctx = context.WithValue(ctx, smartDiagnosticKey{}, true)
	proxy := b.node(name)
	if proxy == nil {
		return 0, smart.ErrUnavailable
	}
	probeURL := currentTestURL()
	expected := utils.IntRanges[uint16]{utils.NewRange[uint16](200, 299)}
	if parsed, err := url.Parse(probeURL); err == nil && strings.HasSuffix(parsed.Path, "generate_204") {
		expected = utils.IntRanges[uint16]{utils.NewRange[uint16](204, 204)}
	}
	delay, err := proxy.URLTest(ctx, probeURL, expected)
	if err == nil && !proxy.AliveForTestUrl(probeURL) {
		err = smart.ErrUnavailable
	}
	if b.runtime != nil && !errors.Is(ctx.Err(), context.Canceled) {
		b.runtime.mu.Lock()
		if b.generation != b.runtime.generation {
			b.runtime.mu.Unlock()
			return time.Duration(delay) * time.Millisecond, err
		}
		if err == nil {
			b.runtime.transportOK = time.Now()
			b.runtime.transportFailure = time.Time{}
			b.runtime.resetAttempt = time.Time{}
		} else if b.runtime.transportFailure.IsZero() {
			b.runtime.transportFailure = time.Now()
		}
		b.runtime.mu.Unlock()
	}
	return time.Duration(delay) * time.Millisecond, err
}

func (b *smartBackend) Select(ctx context.Context, name, reason string) error {
	smartReconnect.mu.Lock()
	selectMu.Lock()
	var obsolete []statistic.Tracker
	defer func() {
		selectMu.Unlock()
		smartReconnect.mu.Unlock()
		for _, connection := range obsolete {
			_ = connection.Close()
		}
	}()
	if err := ctx.Err(); err != nil {
		return err
	}
	if !isRunning.Load() {
		return context.Canceled
	}
	current, err := selectableGroup(b.spec.Name)
	if err != nil || current != b.group {
		return context.Canceled
	}
	if current.Now() != b.expected {
		smartReconnect.pauseLocked(b.spec.Name)
		return context.Canceled
	}
	if name == b.expected {
		return nil
	}
	if name != "REJECT" && b.node(name) == nil {
		return smart.ErrUnavailable
	}
	statistic.DefaultManager.Range(func(connection statistic.Tracker) bool {
		chain := connection.Info().Chain
		if slices.Contains(chain, b.spec.Name) && slices.Contains(chain, b.expected) {
			obsolete = append(obsolete, connection)
		}
		return true
	})
	if err = current.Set(name); err != nil {
		obsolete = nil
		return err
	}
	b.expected = name
	if b.runtime != nil {
		b.nodesMu.RLock()
		state := b.qualified[name]
		b.nodesMu.RUnlock()
		b.runtime.states[b.spec.Name] = state
	}
	refreshRouteLocked(routeUnchanged)
	log.Infoln("[SmartReconnect] switched (%s)", reason)
	appendSmartJournal("switched", reason)
	return nil
}

func (b *smartBackend) Speed(ctx context.Context, name string, limit int64) (float64, error) {
	ctx = context.WithValue(ctx, smartDiagnosticKey{}, true)
	if !smartSpeedSlots.acquireWithin(ctx, b.spec.Options.SpeedTimeout) {
		return 0, context.DeadlineExceeded
	}
	defer smartSpeedSlots.release()
	proxy := b.node(name)
	if proxy == nil {
		return 0, smart.ErrUnavailable
	}
	if b.runtime != nil {
		b.runtime.mu.Lock()
		settings := b.runtime.settings
		b.runtime.mu.Unlock()
		if !smartTraffic.canDownload(b.network, settings, limit) {
			appendSmartJournal("budget-exhausted", "")
			return 0, errors.New("speed test budget exhausted")
		}
	}
	tlsConfig, err := ca.GetTLSConfig(ca.Option{})
	if err != nil {
		return 0, err
	}
	dialer := &probeDialer{proxy: proxy}
	transport := &http.Transport{DialContext: dialer.dial, TLSClientConfig: tlsConfig, DisableCompression: true}
	defer transport.CloseIdleConnections()
	parsed, err := url.Parse(b.spec.SpeedURL)
	if err != nil {
		return 0, err
	}
	query := parsed.Query()
	query.Set("bytes", fmt.Sprint(limit))
	query.Set("nonce", fmt.Sprint(time.Now().UnixNano()))
	parsed.RawQuery = query.Encode()
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, parsed.String(), nil)
	if err != nil {
		return 0, err
	}
	request.Header.Set("Cache-Control", "no-cache")
	client := &http.Client{Transport: transport, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	start := time.Now()
	response, err := client.Do(request)
	if err != nil {
		return 0, err
	}
	defer response.Body.Close()
	if response.StatusCode != 200 || !strings.HasPrefix(strings.ToLower(response.Header.Get("Content-Type")), "application/octet-stream") {
		return 0, errors.New("invalid speed probe response")
	}
	n, err := io.CopyN(io.Discard, response.Body, limit)
	if b.runtime != nil {
		smartTraffic.add(b.network, n, false)
		b.runtime.save()
		appendSmartJournal("speed-measurement", fmt.Sprintf("node=%s payload-bytes=%d complete=%t", smartHash(name)[:12], n, err == nil && n == limit))
	}
	if err != nil || n != limit {
		return 0, errors.New("incomplete speed probe")
	}
	return float64(n) / time.Since(start).Seconds(), nil
}
