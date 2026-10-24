package smart

import (
	"context"
	"time"
)

type replacement struct {
	id  string
	err error
}

type healthOutcome struct {
	recovered bool
	full      bool
	latency   time.Duration
	err       error
}

func (e *Engine) recoverServices(ctx context.Context, network string, backend Backend, qualified QualifyingBackend, current string) error {
	e.report("service-confirming")
	searchCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	searchResult := make(chan replacement, 1)
	searchDone := make(chan struct{})
	e.spawn(func() {
		result := replacement{err: ErrUnavailable}
		defer func() { searchResult <- result; close(searchDone) }()
		result.id, result.err = e.find(searchCtx, network, backend, scanRequest{force: true, exclude: current})
	})
	windowCtx, stopWindow := context.WithTimeout(ctx, e.Options.ServiceFailureWindow)
	defer stopWindow()
	healthResult := make(chan healthOutcome, 1)
	healthDone := make(chan struct{})
	e.spawn(func() {
		result := healthOutcome{err: ErrUnavailable}
		defer func() { healthResult <- result; close(healthDone) }()
		result = e.monitorService(windowCtx, backend, qualified, current)
	})
	defer func() {
		cancel()
		stopWindow()
		<-searchDone
		<-healthDone
		e.persist()
	}()
	var found replacement
	ready, confirmed := false, false
	for !confirmed || !ready {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case result := <-searchResult:
			found, ready = result, true
			searchResult = nil
		case result := <-healthResult:
			healthResult = nil
			if result.err != nil {
				return result.err
			}
			if result.recovered {
				for _, candidate := range backend.Candidates() {
					if candidate.ID == current {
						e.History.Succeeded(network, candidate, result.latency)
						break
					}
				}
				e.report("connected")
				return nil
			}
			confirmed = true
			if result.full {
				e.report("recovering")
			}
		}
	}
	if found.err != nil {
		return found.err
	}
	if backend.Current() != current {
		return context.Canceled
	}
	for _, candidate := range backend.Candidates() {
		if candidate.ID == found.id {
			probeCtx, stop := context.WithTimeout(ctx, e.Options.ProbeTimeout+e.Options.QualificationTimeout)
			_, err := e.probeCandidate(probeCtx, network, backend, candidate)
			stop()
			if err != nil {
				return err
			}
			return e.pick(ctx, backend, found.id, "service-recovery")
		}
	}
	return ErrUnavailable
}

func (e *Engine) monitorService(ctx context.Context, backend Backend, qualified QualifyingBackend, current string) healthOutcome {
	for wait(ctx, e.Options.ServiceRetryInterval) {
		latency, err := e.latency(ctx, backend, current)
		if ctx.Err() != nil {
			return healthOutcome{}
		}
		if err != nil {
			return healthOutcome{full: true}
		}
		probeCtx, cancel := context.WithTimeout(ctx, e.Options.QualificationTimeout)
		err = qualified.Health(probeCtx, current)
		cancel()
		if ctx.Err() != nil {
			return healthOutcome{}
		}
		if err == nil {
			return healthOutcome{recovered: true, latency: latency}
		}
	}
	return healthOutcome{}
}
