package chstore

// /watchers surface reads (v0.9.196). The list page needs, per
// imported watcher rule, "when did it last fire / how often in the
// last 24h / is a problem open right now" — WITHOUT calling the
// per-rule history endpoint N times. One GROUP BY rule_id rollup over
// the problems state table answers it for every watcher in a single
// bounded FINAL scan (problems is ReplacingMergeTree, notification-
// scale row volume, so FINAL + no time prefix matches the existing
// ListProblems posture; LIMIT + max_execution_time bound it anyway).

import (
	"context"
	"time"
)

// WatcherSummary is one rule's problems rollup for the /watchers list.
type WatcherSummary struct {
	RuleID   string `json:"ruleId"`
	LastFire int64  `json:"lastFire"` // unix ns of the newest problem's started_at; 0 = never fired
	Fires24h uint64 `json:"fires24h"` // problems opened in the trailing 24h
	OpenNow  bool   `json:"openNow"`  // an open/acknowledged problem exists right now
	// FiresHourly — the same trailing-24h fires, distributed into 24
	// one-hour slots, oldest→newest (slot 0 = 24h ago … slot 23 = the
	// last hour). Granular-sparklines sweep (M4): drives the /watchers
	// list's per-row fire-distribution mini-bar instead of a bare count.
	FiresHourly []uint64 `json:"firesHourly,omitempty"`
}

// watcherSummarySQL — problems rollup keyed by rule_id, watcher rows
// only (evaluateWatcher stamps metric='watcher' on every problem it
// opens). Const so the mandatory bounds are pinned by a test without
// a live CH. LIMIT 2000 comfortably covers the operator's ~300-watcher
// fleet while keeping the read bounded if rule churn ever explodes.
//
// fires_hourly: per-row 24-slot one-hot array (started_at ≥ since
// guards the intDiv against pre-window rows truncating toward slot 0),
// summed elementwise by sumForEach. Bind order: since (countIf), since
// (guard), since unix-ns (intDiv base). Aggregate-only — no schema
// change, distributed-safe as-is.
const watcherSummarySQL = `
	SELECT rule_id,
	       toUnixTimestamp64Nano(max(started_at)),
	       toUInt64(countIf(started_at >= ?)),
	       toUInt64(countIf(status IN ('open', 'acknowledged'))),
	       sumForEach(arrayMap(h -> toUInt64(started_at >= ?
	         AND intDiv(toUnixTimestamp64Nano(started_at) - ?, 3600000000000) = h),
	         range(0, 24)))
	FROM problems FINAL
	WHERE metric = 'watcher'
	GROUP BY rule_id
	LIMIT 2000
	SETTINGS max_execution_time = 8`

// WatcherProblemSummaries returns the per-rule rollup keyed by rule
// id. Rules that never opened a problem simply have no entry — the
// API layer zero-fills them against the alert_rules list.
func (s *Store) WatcherProblemSummaries(ctx context.Context) (map[string]WatcherSummary, error) {
	since := time.Now().Add(-24 * time.Hour)
	rows, err := s.conn.Query(ctx, watcherSummarySQL, since, since, since.UnixNano())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make(map[string]WatcherSummary)
	for rows.Next() {
		var w WatcherSummary
		var open uint64
		if err := rows.Scan(&w.RuleID, &w.LastFire, &w.Fires24h, &open, &w.FiresHourly); err != nil {
			return nil, err
		}
		w.OpenNow = open > 0
		out[w.RuleID] = w
	}
	return out, rows.Err()
}
