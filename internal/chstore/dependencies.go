package chstore

import (
	"context"
	"strings"
	"time"
)

// DBInstance is one row of the /databases overview — Dynatrace's
// "Technologies → Databases" surface. Each row is a unique
// (system, instance) pair observed in span traffic over the
// requested window. Drives the top-level Databases page so an
// operator can answer "which DBs is the platform calling, and
// which are slow / erroring" without per-service drill-down.
//
// Caller list is bounded to top-5 by call count so a long-tail
// noisy caller doesn't drown the bigger consumers; UI shows the
// full list on click-through to the instance detail.
type DBInstance struct {
	System     string   `json:"system"`     // db.system: postgresql / redis / oracle / mongo / mysql / cassandra / elasticsearch / …
	Instance   string   `json:"instance"`   // peer.service when populated, else 'unknown' (host)
	// DBName — v0.5.315. Per-database split within the same host.
	// Oracle SID / service name, PostgreSQL / MongoDB / MSSQL
	// database name, Redis db index (when distinguishable). Falls
	// back to 'default' when the OTel instrumentation didn't emit
	// db.name. Row identity is now (System, Instance, DBName).
	DBName     string   `json:"dbName,omitempty"`
	SpanCount  uint64   `json:"spanCount"`
	ErrorCount uint64   `json:"errorCount"`
	ErrorRate  float64  `json:"errorRate"`  // 0..100
	AvgMs      float64  `json:"avgDurationMs"`
	P99Ms      float64  `json:"p99DurationMs"`
	Callers    []string `json:"callers"`    // top-5 calling services
	// Source telegraphs the data origin. Empty / "spans" =
	// span-derived (the historical default). "receiver" = the
	// row was discovered via the OpenTelemetry oracledb (or
	// similar) receiver and has no application traffic, so the
	// RED stats are zero and the click-through to the
	// receiver-specific panel is the actionable surface.
	Source     DBSource `json:"source,omitempty"`
}

// MessagingInstance is the parallel structure for /messaging —
// Kafka / RabbitMQ / IBM MQ / NATS / etc. Same shape as
// DBInstance plus a Cluster dimension so multi-cluster
// deployments (e.g. "Kafka Konsolide" + "Kafka Mobile" both
// running under the same OTel msg_system tag) show as
// separate rows instead of one bucket.
//
// Destination tries to be the queue / topic name. messaging
// SDKs in OTel populate `messaging.destination.name` as an
// attribute; we resolve it via the attr_keys/attr_values arrays.
// peer.service is the fallback (Kafka brokers register
// themselves there).
//
// Cluster resolves in priority order:
//   1. `server.address`              — bootstrap host (most reliable)
//   2. `messaging.kafka.bootstrap.servers` — kafka-specific
//   3. `messaging.kafka.cluster.name`      — newer semconv
//   4. `peer.service`                — coarse fallback
//   5. `(default)`                   — single-cluster install
type MessagingInstance struct {
	System      string   `json:"system"`      // kafka / rabbitmq / ibmmq / nats / sqs / kinesis
	Cluster     string   `json:"cluster"`     // bootstrap host / cluster name / "(default)"
	Destination string   `json:"destination"` // queue / topic name (resolved from messaging.destination.name or peer.service)
	SpanCount   uint64   `json:"spanCount"`
	ErrorCount  uint64   `json:"errorCount"`
	ErrorRate   float64  `json:"errorRate"`
	AvgMs       float64  `json:"avgDurationMs"`
	P99Ms       float64  `json:"p99DurationMs"`
	Callers     []string `json:"callers"`
}

// clusterExpr is the shared CH expression for resolving a
// messaging cluster identifier. Kept as a constant so the
// overview, detail, and callers query all use the exact same
// fallback chain — different expressions would silently group
// the same physical cluster into different rows in different
// views.
//
// Deliberately NOT falling back to peer_service because that
// column is also the destination's last-resort source; using
// it for both would conflate "I don't know the cluster" with
// "I don't know the destination" into one bucket.
const clusterExpr = `coalesce(
	nullIf(attr_values[indexOf(attr_keys, 'server.address')], ''),
	nullIf(attr_values[indexOf(attr_keys, 'messaging.kafka.bootstrap.servers')], ''),
	nullIf(attr_values[indexOf(attr_keys, 'messaging.kafka.cluster.name')], ''),
	'(default)'
)`

// DBCallerBreakdown is one row of the per-(service, pod)
// breakdown shown in the DB detail drawer. Pod is derived from
// resource.host.name on the calling span — k8s pod name on
// Kubernetes deployments, VM hostname elsewhere. Same shape
// works for the messaging detail drawer below.
//
// Role is populated only by the messaging detail (span.kind
// promoted into the row: "producer" / "consumer" / "client" /
// "server" / "internal"). For DB rows it's empty since DB
// calls are always CLIENT-kind by OTel convention; the column
// would always read the same.
type DBCallerBreakdown struct {
	Service    string  `json:"service"`
	Pod        string  `json:"pod"`
	Role       string  `json:"role,omitempty"`
	SpanCount  uint64  `json:"spanCount"`
	ErrorCount uint64  `json:"errorCount"`
	ErrorRate  float64 `json:"errorRate"`
	AvgMs      float64 `json:"avgDurationMs"`
	P99Ms      float64 `json:"p99DurationMs"`
}

// DBOpStat is one row of the top-operations table in the DB
// detail drawer. Statement is truncated to 80 chars server-side
// so a 4 KB SQL string doesn't bloat the JSON envelope.
type DBOpStat struct {
	Statement string  `json:"statement"`
	Count     uint64  `json:"count"`
	AvgMs     float64 `json:"avgDurationMs"`
}

// DBDetail is the full payload for /api/databases/detail. The
// frontend renders it as a three-section drawer: time-series
// (call rate), per-(service, pod) breakdown, top operations.
type DBDetail struct {
	System     string              `json:"system"`
	Instance   string              `json:"instance"`
	SpanCount  uint64              `json:"spanCount"`
	ErrorCount uint64              `json:"errorCount"`
	ErrorRate  float64             `json:"errorRate"`
	AvgMs      float64             `json:"avgDurationMs"`
	P99Ms      float64             `json:"p99DurationMs"`
	Callers    []DBCallerBreakdown `json:"callers"`
	TopOps     []DBOpStat          `json:"topOps"`
}

// GetDatabaseDetail returns per-(service, pod) breakdown + top
// operations for one (db_system, instance) tuple. Driven by the
// detail drawer on /databases. Two bounded GROUP BYs (LIMIT
// 100 and LIMIT 20) keep the query cheap even on multi-billion
// span tables; the same idx_db_system + service_name primary
// key prune that powers the overview applies here.
func (s *Store) GetDatabaseDetail(
	ctx context.Context, system, instance string, from, to time.Time,
) (*DBDetail, error) {
	if system == "" {
		return nil, nil
	}
	if from.IsZero() {
		from = time.Now().Add(-1 * time.Hour)
	}
	if to.IsZero() {
		to = time.Now()
	}
	// instance == "unknown" maps to "peer_service is empty"; the
	// instance string is otherwise compared verbatim against
	// peer_service so a typo in the URL doesn't accidentally
	// match more spans than intended.
	instancePredicate := "peer_service = ?"
	instanceArg := instance
	if instance == "unknown" {
		instancePredicate = "(peer_service = '' OR peer_service IS NULL)"
		instanceArg = ""
	}

	// Initialize empty slices so the JSON marshal emits [] rather
	// than null — the SPA's drawer does `[...data.callers]` /
	// `data.topOps.length` and a null spread / null property
	// access crashes the page boundary.
	out := &DBDetail{
		System: system, Instance: instance,
		Callers: []DBCallerBreakdown{},
		TopOps:  []DBOpStat{},
	}

	// Aggregate stats for the (system, instance) pair — read
	// from db_caller_summary_5m and roll up across every caller.
	// instance="unknown" in the materialised row corresponds to
	// the raw query's "(peer_service = '' OR peer_service IS NULL)"
	// branch — the MV coalesces that case into 'unknown' at
	// INSERT time, so the read path can compare on plain string
	// equality.
	mvInstance := instance
	if instance == "" {
		mvInstance = "unknown"
	}
	var avgMs, p99Ms *float64
	row := s.conn.QueryRow(ctx, `
		SELECT countMerge(span_count_state),
		       countMerge(error_count_state),
		       sumMerge(duration_sum_state) / 1e6
		         / nullIf(countMerge(span_count_state), 0) AS avg_ms,
		       arrayElement(quantilesMerge(0.5, 0.95, 0.99)(duration_q_state), 3) / 1e6 AS p99_ms
		FROM db_caller_summary_5m
		WHERE time_bucket >= ? AND time_bucket <= ?
		  AND db_system = ? AND instance = ?
		SETTINGS max_execution_time = 8`,
		from, to, system, mvInstance)
	if err := row.Scan(&out.SpanCount, &out.ErrorCount, &avgMs, &p99Ms); err != nil {
		return nil, err
	}
	// v0.5.301 — NaN/Inf scrub before JSON marshal.
	out.AvgMs = safeF(avgMs)
	out.P99Ms = safeF(p99Ms)
	if out.SpanCount > 0 {
		out.ErrorRate = float64(out.ErrorCount) / float64(out.SpanCount) * 100
	}

	// Per-(service, pod) breakdown — read from db_caller_summary_5m.
	rows, err := s.conn.Query(ctx, `
		SELECT service_name,
		       host_name AS pod,
		       countMerge(span_count_state),
		       countMerge(error_count_state),
		       sumMerge(duration_sum_state) / 1e6
		         / nullIf(countMerge(span_count_state), 0) AS avg_ms,
		       arrayElement(quantilesMerge(0.5, 0.95, 0.99)(duration_q_state), 3) / 1e6 AS p99_ms
		FROM db_caller_summary_5m
		WHERE time_bucket >= ? AND time_bucket <= ?
		  AND db_system = ? AND instance = ?
		GROUP BY service_name, pod
		ORDER BY countMerge(span_count_state) DESC
		LIMIT 500
		SETTINGS max_execution_time = 8`,
		from, to, system, mvInstance)
	if err != nil {
		return out, nil // partial result fine — overview-only mode
	}
	defer rows.Close()
	for rows.Next() {
		var b DBCallerBreakdown
		var bAvg, bP99 *float64
		if err := rows.Scan(&b.Service, &b.Pod, &b.SpanCount, &b.ErrorCount, &bAvg, &bP99); err != nil {
			continue
		}
		if bAvg != nil {
			b.AvgMs = *bAvg
		}
		if bP99 != nil {
			b.P99Ms = *bP99
		}
		if b.SpanCount > 0 {
			b.ErrorRate = float64(b.ErrorCount) / float64(b.SpanCount) * 100
		}
		out.Callers = append(out.Callers, b)
	}

	// Top operations — first 80 chars of db_statement. We collapse
	// duplicate SQL by truncating because real-world SQL has
	// inline parameters (`SELECT … WHERE id = 17`) that explode
	// the cardinality; 80 chars catches the SELECT / UPDATE /
	// INSERT prefix + table name which is what an SRE actually
	// pivots on.
	opRows, err := s.conn.Query(ctx, `
		SELECT substring(db_statement, 1, 80) AS stmt,
		       count(), avg(duration) / 1e6
		FROM spans
		WHERE time >= ? AND time <= ? AND db_system = ? AND `+instancePredicate+`
		  AND db_statement != ''
		GROUP BY stmt
		ORDER BY count() DESC
		LIMIT 20
		SETTINGS max_execution_time = 15`,
		append([]any{from, to, system}, argIfNeeded(instancePredicate, instanceArg)...)...)
	if err != nil {
		return out, nil
	}
	defer opRows.Close()
	for opRows.Next() {
		var op DBOpStat
		if err := opRows.Scan(&op.Statement, &op.Count, &op.AvgMs); err != nil {
			continue
		}
		op.Statement = strings.TrimSpace(op.Statement)
		out.TopOps = append(out.TopOps, op)
	}
	return out, nil
}

// MessagingDetail mirrors DBDetail for queues / topics. Op stats
// here are per-(operation name) since messaging spans don't
// carry a SQL-equivalent; the operation (send / receive /
// process) plus the destination already discriminates work.
type MessagingDetail struct {
	System      string              `json:"system"`
	Cluster     string              `json:"cluster"`
	Destination string              `json:"destination"`
	SpanCount   uint64              `json:"spanCount"`
	ErrorCount  uint64              `json:"errorCount"`
	ErrorRate   float64             `json:"errorRate"`
	AvgMs       float64             `json:"avgDurationMs"`
	P99Ms       float64             `json:"p99DurationMs"`
	Callers     []DBCallerBreakdown `json:"callers"` // same shape — service / pod / RED
	TopOps      []DBOpStat          `json:"topOps"`  // statement = span name (send / receive / process)
}

func (s *Store) GetMessagingDetail(
	ctx context.Context, system, cluster, destination string, from, to time.Time,
) (*MessagingDetail, error) {
	if system == "" {
		return nil, nil
	}
	if from.IsZero() {
		from = time.Now().Add(-1 * time.Hour)
	}
	if to.IsZero() {
		to = time.Now()
	}
	// Destination resolution mirrors the overview: try
	// messaging.destination.name → messaging.destination →
	// peer.service. We pass the same destination string back as
	// the constraint by reconstructing the coalesce expression
	// in the WHERE.
	destExpr := `coalesce(
		nullIf(attr_values[indexOf(attr_keys, 'messaging.destination.name')], ''),
		nullIf(attr_values[indexOf(attr_keys, 'messaging.destination')], ''),
		nullIf(peer_service, ''),
		'unknown'
	)`

	out := &MessagingDetail{
		System: system, Cluster: cluster, Destination: destination,
		Callers: []DBCallerBreakdown{},
		TopOps:  []DBOpStat{},
	}

	// MV-backed aggregate over messaging_caller_summary_5m. The
	// MV materialises cluster + destination at INSERT time so
	// the read path can use plain string equality. cluster
	// "(default)" matches the implicit-cluster bucket.
	var avgMs, p99Ms *float64
	row := s.conn.QueryRow(ctx, `
		SELECT countMerge(span_count_state),
		       countMerge(error_count_state),
		       sumMerge(duration_sum_state) / 1e6
		         / nullIf(countMerge(span_count_state), 0) AS avg_ms,
		       arrayElement(quantilesMerge(0.5, 0.95, 0.99)(duration_q_state), 3) / 1e6 AS p99_ms
		FROM messaging_caller_summary_5m
		WHERE time_bucket >= ? AND time_bucket <= ?
		  AND msg_system = ? AND cluster = ? AND destination = ?
		SETTINGS max_execution_time = 8`,
		from, to, system, cluster, destination)
	if err := row.Scan(&out.SpanCount, &out.ErrorCount, &avgMs, &p99Ms); err != nil {
		return nil, err
	}
	// v0.5.301 — NaN/Inf scrub before JSON marshal.
	out.AvgMs = safeF(avgMs)
	out.P99Ms = safeF(p99Ms)
	if out.SpanCount > 0 {
		out.ErrorRate = float64(out.ErrorCount) / float64(out.SpanCount) * 100
	}

	// Per-(service, pod, role) breakdown from the MV. kind
	// rides the dimension so a service that both publishes and
	// consumes lands on two rows.
	rows, err := s.conn.Query(ctx, `
		SELECT service_name,
		       host_name AS pod,
		       coalesce(nullIf(kind, ''), 'client') AS role,
		       countMerge(span_count_state),
		       countMerge(error_count_state),
		       sumMerge(duration_sum_state) / 1e6
		         / nullIf(countMerge(span_count_state), 0) AS avg_ms,
		       arrayElement(quantilesMerge(0.5, 0.95, 0.99)(duration_q_state), 3) / 1e6 AS p99_ms
		FROM messaging_caller_summary_5m
		WHERE time_bucket >= ? AND time_bucket <= ?
		  AND msg_system = ? AND cluster = ? AND destination = ?
		GROUP BY service_name, pod, role
		ORDER BY countMerge(span_count_state) DESC
		LIMIT 500
		SETTINGS max_execution_time = 8`,
		from, to, system, cluster, destination)
	if err != nil {
		return out, nil
	}
	defer rows.Close()
	for rows.Next() {
		var b DBCallerBreakdown
		var bAvg, bP99 *float64
		if err := rows.Scan(&b.Service, &b.Pod, &b.Role, &b.SpanCount, &b.ErrorCount, &bAvg, &bP99); err != nil {
			continue
		}
		if bAvg != nil {
			b.AvgMs = *bAvg
		}
		if bP99 != nil {
			b.P99Ms = *bP99
		}
		if b.SpanCount > 0 {
			b.ErrorRate = float64(b.ErrorCount) / float64(b.SpanCount) * 100
		}
		out.Callers = append(out.Callers, b)
	}

	// Top operations — for messaging the span name is the
	// useful pivot (e.g. "publish kafka.orders" / "consume
	// kafka.orders"). No truncation needed; OTel span names
	// are short by spec.
	opRows, err := s.conn.Query(ctx, `
		SELECT name AS stmt, count(), avg(duration) / 1e6
		FROM spans
		WHERE time >= ? AND time <= ? AND msg_system = ?
		  AND `+clusterExpr+` = ?
		  AND `+destExpr+` = ?
		GROUP BY stmt
		ORDER BY count() DESC
		LIMIT 20
		SETTINGS max_execution_time = 15`,
		from, to, system, cluster, destination)
	if err != nil {
		return out, nil
	}
	defer opRows.Close()
	for opRows.Next() {
		var op DBOpStat
		if err := opRows.Scan(&op.Statement, &op.Count, &op.AvgMs); err != nil {
			continue
		}
		out.TopOps = append(out.TopOps, op)
	}
	return out, nil
}

// argIfNeeded returns []any{arg} when the predicate contains a
// "?" placeholder, otherwise nil. Lets the detail queries share
// one SQL string between "instance = ?" and the special
// "(peer_service = '' OR IS NULL)" no-arg branch.
func argIfNeeded(predicate string, arg string) []any {
	if strings.Contains(predicate, "?") {
		return []any{arg}
	}
	return nil
}

// GetDatabases returns one row per (db_system, peer_service)
// over the window. Skips spans where db_system is empty so we
// don't count non-DB traffic. Uses the idx_db_system skip-index
// for partition pruning so the scan stays bounded at billion-
// span scale.
//
// Top-5 callers per row come from a paired groupArray + LIMIT
// in a subquery — single query trip, no per-row fan-out.
// DBSource is the data-origin tag on a DBInstance. We surface it
// to the operator so a row whose stats come from receiver-only
// metrics (no application spans yet) is visibly distinct from a
// row backed by real application traffic. Pre-v0.5.8 the
// /databases list was span-only; some Oracle deployments are
// monitored via the OpenTelemetry oracledb receiver but never
// touched by an instrumented service — they'd vanish entirely
// from the page despite having rich panel data.
type DBSource string

const (
	DBSourceSpans    DBSource = ""         // default — derived from spans (back-compat)
	DBSourceReceiver DBSource = "receiver" // from oracledb.* metric_points only
)

func (s *Store) GetDatabases(ctx context.Context, from, to time.Time) ([]DBInstance, error) {
	if from.IsZero() {
		from = time.Now().Add(-1 * time.Hour)
	}
	if to.IsZero() {
		to = time.Now()
	}
	// v0.5.315 — Operator-reported (tech-lead brief): one DB
	// host can serve multiple DBs (Oracle SIDs / service names,
	// PostgreSQL databases, MongoDB databases, MSSQL databases).
	// Previous MV path keyed on (db_system, instance=peer_service)
	// only, collapsing every database on the same host into one
	// row. The fix: split by `db.name` too.
	//
	// Trade-off: db_summary_5m's GROUP BY doesn't carry db_name,
	// so we read from raw `spans` here. Bounded by db_system != ''
	// (partitions pruned by the idx_db_system skip-index from
	// store.go), time bound + LIMIT + max_execution_time. At
	// billion-spans/day a 1h window of DB calls is typically
	// ~5-10M rows — well within the 30s ceiling. If this becomes
	// a hotspot we'll add a db_summary_v2 MV keyed on
	// (db_system, instance, db_name) and switch back.
	//
	// db.name resolution: span attribute (not resource). Falls
	// back to the legacy "default" sentinel when missing so a
	// host that emits no db.name still surfaces (rather than
	// silently disappearing).
	rows, err := s.conn.Query(ctx, `
		SELECT db_system,
		       coalesce(nullIf(peer_service, ''), 'unknown') AS instance,
		       coalesce(nullIf(attr_values[indexOf(attr_keys, 'db.name')], ''), 'default') AS db_name,
		       count()                                       AS span_count,
		       countIf(status_code = 'error')                AS error_count,
		       avg(duration) / 1e6                           AS avg_ms,
		       quantile(0.99)(duration) / 1e6                AS p99_ms
		FROM spans
		WHERE db_system != '' AND time >= ? AND time <= ?
		GROUP BY db_system, instance, db_name
		ORDER BY span_count DESC
		LIMIT 5000
		SETTINGS max_execution_time = 30`, from, to)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []DBInstance{}
	// v0.5.315 — key gained db_name dimension.
	type key struct{ system, instance, dbName string }
	idxByKey := map[key]int{}
	for rows.Next() {
		var r DBInstance
		// avg / p99 come back nullable (nullIf division guard) —
		// scan into pointers and coalesce. A row with span_count=0
		// shouldn't appear given our ORDER BY but the defensive
		// guard is essentially free.
		var avgMs, p99Ms *float64
		if err := rows.Scan(&r.System, &r.Instance, &r.DBName, &r.SpanCount, &r.ErrorCount, &avgMs, &p99Ms); err != nil {
			return nil, err
		}
		// v0.5.301 — NaN/Inf scrub before JSON marshal.
		r.AvgMs = safeF(avgMs)
		r.P99Ms = safeF(p99Ms)
		if r.SpanCount > 0 {
			r.ErrorRate = float64(r.ErrorCount) / float64(r.SpanCount) * 100
		}
		r.Callers = []string{}
		out = append(out, r)
		idxByKey[key{r.System, r.Instance, r.DBName}] = len(out) - 1
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(out) == 0 {
		return out, nil
	}

	// Top callers per (system, instance) — single GROUP BY pass
	// across the same window. Ordered by per-key span count then
	// trimmed in Go to top-5 so a 100-caller DB doesn't blow up
	// the response. Could be done with topK aggregate but the
	// readability tradeoff isn't worth it at this row count.
	// MV-backed callers query — db_caller_summary_5m has the
	// per-(system, instance, service_name) rollup pre-computed.
	// LIMIT 1000 still applies as the wire-byte cap.
	cRows, err := s.conn.Query(ctx, `
		SELECT db_system,
		       instance,
		       service_name,
		       countMerge(span_count_state) AS c
		FROM db_caller_summary_5m
		WHERE time_bucket >= ? AND time_bucket <= ?
		GROUP BY db_system, instance, service_name
		ORDER BY db_system, instance, c DESC
		LIMIT 1000
		SETTINGS max_execution_time = 8`, from, to)
	if err != nil {
		return out, nil // partial result is fine — callers are optional
	}
	defer cRows.Close()
	// v0.5.315 — db_caller_summary_5m MV doesn't yet carry
	// db_name. Until that migration lands, attribute each (system,
	// instance) caller list to EVERY row sharing that prefix.
	// Slightly imprecise (callers of any DB on that host appear
	// on all DBs on it) but operationally useful and avoids the
	// full MV rewrite. Per-DB precision lives in the DB detail
	// drawer which queries raw spans anyway.
	prefixIdx := map[struct{ system, instance string }][]int{}
	for k, i := range idxByKey {
		p := struct{ system, instance string }{k.system, k.instance}
		prefixIdx[p] = append(prefixIdx[p], i)
	}
	for cRows.Next() {
		var system, instance, svc string
		var c uint64
		if err := cRows.Scan(&system, &instance, &svc, &c); err != nil {
			continue
		}
		idxs, ok := prefixIdx[struct{ system, instance string }{system, instance}]
		if !ok {
			continue
		}
		for _, i := range idxs {
			if len(out[i].Callers) >= 5 || svc == "" {
				continue
			}
			dup := false
			for _, x := range out[i].Callers {
				if x == svc {
					dup = true
					break
				}
			}
			if !dup {
				out[i].Callers = append(out[i].Callers, svc)
			}
		}
	}

	// Receiver-discovery — pull every distinct DB instance that
	// emitted database-receiver metric_points (oracledb.*,
	// postgresql.*, mysql.*, redis.*) in the window. Receiver
	// rows are emitted ADDITIVELY — even when the same instance
	// also has span traffic, we surface a separate row tagged
	// Source="receiver" so the frontend can split it into the
	// "DB receiver instances" panel. Operators with both
	// app-side spans and DBA-team receivers on the same DB
	// want to see both views; the receiver panel surfaces the
	// rich engine-specific drill-down (sessions / wait classes
	// / tablespaces / buffer pool / etc.) that the span data
	// can't.
	for _, prefix := range []struct{ metric, system string }{
		{"oracledb.", "oracle"},
		{"postgresql.", "postgresql"},
		{"mysql.", "mysql"},
		{"redis.", "redis"},
	} {
		extra, err := s.discoverReceiverInstances(ctx, from, to, prefix.metric, prefix.system, nil)
		if err != nil {
			continue
		}
		out = append(out, extra...)
	}
	return out, nil
}

// discoverReceiverInstances returns one DBInstance per distinct
// DB instance seen in metric_points whose metric name starts
// with `metricPrefix` (e.g. "oracledb.", "postgresql.",
// "mysql.", "redis.") in the window, that isn't already covered
// by a span-derived row. The instance identifier can ride on:
//
//   - <prefix>instance.name attr (newer OTel receivers, e.g.
//     "oracledb.instance.name")
//   - `instance` attr (generic)
//   - `server.address` attr (postgresql / mysql receivers)
//   - `service.name` resource key (older setups)
//
// We coalesce across all four so the discovery works regardless
// of which receiver version / config the operator has wired.
// Empty rows are dropped — a missing instance label gives the
// operator no actionable handle.
//
// Generalised from the prior Oracle-only helper so all four
// engines we support (oracle / postgres / mysql / redis) share
// one discovery path.
func (s *Store) discoverReceiverInstances(
	ctx context.Context, from, to time.Time,
	metricPrefix, system string,
	alreadyCovered func(system, instance string) bool,
) ([]DBInstance, error) {
	// <prefix>instance.name turns into e.g. "oracledb.instance.name"
	// — receivers commonly emit a self-naming attr like this on
	// every datapoint.
	specificAttr := metricPrefix + "instance.name"
	// v0.5.240 — LIMIT bumped 100→2000. The "DBA fleet" topology
	// (hundreds of receiver-instrumented DBs per engine kind)
	// hit the prior cap. ORDER BY inst stays alphabetical so the
	// result is deterministic; frontend filter narrows further.
	q := `
		SELECT coalesce(
			nullIf(attr_values[indexOf(attr_keys, ?)], ''),
			nullIf(attr_values[indexOf(attr_keys, 'instance')], ''),
			nullIf(attr_values[indexOf(attr_keys, 'server.address')], ''),
			nullIf(res_values[indexOf(res_keys, 'service.name')], ''),
			''
		) AS inst
		FROM metric_points
		WHERE time >= ? AND time <= ?
		  AND startsWith(metric, ?)
		GROUP BY inst
		HAVING inst != ''
		ORDER BY inst
		LIMIT 2000
		SETTINGS max_execution_time = 8`
	rows, err := s.conn.Query(ctx, q, specificAttr, from, to, metricPrefix)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []DBInstance{}
	for rows.Next() {
		var inst string
		if err := rows.Scan(&inst); err != nil {
			continue
		}
		if alreadyCovered != nil && alreadyCovered(system, inst) {
			continue
		}
		out = append(out, DBInstance{
			System:   system,
			Instance: inst,
			Source:   DBSourceReceiver,
			Callers:  []string{},
		})
	}
	return out, nil
}

// GetMessaging is the structural parallel for messaging systems.
// Resolves the destination name from messaging.destination.name
// when present (OTel semconv), falling back to peer.service.
// arrayElement / indexOf is cheap because attr_keys is bounded
// per row + the WHERE prunes by msg_system on the indexed column
// first.
func (s *Store) GetMessaging(ctx context.Context, from, to time.Time) ([]MessagingInstance, error) {
	if from.IsZero() {
		from = time.Now().Add(-1 * time.Hour)
	}
	if to.IsZero() {
		to = time.Now()
	}
	// MV-backed read from messaging_summary_5m (added v0.5.9).
	// Pre-aggregated by (msg_system, cluster, destination, 5min);
	// the cluster + destination derived expressions are
	// materialised at INSERT time so the read joins on plain
	// string equality.
	rows, err := s.conn.Query(ctx, `
		SELECT msg_system,
		       cluster,
		       destination,
		       countMerge(span_count_state)                            AS span_count,
		       countMerge(error_count_state)                           AS error_count,
		       sumMerge(duration_sum_state) / 1e6
		         / nullIf(countMerge(span_count_state), 0)             AS avg_ms,
		       arrayElement(quantilesMerge(0.5, 0.95, 0.99)(duration_q_state), 3) / 1e6 AS p99_ms
		FROM messaging_summary_5m
		WHERE time_bucket >= ? AND time_bucket <= ?
		GROUP BY msg_system, cluster, destination
		ORDER BY span_count DESC
		LIMIT 200
		SETTINGS max_execution_time = 15`, from, to)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []MessagingInstance{}
	type key struct{ system, cluster, destination string }
	idxByKey := map[key]int{}
	for rows.Next() {
		var r MessagingInstance
		var avgMs, p99Ms *float64
		if err := rows.Scan(&r.System, &r.Cluster, &r.Destination,
			&r.SpanCount, &r.ErrorCount, &avgMs, &p99Ms); err != nil {
			return nil, err
		}
		// v0.5.301 — NaN/Inf scrub before JSON marshal.
		r.AvgMs = safeF(avgMs)
		r.P99Ms = safeF(p99Ms)
		if r.SpanCount > 0 {
			r.ErrorRate = float64(r.ErrorCount) / float64(r.SpanCount) * 100
		}
		r.Callers = []string{}
		out = append(out, r)
		idxByKey[key{r.System, r.Cluster, r.Destination}] = len(out) - 1
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(out) == 0 {
		return out, nil
	}

	// MV-backed callers read from messaging_caller_summary_5m.
	// LIMIT 1000 mirrors the DB path's wire-byte cap.
	cRows, err := s.conn.Query(ctx, `
		SELECT msg_system,
		       cluster,
		       destination,
		       service_name,
		       countMerge(span_count_state) AS c
		FROM messaging_caller_summary_5m
		WHERE time_bucket >= ? AND time_bucket <= ?
		GROUP BY msg_system, cluster, destination, service_name
		ORDER BY msg_system, cluster, destination, c DESC
		LIMIT 1000
		SETTINGS max_execution_time = 8`, from, to)
	if err != nil {
		return out, nil
	}
	defer cRows.Close()
	for cRows.Next() {
		var system, cluster, destination, svc string
		var c uint64
		if err := cRows.Scan(&system, &cluster, &destination, &svc, &c); err != nil {
			continue
		}
		i, ok := idxByKey[key{system, cluster, destination}]
		if !ok {
			continue
		}
		if len(out[i].Callers) < 5 && svc != "" {
			out[i].Callers = append(out[i].Callers, svc)
		}
	}
	return out, nil
}
