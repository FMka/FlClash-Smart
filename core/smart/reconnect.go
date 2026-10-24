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
	for ctx.Err() == nil {
		if force || time.Since(optimizeAt) >= e.Options.OptimizeInterval {
			err := e.Scan(ctx, network, backend, force)
			if ctx.Err() != nil {
				return
			}
			if err != nil {
				e.report("unavailable")
				if !e.waitHealth(ctx, backoff) {
					return
				}
				backoff, force = min(backoff*2, time.Minute), true
				continue
			}
			optimizeAt, poolAt, backoff, force = time.Now(), time.Now(), 3*time.Second, false
		}
		if !e.waitHealth(ctx, e.Options.HealthInterval) {
			return
		}
		current := backend.Current()
		latency, err := e.latency(ctx, backend, current)
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			e.failCurrent(network, backend, current, err)
			force = true
			continue
		}
		healthy := true
		if qualified, ok := backend.(QualifyingBackend); ok {
			probeCtx, cancel := context.WithTimeout(ctx, e.Options.QualificationTimeout)
			err = qualified.Health(probeCtx, current)
			cancel()
			healthy = err == nil
			if ctx.Err() != nil {
				return
			}
			if err != nil {
				e.failCurrent(network, backend, current, err)
				if err = e.recoverServices(ctx, network, backend, qualified, current); err != nil {
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
		if time.Since(poolAt) >= e.Options.PoolRefreshInterval {
			e.RefreshPool(ctx, network, backend)
			poolAt = time.Now()
		}
	}
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
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-e.wake:
		return true
	case <-timer.C:
		return true
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
