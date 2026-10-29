package core

import (
	"context"
	"core/smart"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/metacubex/http"
	"github.com/metacubex/mihomo/adapter"
	"github.com/metacubex/mihomo/config"
	"github.com/metacubex/mihomo/constant"
	"go.yaml.in/yaml/v3"
)

type smartImportParams struct {
	Operation string       `json:"operation"`
	ProfileID string       `json:"profile-id"`
	ScanID    string       `json:"scan-id"`
	Params    *SetupParams `json:"params"`
	Config    string       `json:"config"`
}

type smartImportStatus struct {
	ProfileID   string   `json:"profile-id"`
	ScanID      string   `json:"scan-id"`
	Phase       string   `json:"phase"`
	Total       int      `json:"total"`
	Checked     int      `json:"checked"`
	Responded   int      `json:"responded"`
	Unsupported int      `json:"unsupported"`
	Countries   []string `json:"countries"`
	Unresolved  int      `json:"unresolved"`
	Reason      string   `json:"reason,omitempty"`
}

type smartImportJob struct {
	status  smartImportStatus
	cancel  context.CancelFunc
	started time.Time
}

type smartImportManager struct {
	mu   sync.Mutex
	jobs map[string]*smartImportJob
	next atomic.Uint64
}

var smartImports = &smartImportManager{}

func (m *smartImportManager) cancelAll() {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, job := range m.jobs {
		if job.status.Phase == "scanning" {
			job.cancel()
			job.status.Phase, job.status.Reason = "cancelled", "lifecycle-changed"
		}
	}
}

func (m *smartImportManager) snapshot() []smartImportStatus {
	m.mu.Lock()
	defer m.mu.Unlock()
	items := make([]smartImportStatus, 0, len(m.jobs))
	for _, job := range m.jobs {
		item := job.status
		item.Countries = slices.Clone(item.Countries)
		items = append(items, item)
	}
	slices.SortFunc(items, func(a, b smartImportStatus) int { return strings.Compare(a.ProfileID, b.ProfileID) })
	return items
}

func handleScanSmartProfile(params smartImportParams) (smartImportStatus, error) {
	if params.ProfileID == "" {
		return smartImportStatus{}, errors.New("profile-id is required")
	}
	m := smartImports
	if params.Operation == "status" || params.Operation == "cancel" {
		m.mu.Lock()
		defer m.mu.Unlock()
		job := m.jobs[params.ProfileID]
		if job == nil || params.ScanID == "" || job.status.ScanID != params.ScanID {
			return smartImportStatus{}, errors.New("scan is no longer current")
		}
		if params.Operation == "cancel" && job.status.Phase == "scanning" {
			job.cancel()
			job.status.Phase, job.status.Reason = "cancelled", "user-cancelled"
		}
		result := job.status
		result.Countries = slices.Clone(result.Countries)
		return result, nil
	}
	if params.Operation != "" && params.Operation != "start" {
		return smartImportStatus{}, errors.New("invalid scan operation")
	}
	if params.Config == "" && (params.Params == nil || params.Params.Base == nil) {
		return smartImportStatus{}, errors.New("profile base or config is required")
	}
	r := smartReconnect
	r.mu.Lock()
	network := r.network
	if network == "" {
		r.mu.Unlock()
		return smartImportStatus{}, errors.New("network unavailable")
	}
	if r.history == nil {
		r.history = &smart.History{}
	}
	history := r.history
	m.cancelAll()
	ctx, cancel := context.WithCancel(context.Background())
	job := &smartImportJob{cancel: cancel, started: time.Now(), status: smartImportStatus{ProfileID: params.ProfileID, ScanID: strconv.FormatUint(m.next.Add(1), 10), Phase: "scanning", Countries: []string{}}}
	m.mu.Lock()
	if m.jobs == nil {
		m.jobs = map[string]*smartImportJob{}
	}
	if len(m.jobs) >= 32 {
		for id, existing := range m.jobs {
			if existing.status.Phase != "scanning" {
				delete(m.jobs, id)
				break
			}
		}
	}
	m.jobs[params.ProfileID] = job
	result := job.status
	m.mu.Unlock()
	r.mu.Unlock()
	safeGo("smartProfileScan", func() {
		defer cancel()
		m.run(ctx, job, params, network, history)
	})
	return result, nil
}

func smartImportDocument(params smartImportParams) (*config.RawConfig, error) {
	if params.Config != "" {
		if len(params.Config) > 64*1024*1024 {
			return nil, errors.New("profile exceeds scan limit")
		}
		return unmarshalRawConfig([]byte(params.Config))
	}
	if info, err := os.Stat(params.Params.Base.Path); err != nil || info.Size() > 64*1024*1024 {
		return nil, errors.New("profile unavailable or exceeds scan limit")
	}
	document, err := mergeProfileBase(params.Params.Base)
	if err != nil {
		return nil, err
	}
	raw := config.DefaultRawConfig()
	if err = document.Decode(raw); err != nil {
		return nil, err
	}
	return raw, nil
}

type smartImportNode struct {
	proxy    constant.Proxy
	identity string
	groups   []string
}

func smartImportNodes(raw *config.RawConfig, settings smartSettings) (map[string]*smartImportNode, int, error) {
	var specs []smartGroupSpec
	settings.Enabled = true
	if err := prepareSmartGroups(raw, &specs, settings); err != nil {
		return nil, 0, err
	}
	nodes, mappings := map[string]*smartImportNode{}, map[string]map[string]any{}
	providerIDs, memberships := map[string]string{}, map[string][]string{}
	for _, mapping := range raw.Proxy {
		if name, _ := mapping["name"].(string); name != "" {
			mappings[name] = mapping
		}
	}
	unsupported := 0
	for name, definition := range raw.ProxyProvider {
		if smartImportTransforms(definition) {
			unsupported++
			continue
		}
		kind, _ := definition["type"].(string)
		path, _ := definition["path"].(string)
		if kind == "http" && path == "" {
			url, _ := definition["url"].(string)
			path = constant.Path.GetPathByHash("proxies", url)
		} else if !filepath.IsAbs(path) {
			path = filepath.Join(constant.Path.HomeDir(), path)
		}
		var data []byte
		var err error
		if kind == "inline" {
			data, err = yaml.Marshal(map[string]any{"proxies": definition["payload"]})
		} else if kind == "file" || kind == "http" {
			data, err = readSmartImportProvider(path)
		} else {
			err = errors.New("unsupported provider type")
		}
		if err != nil {
			unsupported++
			continue
		}
		var document struct {
			Proxies []map[string]any `yaml:"proxies"`
		}
		if yaml.Unmarshal(data, &document) != nil {
			unsupported++
			continue
		}
		encoded, _ := yaml.Marshal(definition)
		definitionID := smartHash(string(encoded))
		identities := smartProviderNodeIdentities(data, definitionID)
		for _, mapping := range document.Proxies {
			if nodeName, _ := mapping["name"].(string); nodeName != "" {
				if _, declared := mappings[nodeName]; !declared {
					mappings[nodeName], providerIDs[nodeName] = mapping, identities[nodeName]
					if kind == "inline" {
						providerIDs[nodeName] = definitionID
					}
				}
				memberships[name] = append(memberships[name], nodeName)
			}
		}
	}
	groups := map[string]map[string]any{}
	for _, group := range raw.ProxyGroup {
		name, _ := group["name"].(string)
		groups[name] = group
	}
	for _, spec := range specs {
		group := groups[spec.Name]
		if smartImportTransforms(group) {
			unsupported++
			continue
		}
		names := smartStrings(group["proxies"])
		for _, name := range smartStrings(group["use"]) {
			names = append(names, memberships[name]...)
		}
		all, _ := group["include-all"].(bool)
		if providers, _ := group["include-all-providers"].(bool); all || providers {
			for _, members := range memberships {
				names = append(names, members...)
			}
		}
		if proxies, _ := group["include-all-proxies"].(bool); all || proxies {
			for name := range mappings {
				names = append(names, name)
			}
		}
		for _, name := range uniqueSmartStrings(names) {
			mapping := mappings[name]
			if mapping == nil {
				continue
			}
			if node := nodes[name]; node != nil {
				node.groups = append(node.groups, spec.poolName())
				continue
			}
			kind, _ := mapping["type"].(string)
			if mapping["dialer-proxy"] != nil || slices.Contains([]string{"tailscale", "easytier", "zerotier", "openvpn", "direct", "reject", "dns", "rematch"}, kind) {
				unsupported++
				continue
			}
			proxy, err := adapter.ParseProxy(mapping)
			if err != nil {
				unsupported++
				continue
			}
			encoded, _ := yaml.Marshal(mapping)
			identity := smartHash(string(encoded))
			if providerIDs[name] != "" {
				identity = smartHash(name + "|" + proxy.Addr() + "|" + proxy.Type().String() + "|" + providerIDs[name])
			}
			nodes[name] = &smartImportNode{proxy: proxy, identity: identity, groups: []string{spec.poolName()}}
		}
	}
	return nodes, unsupported, nil
}

func smartImportTransforms(mapping map[string]any) bool {
	for _, key := range []string{"override", "filter", "exclude-filter", "exclude-type", "dialer-proxy", "age-secret-key"} {
		if value := mapping[key]; value != nil {
			switch typed := value.(type) {
			case string:
				if strings.TrimSpace(typed) == "" {
					continue
				}
			case map[string]any:
				if len(typed) == 0 {
					continue
				}
			}
			return true
		}
	}
	return false
}

func readSmartImportProvider(path string) ([]byte, error) {
	info, err := os.Stat(path)
	if err != nil || info.IsDir() || info.Size() > 64*1024*1024 {
		return nil, errors.New("provider cache unavailable")
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, 64*1024*1024+1))
	if len(data) > 64*1024*1024 {
		return nil, errors.New("provider exceeds scan limit")
	}
	return data, err
}

func (m *smartImportManager) run(ctx context.Context, job *smartImportJob, params smartImportParams, network string, history *smart.History) {
	started := job.started
	if started.IsZero() {
		started = time.Now()
	}
	var runErr error
	defer func() {
		if recovered := recover(); recovered != nil {
			runErr = fmt.Errorf("scan failed: %v", recovered)
		}
		m.mu.Lock()
		if job.status.Phase == "scanning" {
			job.status.Phase = "complete"
			if runErr != nil {
				job.status.Phase, job.status.Reason = "failed", runErr.Error()
			}
			if ctx.Err() != nil {
				job.status.Phase, job.status.Reason = "cancelled", ctx.Err().Error()
			}
			if job.status.Phase == "complete" && (job.status.Checked < job.status.Total || job.status.Unsupported > 0) {
				job.status.Phase = "partial"
			}
		}
		m.mu.Unlock()
		smartReconnect.save()
	}()
	if ctx.Err() != nil {
		return
	}
	raw, err := smartImportDocument(params)
	if err != nil {
		runErr = err
		return
	}
	settingsData := params.Config
	if settingsData == "" {
		settingsData = params.Params.Base.Overlay
	}
	settings, err := decodeSmartSettings([]byte(settingsData))
	if err != nil {
		runErr = err
		return
	}
	if settings.ProfileID == "" {
		settings.ProfileID = params.ProfileID
	}
	nodes, unsupported, err := smartImportNodes(raw, settings)
	if err != nil {
		runErr = err
		return
	}
	defer func() {
		for _, node := range nodes {
			_ = node.proxy.Close()
		}
	}()
	if ctx.Err() != nil {
		return
	}
	budget, concurrency, maxChecks := 45*time.Second, 4, 256
	if smartMobileNetwork(network) {
		budget, concurrency, maxChecks = 24*time.Second, 2, 64
	}
	scanCtx, cancel := context.WithTimeout(ctx, budget)
	defer cancel()
	b := &smartBackend{isolated: true, network: network, nodes: map[string]constant.Proxy{}, spec: smartGroupSpec{Identities: map[string]string{}}}
	names := make([]string, 0, len(nodes))
	checks := map[string]time.Time{}
	groupHistory := map[string]map[string]smart.Record{}
	for name, node := range nodes {
		b.nodes[name] = node.proxy
		b.spec.Identities[name] = node.identity
		names = append(names, name)
		for _, group := range node.groups {
			if groupHistory[group] == nil {
				groupHistory[group] = history.Snapshot(smartPoolKey(network, group))
			}
			if record := groupHistory[group][name]; record.Fingerprint == node.identity && record.Checked.After(checks[name]) {
				checks[name] = record.Checked
			}
		}
	}
	sort.Slice(names, func(i, j int) bool {
		if checks[names[i]].Equal(checks[names[j]]) {
			return names[i] < names[j]
		}
		return checks[names[i]].Before(checks[names[j]])
	})
	m.mu.Lock()
	job.status.Total, job.status.Unsupported, job.status.Unresolved = len(nodes), unsupported, len(nodes)
	m.mu.Unlock()
	queue := make(chan string)
	testURL := defaultTestURL
	if params.Params != nil && params.Params.TestURL != "" {
		testURL = params.Params.TestURL
	}
	var workers sync.WaitGroup
	for range concurrency {
		workers.Add(1)
		safeGo("smartImportProbe", func() {
			defer workers.Done()
			for name := range queue {
				if scanCtx.Err() != nil {
					return
				}
				node := nodes[name]
				result := runProbe(scanCtx, probeRequest{method: http.MethodHead, url: testURL, proxyName: name, proxy: node.proxy, timeout: 3 * time.Second, noRedirect: true})
				var probeErr error
				delay := int64(0)
				if result == nil {
					probeErr = errors.New("probe cancelled")
				} else if result.Error != "" || result.StatusCode < 200 || result.StatusCode > 299 {
					probeErr = errors.New("probe unavailable")
				} else {
					delay = max(1, result.Delay)
				}
				m.mu.Lock()
				valid := scanCtx.Err() == nil && job.status.Phase == "scanning"
				candidate := smart.Candidate{ID: name, Fingerprint: node.identity}
				if valid {
					job.status.Checked++
					for _, group := range node.groups {
						key := smartPoolKey(network, group)
						history.ObserveIfCurrent(key, candidate, time.Duration(delay)*time.Millisecond, probeErr, started)
					}
					if probeErr == nil {
						job.status.Responded++
					}
				}
				m.mu.Unlock()
				if valid && probeErr == nil {
					country, _ := b.country(scanCtx, name)
					m.mu.Lock()
					if country != "" && scanCtx.Err() == nil && job.status.Phase == "scanning" {
						candidate.Country = country
						for _, group := range node.groups {
							history.ObserveIfCurrent(smartPoolKey(network, group), candidate, time.Duration(delay)*time.Millisecond, nil, started)
						}
						job.status.Unresolved--
						if !slices.Contains(job.status.Countries, country) {
							job.status.Countries = append(job.status.Countries, country)
							slices.Sort(job.status.Countries)
						}
					}
					m.mu.Unlock()
				}
			}
		})
	}
	for _, name := range names[:min(len(names), maxChecks)] {
		select {
		case queue <- name:
		case <-scanCtx.Done():
			break
		}
		if scanCtx.Err() != nil {
			break
		}
	}
	close(queue)
	workers.Wait()
	if len(nodes) == 0 {
		runErr = fmt.Errorf("no independently scannable proxy nodes (%d unsupported)", unsupported)
	}
}
