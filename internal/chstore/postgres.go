package chstore

import (
	"context"
	"strings"
	"time"
)

// PostgresMetrics is the receiver-flavoured drill-down for one
// Postgres instance — what an operator sees when expanding a
// row whose db.system="postgresql" on /databases.
//
// Numbers come from the OpenTelemetry postgresql receiver,
// which scrapes pg_stat_database / pg_stat_replication /
// pg_locks and publishes them as postgresql.* metric_points.
// When no data is in flight the panel renders zeros + a DOWN
// status badge (same pattern as Oracle in v0.5.8 — no demo
// synthetic).
//
// The cumulative counters (commits / rollbacks / blks_read /
// blks_hit / deadlocks) get window-derived rates server-side;
// the operator reads "847 commits/sec" not the raw monotonic
// counter pg exposes.
type PostgresMetrics struct {
	Instance       string             `json:"instance"`
	Status         string             `json:"status"` // up / down
	WindowSeconds  float64            `json:"windowSeconds"`
	Backends       PGGaugeWithCap     `json:"backends"` // current connections + max_connections
	CommitsPS      float64            `json:"commitsPerSec"`
	RollbacksPS    float64            `json:"rollbacksPerSec"`
	DeadlocksPS    float64            `json:"deadlocksPerSec"`
	BlocksReadPS   float64            `json:"blocksReadPerSec"`
	BlocksHitPS    float64            `json:"blocksHitPerSec"`
	CacheHitPct    float64            `json:"cacheHitPct"` // derived: hit/(hit+read)
	TempFilesPS    float64            `json:"tempFilesPerSec"`
	TempBytesPS    float64            `json:"tempBytesPerSec"`
	WALAgeSec      float64            `json:"walAgeSec"`
	WALLagBytes    float64            `json:"walLagBytes"`
	ReplDelaySec   float64            `json:"replicationDelaySec"`
	BgwriterPS     PGBgwriter         `json:"bgwriter"`
	Databases      []PGDatabase       `json:"databases"`
	Locks          []PGLockEntry      `json:"locks"`
	// TopSQL — engine-authoritative heaviest statements from
	// pg_stat_statements (receiver-side parity with Oracle's
	// V$SQL TopSQL). Empty when the operator hasn't enabled the
	// pg_stat_statements scrape — the panel renders an empty
	// state, same no-fake-data policy as the rest of the panel.
	TopSQL         []DBTopSQL         `json:"topSQL"`
}

// PGGaugeWithCap is the (usage, limit) pair pattern shared with
// Oracle's panel. Frontend renders as a progress bar so the
// operator sees "47/100 connections" at a glance.
type PGGaugeWithCap struct {
	Usage float64 `json:"usage"`
	Limit float64 `json:"limit"`
}

// PGBgwriter is the background-writer slice that drives buffer
// allocation efficiency. Surfaces the three rates an operator
// cares about: allocated (new buffers), via_checkpoint
// (clean shutdown evictions), and via_bgwriter (proactive).
// A workload where via_backend climbs vs via_bgwriter says the
// bgwriter isn't keeping up — actionable signal.
type PGBgwriter struct {
	BuffersAllocatedPS  float64 `json:"buffersAllocatedPerSec"`
	BuffersCheckpointPS float64 `json:"buffersCheckpointPerSec"`
	BuffersBgwriterPS   float64 `json:"buffersBgwriterPerSec"`
	BuffersBackendPS    float64 `json:"buffersBackendPerSec"`
}

// PGDatabase is one row of the per-database breakdown — pg's
// equivalent of Oracle's tablespace table. Size + commit/
// rollback activity + live-row count let the operator spot
// the one DB that's both biggest and busiest.
type PGDatabase struct {
	Name       string  `json:"name"`
	SizeBytes  float64 `json:"sizeBytes"`
	CommitsPS  float64 `json:"commitsPerSec"`
	RollbackPS float64 `json:"rollbacksPerSec"`
	BackendCt  float64 `json:"backendCount"`
}

// PGLockEntry is per-lock-mode count: AccessShareLock /
// RowExclusiveLock / ShareLock / etc. The Oracle parallel is
// the wait-class breakdown — answers "where is contention
// concentrated".
type PGLockEntry struct {
	Mode  string  `json:"mode"`
	Count float64 `json:"count"`
}

// GetPostgresMetrics queries metric_points for postgresql.*
// instruments scoped to one instance. Falls back to a zero-
// filled response (status=down) when no data is in flight —
// matches the no-demo-data policy from v0.5.8.
func (s *Store) GetPostgresMetrics(
	ctx context.Context, instance string, from, to time.Time,
) (*PostgresMetrics, error) {
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
	out := &PostgresMetrics{
		Instance:      instance,
		Status:        "down",
		WindowSeconds: windowSec,
		Databases:     []PGDatabase{},
		Locks:         []PGLockEntry{},
		TopSQL:        []DBTopSQL{},
	}

	hasInstanceFilter := instance != "" && instance != "unknown"

	// Gauges — current values.
	if gauges, err := s.queryPgGauges(ctx, from, to, instance, hasInstanceFilter); err == nil && len(gauges) > 0 {
		out.Backends.Usage = gauges["postgresql.backends"]
		out.Backends.Limit = gauges["postgresql.connection.max"]
		out.WALAgeSec = gauges["postgresql.wal.age"]
		out.WALLagBytes = gauges["postgresql.wal.lag"]
		out.ReplDelaySec = gauges["postgresql.replication.data_delay"]
		out.Status = "up"
	}

	// Counters → rates.
	if rates, err := s.queryPgRates(ctx, from, to, instance, hasInstanceFilter, windowSec); err == nil {
		out.CommitsPS = rates["postgresql.commits"]
		out.RollbacksPS = rates["postgresql.rollbacks"]
		out.DeadlocksPS = rates["postgresql.deadlocks"]
		out.BlocksReadPS = rates["postgresql.blocks_read"]
		out.BlocksHitPS = rates["postgresql.blocks_hit"]
		out.TempFilesPS = rates["postgresql.temp_files"]
		out.TempBytesPS = rates["postgresql.temp_bytes"]
		out.BgwriterPS.BuffersAllocatedPS = rates["postgresql.bgwriter.buffers.allocated"]
		out.BgwriterPS.BuffersCheckpointPS = rates["postgresql.bgwriter.buffers.writes"]
		out.BgwriterPS.BuffersBgwriterPS = rates["postgresql.bgwriter.checkpoint.count"]
	}

	// Derived cache hit %.
	total := out.BlocksReadPS + out.BlocksHitPS
	if total > 0 {
		out.CacheHitPct = (out.BlocksHitPS / total) * 100
	}

	// Per-database breakdown.
	if dbs, err := s.queryPgDatabases(ctx, from, to, instance, hasInstanceFilter, windowSec); err == nil {
		out.Databases = dbs
	}

	// Lock breakdown by mode.
	if locks, err := s.queryPgLocks(ctx, from, to, instance, hasInstanceFilter); err == nil {
		out.Locks = locks
	}

	// Top SQL by total exec time — pg_stat_statements parity with
	// Oracle's V$SQL view. Empty when the operator hasn't wired
	// the statement scrape (the common case); the panel renders
	// an empty state. Read source is metric_points (see
	// db_topsql.go), never raw spans.
	if top, err := s.GetPostgresTopSQL(ctx, instance, from, to); err == nil && len(top) > 0 {
		out.TopSQL = top
	}

	return out, nil
}

func (s *Store) queryPgGauges(
	ctx context.Context, from, to time.Time, instance string, withInstance bool,
) (map[string]float64, error) {
	q := `
		SELECT metric, argMax(value, time) AS v
		FROM metric_points
		WHERE time >= ? AND time <= ?
		  AND startsWith(metric, 'postgresql.')
		` + pgInstanceClause(withInstance) + `
		GROUP BY metric`
	args := []any{from, to}
	if withInstance {
		args = append(args, instance, instance)
	}
	return scanMetricMap(ctx, s, q, args)
}

func (s *Store) queryPgRates(
	ctx context.Context, from, to time.Time, instance string, withInstance bool, windowSec float64,
) (map[string]float64, error) {
	q := `
		SELECT metric, (max(value) - min(value)) / ? AS rate
		FROM metric_points
		WHERE time >= ? AND time <= ?
		  AND startsWith(metric, 'postgresql.')
		` + pgInstanceClause(withInstance) + `
		GROUP BY metric`
	args := []any{windowSec, from, to}
	if withInstance {
		args = append(args, instance, instance)
	}
	return scanMetricMap(ctx, s, q, args)
}

func (s *Store) queryPgDatabases(
	ctx context.Context, from, to time.Time, instance string, withInstance bool, windowSec float64,
) ([]PGDatabase, error) {
	// pg receiver attaches the database name as either
	// `database` or `postgresql.database.name`. We coalesce
	// across both. Three rolled-up metrics per row.
	q := `
		WITH dbattr AS (
			SELECT coalesce(
				nullIf(attr_values[indexOf(attr_keys, 'postgresql.database.name')], ''),
				nullIf(attr_values[indexOf(attr_keys, 'database')], ''),
				''
			) AS db,
			metric, value, time
			FROM metric_points
			WHERE time >= ? AND time <= ?
			  AND startsWith(metric, 'postgresql.')
			  AND has(attr_keys, 'database') OR has(attr_keys, 'postgresql.database.name')
			` + pgInstanceClause(withInstance) + `
		)
		SELECT db,
		       argMaxIf(value, time, metric = 'postgresql.database.size') AS size_bytes,
		       (maxIf(value, metric = 'postgresql.commits') - minIf(value, metric = 'postgresql.commits')) / ? AS commits_ps,
		       (maxIf(value, metric = 'postgresql.rollbacks') - minIf(value, metric = 'postgresql.rollbacks')) / ? AS rb_ps,
		       argMaxIf(value, time, metric = 'postgresql.backends') AS backends
		FROM dbattr
		WHERE db != ''
		GROUP BY db
		ORDER BY size_bytes DESC
		LIMIT 50
		SETTINGS max_execution_time = 8`
	args := []any{from, to}
	if withInstance {
		args = append(args, instance, instance)
	}
	args = append(args, windowSec, windowSec)
	rows, err := s.conn.Query(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []PGDatabase{}
	for rows.Next() {
		var db PGDatabase
		if err := rows.Scan(&db.Name, &db.SizeBytes, &db.CommitsPS, &db.RollbackPS, &db.BackendCt); err != nil {
			continue
		}
		if db.CommitsPS < 0 {
			db.CommitsPS = 0
		}
		if db.RollbackPS < 0 {
			db.RollbackPS = 0
		}
		out = append(out, db)
	}
	return out, nil
}

func (s *Store) queryPgLocks(
	ctx context.Context, from, to time.Time, instance string, withInstance bool,
) ([]PGLockEntry, error) {
	// postgresql.database.locks is dimensioned by lock_mode
	// (AccessShareLock / RowExclusiveLock / ExclusiveLock /
	// etc.). Latest reading per mode.
	q := `
		SELECT attr_values[indexOf(attr_keys, 'lock_type')] AS mode,
		       argMax(value, time) AS v
		FROM metric_points
		WHERE time >= ? AND time <= ?
		  AND metric IN ('postgresql.database.locks', 'postgresql.locks')
		  AND has(attr_keys, 'lock_type')
		` + pgInstanceClause(withInstance) + `
		GROUP BY mode
		HAVING mode != ''
		ORDER BY v DESC
		LIMIT 20`
	args := []any{from, to}
	if withInstance {
		args = append(args, instance, instance)
	}
	rows, err := s.conn.Query(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []PGLockEntry{}
	for rows.Next() {
		var e PGLockEntry
		if err := rows.Scan(&e.Mode, &e.Count); err != nil {
			continue
		}
		out = append(out, e)
	}
	return out, nil
}

func pgInstanceClause(withInstance bool) string {
	if !withInstance {
		return ""
	}
	return `AND (
		attr_values[indexOf(attr_keys, 'postgresql.instance.name')] = ?
		OR attr_values[indexOf(attr_keys, 'server.address')] = ?
	)`
}

// scanMetricMap is a shared helper used by all four engine
// drill-downs. Takes a 2-column SELECT (metric, value) and
// returns it as a Go map. Skips rows with non-finite values
// so an upstream receiver glitch can't poison the panel.
func scanMetricMap(ctx context.Context, s *Store, q string, args []any) (map[string]float64, error) {
	rows, err := s.conn.Query(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]float64{}
	for rows.Next() {
		var m string
		var v float64
		if err := rows.Scan(&m, &v); err != nil {
			continue
		}
		if v < 0 {
			v = 0 // counter reset → suppress
		}
		out[strings.TrimSpace(m)] = v
	}
	return out, nil
}
