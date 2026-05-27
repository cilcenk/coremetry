package anomaly

import (
	"context"
	"log"
	"time"

	"github.com/cilcenk/coremetry/internal/cache"
	"github.com/cilcenk/coremetry/internal/chstore"
	"github.com/cilcenk/coremetry/internal/logstore"
)

// Recorder is the persistence side of the anomaly system. The
// existing detectors (log_patterns.go, trace_ops.go) are pure
// readers that compute "what looks anomalous in the last 5
// minutes". This recorder runs them on a tick and upserts an
// AnomalyEvent row per detection so the operator can later
// answer "what fired in the last hour, even if it cleared".
//
// Events go through ReplacingMergeTree keyed on the
// (kind, pattern, service) fingerprint — same pattern firing
// across two consecutive ticks updates the same row, advancing
// last_seen and tracking peak_ratio. "Cleared" status is
// derived in the query layer from last_seen freshness, so we
// don't need a separate sweep job.
type Recorder struct {
	store    *chstore.Store
	logs     logstore.Store // v0.5.241 — drives DetectLogPatterns so the log-anomaly recorder
	                       // works against whichever backend is wired (CH or ES).
	interval time.Duration
	window   time.Duration
	lock     cache.Lock // for multi-replica deployments
	leader   *cache.LeaderHolder // v0.5.426 — true leader designation
}

const recorderLockKey = "coremetry:lock:anomaly-recorder"

// NewRecorder builds a recorder that ticks every `interval` and
// each tick scans `window` of recent data. Default 60s tick is
// fine for the human-grade "anomalies in the last hour" UX —
// faster ticks just multiply CH load with no operator benefit.
func NewRecorder(store *chstore.Store, logs logstore.Store, interval, window time.Duration, lock cache.Lock) *Recorder {
	if interval == 0 {
		interval = 60 * time.Second
	}
	if window == 0 {
		window = 5 * time.Minute
	}
	return &Recorder{
		store: store, logs: logs,
		interval: interval, window: window,
		lock:   lock,
		leader: cache.NewLeaderHolder(lock, recorderLockKey, 3*interval),
	}
}

// Start kicks the recorder into a goroutine. Caller cancels via
// the supplied context. Multi-replica deployments use the lock
// to elect a single writer per tick — the lock TTL is generous
// (the interval × 2) so a slow CH doesn't kill liveness.
func (r *Recorder) Start(ctx context.Context) {
	r.leader.Start(ctx)
	go func() {
		// First tick after a short stagger so the system has
		// time to ingest some logs / spans before the first
		// detection runs against an empty window.
		time.Sleep(15 * time.Second)
		r.tick(ctx)

		t := time.NewTicker(r.interval)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				r.tick(ctx)
			}
		}
	}()
}

func (r *Recorder) tick(ctx context.Context) {
	// v0.5.426 — true leader designation via LeaderHolder; only
	// the leader pod does work, non-leaders skip cleanly.
	// Multi-replica installs still see Upsert idempotency under
	// ReplacingMergeTree(version) as a safety net.
	if !r.leader.IsLeader() {
		return
	}

	now := time.Now()

	// ── Log-pattern anomalies ─────────────────────────────────
	logHits, err := DetectLogPatterns(ctx, r.logs, r.window)
	if err != nil {
		log.Printf("[anomaly-recorder] log patterns: %v", err)
	}
	for _, a := range logHits {
		ev := chstore.AnomalyEvent{
			ID:           chstore.FingerprintAnomaly("log_pattern", a.Pattern, a.Service),
			Kind:         "log_pattern",
			Pattern:      a.Pattern,
			Service:      a.Service,
			StartedAt:    now.UnixNano(),
			LastSeen:     a.LastSeenNs,
			CurrentRatio: a.Ratio,
			CurrentCount: a.CurrentCount,
			Sample:       a.Sample,
		}
		if err := r.store.UpsertAnomalyEvent(ctx, ev); err != nil {
			log.Printf("[anomaly-recorder] upsert log-pattern %s: %v", a.Pattern, err)
		}
	}

	// ── New-template anomalies (v0.6.27) ────────────────────
	// Drain templater publishes shapes it's never seen before;
	// surface them as anomalies so operators get a feed of
	// "what new log line shape just appeared" — complements the
	// curated-regex detector (log_pattern) which only catches
	// known failure shapes. Window is 2× the recorder window
	// to absorb any drift between templater + recorder cadences.
	newTemplateHits, err := DetectNewLogTemplates(ctx, r.store, 2*r.window)
	if err != nil {
		log.Printf("[anomaly-recorder] new log templates: %v", err)
	}
	for _, a := range newTemplateHits {
		ev := chstore.AnomalyEvent{
			// Fingerprint on the stable template ID (Drain hash),
			// NOT the rendered pattern text — the same template
			// keeps the same row across recorder ticks.
			ID:           chstore.FingerprintAnomaly("log_template_new", a.TemplateID, a.Service),
			Kind:         "log_template_new",
			Pattern:      a.Template,
			Service:      a.Service,
			StartedAt:    a.FirstSeenNs,
			LastSeen:     a.LastSeenNs,
			CurrentRatio: 0, // "first appearance" — no ratio over baseline
			CurrentCount: a.TotalCount,
			Sample:       a.Sample,
		}
		if err := r.store.UpsertAnomalyEvent(ctx, ev); err != nil {
			log.Printf("[anomaly-recorder] upsert new log template %s: %v", a.TemplateID, err)
		}
	}

	// ── Trace-op anomalies ────────────────────────────────────
	traceHits, err := DetectTraceOpAnomalies(ctx, r.store, r.window)
	if err != nil {
		log.Printf("[anomaly-recorder] trace ops: %v", err)
	}
	for _, a := range traceHits {
		ev := chstore.AnomalyEvent{
			ID:           chstore.FingerprintAnomaly("trace_op", a.Operation, a.Service),
			Kind:         "trace_op",
			Pattern:      a.Operation,
			Service:      a.Service,
			StartedAt:    now.UnixNano(),
			LastSeen:     a.LastSeenNs,
			CurrentRatio: a.Ratio,
			CurrentCount: a.CurrentErrors,
			Sample:       a.SampleTraceID,
		}
		if err := r.store.UpsertAnomalyEvent(ctx, ev); err != nil {
			log.Printf("[anomaly-recorder] upsert trace-op %s/%s: %v", a.Service, a.Operation, err)
		}
	}
}
