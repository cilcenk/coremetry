package chstore

import (
	"context"
	"time"
)

// db_capacity.go — receiver-derived capacity/saturation samples for the
// alert evaluator (feature #5). The DB dashboards (oracle.go / postgres.go
// / mysql.go / redis.go) read these same gauges to colour their tiles, but
// nothing PAGED off them. The evaluator now reads the raw saturation
// gauges here, computes a usage% per (instance, check, subkey), and opens
// /resolves Problems against it on the existing leader-locked tick.
//
// This file is intentionally a pure metric READ — no thresholding, no
// Problem creation. The threshold logic lives in the evaluator
// (internal/evaluator/db_capacity.go) so it stays unit-testable without a
// live ClickHouse.
//
// All queries are time-bounded on `time` (the metric_points ORDER BY
// prefix is (service_name, metric, time)), filter `metric` to a small IN
// set, carry a LIMIT + max_execution_time, and group by the instance
// identity so one tick covers every receiver instance in a single trip
// per check (no per-instance fan-out).

// CapacitySample is one saturation reading for one (instance, check,
// subkey). Usage / Limit are the raw gauge pair; the evaluator derives
// the percentage so the read stays dumb. Subkey distinguishes the
// dimensioned checks (Oracle tablespace_name) from the undimensioned
// ones (sessions / processes / connections) where it is empty.
//
// Pct is pre-computed (Usage/Limit*100) ONLY as a convenience for the
// rate-style checks (Redis eviction) that have no meaningful limit; for
// the usage/limit pairs the evaluator recomputes from Usage/Limit so a
// zero/absent Limit is handled in one place.
type CapacitySample struct {
	Instance string  // receiver instance identity (instance attr → service.name res key)
	Subkey   string  // e.g. Oracle tablespace name; empty for undimensioned checks
	Usage    float64 // current gauge value
	Limit    float64 // cap gauge value (0 when the check is a raw rate, e.g. evictions)
}

// instanceExpr is the canonical "which DB instance produced this point"
// expression, matching the per-receiver instance clauses
// (oracleInstanceClause / redisInstanceClause / …): prefer an `instance`
// attribute, fall back to the `service.name` resource key that older
// receiver wirings tag at the resource level. Coalesced so single-DB
// deployments (no per-instance attr) still collapse to one stable key.
const instanceExpr = `coalesce(
	nullIf(attr_values[indexOf(attr_keys, 'instance')], ''),
	nullIf(res_values[indexOf(res_keys, 'service.name')], ''),
	service_name
)`

// capacityWindow is how far back the capacity read looks for the latest
// gauge value. Receivers scrape on a 10-60s cadence; a 10-min window is
// generous enough to ride a missed scrape without going stale, and it's
// the same "latest over the window" semantics the dashboards use.
const capacityWindow = 10 * time.Minute

// UsageLimit reads a set of (usage-metric, limit-metric) gauge pairs
// per instance, latest value over the window. Returns one CapacitySample
// per instance that has BOTH gauges present (a usage with no limit can't
// be turned into a saturation %, so it's skipped — the dashboard shows it
// raw, but it isn't pageable). Subkey is always empty for these
// undimensioned checks.
//
// usageMetric / limitMetric are literal metric names from the receiver
// semantic conventions (oracledb.sessions.usage, …) — never operator
// input — so the IN list is safe to build.
func (s *Store) UsageLimit(
	ctx context.Context, usageMetric, limitMetric string,
) ([]CapacitySample, error) {
	now := time.Now()
	from := now.Add(-capacityWindow)
	q := `
		SELECT
			` + instanceExpr + ` AS inst,
			argMaxIf(value, time, metric = ?) AS usage,
			argMaxIf(value, time, metric = ?) AS lim
		FROM metric_points
		WHERE time >= ? AND time <= ?
		  AND metric IN (?, ?)
		GROUP BY inst
		LIMIT 1000
		SETTINGS max_execution_time = 10`
	rows, err := s.conn.Query(ctx, q, usageMetric, limitMetric, from, now, usageMetric, limitMetric)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []CapacitySample{}
	for rows.Next() {
		var c CapacitySample
		if err := rows.Scan(&c.Instance, &c.Usage, &c.Limit); err != nil {
			continue
		}
		if c.Instance == "" || c.Limit <= 0 {
			continue // can't compute a saturation % without a positive cap
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// DimensionedUsageLimit is UsageLimit for a check that is
// dimensioned by a single attribute (Oracle tablespace_size.* is keyed by
// `tablespace_name`). Groups by (instance, attr-value) so each tablespace
// is its own pageable check; Subkey carries the dimension value.
func (s *Store) DimensionedUsageLimit(
	ctx context.Context, usageMetric, limitMetric, attrKey string,
) ([]CapacitySample, error) {
	now := time.Now()
	from := now.Add(-capacityWindow)
	q := `
		SELECT
			` + instanceExpr + ` AS inst,
			attr_values[indexOf(attr_keys, ?)] AS subkey,
			argMaxIf(value, time, metric = ?) AS usage,
			argMaxIf(value, time, metric = ?) AS lim
		FROM metric_points
		WHERE time >= ? AND time <= ?
		  AND metric IN (?, ?)
		  AND has(attr_keys, ?)
		GROUP BY inst, subkey
		LIMIT 5000
		SETTINGS max_execution_time = 10`
	rows, err := s.conn.Query(ctx, q,
		attrKey, usageMetric, limitMetric, from, now, usageMetric, limitMetric, attrKey)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []CapacitySample{}
	for rows.Next() {
		var c CapacitySample
		if err := rows.Scan(&c.Instance, &c.Subkey, &c.Usage, &c.Limit); err != nil {
			continue
		}
		if c.Instance == "" || c.Subkey == "" || c.Limit <= 0 {
			continue
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// rateGauge reads a single cumulative-counter metric per instance and
// derives its per-second rate over the window ((max-min)/windowSec),
// matching the queryOracleRates / queryRedisRates derivation. Used for
// the Redis eviction-rate check, where there is no cap gauge — a positive
// rate is itself the saturation signal (maxmemory-policy is evicting). A
// counter reset (negative delta) is suppressed to 0. Usage carries the
// rate; Limit is left 0 (the evaluator treats Limit==0 checks as raw-rate).
func (s *Store) RateGauge(
	ctx context.Context, metric string,
) ([]CapacitySample, error) {
	now := time.Now()
	from := now.Add(-capacityWindow)
	windowSec := capacityWindow.Seconds()
	q := `
		SELECT
			` + instanceExpr + ` AS inst,
			(max(value) - min(value)) / ? AS rate
		FROM metric_points
		WHERE time >= ? AND time <= ?
		  AND metric = ?
		GROUP BY inst
		LIMIT 1000
		SETTINGS max_execution_time = 10`
	rows, err := s.conn.Query(ctx, q, windowSec, from, now, metric)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []CapacitySample{}
	for rows.Next() {
		var c CapacitySample
		if err := rows.Scan(&c.Instance, &c.Usage); err != nil {
			continue
		}
		if c.Instance == "" {
			continue
		}
		if c.Usage < 0 {
			c.Usage = 0 // counter reset
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// metricExists reports whether ANY point for `metric` landed in the
// window. The defensive Postgres/MySQL/Redis checks only run when their
// receiver is actually publishing, so an install with no such receiver
// never sees spurious Problems (nor pays for the read every tick once it
// knows the metric is absent — but we keep this cheap + stateless rather
// than caching, since it's one indexed point-existence probe).
func (s *Store) MetricExists(ctx context.Context, metric string) (bool, error) {
	now := time.Now()
	from := now.Add(-capacityWindow)
	var n uint64
	err := s.conn.QueryRow(ctx, `
		SELECT count() FROM metric_points
		WHERE time >= ? AND time <= ? AND metric = ?
		LIMIT 1
		SETTINGS max_execution_time = 5`,
		from, now, metric).Scan(&n)
	return n > 0, err
}
