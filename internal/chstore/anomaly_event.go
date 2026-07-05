package chstore

import (
	"context"
	"crypto/sha1"
	"encoding/hex"
	"time"
)

// AnomalyEvent is one continuously-occurring anomaly tracked over
// time. Same fingerprint (kind, pattern, service) keeps re-using
// the row — last_seen advances on every detection, started_at and
// peak_ratio capture history. An event is "active" iff last_seen
// is recent; the "cleared" status is derived in the query layer
// from last_seen freshness so we don't need a separate sweep.
type AnomalyEvent struct {
	ID            string  `json:"id"`
	Kind          string  `json:"kind"`     // "log_pattern" | "trace_op"
	Pattern       string  `json:"pattern"`  // pattern name (logs) or operation name (trace ops)
	Service       string  `json:"service"`
	StartedAt     int64   `json:"startedAt"`     // unix ns — first observation
	LastSeen      int64   `json:"lastSeen"`      // unix ns — most recent observation
	PeakRatio     float64 `json:"peakRatio"`     // worst ratio seen during the event
	CurrentRatio  float64 `json:"currentRatio"`  // ratio at last_seen
	CurrentCount  uint64  `json:"currentCount"`
	Sample        string  `json:"sample"`
	// Status is computed in the query, not stored. "active" while
	// last_seen >= now() - 10m, otherwise "cleared".
	Status        string `json:"status"`
	// Clusters — k8s/openshift cluster names the anomaly's
	// service was active in around the time of detection.
	// Enriched at read time (no schema migration); empty for
	// services without cluster attrs.
	Clusters []string `json:"clusters,omitempty"`
	// RecentDeploy — v0.5.286. Most recent deploy of this
	// service observed within `lookback` (default 30m) before
	// StartedAt. Populated at READ time by
	// EnrichAnomaliesWithDeploys so the /anomalies page can
	// show a "deployed v1.2.3 · 4m before" chip — collapses
	// the "did this break because of a deploy?" question into
	// a single glance.
	RecentDeploy *RecentDeploy `json:"recentDeploy,omitempty"`
	// RootCause — compact top-suspect summary of the persisted
	// root-cause hypothesis the worker synthesized for this anomaly
	// (rc #3 of the anomaly → root-cause feature). Attached at READ
	// time by the /anomalies events handler via a single batch
	// GetHypotheses join (NO per-row fetch); nil when the worker
	// hasn't synthesized a hypothesis for this anchor yet. The
	// RootCauseRibbon renders the collapsed chip from this; the
	// expand fetches the full /anomalies/{id}/rootcause fan-out.
	RootCause *RootCauseSummary `json:"rootCause,omitempty"`
}

// FingerprintAnomaly stitches the same (kind, pattern, service)
// detections into one event row across detector ticks. Stable
// across process restarts — sha1 is deterministic.
func FingerprintAnomaly(kind, pattern, service string) string {
	h := sha1.New()
	h.Write([]byte(kind))
	h.Write([]byte("|"))
	h.Write([]byte(pattern))
	h.Write([]byte("|"))
	h.Write([]byte(service))
	return hex.EncodeToString(h.Sum(nil))[:16]
}

// UpsertAnomalyEvent records (or refreshes) an event. ReplacingMergeTree
// picks the latest version on read. peak_ratio is monotonic — we
// pass max(prev, new) in the application layer because CH lacks an
// atomic max-on-upsert primitive on this engine.
func (s *Store) UpsertAnomalyEvent(ctx context.Context, e AnomalyEvent) error {
	// Look up the previous peak_ratio + started_at — if the event
	// already exists we keep the original started_at (so the row
	// represents a single continuous anomaly span) and only bump
	// peak_ratio if the new ratio is higher.
	var prevStartedAt time.Time
	var prevPeak float64
	row := s.conn.QueryRow(ctx,
		`SELECT started_at, peak_ratio FROM anomaly_events FINAL WHERE id = ?`, e.ID)
	if err := row.Scan(&prevStartedAt, &prevPeak); err != nil {
		// "no rows" → first sighting; use the current values.
		prevStartedAt = time.Unix(0, e.StartedAt)
		prevPeak = e.CurrentRatio
	}
	if e.CurrentRatio > prevPeak {
		prevPeak = e.CurrentRatio
	}

	// Explicit column list: anomaly_events has a `version` column with a
	// DEFAULT (toUnixTimestamp64Nano(now64(9))). The bare-form
	// "INSERT INTO anomaly_events" requires all 11 columns; supplying
	// 10 args trips clickhouse-go's "expected 11 arguments" error,
	// which spammed the logs once the table grew that DEFAULT column.
	// Naming the columns we actually populate lets the DEFAULT do its
	// job and the recorder stays in sync without handcrafting a version
	// value here.
	batch, err := s.conn.PrepareBatch(ctx, `INSERT INTO anomaly_events
		(id, kind, pattern, service, started_at, last_seen,
		 peak_ratio, current_ratio, current_count, sample)`)
	if err != nil {
		return err
	}
	if err := batch.Append(
		e.ID, e.Kind, e.Pattern, e.Service,
		prevStartedAt,
		time.Unix(0, e.LastSeen),
		prevPeak, e.CurrentRatio, e.CurrentCount, e.Sample,
	); err != nil {
		return err
	}
	return batch.Send()
}

// GetAnomalyEvent reads one event by id (the FingerprintAnomaly hash).
// FINAL collapses the ReplacingMergeTree versions to the latest row.
// Returns (nil, nil) on no-match so the API layer can answer a clean 404
// instead of treating "not found" as an error. The status is derived the
// same way ListAnomalyEvents derives it — active iff last_seen is fresh
// within ActiveAge (default 10m) — so an event-anchored read agrees with
// the list view. Bounded by the id equality on the PK; anomaly_events is a
// small state table, not spans/metric_points, so no time-bound is needed.
func (s *Store) GetAnomalyEvent(ctx context.Context, id string, activeAge time.Duration) (*AnomalyEvent, error) {
	if activeAge == 0 {
		activeAge = 10 * time.Minute
	}
	var e AnomalyEvent
	row := s.conn.QueryRow(ctx, `
		SELECT id, kind, pattern, service,
		       toUnixTimestamp64Nano(started_at),
		       toUnixTimestamp64Nano(last_seen),
		       peak_ratio, current_ratio, current_count, sample,
		       if(last_seen >= now64() - INTERVAL ? SECOND, 'active', 'cleared') AS status
		FROM anomaly_events FINAL
		WHERE id = ?
		LIMIT 1`,
		int64(activeAge.Seconds()),
		id,
	)
	if err := row.Scan(
		&e.ID, &e.Kind, &e.Pattern, &e.Service,
		&e.StartedAt, &e.LastSeen,
		&e.PeakRatio, &e.CurrentRatio, &e.CurrentCount, &e.Sample,
		&e.Status,
	); err != nil {
		// clickhouse-go's QueryRow surfaces an empty result as this exact
		// string (no typed sentinel) — the same no-rows idiom the other
		// by-id reads use (dashboard.go, monitor.go). Soft "not found".
		if err.Error() == "sql: no rows in result set" {
			return nil, nil
		}
		return nil, err
	}
	return &e, nil
}

// ListAnomalyEventsFilter is the read-side cut. SinceNs filters by
// last_seen >= … so a 24h window returns both currently-active
// events and ones that cleared up to 24h ago.
type ListAnomalyEventsFilter struct {
	SinceNs   int64   // unix ns; 0 = last 24h default
	ActiveAge time.Duration // last_seen freshness for "active" status; 0 = 10m default
	Limit     int
}

// CountActiveAnomalyEvents returns the number of anomaly events currently
// "active" — i.e. last_seen fresher than activeAge (the same derivation the
// list/detail reads use). For the /inbox badge (v0.8.288): a cheap COUNT(*)
// FINAL on the small state table, no row scan. activeAge 0 → 10m default.
func (s *Store) CountActiveAnomalyEvents(ctx context.Context, activeAge time.Duration) (uint64, error) {
	if activeAge == 0 {
		activeAge = 10 * time.Minute
	}
	var n uint64
	err := s.conn.QueryRow(ctx, `
		SELECT count() FROM anomaly_events FINAL
		WHERE last_seen >= now64() - INTERVAL ? SECOND`,
		int64(activeAge.Seconds()),
	).Scan(&n)
	return n, err
}

func (s *Store) ListAnomalyEvents(ctx context.Context, f ListAnomalyEventsFilter) ([]AnomalyEvent, error) {
	if f.Limit == 0 {
		f.Limit = 200
	}
	if f.ActiveAge == 0 {
		f.ActiveAge = 10 * time.Minute
	}
	since := f.SinceNs
	if since == 0 {
		since = time.Now().Add(-24 * time.Hour).UnixNano()
	}

	rows, err := s.conn.Query(ctx, `
		SELECT id, kind, pattern, service,
		       toUnixTimestamp64Nano(started_at),
		       toUnixTimestamp64Nano(last_seen),
		       peak_ratio, current_ratio, current_count, sample,
		       if(last_seen >= now64() - INTERVAL ? SECOND, 'active', 'cleared') AS status
		FROM anomaly_events FINAL
		WHERE toUnixTimestamp64Nano(last_seen) >= ?
		ORDER BY status DESC, last_seen DESC
		LIMIT ?`,
		int64(f.ActiveAge.Seconds()),
		since,
		f.Limit,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []AnomalyEvent
	for rows.Next() {
		var e AnomalyEvent
		if err := rows.Scan(
			&e.ID, &e.Kind, &e.Pattern, &e.Service,
			&e.StartedAt, &e.LastSeen,
			&e.PeakRatio, &e.CurrentRatio, &e.CurrentCount, &e.Sample,
			&e.Status,
		); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}
