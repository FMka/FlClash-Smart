package core

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/metacubex/mihomo/constant"
)

type smartGroupStatus struct {
	Name        string          `json:"name"`
	Mode        string          `json:"mode"`
	Current     string          `json:"current"`
	Phase       string          `json:"phase"`
	Country     string          `json:"country"`
	Reason      string          `json:"reason"`
	LastChecked int64           `json:"last-checked"`
	Speed       float64         `json:"last-speed-bytes-per-second"`
	SpeedAt     int64           `json:"speed-at"`
	SpeedSource string          `json:"speed-source"`
	Pool        []smartPoolItem `json:"pool"`
}

type smartPoolItem struct {
	Name      string  `json:"name"`
	Checked   int64   `json:"checked"`
	LatencyMS int64   `json:"latency-ms"`
	Speed     float64 `json:"speed-bytes-per-second"`
	SpeedAt   int64   `json:"speed-at"`
}

type smartStatus struct {
	Enabled      bool               `json:"enabled"`
	Phase        string             `json:"phase"`
	Network      string             `json:"network"`
	Groups       []smartGroupStatus `json:"groups"`
	MobileUsed   int64              `json:"mobile-used-bytes"`
	WiFiUsed     int64              `json:"wifi-used-bytes"`
	MobileBudget int64              `json:"mobile-budget-bytes"`
	WiFiBudget   int64              `json:"wifi-budget-bytes"`
	Diagnostic   int64              `json:"diagnostic-used-bytes"`
}

type smartGeoEntry struct {
	Country string    `json:"country"`
	Exit    string    `json:"exit"`
	Checked time.Time `json:"checked"`
}
type smartTrafficState struct {
	mu         sync.Mutex
	Day        string                   `json:"day"`
	Mobile     int64                    `json:"mobile"`
	WiFi       int64                    `json:"wifi"`
	Diagnostic int64                    `json:"diagnostic"`
	Geo        map[string]smartGeoEntry `json:"geo"`
}

var smartTraffic = &smartTrafficState{}
var smartJournalMu sync.Mutex
var networkResetMu sync.Mutex

func smartMobileNetwork(network string) bool {
	network = strings.ToLower(network)
	return !strings.HasPrefix(network, "wifi") && !strings.HasPrefix(network, "desktop") && !strings.HasPrefix(network, "ethernet")
}

func (s *smartTrafficState) rolloverLocked() {
	day := time.Now().UTC().Format("2006-01-02")
	if day != s.Day {
		s.Day = day
		s.Mobile = 0
		s.WiFi = 0
		s.Diagnostic = 0
	}
}

func (s *smartTrafficState) canDownload(network string, settings smartSettings, amount int64) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.rolloverLocked()
	if amount <= 0 {
		return false
	}
	if smartMobileNetwork(network) {
		return s.Mobile+amount <= settings.MobileBudgetMiB*1024*1024
	}
	return s.WiFi+amount <= settings.WiFiBudgetMiB*1024*1024
}

func (s *smartTrafficState) add(network string, bytes int64, diagnostic bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.rolloverLocked()
	if diagnostic {
		s.Diagnostic += bytes
	} else if smartMobileNetwork(network) {
		s.Mobile += bytes
	} else {
		s.WiFi += bytes
	}
}

func loadSmartMetadata() {
	smartTraffic.mu.Lock()
	defer smartTraffic.mu.Unlock()
	data, err := os.ReadFile(filepath.Join(constant.Path.HomeDir(), "smart-metadata.json"))
	if err == nil && len(data) <= 512*1024 {
		_ = json.Unmarshal(data, smartTraffic)
	}
	if smartTraffic.Geo == nil {
		smartTraffic.Geo = map[string]smartGeoEntry{}
	}
	smartTraffic.rolloverLocked()
}

func saveSmartMetadata() {
	smartTraffic.mu.Lock()
	defer smartTraffic.mu.Unlock()
	data, err := json.Marshal(smartTraffic)
	if err != nil {
		return
	}
	path := filepath.Join(constant.Path.HomeDir(), "smart-metadata.json")
	if os.WriteFile(path+".tmp", data, 0600) == nil {
		_ = os.Rename(path+".tmp", path)
	}
}

func appendSmartJournal(phase, reason string) {
	smartJournalMu.Lock()
	defer smartJournalMu.Unlock()
	path := filepath.Join(constant.Path.HomeDir(), "smart-journal.log")
	if info, err := os.Stat(path); err == nil && info.Size() > 400*1024 {
		data, _ := os.ReadFile(path)
		_ = os.WriteFile(path+".previous", data, 0600)
		_ = os.WriteFile(path, nil, 0600)
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0600)
	if err != nil {
		return
	}
	defer file.Close()
	entry, _ := json.Marshal(map[string]any{"time": time.Now().UTC().Format(time.RFC3339), "phase": phase, "reason": reason})
	_, _ = file.Write(append(entry, '\n'))
}

func handleGetSmartJournal() string {
	smartJournalMu.Lock()
	defer smartJournalMu.Unlock()
	path := filepath.Join(constant.Path.HomeDir(), "smart-journal.log")
	older, _ := os.ReadFile(path + ".previous")
	current, _ := os.ReadFile(path)
	return string(older) + string(current)
}

func handleGetSmartStatus() smartStatus {
	r := smartReconnect
	r.mu.Lock()
	defer r.mu.Unlock()
	status := smartStatus{Enabled: r.settings.Enabled, Phase: "idle", Groups: []smartGroupStatus{}, Network: "wifi", MobileBudget: r.settings.MobileBudgetMiB * 1024 * 1024, WiFiBudget: r.settings.WiFiBudgetMiB * 1024 * 1024}
	if smartMobileNetwork(r.network) {
		status.Network = "mobile"
	}
	if r.network == "" {
		status.Network = "none"
		status.Phase = "network-unavailable"
	}
	if !isRunning.Load() {
		status.Phase = "stopped"
	}
	if !r.settings.Enabled {
		status.Phase = "disabled"
	}
	selectMu.Lock()
	for _, spec := range r.specs {
		item := r.states[spec.Name]
		item.Name = spec.Name
		item.Mode = "auto"
		item.Phase = r.phases[spec.Name]
		if group, err := selectableGroup(spec.Name); err == nil {
			item.Current = group.Now()
		}
		if r.history != nil {
			key := smartHash(r.network) + ":" + smartHash(spec.Name)
			record := r.history.Snapshot(key)[item.Current]
			item.Speed = record.BytesPerSec
			if !record.Checked.IsZero() {
				item.LastChecked = record.Checked.UnixMilli()
			}
			if !record.SpeedAt.IsZero() {
				item.SpeedAt = record.SpeedAt.UnixMilli()
				item.SpeedSource = "cached"
			}
			item.Pool = []smartPoolItem{}
			for name, record := range r.history.ActivePool(key, 20) {
				poolItem := smartPoolItem{Name: name, Checked: record.Checked.UnixMilli(), LatencyMS: record.Latency.Milliseconds(), Speed: record.BytesPerSec}
				if !record.SpeedAt.IsZero() {
					poolItem.SpeedAt = record.SpeedAt.UnixMilli()
				}
				item.Pool = append(item.Pool, poolItem)
			}
		}
		if r.paused[spec.Name] {
			item.Mode = "manual"
			item.Phase = "manual"
		}
		if isRunning.Load() && r.settings.Enabled && item.Phase != "" {
			status.Phase = item.Phase
		}
		status.Groups = append(status.Groups, item)
	}
	selectMu.Unlock()
	smartTraffic.mu.Lock()
	smartTraffic.rolloverLocked()
	status.MobileUsed = smartTraffic.Mobile
	status.WiFiUsed = smartTraffic.WiFi
	status.Diagnostic = smartTraffic.Diagnostic
	smartTraffic.mu.Unlock()
	return status
}

func refreshRouteLockedSafe() { selectMu.Lock(); defer selectMu.Unlock(); refreshRouteLocked(false) }

func SmartRecheck() bool {
	defer recoverExport("smartRecheck")
	r := smartReconnect
	r.mu.Lock()
	defer r.mu.Unlock()
	if !isRunning.Load() || !r.settings.Enabled || r.configuring || r.network == "" {
		return false
	}
	r.startLocked()
	requested := false
	for name, engine := range r.engines {
		if !r.paused[name] {
			engine.Recheck()
			requested = true
		}
	}
	return requested
}

func TakeSmartRestartRequest() bool {
	defer recoverExport("takeSmartRestartRequest")
	r := smartReconnect
	r.mu.Lock()
	if !isRunning.Load() || !r.settings.Enabled || r.configuring || r.network == "" || len(r.workers) == 0 || r.transportFailure.IsZero() || time.Since(r.transportFailure) < 60*time.Second || time.Since(r.transportOK) < 60*time.Second || time.Since(r.lastRestart) < 5*time.Minute {
		r.mu.Unlock()
		return false
	}
	if r.resetAttempt.IsZero() {
		r.resetAttempt = time.Now()
		generation, network := r.generation, r.network
		r.mu.Unlock()
		safeGo("smartResetNetwork", func() {
			networkResetMu.Lock()
			defer networkResetMu.Unlock()
			valid := func() bool {
				r.mu.Lock()
				defer r.mu.Unlock()
				valid := isRunning.Load() && generation == r.generation && network == r.network && r.settings.Enabled && len(r.workers) > 0 && !r.transportFailure.IsZero()
				if valid {
					handleResetNetwork()
				}
				return valid
			}()
			if valid {
				SmartRecheck()
			}
		})
		appendSmartJournal("recovering", "network reset after persistent transport failure")
		return false
	}
	if time.Since(r.resetAttempt) < 30*time.Second {
		r.mu.Unlock()
		return false
	}
	r.lastRestart = time.Now()
	r.mu.Unlock()
	appendSmartJournal("recovering", "VPN restart requested after persistent transport failure")
	return true
}

func SmartRestartStillNeeded() bool {
	defer recoverExport("smartRestartStillNeeded")
	r := smartReconnect
	r.mu.Lock()
	defer r.mu.Unlock()
	return isRunning.Load() && r.settings.Enabled && !r.configuring && r.network != "" && len(r.workers) > 0 && !r.transportFailure.IsZero() && time.Since(r.transportFailure) >= 60*time.Second && time.Since(r.transportOK) >= 60*time.Second && !r.resetAttempt.IsZero() && time.Since(r.resetAttempt) >= 30*time.Second && time.Since(r.lastRestart) < time.Minute
}
