package smart

import (
	"encoding/json"
	"strings"
	"sync"
	"time"
)

const historyTTL = 7 * 24 * time.Hour

type Record struct {
	Fingerprint  string        `json:"fingerprint"`
	Latency      time.Duration `json:"latency"`
	Checked      time.Time     `json:"checked"`
	BytesPerSec  float64       `json:"bytes_per_second"`
	SpeedAt      time.Time     `json:"speed_at"`
	Alive        bool          `json:"alive"`
	LastSuccess  time.Time     `json:"last_success"`
	Failure      string        `json:"failure,omitempty"`
	FailedAt     time.Time     `json:"failed_at,omitempty"`
	RetryAt      time.Time     `json:"retry_at,omitempty"`
	Reachable    bool          `json:"reachable,omitempty"`
	RespondedAt  time.Time     `json:"responded_at,omitempty"`
	Country      string        `json:"country,omitempty"`
	SpeedContext string        `json:"speed_context,omitempty"`
}

func (r Record) available() bool {
	age := time.Since(r.Checked)
	return (r.Reachable || r.Alive || r.Failure == "") && !r.Checked.IsZero() && age >= 0
}

func (r Record) responded() bool {
	return r.available() || !r.RespondedAt.IsZero() || !r.LastSuccess.IsZero()
}

type History struct {
	mu      sync.Mutex
	Records map[string]map[string]Record `json:"records"`
}

func (h *History) MarshalJSON() ([]byte, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	return json.Marshal(struct {
		Records map[string]map[string]Record `json:"records"`
	}{h.Records})
}

func (h *History) UnmarshalJSON(data []byte) error {
	var decoded struct {
		Records map[string]map[string]Record `json:"records"`
	}
	if err := json.Unmarshal(data, &decoded); err != nil {
		return err
	}
	for key, records := range decoded.Records {
		for id, record := range records {
			if record.RespondedAt.IsZero() && record.available() {
				record.RespondedAt = record.Checked
			}
			if age := time.Since(record.Checked); age >= 0 && (record.responded() || age < historyTTL) {
				h.Put(key, id, record)
			}
		}
	}
	return nil
}

func (h *History) Remove(key, id string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	delete(h.Records[key], id)
}

func (h *History) Snapshot(key string) map[string]Record {
	h.mu.Lock()
	defer h.mu.Unlock()
	copy := make(map[string]Record, len(h.Records[key]))
	for id, record := range h.Records[key] {
		copy[id] = record
	}
	return copy
}

func (h *History) ActivePool(key string) map[string]Record {
	return activePool(h.Snapshot(key))
}

func (h *History) SavedPool(key string) map[string]Record {
	pool := h.Snapshot(key)
	for id, record := range pool {
		if !record.responded() {
			delete(pool, id)
		}
	}
	return pool
}

func (h *History) SavedPoolCount(key string) int {
	h.mu.Lock()
	defer h.mu.Unlock()
	count := 0
	for _, record := range h.Records[key] {
		if record.responded() {
			count++
		}
	}
	return count
}

func activePool(records map[string]Record) map[string]Record {
	pool := make(map[string]Record)
	for id, record := range records {
		if record.available() {
			pool[id] = record
		}
	}
	return pool
}

func (h *History) record(key string, candidate Candidate) Record {
	h.mu.Lock()
	record := h.Records[key][candidate.ID]
	h.mu.Unlock()
	if record.Fingerprint != candidate.Fingerprint {
		record = Record{Fingerprint: candidate.Fingerprint}
	}
	return record
}

func (h *History) Succeeded(key string, candidate Candidate, latency time.Duration) {
	record := h.record(key, candidate)
	record.Latency, record.Checked, record.LastSuccess = latency, time.Now(), time.Now()
	record.Alive, record.Reachable, record.Failure, record.RetryAt = true, true, "", time.Time{}
	record.RespondedAt, record.Country = record.Checked, candidate.Country
	h.Put(key, candidate.ID, record)
}

func (h *History) Responded(key string, candidate Candidate, latency time.Duration) {
	record := h.record(key, candidate)
	record.Latency, record.Checked, record.Reachable = latency, time.Now(), true
	record.RespondedAt = record.Checked
	if candidate.Country != "" {
		record.Country = candidate.Country
	}
	h.Put(key, candidate.ID, record)
}

func (h *History) Failed(key string, candidate Candidate, err error, cooldown time.Duration) {
	record := h.record(key, candidate)
	record.Alive, record.Reachable, record.Failure = false, false, err.Error()
	record.Checked, record.FailedAt, record.RetryAt = time.Now(), time.Now(), time.Now().Add(cooldown)
	h.Put(key, candidate.ID, record)
}

func (h *History) Unqualified(key string, candidate Candidate, latency time.Duration, err error, cooldown time.Duration) {
	h.Failed(key, candidate, err, cooldown)
	record := h.record(key, candidate)
	record.Latency, record.Reachable, record.RespondedAt = latency, true, time.Now()
	record.Country = candidate.Country
	h.Put(key, candidate.ID, record)
}

func (h *History) Reconcile(key string, candidates []Candidate) {
	valid := make(map[string]string, len(candidates))
	for _, candidate := range candidates {
		valid[candidate.ID] = candidate.Fingerprint
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	for id, record := range h.Records[key] {
		if identity, ok := valid[id]; !ok || identity != record.Fingerprint {
			delete(h.Records[key], id)
		}
	}
}

func (h *History) Import(from, to string) {
	for id, record := range h.Snapshot(from) {
		current := h.record(to, Candidate{ID: id, Fingerprint: record.Fingerprint})
		if current.Checked.Before(record.Checked) {
			h.Put(to, id, record)
		}
	}
}

func (h *History) Put(key, id string, record Record) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.putLocked(key, id, record)
}

func (h *History) ObserveIfCurrent(key string, candidate Candidate, latency time.Duration, failure error, started time.Time) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	record := h.Records[key][candidate.ID]
	if record.Fingerprint != candidate.Fingerprint {
		if record.Checked.After(started) {
			return false
		}
		record = Record{Fingerprint: candidate.Fingerprint}
	}
	record.Checked = time.Now()
	if candidate.Country != "" {
		record.Country = candidate.Country
	}
	if failure == nil {
		record.Latency, record.Reachable, record.RespondedAt = latency, true, record.Checked
	} else {
		record.Alive, record.Reachable, record.Failure = false, false, failure.Error()
		record.FailedAt, record.RetryAt = record.Checked, record.Checked.Add(time.Minute)
	}
	h.putLocked(key, candidate.ID, record)
	return true
}

func (h *History) putLocked(key, id string, record Record) {
	if h.Records == nil {
		h.Records = make(map[string]map[string]Record)
	}
	if h.Records[key] == nil {
		h.Records[key] = make(map[string]Record)
	}
	h.Records[key][id] = record
	if !record.responded() && len(h.Records[key]) > 128 {
		oldest, failures := "", 0
		for candidate, value := range h.Records[key] {
			if value.responded() {
				continue
			}
			failures++
			if oldest == "" || value.Checked.Before(h.Records[key][oldest].Checked) {
				oldest = candidate
			}
		}
		if failures > 128 {
			delete(h.Records[key], oldest)
		}
	}
	if len(h.Records) > 32 {
		oldest, oldestAt := "", time.Now()
		for other, records := range h.Records {
			if other == key || strings.HasPrefix(other, "wifi:") || strings.HasPrefix(other, "mobile:") {
				continue
			}
			newest := time.Time{}
			for _, value := range records {
				if value.Checked.After(newest) {
					newest = value.Checked
				}
			}
			if oldest == "" || newest.Before(oldestAt) {
				oldest, oldestAt = other, newest
			}
		}
		if oldest != "" {
			delete(h.Records, oldest)
		}
	}
}
