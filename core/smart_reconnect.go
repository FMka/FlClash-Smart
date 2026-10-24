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
	"github.com/metacubex/mihomo/common/utils"
	"github.com/metacubex/mihomo/component/ca"
	"github.com/metacubex/mihomo/config"
	"github.com/metacubex/mihomo/constant"
	"github.com/metacubex/mihomo/log"
	"github.com/metacubex/mihomo/tunnel/statistic"
	"go.yaml.in/yaml/v3"
)

type smartGroupSpec struct {
	Name     string
	Options  smart.Options
	SpeedURL string
}

func prepareSmartGroups(raw *config.RawConfig, specs *[]smartGroupSpec) error {
	defaultAssigned := false
	for _, group := range raw.ProxyGroup {
		value, exists := group["smart-reconnect"]
		kind, _ := group["type"].(string)
		if !exists && kind == "url-test" && !defaultAssigned {
			value, exists = true, true
		}
		if !exists || value == false {
			continue
		}
		spec := smartGroupSpec{Options: smart.DefaultOptions(), SpeedURL: "https://speed.cloudflare.com/__down"}
		spec.Name, _ = group["name"].(string)
		if kind != "select" && kind != "url-test" {
			return fmt.Errorf("smart-reconnect requires a select or url-test group")
		}
		defaultAssigned = true
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
		members := []string{"REJECT"}
		if list, ok := group["proxies"].([]any); ok {
			for _, member := range list {
				if name, ok := member.(string); ok && name != "DIRECT" && name != "REJECT" {
					members = append(members, name)
				}
			}
		} else if list, ok := group["proxies"].([]string); ok {
			for _, name := range list {
				if name != "DIRECT" && name != "REJECT" {
					members = append(members, name)
				}
			}
		}
		group["proxies"] = members
		if specs != nil {
			*specs = append(*specs, spec)
		}
	}
	return nil
}

type smartRuntime struct {
	mu          sync.Mutex
	saveMu      sync.Mutex
	specs       []smartGroupSpec
	workers     map[string]context.CancelFunc
	paused      map[string]bool
	history     *smart.History
	network     string
	configuring bool
}

var smartReconnect = &smartRuntime{history: &smart.History{}}
var smartSpeedSlots = make(slots, 1)

func (r *smartRuntime) cancelLocked() {
	for _, cancel := range r.workers {
		cancel()
	}
	r.workers = map[string]context.CancelFunc{}
}

func (r *smartRuntime) beginConfig() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.configuring = true
	r.cancelLocked()
}

func (r *smartRuntime) configure(specs []smartGroupSpec) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.cancelLocked()
	r.specs, r.paused, r.configuring = specs, map[string]bool{}, false
	if runtime.GOOS != "android" && r.network == "" {
		r.network = "desktop"
	}
	r.startLocked()
}

func (r *smartRuntime) stop() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.cancelLocked()
}

func (r *smartRuntime) resume() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.paused = map[string]bool{}
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
}

func (r *smartRuntime) startLocked() {
	if !isRunning.Load() || r.configuring || r.network == "" {
		return
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
		if err == nil {
			err = group.Set("REJECT")
		}
		selectMu.Unlock()
		if err != nil {
			log.Warnln("[SmartReconnect] group cannot be managed: %v", err)
			continue
		}
		ctx, cancel := context.WithCancel(context.Background())
		r.workers[spec.Name] = cancel
		backend := &smartBackend{spec: spec, group: group, expected: "REJECT"}
		engine := smart.New(spec.Options, r.history)
		engine.Status = func(phase string) {
			if phase == "unavailable" || phase == "internal-error" {
				log.Warnln("[SmartReconnect] %s", phase)
			} else {
				log.Infoln("[SmartReconnect] %s", phase)
			}
		}
		engine.Save = r.save
		network := smartHash(r.network) + ":" + smartHash(spec.Name)
		safeGo("smartReconnect", func() { engine.Run(ctx, network, backend) })
	}
}

func smartHash(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])
}

func (r *smartRuntime) load() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.cancelLocked()
	r.history = &smart.History{}
	data, err := os.ReadFile(filepath.Join(constant.Path.HomeDir(), "smart-history.json"))
	if err == nil && len(data) <= 1024*1024 {
		_ = json.Unmarshal(data, r.history)
	}
}

func (r *smartRuntime) save() {
	r.saveMu.Lock()
	defer r.saveMu.Unlock()
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
	spec     smartGroupSpec
	group    pickableGroup
	expected string
	nodes    map[string]constant.Proxy
}

func (b *smartBackend) Candidates() []smart.Candidate {
	selectMu.Lock()
	defer selectMu.Unlock()
	current, err := selectableGroup(b.spec.Name)
	if err != nil || current != b.group {
		return nil
	}
	b.nodes = map[string]constant.Proxy{}
	var candidates []smart.Candidate
	for _, proxy := range b.group.Proxies() {
		if proxy.Type() < constant.Shadowsocks || isProxyGroupType(proxy.Type()) {
			continue
		}
		b.nodes[proxy.Name()] = proxy
		candidates = append(candidates, smart.Candidate{ID: proxy.Name(), Fingerprint: smartHash(proxy.Name() + "|" + proxy.Addr() + "|" + proxy.Type().String())})
	}
	return candidates
}

func (b *smartBackend) Current() string {
	selectMu.Lock()
	defer selectMu.Unlock()
	return b.group.Now()
}

func (b *smartBackend) Latency(ctx context.Context, name string) (time.Duration, error) {
	proxy := b.nodes[name]
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
	if b.nodes[name] == nil {
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
	refreshRouteLocked(false)
	log.Infoln("[SmartReconnect] switched (%s)", reason)
	return nil
}

func (b *smartBackend) Speed(ctx context.Context, name string, limit int64) (float64, error) {
	if !smartSpeedSlots.acquireWithin(ctx, b.spec.Options.SpeedTimeout) {
		return 0, context.DeadlineExceeded
	}
	defer smartSpeedSlots.release()
	proxy := b.nodes[name]
	if proxy == nil {
		return 0, smart.ErrUnavailable
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
	if err != nil || n != limit {
		return 0, errors.New("incomplete speed probe")
	}
	return float64(n) / time.Since(start).Seconds(), nil
}
