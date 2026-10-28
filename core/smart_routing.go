package core

import (
	"bytes"
	"context"
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/metacubex/http"
	"github.com/metacubex/mihomo/component/ca"
	"github.com/metacubex/mihomo/config"
	"github.com/metacubex/mihomo/constant"
	"github.com/metacubex/mihomo/tunnel"
	"go.yaml.in/yaml/v3"
)

const smartRoutingURL = "https://raw.githubusercontent.com/MetaCubeX/meta-rules-dat/meta/geo/geosite/category-ru.list"
const smartRoutingMaxBytes = 256 * 1024

//go:embed resources/smart-routing/*
var smartRoutingResources embed.FS

var bundledSmartRouting, _ = smartRoutingResources.ReadFile("resources/smart-routing/category-ru.list")

type smartRoutingInfo struct {
	Source    string `json:"source"`
	Version   string `json:"version"`
	UpdatedAt string `json:"updated-at"`
	Error     string `json:"error"`
}

var smartRouting struct {
	sync.Mutex
	info        smartRoutingInfo
	group       string
	provider    string
	path        string
	generation  uint64
	cancel      context.CancelFunc
	nextAttempt time.Time
}

func smartRoutingSnapshot() smartRoutingInfo {
	smartRouting.Lock()
	defer smartRouting.Unlock()
	return smartRouting.info
}

func smartRoutingSource(data []byte) string {
	var header struct {
		Source string `yaml:"smart-routing-source"`
	}
	_ = yaml.Unmarshal(data, &header)
	return header.Source
}

func prepareSmartRouting(raw *config.RawConfig, documents ...[]byte) error {
	source := "subscription"
	if len(documents) > 0 {
		if value := smartRoutingSource(documents[0]); value != "" {
			source = value
		}
	}
	if source == "builtin-ru" {
		for name, provider := range raw.RuleProvider {
			if strings.HasPrefix(name, "__flclash_smart_ru") {
				path := filepath.Join(constant.Path.HomeDir(), "smart-routing", "category-ru.list")
				provider["path"] = path
				body, updated, err := smartRoutingData(path)
				if err != nil {
					return err
				}
				setSmartRoutingInfo("builtin-ru", body, updated, primarySmartGroup(raw), name, path)
				return nil
			}
		}
	}
	if len(raw.Rule) > 0 && source != "none" {
		if source != "custom" {
			source = "subscription"
		}
		setSmartRoutingInfo(source, nil, time.Time{}, "", "", "")
		return nil
	}
	for _, rule := range raw.Rule {
		if isMatchRule(rule) {
			setSmartRoutingInfo("custom", nil, time.Time{}, "", "", "")
			return nil
		}
	}
	if len(raw.Proxy) == 0 && len(raw.ProxyProvider) == 0 && len(raw.ProxyGroup) == 0 {
		setSmartRoutingInfo("none", nil, time.Time{}, "", "", "")
		return nil
	}
	group := primarySmartGroup(raw)
	if group == "" {
		for _, entry := range raw.ProxyGroup {
			if entry["type"] == "select" || entry["type"] == "url-test" {
				group, _ = entry["name"].(string)
				if group != "" {
					break
				}
			}
		}
	}
	if group == "" {
		names := []string{}
		used := map[string]bool{"DIRECT": true, "REJECT": true, "GLOBAL": true}
		for _, proxy := range raw.Proxy {
			if name, ok := proxy["name"].(string); ok && name != "" {
				names = append(names, name)
				used[name] = true
			}
		}
		for _, entry := range raw.ProxyGroup {
			name, _ := entry["name"].(string)
			used[name] = true
		}
		group = "FlClash Smart"
		for index := 2; used[group]; index++ {
			group = fmt.Sprintf("FlClash Smart %d", index)
		}
		providers := []string{}
		for name := range raw.ProxyProvider {
			providers = append(providers, name)
		}
		slices.Sort(providers)
		entry := map[string]any{"name": group, "type": "select", "proxies": names}
		if len(providers) > 0 {
			entry["use"] = providers
		}
		raw.ProxyGroup = append(raw.ProxyGroup, entry)
	}
	name := "__flclash_smart_ru"
	for index := 2; raw.RuleProvider[name] != nil; index++ {
		name = fmt.Sprintf("__flclash_smart_ru_%d", index)
	}
	path := filepath.Join(constant.Path.HomeDir(), "smart-routing", "category-ru.list")
	body, updated, err := smartRoutingData(path)
	if err != nil {
		return err
	}
	if raw.RuleProvider == nil {
		raw.RuleProvider = map[string]map[string]any{}
	}
	raw.RuleProvider[name] = map[string]any{"type": "file", "behavior": "domain", "format": "text", "path": path}
	raw.Rule = append(raw.Rule, "RULE-SET,"+name+",DIRECT", "MATCH,"+group)
	setSmartRoutingInfo("builtin-ru", body, updated, group, name, path)
	return nil
}

func validateSmartRouting(data []byte) ([]byte, error) {
	if len(data) == 0 || len(data) > smartRoutingMaxBytes {
		return nil, errors.New("invalid Russian bypass list size")
	}
	seen := map[string]bool{}
	lines := []string{}
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		domain := strings.TrimPrefix(line, "+.")
		if len(domain) > 253 || strings.HasPrefix(domain, ".") || strings.HasSuffix(domain, ".") {
			return nil, errors.New("invalid Russian bypass domain")
		}
		for _, label := range strings.Split(domain, ".") {
			if label == "" || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
				return nil, errors.New("invalid Russian bypass domain label")
			}
			for _, char := range label {
				if !(char >= 'a' && char <= 'z' || char >= '0' && char <= '9' || char == '-') {
					return nil, errors.New("invalid Russian bypass domain character")
				}
			}
		}
		if !seen[line] {
			seen[line] = true
			lines = append(lines, line)
		}
	}
	if len(lines) < 100 || !seen["+.ru"] || !seen["+.vk.com"] || !seen["+.sberbank.com"] {
		return nil, errors.New("incomplete Russian bypass list")
	}
	return []byte(strings.Join(lines, "\n") + "\n"), nil
}

func smartRoutingData(path string) ([]byte, time.Time, error) {
	if data, err := os.ReadFile(path); err == nil {
		if valid, validationErr := validateSmartRouting(data); validationErr == nil {
			info, statErr := os.Stat(path)
			if statErr == nil {
				return valid, info.ModTime(), nil
			}
		}
	}
	body, err := validateSmartRouting(bundledSmartRouting)
	if err != nil {
		return nil, time.Time{}, err
	}
	if err = writeSmartRoutingFile(path, body); err != nil {
		return nil, time.Time{}, err
	}
	updated := time.Date(2026, 10, 28, 0, 0, 0, 0, time.UTC)
	_ = os.Chtimes(path, updated, updated)
	return body, updated, nil
}

func writeSmartRoutingFile(path string, body []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	file, err := os.CreateTemp(filepath.Dir(path), ".rules-*")
	if err != nil {
		return err
	}
	defer os.Remove(file.Name())
	_, writeErr := file.Write(body)
	if err = errors.Join(writeErr, file.Close()); err != nil {
		return err
	}
	return os.Rename(file.Name(), path)
}

func setSmartRoutingInfo(source string, data []byte, updated time.Time, group, provider, path string) {
	smartRouting.Lock()
	defer smartRouting.Unlock()
	if smartRouting.cancel != nil {
		smartRouting.cancel()
	}
	smartRouting.cancel = nil
	smartRouting.generation++
	smartRouting.group, smartRouting.provider, smartRouting.path = group, provider, path
	smartRouting.info = smartRoutingInfo{Source: source}
	if len(data) > 0 {
		digest := sha256.Sum256(data)
		smartRouting.info.Version = hex.EncodeToString(digest[:])[:12]
		smartRouting.info.UpdatedAt = updated.UTC().Format(time.RFC3339)
		if time.Since(updated) < 24*time.Hour {
			smartRouting.nextAttempt = updated.Add(24 * time.Hour)
		}
	}
}

func setSmartRoutingRunning(running bool) {
	smartRouting.Lock()
	if !running {
		smartRouting.generation++
		if smartRouting.cancel != nil {
			smartRouting.cancel()
			smartRouting.cancel = nil
		}
		smartRouting.Unlock()
		return
	}
	if smartRouting.info.Source != "builtin-ru" || smartRouting.cancel != nil || time.Now().Before(smartRouting.nextAttempt) {
		smartRouting.Unlock()
		return
	}
	generation, group, path, provider := smartRouting.generation, smartRouting.group, smartRouting.path, smartRouting.provider
	smartRouting.Unlock()
	if !smartRoutingRouteReady(group) || !smartBeginDiagnostic("rules") {
		return
	}
	smartRouting.Lock()
	if !isRunning.Load() || generation != smartRouting.generation || smartRouting.cancel != nil || time.Now().Before(smartRouting.nextAttempt) {
		smartRouting.Unlock()
		smartEndDiagnostic("rules")
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	smartRouting.cancel = cancel
	smartRouting.nextAttempt = time.Now().Add(6 * time.Hour)
	smartRouting.Unlock()
	safeGo("smart-routing-update", func() {
		defer smartEndDiagnostic("rules")
		defer cancel()
		body, err := fetchSmartRouting(ctx, group)
		if err == nil {
			body, err = validateSmartRouting(body)
		}
		configMu.Lock()
		defer configMu.Unlock()
		smartRouting.Lock()
		defer smartRouting.Unlock()
		if generation == smartRouting.generation {
			smartRouting.cancel = nil
		}
		if ctx.Err() != nil || !isRunning.Load() || generation != smartRouting.generation {
			return
		}
		if err == nil {
			previous, _ := os.ReadFile(path)
			err = writeSmartRoutingFile(path, body)
			if err == nil {
				if active := tunnel.RuleProvidersSnapshot()[provider]; active != nil {
					err = active.Update()
				}
				if err != nil && len(previous) > 0 {
					_ = writeSmartRoutingFile(path, previous)
				}
			}
		}
		if err != nil {
			smartRouting.info.Error = "update-failed"
			return
		}
		digest := sha256.Sum256(body)
		smartRouting.info.Version = hex.EncodeToString(digest[:])[:12]
		smartRouting.info.UpdatedAt = time.Now().UTC().Format(time.RFC3339)
		smartRouting.info.Error = ""
		smartRouting.nextAttempt = time.Now().Add(24 * time.Hour)
	})
}

var smartRoutingRouteReady = func(group string) bool {
	selectMu.Lock()
	defer selectMu.Unlock()
	selected, err := selectableGroup(group)
	if err != nil {
		return false
	}
	name := selected.Now()
	return name != "" && name != "REJECT" && !strings.HasPrefix(name, "COMPATIBLE")
}

var fetchSmartRouting = func(ctx context.Context, group string) ([]byte, error) {
	proxy := tunnel.AllProxies()[group]
	if proxy == nil {
		return nil, errors.New("no managed route for rule update")
	}
	tlsConfig, err := ca.GetTLSConfig(ca.Option{})
	if err != nil {
		return nil, err
	}
	dialer := &probeDialer{proxy: proxy}
	transport := &http.Transport{DialContext: dialer.dial, TLSClientConfig: tlsConfig, DisableCompression: true}
	defer transport.CloseIdleConnections()
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, smartRoutingURL, nil)
	if err != nil {
		return nil, err
	}
	client := &http.Client{Transport: transport, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	response, err := client.Do(request)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return nil, errors.New("Russian bypass update rejected")
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, smartRoutingMaxBytes+1))
	smartTraffic.add("", int64(len(body)), true)
	if bytes.Contains(body, []byte("<html")) {
		return nil, errors.New("Russian bypass update returned HTML")
	}
	return body, err
}
