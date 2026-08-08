package chstore

import (
	"context"
	"strconv"
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
	System   string `json:"system"`   // db.system: postgresql / redis / oracle / mongo / mysql / cassandra / elasticsearch / …
	Instance string `json:"instance"` // peer.service when populated, else 'unknown' (host)
	// DBName — v0.5.315. Per-database split within the same host.
	// Oracle SID / service name, PostgreSQL / MongoDB / MSSQL
	// database name, Redis db index (when distinguishable). Falls
	// back to 'default' when the OTel instrumentation didn't emit
	// db.name. Row identity is now (System, Instance, DBName).
	DBName     string  `json:"dbName,omitempty"`
	SpanCount  uint64  `json:"spanCount"`
	ErrorCount uint64  `json:"errorCount"`
	ErrorRate  float64 `json:"errorRate"` // 0..100
	AvgMs      float64 `json:"avgDurationMs"`
	// v0.9.262 — P50/P95 read off the SAME db_summary_5m TDigest state that
	// already produced P99 (indices 1 and 2 of the 3-wide (0.5, 0.95, 0.99)
	// arg list). CH evaluates the identical quantilesTDigestMerge
	// subexpression once, so surfacing them costs no extra scan.
	//
	// Caveat: discoverReceiverInstances builds DBInstance rows from
	// metric_points, which carry no quantiles at all — those rows leave all
	// three at 0 and are tagged Source="receiver". The frontend badges them;
	// do not read a 0 here as "this database is fast".
	P50Ms float64 `json:"p50DurationMs"`
	P95Ms float64 `json:"p95DurationMs"`
	P99Ms float64 `json:"p99DurationMs"`
	// Prior* (v0.9.433) — ?compare=prior: bir-önceki eş-pencere
	// sayaçları (mergeDBPrior, api_databases.go). omitempty: yalnız
	// prior ikizi eşleşen satırlarda taşınır — sıfırlanmış prior sahte
	// NEW rozeti çizdirirdi (messaging v0.8.364 sözleşmesinin aynısı).
	PriorSpanCount  uint64   `json:"priorSpanCount,omitempty"`
	PriorErrorCount uint64   `json:"priorErrorCount,omitempty"`
	PriorAvgMs      float64  `json:"priorAvgMs,omitempty"`
	PriorP50Ms      float64  `json:"priorP50Ms,omitempty"`
	PriorP99Ms      float64  `json:"priorP99Ms,omitempty"`
	Callers         []string `json:"callers"` // top-5 calling services
	// Source telegraphs the data origin. Empty / "spans" =
	// span-derived (the historical default). "receiver" = the
	// row was discovered via the OpenTelemetry oracledb (or
	// similar) receiver and has no application traffic, so the
	// RED stats are zero and the click-through to the
	// receiver-specific panel is the actionable surface.
	Source DBSource `json:"source,omitempty"`
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
//  1. `server.address`              — bootstrap host (most reliable)
//  2. `messaging.kafka.bootstrap.servers` — kafka-specific
//  3. `messaging.kafka.cluster.name`      — newer semconv
//  4. `peer.service`                — coarse fallback
//  5. `(default)`                   — single-cluster install
type MessagingInstance struct {
	System      string  `json:"system"`      // kafka / rabbitmq / ibmmq / nats / sqs / kinesis
	Cluster     string  `json:"cluster"`     // bootstrap host / cluster name / "(default)"
	Destination string  `json:"destination"` // queue / topic name (resolved from messaging.destination.name or peer.service)
	SpanCount   uint64  `json:"spanCount"`
	ErrorCount  uint64  `json:"errorCount"`
	ErrorRate   float64 `json:"errorRate"`
	AvgMs       float64 `json:"avgDurationMs"`
	// v0.8.364 (Stage-2 M1) — full quantile grid. The MV state is
	// quantilesTDigestState(0.5, 0.95, 0.99); pre-M1 only element
	// 3 (p99) was projected out of the merge.
	P50Ms float64 `json:"p50DurationMs"`
	P95Ms float64 `json:"p95DurationMs"`
	P99Ms float64 `json:"p99DurationMs"`
	// v0.8.364 (Stage-2 M1) — producer/consumer split. Sourced from
	// messaging_caller_summary_5m (the kind dimension lives on that
	// MV; messaging_summary_5m collapses it). Raw counts, not
	// rates: the caller knows the window length, and equal-length
	// prior windows make count deltas identical to rate deltas.
	// Spans of other kinds (client/server/internal brokers chatter)
	// count toward SpanCount but neither split bucket.
	ProduceCount  uint64 `json:"produceCount"`
	ConsumeCount  uint64 `json:"consumeCount"`
	ProduceErrors uint64 `json:"produceErrors"`
	ConsumeErrors uint64 `json:"consumeErrors"`
	// v0.9.816 — GECİKME AYRIŞMASI. Satırın tek P95'i üretici ve tüketici
	// span'lerini TEK dağılımda topluyordu, oysa bunlar farklı işler:
	// publish (broker'a yazma, tipik olarak hızlı) ile process (mesajı
	// işleme, iş mantığının kendisi). Karışık p95, yavaş bir tüketiciyi
	// hızlı üreticinin içinde saklıyordu — "bu topic yavaş" deyip nerede
	// yavaş olduğunu söylemiyordu.
	//
	// Kaynak messaging_caller_summary_5m'in ZATEN taşıdığı duration_q_state
	// (kind boyutu o MV'de var). Ana MV'ye DOKUNULMADI ve ikinci bir tur
	// atılmadı: bu iki alan, kind ayrımını okuyan MEVCUT sorgunun aynı
	// merge'ünden projekte ediliyor. Ölçüm (24s pencere, query_log medyanı,
	// n=7): tarama satırı BİREBİR AYNI (67.470), süre 18ms → 27ms.
	//
	// omitempty: 0 ms bir ölçüm değil, ölçüm YOKLUĞUDUR — alan düşer ve
	// frontend '—' basar (rolling deploy'da eski payload da böyle davranır).
	ProduceP95Ms float64 `json:"produceP95Ms,omitempty"`
	ConsumeP95Ms float64 `json:"consumeP95Ms,omitempty"`
	// Prior* — same rollup over the immediately-preceding
	// equal-length window. Populated only when /api/messaging runs
	// with compare=prior (v0.8.364; the endpoints v0.5.404
	// pattern). omitempty keeps the default payload unchanged.
	PriorSpanCount    uint64   `json:"priorSpanCount,omitempty"`
	PriorErrorCount   uint64   `json:"priorErrorCount,omitempty"`
	PriorProduceCount uint64   `json:"priorProduceCount,omitempty"`
	PriorConsumeCount uint64   `json:"priorConsumeCount,omitempty"`
	PriorAvgMs        float64  `json:"priorAvgMs,omitempty"`
	PriorP50Ms        float64  `json:"priorP50Ms,omitempty"`
	PriorP99Ms        float64  `json:"priorP99Ms,omitempty"`
	Callers           []string `json:"callers"`
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

// dbInstanceExpr reproduces db_summary_5m's own `instance` identity on RAW
// spans, verbatim (store.go:2494-2502). Kept as a shared constant for the same
// reason clusterExpr above is: a raw query that spells this chain differently
// resolves the SAME physical database to a DIFFERENT name, and the two halves
// of one drawer then disagree.
//
// v0.9.274 — the Top-statements scan used to AND `peer_service = ?` here, one
// rung of the six. Any instance the MV named from a later rung matched zero
// spans, so the drawer showed a span count and callers (both MV-sourced, hence
// coalesced) beside an EMPTY Top statements table. Measured live: the
// clickhouse row's predicate matched 0 spans where the real identity matched
// 4659. Same defect as the dead row link fixed in v0.9.268, on the backend.
//
// The 'unknown' case needs no special branch: when every rung is empty the
// expression yields the literal 'unknown', which is exactly what the MV stores.
const dbInstanceExpr = `coalesce(
	nullIf(peer_service, ''),
	nullIf(attr_values[indexOf(attr_keys, 'server.address')], ''),
	nullIf(attr_values[indexOf(attr_keys, 'net.peer.name')], ''),
	nullIf(attr_values[indexOf(attr_keys, 'db.host')], ''),
	nullIf(attr_values[indexOf(attr_keys, 'db.name')], ''),
	nullIf(service_name, ''),
	'unknown'
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
	// v0.9.273 — P50 completes the grid. It was missed in v0.9.263: the detail
	// AGGREGATE above gained P50/P95 but this per-caller struct only gained
	// P95, so the drawer showed three percentiles at the top and two per row.
	// The data was free the whole time — same merge, index 1.
	P50Ms float64 `json:"p50DurationMs"`
	// v0.9.263 — P95 off the same 3-wide TDigest state (index 2).
	//
	// ⚠️ This struct is filled by TWO queries — the /databases caller
	// breakdown and the /messaging one — and P95Ms is a plain float64, not
	// a pointer. A path that fails to SELECT it marshals 0 and the drawer
	// prints "0.0ms": a plausible wrong number, not a visible blank. Both
	// queries must always project it; a third producer must too.
	P95Ms float64 `json:"p95DurationMs"`
	P99Ms float64 `json:"p99DurationMs"`
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
	System     string  `json:"system"`
	Instance   string  `json:"instance"`
	SpanCount  uint64  `json:"spanCount"`
	ErrorCount uint64  `json:"errorCount"`
	ErrorRate  float64 `json:"errorRate"`
	AvgMs      float64 `json:"avgDurationMs"`
	// v0.9.263 — same db_caller_summary_5m merge as P99, indices 1 and 2.
	P50Ms   float64             `json:"p50DurationMs"`
	P95Ms   float64             `json:"p95DurationMs"`
	P99Ms   float64             `json:"p99DurationMs"`
	Callers []DBCallerBreakdown `json:"callers"`
	TopOps  []DBOpStat          `json:"topOps"`
}

// GetDatabaseDetail returns per-(service, pod) breakdown + top
// operations for one (db_system, instance) tuple. Driven by the
// detail drawer on /databases. Two bounded GROUP BYs (LIMIT
// 100 and LIMIT 20) keep the query cheap even on multi-billion
// span tables; the same idx_db_system + service_name primary
// key prune that powers the overview applies here.
// distinctCallerServices returns the unique, non-empty service names from a
// database's caller breakdown — used to scope the top-statement scan to just
// those services so it can use the spans (service_name, time) primary key
// instead of a full-window scan that times out at billion-span scale (v0.7.35).
func distinctCallerServices(callers []DBCallerBreakdown) []string {
	seen := map[string]struct{}{}
	out := []string{}
	for _, c := range callers {
		if c.Service == "" {
			continue
		}
		if _, ok := seen[c.Service]; ok {
			continue
		}
		seen[c.Service] = struct{}{}
		out = append(out, c.Service)
	}
	return out
}

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
	// v0.9.274 — the raw scans below now resolve instance the SAME way the MV
	// does (dbInstanceExpr). The old two-branch form compared only peer_service
	// and needed a special "unknown" case; the coalesce collapses both, because
	// it yields the literal 'unknown' precisely when every rung is empty.
	instancePredicate := dbInstanceExpr + " = ?"
	instanceArg := instance

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
	// v0.9.274 — snap the window start DOWN to the MV's 5-minute grid, exactly
	// as GetDatabases does for the row this drawer was opened from. Without it
	// an unaligned `from` half-clips the first bucket and the drawer reads LOWER
	// than the row it is explaining — up to five minutes of traffic missing, on
	// the same screen, with no way to tell which number is right.
	bucketStart := from.Truncate(5 * time.Minute)

	mvInstance := instance
	if instance == "" {
		mvInstance = "unknown"
	}
	// Scan is POSITIONAL — pointer order must mirror the SELECT exactly.
	var avgMs, p50Ms, p95Ms, p99Ms *float64
	row := s.telemetryReadConn().QueryRow(ctx, `
		SELECT countMerge(span_count_state),
		       countMerge(error_count_state),
		       sumMerge(duration_sum_state) / 1e6
		         / nullIf(countMerge(span_count_state), 0) AS avg_ms,
		       arrayElement(quantilesTDigestMerge(0.5, 0.95, 0.99)(duration_q_state), 1) / 1e6 AS p50_ms,
		       arrayElement(quantilesTDigestMerge(0.5, 0.95, 0.99)(duration_q_state), 2) / 1e6 AS p95_ms,
		       arrayElement(quantilesTDigestMerge(0.5, 0.95, 0.99)(duration_q_state), 3) / 1e6 AS p99_ms
		FROM db_caller_summary_5m
		WHERE time_bucket >= ? AND time_bucket <= ?
		  AND db_system = ? AND instance = ?
		SETTINGS max_execution_time = 8`,
		bucketStart, to, system, mvInstance)
	if err := row.Scan(&out.SpanCount, &out.ErrorCount, &avgMs, &p50Ms, &p95Ms, &p99Ms); err != nil {
		return nil, err
	}
	// v0.5.301 — NaN/Inf scrub before JSON marshal.
	out.AvgMs = safeF(avgMs)
	out.P50Ms = safeF(p50Ms)
	out.P95Ms = safeF(p95Ms)
	out.P99Ms = safeF(p99Ms)
	if out.SpanCount > 0 {
		out.ErrorRate = float64(out.ErrorCount) / float64(out.SpanCount) * 100
	}

	// Per-(service, pod) breakdown — read from db_caller_summary_5m.
	rows, err := s.telemetryReadConn().Query(ctx, `
		SELECT service_name,
		       host_name AS pod,
		       countMerge(span_count_state),
		       countMerge(error_count_state),
		       sumMerge(duration_sum_state) / 1e6
		         / nullIf(countMerge(span_count_state), 0) AS avg_ms,
		       arrayElement(quantilesTDigestMerge(0.5, 0.95, 0.99)(duration_q_state), 1) / 1e6 AS p50_ms,
		       arrayElement(quantilesTDigestMerge(0.5, 0.95, 0.99)(duration_q_state), 2) / 1e6 AS p95_ms,
		       arrayElement(quantilesTDigestMerge(0.5, 0.95, 0.99)(duration_q_state), 3) / 1e6 AS p99_ms
		FROM db_caller_summary_5m
		WHERE time_bucket >= ? AND time_bucket <= ?
		  AND db_system = ? AND instance = ?
		GROUP BY service_name, pod
		ORDER BY countMerge(span_count_state) DESC
		LIMIT 500
		SETTINGS max_execution_time = 8`,
		bucketStart, to, system, mvInstance)
	if err != nil {
		return out, nil // partial result fine — overview-only mode
	}
	defer rows.Close()
	for rows.Next() {
		var b DBCallerBreakdown
		var bAvg, bP50, bP95, bP99 *float64
		if err := rows.Scan(&b.Service, &b.Pod, &b.SpanCount, &b.ErrorCount, &bAvg, &bP50, &bP95, &bP99); err != nil {
			continue
		}
		if bAvg != nil {
			b.AvgMs = *bAvg
		}
		if bP50 != nil {
			b.P50Ms = *bP50
		}
		if bP95 != nil {
			b.P95Ms = *bP95
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
	// v0.7.35 — scope the statement scan to the services that actually call
	// this database (known cheaply from the caller breakdown above, which reads
	// the db_caller_summary_5m MV). The spans primary key is (service_name,
	// time); WITHOUT a service_name predicate this scan can't prune and times
	// out at billion-span scale — operator-reported: "top statements blank at
	// 1000s of services / 100+ DBs" while fine locally. IN (literal list) keeps
	// the (service_name, time) prefix usable (no GLOBAL needed — it's a value
	// list, not a subquery). Empty list → unscoped fallback (nothing to show
	// anyway when the MV has no callers).
	callerSvcs := distinctCallerServices(out.Callers)
	stmtSQL := `
		SELECT substring(db_statement, 1, 80) AS stmt,
		       count(), avg(duration) / 1e6
		FROM spans
		WHERE time >= ? AND time <= ? AND db_system = ? AND ` + instancePredicate
	stmtArgs := append([]any{from, to, system}, argIfNeeded(instancePredicate, instanceArg)...)
	if len(callerSvcs) > 0 {
		stmtSQL += ` AND service_name IN (?)`
		stmtArgs = append(stmtArgs, callerSvcs)
	}
	stmtSQL += `
		  AND db_statement != ''
		GROUP BY stmt
		ORDER BY count() DESC
		LIMIT 20
		SETTINGS max_execution_time = 15`
	opRows, err := s.telemetryReadConn().Query(ctx, stmtSQL, stmtArgs...)
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
	System      string  `json:"system"`
	Cluster     string  `json:"cluster"`
	Destination string  `json:"destination"`
	SpanCount   uint64  `json:"spanCount"`
	ErrorCount  uint64  `json:"errorCount"`
	ErrorRate   float64 `json:"errorRate"`
	AvgMs       float64 `json:"avgDurationMs"`
	// v0.9.263 — same merge as P99, indices 1 and 2. No extra scan.
	P50Ms   float64             `json:"p50DurationMs"`
	P95Ms   float64             `json:"p95DurationMs"`
	P99Ms   float64             `json:"p99DurationMs"`
	Callers []DBCallerBreakdown `json:"callers"` // same shape — service / pod / RED
	TopOps  []DBOpStat          `json:"topOps"`  // statement = span name (send / receive / process)
	// Series — v0.8.364 (Stage-2 M1). Per-5-minute produce/consume
	// counts across the window, straight off
	// messaging_caller_summary_5m (kind + time_bucket are both
	// dimensions there, so the split series is one bounded merged-
	// state GROUP BY — no raw-spans read). Drives the drawer's
	// produce/consume sparklines.
	Series []MsgKindPoint `json:"series"`
	// E2E — v0.8.372 (Stage-2 M2). span_links-correlated end-to-end
	// produce→consume latency (messaging_e2e.go). Nil when the read
	// fails (drawer omits the section); non-nil with Linkless=true
	// when no links correlated in the window (honest empty state).
	E2E *MsgE2E `json:"e2e,omitempty"`
}

// MsgKindPoint is one 5-minute bucket of the messaging detail's
// produce/consume series (v0.8.364). TimeS is the bucket start in
// unix seconds; counts are spans in that bucket by span kind.
type MsgKindPoint struct {
	TimeS        int64  `json:"timeS"`
	ProduceCount uint64 `json:"produceCount"`
	ConsumeCount uint64 `json:"consumeCount"`
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

	// v0.9.813 — MV okumaları için hizalanmış pencere başlangıcı.
	// SADECE time_bucket sorgularında kullanılır: aşağıdaki TopOps ham
	// `spans`'i `time` ile, getMessagingE2E ise ham span_links'i okuyor —
	// onları geri kaydırmak taranan aralığı GERÇEKTEN genişletir ve
	// drawer'ın sayıları tablodan sapardı. Hizalama kova etiketlemesinin
	// düzeltmesidir, pencereyi büyütme lisansı değil.
	bucketStart := alignBucketStart(from)

	out := &MessagingDetail{
		System: system, Cluster: cluster, Destination: destination,
		Callers: []DBCallerBreakdown{},
		TopOps:  []DBOpStat{},
		Series:  []MsgKindPoint{},
	}

	// MV-backed aggregate over messaging_caller_summary_5m. The
	// MV materialises cluster + destination at INSERT time so
	// the read path can use plain string equality. cluster
	// "(default)" matches the implicit-cluster bucket.
	// Scan is POSITIONAL — pointer order must mirror the SELECT exactly.
	var avgMs, p50Ms, p95Ms, p99Ms *float64
	row := s.telemetryReadConn().QueryRow(ctx, `
		SELECT countMerge(span_count_state),
		       countMerge(error_count_state),
		       sumMerge(duration_sum_state) / 1e6
		         / nullIf(countMerge(span_count_state), 0) AS avg_ms,
		       arrayElement(quantilesTDigestMerge(0.5, 0.95, 0.99)(duration_q_state), 1) / 1e6 AS p50_ms,
		       arrayElement(quantilesTDigestMerge(0.5, 0.95, 0.99)(duration_q_state), 2) / 1e6 AS p95_ms,
		       arrayElement(quantilesTDigestMerge(0.5, 0.95, 0.99)(duration_q_state), 3) / 1e6 AS p99_ms
		FROM messaging_caller_summary_5m
		WHERE time_bucket >= ? AND time_bucket <= ?
		  AND msg_system = ? AND cluster = ? AND destination = ?
		SETTINGS max_execution_time = 8`,
		bucketStart, to, system, cluster, destination)
	if err := row.Scan(&out.SpanCount, &out.ErrorCount, &avgMs, &p50Ms, &p95Ms, &p99Ms); err != nil {
		return nil, err
	}
	// v0.5.301 — NaN/Inf scrub before JSON marshal.
	out.AvgMs = safeF(avgMs)
	out.P50Ms = safeF(p50Ms)
	out.P95Ms = safeF(p95Ms)
	out.P99Ms = safeF(p99Ms)
	if out.SpanCount > 0 {
		out.ErrorRate = float64(out.ErrorCount) / float64(out.SpanCount) * 100
	}

	// Per-(service, pod, role) breakdown from the MV. kind
	// rides the dimension so a service that both publishes and
	// consumes lands on two rows.
	rows, err := s.telemetryReadConn().Query(ctx, `
		SELECT service_name,
		       host_name AS pod,
		       coalesce(nullIf(kind, ''), 'client') AS role,
		       countMerge(span_count_state),
		       countMerge(error_count_state),
		       sumMerge(duration_sum_state) / 1e6
		         / nullIf(countMerge(span_count_state), 0) AS avg_ms,
		       arrayElement(quantilesTDigestMerge(0.5, 0.95, 0.99)(duration_q_state), 1) / 1e6 AS p50_ms,
		       arrayElement(quantilesTDigestMerge(0.5, 0.95, 0.99)(duration_q_state), 2) / 1e6 AS p95_ms,
		       arrayElement(quantilesTDigestMerge(0.5, 0.95, 0.99)(duration_q_state), 3) / 1e6 AS p99_ms
		FROM messaging_caller_summary_5m
		WHERE time_bucket >= ? AND time_bucket <= ?
		  AND msg_system = ? AND cluster = ? AND destination = ?
		GROUP BY service_name, pod, role
		ORDER BY countMerge(span_count_state) DESC
		LIMIT 500
		SETTINGS max_execution_time = 8`,
		bucketStart, to, system, cluster, destination)
	if err != nil {
		return out, nil
	}
	defer rows.Close()
	for rows.Next() {
		var b DBCallerBreakdown
		var bAvg, bP50, bP95, bP99 *float64
		if err := rows.Scan(&b.Service, &b.Pod, &b.Role, &b.SpanCount, &b.ErrorCount, &bAvg, &bP50, &bP95, &bP99); err != nil {
			continue
		}
		if bAvg != nil {
			b.AvgMs = *bAvg
		}
		if bP50 != nil {
			b.P50Ms = *bP50
		}
		if bP95 != nil {
			b.P95Ms = *bP95
		}
		if bP99 != nil {
			b.P99Ms = *bP99
		}
		if b.SpanCount > 0 {
			b.ErrorRate = float64(b.ErrorCount) / float64(b.SpanCount) * 100
		}
		out.Callers = append(out.Callers, b)
	}

	// Produce/consume series — v0.8.364 (Stage-2 M1). The caller MV
	// already carries kind + time_bucket, so the split-by-time read
	// is a single bounded merged-state GROUP BY (window/5min buckets
	// × ≤2 kinds; LIMIT 5000 covers >17 days). ORDER BY t lets the
	// fold below build the ascending series in one pass. Failure is
	// non-fatal — the drawer renders without sparklines.
	sRows, err := s.telemetryReadConn().Query(ctx, `
		SELECT toUnixTimestamp(time_bucket) AS t,
		       kind,
		       countMerge(span_count_state) AS c
		FROM messaging_caller_summary_5m
		WHERE time_bucket >= ? AND time_bucket <= ?
		  AND msg_system = ? AND cluster = ? AND destination = ?
		  AND kind IN ('producer', 'consumer')
		GROUP BY t, kind
		ORDER BY t
		LIMIT 5000
		SETTINGS max_execution_time = 8`,
		bucketStart, to, system, cluster, destination)
	if err == nil {
		for sRows.Next() {
			// v0.9.817 — `toUnixTimestamp()` UInt32 döndürür. Bu satır
			// `int64` bağlıyordu; sürücü dönüşümü DESTEKLEMİYOR, yani Scan
			// HER satırda hata veriyor ve aşağıdaki `continue` onu yutuyordu.
			// Sonuç: seri HER ZAMAN boş, drawer'ın produce/consume
			// sparkline'ları hiç çizilmedi — hata da yok, log da yok.
			// Kardeş okumaların hepsi bu tipi doğru bağlıyor (external.go:221,
			// anomaly.go:690, heatmap.go:201); yalnız messaging kaçırmıştı.
			var t uint32
			var kind string
			var c uint64
			if err := sRows.Scan(&t, &kind, &c); err != nil {
				continue
			}
			ts := int64(t)
			if n := len(out.Series); n == 0 || out.Series[n-1].TimeS != ts {
				out.Series = append(out.Series, MsgKindPoint{TimeS: ts})
			}
			p := &out.Series[len(out.Series)-1]
			switch kind {
			case "producer":
				p.ProduceCount += c
			case "consumer":
				p.ConsumeCount += c
			}
		}
		sRows.Close()
	}

	// End-to-end produce→consume latency — v0.8.372 (Stage-2 M2).
	// span_links-correlated, one bounded scan (messaging_e2e.go).
	// Best-effort: an error leaves E2E nil and the drawer renders
	// without the section — it never blocks the detail payload.
	if e2e, err := s.getMessagingE2E(ctx, system, cluster, destination, from, to); err == nil {
		out.E2E = e2e
	}

	// Top operations — for messaging the span name is the
	// useful pivot (e.g. "publish kafka.orders" / "consume
	// kafka.orders"). No truncation needed; OTel span names
	// are short by spec.
	opRows, err := s.telemetryReadConn().Query(ctx, `
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
// "(peer_service = ” OR IS NULL)" no-arg branch.
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
	// v0.5.327 — back on the MV path. db_summary_5m now carries
	// the db_name dim (added by the migration in store.go's
	// runMigrations), so the per-(host, database) split lives in
	// the pre-aggregate. Drops the cost of the v0.5.315 raw-spans
	// stopgap from ~5-10M-row GROUP BY to ~thousands of rows of
	// merged state — typically sub-100ms vs the prior 1-5s on
	// wider windows.
	bucketStart := from.Truncate(5 * time.Minute)
	rows, err := s.telemetryReadConn().Query(ctx, `
		SELECT db_system,
		       instance,
		       db_name,
		       countMerge(span_count_state)                                          AS span_count,
		       countMerge(error_count_state)                                         AS error_count,
		       sumMerge(duration_sum_state) / 1e6
		         / nullIf(countMerge(span_count_state), 0)                           AS avg_ms,
		       arrayElement(quantilesTDigestMerge(0.5, 0.95, 0.99)(duration_q_state), 1) / 1e6 AS p50_ms,
		       arrayElement(quantilesTDigestMerge(0.5, 0.95, 0.99)(duration_q_state), 2) / 1e6 AS p95_ms,
		       arrayElement(quantilesTDigestMerge(0.5, 0.95, 0.99)(duration_q_state), 3) / 1e6 AS p99_ms
		FROM db_summary_5m
		WHERE time_bucket >= ? AND time_bucket <= ?
		GROUP BY db_system, instance, db_name
		ORDER BY span_count DESC
		LIMIT 5000
		SETTINGS max_execution_time = 15`, bucketStart, to)
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
		// Scan is POSITIONAL — the pointer order must track the SELECT
		// order exactly, or every later column shifts by one.
		var avgMs, p50Ms, p95Ms, p99Ms *float64
		if err := rows.Scan(&r.System, &r.Instance, &r.DBName, &r.SpanCount, &r.ErrorCount,
			&avgMs, &p50Ms, &p95Ms, &p99Ms); err != nil {
			return nil, err
		}
		// v0.5.301 — NaN/Inf scrub before JSON marshal.
		r.AvgMs = safeF(avgMs)
		r.P50Ms = safeF(p50Ms)
		r.P95Ms = safeF(p95Ms)
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

	// v0.5.327 — caller pass is precise per-db now that the MV
	// carries db_name. Maps directly to the (system, instance,
	// db_name) row identity used above; no more prefix-spread
	// approximation. db_caller_summary_5m's GROUP BY produces
	// distinct rollups keyed on the same triple plus the calling
	// service / host.
	cRows, err := s.telemetryReadConn().Query(ctx, `
		SELECT db_system,
		       instance,
		       db_name,
		       service_name,
		       countMerge(span_count_state) AS c
		FROM db_caller_summary_5m
		WHERE time_bucket >= ? AND time_bucket <= ?
		GROUP BY db_system, instance, db_name, service_name
		ORDER BY db_system, instance, db_name, c DESC
		LIMIT 2000
		SETTINGS max_execution_time = 8`, bucketStart, to)
	if err != nil {
		return out, nil // partial result is fine — callers are optional
	}
	defer cRows.Close()
	for cRows.Next() {
		var system, instance, dbName, svc string
		var c uint64
		if err := cRows.Scan(&system, &instance, &dbName, &svc, &c); err != nil {
			continue
		}
		i, ok := idxByKey[key{system, instance, dbName}]
		if !ok {
			continue
		}
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
	// v0.9.693 (perf taraması #4) — ÖNEK KAPISI. Dört receiver ailesi
	// koşulsuz taranıyordu; kurulumların çoğunda üçü HİÇ veri
	// üretmiyor ve her tik boş bir tam tarama ödeniyordu.
	//
	// ÖLÇÜLDÜ (chc-0, 2 saat): oracledb.* = 173.160 satır;
	// postgresql./mysql./redis. = 0 / 0 / 0. Tarama raporu bu şekli
	// SELECT baytının %9.5'i olarak ölçmüştü.
	//
	// Kapı metric_catalog'dan: MV insert anında dolar (v0.8.396), yani
	// YANLIŞ NEGATİF üretmez — bir motor veri yaymaya başladığı anda
	// katalogda görünür.
	//
	// TAZELİK ŞART: katalogda TTL YOK. `maxMerge(last_seen_state)`
	// olmadan, bir kez bağlanıp sonra sökülen bir motor kapıyı sonsuza
	// kadar açık tutardı — yani kapı zamanla kendiliğinden anlamsızlaşır.
	for _, prefix := range []struct{ metric, system string }{
		{"oracledb.", "oracle"},
		{"postgresql.", "postgresql"},
		{"mysql.", "mysql"},
		{"redis.", "redis"},
	} {
		if !s.receiverPrefixActive(ctx, prefix.metric) {
			continue
		}
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
	rows, err := s.telemetryReadConn().Query(ctx, q, specificAttr, from, to, metricPrefix)
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

// msgOverviewRowLimit — /messaging genel bakışın satır tavanı. Tavana
// DAYANMAK sessiz bir yalandır: 200 satır dönen bir sayfa "estateın
// tamamı bu" gibi okunur, oysa 201. destination hiç görünmez. v0.9.813
// bu yüzden tavanı zarfta İLAN ediyor (MessagingOverview.RowsCapped).
const msgOverviewRowLimit = 200

// msgTopCallersPerDest — satır başına taşınan üst çağıran sayısı.
// Frontend zaten ilk 3'ü çizip "+N" diyor; 5 hem eski davranışın
// (len(Callers) < 5) birebir aynısı hem de LIMIT n BY'ın n'i.
const msgTopCallersPerDest = 5

// MessagingOverview — /api/messaging'in zarfı (v0.9.813).
//
// ZARF NEDEN: eski uç çıplak dizi döndürüyordu ve bir dizi "kesildim"
// diyemez. RowsCapped olmadan LIMIT 200 tamamen görünmez bir kesme
// noktasıydı: 1000 topic'li bir kurulumda operatör 200 satır görüp
// listeyi tam sanıyordu. Zarf değişimi cache anahtar önekini de
// sürümledi (messaging:v2:) — v0.9.443/458 dersi: eski zarf yeni
// koda 30 sn boyunca servis edilirse sayfa boş açılır.
type MessagingOverview struct {
	Rows []MessagingInstance `json:"rows"`
	// RowsCapped — okuma tavana dayandı, liste EKSİK olabilir.
	RowsCapped bool `json:"rowsCapped,omitempty"`
	// RowLimit — tavanın kendisi; UI şeridi sayıyı hardcode etmesin.
	RowLimit int `json:"rowLimit,omitempty"`
}

// msgTopCallersSQL — satır başına ÜST çağıranlar (v0.9.813).
//
// ESKİ HÂLİ ALFABETİK KESİYORDU: `ORDER BY msg_system, cluster,
// destination, c DESC LIMIT 1000` — sıralama önce KİMLİĞE göre, kesme
// ise global 1000'de. Yani destination adı alfabenin sonunda kalan her
// topic'in çağıranları tamamen düşüyordu ve tabloda "—" görünüyordu:
// "bu topic'e kimse yazmıyor" diye okunan bir boşluk, oysa yalnız
// listenin 1000. satırından sonraya düşmüştü. 200 destination × 5
// çağıran zaten 1000'e dayanıyor, yani kesme İSTİSNA değil KURALDI.
//
// CH'nin `LIMIT n BY` tam olarak bunun için var: kesme artık grup
// BAŞINA uygulanıyor, ORDER BY ise saf `c DESC`. Davranış "her
// destination'ın en yoğun 5 çağıranı" — ilan edilen şeyin ta kendisi.
// Dıştaki LIMIT tel-bayt tavanı olarak kalıyor (200 × 5 = 1000 tam
// oturur; tavan hiçbir destination'ı ayrım gözetmeden kesmez).
var msgTopCallersSQL = `
	SELECT msg_system,
	       cluster,
	       destination,
	       service_name,
	       countMerge(span_count_state) AS c
	FROM messaging_caller_summary_5m
	WHERE time_bucket >= ? AND time_bucket <= ?
	GROUP BY msg_system, cluster, destination, service_name
	ORDER BY c DESC
	LIMIT ` + strconv.Itoa(msgTopCallersPerDest) + ` BY msg_system, cluster, destination
	LIMIT ` + strconv.Itoa(msgOverviewRowLimit*msgTopCallersPerDest) + `
	SETTINGS max_execution_time = 8`

// msgOverviewCapped — okunan satır sayısı tavana dayandı mı? SAF;
// tablo-güdümlü test v0.9.813. `>=` bilinçli: CH tam LIMIT kadar satır
// döndürdüğünde daha fazlası VAR MI bilinmiyor demektir, ve "bilmiyorum"
// burada "eksik olabilir" tarafına yuvarlanmalı.
func msgOverviewCapped(rowCount int) bool { return rowCount >= msgOverviewRowLimit }

// GetMessaging is the structural parallel for messaging systems.
// Resolves the destination name from messaging.destination.name
// when present (OTel semconv), falling back to peer.service.
// arrayElement / indexOf is cheap because attr_keys is bounded
// per row + the WHERE prunes by msg_system on the indexed column
// first.
func (s *Store) GetMessaging(ctx context.Context, from, to time.Time) (*MessagingOverview, error) {
	rows, err := s.getMessaging(ctx, from, to, true)
	if err != nil {
		return nil, err
	}
	return &MessagingOverview{
		Rows:       rows,
		RowsCapped: msgOverviewCapped(len(rows)),
		RowLimit:   msgOverviewRowLimit,
	}, nil
}

// GetMessagingRollup — v0.8.364 (Stage-2 M1). Prior-window read
// for the /api/messaging compare=prior merge. Identical rollup to
// GetMessaging minus the top-callers pass: the delta badges only
// consume counts + quantiles, so the prior scan skips the extra
// MV trip (the endpoints SkipStatus pattern, v0.5.404).
func (s *Store) GetMessagingRollup(ctx context.Context, from, to time.Time) ([]MessagingInstance, error) {
	return s.getMessaging(ctx, from, to, false)
}

// applyMsgKindSplit folds one (kind, calls, errors, p95) rollup row from
// messaging_caller_summary_5m onto its overview row. Producer /
// consumer land in the split buckets; every other span kind
// (client/server/internal broker chatter) intentionally counts
// toward SpanCount only. Pure — table-driven tested (v0.8.364).
//
// v0.9.816 — p95 katıldı. SAYILAR TOPLANIR, QUANTİLE TOPLANMAZ: iki
// TDigest'in p95'i toplanamaz da ortalanamaz da. Sorgu (msg_system,
// cluster, destination, kind) ile GROUP BY yaptığı için satır başına
// tek değer gelir ve atama yeterlidir; yine de burada MAX alınıyor.
// Gerekçe: o değişmez bir gün bozulursa (GROUP BY genişler, iki MV
// birleşir) keyfi bir "son yazan kazanır" değeri değil, GÖRÜLEN EN
// KÖTÜSÜ raporlanır. Gecikme kolonunun sessizce İYİMSER olması, sessizce
// kötümser olmasından tehlikelidir.
func applyMsgKindSplit(r *MessagingInstance, kind string, calls, errs uint64, p95Ms float64) {
	switch kind {
	case "producer":
		r.ProduceCount += calls
		r.ProduceErrors += errs
		if p95Ms > r.ProduceP95Ms {
			r.ProduceP95Ms = p95Ms
		}
	case "consumer":
		r.ConsumeCount += calls
		r.ConsumeErrors += errs
		if p95Ms > r.ConsumeP95Ms {
			r.ConsumeP95Ms = p95Ms
		}
	}
}

func (s *Store) getMessaging(ctx context.Context, from, to time.Time, includeCallers bool) ([]MessagingInstance, error) {
	if from.IsZero() {
		from = time.Now().Add(-1 * time.Hour)
	}
	if to.IsZero() {
		to = time.Now()
	}
	// v0.9.813 — PENCERE HİZALAMASI. MV kovaları BAŞLANGIÇLARIYLA
	// etiketli; hizalanmamış `time_bucket >= from` baştaki KISMİ kovayı
	// tamamen eler (from=10:03 → 10:00–10:05 arasındaki her şey düşer).
	// Kardeşleri (GetDatabases, GetMessagingTrends) bunu zaten yapıyordu;
	// messaging genel bakışı yapmıyordu, yani Calls kolonu ile satır-içi
	// Trend sparkline'ı AYNI pencereyi farklı okuyordu — tablo trendden
	// az sayı gösteriyordu ve ikisi de "doğru" görünüyordu.
	bucketStart := alignBucketStart(from)
	// MV-backed read from messaging_summary_5m (added v0.5.9).
	// Pre-aggregated by (msg_system, cluster, destination, 5min);
	// the cluster + destination derived expressions are
	// materialised at INSERT time so the read joins on plain
	// string equality. v0.8.364 — p50/p95 projected out of the
	// same TDigest state that already served p99 (elements 1/2 of
	// the 0.5/0.95/0.99 grid); zero extra scan cost.
	rows, err := s.telemetryReadConn().Query(ctx, `
		SELECT msg_system,
		       cluster,
		       destination,
		       countMerge(span_count_state)                            AS span_count,
		       countMerge(error_count_state)                           AS error_count,
		       sumMerge(duration_sum_state) / 1e6
		         / nullIf(countMerge(span_count_state), 0)             AS avg_ms,
		       arrayElement(quantilesTDigestMerge(0.5, 0.95, 0.99)(duration_q_state), 1) / 1e6 AS p50_ms,
		       arrayElement(quantilesTDigestMerge(0.5, 0.95, 0.99)(duration_q_state), 2) / 1e6 AS p95_ms,
		       arrayElement(quantilesTDigestMerge(0.5, 0.95, 0.99)(duration_q_state), 3) / 1e6 AS p99_ms
		FROM messaging_summary_5m
		WHERE time_bucket >= ? AND time_bucket <= ?
		GROUP BY msg_system, cluster, destination
		ORDER BY span_count DESC
		LIMIT `+strconv.Itoa(msgOverviewRowLimit)+`
		SETTINGS max_execution_time = 15`, bucketStart, to)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []MessagingInstance{}
	type key struct{ system, cluster, destination string }
	idxByKey := map[key]int{}
	for rows.Next() {
		var r MessagingInstance
		var avgMs, p50Ms, p95Ms, p99Ms *float64
		if err := rows.Scan(&r.System, &r.Cluster, &r.Destination,
			&r.SpanCount, &r.ErrorCount, &avgMs, &p50Ms, &p95Ms, &p99Ms); err != nil {
			return nil, err
		}
		// v0.5.301 — NaN/Inf scrub before JSON marshal.
		r.AvgMs = safeF(avgMs)
		r.P50Ms = safeF(p50Ms)
		r.P95Ms = safeF(p95Ms)
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

	// Producer/consumer split — v0.8.364 (Stage-2 M1). The kind
	// dimension only exists on messaging_caller_summary_5m; one
	// bounded merged-state GROUP BY per page load rolls it up to
	// (system, cluster, destination, kind) and the fold distributes
	// it onto the overview rows. Rows outside the top-200 overview
	// simply don't match the index and are dropped. Failure is
	// non-fatal — the split columns render as zero.
	// v0.9.816 — p95 AYNI merge'den projekte edildi. duration_q_state
	// quantilesTDigestState(0.5, 0.95, 0.99); eleman 2 = p95. Ek TARAMA
	// yok (ölçüm: read_rows birebir aynı), ek TUR hiç yok — kolonlar
	// zaten çalışan bu sorgunun içinde doğuyor. Ana MV'ye dokunulmadı.
	kRows, err := s.telemetryReadConn().Query(ctx, `
		SELECT msg_system,
		       cluster,
		       destination,
		       kind,
		       countMerge(span_count_state)  AS c,
		       countMerge(error_count_state) AS e,
		       arrayElement(quantilesTDigestMerge(0.5, 0.95, 0.99)(duration_q_state), 2) / 1e6 AS p95_ms
		FROM messaging_caller_summary_5m
		WHERE time_bucket >= ? AND time_bucket <= ?
		  AND kind IN ('producer', 'consumer')
		GROUP BY msg_system, cluster, destination, kind
		ORDER BY c DESC
		LIMIT 2000
		SETTINGS max_execution_time = 8`, bucketStart, to)
	if err == nil {
		for kRows.Next() {
			var system, cluster, destination, kind string
			var c, e uint64
			var p95 *float64
			if err := kRows.Scan(&system, &cluster, &destination, &kind, &c, &e, &p95); err != nil {
				continue
			}
			if i, ok := idxByKey[key{system, cluster, destination}]; ok {
				// safeF — NaN/Inf JSON'a çıkmadan temizlenir (v0.5.301).
				applyMsgKindSplit(&out[i], kind, c, e, safeF(p95))
			}
		}
		kRows.Close()
	}

	if !includeCallers {
		return out, nil
	}

	cRows, err := s.telemetryReadConn().Query(ctx, msgTopCallersSQL, bucketStart, to)
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
		if len(out[i].Callers) < msgTopCallersPerDest && svc != "" {
			out[i].Callers = append(out[i].Callers, svc)
		}
	}
	return out, nil
}

// receiverPrefixActive — bu metrik önekinden SON DÖNEMDE veri geldi mi?
//
// v0.9.693. metric_catalog (AggregatingMergeTree, ORDER BY
// (service_name, metric)) insert anında MV ile dolar; önek araması
// sıralama anahtarının önekine düştüğü için ucuz.
//
// TAZELİK KAPISI (maxMerge(last_seen_state)) ŞART: katalogda TTL yok,
// yani bir kez bağlanmış bir motor kaydı kalıcı. Tazelik olmadan kapı
// ilk bağlantıdan sonra sonsuza kadar açık kalır ve hiçbir işe yaramaz.
// Aynı kalıp ListMetricNames'te de kullanılıyor (repo.go).
//
// HATA → AÇIK KAPI: sorgu patlarsa `true` dönüyoruz. Keşfi sessizce
// kapatmak, yavaş bir sorgudan kötüdür — eksik veri, yavaş veriden
// beterdir.
//
// v0.9.698 — BOŞ KATALOG DA AÇIK KAPI. v0.9.693'te bu kapıyı yazarken
// yalnız HATA yolunu fail-open yaptım; `n=0` yolunu atladım. İkisi
// FARKLI sorular:
//
//	n=0  + dolu katalog  → "bu motor gerçekten yok"        → kapat, doğru
//	n=0  + BOŞ katalog   → "katalog henüz cevap veremiyor"  → kapatmak YANLIŞ
//
// Katalog taze kurulumda, şema resetinde ve MV drop+recreate penceresinde
// (kod tabanı MV tipi değişiminde bunu yapıyor) boştur. O aralıkta kapı
// kapanınca receiver ile keşfedilen TÜM DB instance'ları listeden sessizce
// düşer — hata yok, log yok, sadece eksik veri.
//
// Kardeş çağrı yerleri bu ayrımı zaten yapıyor (MetricExists /
// db_capacity.go, ListMetricNames / repo.go): ikisi de aynı
// metricCatalogHasRows kapısından geçiyor. v0.9.693 sırayı bozan tek
// yerdi.
func (s *Store) receiverPrefixActive(ctx context.Context, prefix string) bool {
	since := time.Now().Add(-metricNameLookback)
	var n uint64
	err := s.telemetryReadConn().QueryRow(ctx,
		`SELECT count() FROM (
			SELECT metric FROM metric_catalog
			WHERE metric LIKE ?
			GROUP BY metric
			HAVING maxMerge(last_seen_state) >= ?
			LIMIT 1
		) SETTINGS max_execution_time = 5`,
		prefix+"%", since).Scan(&n)
	if err != nil {
		return true // kapıyı kapatma
	}
	if n > 0 {
		return true
	}
	// n == 0 — "yok" mu, "henüz bilmiyorum" mu? Katalog tamamen boşsa
	// ikincisi; kapıyı açık bırak ve ham yol çalışsın.
	return !s.metricCatalogHasRows(ctx)
}
