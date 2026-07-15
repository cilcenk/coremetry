package chstore

import (
	"context"
	"fmt"
	"log"
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
		key   string
		val   string
		table string
		col   string // timestamp column the TTL is computed against
		// persist writes the system_settings override row. False for
		// tables that RIDE another signal's key (exemplars ← retention.spans)
		// so the shared key is upserted exactly once per apply.
		persist bool
	}
	plans := []tbl{
		{"retention.spans", sp.Spans, "spans", "time", true},
		// v0.8.328 — exemplars follow the SPANS retention on purpose (not
		// metrics): an OTLP exemplar's payload is its trace link, and an
		// exemplar outliving its trace is a dead click. Keyed off the same
		// retention.spans value so an operator TTL edit propagates here in
		// the same apply.
		{"retention.spans", sp.Spans, "exemplars", "timestamp", false},
		// v0.8.329 — span links (both directions) ride the SPANS retention
		// too: a link outliving its spans is a dead edge whichever way it's
		// traversed. Same ride-along persist:false pattern — the shared
		// retention.spans key is upserted once by the spans row above.
		{"retention.spans", sp.Spans, "span_links", "time", false},
		{"retention.spans", sp.Spans, "span_links_reverse", "time", false},
		{"retention.logs", sp.Logs, "logs", "time", true},
		{"retention.metrics", sp.Metrics, "metric_points", "time", true},
		{"retention.profiles", sp.Profiles, "profiles", "start_time", true},
	}
	for _, p := range plans {
		if p.val == "" {
			continue
		}
		ttl, err := buildRetentionTTL(p.val, p.col)
		if err != nil {
			return fmt.Errorf("bad retention for %s: %w", p.key, err)
		}
		// Apply to the live table. ALTER MODIFY TTL is online —
		// doesn't lock the table — but CH defaults to
		// materialize_ttl_after_modify=1 which synchronously
		// re-evaluates TTL across every existing partition
		// during the ALTER. On a banking-scale spans table that
		// blocks the HTTP request for minutes → gateway 504. We
		// disable that step so the ALTER just updates table
		// metadata and returns immediately; the new TTL still
		// takes effect on the next merge cycle which CH runs
		// every ~10 min, with no user-visible difference apart
		// from the operator's request completing in <1s.
		//
		// alter_sync=0 makes the ALTER fire-and-forget on
		// ReplicatedMergeTree clusters too: we don't wait for
		// other replicas to acknowledge the metadata change.
		// Each replica will pick it up via the replication log
		// within seconds — but we don't block the operator on
		// network latency.
		stmt := fmt.Sprintf(
			"ALTER TABLE %s MODIFY TTL %s SETTINGS materialize_ttl_after_modify = 0, alter_sync = 0",
			p.table, ttl)
		// execDDL routes through adaptDDL so cluster-mode installs
		// ALTER the `_local` shard tables with ON CLUSTER instead
		// of touching only the Distributed wrapper (which has no
		// TTL). Single-node mode runs the SQL unchanged.
		if err := s.execDDL(ctx, stmt); err != nil {
			if !isClusterUnsupportedAlter(err) {
				return fmt.Errorf("apply TTL on %s: %w", p.table, err)
			}
			// External Distributed `spans` with cluster_name unset: the
			// wrapper engine can't carry a TTL and adaptDDL can't rewrite to
			// <table>_local ON CLUSTER without the cluster name. Skip the apply
			// (don't crash-loop boot / 500 the admin PUT) but still record the
			// intent below, so it's re-applied automatically if cluster_name is
			// later set. Until then the operator manages TTL on the per-shard
			// local table. (v0.8.162 — operator-reported: distributed prod
			// errored "Engine Distributed doesn't support TTL clause" each cycle.)
			log.Printf("[chstore] retention TTL on %s not applied: %v — set chstore.cluster_name or apply TTL on the per-shard local table; the override is still recorded", p.table, err)
		}
		// Persist the override (once per key — ride-along tables skip).
		if !p.persist {
			continue
		}
		if err := s.upsertSetting(ctx, p.key, p.val, actor); err != nil {
			return fmt.Errorf("persist %s: %w", p.key, err)
		}
	}
	if sp.Spans != "" {
		s.applyExemplarColTTL(ctx, sp.Spans)
	}
	return nil
}

// exemplarStateMVs — the MVs whose argMax/argMaxIf states copy a trace_id
// out of spans (store.go, the spanmetrics tiers). Their ROW TTL is set at
// create time and deliberately outlives spans (spanmetrics_1m keeps 30d of
// aggregates against a 7d spans retention) — correct for the aggregates,
// wrong for the exemplars: a trace_id that outlives its trace is a dead
// click, the same reasoning that puts `exemplars` on retention.spans above.
//
// So the exemplar COLUMNS get a column-level TTL riding retention.spans
// while the row TTL keeps the aggregate history. Verified on CH 24.8: a row
// past the column TTL but inside the row TTL keeps countMerge/quantiles and
// returns '' for the exemplar.
//
// The shorter tiers (10s → 2d, 1s → 6h) can't produce a dead link today —
// they expire before spans — but they're listed anyway: the column TTL is a
// no-op while the row TTL is shorter, and it becomes the fix the moment an
// operator drops spans retention below 2d.
var exemplarStateMVs = []string{"spanmetrics_1m", "spanmetrics_10s", "spanmetrics_1s"}

var exemplarStateCols = []string{"slow_exemplar_state", "error_exemplar_state"}

// exemplarColTTLStmt builds the column-TTL ALTER. Split out for a pure test:
// the type is threaded through verbatim because CH REJECTS a type-less
// `MODIFY COLUMN <c> TTL <expr>` (syntax error on 24.8), so the caller must
// echo the column's CURRENT type back — read from system.columns, never
// hardcoded, or a drifted AggregateFunction signature would be silently
// rewritten.
func exemplarColTTLStmt(inner, onCluster, col, typ, ttl string) string {
	return fmt.Sprintf(
		"ALTER TABLE `%s`%s MODIFY COLUMN `%s` %s TTL %s"+
			" SETTINGS materialize_ttl_after_modify = 0, alter_sync = 0",
		inner, onCluster, col, typ, ttl)
}

// applyExemplarColTTL expires the exemplar trace_ids in the spanmetrics MVs
// at the spans retention, leaving their aggregates on the row TTL.
//
// Best-effort by design: every failure logs and moves on, exactly like the
// isClusterUnsupportedAlter skip above. A missing column TTL costs a dead
// link on an aged bucket; failing the operator's whole retention PUT (or
// crash-looping boot replay) costs more.
//
// Why the inner table: a combined MV rejects TTL outright ("Engine
// MaterializedView doesn't support TTL clause", CH 24.8), so the ALTER has
// to name the storage behind it — `.inner_id.<uuid>`. That name is stable
// across shards (Atomic DB, one uuid propagated by the ON CLUSTER create),
// which is what makes s.onCluster() valid here. execDDL is deliberately NOT
// used: adaptDDL rewrites by table name and would hand us back the MV.
func (s *Store) applyExemplarColTTL(ctx context.Context, spansVal string) {
	ttl, err := buildRetentionTTL(spansVal, "time_bucket")
	if err != nil {
		log.Printf("[chstore] exemplar column TTL skipped: bad retention %q: %v", spansVal, err)
		return
	}
	for _, mv := range exemplarStateMVs {
		name := mv
		if s.clusterMode() && highVolumeTables[mv] {
			name = mv + "_local"
		}
		inner, ok := s.mvInnerTable(ctx, name)
		if !ok {
			log.Printf("[chstore] exemplar column TTL skipped for %s: inner table unresolved (TO-table MV or absent) — exemplars there may outlive their traces", name)
			continue
		}
		for _, col := range exemplarStateCols {
			typ, ok := s.columnType(ctx, inner, col)
			if !ok {
				continue // column absent — nothing to expire
			}
			if err := s.execWithReadonlyRetry(ctx,
				exemplarColTTLStmt(inner, s.onCluster(), col, typ, ttl)); err != nil {
				log.Printf("[chstore] exemplar column TTL on %s.%s not applied: %v — exemplars there may outlive their traces", name, col, err)
			}
		}
	}
}

// mvInnerTable resolves a combined MV's storage table (`.inner_id.<uuid>`).
// A TO-table MV has no uuid of its own and returns false.
func (s *Store) mvInnerTable(ctx context.Context, mv string) (string, bool) {
	var uuid string
	row := s.conn.QueryRow(ctx, `
		SELECT toString(uuid) FROM system.tables
		WHERE database = currentDatabase() AND name = ? AND engine = 'MaterializedView'`, mv)
	if err := row.Scan(&uuid); err != nil || uuid == "" ||
		uuid == "00000000-0000-0000-0000-000000000000" {
		return "", false
	}
	return ".inner_id." + uuid, true
}

// columnType reads a column's CURRENT declared type — see exemplarColTTLStmt
// for why the ALTER cannot omit it.
func (s *Store) columnType(ctx context.Context, table, col string) (string, bool) {
	var typ string
	row := s.conn.QueryRow(ctx, `
		SELECT type FROM system.columns
		WHERE database = currentDatabase() AND table = ? AND name = ?`, table, col)
	if err := row.Scan(&typ); err != nil || typ == "" {
		return "", false
	}
	return typ, true
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

// buildRetentionTTL turns a "<n>h" / "<n>d" retention shorthand plus
// the table's timestamp column into a correct ClickHouse TTL right-
// hand-side.
//
// v0.6.36 — operator-reported: spans/logs/metrics/profiles silently
// drained because retention.spans = "1h" had been persisted via the
// admin UI, and the previous template `toDate(time) + INTERVAL %s`
// expanded to `toDate(time) + INTERVAL 1 HOUR` — i.e. midnight + 1h
// = 01:00 of the SAME day. Every row inserted after 01:00 was already
// past TTL and got dropped on the next merge cycle. The pre-existing
// "Nd" case worked because adding DAYS to a date stays meaningful.
//
// v0.6.37 — the first cut of v0.6.36 used `<col> + INTERVAL N HOUR`
// for the hour case, but `time` is `DateTime64(9)` and CH rejects
// non-DateTime/Date results in a TTL expression with error 450
// ("TTL expression result column should have DateTime or Date type").
// Wrap in `toDateTime(...)` to drop the nanosecond precision so the
// result is plain DateTime — still a rolling N-hour window from the
// row time, just at second granularity (irrelevant for retention).
//
// The fix splits the expression by unit:
//   - "Nd"  → `toDate(<col>) + INTERVAL N DAY`        (partition-aligned;
//             lets CH DROP whole day partitions cheaply)
//   - "Nh"  → `toDateTime(<col>) + INTERVAL N HOUR`   (row-level TTL,
//             DateTime-typed result; correct rolling window)
//
// Hour-granularity TTLs can't ride the partition-drop fast path
// because spans are PARTITION BY toDate(time) — a 1-hour TTL crosses
// at most one partition boundary per day and needs row-level cleanup
// anyway. Day-granularity stays on the partition boundary so
// banking-scale tables keep their O(1) cleanup.
var retentionRe = regexp.MustCompile(`^([1-9][0-9]*)([hd])$`)

func buildRetentionTTL(s, col string) (string, error) {
	s = strings.TrimSpace(strings.ToLower(s))
	m := retentionRe.FindStringSubmatch(s)
	if m == nil {
		return "", fmt.Errorf("expected <n>h or <n>d, got %q", s)
	}
	n, _ := strconv.Atoi(m[1])
	if m[2] == "h" {
		return fmt.Sprintf("toDateTime(%s) + INTERVAL %d HOUR", col, n), nil
	}
	return fmt.Sprintf("toDate(%s) + INTERVAL %d DAY", col, n), nil
}
