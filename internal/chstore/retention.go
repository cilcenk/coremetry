package chstore

import (
	"context"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// Runtime data-retention controls. The retention TTL on each signal
// table is initially set at table-create time from config.yaml's
// retention block, but operators can override it live via the admin
// UI without a restart.
//
// Storage: each signal gets a row in system_settings with a string
// value of the form "<n><unit>", where unit is 'h' (hours) or 'd'
// (days). Examples: "48h", "30d". This is denser to read than two
// separate columns and supports both granularities in one shape.
//
// Apply: SetRetention runs ALTER TABLE ... MODIFY TTL on the
// underlying table. ClickHouse re-evaluates TTL on the next merge,
// so deletions happen lazily — the new policy fully takes effect
// within ~10 minutes for most tables.

// RetentionSpec is the on-the-wire shape sent by /api/settings/retention.
// Empty / zero fields preserve the existing value for that signal.
type RetentionSpec struct {
	Spans    string `json:"spans,omitempty"`    // e.g. "48h", "30d"
	Logs     string `json:"logs,omitempty"`
	Metrics  string `json:"metrics,omitempty"`
	Profiles string `json:"profiles,omitempty"`
}

// GetRetention reads the current overrides. Falls back to the
// config-file defaults via the caller (we don't peek at config from
// this layer; just return what's persisted in system_settings).
func (s *Store) GetRetention(ctx context.Context) (RetentionSpec, error) {
	rows, err := s.conn.Query(ctx, `
		SELECT key, value FROM system_settings FINAL
		WHERE key LIKE 'retention.%'`)
	if err != nil {
		return RetentionSpec{}, err
	}
	defer rows.Close()
	var sp RetentionSpec
	for rows.Next() {
		var k, v string
		if err := rows.Scan(&k, &v); err != nil {
			return RetentionSpec{}, err
		}
		switch k {
		case "retention.spans":    sp.Spans = v
		case "retention.logs":     sp.Logs = v
		case "retention.metrics":  sp.Metrics = v
		case "retention.profiles": sp.Profiles = v
		}
	}
	return sp, rows.Err()
}

// SetRetention persists the new retention values + applies them via
// ALTER TABLE MODIFY TTL. Only fields with a non-empty value are
// touched; empty preserves the existing setting.
//
// `actor` is the user email (or "system" on boot replay) recorded for
// audit. Per-table TTL is set against that table's primary time
// column — toDate(time) for spans/logs/metrics (day-precision is
// enough at high cardinality), `start_time` for profiles, plain
// `time` for metric_points.
func (s *Store) SetRetention(ctx context.Context, sp RetentionSpec, actor string) error {
	type tbl struct {
		key       string
		val       string
		table     string
		ttlExpr   string // ClickHouse TTL expression — substitution placeholder is `%s` for the interval
	}
	plans := []tbl{
		{"retention.spans",    sp.Spans,    "spans",         "toDate(time) + INTERVAL %s"},
		{"retention.logs",     sp.Logs,     "logs",          "toDate(time) + INTERVAL %s"},
		{"retention.metrics",  sp.Metrics,  "metric_points", "toDate(time) + INTERVAL %s"},
		{"retention.profiles", sp.Profiles, "profiles",      "toDate(start_time) + INTERVAL %s"},
	}
	for _, p := range plans {
		if p.val == "" {
			continue
		}
		interval, err := parseRetention(p.val)
		if err != nil {
			return fmt.Errorf("bad retention for %s: %w", p.key, err)
		}
		ttl := fmt.Sprintf(p.ttlExpr, interval)
		// Apply to the live table. ALTER MODIFY TTL is online — does
		// not lock the table, takes effect at next merge cycle.
		if err := s.conn.Exec(ctx,
			fmt.Sprintf("ALTER TABLE %s MODIFY TTL %s", p.table, ttl)); err != nil {
			return fmt.Errorf("apply TTL on %s: %w", p.table, err)
		}
		// Persist the override.
		if err := s.upsertSetting(ctx, p.key, p.val, actor); err != nil {
			return fmt.Errorf("persist %s: %w", p.key, err)
		}
	}
	return nil
}

func (s *Store) upsertSetting(ctx context.Context, key, value, actor string) error {
	now := time.Now().UTC()
	batch, err := s.conn.PrepareBatch(ctx,
		`INSERT INTO system_settings (key, value, updated_at, updated_by, version)`)
	if err != nil {
		return err
	}
	if err := batch.Append(key, value, now, actor, uint64(now.UnixNano())); err != nil {
		return err
	}
	return batch.Send()
}

// ApplyPersistedRetention re-runs the live ALTERs from whatever's
// currently in system_settings. Called on boot so a restart picks up
// previously-persisted overrides without the operator having to click
// "Apply" again.
func (s *Store) ApplyPersistedRetention(ctx context.Context) error {
	sp, err := s.GetRetention(ctx)
	if err != nil {
		return err
	}
	if sp == (RetentionSpec{}) {
		return nil // nothing persisted yet — config defaults stay in effect
	}
	return s.SetRetention(ctx, sp, "system:boot")
}

// parseRetention turns "48h" / "30d" into a ClickHouse INTERVAL
// expression like "48 HOUR" / "30 DAY". Rejects anything else so we
// don't ALTER the table to garbage.
var retentionRe = regexp.MustCompile(`^([1-9][0-9]*)([hd])$`)

func parseRetention(s string) (string, error) {
	s = strings.TrimSpace(strings.ToLower(s))
	m := retentionRe.FindStringSubmatch(s)
	if m == nil {
		return "", fmt.Errorf("expected <n>h or <n>d, got %q", s)
	}
	n, _ := strconv.Atoi(m[1])
	unit := "DAY"
	if m[2] == "h" {
		unit = "HOUR"
	}
	return fmt.Sprintf("%d %s", n, unit), nil
}
