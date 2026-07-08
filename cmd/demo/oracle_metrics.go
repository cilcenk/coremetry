// oracle_metrics.go — synthetic OracleDB-receiver metric emission.
//
// The demo emits Oracle DB *spans* (oraDB, peer.service=oracle) but the
// /databases page's OracleDB-receiver drill-down panel reads oracledb.*
// *metric_points* via chstore.GetOracleMetrics. Without those points the
// panel renders "no data" and no source="receiver" Oracle row appears in
// the DB-instances list.
//
// This file plays the role the real OpenTelemetry `oracledb` receiver
// would. It is a FAITHFUL mirror of the upstream contrib receiver's
// documented default metric set:
//
//   https://github.com/open-telemetry/opentelemetry-collector-contrib/
//     blob/main/receiver/oracledbreceiver/documentation.md
//
// — exact metric names, units, instrument types, and attributes — so the
// operator who collects these in production sees the same shapes here.
//
// Read-contract alignment (internal/chstore/oracle.go +
// internal/chstore/dependencies.go discoverReceiverInstances):
//
//   - Each datapoint carries an `instance` attribute (= "corebank-scan.prod"
//     / "corebank-dg.prod"). discoverReceiverInstances coalesces the
//     receiver-row instance label from that attr, and GetOracleMetrics
//     filters on the same attr — so a single `instance` value BOTH creates
//     the receiver row AND scopes every drill-down read to it. We KEEP this
//     datapoint attr and ADD the faithful resource attributes alongside it.
//   - Resource attributes mirror the real receiver: host.name,
//     oracledb.instance.name, service.instance.id ("host:port/serviceName"),
//     plus service.name + db.system=oracle as before.
//   - Gauges (sessions/processes/transactions/dml_locks/enqueue_*/tablespace
//     /pga_memory/sga_max_size) are emitted as Gauge instruments —
//     GetOracleMetrics reads them with argMax(value,time).
//   - Monotonic Sums (cpu_time, logical/physical reads, parses, executions,
//     commits/rollbacks, deadlocks, io requests, block gets, transactions,
//     wait_time.<class>) climb every flush so GetOracleMetrics's
//     (max-min)/window rate read stays positive across the run — hence the
//     per-instance cumulative state held in oracleReceiverState.
//
// NOTE on transactions: the upstream receiver emits transactions.usage /
// .limit as GAUGES (emitted here). The backend's GetOracleMetrics also reads
// a per-second TransactionsPS off a monotonic `oracledb.transactions` sum, so
// we ALSO keep that cumulative sum — both are present, faithful + working.
//
// Emitted once per metric flush (the existing 10s metrics tick), for BOTH
// instances (corebank-scan.prod RAC SCAN primary at full load +
// corebank-dg.prod Data Guard standby ~0.25x) so both receiver rows populate.

package main

import (
	"strconv"
	"sync"

	commonpb "go.opentelemetry.io/proto/otlp/common/v1"
	metricspb "go.opentelemetry.io/proto/otlp/metrics/v1"
	resourcepb "go.opentelemetry.io/proto/otlp/resource/v1"

	mrand "math/rand/v2"
)

// oracleReceiverService is the synthetic scraper identity that owns the
// oracledb.* points — the equivalent of the OTel Collector pod running the
// oracledb receiver. Kept distinct from the demo's app services so it never
// collides with a span-derived service row.
const oracleReceiverService = "oracledb-receiver"

// oracleInstance describes one monitored Oracle instance. The `name` is the
// `instance` datapoint-attribute label the backend read-contract filters on
// (host portion of the oraDB span server.address). host/port/serviceName feed
// the faithful resource attributes (oracledb.instance.name +
// service.instance.id in "host:port/serviceName" form).
type oracleInstance struct {
	name        string  // `instance` datapoint attr (backend filter key)
	host        string  // host.name resource attr
	port        int     // for service.instance.id
	serviceName string  // Oracle service name, for service.instance.id
	load        float64 // counter advance multiplier (standby runs lighter)
}

// oracleInstances — the RAC SCAN primary (full load) + the Data Guard standby
// (read-only, ~0.25x). Both populate a source="receiver" row.
var oracleInstances = []oracleInstance{
	{name: "corebank-scan.prod", host: "corebank-scan.prod", port: 1521, serviceName: "COREBANK", load: 1.0},
	{name: "corebank-dg.prod", host: "corebank-dg.prod", port: 1521, serviceName: "COREBANK_DG", load: 0.25},
}

// Standard Oracle wait classes (V$SYSTEM_WAIT_CLASS). Each becomes a
// oracledb.wait_time.<class> cumulative counter; GetOracleMetrics strips the
// prefix to recover the bare class name. (Opt-in / panel extra — kept.)
var oracleWaitClasses = []string{
	"user_io", "system_io", "concurrency", "commit",
	"network", "application", "configuration", "scheduler",
	"administrative", "other",
}

// oracleTablespaces is the per-tablespace size table dimensioned by the
// tablespace_name attribute on oracledb.tablespace_size.usage/.limit.
// usedFrac is the steady-state fill ratio; limitBytes the configured max.
var oracleTablespaces = []struct {
	name       string
	usedFrac   float64
	limitBytes float64
}{
	{"SYSTEM", 0.62, 2 * 1024 * 1024 * 1024},
	{"SYSAUX", 0.55, 4 * 1024 * 1024 * 1024},
	{"USERS", 0.78, 32 * 1024 * 1024 * 1024},
	{"UNDOTBS1", 0.34, 8 * 1024 * 1024 * 1024},
	{"TEMP", 0.21, 16 * 1024 * 1024 * 1024},
}

// oracleTopSQL is the Top-SQL-by-elapsed table. sql_text + executions ride
// as attributes on oracledb.top_sql.elapsed; GetOracleMetrics reads sql_text
// + executions and derives avg_elapsed_ms client-side. elapsedPerFlush is
// added to the cumulative elapsed counter each flush; execsPerFlush to the
// execution count — both monotonic so argMax reads the latest total.
// (Panel extra — kept.)
var oracleTopSQL = []struct {
	sqlID           string
	sqlText         string
	elapsedPerFlush float64 // seconds added per flush
	execsPerFlush   uint64
}{
	{"7gq9x2k1m4p", "SELECT * FROM ACCOUNTS WHERE ACCOUNT_ID = :1", 3.2, 4200},
	{"a1b2c3d4e5f", "UPDATE ACCOUNTS SET BALANCE = :1 WHERE ACCOUNT_ID = :2", 5.8, 1800},
	{"z9y8x7w6v5u", "INSERT INTO TXN_JOURNAL (ID, AMT, TS) VALUES (:1, :2, :3)", 4.1, 2600},
	{"q1w2e3r4t5y", "SELECT SUM(AMOUNT) FROM TXN_JOURNAL WHERE ACCOUNT_ID = :1", 8.7, 950},
	{"m5n6b7v8c9x", "BEGIN GL_POST(:1, :2, :3); END;", 6.3, 740},
}

// oracleCounters holds the monotonic-cumulative state for one instance.
// Each field is the running total that climbs every flush; GetOracleMetrics
// reads (max-min)/window over the query window to recover a per-second rate,
// so these MUST only ever increase across the process lifetime.
type oracleCounters struct {
	// ── canonical default monotonic sums ──
	cpuTime        float64 // seconds  (oracledb.cpu_time)
	enqueueDeadlks float64 // oracledb.enqueue_deadlocks
	exchangeDeadlk float64 // oracledb.exchange_deadlocks
	executions     float64 // oracledb.executions
	hardParses     float64 // oracledb.hard_parses
	parseCalls     float64 // oracledb.parse_calls
	logicalReads   float64 // oracledb.logical_reads
	physicalReads  float64 // oracledb.physical_reads
	userCommits    float64 // oracledb.user_commits
	userRollbacks  float64 // oracledb.user_rollbacks
	// ── opt-in monotonic sums the operator's setup + panel use ──
	physReadIOReq  float64 // oracledb.physical_read_io_requests
	physWriteIOReq float64 // oracledb.physical_write_io_requests
	physicalWrites float64 // oracledb.physical_writes
	dbBlockGets    float64 // oracledb.db_block_gets
	consistentGets float64 // oracledb.consistent_gets
	// transactions — real receiver = gauge usage/limit; we ALSO keep a
	// monotonic sum so the backend's TransactionsPS rate read still works.
	transactions float64 // oracledb.transactions (cumulative)
	// ── panel extras (kept) ──
	waitTime      map[string]float64 // class → cumulative seconds waited
	topSQLElapsed map[string]float64 // sql_id → cumulative elapsed seconds
	topSQLExecs   map[string]uint64  // sql_id → cumulative executions
}

// oracleReceiverState carries per-instance cumulative counters across flushes.
// Guarded by mu because flush() runs on the metrics-tick goroutine.
type oracleReceiverState struct {
	mu       sync.Mutex
	counters map[string]*oracleCounters // instance → counters
}

var oracleRX = &oracleReceiverState{counters: map[string]*oracleCounters{}}

func (s *oracleReceiverState) get(instance string) *oracleCounters {
	c, ok := s.counters[instance]
	if !ok {
		c = &oracleCounters{
			waitTime:      map[string]float64{},
			topSQLElapsed: map[string]float64{},
			topSQLExecs:   map[string]uint64{},
		}
		s.counters[instance] = c
	}
	return c
}

// oracleReceiverMetrics builds the oracledb.* ResourceMetrics for both
// instances. Called from metricsState.flush; the result is appended to the
// flush's ResourceMetrics slice. nowNs is the flush timestamp; startNs the
// counter epoch (mirrors how the rest of flush stamps points).
func oracleReceiverMetrics(startNs, nowNs uint64) []*metricspb.ResourceMetrics {
	oracleRX.mu.Lock()
	defer oracleRX.mu.Unlock()

	// Read the live load factors ONCE per flush (mirrors main.go flush) so the
	// Oracle receiver breathes with the diurnal curve + incidents like every
	// other signal, instead of staying flat on the static inst.load. (drift fix)
	//   rateFactor   → throughput counters (execs, reads, commits …)
	//   latencyFactor→ wait-time + SQL-elapsed (saturation stretches waits)
	//   errorBump    → deadlock probability (correlated failures)
	rf := L.rateFactor()
	lf := L.latencyFactor()
	dlMult := oracleDeadlockMult(L.errorBump(), L.incidentLabel() == "oracle-row-lock-contention")

	var rms []*metricspb.ResourceMetrics
	for _, inst := range oracleInstances {
		c := oracleRX.get(inst.name)
		advanceOracleCounters(c, inst.load*rf, lf, dlMult)

		metrics := buildOracleMetrics(inst, c, startNs, nowNs)
		// service.instance.id in the receiver's documented
		// "host:port/serviceName" shape, e.g. "corebank-scan.prod:1521/COREBANK".
		svcInstID := inst.host + ":" + strconv.Itoa(inst.port) + "/" + inst.serviceName
		rms = append(rms, &metricspb.ResourceMetrics{
			Resource: &resourcepb.Resource{Attributes: []*commonpb.KeyValue{
				kvStr("service.name", oracleReceiverService),
				// Faithful oracledb-receiver resource attributes.
				kvStr("host.name", inst.host),
				kvStr("oracledb.instance.name", inst.name),
				kvStr("service.instance.id", svcInstID),
				kvStr("db.system", "oracle"),
				// v0.8.383 — no environment attr: the Oracle RAC is
				// shared cross-env infrastructure (topology's infra
				// nodes deliberately don't inherit an env either), and
				// emitting one here would pollute /api/environments
				// with a fourth pseudo-env next to int/uat/prep.
			}},
			ScopeMetrics: []*metricspb.ScopeMetrics{{
				Scope:   &commonpb.InstrumentationScope{Name: "otelcol/oracledbreceiver"},
				Metrics: metrics,
			}},
		})
	}
	return rms
}

// advanceOracleCounters bumps every monotonic counter for one instance by a
// jittered per-flush delta scaled by `load`. Keeps the (max-min)/window rate
// read positive and lifelike across the run. Internally consistent: logical
// reads >> physical reads (~99% cache hit), deadlocks grow rarely/slowly,
// commits >> rollbacks.
// oracleDeadlockMult amplifies the per-flush deadlock probability. Any incident
// nudges it up via the error-bump (correlated failures across the mesh), and the
// oracle-row-lock-contention incident — for which deadlocks ARE the symptom —
// spikes it hard so the operator sees the deadlock metrics move together with the
// row-lock trace incident, not stay flat. Pure so the realism stays tested.
func oracleDeadlockMult(errBump float64, rowLock bool) float64 {
	m := 1 + errBump*12
	if rowLock {
		m *= 5
	}
	return m
}

// advanceOracleCounters bumps the monotonic counters for one instance. `load`
// is the EFFECTIVE rate (inst.load × rateFactor) so throughput breathes with the
// diurnal curve + incidents; `latFactor` stretches wait-time / SQL-elapsed under
// saturation; `dlMult` amplifies deadlocks during incidents (see
// oracleDeadlockMult). (drift fix — previously read only the static inst.load.)
func advanceOracleCounters(c *oracleCounters, load, latFactor, dlMult float64) {
	jit := func(base float64) float64 { return base * load * (0.8 + mrand.Float64()*0.4) }

	c.cpuTime += jit(2.5) // ~2.5 CPU-sec per 10s flush at full load
	c.executions += jit(46_000)
	c.hardParses += jit(180)
	c.parseCalls += jit(9_500)
	c.logicalReads += jit(370_000) // ~37k logical reads/sec
	c.physicalReads += jit(4_200)  // ~420 physical reads/sec → ~98.9% cache hit
	c.userCommits += jit(3_100)
	c.userRollbacks += jit(120)
	c.transactions += jit(3_220) // commits + rollbacks ≈ transactions

	// Deadlocks are rare: only occasionally tick up, and slowly. At full
	// load roughly one enqueue-deadlock every few minutes; exchange even
	// rarer. Keeps them monotonic without an unrealistic flood.
	if mrand.Float64() < 0.15*load*dlMult {
		c.enqueueDeadlks += 1
	}
	if mrand.Float64() < 0.05*load*dlMult {
		c.exchangeDeadlk += 1
	}

	// I/O + block-gets — physical writes track a fraction of reads; block
	// gets dominate (current + consistent reads make up the logical reads).
	c.physReadIOReq += jit(3_400)
	c.physWriteIOReq += jit(2_100)
	c.physicalWrites += jit(2_600)
	c.dbBlockGets += jit(95_000)
	c.consistentGets += jit(275_000) // db_block_gets + consistent_gets ≈ logical_reads

	// Wait time stretches with saturation: delta ∝ load (how many waits) ×
	// latFactor (how long each wait blocks). The row-lock incident (latFactor
	// 2.4) thus visibly inflates the Concurrency/enqueue waits with the trace
	// incident instead of staying flat.
	for _, wc := range oracleWaitClasses {
		c.waitTime[wc] += jit(oracleWaitClassWeight(wc)) * latFactor
	}

	for _, sq := range oracleTopSQL {
		c.topSQLElapsed[sq.sqlID] += sq.elapsedPerFlush * load * latFactor * (0.85 + mrand.Float64()*0.3)
		c.topSQLExecs[sq.sqlID] += uint64(float64(sq.execsPerFlush) * load)
	}
}

// oracleWaitClassWeight returns the per-flush cumulative-seconds delta for a
// wait class — shapes the wait-class distribution panel.
func oracleWaitClassWeight(class string) float64 {
	switch class {
	case "user_io":
		return 4.2
	case "commit":
		return 2.8
	case "concurrency":
		return 1.1
	case "network":
		return 0.9
	case "system_io":
		return 0.7
	case "application":
		return 0.4
	case "configuration":
		return 0.2
	case "scheduler":
		return 0.15
	case "administrative":
		return 0.1
	default: // other
		return 0.25
	}
}

// buildOracleMetrics assembles the oracledb.* Metric protobufs for one
// instance. Gauges use the latest computed reading; sums emit the running
// cumulative total. Every datapoint carries the `instance` attribute that the
// backend read-contract filters on.
func buildOracleMetrics(inst oracleInstance, c *oracleCounters, startNs, nowNs uint64) []*metricspb.Metric {
	instance := inst.name
	load := inst.load
	instAttr := func(extra ...*commonpb.KeyValue) []*commonpb.KeyValue {
		return append([]*commonpb.KeyValue{kvStr("instance", instance)}, extra...)
	}

	gauge := func(name, unit, desc string, val float64, attrs []*commonpb.KeyValue) *metricspb.Metric {
		return &metricspb.Metric{
			Name: name, Unit: unit, Description: desc,
			Data: &metricspb.Metric_Gauge{Gauge: &metricspb.Gauge{
				DataPoints: []*metricspb.NumberDataPoint{{
					TimeUnixNano: nowNs,
					Value:        &metricspb.NumberDataPoint_AsDouble{AsDouble: val},
					Attributes:   attrs,
				}},
			}},
		}
	}
	sum := func(name, unit, desc string, val float64, attrs []*commonpb.KeyValue) *metricspb.Metric {
		return &metricspb.Metric{
			Name: name, Unit: unit, Description: desc,
			Data: &metricspb.Metric_Sum{Sum: &metricspb.Sum{
				IsMonotonic:            true,
				AggregationTemporality: metricspb.AggregationTemporality_AGGREGATION_TEMPORALITY_CUMULATIVE,
				DataPoints: []*metricspb.NumberDataPoint{{
					StartTimeUnixNano: startNs, TimeUnixNano: nowNs,
					Value:      &metricspb.NumberDataPoint_AsDouble{AsDouble: val},
					Attributes: attrs,
				}},
			}},
		}
	}

	var out []*metricspb.Metric

	// ──────────────────────────────────────────────────────────────────────
	// CANONICAL DEFAULT GAUGES
	// ──────────────────────────────────────────────────────────────────────

	// Resource / lock caps. usage jitters under a fixed limit so the
	// progress bars move and usage < limit always holds.
	usageUnder := func(limit, base, span float64) float64 {
		v := base + span*load*(0.7+mrand.Float64()*0.5)
		if v > limit {
			v = limit
		}
		return v
	}

	dmlLockLimit := 8000.0
	dmlLockUsage := usageUnder(dmlLockLimit, 600, 2200)
	enqLockLimit := 12000.0
	enqLockUsage := usageUnder(enqLockLimit, 900, 4200)
	enqResLimit := 16000.0
	enqResUsage := usageUnder(enqResLimit, 1200, 5600)
	procLimit := 800.0
	procUsage := usageUnder(procLimit, 150, 260)
	sessLimit := 1224.0 // Oracle default = 1.1*processes + 5, rounded
	txnLimit := 605.0    // transactions = 1.1*sessions, rounded
	txnUsage := usageUnder(txnLimit, 60, 180)

	out = append(out,
		gauge("oracledb.dml_locks.limit", "{locks}", "Maximum limit of DML locks, -1 if unlimited", dmlLockLimit, instAttr()),
		gauge("oracledb.dml_locks.usage", "{locks}", "Current count of DML locks", dmlLockUsage, instAttr()),
		gauge("oracledb.enqueue_locks.limit", "{locks}", "Maximum limit of enqueue locks, -1 if unlimited", enqLockLimit, instAttr()),
		gauge("oracledb.enqueue_locks.usage", "{locks}", "Current count of enqueue locks", enqLockUsage, instAttr()),
		gauge("oracledb.enqueue_resources.limit", "{resources}", "Maximum limit of enqueue resources, -1 if unlimited", enqResLimit, instAttr()),
		gauge("oracledb.enqueue_resources.usage", "{resources}", "Current count of enqueue resources", enqResUsage, instAttr()),
		gauge("oracledb.processes.limit", "{processes}", "Maximum limit of processes, -1 if unlimited", procLimit, instAttr()),
		gauge("oracledb.processes.usage", "{processes}", "Current count of processes", procUsage, instAttr()),
		gauge("oracledb.sessions.limit", "{sessions}", "Maximum limit of sessions, -1 if unlimited", sessLimit, instAttr()),
		gauge("oracledb.transactions.limit", "{transactions}", "Maximum limit of transactions, -1 if unlimited", txnLimit, instAttr()),
		gauge("oracledb.transactions.usage", "{transactions}", "Current count of transactions", txnUsage, instAttr()),
	)

	// oracledb.sessions.usage — DIMENSIONED by session_type + session_status,
	// exactly as the real receiver emits it. The four (type × status) buckets
	// sum to a fraction of sessions.limit.
	//
	// We ALSO emit the legacy single-attr `oracledb.sessions` keyed on
	// `status` (active/inactive) that the backend's queryOracleSessionsByStatus
	// historically read, so neither read path goes blank.
	totalSessions := usageUnder(sessLimit, 120, 440)
	// Split: ~70% USER / 30% BACKGROUND; within each, active vs inactive.
	userTotal := totalSessions * 0.70
	bgTotal := totalSessions * 0.30
	userActiveFrac := 0.40 + mrand.Float64()*0.15
	bgActiveFrac := 0.85 + mrand.Float64()*0.10 // background mostly active
	userActive := userTotal * userActiveFrac
	userInactive := userTotal - userActive
	bgActive := bgTotal * bgActiveFrac
	bgInactive := bgTotal - bgActive

	sessUsage := func(typ, status string, v float64) *metricspb.Metric {
		return gauge("oracledb.sessions.usage", "{sessions}", "Count of active sessions", v,
			instAttr(kvStr("session_type", typ), kvStr("session_status", status)))
	}
	out = append(out,
		sessUsage("USER", "ACTIVE", userActive),
		sessUsage("USER", "INACTIVE", userInactive),
		sessUsage("BACKGROUND", "ACTIVE", bgActive),
		sessUsage("BACKGROUND", "INACTIVE", bgInactive),
	)
	// Legacy active/inactive split (status attr) for the backend's
	// queryOracleSessionsByStatus read.
	totalActive := userActive + bgActive
	totalInactive := userInactive + bgInactive
	out = append(out,
		gauge("oracledb.sessions", "{sessions}", "Sessions by status", totalActive,
			instAttr(kvStr("status", "active"))),
		gauge("oracledb.sessions", "{sessions}", "Sessions by status", totalInactive,
			instAttr(kvStr("status", "inactive"))),
	)

	// Tablespace size gauges (dimensioned by tablespace_name), unit By.
	for _, ts := range oracleTablespaces {
		used := ts.limitBytes * ts.usedFrac * (0.97 + mrand.Float64()*0.06)
		if used > ts.limitBytes {
			used = ts.limitBytes
		}
		out = append(out,
			gauge("oracledb.tablespace_size.limit", "By", "Maximum size of tablespace in bytes, -1 if unlimited",
				ts.limitBytes, instAttr(kvStr("tablespace_name", ts.name))),
			gauge("oracledb.tablespace_size.usage", "By", "Used tablespace in bytes",
				used, instAttr(kvStr("tablespace_name", ts.name))),
		)
	}

	// SGA max size gauge (panel extra — kept; fixed allocation).
	out = append(out,
		gauge("oracledb.sga_max_size", "By", "SGA maximum size", 8.0*1024*1024*1024, instAttr()),
	)

	// ──────────────────────────────────────────────────────────────────────
	// CANONICAL DEFAULT MONOTONIC SUMS
	// ──────────────────────────────────────────────────────────────────────
	out = append(out,
		sum("oracledb.cpu_time", "s", "Cumulative CPU time, in seconds", c.cpuTime, instAttr()),
		sum("oracledb.enqueue_deadlocks", "{deadlocks}", "Total number of deadlocks between table or row locks in different sessions", c.enqueueDeadlks, instAttr()),
		sum("oracledb.exchange_deadlocks", "{deadlocks}", "Number of times a process detected a potential deadlock when exchanging two buffers", c.exchangeDeadlk, instAttr()),
		sum("oracledb.executions", "{executions}", "Total number of calls (user and recursive) that executed SQL statements", c.executions, instAttr()),
		sum("oracledb.hard_parses", "{parses}", "Number of hard parses", c.hardParses, instAttr()),
		sum("oracledb.parse_calls", "{parses}", "Total number of parse calls", c.parseCalls, instAttr()),
		sum("oracledb.logical_reads", "{reads}", "Number of logical reads", c.logicalReads, instAttr()),
		sum("oracledb.physical_reads", "{reads}", "Number of physical reads", c.physicalReads, instAttr()),
		// PGA: the receiver types this as a monotonic Sum (By), but the
		// reported value is the CURRENT total PGA allocated (V$PGASTAT), a
		// roughly-stable ~2.4GB figure — NOT a runaway per-scrape accumulator.
		// We emit it as a Sum (faithful instrument) whose datapoint carries the
		// live value, so the backend's argMax gauge read shows real bytes.
		sum("oracledb.pga_memory", "By", "Session PGA (Program Global Area) memory",
			2.4*1024*1024*1024+load*mrand.Float64()*512*1024*1024, instAttr()),
		sum("oracledb.user_commits", "{commits}", "Number of user commits. When a user commits a transaction, the redo generated that reflects the changes made to database blocks must be written to disk", c.userCommits, instAttr()),
		sum("oracledb.user_rollbacks", "1", "Number of times users manually issue the ROLLBACK statement or an error occurs during a user's transactions", c.userRollbacks, instAttr()),
	)

	// transactions monotonic sum — kept ALONGSIDE the gauge usage/limit so
	// the backend's TransactionsPS (max-min)/window rate read still renders.
	out = append(out,
		sum("oracledb.transactions", "{transaction}", "Cumulative transactions (kept for rate read)", c.transactions, instAttr()),
	)

	// ──────────────────────────────────────────────────────────────────────
	// OPT-IN MONOTONIC SUMS (operator's setup + panel use these)
	// ──────────────────────────────────────────────────────────────────────
	out = append(out,
		sum("oracledb.physical_read_io_requests", "{requests}", "Number of read requests for application activity", c.physReadIOReq, instAttr()),
		sum("oracledb.physical_write_io_requests", "{requests}", "Number of write requests for application activity", c.physWriteIOReq, instAttr()),
		sum("oracledb.physical_writes", "{writes}", "Number of physical writes", c.physicalWrites, instAttr()),
		sum("oracledb.db_block_gets", "{gets}", "Number of times a current block was requested from the buffer cache", c.dbBlockGets, instAttr()),
		sum("oracledb.consistent_gets", "{gets}", "Number of times a consistent read was requested for a block from the buffer cache", c.consistentGets, instAttr()),
	)

	// ──────────────────────────────────────────────────────────────────────
	// PANEL EXTRAS (kept — backend currently reads these)
	// ──────────────────────────────────────────────────────────────────────

	// Wait-class cumulative counters.
	for _, wc := range oracleWaitClasses {
		out = append(out, sum("oracledb.wait_time."+wc, "s",
			"Cumulative time waited in the "+wc+" class", c.waitTime[wc], instAttr()))
	}

	// Top SQL by elapsed (sql_id / sql_text / executions attrs).
	for _, sq := range oracleTopSQL {
		out = append(out, &metricspb.Metric{
			Name: "oracledb.top_sql.elapsed", Unit: "s",
			Description: "Top SQL by cumulative elapsed time",
			Data: &metricspb.Metric_Gauge{Gauge: &metricspb.Gauge{
				DataPoints: []*metricspb.NumberDataPoint{{
					TimeUnixNano: nowNs,
					Value:        &metricspb.NumberDataPoint_AsDouble{AsDouble: c.topSQLElapsed[sq.sqlID]},
					Attributes: instAttr(
						kvStr("sql_id", sq.sqlID),
						kvStr("sql_text", sq.sqlText),
						kvStr("executions", strconv.FormatUint(c.topSQLExecs[sq.sqlID], 10)),
					),
				}},
			}},
		})
	}

	return out
}
