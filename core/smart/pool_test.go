package smart

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"
)

type qualifiedBackend struct {
	*fakeBackend
	qualify func(context.Context, string) error
	health  func(context.Context, string) error
}

func (b *qualifiedBackend) Qualify(ctx context.Context, id string) error {
	if b.qualify != nil {
		return b.qualify(ctx, id)
	}
	return ctx.Err()
}

func (b *qualifiedBackend) Health(ctx context.Context, id string) error {
	if b.health != nil {
		return b.health(ctx, id)
	}
	return ctx.Err()
}

func TestPoolEvictsFailureRetainsSpeedAndReintroducesRecoveredNode(t *testing.T) {
	h := &History{}
	candidate := Candidate{ID: "a", Fingerprint: "v1"}
	h.Put("lte", "a", Record{Fingerprint: "v1", Checked: time.Now(), Alive: true, BytesPerSec: 123, SpeedAt: time.Now()})
	b := &fakeBackend{candidates: []Candidate{candidate}, current: "a"}
	e := New(testOptions(), h)
	e.RefreshPool(context.Background(), "lte", b)
	failed := h.Snapshot("lte")["a"]
	if len(h.ActivePool("lte", 20)) != 0 || failed.Failure == "" || failed.BytesPerSec != 123 {
		t.Fatal("failure did not leave the pool while preserving history")
	}
	b.latencies = map[string]time.Duration{"a": 20 * time.Millisecond}
	e.RefreshPool(context.Background(), "lte", b)
	if len(h.ActivePool("lte", 20)) != 0 {
		t.Fatal("ignored failed-node cooldown")
	}
	failed.RetryAt = time.Now().Add(-time.Second)
	h.Put("lte", "a", failed)
	e.RefreshPool(context.Background(), "lte", b)
	recovered := h.Snapshot("lte")["a"]
	if len(h.ActivePool("lte", 20)) != 1 || recovered.Failure != "" || recovered.BytesPerSec != 123 {
		t.Fatal("recovered node was not restored with its previous measurement")
	}
	if len(b.selected) != 0 || b.speedCalls != 0 {
		t.Fatal("pool maintenance switched the route or downloaded a speed test")
	}
}

func TestPoolIsBoundedButRefreshDiscoversBeyondKnownNodes(t *testing.T) {
	h := &History{}
	b := &fakeBackend{current: "0"}
	var mu sync.Mutex
	probed := map[string]bool{}
	for i := range 80 {
		candidate := Candidate{ID: fmt.Sprint(i)}
		b.candidates = append(b.candidates, candidate)
		if i < 30 {
			h.Succeeded("wifi", candidate, time.Duration(i+1)*time.Millisecond)
		}
	}
	b.probe = func(_ context.Context, id string) (time.Duration, error) {
		mu.Lock()
		probed[id] = true
		mu.Unlock()
		return time.Millisecond, nil
	}
	e := New(testOptions(), h)
	for range 20 {
		e.RefreshPool(context.Background(), "wifi", b)
	}
	if len(h.ActivePool("wifi", 20)) != 20 || len(probed) <= 30 || !probed["79"] {
		t.Fatalf("pool froze on old entries: active=%d discovered=%d", len(h.ActivePool("wifi", 20)), len(probed))
	}
}

func TestExpiredSpeedIsRetainedButNotUsedToDisplaceFreshWinner(t *testing.T) {
	h := &History{}
	oldAt := time.Now().Add(-2 * time.Hour)
	h.Put("wifi", "old", Record{Checked: time.Now(), BytesPerSec: 99999, SpeedAt: oldAt})
	b := &fakeBackend{candidates: []Candidate{{ID: "old"}, {ID: "new"}}, latencies: map[string]time.Duration{"old": time.Millisecond, "new": 2 * time.Millisecond}, speeds: map[string]float64{"new": 100}}
	if err := New(testOptions(), h).Scan(context.Background(), "wifi", b, true); err != nil {
		t.Fatal(err)
	}
	old := h.Snapshot("wifi")["old"]
	if b.Current() != "new" || old.BytesPerSec != 99999 || !old.SpeedAt.Equal(oldAt) {
		t.Fatal("stale speed was used or discarded after a failed remeasurement")
	}
}

func TestQualificationRejectsLowLatencyBeforeSelection(t *testing.T) {
	b := &qualifiedBackend{fakeBackend: &fakeBackend{candidates: []Candidate{{ID: "ru"}, {ID: "good"}}, latencies: map[string]time.Duration{"ru": time.Millisecond, "good": 20 * time.Millisecond}, speeds: map[string]float64{"ru": 1000, "good": 100}}}
	b.qualify = func(_ context.Context, id string) error {
		if id == "ru" {
			return errors.New("country-rejected")
		}
		return nil
	}
	if err := New(testOptions(), nil).Scan(context.Background(), "wifi", b, true); err != nil {
		t.Fatal(err)
	}
	for _, id := range b.selected {
		if id != "good" {
			t.Fatal("ineligible candidate was used even provisionally")
		}
	}
}
