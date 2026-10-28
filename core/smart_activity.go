package core

import (
	"sync"
	"sync/atomic"
	"time"

	"github.com/metacubex/mihomo/tunnel/statistic"
)

const smartActivityWindow = time.Minute

var smartLastActivity atomic.Int64

func smartRecordActivity() {
	smartLastActivity.Store(time.Now().UnixNano())
}

func smartHasRecentActivity() bool {
	last := smartLastActivity.Load()
	age := time.Since(time.Unix(0, last))
	return last > 0 && age >= 0 && age < smartActivityWindow
}

func smartNotifyActivity() {
	now := time.Now()
	previous := smartLastActivity.Swap(now.UnixNano())
	if previous == 0 || now.Sub(time.Unix(0, previous)) >= smartActivityWindow {
		safeGo("smartTrafficResume", func() { SmartRecheck() })
	}
}

type smartActivitySample struct {
	mu               sync.Mutex
	upload, download int64
	initialized      bool
}

func (b *smartBackend) RecentlyActive() bool {
	b.activity.mu.Lock()
	upload, download := statistic.DefaultManager.Total()
	changed := b.activity.initialized && (upload > b.activity.upload || download > b.activity.download)
	b.activity.upload, b.activity.download = upload, download
	b.activity.initialized = true
	b.activity.mu.Unlock()
	if changed {
		smartRecordActivity()
	}
	return smartHasRecentActivity()
}

func (b *smartBackend) OptionalWorkAllowed() bool {
	return b.RecentlyActive() && !smartDNSDiagnosticActive()
}

func (b *smartBackend) BeginRecovery() bool {
	return smartBeginDiagnostic("transport")
}

func (b *smartBackend) EndRecovery() {
	smartEndDiagnostic("transport")
	smartDNS.retryPending()
	setSmartRoutingRunning(isRunning.Load())
}
