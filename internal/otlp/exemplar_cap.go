package otlp

import (
	"sync"
	"time"
)

// exemplarRateLimiter — the v0.8.433 (exemplar audit Faz C) per-series×
// minute ingest cap. One counter map per wall-clock minute: when the
// minute advances the map is dropped wholesale, so memory is bounded by
// the number of DISTINCT exemplar-bearing series active within a single
// minute (not by lifetime cardinality). The audit's worst-case math
// (~864M rows/day at 100k series × 10s exports) is what this caps when
// an SDK misbehaves; the normal producer-bounded case never hits it.
type exemplarRateLimiter struct {
	mu     sync.Mutex
	max    int
	minute int64 // unix minute the counts map belongs to
	counts map[uint64]int
}

func newExemplarRateLimiter(max int) *exemplarRateLimiter {
	return &exemplarRateLimiter{max: max, counts: make(map[uint64]int)}
}

// allow reports whether one more exemplar for fp fits inside the
// current minute's budget, counting it if so.
func (l *exemplarRateLimiter) allow(fp uint64, now time.Time) bool {
	min := now.Unix() / 60
	l.mu.Lock()
	defer l.mu.Unlock()
	if min != l.minute {
		l.minute = min
		l.counts = make(map[uint64]int)
	}
	if l.counts[fp] >= l.max {
		return false
	}
	l.counts[fp]++
	return true
}
