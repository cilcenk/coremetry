package chstore

import (
	"context"
	"time"
)

// NoisyRule summarises how frequently and how long a rule's
// problems have been open over a window. Drives the /admin/alert-
// tuning report — operators looking to silence prod alert spam
// scan this list to find the loudest rules and tighten them.
type NoisyRule struct {
	RuleID         string  `json:"ruleId"`
	RuleName       string  `json:"ruleName"`
	Severity       string  `json:"severity"`
	OpenCount      uint64  `json:"openCount"`
	MedianDurSec   float64 `json:"medianDurSec"`
	LastFiredNs    int64   `json:"lastFiredNs"`
	TotalDurSec    float64 `json:"totalDurSec"`
}

// NoisyRules returns rules ranked by problem-open count over
// [from, to]. Median duration + total duration come from the
// resolved problems only (open problems contribute to the count
// but not the duration — their duration isn't bounded yet).
//
// Read pattern: single GROUP BY rule_id on the problems table.
// ReplacingMergeTree FINAL pulls the latest version per id so
// the count doesn't double on a status flip. Partition pruning
// drops every date outside [from, to] before the GROUP BY hits.
// 30s execution-time guard keeps the worst case bounded.
func (s *Store) NoisyRules(ctx context.Context, from, to time.Time, limit int) ([]NoisyRule, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	rows, err := s.conn.Query(ctx, `
		SELECT
			rule_id,
			any(rule_name) AS rule_name,
			any(severity)  AS severity,
			toUInt64(count()) AS open_count,
			coalesce(
				toFloat64(
					quantile(0.5)(
						if(isNull(resolved_at),
						   NULL,
						   toFloat64(
						     toUnixTimestamp64Nano(resolved_at) -
						     toUnixTimestamp64Nano(started_at)
						   ) / 1e9)
					)
				), 0) AS median_dur_sec,
			toInt64(max(toUnixTimestamp64Nano(started_at))) AS last_fired_ns,
			coalesce(
				sum(if(isNull(resolved_at),
				       0,
				       toFloat64(
				         toUnixTimestamp64Nano(resolved_at) -
				         toUnixTimestamp64Nano(started_at)
				       ) / 1e9)),
				0) AS total_dur_sec
		FROM problems FINAL
		WHERE started_at >= toDateTime64(?, 9, 'UTC')
		  AND started_at <  toDateTime64(?, 9, 'UTC')
		  AND rule_id != ''
		GROUP BY rule_id
		ORDER BY open_count DESC
		LIMIT ?
		SETTINGS max_execution_time = 30`,
		chDateTime64Arg(from),
		chDateTime64Arg(to),
		limit,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []NoisyRule{}
	for rows.Next() {
		var r NoisyRule
		if err := rows.Scan(&r.RuleID, &r.RuleName, &r.Severity,
			&r.OpenCount, &r.MedianDurSec, &r.LastFiredNs,
			&r.TotalDurSec); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}
