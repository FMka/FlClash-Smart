package smart

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"
)

func serviceTestBackend() (*Engine, *qualifiedBackend) {
	options := testOptions()
	options.ServiceFailureWindow = 80 * time.Millisecond
	options.ServiceRetryInterval = 5 * time.Millisecond
	options.QualificationTimeout = 50 * time.Millisecond
	b := &qualifiedBackend{fakeBackend: &fakeBackend{
		candidates: []Candidate{{ID: "current"}, {ID: "replacement"}}, current: "current",
		latencies: map[string]time.Duration{"current": time.Millisecond, "replacement": 2 * time.Millisecond},
		speeds:    map[string]float64{"replacement": 100},
	}}
	b.health = func(context.Context, string) error { return errors.New("service-unavailable") }
	return New(options, nil), b
}

func TestPersistentServiceFailureRetainsRouteUntilWindowAndSearchesConcurrently(t *testing.T) {
	e, b := serviceTestBackend()
	searched := make(chan struct{}, 1)
	b.qualify = func(context.Context, string) error {
		select {
		case searched <- struct{}{}:
		default:
		}
		return nil
	}
	started := time.Now()
	done := make(chan error, 1)
	go func() { done <- e.recoverServices(context.Background(), "wifi", b, b, "current") }()
	select {
	case <-searched:
	case <-time.After(time.Second):
		t.Fatal("replacement search did not start in parallel")
	}
	if b.Current() != "current" {
		t.Fatal("partial failure immediately changed the route")
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if time.Since(started) < e.Options.ServiceFailureWindow || b.Current() != "replacement" {
		t.Fatal("replacement did not wait for confirmation")
	}
}

func TestTransientServiceRecoveryCancelsReplacementSearch(t *testing.T) {
	e, b := serviceTestBackend()
	var started, cancelled atomic.Bool
	b.qualify = func(ctx context.Context, _ string) error {
		started.Store(true)
		<-ctx.Done()
		cancelled.Store(true)
		return ctx.Err()
	}
	b.health = func(context.Context, string) error { return nil }
	if err := e.recoverServices(context.Background(), "wifi", b, b, "current"); err != nil {
		t.Fatal(err)
	}
	if !started.Load() || !cancelled.Load() || b.Current() != "current" || len(b.selected) != 0 {
		t.Fatal("recovered service did not retain its route and cancel candidate work")
	}
}

func TestServiceFailureWithoutQualifiedAlternativeNeverSwitches(t *testing.T) {
	e, b := serviceTestBackend()
	b.qualify = func(context.Context, string) error { return errors.New("same-service-unavailable") }
	if err := e.recoverServices(context.Background(), "wifi", b, b, "current"); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("got %v", err)
	}
	if b.Current() != "current" || len(b.selected) != 0 {
		t.Fatal("common service outage caused a pointless switch")
	}
}

func TestCancelledServiceRecoveryCannotSelect(t *testing.T) {
	e, b := serviceTestBackend()
	ctx, cancel := context.WithCancel(context.Background())
	b.qualify = func(context.Context, string) error { cancel(); return nil }
	if err := e.recoverServices(ctx, "wifi", b, b, "current"); !errors.Is(err, context.Canceled) {
		t.Fatalf("got %v", err)
	}
	if b.Current() != "current" || len(b.selected) != 0 {
		t.Fatal("cancelled recovery changed route")
	}
}

func TestManualRouteChangeDuringServiceConfirmationCannotBeOverwritten(t *testing.T) {
	e, b := serviceTestBackend()
	b.qualify = func(context.Context, string) error {
		b.mu.Lock()
		b.current = "manual"
		b.mu.Unlock()
		return nil
	}
	if err := e.recoverServices(context.Background(), "wifi", b, b, "current"); !errors.Is(err, context.Canceled) {
		t.Fatalf("got %v", err)
	}
	if b.Current() != "manual" || len(b.selected) != 0 {
		t.Fatal("late candidate replaced the user's newer route")
	}
}

func TestPanickingServiceMonitorDoesNotHangOrConfirmFailure(t *testing.T) {
	e, b := serviceTestBackend()
	b.health = func(context.Context, string) error { panic("probe fault") }
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := e.recoverServices(ctx, "wifi", b, b, "current"); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("got %v", err)
	}
	if len(b.selected) != 0 {
		t.Fatal("probe panic was treated as confirmed failure")
	}
}

func TestCompleteConnectionFailureSkipsServiceWindow(t *testing.T) {
	e, b := serviceTestBackend()
	e.Options.ServiceFailureWindow = time.Second
	b.probe = func(_ context.Context, id string) (time.Duration, error) {
		if id == "current" {
			return 0, ErrUnavailable
		}
		return time.Millisecond, nil
	}
	started := time.Now()
	if err := e.recoverServices(context.Background(), "wifi", b, b, "current"); err != nil {
		t.Fatal(err)
	}
	if time.Since(started) >= 200*time.Millisecond || b.Current() != "replacement" {
		t.Fatal("full connection failure waited for partial-service confirmation")
	}
}

func TestRecheckWakesHealthWithoutRestartingHealthySearch(t *testing.T) {
	e, b := serviceTestBackend()
	e.Options.HealthInterval = time.Hour
	e.Options.OptimizeInterval = time.Hour
	e.Options.PoolRefreshInterval = time.Hour
	connected := make(chan struct{}, 4)
	e.Status = func(phase string) {
		if phase == "connected" {
			connected <- struct{}{}
		}
	}
	checked := make(chan struct{}, 1)
	b.health = func(context.Context, string) error { checked <- struct{}{}; return nil }
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() { e.Run(ctx, "wifi", b); close(done) }()
	for range 2 {
		select {
		case <-connected:
		case <-time.After(time.Second):
			t.Fatal("startup scan did not complete")
		}
	}
	b.mu.Lock()
	before := b.speedCalls
	b.mu.Unlock()
	e.Recheck()
	select {
	case <-checked:
	case <-time.After(time.Second):
		t.Fatal("wake signal did not interrupt health wait")
	}
	cancel()
	<-done
	if b.speedCalls != before {
		t.Fatal("healthy wake launched another speed search")
	}
}

func TestRunDoesNotReviveOldNodeAfterServiceReplacement(t *testing.T) {
	e, b := serviceTestBackend()
	e.Options.Concurrency = 1
	e.Options.HealthInterval = 5 * time.Millisecond
	e.Options.ServiceFailureWindow = 15 * time.Millisecond
	e.Options.PoolRefreshInterval = time.Hour
	b.speeds["current"] = 200
	b.health = func(_ context.Context, id string) error {
		if id == "current" {
			return errors.New("service-unavailable")
		}
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	e.Status = func(phase string) {
		if phase == "connected" && b.Current() == "replacement" {
			cancel()
		}
	}
	e.Run(ctx, "wifi", b)
	if b.Current() != "replacement" {
		t.Fatal("run did not replace the failing service route")
	}
	history := e.History.Snapshot("wifi")
	if history["current"].Alive || history["current"].Failure == "" || !history["replacement"].Alive {
		t.Fatal("old failed node was revived using replacement's successful result")
	}
}
