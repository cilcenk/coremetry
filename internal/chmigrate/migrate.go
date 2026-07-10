// Package chmigrate copies historical data from a single-node
// ClickHouse instance into a Distributed-CH cluster managed by
// Coremetry. Day-partitioned via ClickHouse's remote() function so
// each step is bounded, resumable, and observable.
package chmigrate

import (
	"context"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/ClickHouse/clickhouse-go/v2/lib/driver"
)

// SourceConfig points the migration at a remote single-node CH.
// Username / Password feed straight into the remote() function;
// the migration runs entirely server-side on the cluster (no
// data flows through this process), so credentials only need
// network read access from the cluster's CH nodes.
type SourceConfig struct {
	Addr     string // e.g. "old-ch:9000"
	Database string // e.g. "coremetry"
	Username string
	Password string
}

// Plan is a single-table migration job. Produces a sequence of
// per-day INSERT SELECTs from remote → local. The table name is
// the un-suffixed logical name; Migrate() resolves the local
// shard name (`<name>_local` in cluster mode, `<name>` otherwise).
type Plan struct {
	Table    string    // "spans" | "logs" | "metric_points" | "profiles"
// exemplars/span_links are EXCLUDED by decision (exemplar audit v0.8.431,
// açık soru 4): their TTL rides retention.spans (days, not weeks) — by the
// time a single-node→cluster migration runs, the copyable window is a
// rounding error and the pivot degrades gracefully without it.
	TimeCol  string    // "time" (default) | "start_time" for profiles
	From, To time.Time // date range (inclusive on `From`, exclusive on `To`)
}

// Migrator runs a slice of Plans serially, day by day, with
// progress logging and per-day idempotency. Re-running with the
// same range is safe: a day whose row count already matches the
// remote source is skipped.
type Migrator struct {
	Conn   driver.Conn
	Source SourceConfig
	// LocalTable resolves a logical name to the actual local
	// flavour. In cluster mode this returns "<name>_local"; in
	// single-node mode it's identity. Wired by the caller so the
	// migrator stays agnostic of cluster mode internals.
	LocalTable func(name string) string
	// Progress is invoked after each day finishes (success or
	// skip). Non-nil-only; safe to leave nil for quiet runs.
	Progress func(day time.Time, plan Plan, copied uint64, skipped bool)
}

// Run executes every plan, day by day, in chronological order.
// Returns on first error so the caller can retry the same range
// after fixing the underlying issue (network, credentials, etc.).
func (m *Migrator) Run(ctx context.Context, plans []Plan) error {
	if m.Source.Addr == "" {
		return fmt.Errorf("source addr required")
	}
	if m.LocalTable == nil {
		m.LocalTable = func(s string) string { return s }
	}
	for _, p := range plans {
		if p.From.After(p.To) || p.From.Equal(p.To) {
			return fmt.Errorf("plan %s: empty range %s..%s", p.Table, p.From, p.To)
		}
		log.Printf("[migrate] %s: %s..%s", p.Table, p.From.Format("2006-01-02"), p.To.Format("2006-01-02"))
		for day := p.From; day.Before(p.To); day = day.AddDate(0, 0, 1) {
			if err := ctx.Err(); err != nil {
				return err
			}
			copied, skipped, err := m.copyDay(ctx, p, day)
			if err != nil {
				return fmt.Errorf("%s %s: %w", p.Table, day.Format("2006-01-02"), err)
			}
			if m.Progress != nil {
				m.Progress(day, p, copied, skipped)
			}
		}
	}
	return nil
}

// copyDay performs the per-day INSERT SELECT FROM remote(),
// guarded by an idempotency check: if the local count for `day`
// already matches the remote count, we skip.
func (m *Migrator) copyDay(ctx context.Context, p Plan, day time.Time) (uint64, bool, error) {
	timeCol := p.TimeCol
	if timeCol == "" {
		timeCol = "time"
	}
	dayStr := day.Format("2006-01-02")
	dayPredicate := fmt.Sprintf("toDate(%s) = '%s'", timeCol, dayStr)

	remoteRef := m.remoteRef(p.Table)
	localTable := m.LocalTable(p.Table)

	// Idempotency: count both sides; skip if equal.
	var remoteCount, localCount uint64
	if err := m.Conn.QueryRow(ctx,
		"SELECT count() FROM "+remoteRef+" WHERE "+dayPredicate,
	).Scan(&remoteCount); err != nil {
		return 0, false, fmt.Errorf("count remote: %w", err)
	}
	if err := m.Conn.QueryRow(ctx,
		"SELECT count() FROM "+localTable+" WHERE "+dayPredicate,
	).Scan(&localCount); err != nil {
		return 0, false, fmt.Errorf("count local: %w", err)
	}
	if localCount >= remoteCount {
		return localCount, true, nil
	}
	if remoteCount == 0 {
		return 0, true, nil
	}

	// Bulk copy. This runs entirely inside CH server-side; the
	// migrator process just issues the statement.
	stmt := fmt.Sprintf("INSERT INTO %s SELECT * FROM %s WHERE %s",
		localTable, remoteRef, dayPredicate)
	if err := m.Conn.Exec(ctx, stmt); err != nil {
		return 0, false, fmt.Errorf("insert: %w", err)
	}
	return remoteCount, false, nil
}

// remoteRef builds the `remote(addr, db, table, user, pass)`
// expression for the configured source. Single-quoting is the
// standard CH pattern — `pass` flows in via parameter only when
// non-empty so we don't paint an `''` argument the auth check
// would reject.
func (m *Migrator) remoteRef(table string) string {
	s := m.Source
	parts := []string{
		quoteSQL(s.Addr),
		quoteSQL(s.Database),
		quoteSQL(table),
		quoteSQL(s.Username),
	}
	if s.Password != "" {
		parts = append(parts, quoteSQL(s.Password))
	}
	return "remote(" + strings.Join(parts, ", ") + ")"
}

// quoteSQL surrounds a string literal with single quotes and
// escapes any embedded single quote. Used only on operator-
// supplied configuration values that are NOT user-controllable
// at runtime.
func quoteSQL(s string) string {
	return "'" + strings.ReplaceAll(s, "'", "\\'") + "'"
}
