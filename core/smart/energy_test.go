package smart

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type idleBackend struct {
	*fakeBackend
	active    atomic.Bool
	health    atomic.Int64
	qualified atomic.Int64
}

func (b *idleBackend) RecentlyActive() bool                  { return b.active.Load() }
func (b *idleBackend) OptionalWorkAllowed() bool             { return b.active.Load() }
func (b *idleBackend) Qualify(context.Context, string) error { b.qualified.Add(1); return nil }
func (b *idleBackend) Health(context.Context, string) error  { b.health.Add(1); return nil }

func TestIdleDefersOptionalProbesAndRecheckStillChecksCurrent(t *testing.T) {
	b := &idleBackend{fakeBackend: &fakeBackend{
		candidates: []Candidate{{ID: "node"}},
		latencies:  map[string]time.Duration{"node": time.Millisecond},
		speeds:     map[string]float64{"node": 100},
	}}
	options := testOptions()
	options.HealthInterval = time.Hour
	options.OptimizeInterval = time.Millisecond
	options.PoolRefreshInterval = time.Millisecond
	options.IdleInterval = 5 * time.Millisecond
	e := New(options, nil)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	connected, done := make(chan struct{}), make(chan struct{})
	var once sync.Once
	e.Status = func(phase string) {
		if phase == "connected" {
			once.Do(func() { close(connected) })
		}
	}
	go func() { e.Run(ctx, "wifi", b); close(done) }()
	select {
	case <-connected:
	case <-time.After(time.Second):
		t.Fatal("idle startup did not restore route")
	}
	time.Sleep(35 * time.Millisecond)
	if b.health.Load() != 0 {
		t.Fatal("idle triggered full service health probes")
	}
	b.mu.Lock()
	before := b.speedCalls
	b.mu.Unlock()
	if before != 0 {
		t.Fatal("idle startup downloaded speed payload")
	}
	e.Recheck()
	deadline := time.Now().Add(time.Second)
	for b.health.Load() == 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if b.health.Load() == 0 {
		t.Fatal("wake did not check current despite long health interval")
	}
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("idle engine ignored Stop")
	}
	if b.speedCalls != before {
		t.Fatal("wake performed optional speed test")
	}
}

func TestIdleRetainsOnlySparseEssentialLatencyChecks(t *testing.T) {
	b := &idleBackend{fakeBackend: &fakeBackend{candidates: []Candidate{{ID: "node"}}}}
	probes := make(chan time.Time, 16)
	b.probe = func(context.Context, string) (time.Duration, error) {
		probes <- time.Now()
		return time.Millisecond, nil
	}
	options := testOptions()
	options.CollectWindow = time.Millisecond
	options.HealthInterval = time.Millisecond
	options.IdleInterval = 20 * time.Millisecond
	options.OptimizeInterval = time.Millisecond
	options.PoolRefreshInterval = time.Millisecond
	e := New(options, nil)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() { e.Run(ctx, "wifi", b); close(done) }()
	times := []time.Time{}
	for len(times) < 3 {
		select {
		case at := <-probes:
			times = append(times, at)
		case <-time.After(time.Second):
			t.Fatal("idle essential check did not run")
		}
	}
	cancel()
	<-done
	if times[2].Sub(times[1]) < 15*time.Millisecond {
		t.Fatal("idle essential checks ran at active frequency")
	}
	if b.health.Load() != 0 || b.qualified.Load() != 1 || b.speedCalls != 0 {
		t.Fatalf("idle performed optional work: health=%d qualify=%d speed=%d", b.health.Load(), b.qualified.Load(), b.speedCalls)
	}
}

func TestIdleEssentialFailureRecoversWithoutTrafficSignal(t *testing.T) {
	b := &idleBackend{fakeBackend: &fakeBackend{candidates: []Candidate{{ID: "old"}, {ID: "backup"}}}}
	var failed atomic.Bool
	var oldProbes atomic.Int64
	b.probe = func(ctx context.Context, id string) (time.Duration, error) {
		if id == "old" {
			if oldProbes.Add(1) > 1 {
				failed.Store(true)
				return 0, ErrUnavailable
			}
			return time.Millisecond, nil
		}
		if !failed.Load() {
			<-ctx.Done()
			return 0, ctx.Err()
		}
		return time.Millisecond, nil
	}
	options := testOptions()
	options.CollectWindow = time.Millisecond
	options.HealthInterval = time.Millisecond
	options.IdleInterval = 10 * time.Millisecond
	e := New(options, nil)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	e.Status = func(phase string) {
		if phase == "connected" && b.Current() == "backup" {
			cancel()
		}
	}
	e.Run(ctx, "wifi", b)
	if !failed.Load() || b.Current() != "backup" {
		t.Fatalf("quiet failed route did not recover: %v", b.selected)
	}
	if b.speedCalls != 0 || b.health.Load() != 0 {
		t.Fatal("idle repair started optional speed or periodic service checks")
	}
}

func TestIdleRefreshCannotProbePoolButActiveTrafficCan(t *testing.T) {
	b := &idleBackend{fakeBackend: &fakeBackend{candidates: []Candidate{{ID: "node"}}, latencies: map[string]time.Duration{"node": time.Millisecond}}}
	e := New(testOptions(), nil)
	e.RefreshPool(context.Background(), "wifi", b)
	if len(e.History.Snapshot("wifi")) != 0 {
		t.Fatal("idle refreshed pool")
	}
	b.active.Store(true)
	e.RefreshPool(context.Background(), "wifi", b)
	if !e.History.Snapshot("wifi")["node"].Alive {
		t.Fatal("active traffic did not refresh pool")
	}
}

type gatedBackend struct {
	*fakeBackend
	allowed      bool
	begins, ends int
}

func (b *gatedBackend) BeginRecovery() bool { b.begins++; return b.allowed }
func (b *gatedBackend) EndRecovery()        { b.ends++ }

func TestRecoveryDoesNotRaceOtherDiagnosticAndReleasesGateOnStop(t *testing.T) {
	b := &gatedBackend{fakeBackend: &fakeBackend{candidates: []Candidate{{ID: "node"}}, latencies: map[string]time.Duration{"node": time.Millisecond}}}
	e := New(testOptions(), nil)
	if err, admitted := e.scanWithRecovery(context.Background(), "wifi", b, true); err != nil || admitted || b.ends != 0 || b.Current() != "" {
		t.Fatal("busy diagnostic gate admitted transport scan")
	}
	b.allowed = true
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err, admitted := e.scanWithRecovery(ctx, "wifi", b, true); err == nil || !admitted || b.ends != 1 {
		t.Fatal("cancelled recovery did not release gate")
	}
}

type policyBackend struct {
	*fakeBackend
	rejected atomic.Bool
}

func (b *policyBackend) Qualify(_ context.Context, id string) error {
	if id == "old" && b.rejected.Load() {
		return errors.New("country-excluded:RU")
	}
	return nil
}
func (b *policyBackend) Health(_ context.Context, id string) error {
	if id == "old" {
		b.rejected.Store(true)
		return errors.New("country-excluded:RU")
	}
	return nil
}
func (b *policyBackend) PolicyFailure(err error) bool {
	return err != nil && err.Error() == "country-excluded:RU"
}
func (b *policyBackend) Block(ctx context.Context, reason string) error {
	return b.Select(ctx, "REJECT", reason)
}

func TestCountryPolicyFailureBlocksImmediatelyWithoutServiceGrace(t *testing.T) {
	b := &policyBackend{fakeBackend: &fakeBackend{
		candidates: []Candidate{{ID: "old"}, {ID: "new"}},
		latencies:  map[string]time.Duration{"old": time.Millisecond, "new": 100 * time.Millisecond},
	}}
	b.probe = func(ctx context.Context, id string) (time.Duration, error) {
		if id == "new" && !b.rejected.Load() {
			<-ctx.Done()
			return 0, ctx.Err()
		}
		return b.latencies[id], nil
	}
	options := testOptions()
	options.HealthInterval = time.Millisecond
	options.ServiceFailureWindow = time.Hour
	e := New(options, nil)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	e.Status = func(phase string) {
		if phase == "connected" && b.Current() == "new" {
			cancel()
		}
	}
	e.Run(ctx, "wifi", b)
	blocked := false
	for _, id := range b.selected {
		if id == "REJECT" {
			blocked = true
		}
		if id == "new" && !blocked {
			t.Fatal("policy violation retained old traffic while replacing")
		}
	}
	if !blocked || b.Current() != "new" {
		t.Fatalf("country policy failed to recover: %v", b.selected)
	}
}

type discoveryBackend struct {
	*idleBackend
	discovered atomic.Int64
}

func (b *discoveryBackend) Discover(context.Context, string) { b.discovered.Add(1) }

func TestOptionalCountryDiscoveryIsIndependentOfLatencyAdmission(t *testing.T) {
	b := &discoveryBackend{idleBackend: &idleBackend{fakeBackend: &fakeBackend{candidates: []Candidate{{ID: "unavailable"}}}}}
	b.active.Store(true)
	New(testOptions(), nil).RefreshPool(context.Background(), "wifi", b)
	if b.discovered.Load() != 1 {
		t.Fatal("country discovery was skipped after latency failure")
	}
}
