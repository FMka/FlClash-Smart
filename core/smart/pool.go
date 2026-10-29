package smart

import (
	"context"
	"sort"
	"sync"
	"time"
)

func Order(candidates []Candidate, history map[string]Record, round int) []Candidate {
	return orderCandidates(candidates, history, round, "")
}

func countryPriority(country, preferred string) int {
	if preferred == "" || country == preferred {
		return 0
	}
	if country == "" {
		return 1
	}
	return 2
}

func orderCandidates(candidates []Candidate, history map[string]Record, round int, preferred string) []Candidate {
	matching := make(map[string]Record)
	for _, candidate := range candidates {
		if record, ok := history[candidate.ID]; ok && record.Fingerprint == candidate.Fingerprint {
			matching[candidate.ID] = record
		}
	}
	pool := activePool(matching)
	var known []Candidate
	for _, candidate := range candidates {
		if _, ok := pool[candidate.ID]; ok {
			known = append(known, candidate)
		}
	}
	sort.SliceStable(known, func(i, j int) bool {
		return history[known[i].ID].Latency < history[known[j].ID].Latency
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
	for _, candidate := range known {
		add(candidate)
	}
	for _, candidate := range discovery {
		add(candidate)
	}
	sort.SliceStable(ordered, func(i, j int) bool {
		return countryPriority(ordered[i].Country, preferred) < countryPriority(ordered[j].Country, preferred)
	})
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
	responded := err == nil
	if responded && ctx.Err() == nil {
		e.History.Responded(network, candidate, latency)
	}
	if err == nil {
		if qualified, ok := backend.(QualifyingBackend); ok {
			probeCtx, cancel := context.WithTimeout(ctx, e.Options.QualificationTimeout)
			err = qualified.Qualify(probeCtx, candidate.ID)
			cancel()
		}
	}
	if country, ok := backend.(CountryBackend); ok {
		if known := country.KnownCountry(candidate.ID); known != "" {
			candidate.Country = known
		}
	}
	if ctx.Err() == nil {
		if err == nil {
			e.History.Succeeded(network, candidate, latency)
		} else if responded {
			e.History.Unqualified(network, candidate, latency, err, e.Options.FailureCooldown)
		} else {
			e.History.Failed(network, candidate, err, e.Options.FailureCooldown)
		}
	}
	return measurement{candidate, latency}, err
}

func (e *Engine) RefreshPool(ctx context.Context, network string, backend Backend) {
	if ctx.Err() != nil || !optionalWorkAllowed(backend) {
		return
	}
	if recovery, ok := backend.(RecoveryBackend); ok {
		if !recovery.BeginRecovery() {
			return
		}
		defer recovery.EndRecovery()
	}
	defer e.persist()
	history := e.History.Snapshot(network)
	candidates := backend.Candidates()
	e.History.Reconcile(network, candidates)
	matching := make(map[string]Record)
	for _, candidate := range candidates {
		if record, ok := history[candidate.ID]; ok && record.Fingerprint == candidate.Fingerprint {
			matching[candidate.ID] = record
		}
	}
	pool := activePool(matching)
	var active []Candidate
	for _, candidate := range candidates {
		if _, ok := pool[candidate.ID]; ok && !time.Now().Before(history[candidate.ID].RetryAt) {
			active = append(active, candidate)
		}
	}
	sort.SliceStable(active, func(i, j int) bool {
		if left, right := countryPriority(active[i].Country, e.Options.PreferredCountry), countryPriority(active[j].Country, e.Options.PreferredCountry); left != right {
			return left < right
		}
		return history[active[i].ID].Checked.Before(history[active[j].ID].Checked)
	})
	batchSize := max(1, e.Options.Concurrency)
	batch := append([]Candidate{}, active[:min(len(active), batchSize/2)]...)
	seen := map[string]bool{}
	for _, candidate := range batch {
		seen[candidate.ID] = true
	}
	discovery := spread(candidates, e.round)
	sort.SliceStable(discovery, func(i, j int) bool {
		return countryPriority(discovery[i].Country, e.Options.PreferredCountry) < countryPriority(discovery[j].Country, e.Options.PreferredCountry)
	})
	for _, candidate := range discovery {
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
	var found []measurement
	var mu sync.Mutex
	e.runWorkers(probeCtx, batch, func(candidate Candidate) {
		if discovery, ok := backend.(DiscoveryBackend); ok {
			discovery.Discover(probeCtx, candidate.ID)
		}
		result, err := e.probeCandidate(probeCtx, network, backend, candidate)
		if err == nil {
			mu.Lock()
			found = append(found, result)
			mu.Unlock()
		}
	})
	if ctx.Err() == nil && e.Options.PreferredCountry != "" {
		current := backend.Current()
		if history[current].Country != e.Options.PreferredCountry {
			sort.SliceStable(found, func(i, j int) bool { return found[i].latency < found[j].latency })
			for _, result := range found {
				if result.candidate.Country == e.Options.PreferredCountry {
					_ = e.pick(ctx, backend, result.candidate.ID, "preferred-country-restored")
					break
				}
			}
		}
	}
}
