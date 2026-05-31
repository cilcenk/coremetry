package chstore

import (
	"context"
	"time"
)

// MySQLMetrics is the receiver-flavoured drill-down for one
// MySQL instance — analogous to OracleMetrics / PostgresMetrics.
// Reads from OpenTelemetry mysql receiver metric_points
// (`mysql.*`).
//
// Operator-relevant signals:
//   - Threads (connected / running) + connections cap → load
//     ceiling proximity
//   - Buffer-pool usage % + pages_dirty → InnoDB memory health
//   - Slow-query rate → easy "is the DB hot?" signal
//   - Row-lock waits → contention indicator (MySQL's parallel
//     to Oracle row-lock waits)
//   - Handler ratios (read_first vs read_next vs read_rnd_next)
//     → index efficiency proxy
//   - Replica delay (seconds behind master) → replication health
type MySQLMetrics struct {
	Instance       string         `json:"instance"`
	Status         string         `json:"status"`
	WindowSeconds  float64        `json:"windowSeconds"`
	Threads        MySQLThreads   `json:"threads"`
	Connections    PGGaugeWithCap `json:"connections"`
	QuestionsPS    float64        `json:"questionsPerSec"`
	SlowQueriesPS  float64        `json:"slowQueriesPerSec"`
	RowLockWaitsPS float64        `json:"rowLockWaitsPerSec"`
	RowLockTimeSec float64        `json:"rowLockTimeSec"`
	TmpDiskPS      float64        `json:"tmpDiskTablesPerSec"`
	OpenedTblPS    float64        `json:"openedTablesPerSec"`
	BufferPool     MySQLBufferPool `json:"bufferPool"`
	HandlersPS     MySQLHandlers   `json:"handlers"`
	RowOpsPS       MySQLRowOps     `json:"rowOps"`
	ReplicaDelay   float64         `json:"replicaDelaySec"`
	// TopSQL — engine-authoritative heaviest statements from
	// performance_schema (events_statements_summary_by_digest),
	// receiver-side parity with Oracle's V$SQL TopSQL. Empty when
	// the operator hasn't enabled the performance_schema statement
	// scrape — panel renders an empty state.
	TopSQL         []DBTopSQL      `json:"topSQL"`
}

// MySQLThreads — running vs connected says how many of the
// connected client threads are actually working right now.
type MySQLThreads struct {
	Connected float64 `json:"connected"`
	Running   float64 `json:"running"`
	Created   float64 `json:"createdPerSec"` // thread cache hit rate proxy
}

// MySQLBufferPool — InnoDB buffer pool snapshot. Dirty/total
// is the "how dirty is the cache" signal; usage_pct shows
// whether the operator should grow innodb_buffer_pool_size.
type MySQLBufferPool struct {
	PagesData    float64 `json:"pagesData"`
	PagesDirty   float64 `json:"pagesDirty"`
	PagesFree    float64 `json:"pagesFree"`
	PagesTotal   float64 `json:"pagesTotal"`
	UsagePct     float64 `json:"usagePct"`
	DirtyPct     float64 `json:"dirtyPct"`
}

// MySQLHandlers — read_first / read_key are index-driven;
// read_rnd_next is full-table-scan-driven. A spike in
// read_rnd_next over read_key means new query plans turned
// sequential.
type MySQLHandlers struct {
	ReadFirstPS   float64 `json:"readFirstPerSec"`
	ReadKeyPS     float64 `json:"readKeyPerSec"`
	ReadNextPS    float64 `json:"readNextPerSec"`
	ReadRndNextPS float64 `json:"readRndNextPerSec"`
	WritePS       float64 `json:"writePerSec"`
}

// MySQLRowOps — insert/update/delete/select per sec. Same
// shape every other engine surfaces; lets the operator pivot
// between write-heavy and read-heavy DBs.
type MySQLRowOps struct {
	InsertPS float64 `json:"insertPerSec"`
	UpdatePS float64 `json:"updatePerSec"`
	DeletePS float64 `json:"deletePerSec"`
	SelectPS float64 `json:"selectPerSec"`
}

func (s *Store) GetMySQLMetrics(
	ctx context.Context, instance string, from, to time.Time,
) (*MySQLMetrics, error) {
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
	out := &MySQLMetrics{
		Instance: instance, Status: "down", WindowSeconds: windowSec,
		TopSQL: []DBTopSQL{},
	}
	hasInstanceFilter := instance != "" && instance != "unknown"

	if gauges, err := s.queryMysqlGauges(ctx, from, to, instance, hasInstanceFilter); err == nil && len(gauges) > 0 {
		out.Threads.Connected = gauges["mysql.threads"]
		out.Threads.Running = gauges["mysql.threads.running"]
		out.Connections.Usage = gauges["mysql.connection.count"]
		out.Connections.Limit = gauges["mysql.max_used_connections"]
		out.BufferPool.PagesData = gauges["mysql.buffer_pool.pages.data"]
		out.BufferPool.PagesDirty = gauges["mysql.buffer_pool.pages.dirty"]
		out.BufferPool.PagesFree = gauges["mysql.buffer_pool.pages.free"]
		out.BufferPool.PagesTotal = gauges["mysql.buffer_pool.pages.total"]
		out.ReplicaDelay = gauges["mysql.replica.time_behind_source"]
		if out.ReplicaDelay == 0 {
			out.ReplicaDelay = gauges["mysql.replica.sql_delay"]
		}
		out.Status = "up"
	}
	if rates, err := s.queryMysqlRates(ctx, from, to, instance, hasInstanceFilter, windowSec); err == nil {
		out.QuestionsPS = rates["mysql.questions"]
		out.SlowQueriesPS = rates["mysql.slow_queries"]
		out.RowLockWaitsPS = rates["mysql.row_locks"]
		if out.RowLockWaitsPS == 0 {
			out.RowLockWaitsPS = rates["mysql.innodb.row_lock.waits"]
		}
		out.TmpDiskPS = rates["mysql.tmp_resources.disk"]
		out.OpenedTblPS = rates["mysql.opened_resources.tables"]
		out.Threads.Created = rates["mysql.threads.created"]
		out.HandlersPS.ReadFirstPS = rates["mysql.handlers.read_first"]
		out.HandlersPS.ReadKeyPS = rates["mysql.handlers.read_key"]
		out.HandlersPS.ReadNextPS = rates["mysql.handlers.read_next"]
		out.HandlersPS.ReadRndNextPS = rates["mysql.handlers.read_rnd_next"]
		out.HandlersPS.WritePS = rates["mysql.handlers.write"]
		out.RowOpsPS.InsertPS = rates["mysql.row_operations.insert"]
		out.RowOpsPS.UpdatePS = rates["mysql.row_operations.update"]
		out.RowOpsPS.DeletePS = rates["mysql.row_operations.delete"]
		out.RowOpsPS.SelectPS = rates["mysql.row_operations.select"]
	}

	// Derived percentages on the buffer pool.
	if out.BufferPool.PagesTotal > 0 {
		used := out.BufferPool.PagesTotal - out.BufferPool.PagesFree
		out.BufferPool.UsagePct = (used / out.BufferPool.PagesTotal) * 100
		out.BufferPool.DirtyPct = (out.BufferPool.PagesDirty / out.BufferPool.PagesTotal) * 100
	}

	// Row-lock total time uses a cumulative counter; convert
	// to window total here.
	if t, err := s.queryMysqlRowLockTime(ctx, from, to, instance, hasInstanceFilter); err == nil {
		out.RowLockTimeSec = t
	}

	// Top SQL by total exec time — performance_schema digest
	// parity with Oracle's V$SQL view. Empty when the operator
	// hasn't wired the statement scrape (the common case); panel
	// renders an empty state. Read source is metric_points (see
	// db_topsql.go), never raw spans.
	if top, err := s.GetMySQLTopSQL(ctx, instance, from, to); err == nil && len(top) > 0 {
		out.TopSQL = top
	}

	return out, nil
}

func (s *Store) queryMysqlGauges(
	ctx context.Context, from, to time.Time, instance string, withInstance bool,
) (map[string]float64, error) {
	q := `
		SELECT metric, argMax(value, time) AS v
		FROM metric_points
		WHERE time >= ? AND time <= ?
		  AND startsWith(metric, 'mysql.')
		` + mysqlInstanceClause(withInstance) + `
		GROUP BY metric`
	args := []any{from, to}
	if withInstance {
		args = append(args, instance, instance)
	}
	return scanMetricMap(ctx, s, q, args)
}

func (s *Store) queryMysqlRates(
	ctx context.Context, from, to time.Time, instance string, withInstance bool, windowSec float64,
) (map[string]float64, error) {
	q := `
		SELECT metric, (max(value) - min(value)) / ? AS rate
		FROM metric_points
		WHERE time >= ? AND time <= ?
		  AND startsWith(metric, 'mysql.')
		` + mysqlInstanceClause(withInstance) + `
		GROUP BY metric`
	args := []any{windowSec, from, to}
	if withInstance {
		args = append(args, instance, instance)
	}
	return scanMetricMap(ctx, s, q, args)
}

func (s *Store) queryMysqlRowLockTime(
	ctx context.Context, from, to time.Time, instance string, withInstance bool,
) (float64, error) {
	q := `
		SELECT max(value) - min(value)
		FROM metric_points
		WHERE time >= ? AND time <= ?
		  AND metric IN ('mysql.innodb.row_lock.time', 'mysql.row_locks_time')
		` + mysqlInstanceClause(withInstance)
	args := []any{from, to}
	if withInstance {
		args = append(args, instance, instance)
	}
	row := s.conn.QueryRow(ctx, q, args...)
	var v float64
	if err := row.Scan(&v); err != nil {
		return 0, err
	}
	if v < 0 {
		v = 0
	}
	// receiver units are milliseconds → convert to seconds for
	// the human-readable tile.
	return v / 1000, nil
}

func mysqlInstanceClause(withInstance bool) string {
	if !withInstance {
		return ""
	}
	return `AND (
		attr_values[indexOf(attr_keys, 'mysql.instance.name')] = ?
		OR attr_values[indexOf(attr_keys, 'server.address')] = ?
	)`
}
