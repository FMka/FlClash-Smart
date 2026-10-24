package smart

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"
)

type fakeBackend struct {
	mu           sync.Mutex
	candidates   []Candidate
	latencies    map[string]time.Duration
	speeds       map[string]float64
	current      string
	selected     []string
	measured     []string
	speedCalls   int
	active, peak int
	probe        func(context.Context, string) (time.Duration, error)
}

func (b *fakeBackend) Candidates() []Candidate { return b.candidates }
func (b *fakeBackend) Current() string         { b.mu.Lock(); defer b.mu.Unlock(); return b.current }
func (b *fakeBackend) Select(ctx context.Context, id, _ string) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	b.current = id
	b.selected = append(b.selected, id)
	return nil
}
func (b *fakeBackend) Latency(ctx context.Context, id string) (time.Duration, error) {
	b.mu.Lock()
	b.active++
	b.peak = max(b.peak, b.active)
	b.mu.Unlock()
	defer func() { b.mu.Lock(); b.active--; b.mu.Unlock() }()
	if b.probe != nil {
		return b.probe(ctx, id)
	}
	if latency, ok := b.latencies[id]; ok {
		return latency, nil
	}
	return 0, ErrUnavailable
}
func (b *fakeBackend) Speed(ctx context.Context, id string, _ int64) (float64, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.measured = append(b.measured, id)
	b.speedCalls++
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	return b.speeds[id], nil
}

func testOptions() Options {
	options := DefaultOptions()
	options.CollectWindow = 15 * time.Millisecond
	options.ProbeTimeout = 80 * time.Millisecond
	options.ScanBudget = 300 * time.Millisecond
	options.SpeedTimeout = 50 * time.Millisecond
	return options
}

func TestLateLTECandidateRestoresBeforePrefixTimeouts(t *testing.T) {
	b := &fakeBackend{current: "dead"}
	for i := range 1000 {
		b.candidates = append(b.candidates, Candidate{ID: fmt.Sprint(i)})
	}
	b.probe = func(ctx context.Context, id string) (time.Duration, error) {
		if id == "999" {
			return 50 * time.Millisecond, nil
		}
		<-ctx.Done()
		return 0, ctx.Err()
	}
	start := time.Now()
	if err := New(testOptions(), nil).Scan(context.Background(), "lte", b, true); err != nil {
		t.Fatal(err)
	}
	if b.Current() != "999" {
		t.Fatalf("selected %s", b.Current())
	}
	if time.Since(start) >= 200*time.Millisecond {
		t.Fatal("recovery waited behind dead prefix")
	}
	if b.peak > testOptions().Concurrency {
		t.Fatalf("unbounded probes: %d", b.peak)
	}
}

func TestChoosesThroughputInsideLatencyWindow(t *testing.T) {
	b := &fakeBackend{
		candidates: []Candidate{{ID: "low"}, {ID: "fast"}, {ID: "far"}},
		latencies:  map[string]time.Duration{"low": 40 * time.Millisecond, "fast": 50 * time.Millisecond, "far": 80 * time.Millisecond},
		speeds:     map[string]float64{"low": 100, "fast": 1000, "far": 10000},
	}
	if err := New(testOptions(), nil).Scan(context.Background(), "wifi", b, true); err != nil {
		t.Fatal(err)
	}
	if b.Current() != "fast" {
		t.Fatalf("wanted fast, got %s", b.Current())
	}
	for _, name := range b.measured {
		if name == "far" {
			t.Fatal("measured out-of-window node")
		}
	}
}

func TestSpeedCacheIsPerNetworkAndFingerprint(t *testing.T) {
	b := &fakeBackend{candidates: []Candidate{{ID: "a", Fingerprint: "v1"}}, latencies: map[string]time.Duration{"a": time.Millisecond}, speeds: map[string]float64{"a": 100}}
	e := New(testOptions(), nil)
	for _, network := range []string{"wifi", "wifi", "lte"} {
		if err := e.Scan(context.Background(), network, b, true); err != nil {
			t.Fatal(err)
		}
	}
	if b.speedCalls != 2 {
		t.Fatalf("got %d speed tests", b.speedCalls)
	}
	b.candidates[0].Fingerprint = "v2"
	if err := e.Scan(context.Background(), "lte", b, true); err != nil {
		t.Fatal(err)
	}
	if b.speedCalls != 3 {
		t.Fatal("reused changed-node measurement")
	}
}

func TestUnavailableDoesNotUseStaleHistory(t *testing.T) {
	h := &History{}
	h.Put("wifi", "old", Record{Checked: time.Now(), Latency: time.Millisecond, BytesPerSec: 10000})
	b := &fakeBackend{candidates: []Candidate{{ID: "old"}}, current: "old"}
	if err := New(testOptions(), h).Scan(context.Background(), "wifi", b, true); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("got %v", err)
	}
	if len(b.selected) != 0 {
		t.Fatal("selected an unreachable node")
	}
	if len(h.ActivePool("wifi", 20)) != 0 || h.Snapshot("wifi")["old"].BytesPerSec != 10000 {
		t.Fatal("failed node remained active or its speed history was discarded")
	}
}

func TestCancellationCannotSelectOrMeasure(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	b := &fakeBackend{candidates: []Candidate{{ID: "a"}}}
	b.probe = func(context.Context, string) (time.Duration, error) { cancel(); return time.Millisecond, nil }
	if err := New(testOptions(), nil).Scan(ctx, "lte", b, true); !errors.Is(err, context.Canceled) {
		t.Fatalf("got %v", err)
	}
	if len(b.selected) != 0 || b.speedCalls != 0 {
		t.Fatal("cancelled scan changed route or spent speed-test data")
	}
}

func TestMinimumHoldStopsOptimizationButNotRecovery(t *testing.T) {
	b := &fakeBackend{candidates: []Candidate{{ID: "old"}, {ID: "new"}}, current: "old", latencies: map[string]time.Duration{"old": 40 * time.Millisecond, "new": 45 * time.Millisecond}, speeds: map[string]float64{"old": 100, "new": 1000}}
	e := New(testOptions(), nil)
	e.lastPick, e.pickedAt = "old", time.Now()
	if err := e.Scan(context.Background(), "wifi", b, false); err != nil {
		t.Fatal(err)
	}
	if b.Current() != "old" {
		t.Fatal("optimization bypassed minimum hold")
	}
	delete(b.latencies, "old")
	if err := e.Scan(context.Background(), "wifi", b, true); err != nil {
		t.Fatal(err)
	}
	if b.Current() != "new" {
		t.Fatal("minimum hold blocked failure recovery")
	}
}

func TestOrderIsCompleteAndPrioritizesOnlyMatchingHistory(t *testing.T) {
	for n := 1; n < 500; n++ {
		var candidates []Candidate
		for i := range n {
			candidates = append(candidates, Candidate{ID: fmt.Sprint(i), Fingerprint: "v2"})
		}
		h := map[string]Record{"0": {Fingerprint: "v1", Checked: time.Now()}, fmt.Sprint(n - 1): {Fingerprint: "v2", Checked: time.Now()}}
		ordered := Order(candidates, h, n+3)
		if len(ordered) != n || ordered[0].ID != fmt.Sprint(n-1) {
			t.Fatalf("incomplete or wrong priority at n=%d", n)
		}
		seen := map[string]bool{}
		for _, candidate := range ordered {
			if seen[candidate.ID] {
				t.Fatal("duplicate candidate")
			}
			seen[candidate.ID] = true
		}
	}
}

func TestHistoryRoundTripIsBounded(t *testing.T) {
	h := &History{}
	for i := range 200 {
		h.Put("wifi", fmt.Sprint(i), Record{Checked: time.Now()})
	}
	data, err := json.Marshal(h)
	if err != nil {
		t.Fatal(err)
	}
	var restored History
	if err = json.Unmarshal(data, &restored); err != nil {
		t.Fatal(err)
	}
	if len(restored.Snapshot("wifi")) != 128 {
		t.Fatal("history exceeded bound or was lost")
	}
}

func TestOptimizationMeasuresCurrentAndRequiresSpeedGain(t *testing.T) {
	b := &fakeBackend{
		candidates: []Candidate{{ID: "first"}, {ID: "second"}, {ID: "third"}, {ID: "current"}},
		current:    "current",
		latencies:  map[string]time.Duration{"first": 40 * time.Millisecond, "second": 41 * time.Millisecond, "third": 42 * time.Millisecond, "current": 45 * time.Millisecond},
		speeds:     map[string]float64{"first": 110, "second": 105, "third": 106, "current": 100},
	}
	e := New(testOptions(), nil)
	if err := e.Scan(context.Background(), "wifi", b, false); err != nil {
		t.Fatal(err)
	}
	if b.Current() != "current" {
		t.Fatal("a small speed gain displaced a healthy node")
	}
	measuredCurrent := false
	for _, id := range b.measured {
		measuredCurrent = measuredCurrent || id == "current"
	}
	if !measuredCurrent || len(b.measured) != e.Options.SpeedCandidates {
		t.Fatal("current node was excluded from the bounded speed comparison")
	}
}
