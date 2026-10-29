package smart

import (
	"context"
	"math"
	"sort"
	"time"
)

type measurement struct {
	candidate Candidate
	latency   time.Duration
}

type scanRequest struct {
	force       bool
	provisional bool
	exclude     string
}

func (e *Engine) Scan(ctx context.Context, network string, backend Backend, force bool) error {
	defer e.persist()
	winner, err := e.find(ctx, network, backend, scanRequest{force: force, provisional: force})
	if err != nil {
		return err
	}
	return e.pick(ctx, backend, winner, "optimized")
}

func (e *Engine) pick(ctx context.Context, backend Backend, id, reason string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := backend.Select(ctx, id, reason); err != nil {
		return err
	}
	if id != e.lastPick {
		e.lastPick, e.pickedAt = id, time.Now()
	}
	e.report("connected")
	return nil
}

func (e *Engine) find(ctx context.Context, network string, backend Backend, request scanRequest) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	e.report("searching")
	available := backend.Candidates()
	e.History.Reconcile(network, available)
	candidates := orderCandidates(available, e.History.Snapshot(network), e.round, e.Options.PreferredCountry)
	current := backend.Current()
	filtered := candidates[:0]
	for _, candidate := range candidates {
		if candidate.ID != request.exclude {
			filtered = append(filtered, candidate)
		}
	}
	candidates = filtered
	if !request.force {
		for index, candidate := range candidates {
			if candidate.ID == current {
				copy(candidates[1:index+1], candidates[:index])
				candidates[0] = candidate
				break
			}
		}
	}
	e.round += max(1, e.Options.Concurrency)
	if len(candidates) == 0 {
		return "", ErrUnavailable
	}
	scanCtx, stop := context.WithTimeout(ctx, e.Options.ScanBudget)
	defer stop()
	if e.Options.PreferredCountry != "" {
		var preferred, rest []Candidate
		for _, candidate := range candidates {
			if candidate.Country == e.Options.PreferredCountry {
				preferred = append(preferred, candidate)
			} else {
				rest = append(rest, candidate)
			}
		}
		if len(preferred) > 0 && len(rest) > 0 {
			found, err := e.collect(scanCtx, network, backend, preferred, request.provisional, e.Options.ScanBudget/2)
			if err == nil {
				return e.choose(ctx, network, backend, found, request.force)
			}
			if scanCtx.Err() != nil {
				return "", scanCtx.Err()
			}
			candidates = rest
		}
	}
	found, err := e.collect(scanCtx, network, backend, candidates, request.provisional)
	if err != nil {
		return "", err
	}
	return e.choose(ctx, network, backend, found, request.force)
}

func (e *Engine) collect(ctx context.Context, network string, backend Backend, candidates []Candidate, provisional bool, budgets ...time.Duration) ([]measurement, error) {
	budget := e.Options.ScanBudget
	if len(budgets) > 0 {
		budget = budgets[0]
	}
	scanCtx, cancel := context.WithTimeout(ctx, budget)
	defer cancel()
	results := make(chan measurement, max(1, e.Options.Concurrency))
	done := make(chan struct{})
	e.spawn(func() {
		defer close(done)
		defer close(results)
		e.runWorkers(scanCtx, candidates, func(candidate Candidate) {
			result, err := e.probeCandidate(scanCtx, network, backend, candidate)
			if err == nil && scanCtx.Err() == nil {
				select {
				case results <- result:
				case <-scanCtx.Done():
				}
			}
		})
	})
	defer func() { cancel(); <-done }()
	var found []measurement
	var collect <-chan time.Time
	var timer *time.Timer
	defer func() {
		if timer != nil {
			timer.Stop()
		}
	}()
	for {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-scanCtx.Done():
			return foundOrUnavailable(found)
		case <-collect:
			return foundOrUnavailable(found)
		case result, ok := <-results:
			if !ok {
				return foundOrUnavailable(found)
			}
			found = append(found, result)
			if len(found) == 1 {
				if provisional {
					if err := e.pick(ctx, backend, result.candidate.ID, "recovery"); err != nil {
						return nil, err
					}
					e.persist()
				}
				timer = time.NewTimer(e.Options.CollectWindow)
				collect = timer.C
			}
		}
	}
}

func foundOrUnavailable(found []measurement) ([]measurement, error) {
	if len(found) == 0 {
		return nil, ErrUnavailable
	}
	return found, nil
}

func (e *Engine) choose(ctx context.Context, network string, backend Backend, found []measurement, force bool) (string, error) {
	sort.SliceStable(found, func(i, j int) bool {
		if left, right := countryPriority(found[i].candidate.Country, e.Options.PreferredCountry), countryPriority(found[j].candidate.Country, e.Options.PreferredCountry); left != right {
			return left < right
		}
		return found[i].latency < found[j].latency
	})
	current, history := backend.Current(), e.History.Snapshot(network)
	fresh := map[string]bool{}
	for _, result := range found {
		fresh[result.candidate.ID] = true
	}
	shortlist := make([]measurement, 0, max(1, e.Options.SpeedCandidates))
	for _, candidate := range found {
		if countryPriority(candidate.candidate.Country, e.Options.PreferredCountry) != countryPriority(found[0].candidate.Country, e.Options.PreferredCountry) {
			break
		}
		if candidate.latency > found[0].latency+e.Options.LatencyWindow {
			break
		}
		shortlist = append(shortlist, candidate)
		if len(shortlist) >= max(1, e.Options.SpeedCandidates) {
			break
		}
	}
	if !force {
		for _, candidate := range found {
			if candidate.candidate.ID != current || countryPriority(candidate.candidate.Country, e.Options.PreferredCountry) != countryPriority(found[0].candidate.Country, e.Options.PreferredCountry) || candidate.latency > found[0].latency+e.Options.LatencyWindow {
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
			return "", err
		}
		id := candidate.candidate.ID
		record := history[id]
		if !e.speedFresh(record) && optionalWorkAllowed(backend) {
			speedCtx, stop := context.WithTimeout(ctx, e.Options.SpeedTimeout)
			speed, err := backend.Speed(speedCtx, id, e.Options.SpeedBytes)
			stop()
			if ctx.Err() != nil {
				return "", ctx.Err()
			}
			if err == nil && speed > 0 && !math.IsNaN(speed) && !math.IsInf(speed, 0) {
				record.BytesPerSec, record.SpeedAt, record.SpeedContext = speed, time.Now(), e.Options.NetworkContext
				history[id] = record
				e.History.Put(network, id, record)
			}
		}
		if e.speedFresh(record) && record.BytesPerSec > bestSpeed {
			winner, bestSpeed = id, record.BytesPerSec
		}
	}
	if !force && current != winner && countryPriority(history[current].Country, e.Options.PreferredCountry) <= countryPriority(history[winner].Country, e.Options.PreferredCountry) {
		if record, alive := history[current]; alive && fresh[current] {
			if bestSpeed == 0 && record.Latency <= found[0].latency+e.Options.LatencyWindow {
				winner = current
			}
			if current == e.lastPick && time.Since(e.pickedAt) < e.Options.MinimumHold {
				winner = current
			}
			if e.speedFresh(record) && bestSpeed < record.BytesPerSec*(1+e.Options.MinimumSpeedGain) {
				winner = current
			}
		}
	}
	return winner, ctx.Err()
}

func (e *Engine) speedFresh(record Record) bool {
	return (e.Options.NetworkContext == "" || record.SpeedContext == e.Options.NetworkContext) && speedFresh(record, e.Options.SpeedTTL)
}

func speedFresh(record Record, ttl time.Duration) bool {
	age := time.Since(record.SpeedAt)
	return record.BytesPerSec > 0 && age >= 0 && age < ttl
}
