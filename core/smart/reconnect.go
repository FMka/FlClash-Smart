package smart

import (
	"context"
	"errors"
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

type QualifyingBackend interface {
	Qualify(context.Context, string) error
	Health(context.Context, string) error
}

type ActivityBackend interface {
	RecentlyActive() bool
	OptionalWorkAllowed() bool
}

type RecoveryBackend interface {
	BeginRecovery() bool
	EndRecovery()
}

type PolicyBackend interface {
	PolicyFailure(error) bool
	Block(context.Context, string) error
}

type DiscoveryBackend interface {
	Discover(context.Context, string)
}

type Options struct {
	Concurrency          int
	ProbeTimeout         time.Duration
	QualificationTimeout time.Duration
	ScanBudget           time.Duration
	CollectWindow        time.Duration
	LatencyWindow        time.Duration
	HealthInterval       time.Duration
	OptimizeInterval     time.Duration
	MinimumHold          time.Duration
	SpeedTimeout         time.Duration
	SpeedTTL             time.Duration
	SpeedCandidates      int
	SpeedBytes           int64
	MinimumSpeedGain     float64
	PoolSize             int
	PoolRefreshInterval  time.Duration
	FailureCooldown      time.Duration
	ServiceFailureWindow time.Duration
	ServiceRetryInterval time.Duration
	IdleInterval         time.Duration
}

func DefaultOptions() Options {
	return Options{
		Concurrency: 8, ProbeTimeout: 3 * time.Second,
		QualificationTimeout: 12 * time.Second,
		ScanBudget:           24 * time.Second, CollectWindow: time.Second,
		LatencyWindow: 15 * time.Millisecond, HealthInterval: 15 * time.Second,
		OptimizeInterval: 5 * time.Minute, MinimumHold: time.Minute,
		SpeedTimeout: 3 * time.Second, SpeedTTL: 30 * time.Minute,
		SpeedCandidates: 3, SpeedBytes: 512 * 1024, MinimumSpeedGain: 0.20,
		PoolSize: 20, PoolRefreshInterval: 2 * time.Minute,
		FailureCooldown: time.Minute, ServiceFailureWindow: 15 * time.Second,
		ServiceRetryInterval: 3 * time.Second,
		IdleInterval:         2 * time.Minute,
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
	wake     chan struct{}
}

func New(options Options, history *History) *Engine {
	if history == nil {
		history = &History{}
	}
	return &Engine{Options: options, History: history, wake: make(chan struct{}, 1)}
}

func (e *Engine) Recheck() {
	select {
	case e.wake <- struct{}{}:
	default:
	}
}

func (e *Engine) report(phase string) {
	if e.Status != nil {
		e.Status(phase)
	}
}

func (e *Engine) persist() {
	if e.Save != nil {
		e.Save()
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
	optimizeAt, poolAt := time.Time{}, time.Time{}
	backoff, force := 3*time.Second, true
	initial, explicit := true, false
	for ctx.Err() == nil {
		if !initial && !explicit && !recentlyActive(backend) {
			event := e.waitHealthEvent(ctx, e.idleInterval())
			if event == waitCancelled {
				return
			}
			explicit = event == waitRequested
			if event == waitElapsed && !recentlyActive(backend) {
				current := backend.Current()
				_, err, _, admitted := e.checkCurrent(ctx, backend, current, false)
				if ctx.Err() != nil {
					return
				}
				if admitted && err != nil {
					e.failCurrent(network, backend, current, err)
					force, explicit = true, true
				}
			}
			continue
		}
		requested := explicit
		initial, explicit = false, false
		if force || (optionalWorkAllowed(backend) && time.Since(optimizeAt) >= e.Options.OptimizeInterval) {
			err, admitted := e.scanWithRecovery(ctx, network, backend, force)
			if !admitted {
				event := e.waitHealthEvent(ctx, e.Options.HealthInterval)
				if event == waitCancelled {
					return
				}
				explicit = event == waitRequested
				continue
			}
			if ctx.Err() != nil {
				return
			}
			if err != nil {
				e.report("unavailable")
				event := e.waitHealthEvent(ctx, backoff)
				if event == waitCancelled {
					return
				}
				explicit = event == waitRequested
				backoff, force = min(backoff*2, time.Minute), true
				continue
			}
			optimizeAt, poolAt, backoff, force = time.Now(), time.Now(), 3*time.Second, false
		}
		event := waitRequested
		if !requested {
			event = e.waitHealthEvent(ctx, e.Options.HealthInterval)
		}
		if event == waitCancelled {
			return
		}
		if event != waitRequested && !recentlyActive(backend) {
			continue
		}
		current := backend.Current()
		latency, err, serviceErr, admitted := e.checkCurrent(ctx, backend, current, true)
		if !admitted {
			continue
		}
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			e.failCurrent(network, backend, current, err)
			force, explicit = true, true
			continue
		}
		healthy := true
		if qualified, ok := backend.(QualifyingBackend); ok {
			err = serviceErr
			healthy = err == nil
			if ctx.Err() != nil {
				return
			}
			if err != nil {
				e.failCurrent(network, backend, current, err)
				if policy, ok := backend.(PolicyBackend); ok && policy.PolicyFailure(err) {
					if blockErr := policy.Block(ctx, err.Error()); blockErr != nil {
						return
					}
					force, explicit = true, true
					continue
				}
				if err = e.recoverServicesCoordinated(ctx, network, backend, qualified, current); err != nil {
					if ctx.Err() != nil || errors.Is(err, context.Canceled) {
						return
					}
					e.report("service-unavailable")
				}
				optimizeAt = time.Now()
			}
		}
		if healthy {
			for _, candidate := range backend.Candidates() {
				if candidate.ID == current {
					e.History.Succeeded(network, candidate, latency)
					break
				}
			}
			e.persist()
		}
		if optionalWorkAllowed(backend) && time.Since(poolAt) >= e.Options.PoolRefreshInterval {
			e.RefreshPool(ctx, network, backend)
			poolAt = time.Now()
		}
	}
}

func recentlyActive(backend Backend) bool {
	activity, ok := backend.(ActivityBackend)
	return !ok || activity.RecentlyActive()
}

func optionalWorkAllowed(backend Backend) bool {
	activity, ok := backend.(ActivityBackend)
	return !ok || activity.OptionalWorkAllowed()
}

func (e *Engine) idleInterval() time.Duration {
	if e.Options.IdleInterval > 0 {
		return e.Options.IdleInterval
	}
	return 2 * time.Minute
}

func (e *Engine) scanWithRecovery(ctx context.Context, network string, backend Backend, force bool) (error, bool) {
	if recovery, ok := backend.(RecoveryBackend); ok {
		if !recovery.BeginRecovery() {
			return nil, false
		}
		defer recovery.EndRecovery()
	}
	return e.Scan(ctx, network, backend, force), true
}

func (e *Engine) checkCurrent(ctx context.Context, backend Backend, current string, qualify bool) (time.Duration, error, error, bool) {
	if recovery, ok := backend.(RecoveryBackend); ok {
		if !recovery.BeginRecovery() {
			return 0, nil, nil, false
		}
		defer recovery.EndRecovery()
	}
	latency, err := e.latency(ctx, backend, current)
	if err != nil {
		return latency, err, nil, true
	}
	if !qualify {
		return latency, nil, nil, true
	}
	if qualified, ok := backend.(QualifyingBackend); ok {
		probeCtx, cancel := context.WithTimeout(ctx, e.Options.QualificationTimeout)
		defer cancel()
		return latency, nil, qualified.Health(probeCtx, current), true
	}
	return latency, nil, nil, true
}

func (e *Engine) recoverServicesCoordinated(ctx context.Context, network string, backend Backend, qualified QualifyingBackend, current string) error {
	if recovery, ok := backend.(RecoveryBackend); ok {
		if !recovery.BeginRecovery() {
			return nil
		}
		defer recovery.EndRecovery()
	}
	return e.recoverServices(ctx, network, backend, qualified, current)
}

func (e *Engine) failCurrent(network string, backend Backend, current string, err error) {
	for _, candidate := range backend.Candidates() {
		if candidate.ID == current {
			e.History.Failed(network, candidate, err, e.Options.FailureCooldown)
			break
		}
	}
	e.persist()
}

func (e *Engine) latency(ctx context.Context, backend Backend, id string) (time.Duration, error) {
	probeCtx, cancel := context.WithTimeout(ctx, e.Options.ProbeTimeout)
	defer cancel()
	return backend.Latency(probeCtx, id)
}

func (e *Engine) waitHealth(ctx context.Context, delay time.Duration) bool {
	return e.waitHealthEvent(ctx, delay) != waitCancelled
}

type waitEvent uint8

const (
	waitCancelled waitEvent = iota
	waitElapsed
	waitRequested
)

func (e *Engine) waitHealthEvent(ctx context.Context, delay time.Duration) waitEvent {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return waitCancelled
	case <-e.wake:
		return waitRequested
	case <-timer.C:
		return waitElapsed
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

func (e *Engine) runWorkers(ctx context.Context, candidates []Candidate, run func(Candidate)) {
	jobs := make(chan Candidate)
	var workers sync.WaitGroup
	for range min(max(1, e.Options.Concurrency), len(candidates)) {
		workers.Add(1)
		e.spawn(func() {
			defer workers.Done()
			for candidate := range jobs {
				run(candidate)
			}
		})
	}
	for _, candidate := range candidates {
		select {
		case jobs <- candidate:
		case <-ctx.Done():
			close(jobs)
			workers.Wait()
			return
		}
	}
	close(jobs)
	workers.Wait()
}
