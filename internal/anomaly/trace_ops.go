package anomaly

import (
	"context"
	"sort"
	"time"

	"github.com/cilcenk/coremetry/internal/chstore"
)

// TraceOpAnomaly is a per-(service, operation) error or latency
// signal that's either brand new or up sharply over baseline.
// Different from the service-wide metric anomaly detector in
// that it pinpoints the SPECIFIC operation that's misbehaving —
// the SRE's first question after "is service X broken" is "which
// endpoint inside X".
type TraceOpAnomaly struct {
	Service        string  `json:"service"`
	Operation      string  `json:"operation"`
	Kind           string  `json:"kind"` // "new_error" | "error_spike"
	CurrentErrors  uint64  `json:"currentErrors"`
	BaselineErrors uint64  `json:"baselineErrors"`
	Ratio          float64 `json:"ratio"`         // current / max(baseline, 1)
	SampleTraceID  string  `json:"sampleTraceId"` // representative trace for one-click drill-in
	LastSeenNs     int64   `json:"lastSeenNs"`
}

// DetectTraceOpAnomalies finds per-operation error spikes over
// the last `window` against a longer trailing baseline (1h or
// 12×window, whichever is larger). Two qualifying conditions
// using the per-window-equivalent baseline:
//   - baseline_per_window == 0 AND current_errors >= 3
//     ("new error pattern", i.e. an op that started failing now)
//   - baseline_per_window > 0 AND ratio >= 2
//     (existing op whose error rate just doubled)
//
// The asymmetric baseline (5-min current vs 1-hour trailing)
// keeps fresh spikes visible for ~1 hour — a 5m-vs-5m comparison
// would have the spike fall into baseline within minutes,
// flickering the anomaly section as windows slide.
//
// One CH query LEFT JOINs the current and trailing windows over
// the (service_name, name) primary-key prefix, so even at 1B
// spans/day the scan is bounded to the matching slice.
func DetectTraceOpAnomalies(ctx context.Context, store *chstore.Store, window time.Duration) ([]TraceOpAnomaly, error) {
	conn := store.Conn()
	now := time.Now()
	curStart := now.Add(-window)
	baseLookback := time.Hour
	if 12*window > baseLookback {
		baseLookback = 12 * window
	}
	if baseLookback > 24*time.Hour {
		baseLookback = 24 * time.Hour
	}
	baseStart := now.Add(-baseLookback)
	// Normalisation factor: ratio of current window length to
	// baseline lookback. We compare current_errors against
	// baseline_errors × this factor, so e.g. 5m vs 1h needs the
	// raw baseline divided by 12.
	windowRatio := float64(window) / float64(baseLookback)

	// `?` placeholders carry windowRatio in the spots where we
	// normalise the trailing baseline to the same window length
	// as `cur`. Otherwise a 12× longer baseline would inflate
	// base.errs and the spike check would never fire.
	rows, err := conn.Query(ctx, `
		WITH cur AS (
		  SELECT service_name, name,
		         countIf(status_code='error') AS errs,
		         anyIf(trace_id, status_code='error') AS sample,
		         maxIf(time, status_code='error') AS last_at
		  FROM spans
		  WHERE time >= ? AND time < ?
		  GROUP BY service_name, name
		  HAVING errs > 0
		),
		base AS (
		  SELECT service_name, name,
		         countIf(status_code='error') AS errs
		  FROM spans
		  WHERE time >= ? AND time < ?
		  GROUP BY service_name, name
		)
		SELECT
		  cur.service_name,
		  cur.name,
		  cur.errs,
		  toUInt64(ifNull(base.errs, 0) * ?)                        AS base_errs_per_window,
		  cur.sample,
		  toUnixTimestamp64Nano(cur.last_at)                        AS last_ns,
		  if(base_errs_per_window = 0,
		     toFloat64(cur.errs),
		     cur.errs / base_errs_per_window)                       AS ratio
		FROM cur
		LEFT JOIN base
		  ON cur.service_name = base.service_name AND cur.name = base.name
		WHERE
		  (ifNull(base.errs, 0) = 0 AND cur.errs >= 3) OR
		  (ifNull(base.errs, 0) > 0 AND cur.errs / (ifNull(base.errs, 0) * ?) >= 2)
		ORDER BY ratio DESC, cur.errs DESC
		LIMIT 50
		SETTINGS max_execution_time = 30`,
		curStart, now,
		baseStart, curStart,
		windowRatio,
		windowRatio,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := []TraceOpAnomaly{}
	for rows.Next() {
		var a TraceOpAnomaly
		if err := rows.Scan(
			&a.Service, &a.Operation,
			&a.CurrentErrors, &a.BaselineErrors,
			&a.SampleTraceID, &a.LastSeenNs,
			&a.Ratio,
		); err != nil {
			return nil, err
		}
		if a.BaselineErrors == 0 {
			a.Kind = "new_error"
		} else {
			a.Kind = "error_spike"
		}
		out = append(out, a)
	}

	// Stable order: new errors first (always more interesting
	// than amplified existing ones), then spikes by ratio desc.
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Kind != out[j].Kind {
			return out[i].Kind == "new_error"
		}
		if out[i].Ratio != out[j].Ratio {
			return out[i].Ratio > out[j].Ratio
		}
		return out[i].CurrentErrors > out[j].CurrentErrors
	})
	return out, rows.Err()
}
