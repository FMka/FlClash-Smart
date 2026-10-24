package smart

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"sort"
	"sync"
	"time"
)

var ErrUnavailable = errors.New("no reachable VPN candidate")

type Candidate struct {
	ID          string
	Fingerprint string
}

type Backend interface {
	Candidates() []Candidate
	Current() string
	Latency(context.Context, string) (time.Duration, error)
	Speed(context.Context, string, int64) (float64, error)
	Select(context.Context, string, string) error
}

type Options struct {
	Concurrency      int
	ProbeTimeout     time.Duration
	ScanBudget       time.Duration
	CollectWindow    time.Duration
	LatencyWindow    time.Duration
	HealthInterval   time.Duration
	OptimizeInterval time.Duration
	MinimumHold      time.Duration
	SpeedTimeout     time.Duration
	SpeedTTL         time.Duration
	SpeedCandidates  int
	SpeedBytes       int64
	MinimumSpeedGain float64
}

func DefaultOptions() Options {
	return Options{
		Concurrency: 8, ProbeTimeout: 3 * time.Second,
		ScanBudget: 12 * time.Second, CollectWindow: time.Second,
		LatencyWindow: 15 * time.Millisecond, HealthInterval: 30 * time.Second,
		OptimizeInterval: 5 * time.Minute, MinimumHold: time.Minute,
		SpeedTimeout: 3 * time.Second, SpeedTTL: 5 * time.Minute,
		SpeedCandidates: 3, SpeedBytes: 512 * 1024, MinimumSpeedGain: 0.20,
	}
}

type Record struct {
	Fingerprint string        `json:"fingerprint"`
	Latency     time.Duration `json:"latency"`
	Checked     time.Time     `json:"checked"`
	BytesPerSec float64       `json:"bytes_per_second"`
	SpeedAt     time.Time     `json:"speed_at"`
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
			if time.Since(record.Checked) < 24*time.Hour {
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
	if len(h.Records[key]) > 32 {
		oldest := id
		for candidate, value := range h.Records[key] {
			if value.Checked.Before(h.Records[key][oldest].Checked) {
				oldest = candidate
			}
		}
		delete(h.Records[key], oldest)
	}
	if len(h.Records) > 32 {
		for other := range h.Records {
			if other != key {
				delete(h.Records, other)
				break
			}
		}
	}
}

type Engine struct {
	Options  Options
	History  *History
	Status   func(string)
	Save     func()
	lastPick string
	pickedAt time.Time
	round    int
}

func New(options Options, history *History) *Engine {
	if history == nil {
		history = &History{}
	}
	return &Engine{Options: options, History: history}
}

func (e *Engine) report(phase string) {
	if e.Status != nil {
		e.Status(phase)
	}
}

func (e *Engine) spawn(run func()) {
	go func() {
		defer func() {
			if recover() != nil {
				e.report("internal-error")
			}
		}()
		run()
	}()
}

func (e *Engine) Run(ctx context.Context, network string, backend Backend) {
	if network == "" {
		return
	}
	optimizeAt := time.Time{}
	backoff := 3 * time.Second
	force := true
	for ctx.Err() == nil {
		if force || time.Since(optimizeAt) >= e.Options.OptimizeInterval {
			err := e.Scan(ctx, network, backend, force)
			if ctx.Err() != nil {
				return
			}
			if err != nil {
				e.report("unavailable")
				if !wait(ctx, backoff) {
					return
				}
				backoff = min(backoff*2, time.Minute)
				force = true
				continue
			}
			optimizeAt, backoff = time.Now(), 3*time.Second
		}
		if !wait(ctx, e.Options.HealthInterval) {
			return
		}
		probeCtx, cancel := context.WithTimeout(ctx, e.Options.ProbeTimeout)
		_, err := backend.Latency(probeCtx, backend.Current())
		cancel()
		force = err != nil
	}
}

func wait(ctx context.Context, delay time.Duration) bool {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}

type measurement struct {
	candidate Candidate
	latency   time.Duration
}

// The list is sampled across its full extent before filling gaps: LTE nodes
// near its end must not wait behind a long prefix of Wi-Fi-only timeouts.
func Order(candidates []Candidate, history map[string]Record, round int) []Candidate {
	ordered := make([]Candidate, 0, len(candidates))
	seen := make(map[string]bool, len(candidates))
	add := func(candidate Candidate) {
		if candidate.ID != "" && !seen[candidate.ID] {
			ordered = append(ordered, candidate)
			seen[candidate.ID] = true
		}
	}
	var known []Candidate
	for _, candidate := range candidates {
		if record, ok := history[candidate.ID]; ok && record.Fingerprint == candidate.Fingerprint && time.Since(record.Checked) < 24*time.Hour {
			known = append(known, candidate)
		}
	}
	sort.SliceStable(known, func(i, j int) bool {
		return history[known[i].ID].Checked.After(history[known[j].ID].Checked)
	})
	for _, candidate := range known {
		add(candidate)
	}
	n := len(candidates)
	if n == 0 {
		return ordered
	}
	add(candidates[round%n])
	add(candidates[(n-1+round)%n])
	stride := max(1, int(float64(n)*0.61803398875))
	for gcd(stride, n) != 1 {
		stride++
	}
	for i := 0; i < n; i++ {
		add(candidates[(round+i*stride)%n])
	}
	return ordered
}

func gcd(a, b int) int {
	for b != 0 {
		a, b = b, a%b
	}
	return a
}

func (e *Engine) Scan(ctx context.Context, network string, backend Backend, force bool) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	e.report("searching")
	candidates := Order(backend.Candidates(), e.History.Snapshot(network), e.round)
	current := backend.Current()
	if !force {
		for index, candidate := range candidates {
			if candidate.ID == current {
				copy(candidates[1:index+1], candidates[:index])
				candidates[0] = candidate
				break
			}
		}
	}
	e.round += e.Options.Concurrency
	if len(candidates) == 0 {
		return ErrUnavailable
	}
	scanCtx, cancel := context.WithTimeout(ctx, e.Options.ScanBudget)
	defer cancel()
	jobs := make(chan Candidate)
	results := make(chan measurement, e.Options.Concurrency)
	var workers sync.WaitGroup
	for range min(e.Options.Concurrency, len(candidates)) {
		workers.Add(1)
		e.spawn(func() {
			defer workers.Done()
			for candidate := range jobs {
				probeCtx, stop := context.WithTimeout(scanCtx, e.Options.ProbeTimeout)
				latency, err := backend.Latency(probeCtx, candidate.ID)
				stop()
				if err != nil && scanCtx.Err() == nil {
					e.History.Remove(network, candidate.ID)
				}
				if err == nil && latency >= 0 && scanCtx.Err() == nil {
					select {
					case results <- measurement{candidate, latency}:
					case <-scanCtx.Done():
					}
				}
			}
		})
	}
	e.spawn(func() {
		defer close(jobs)
		for _, candidate := range candidates {
			select {
			case jobs <- candidate:
			case <-scanCtx.Done():
				return
			}
		}
	})
	e.spawn(func() {
		workers.Wait()
		close(results)
	})
	var found []measurement
	var collect <-chan time.Time
	var timer *time.Timer
	collecting := true
	for collecting {
		select {
		case <-ctx.Done():
			if timer != nil {
				timer.Stop()
			}
			return ctx.Err()
		case <-scanCtx.Done():
			collecting = false
		case <-collect:
			collecting = false
		case result, ok := <-results:
			if !ok {
				collecting = false
				break
			}
			found = append(found, result)
			if len(found) == 1 {
				if force {
					if err := backend.Select(ctx, result.candidate.ID, "recovery"); err != nil {
						cancel()
						return err
					}
				}
				timer = time.NewTimer(e.Options.CollectWindow)
				collect = timer.C
			}
		}
	}
	cancel()
	if timer != nil {
		timer.Stop()
	}
	workers.Wait()
	if err := ctx.Err(); err != nil {
		return err
	}
	if len(found) == 0 {
		return ErrUnavailable
	}
	sort.SliceStable(found, func(i, j int) bool { return found[i].latency < found[j].latency })
	history := e.History.Snapshot(network)
	fresh := map[string]bool{}
	for _, result := range found {
		fresh[result.candidate.ID] = true
		record := history[result.candidate.ID]
		if record.Fingerprint != result.candidate.Fingerprint {
			record = Record{}
		}
		record.Fingerprint, record.Latency, record.Checked = result.candidate.Fingerprint, result.latency, time.Now()
		history[result.candidate.ID] = record
		e.History.Put(network, result.candidate.ID, record)
	}
	shortlist := make([]measurement, 0, e.Options.SpeedCandidates)
	for _, candidate := range found {
		if candidate.latency > found[0].latency+e.Options.LatencyWindow {
			break
		}
		shortlist = append(shortlist, candidate)
		if len(shortlist) >= e.Options.SpeedCandidates {
			break
		}
	}
	if !force {
		for _, candidate := range found {
			if candidate.candidate.ID != current || candidate.latency > found[0].latency+e.Options.LatencyWindow {
				continue
			}
			included := false
			for _, selected := range shortlist {
				included = included || selected.candidate.ID == current
			}
			if !included {
				shortlist[len(shortlist)-1] = candidate
			}
			break
		}
	}
	e.report("measuring")
	winner, bestSpeed := shortlist[0].candidate.ID, float64(0)
	for _, candidate := range shortlist {
		if err := ctx.Err(); err != nil {
			return err
		}
		id := candidate.candidate.ID
		record := history[id]
		if record.BytesPerSec <= 0 || time.Since(record.SpeedAt) >= e.Options.SpeedTTL {
			speedCtx, stop := context.WithTimeout(ctx, e.Options.SpeedTimeout)
			speed, err := backend.Speed(speedCtx, id, e.Options.SpeedBytes)
			stop()
			if ctx.Err() != nil {
				return ctx.Err()
			}
			if err == nil && speed > 0 && !math.IsNaN(speed) && !math.IsInf(speed, 0) {
				record.BytesPerSec, record.SpeedAt = speed, time.Now()
			} else {
				record.BytesPerSec = 0
			}
			history[id] = record
			e.History.Put(network, id, record)
		}
		if record.BytesPerSec > bestSpeed {
			winner, bestSpeed = id, record.BytesPerSec
		}
	}
	current = backend.Current()
	if !force && current != winner {
		if record, alive := history[current]; alive && fresh[current] {
			if bestSpeed == 0 && record.Latency <= found[0].latency+e.Options.LatencyWindow {
				winner = current
			}
			if current == e.lastPick && time.Since(e.pickedAt) < e.Options.MinimumHold {
				winner = current
			}
			if record.BytesPerSec > 0 && time.Since(record.SpeedAt) < e.Options.SpeedTTL && bestSpeed < record.BytesPerSec*(1+e.Options.MinimumSpeedGain) {
				winner = current
			}
		}
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := backend.Select(ctx, winner, "optimized"); err != nil {
		return err
	}
	if winner != e.lastPick {
		e.lastPick, e.pickedAt = winner, time.Now()
	}
	e.report("connected")
	if e.Save != nil {
		e.Save()
	}
	return nil
}
