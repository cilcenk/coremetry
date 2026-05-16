package chstore

import (
	"context"
	"fmt"
	"time"
)

// SLI types — kept tiny on purpose. availability counts ok-vs-error spans
// against a service (and optional operation); latency counts spans whose
// duration is within the threshold as "good".
const (
	SLITypeAvailability = "availability"
	SLITypeLatency      = "latency"
)

type SLO struct {
	ID          string  `json:"id"`
	Name        string  `json:"name"`
	Service     string  `json:"service"`
	SLIType     string  `json:"sliType"`
	Target      float64 `json:"target"`        // 0..1, e.g. 0.99
	WindowDays  uint16  `json:"windowDays"`    // rolling window
	ThresholdMs float64 `json:"thresholdMs"`   // latency only
	Operation   string  `json:"operation"`     // optional span-name filter
	CreatedAt   int64   `json:"createdAt"`     // unix ns
}

// SLOStatus is the computed runtime state of an SLO. Burn rate > 1 means
// the budget is being consumed faster than its replenishment rate.
type SLOStatus struct {
	Total           uint64  `json:"total"`            // events in window
	Good            uint64  `json:"good"`             // satisfying events
	Bad             uint64  `json:"bad"`              // total - good
	SLI             float64 `json:"sli"`              // good/total, 0..1
	BudgetRemaining float64 `json:"budgetRemaining"`  // 0..1, share of error budget left
	BurnRate        float64 `json:"burnRate"`         // current_error_rate / (1 - target)
	Healthy         bool    `json:"healthy"`          // SLI >= target
}

func (s *Store) ListSLOs(ctx context.Context) ([]SLO, error) {
	rows, err := s.conn.Query(ctx, `
		SELECT id, name, service, sli_type, target, window_days, threshold_ms,
		       operation, toUnixTimestamp64Nano(created_at)
		FROM slos FINAL
		ORDER BY created_at DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []SLO
	for rows.Next() {
		var o SLO
		if err := rows.Scan(&o.ID, &o.Name, &o.Service, &o.SLIType, &o.Target,
			&o.WindowDays, &o.ThresholdMs, &o.Operation, &o.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, o)
	}
	return out, rows.Err()
}

func (s *Store) GetSLO(ctx context.Context, id string) (*SLO, error) {
	row := s.conn.QueryRow(ctx, `
		SELECT id, name, service, sli_type, target, window_days, threshold_ms,
		       operation, toUnixTimestamp64Nano(created_at)
		FROM slos FINAL
		WHERE id = ? LIMIT 1`, id)
	var o SLO
	if err := row.Scan(&o.ID, &o.Name, &o.Service, &o.SLIType, &o.Target,
		&o.WindowDays, &o.ThresholdMs, &o.Operation, &o.CreatedAt); err != nil {
		if err.Error() == "sql: no rows in result set" {
			return nil, nil
		}
		return nil, err
	}
	return &o, nil
}

func (s *Store) UpsertSLO(ctx context.Context, o SLO) error {
	if o.WindowDays == 0 {
		o.WindowDays = 30
	}
	batch, err := s.conn.PrepareBatch(ctx,
		"INSERT INTO slos (id, name, service, sli_type, target, window_days, threshold_ms, operation)")
	if err != nil {
		return fmt.Errorf("prepare slos: %w", err)
	}
	if err := batch.Append(o.ID, o.Name, o.Service, o.SLIType,
		o.Target, o.WindowDays, o.ThresholdMs, o.Operation); err != nil {
		return fmt.Errorf("append slo: %w", err)
	}
	return batch.Send()
}

func (s *Store) DeleteSLO(ctx context.Context, id string) error {
	return s.conn.Exec(ctx, `ALTER TABLE slos DELETE WHERE id = ?`, id)
}

// ComputeSLOStatus runs a single ClickHouse query over the spans table to
// derive total/good counts within the SLO's rolling window. Cheap because
// the WHERE pushes down to the partitioned `time` column.
func (s *Store) ComputeSLOStatus(ctx context.Context, o SLO) (*SLOStatus, error) {
	if o.Service == "" {
		return nil, fmt.Errorf("slo service is required")
	}
	since := time.Now().Add(-time.Duration(o.WindowDays) * 24 * time.Hour)

	var goodExpr string
	switch o.SLIType {
	case SLITypeAvailability:
		// "good" = span did not error out
		goodExpr = "countIf(status_code != 'error')"
	case SLITypeLatency:
		// "good" = duration (ns) under threshold (ms)
		goodExpr = fmt.Sprintf("countIf(duration <= %f)", o.ThresholdMs*1e6)
	default:
		return nil, fmt.Errorf("unknown sli_type: %s", o.SLIType)
	}

	q := `
		SELECT count() AS total, ` + goodExpr + ` AS good
		FROM spans
		WHERE service_name = ? AND time >= ?`
	args := []any{o.Service, since}
	if o.Operation != "" {
		q += ` AND name = ?`
		args = append(args, o.Operation)
	}

	var total, good uint64
	if err := s.conn.QueryRow(ctx, q, args...).Scan(&total, &good); err != nil {
		return nil, err
	}

	st := &SLOStatus{Total: total, Good: good}
	if total > 0 {
		st.Bad = total - good
		st.SLI = float64(good) / float64(total)
	} else {
		st.SLI = 1.0 // no traffic → vacuously meeting target
	}
	st.Healthy = st.SLI >= o.Target

	// Error budget: how much of the allowed-failure share is left.
	budget := 1.0 - o.Target
	if budget > 0 {
		used := 1.0 - st.SLI
		st.BudgetRemaining = 1.0 - (used / budget)
		if st.BudgetRemaining < 0 {
			st.BudgetRemaining = 0
		}
		// Burn rate over the entire window. > 1 → faster than budget allows.
		st.BurnRate = used / budget
	}
	return st, nil
}

// BurnPoint is one bucket of the per-day burn-rate timeseries
// surfaced as a sparkline on the /slos overview (v0.5.150).
// Time is the bucket-start (UTC, day granularity for a 7d view).
// BurnRate is (1-SLI_bucket)/(1-target) — same definition as
// SLOStatus.BurnRate, just over the day instead of the SLO's
// rolling window.
type BurnPoint struct {
	Time     int64   `json:"time"`     // unix ns, bucket start
	Total    uint64  `json:"total"`
	Good     uint64  `json:"good"`
	BurnRate float64 `json:"burnRate"` // >1 = eating budget faster than allowed
}

// ComputeSLOBurnSeries returns a per-day burn-rate timeseries
// over the past `days` days. Used by the SLO list page to
// render a sparkline next to each row so the operator can spot
// "this SLO has been eroding for the last 3 days" without
// opening the detail view. Day granularity is enough for a 7-30
// day picture; tighter granularity would just add noise to the
// thumb-sized chart.
func (s *Store) ComputeSLOBurnSeries(ctx context.Context, o SLO, days int) ([]BurnPoint, error) {
	if o.Service == "" {
		return nil, fmt.Errorf("slo service is required")
	}
	if days <= 0 || days > 90 {
		days = 7
	}
	since := time.Now().Add(-time.Duration(days) * 24 * time.Hour)

	var goodExpr string
	switch o.SLIType {
	case SLITypeAvailability:
		goodExpr = "countIf(status_code != 'error')"
	case SLITypeLatency:
		goodExpr = fmt.Sprintf("countIf(duration <= %f)", o.ThresholdMs*1e6)
	default:
		return nil, fmt.Errorf("unknown sli_type: %s", o.SLIType)
	}
	q := `
		SELECT toStartOfDay(time)            AS bucket,
		       count()                       AS total,
		       ` + goodExpr + `              AS good
		FROM spans
		WHERE service_name = ? AND time >= ?`
	args := []any{o.Service, since}
	if o.Operation != "" {
		q += ` AND name = ?`
		args = append(args, o.Operation)
	}
	q += `
		GROUP BY bucket
		ORDER BY bucket
		SETTINGS max_execution_time = 15`
	rows, err := s.conn.Query(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	budget := 1.0 - o.Target
	out := []BurnPoint{}
	for rows.Next() {
		var bucket time.Time
		var total, good uint64
		if err := rows.Scan(&bucket, &total, &good); err != nil {
			return nil, err
		}
		bp := BurnPoint{
			Time: bucket.UnixNano(),
			Total: total, Good: good,
		}
		if total > 0 && budget > 0 {
			sli := float64(good) / float64(total)
			used := 1.0 - sli
			bp.BurnRate = used / budget
		}
		out = append(out, bp)
	}
	return out, rows.Err()
}

// ComputeSLOBurnRate calculates the burn rate over a SHORT
// look-back window — used by the 2-window burn-rate alarm
// pattern (Google SRE Workbook). The status method above runs
// over the SLO's full rolling window (e.g. 30 days), which
// smooths out short bursts; for alerting we want to detect
// "currently burning fast" within the last 1h or 6h.
//
// Returned rate units: same as BurnRate on SLOStatus —
// (1 − SLI_window) / (1 − target). > 1 means the budget would
// be exhausted before the SLO window completes.
func (s *Store) ComputeSLOBurnRate(ctx context.Context, o SLO, window time.Duration) (float64, uint64, error) {
	if o.Service == "" {
		return 0, 0, fmt.Errorf("slo service is required")
	}
	since := time.Now().Add(-window)
	var goodExpr string
	switch o.SLIType {
	case SLITypeAvailability:
		goodExpr = "countIf(status_code != 'error')"
	case SLITypeLatency:
		goodExpr = fmt.Sprintf("countIf(duration <= %f)", o.ThresholdMs*1e6)
	default:
		return 0, 0, fmt.Errorf("unknown sli_type: %s", o.SLIType)
	}
	q := `SELECT count() AS total, ` + goodExpr + ` AS good
	      FROM spans
	      WHERE service_name = ? AND time >= ?`
	args := []any{o.Service, since}
	if o.Operation != "" {
		q += ` AND name = ?`
		args = append(args, o.Operation)
	}
	var total, good uint64
	if err := s.conn.QueryRow(ctx, q, args...).Scan(&total, &good); err != nil {
		return 0, 0, err
	}
	if total == 0 {
		return 0, 0, nil
	}
	sli := float64(good) / float64(total)
	budget := 1.0 - o.Target
	if budget <= 0 {
		return 0, total, nil
	}
	used := 1.0 - sli
	return used / budget, total, nil
}
