package smart

import (
	"encoding/json"
	"sort"
	"sync"
	"time"
)

const historyTTL = 7 * 24 * time.Hour

type Record struct {
	Fingerprint string        `json:"fingerprint"`
	Latency     time.Duration `json:"latency"`
	Checked     time.Time     `json:"checked"`
	BytesPerSec float64       `json:"bytes_per_second"`
	SpeedAt     time.Time     `json:"speed_at"`
	Alive       bool          `json:"alive"`
	LastSuccess time.Time     `json:"last_success"`
	Failure     string        `json:"failure,omitempty"`
	FailedAt    time.Time     `json:"failed_at,omitempty"`
	RetryAt     time.Time     `json:"retry_at,omitempty"`
}

func (r Record) available() bool {
	age := time.Since(r.Checked)
	return (r.Alive || r.Failure == "") && !r.Checked.IsZero() && age >= 0 && age < 30*time.Minute
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
			if age := time.Since(record.Checked); age >= 0 && age < historyTTL {
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

func (h *History) ActivePool(key string, limit int) map[string]Record {
	return activePool(h.Snapshot(key), limit)
}

func activePool(records map[string]Record, limit int) map[string]Record {
	var ids []string
	for id, record := range records {
		if record.available() {
			ids = append(ids, id)
		}
	}
	sort.Slice(ids, func(i, j int) bool {
		left, right := records[ids[i]], records[ids[j]]
		if left.Latency == right.Latency {
			return ids[i] < ids[j]
		}
		return left.Latency < right.Latency
	})
	pool := make(map[string]Record, min(len(ids), max(0, limit)))
	for _, id := range ids[:min(len(ids), max(0, limit))] {
		pool[id] = records[id]
	}
	return pool
}

func (h *History) record(key string, candidate Candidate) Record {
	record := h.Snapshot(key)[candidate.ID]
	if record.Fingerprint != candidate.Fingerprint {
		record = Record{Fingerprint: candidate.Fingerprint}
	}
	return record
}

func (h *History) Succeeded(key string, candidate Candidate, latency time.Duration) {
	record := h.record(key, candidate)
	record.Latency, record.Checked, record.LastSuccess = latency, time.Now(), time.Now()
	record.Alive, record.Failure, record.RetryAt = true, "", time.Time{}
	h.Put(key, candidate.ID, record)
}

func (h *History) Failed(key string, candidate Candidate, err error, cooldown time.Duration) {
	record := h.record(key, candidate)
	record.Alive, record.Failure = false, err.Error()
	record.Checked, record.FailedAt, record.RetryAt = time.Now(), time.Now(), time.Now().Add(cooldown)
	h.Put(key, candidate.ID, record)
}

func (h *History) Put(key, id string, record Record) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.Records == nil {
		h.Records = make(map[string]map[string]Record)
	}
	if h.Records[key] == nil {
		h.Records[key] = make(map[string]Record)
	}
	h.Records[key][id] = record
	if len(h.Records[key]) > 128 {
		oldest := id
		for candidate, value := range h.Records[key] {
			if value.Checked.Before(h.Records[key][oldest].Checked) {
				oldest = candidate
			}
		}
		delete(h.Records[key], oldest)
	}
	if len(h.Records) > 32 {
		oldest, oldestAt := "", time.Now()
		for other, records := range h.Records {
			if other == key {
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
		delete(h.Records, oldest)
	}
}
