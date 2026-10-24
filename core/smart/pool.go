package smart

import (
	"context"
	"sort"
	"time"
)

func Order(candidates []Candidate, history map[string]Record, round int) []Candidate {
	return orderCandidates(candidates, history, round, 20)
}

func orderCandidates(candidates []Candidate, history map[string]Record, round, poolSize int) []Candidate {
	matching := make(map[string]Record)
	for _, candidate := range candidates {
		if record, ok := history[candidate.ID]; ok && record.Fingerprint == candidate.Fingerprint {
			matching[candidate.ID] = record
		}
	}
	pool := activePool(matching, poolSize)
	var known []Candidate
	for _, candidate := range candidates {
		if _, ok := pool[candidate.ID]; ok {
			known = append(known, candidate)
		}
	}
	sort.SliceStable(known, func(i, j int) bool {
		return history[known[i].ID].Checked.After(history[known[j].ID].Checked)
	})
	seen := make(map[string]bool, len(candidates))
	ordered := make([]Candidate, 0, len(candidates))
	add := func(candidate Candidate) {
		if candidate.ID != "" && !seen[candidate.ID] {
			record, ok := history[candidate.ID]
			if ok && record.Fingerprint == candidate.Fingerprint && time.Now().Before(record.RetryAt) {
				return
			}
			ordered, seen[candidate.ID] = append(ordered, candidate), true
		}
	}
	for _, candidate := range known[:min(4, len(known))] {
		add(candidate)
	}
	var discovery []Candidate
	knownIDs := make(map[string]bool, len(known))
	for _, candidate := range known {
		knownIDs[candidate.ID] = true
	}
	for _, candidate := range spread(candidates, round) {
		if !knownIDs[candidate.ID] {
			discovery = append(discovery, candidate)
		}
	}
	for _, candidate := range discovery[:min(4, len(discovery))] {
		add(candidate)
	}
	for _, candidate := range known {
		add(candidate)
	}
	for _, candidate := range discovery {
		add(candidate)
	}
	return ordered
}

// Distributed positions reach late LTE nodes without waiting behind a dead prefix.
func spread(candidates []Candidate, round int) []Candidate {
	n := len(candidates)
	if n == 0 {
		return nil
	}
	round = ((round % n) + n) % n
	ordered := make([]Candidate, 0, n)
	seen := make(map[string]bool, n)
	add := func(candidate Candidate) {
		if !seen[candidate.ID] {
			seen[candidate.ID] = true
			ordered = append(ordered, candidate)
		}
	}
	add(candidates[round])
	add(candidates[(n-1+round)%n])
	stride := max(1, int(float64(n)*0.61803398875))
	for gcd(stride, n) != 1 {
		stride++
	}
	for i := range n {
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

func (e *Engine) probeCandidate(ctx context.Context, network string, backend Backend, candidate Candidate) (measurement, error) {
	latency, err := e.latency(ctx, backend, candidate.ID)
	if err == nil && latency < 0 {
		err = ErrUnavailable
	}
	if err == nil {
		if qualified, ok := backend.(QualifyingBackend); ok {
			probeCtx, cancel := context.WithTimeout(ctx, e.Options.QualificationTimeout)
			err = qualified.Qualify(probeCtx, candidate.ID)
			cancel()
		}
	}
	if ctx.Err() == nil {
		if err == nil {
			e.History.Succeeded(network, candidate, latency)
		} else {
			e.History.Failed(network, candidate, err, e.Options.FailureCooldown)
		}
	}
	return measurement{candidate, latency}, err
}

func (e *Engine) RefreshPool(ctx context.Context, network string, backend Backend) {
	if ctx.Err() != nil {
		return
	}
	defer e.persist()
	history := e.History.Snapshot(network)
	candidates := backend.Candidates()
	matching := make(map[string]Record)
	for _, candidate := range candidates {
		if record, ok := history[candidate.ID]; ok && record.Fingerprint == candidate.Fingerprint {
			matching[candidate.ID] = record
		}
	}
	pool := activePool(matching, e.Options.PoolSize)
	var active []Candidate
	for _, candidate := range candidates {
		if _, ok := pool[candidate.ID]; ok {
			active = append(active, candidate)
		}
	}
	sort.SliceStable(active, func(i, j int) bool {
		return history[active[i].ID].Checked.Before(history[active[j].ID].Checked)
	})
	batchSize := max(1, e.Options.Concurrency)
	batch := append([]Candidate{}, active[:min(len(active), batchSize/2)]...)
	seen := map[string]bool{}
	for _, candidate := range batch {
		seen[candidate.ID] = true
	}
	for _, candidate := range spread(candidates, e.round) {
		record, ok := history[candidate.ID]
		if seen[candidate.ID] || (ok && record.Fingerprint == candidate.Fingerprint && time.Now().Before(record.RetryAt)) {
			continue
		}
		batch = append(batch, candidate)
		seen[candidate.ID] = true
		if len(batch) >= batchSize {
			break
		}
	}
	e.round += batchSize
	probeCtx, cancel := context.WithTimeout(ctx, e.Options.ScanBudget)
	defer cancel()
	e.runWorkers(probeCtx, batch, func(candidate Candidate) {
		e.probeCandidate(probeCtx, network, backend, candidate)
	})
}
