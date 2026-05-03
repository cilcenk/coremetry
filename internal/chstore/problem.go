package chstore

import (
	"context"
	"fmt"
	"time"
)

// AlertRule defines an evaluator condition. metric is one of:
//   error_rate    — % of error spans  (operand: percentage)
//   p99_ms / p95_ms / avg_ms / p50_ms — latency in ms
//   request_rate  — spans per second  (typically used with `<` to detect drops)
//   error_count   — number of error spans (absolute)
type AlertRule struct {
	ID         string  `json:"id"`
	Name       string  `json:"name"`
	Service    string  `json:"service"`     // empty = applies to all services
	Metric     string  `json:"metric"`      // error_rate | p99_ms | request_rate | …
	Comparator string  `json:"comparator"`  // > | >= | < | <=
	Threshold  float64 `json:"threshold"`
	WindowSec  uint32  `json:"windowSec"`   // sliding window size
	Severity   string  `json:"severity"`    // info | warning | critical
	Enabled    bool    `json:"enabled"`
	BuiltIn    bool    `json:"builtIn"`
	CreatedAt  int64   `json:"createdAt"`   // unix nanoseconds
}

type Problem struct {
	ID          string  `json:"id"`
	RuleID      string  `json:"ruleId"`
	RuleName    string  `json:"ruleName"`
	Severity    string  `json:"severity"`
	Service     string  `json:"service"`
	Metric      string  `json:"metric"`
	Value       float64 `json:"value"`
	Threshold   float64 `json:"threshold"`
	Status      string  `json:"status"`     // open | resolved
	Description string  `json:"description"`
	StartedAt   int64   `json:"startedAt"`  // unix ns
	ResolvedAt  *int64  `json:"resolvedAt,omitempty"`
}

// ── Alert rules ───────────────────────────────────────────────────────────────

func (s *Store) ListAlertRules(ctx context.Context) ([]AlertRule, error) {
	rows, err := s.conn.Query(ctx, `
		SELECT id, name, service, metric, comparator, threshold, window_sec,
		       severity, enabled, built_in, toUnixTimestamp64Nano(created_at)
		FROM alert_rules FINAL
		ORDER BY created_at DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []AlertRule
	for rows.Next() {
		var r AlertRule
		var enabled, builtIn uint8
		if err := rows.Scan(&r.ID, &r.Name, &r.Service, &r.Metric, &r.Comparator,
			&r.Threshold, &r.WindowSec, &r.Severity, &enabled, &builtIn, &r.CreatedAt); err != nil {
			return nil, err
		}
		r.Enabled = enabled == 1
		r.BuiltIn = builtIn == 1
		out = append(out, r)
	}
	return out, rows.Err()
}

func (s *Store) UpsertAlertRule(ctx context.Context, r AlertRule) error {
	enabled := uint8(0); if r.Enabled { enabled = 1 }
	builtIn := uint8(0); if r.BuiltIn { builtIn = 1 }
	batch, err := s.conn.PrepareBatch(ctx, "INSERT INTO alert_rules")
	if err != nil { return err }
	if err := batch.Append(r.ID, r.Name, r.Service, r.Metric, r.Comparator,
		r.Threshold, r.WindowSec, r.Severity, enabled, builtIn,
		time.Now().UTC(), uint64(time.Now().UnixNano())); err != nil {
		return err
	}
	return batch.Send()
}

func (s *Store) DeleteAlertRule(ctx context.Context, id string) error {
	// Soft delete via a tombstone row with enabled=0; ReplacingMergeTree will
	// keep the latest version. (No real DELETE on MergeTree without alter.)
	return s.SetAlertRuleEnabled(ctx, id, false)
}

// SetAlertRuleEnabled flips a rule's enabled flag. Used by both the
// disable (DELETE) and re-enable endpoints.
func (s *Store) SetAlertRuleEnabled(ctx context.Context, id string, enabled bool) error {
	r, err := s.GetAlertRule(ctx, id)
	if err != nil {
		return err
	}
	r.Enabled = enabled
	return s.UpsertAlertRule(ctx, *r)
}

func (s *Store) GetAlertRule(ctx context.Context, id string) (*AlertRule, error) {
	var r AlertRule
	var enabled, builtIn uint8
	err := s.conn.QueryRow(ctx, `
		SELECT id, name, service, metric, comparator, threshold, window_sec,
		       severity, enabled, built_in, toUnixTimestamp64Nano(created_at)
		FROM alert_rules FINAL WHERE id = ? LIMIT 1`, id).
		Scan(&r.ID, &r.Name, &r.Service, &r.Metric, &r.Comparator, &r.Threshold,
			&r.WindowSec, &r.Severity, &enabled, &builtIn, &r.CreatedAt)
	if err != nil { return nil, err }
	r.Enabled = enabled == 1
	r.BuiltIn = builtIn == 1
	return &r, nil
}

// ── Problems ─────────────────────────────────────────────────────────────────

type ProblemFilter struct {
	Status   string // "open" | "resolved" | ""
	Service  string
	Severity string
	Limit    int
}

func (s *Store) ListProblems(ctx context.Context, f ProblemFilter) ([]Problem, error) {
	var wc whereClause
	if f.Status   != "" { wc.add("status = ?", f.Status) }
	if f.Service  != "" { wc.add("service = ?", f.Service) }
	if f.Severity != "" { wc.add("severity = ?", f.Severity) }
	if f.Limit == 0 { f.Limit = 100 }

	rows, err := s.conn.Query(ctx, `
		SELECT id, rule_id, rule_name, severity, service, metric,
		       value, threshold, status, description,
		       toUnixTimestamp64Nano(started_at),
		       resolved_at
		FROM problems FINAL `+wc.sql()+`
		ORDER BY started_at DESC
		LIMIT ?`, append(wc.args, f.Limit)...)
	if err != nil { return nil, err }
	defer rows.Close()

	var out []Problem
	for rows.Next() {
		var p Problem
		var resolvedAt *time.Time
		if err := rows.Scan(&p.ID, &p.RuleID, &p.RuleName, &p.Severity, &p.Service,
			&p.Metric, &p.Value, &p.Threshold, &p.Status, &p.Description,
			&p.StartedAt, &resolvedAt); err != nil {
			return nil, err
		}
		if resolvedAt != nil {
			ns := resolvedAt.UnixNano()
			p.ResolvedAt = &ns
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

func (s *Store) FindOpenProblem(ctx context.Context, ruleID, service string) (*Problem, error) {
	var p Problem
	var resolvedAt *time.Time
	err := s.conn.QueryRow(ctx, `
		SELECT id, rule_id, rule_name, severity, service, metric,
		       value, threshold, status, description,
		       toUnixTimestamp64Nano(started_at),
		       resolved_at
		FROM problems FINAL
		WHERE rule_id = ? AND service = ? AND status = 'open'
		ORDER BY started_at DESC LIMIT 1`, ruleID, service).
		Scan(&p.ID, &p.RuleID, &p.RuleName, &p.Severity, &p.Service,
			&p.Metric, &p.Value, &p.Threshold, &p.Status, &p.Description,
			&p.StartedAt, &resolvedAt)
	if err != nil { return nil, err }
	if resolvedAt != nil {
		ns := resolvedAt.UnixNano()
		p.ResolvedAt = &ns
	}
	return &p, nil
}

func (s *Store) UpsertProblem(ctx context.Context, p Problem) error {
	batch, err := s.conn.PrepareBatch(ctx, "INSERT INTO problems")
	if err != nil { return err }
	startedAt := time.Unix(0, p.StartedAt).UTC()
	var resolvedAt *time.Time
	if p.ResolvedAt != nil {
		t := time.Unix(0, *p.ResolvedAt).UTC()
		resolvedAt = &t
	}
	if err := batch.Append(p.ID, p.RuleID, p.RuleName, p.Severity, p.Service,
		p.Metric, p.Value, p.Threshold, p.Status, p.Description,
		startedAt, resolvedAt, time.Now().UTC(), uint64(time.Now().UnixNano())); err != nil {
		return fmt.Errorf("append problem: %w", err)
	}
	return batch.Send()
}

// ── Service backtrace queries ────────────────────────────────────────────────

type ServiceEdgeStats struct {
	Service   string  `json:"service"`
	Calls     uint64  `json:"calls"`
	ErrorRate float64 `json:"errorRate"`
	AvgMs     float64 `json:"avgMs"`
	P99Ms     float64 `json:"p99Ms"`
}

// CallersOf returns services that called `service` (incoming dependency view).
func (s *Store) CallersOf(ctx context.Context, service string, since time.Duration) ([]ServiceEdgeStats, error) {
	cutoff := time.Now().Add(-since)
	rows, err := s.conn.Query(ctx, `
		SELECT service_name AS caller,
		       count() AS calls,
		       countIf(status_code = 'error') / count() * 100 AS error_rate,
		       avg(duration) / 1e6 AS avg_ms,
		       quantile(0.99)(duration) / 1e6 AS p99_ms
		FROM spans
		WHERE time >= ?
		  AND peer_service = ?
		  AND kind IN ('client', 'producer')
		GROUP BY caller
		ORDER BY calls DESC`, cutoff, service)
	if err != nil { return nil, err }
	defer rows.Close()
	var out []ServiceEdgeStats
	for rows.Next() {
		var e ServiceEdgeStats
		if err := rows.Scan(&e.Service, &e.Calls, &e.ErrorRate, &e.AvgMs, &e.P99Ms); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// CalleesOf returns services that `service` calls (outgoing dependency view).
func (s *Store) CalleesOf(ctx context.Context, service string, since time.Duration) ([]ServiceEdgeStats, error) {
	cutoff := time.Now().Add(-since)
	rows, err := s.conn.Query(ctx, `
		SELECT peer_service AS callee,
		       count() AS calls,
		       countIf(status_code = 'error') / count() * 100 AS error_rate,
		       avg(duration) / 1e6 AS avg_ms,
		       quantile(0.99)(duration) / 1e6 AS p99_ms
		FROM spans
		WHERE time >= ?
		  AND service_name = ?
		  AND peer_service != ''
		  AND kind IN ('client', 'producer')
		GROUP BY callee
		ORDER BY calls DESC`, cutoff, service)
	if err != nil { return nil, err }
	defer rows.Close()
	var out []ServiceEdgeStats
	for rows.Next() {
		var e ServiceEdgeStats
		if err := rows.Scan(&e.Service, &e.Calls, &e.ErrorRate, &e.AvgMs, &e.P99Ms); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}
