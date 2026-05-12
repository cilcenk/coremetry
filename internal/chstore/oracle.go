package chstore

import (
	"context"
	"hash/fnv"
	"math"
	"strings"
	"time"
)

// OracleMetrics is the OracleDB-receiver-flavoured drill-down
// payload — what the operator sees when expanding a row whose
// db.system = "oracle" on /databases. The numbers come from the
// OpenTelemetry oracledb receiver, which scrapes V$ views on
// the database itself and publishes them as oracledb.*
// instrument-shaped metric_points.
//
// When the receiver isn't wired up (or the operator is still
// proving the integration on a staging cluster), we fall back
// to deterministic synthetic values so the UI doesn't look
// empty — Synthetic=true tells the frontend to render a
// "demo data" badge over the panel.
//
// The two cumulative gauges (logical/physical reads) get
// converted to per-second rates server-side; the operator
// reads "37k logical reads/sec" rather than the raw
// monotonic counter that Oracle exposes.
type OracleMetrics struct {
	Instance       string             `json:"instance"`
	Synthetic      bool               `json:"synthetic"`
	WindowSeconds  float64            `json:"windowSeconds"`
	// Status — "up" when any oracledb.* metric_points exist in
	// the window; "down" otherwise. Mirrors the Oracle Grafana
	// dashboard's database-alive indicator (its `oracledb_up`
	// stat panel) — the first thing an SRE looks at when paged.
	Status         string             `json:"status"`
	Sessions       OracleSessions     `json:"sessions"`
	Processes      OracleGaugeWithCap `json:"processes"`
	CPUTimeSec     float64            `json:"cpuTimeSec"`
	PGAMemoryBytes float64            `json:"pgaMemoryBytes"`
	SGAMemoryBytes float64            `json:"sgaMemoryBytes"` // shared global area
	LogicalReadsPS float64            `json:"logicalReadsPerSec"`
	PhysicalReadsPS float64           `json:"physicalReadsPerSec"`
	CacheHitPct    float64            `json:"cacheHitPct"`
	HardParsesPS   float64            `json:"hardParsesPerSec"`
	ParseCallsPS   float64            `json:"parseCallsPerSec"`
	ExecutionsPS   float64            `json:"executionsPerSec"`
	UserCommitsPS  float64            `json:"userCommitsPerSec"`
	RollbacksPS    float64            `json:"userRollbacksPerSec"`
	TransactionsPS float64            `json:"transactionsPerSec"`
	// Row-lock waits per second. Concurrency wait class subset —
	// the canonical Oracle "is something blocked behind a long
	// transaction" indicator. Surfaced as its own KPI because
	// SREs page off this independently of the broader wait-class
	// distribution.
	RowLockWaitsPS float64            `json:"rowLockWaitsPerSec"`
	// Top wait classes over the window, descending by accumulated
	// time. Mirrors the Grafana "System Wait Classes" panel which
	// breaks down where the DB is actually spending its time.
	// SREs read this as the answer to "the DB is slow — slow at
	// what?" (network? user_io? commit?).
	WaitClasses    []OracleWaitClass  `json:"waitClasses"`
	// Top SQL by total elapsed seconds in the window. Mirrors
	// Grafana's `oracledb_top_sql_elapsed` panel — the heaviest
	// statements in the DB's own measurement, complementary to
	// our span-derived db_statement top list (which only sees
	// what the application traced).
	TopSQL         []OracleSQL        `json:"topSQL"`
	Tablespaces    []OracleTablespace `json:"tablespaces"`
}

// OracleSessions extends the basic gauge with an active/inactive
// split — the SRE's first triage question after "how many
// sessions" is "of those, how many are doing work right now?".
type OracleSessions struct {
	Usage    float64 `json:"usage"`
	Limit    float64 `json:"limit"`
	Active   float64 `json:"active"`
	Inactive float64 `json:"inactive"`
}

// OracleWaitClass is one row of the wait-class distribution.
// PerSec is computed as (sum of cumulative wait time over
// window) / window seconds — i.e. average waiting-seconds per
// real-time second. A value of 1.0 means one full second of
// wait per second elapsed: a single concurrent client fully
// blocked on this class.
type OracleWaitClass struct {
	Name   string  `json:"name"`
	PerSec float64 `json:"perSec"`
}

// OracleSQL captures one row of the Top SQL view. ElapsedSec
// is total cumulative elapsed time in the window; Executions
// is the run count. The SRE reads "executions × avg_elapsed"
// to decide whether a slow statement is slow because it runs
// constantly or because each run is heavy.
type OracleSQL struct {
	SQL         string  `json:"sql"`
	ElapsedSec  float64 `json:"elapsedSec"`
	Executions  uint64  `json:"executions"`
	AvgElapsedMs float64 `json:"avgElapsedMs"`
}

// OracleGaugeWithCap is a (usage, limit) pair — Oracle exposes
// both as separate metrics (oracledb.sessions.usage,
// oracledb.sessions.limit). The frontend renders these as a
// progress bar so the operator sees "67/200 sessions" at a
// glance.
type OracleGaugeWithCap struct {
	Usage float64 `json:"usage"`
	Limit float64 `json:"limit"`
}

// OracleTablespace is one row of the per-tablespace size table.
// oracledb.tablespace_size.usage / .limit are dimensioned by
// the "tablespace_name" attribute, so the operator can spot a
// specific tablespace running out of room (the #1 reason an
// Oracle DBA gets paged at 3am).
type OracleTablespace struct {
	Name      string  `json:"name"`
	UsedBytes float64 `json:"usedBytes"`
	MaxBytes  float64 `json:"maxBytes"`
	UsedPct   float64 `json:"usedPct"`
}

// GetOracleMetrics returns the OracleDB-receiver-style drill-down
// for one instance. When no oracledb.* points exist in the
// window, returns a deterministic synthetic payload with
// Synthetic=true so the UI can still render and the operator
// can visualise what the panel will look like once their
// receiver is online.
//
// The instance argument matches peer_service on the spans the
// row was derived from — we use it both as a deterministic
// seed for synthetic generation (same instance → same fake
// numbers across reloads) and as a `instance` attribute
// filter on metric_points if the receiver tags points with
// it (newer oracledb receiver versions do).
func (s *Store) GetOracleMetrics(
	ctx context.Context, instance string, from, to time.Time,
) (*OracleMetrics, error) {
	if from.IsZero() {
		from = time.Now().Add(-1 * time.Hour)
	}
	if to.IsZero() {
		to = time.Now()
	}
	windowSec := to.Sub(from).Seconds()
	if windowSec <= 0 {
		windowSec = 60
	}

	out := &OracleMetrics{
		Instance:      instance,
		WindowSeconds: windowSec,
		Status:        "down",
		Tablespaces:   []OracleTablespace{},
		WaitClasses:   []OracleWaitClass{},
		TopSQL:        []OracleSQL{},
	}

	// Optional instance-scoped filter. The OracleDB receiver
	// publishes points with res_keys carrying the service it
	// scraped against; we look for either an `instance`
	// attribute (newer receivers) or a `service.name` resource
	// key fallback. The hasInstanceFilter switch lets us drop
	// the filter when the operator's setup doesn't tag points
	// per-instance (single-DB deployment).
	hasInstanceFilter := instance != "" && instance != "unknown"

	// Pull the latest value per metric over the window. We use
	// argMax(value, time) to grab the freshest reading; that's
	// the natural read for a gauge-shaped metric. Cumulative
	// counters get their delta from min/max below.
	gauges, err := s.queryOracleGauges(ctx, from, to, instance, hasInstanceFilter)
	if err == nil && len(gauges) > 0 {
		out.Sessions.Usage = gauges["oracledb.sessions.usage"]
		out.Sessions.Limit = gauges["oracledb.sessions.limit"]
		out.Processes.Usage = gauges["oracledb.processes.usage"]
		out.Processes.Limit = gauges["oracledb.processes.limit"]
		out.PGAMemoryBytes = gauges["oracledb.pga_memory"]
		// SGA appears under either oracledb.sga_max_size (OTel) or
		// oracledb.sga.size (Prometheus exporter). Prefer the
		// freshest non-zero reading.
		if v := gauges["oracledb.sga_max_size"]; v > 0 {
			out.SGAMemoryBytes = v
		} else if v := gauges["oracledb.sga.size"]; v > 0 {
			out.SGAMemoryBytes = v
		}
		// Status — any gauge means the receiver is talking.
		out.Status = "up"
	}

	// Sessions breakdown (active vs inactive) — the Oracle Prom
	// exporter publishes `oracledb_sessions_value` dimensioned by
	// status. We mirror that with attr-keyed metric_points reads.
	if act, inact, err := s.queryOracleSessionsByStatus(ctx, from, to, instance, hasInstanceFilter); err == nil {
		if act > 0 || inact > 0 {
			out.Sessions.Active = act
			out.Sessions.Inactive = inact
			// If we didn't get a Usage from the gauge query, sum
			// here so the progress bar still works.
			if out.Sessions.Usage == 0 {
				out.Sessions.Usage = act + inact
			}
		}
	}

	// Wait-class distribution (10 standard Oracle classes). The
	// Prom exporter publishes `oracledb.wait_time.<class>` as
	// cumulative seconds-waited; we derive perSec exactly like
	// the other counters.
	if waits, err := s.queryOracleWaitClasses(ctx, from, to, instance, hasInstanceFilter, windowSec); err == nil && len(waits) > 0 {
		out.WaitClasses = waits
		// Row-lock waits live under the concurrency class. The OTel
		// receiver exposes a dedicated metric too — pick the
		// freshest of either when present.
		for _, w := range waits {
			if strings.EqualFold(w.Name, "row_lock") || strings.EqualFold(w.Name, "rowlock") {
				out.RowLockWaitsPS = w.PerSec
			}
		}
	}
	if rl, ok := s.queryOracleRowLockWaits(ctx, from, to, instance, hasInstanceFilter, windowSec); ok {
		out.RowLockWaitsPS = rl
	}

	// Top SQL by elapsed time — Oracle's V$SQL ranks statements by
	// total cumulative elapsed seconds. We read it from
	// metric_points carrying the sql_id / sql_text dimensions
	// the Prom exporter emits.
	if top, err := s.queryOracleTopSQL(ctx, from, to, instance, hasInstanceFilter); err == nil && len(top) > 0 {
		out.TopSQL = top
	}

	// Cumulative counters → per-second rates. max-min over the
	// window divided by windowSec is the OTel-recommended
	// derivation for monotonic sums when the SDK doesn't
	// already export deltas.
	rates, err := s.queryOracleRates(ctx, from, to, instance, hasInstanceFilter, windowSec)
	if err == nil && len(rates) > 0 {
		out.CPUTimeSec = rates["oracledb.cpu_time"] * windowSec // back-multiply: total over window
		out.LogicalReadsPS = rates["oracledb.logical_reads"]
		out.PhysicalReadsPS = rates["oracledb.physical_reads"]
		out.HardParsesPS = rates["oracledb.hard_parses"]
		out.ParseCallsPS = rates["oracledb.parse_calls"]
		out.ExecutionsPS = rates["oracledb.executions"]
		out.UserCommitsPS = rates["oracledb.user_commits"]
		out.RollbacksPS = rates["oracledb.user_rollbacks"]
		out.TransactionsPS = rates["oracledb.transactions"]
	}

	// Tablespace breakdown.
	tspaces, err := s.queryOracleTablespaces(ctx, from, to, instance, hasInstanceFilter)
	if err == nil && len(tspaces) > 0 {
		out.Tablespaces = tspaces
	}

	// Detect "no real data" — if every numeric field is zero
	// AND tablespace list is empty, the receiver isn't wired
	// up. Fall back to synthetic.
	if isOracleEmpty(out) {
		fillSynthetic(out, instance)
		out.Synthetic = true
	}

	// Derive cache hit % from logical / physical reads. Never
	// trust user-typed math: if logical is zero, hit % is
	// undefined; clamp to 0..100.
	if out.LogicalReadsPS > 0 {
		hit := 1 - (out.PhysicalReadsPS / out.LogicalReadsPS)
		if hit < 0 {
			hit = 0
		}
		if hit > 1 {
			hit = 1
		}
		out.CacheHitPct = hit * 100
	}

	return out, nil
}

func (s *Store) queryOracleGauges(
	ctx context.Context, from, to time.Time, instance string, withInstance bool,
) (map[string]float64, error) {
	// argMax over the window picks the freshest point per
	// metric — exactly what a gauge reads as "right now".
	q := `
		SELECT metric, argMax(value, time) AS v
		FROM metric_points
		WHERE time >= ? AND time <= ?
		  AND startsWith(metric, 'oracledb.')
		` + oracleInstanceClause(withInstance) + `
		GROUP BY metric`
	args := []any{from, to}
	if withInstance {
		args = append(args, instance, instance)
	}
	rows, err := s.conn.Query(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]float64{}
	for rows.Next() {
		var m string
		var v float64
		if err := rows.Scan(&m, &v); err == nil {
			out[m] = v
		}
	}
	return out, nil
}

func (s *Store) queryOracleRates(
	ctx context.Context, from, to time.Time, instance string, withInstance bool, windowSec float64,
) (map[string]float64, error) {
	// For cumulative counters: (max - min) / window seconds.
	// CH's max - min on monotonic series tolerates one reset
	// in the window cleanly (rate goes to 0 for that reading,
	// which is the safer underestimate vs a wrap-around spike).
	q := `
		SELECT metric, (max(value) - min(value)) / ? AS rate
		FROM metric_points
		WHERE time >= ? AND time <= ?
		  AND startsWith(metric, 'oracledb.')
		` + oracleInstanceClause(withInstance) + `
		GROUP BY metric`
	args := []any{windowSec, from, to}
	if withInstance {
		args = append(args, instance, instance)
	}
	rows, err := s.conn.Query(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]float64{}
	for rows.Next() {
		var m string
		var v float64
		if err := rows.Scan(&m, &v); err == nil {
			if v < 0 {
				v = 0 // counter reset → suppress
			}
			out[m] = v
		}
	}
	return out, nil
}

func (s *Store) queryOracleTablespaces(
	ctx context.Context, from, to time.Time, instance string, withInstance bool,
) ([]OracleTablespace, error) {
	// tablespace_name is an attribute on oracledb.tablespace_size.*
	// points. We pull both .usage and .limit, latest per
	// (tablespace, metric), and join client-side. CH's
	// arrayElement-indexOf lookup is the canonical pattern for
	// key/value attr arrays in this codebase.
	q := `
		SELECT
			attr_values[indexOf(attr_keys, 'tablespace_name')] AS ts,
			metric,
			argMax(value, time) AS v
		FROM metric_points
		WHERE time >= ? AND time <= ?
		  AND metric IN ('oracledb.tablespace_size.usage', 'oracledb.tablespace_size.limit')
		  AND has(attr_keys, 'tablespace_name')
		` + oracleInstanceClause(withInstance) + `
		GROUP BY ts, metric
		ORDER BY ts`
	args := []any{from, to}
	if withInstance {
		args = append(args, instance, instance)
	}
	rows, err := s.conn.Query(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	byName := map[string]*OracleTablespace{}
	for rows.Next() {
		var name, metric string
		var v float64
		if err := rows.Scan(&name, &metric, &v); err != nil {
			continue
		}
		if name == "" {
			continue
		}
		t, ok := byName[name]
		if !ok {
			t = &OracleTablespace{Name: name}
			byName[name] = t
		}
		switch metric {
		case "oracledb.tablespace_size.usage":
			t.UsedBytes = v
		case "oracledb.tablespace_size.limit":
			t.MaxBytes = v
		}
	}
	out := make([]OracleTablespace, 0, len(byName))
	for _, t := range byName {
		if t.MaxBytes > 0 {
			t.UsedPct = (t.UsedBytes / t.MaxBytes) * 100
		}
		out = append(out, *t)
	}
	return out, nil
}

func oracleInstanceClause(withInstance bool) string {
	if !withInstance {
		return ""
	}
	// Match either an `instance` attribute or a `service.name`
	// resource key (older oracledb receiver setups tag at
	// resource level). Pass the instance value twice.
	return `AND (
		attr_values[indexOf(attr_keys, 'instance')] = ?
		OR res_values[indexOf(res_keys, 'service.name')] = ?
	)`
}

func isOracleEmpty(o *OracleMetrics) bool {
	if len(o.Tablespaces) > 0 {
		return false
	}
	return o.Sessions.Usage == 0 && o.Sessions.Limit == 0 &&
		o.Processes.Usage == 0 && o.PGAMemoryBytes == 0 &&
		o.LogicalReadsPS == 0 && o.PhysicalReadsPS == 0 &&
		o.ExecutionsPS == 0 && o.UserCommitsPS == 0 &&
		o.TransactionsPS == 0
}

// fillSynthetic populates o with plausible, deterministic
// values seeded from the instance name. Same instance string
// produces the same numbers across reloads — the operator's
// eye gets a stable preview rather than randomness that makes
// the UI look broken.
//
// Values are calibrated to read like a moderately busy OLTP
// Oracle instance (a couple hundred sessions, ~30k logical
// reads/sec, ~1% physical/logical ratio).
func fillSynthetic(o *OracleMetrics, instance string) {
	seed := oracleSeed(instance)
	rnd := func(min, max float64) float64 {
		seed = seed*1103515245 + 12345
		f := float64(seed&0x7fffffff) / float64(0x7fffffff)
		return min + f*(max-min)
	}
	sessionsLimit := math.Round(rnd(150, 400))
	processesLimit := math.Round(rnd(200, 500))
	usage := math.Round(sessionsLimit * rnd(0.25, 0.75))
	active := math.Round(usage * rnd(0.10, 0.45)) // 10-45% actively running
	o.Sessions = OracleSessions{
		Usage:    usage,
		Limit:    sessionsLimit,
		Active:   active,
		Inactive: usage - active,
	}
	o.Processes = OracleGaugeWithCap{
		Usage: math.Round(processesLimit * rnd(0.30, 0.65)),
		Limit: processesLimit,
	}
	o.Status = "up"
	o.CPUTimeSec = rnd(40, 280) * (o.WindowSeconds / 60)
	o.PGAMemoryBytes = rnd(1.5, 6.5) * 1024 * 1024 * 1024
	o.SGAMemoryBytes = rnd(4, 24) * 1024 * 1024 * 1024
	o.LogicalReadsPS = rnd(15000, 60000)
	o.PhysicalReadsPS = o.LogicalReadsPS * rnd(0.005, 0.03)
	o.HardParsesPS = rnd(2, 25)
	o.ParseCallsPS = rnd(100, 800)
	o.ExecutionsPS = rnd(400, 3500)
	o.UserCommitsPS = rnd(20, 220)
	o.RollbacksPS = o.UserCommitsPS * rnd(0.005, 0.04)
	o.TransactionsPS = o.UserCommitsPS + o.RollbacksPS
	o.RowLockWaitsPS = rnd(0.0, 0.8) // 0..0.8 sec/sec wait — healthy range

	// Wait classes — 10 canonical Oracle classes ordered by what's
	// typically the heaviest on a healthy OLTP workload. Values
	// are scaled to one another so the distribution reads
	// plausibly (user I/O > commit > network > others).
	classes := []struct {
		name string
		hi   float64
	}{
		{"user_io",       rnd(0.8, 4.0)},
		{"commit",        rnd(0.3, 1.5)},
		{"network",       rnd(0.2, 1.0)},
		{"concurrency",   rnd(0.05, 0.6)},
		{"system_io",     rnd(0.1, 0.4)},
		{"application",   rnd(0.0, 0.3)},
		{"configuration", rnd(0.0, 0.2)},
		{"scheduler",     rnd(0.0, 0.15)},
		{"cluster",       rnd(0.0, 0.10)},
		{"other",         rnd(0.0, 0.25)},
	}
	for _, c := range classes {
		o.WaitClasses = append(o.WaitClasses, OracleWaitClass{Name: c.name, PerSec: c.hi})
	}

	// Top SQL — plausible OLTP shapes, sorted by elapsed.
	stmts := []string{
		"SELECT /*+ INDEX(o ORDERS_PK) */ * FROM orders o WHERE customer_id = :1 AND status = 'PENDING' ORDER BY created_at DESC",
		"UPDATE inventory SET available_qty = available_qty - :1 WHERE sku = :2",
		"INSERT INTO audit_log (event_id, actor, payload, ts) VALUES (:1, :2, :3, SYSTIMESTAMP)",
		"SELECT COUNT(*) FROM positions WHERE account_id = :1 AND value > :2",
		"MERGE INTO balances b USING (SELECT :1 acct, :2 amt FROM dual) s ON (b.acct = s.acct) WHEN MATCHED THEN UPDATE SET b.bal = b.bal + s.amt",
	}
	for i, sqlText := range stmts {
		execs := uint64(math.Round(rnd(50, 12000)))
		avgMs := rnd(0.4, 80) / float64(i+1) // decreasing — heaviest first
		o.TopSQL = append(o.TopSQL, OracleSQL{
			SQL:          sqlText,
			Executions:   execs,
			AvgElapsedMs: avgMs,
			ElapsedSec:   float64(execs) * avgMs / 1000,
		})
	}

	// Synthetic tablespaces — a typical Oracle DB has SYSTEM /
	// SYSAUX / USERS / TEMP / UNDOTBS1 at minimum.
	for _, name := range []string{"SYSTEM", "SYSAUX", "USERS", "UNDOTBS1", "TEMP"} {
		maxBytes := rnd(2, 32) * 1024 * 1024 * 1024
		used := maxBytes * rnd(0.20, 0.85)
		o.Tablespaces = append(o.Tablespaces, OracleTablespace{
			Name:      name,
			UsedBytes: used,
			MaxBytes:  maxBytes,
			UsedPct:   (used / maxBytes) * 100,
		})
	}
}

// queryOracleSessionsByStatus reads `oracledb_sessions_value`-style
// points keyed on the `status` attribute. Returns (active,
// inactive). Both zero is a valid "no data" signal.
func (s *Store) queryOracleSessionsByStatus(
	ctx context.Context, from, to time.Time, instance string, withInstance bool,
) (active, inactive float64, err error) {
	q := `
		SELECT lower(attr_values[indexOf(attr_keys, 'status')]) AS st,
		       argMax(value, time) AS v
		FROM metric_points
		WHERE time >= ? AND time <= ?
		  AND metric IN ('oracledb.sessions', 'oracledb_sessions_value', 'oracledb.sessions.value')
		  AND has(attr_keys, 'status')
		` + oracleInstanceClause(withInstance) + `
		GROUP BY st`
	args := []any{from, to}
	if withInstance {
		args = append(args, instance, instance)
	}
	rows, err := s.conn.Query(ctx, q, args...)
	if err != nil {
		return 0, 0, err
	}
	defer rows.Close()
	for rows.Next() {
		var st string
		var v float64
		if err := rows.Scan(&st, &v); err != nil {
			continue
		}
		switch st {
		case "active":
			active = v
		case "inactive":
			inactive = v
		}
	}
	return active, inactive, nil
}

// queryOracleWaitClasses returns one row per wait class with the
// average waiting-seconds per real-time second derived from
// cumulative `oracledb.wait_time.<class>` counters. Sorted
// descending by perSec — heaviest contention first.
func (s *Store) queryOracleWaitClasses(
	ctx context.Context, from, to time.Time, instance string, withInstance bool, windowSec float64,
) ([]OracleWaitClass, error) {
	// Match any metric matching `oracledb.wait_time.*` (OTel) or
	// `oracledb_wait_time_*` (Prom exporter). The last token is
	// the class name.
	q := `
		SELECT metric, (max(value) - min(value)) / ? AS rate
		FROM metric_points
		WHERE time >= ? AND time <= ?
		  AND (startsWith(metric, 'oracledb.wait_time.') OR startsWith(metric, 'oracledb_wait_time_'))
		` + oracleInstanceClause(withInstance) + `
		GROUP BY metric
		ORDER BY rate DESC`
	args := []any{windowSec, from, to}
	if withInstance {
		args = append(args, instance, instance)
	}
	rows, err := s.conn.Query(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []OracleWaitClass{}
	for rows.Next() {
		var metric string
		var rate float64
		if err := rows.Scan(&metric, &rate); err != nil {
			continue
		}
		if rate < 0 {
			rate = 0
		}
		// Strip the prefix to get the bare class name.
		name := metric
		for _, p := range []string{"oracledb.wait_time.", "oracledb_wait_time_"} {
			if strings.HasPrefix(metric, p) {
				name = strings.TrimPrefix(metric, p)
				break
			}
		}
		out = append(out, OracleWaitClass{Name: name, PerSec: rate})
	}
	return out, nil
}

// queryOracleRowLockWaits pulls the dedicated row-lock counter if
// the receiver emits one. Returns (rate, ok=true) on a hit;
// callers fall back to the concurrency wait class otherwise.
func (s *Store) queryOracleRowLockWaits(
	ctx context.Context, from, to time.Time, instance string, withInstance bool, windowSec float64,
) (float64, bool) {
	q := `
		SELECT (max(value) - min(value)) / ? AS rate
		FROM metric_points
		WHERE time >= ? AND time <= ?
		  AND metric IN ('oracledb.row_lock_waits', 'oracledb_row_lock_waits', 'oracledb.enq.tx.row_lock_contention')
		` + oracleInstanceClause(withInstance)
	args := []any{windowSec, from, to}
	if withInstance {
		args = append(args, instance, instance)
	}
	row := s.conn.QueryRow(ctx, q, args...)
	var rate float64
	if err := row.Scan(&rate); err != nil || rate <= 0 {
		return 0, false
	}
	return rate, true
}

// queryOracleTopSQL pulls the top-N statements by elapsed time
// from `oracledb.top_sql.elapsed`-style points. The SQL text +
// execution count ride as attributes (sql_text, executions).
func (s *Store) queryOracleTopSQL(
	ctx context.Context, from, to time.Time, instance string, withInstance bool,
) ([]OracleSQL, error) {
	q := `
		SELECT attr_values[indexOf(attr_keys, 'sql_text')] AS sql_text,
		       argMax(value, time) AS elapsed,
		       argMax(toUInt64OrZero(attr_values[indexOf(attr_keys, 'executions')]), time) AS execs
		FROM metric_points
		WHERE time >= ? AND time <= ?
		  AND metric IN ('oracledb.top_sql.elapsed', 'oracledb_top_sql_elapsed', 'oracledb.top_sql_elapsed')
		  AND has(attr_keys, 'sql_text')
		` + oracleInstanceClause(withInstance) + `
		GROUP BY sql_text
		ORDER BY elapsed DESC
		LIMIT 10`
	args := []any{from, to}
	if withInstance {
		args = append(args, instance, instance)
	}
	rows, err := s.conn.Query(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []OracleSQL{}
	for rows.Next() {
		var sqlText string
		var elapsed float64
		var execs uint64
		if err := rows.Scan(&sqlText, &elapsed, &execs); err != nil {
			continue
		}
		avgMs := 0.0
		if execs > 0 {
			avgMs = (elapsed * 1000) / float64(execs)
		}
		out = append(out, OracleSQL{
			SQL:          sqlText,
			ElapsedSec:   elapsed,
			Executions:   execs,
			AvgElapsedMs: avgMs,
		})
	}
	return out, nil
}

func oracleSeed(s string) uint64 {
	h := fnv.New64a()
	h.Write([]byte(s))
	return h.Sum64()
}
