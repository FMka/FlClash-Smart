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

func TestCompletePoolsSurviveAgeRestartAndDiscoveryFailures(t *testing.T) {
	h := &History{}
	old := time.Now().Add(-10 * 24 * time.Hour)
	for _, network := range []string{"wifi:group", "mobile:group"} {
		for i := range 600 {
			h.Put(network, fmt.Sprint(i), Record{Fingerprint: "same", Alive: true, Checked: old, LastSuccess: old, Country: "DE", BytesPerSec: 123, SpeedAt: old})
		}
		for i := range 400 {
			h.Failed(network, Candidate{ID: fmt.Sprintf("failed-%d", i)}, ErrUnavailable, time.Minute)
		}
	}
	data, err := json.Marshal(h)
	if err != nil {
		t.Fatal(err)
	}
	var restored History
	if err = json.Unmarshal(data, &restored); err != nil {
		t.Fatal(err)
	}
	for _, network := range []string{"wifi:group", "mobile:group"} {
		if len(restored.ActivePool(network)) != 600 || len(restored.SavedPool(network)) != 600 {
			t.Fatalf("full pool was aged out or truncated: %s", network)
		}
		if record := restored.Snapshot(network)["0"]; !record.SpeedAt.Equal(old) || record.BytesPerSec != 123 {
			t.Fatal("restoring history refreshed or erased a measurement")
		}
	}
	if len(restored.Snapshot("wifi:group")) > 728 {
		t.Fatal("never-working discovery grew without a bound")
	}
}

func TestFullPoolReconcilesOnlyChangedAndRemovedDefinitions(t *testing.T) {
	h := &History{}
	for _, id := range []string{"kept", "changed", "removed"} {
		h.Succeeded("wifi", Candidate{ID: id, Fingerprint: "v1", Country: "DE"}, time.Millisecond)
	}
	h.Failed("wifi", Candidate{ID: "kept", Fingerprint: "v1"}, ErrUnavailable, time.Minute)
	h.Reconcile("wifi", []Candidate{{ID: "kept", Fingerprint: "v1"}, {ID: "changed", Fingerprint: "v2"}})
	if len(h.SavedPool("wifi")) != 1 || len(h.ActivePool("wifi")) != 0 {
		t.Fatal("failed previously working node was lost, or changed/deleted node survived")
	}
}

func TestRespondingUnqualifiedNodeIsStoredButNeverSelected(t *testing.T) {
	b := &qualifiedBackend{fakeBackend: &fakeBackend{candidates: []Candidate{{ID: "blocked", Country: "RU"}}, latencies: map[string]time.Duration{"blocked": time.Millisecond}}}
	b.qualify = func(context.Context, string) error { return errors.New("country-excluded:RU") }
	e := New(testOptions(), nil)
	if err := e.Scan(context.Background(), "wifi", b, true); !errors.Is(err, ErrUnavailable) {
		t.Fatal(err)
	}
	if len(e.History.SavedPool("wifi")) != 1 || e.History.Snapshot("wifi")["blocked"].Alive || len(b.selected) != 0 {
		t.Fatal("response evidence was discarded or qualification was bypassed")
	}
}

func TestReplyIsSavedEvenWhenCollectionCancelsSlowQualification(t *testing.T) {
	slowStarted := make(chan struct{})
	b := &qualifiedBackend{fakeBackend: &fakeBackend{candidates: []Candidate{{ID: "winner"}, {ID: "slow"}}, latencies: map[string]time.Duration{"winner": time.Millisecond, "slow": 2 * time.Millisecond}}}
	b.qualify = func(ctx context.Context, id string) error {
		if id == "slow" {
			close(slowStarted)
			<-ctx.Done()
			return ctx.Err()
		}
		select {
		case <-slowStarted:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	e := New(testOptions(), nil)
	if err := e.Scan(context.Background(), "wifi", b, true); err != nil {
		t.Fatal(err)
	}
	if b.Current() != "winner" || len(e.History.SavedPool("wifi")) != 2 || e.History.Snapshot("wifi")["slow"].Alive {
		t.Fatal("latency response was lost or incomplete qualification was selected")
	}
}

func TestPreferredCountryFallbackAndReturnOverrideSpeedAndHold(t *testing.T) {
	b := &fakeBackend{current: "fallback", candidates: []Candidate{{ID: "fallback", Country: "US"}, {ID: "preferred", Country: "DE"}}, latencies: map[string]time.Duration{"fallback": time.Millisecond}, speeds: map[string]float64{"fallback": 99999}}
	options := testOptions()
	options.PreferredCountry = "DE"
	e := New(options, nil)
	if err := e.Scan(context.Background(), "wifi", b, true); err != nil || b.Current() != "fallback" {
		t.Fatalf("no usable fallback: %v", err)
	}
	failed := e.History.Snapshot("wifi")["preferred"]
	failed.RetryAt = time.Now().Add(-time.Second)
	e.History.Put("wifi", "preferred", failed)
	b.latencies["preferred"] = 8 * time.Millisecond
	b.speedCalls = 0
	e.RefreshPool(context.Background(), "wifi", b)
	if b.Current() != "preferred" || b.speedCalls != 0 {
		t.Fatal("preferred recovery did not switch without an optional speed download")
	}
	if err := e.Scan(context.Background(), "wifi", b, false); err != nil || b.Current() != "preferred" {
		t.Fatalf("faster foreign node displaced preferred: %v", err)
	}
}

func TestDeadPreferredTierCannotStarveOtherCountryFallback(t *testing.T) {
	b := &fakeBackend{}
	for i := range 100 {
		b.candidates = append(b.candidates, Candidate{ID: fmt.Sprint(i), Country: "DE"})
	}
	b.candidates = append(b.candidates, Candidate{ID: "fallback", Country: "US"})
	b.probe = func(ctx context.Context, id string) (time.Duration, error) {
		if id == "fallback" {
			return time.Millisecond, nil
		}
		<-ctx.Done()
		return 0, ctx.Err()
	}
	options := testOptions()
	options.PreferredCountry = "DE"
	e := New(options, nil)
	if err := e.Scan(context.Background(), "lte", b, true); err != nil || b.Current() != "fallback" {
		t.Fatalf("preferred prefix starved fallback: %v", err)
	}
}

func TestSavedCountryPriorityAndAllKnownNodesPrecedeDiscovery(t *testing.T) {
	var candidates []Candidate
	history := map[string]Record{}
	for i := range 160 {
		id := fmt.Sprint(i)
		candidates = append(candidates, Candidate{ID: id, Country: "US"})
		history[id] = Record{Alive: true, Checked: time.Now(), Latency: time.Duration(i) * time.Millisecond}
	}
	candidates = append(candidates, Candidate{ID: "new", Country: "US"}, Candidate{ID: "preferred", Country: "DE"})
	ordered := orderCandidates(candidates, history, 11, "DE")
	if ordered[0].ID != "preferred" {
		t.Fatal("preferred discovery was delayed behind a foreign saved pool")
	}
	ordered = Order(candidates, history, 11)
	for _, candidate := range ordered[:160] {
		if _, ok := history[candidate.ID]; !ok {
			t.Fatal("discovery displaced a saved node")
		}
	}
}

func TestKeepCurrentChecksInPlaceAndOnlySwitchesAfterFailure(t *testing.T) {
	for _, dead := range []bool{false, true} {
		t.Run(fmt.Sprint(dead), func(t *testing.T) {
			b := &fakeBackend{current: "common", candidates: []Candidate{{ID: "common"}, {ID: "replacement"}}, latencies: map[string]time.Duration{"replacement": time.Millisecond}}
			if !dead {
				b.latencies["common"] = 2 * time.Millisecond
			}
			options := testOptions()
			options.KeepCurrent = true
			options.HealthInterval = time.Hour
			e := New(options, nil)
			saved := make(chan struct{})
			var once sync.Once
			e.Save = func() { once.Do(func() { close(saved) }) }
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			done := make(chan struct{})
			go func() { defer close(done); e.Run(ctx, "wifi", b) }()
			if dead {
				deadline := time.Now().Add(time.Second)
				for b.Current() != "replacement" && time.Now().Before(deadline) {
					time.Sleep(time.Millisecond)
				}
				if b.Current() != "replacement" {
					cancel()
					<-done
					t.Fatal("failed common node was kept")
				}
			} else {
				select {
				case <-saved:
				case <-time.After(time.Second):
					cancel()
					<-done
					t.Fatal("common node was not checked immediately")
				}
				b.mu.Lock()
				switched := len(b.selected) > 0
				b.mu.Unlock()
				if switched {
					cancel()
					<-done
					t.Fatal("healthy common node was changed before checking")
				}
			}
			cancel()
			<-done
		})
	}
}

func TestSpeedFromAnotherWiFiContextIsNotReused(t *testing.T) {
	h := &History{}
	h.Put("wifi", "old", Record{Checked: time.Now(), Alive: true, BytesPerSec: 99999, SpeedAt: time.Now(), SpeedContext: "previous-router"})
	b := &fakeBackend{candidates: []Candidate{{ID: "old"}, {ID: "new"}}, latencies: map[string]time.Duration{"old": time.Millisecond, "new": 2 * time.Millisecond}, speeds: map[string]float64{"new": 100}}
	options := testOptions()
	options.NetworkContext = "current-router"
	if err := New(options, h).Scan(context.Background(), "wifi", b, true); err != nil || b.Current() != "new" {
		t.Fatalf("another link's speed was reused: %v", err)
	}
}
