// oracle_metrics.go — synthetic OracleDB-receiver metric emission.
//
// The demo emits Oracle DB *spans* (oraDB, peer.service=oracle) but the
// /databases page's OracleDB-receiver drill-down panel reads oracledb.*
// *metric_points* via chstore.GetOracleMetrics. Without those points the
// panel renders "no data" and no source="receiver" Oracle row appears in
// the DB-instances list.
//
// This file plays the role the real OpenTelemetry `oracledb` receiver
// would: it scrapes nothing, but it emits the exact oracledb.* instrument
// shapes that the read contract in internal/chstore/oracle.go +
// internal/chstore/dependencies.go (discoverReceiverInstances) expect:
//
//   - Each datapoint carries an `instance` attribute. discoverReceiverInstances
//     coalesces the receiver-row instance label from that attr (2nd in its
//     coalesce list), and GetOracleMetrics filters on the same attr — so a
//     single `instance` value BOTH creates the receiver row AND scopes every
//     drill-down read to it.
//   - Resource service.name = "oracledb-receiver" (a synthetic scraper
//     identity, not one of the demo's app services). The receiver-row query
//     keys on the `instance` attr, not service.name, so this name stays out
//     of the way; it only shows up as the scope/owner of the points.
//   - Gauges (oracledb.sessions.usage/limit, processes.usage/limit,
//     pga_memory, sga_max_size, tablespace_size.usage/limit) are emitted as
//     Gauge instruments — GetOracleMetrics reads them with argMax(value,time).
//   - Counters (cpu_time, logical_reads, physical_reads, hard_parses,
//     parse_calls, executions, user_commits, user_rollbacks, transactions,
//     wait_time.<class>) are emitted as monotonic cumulative Sums whose value
//     INCREASES every flush. GetOracleMetrics derives per-second rates as
//     (max-min)/window, so the counter must keep climbing across the run —
//     hence the per-instance state held in oracleReceiverState.
//
// Emitted once per metric flush (the existing 10s metrics tick), for BOTH
// instances (corebank-scan.prod RAC SCAN + corebank-dg.prod Data Guard
// standby) so both receiver rows populate.

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

// Oracle instance labels carried on every oracledb.* datapoint's `instance`
// attribute. These match the host portion of the oraDB span server.address
// values (corebank-scan.prod:1521 / corebank-dg.prod:1521) without the port,
// so the operator's eye maps the receiver row onto the same DB they see in
// the span-derived topology.
var oracleInstances = []string{"corebank-scan.prod", "corebank-dg.prod"}

// Standard Oracle wait classes (V$SYSTEM_WAIT_CLASS). Each becomes a
// oracledb.wait_time.<class> cumulative counter; GetOracleMetrics strips the
// prefix to recover the bare class name.
var oracleWaitClasses = []string{
	"user_io", "system_io", "concurrency", "commit",
	"network", "application", "configuration", "scheduler",
	"administrative", "other",
}

// oracleTablespaces is the per-tablespace size table dimensioned by the
// tablespace_name attribute on oracledb.tablespace_size.usage/.limit.
// usedFrac is the steady-state fill ratio; limitBytes the configured max.
var oracleTablespaces = []struct {
	name      string
	usedFrac  float64
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
var oracleTopSQL = []struct {
	sqlID          string
	sqlText        string
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
	cpuTime       float64 // seconds
	logicalReads  float64
	physicalReads float64
	hardParses    float64
	parseCalls    float64
	executions    float64
	userCommits   float64
	userRollbacks float64
	transactions  float64
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

	var rms []*metricspb.ResourceMetrics
	for _, instance := range oracleInstances {
		c := oracleRX.get(instance)
		// Advance every cumulative counter by a realistic per-flush delta
		// (jittered) so the rate read is non-zero and varies. The standby
		// (corebank-dg.prod) runs lighter than the RAC SCAN primary.
		load := 1.0
		if instance == "corebank-dg.prod" {
			load = 0.25 // Data Guard standby: read-only, lighter
		}
		advanceOracleCounters(c, load)

		metrics := buildOracleMetrics(instance, c, startNs, nowNs, load)
		rms = append(rms, &metricspb.ResourceMetrics{
			Resource: &resourcepb.Resource{Attributes: []*commonpb.KeyValue{
				kvStr("service.name", oracleReceiverService),
				kvStr("host.name", instance),
				kvStr("deployment.environment", "demo"),
				// db.system on the resource mirrors the real oracledb
				// receiver's resource shape; not read by the contract but
				// keeps the points self-describing.
				kvStr("db.system", "oracle"),
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
// read positive and lifelike across the run.
func advanceOracleCounters(c *oracleCounters, load float64) {
	jit := func(base float64) float64 { return base * load * (0.8 + mrand.Float64()*0.4) }

	c.cpuTime += jit(2.5)            // ~2.5 CPU-sec per 10s flush at full load
	c.logicalReads += jit(370_000)  // ~37k logical reads/sec
	c.physicalReads += jit(4_200)   // ~420 physical reads/sec → ~98.9% cache hit
	c.hardParses += jit(180)
	c.parseCalls += jit(9_500)
	c.executions += jit(46_000)
	c.userCommits += jit(3_100)
	c.userRollbacks += jit(120)
	c.transactions += jit(3_220)

	for _, wc := range oracleWaitClasses {
		// Per-class weight so the distribution looks like a real DB:
		// user_io + commit dominate, scheduler/administrative are tiny.
		w := oracleWaitClassWeight(wc)
		c.waitTime[wc] += jit(w)
	}

	for _, sq := range oracleTopSQL {
		c.topSQLElapsed[sq.sqlID] += sq.elapsedPerFlush * load * (0.85 + mrand.Float64()*0.3)
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
// cumulative total. Every datapoint carries the `instance` attribute.
func buildOracleMetrics(instance string, c *oracleCounters, startNs, nowNs uint64, load float64) []*metricspb.Metric {
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

	// ── Session / process gauges + caps ──────────────────────────────────────
	// usage jitters under a fixed limit so the progress bar moves.
	sessLimit := 600.0
	sessUsage := 120.0 + 220.0*load*(0.7+mrand.Float64()*0.5)
	if sessUsage > sessLimit {
		sessUsage = sessLimit
	}
	procLimit := 800.0
	procUsage := 150.0 + 260.0*load*(0.7+mrand.Float64()*0.5)
	if procUsage > procLimit {
		procUsage = procLimit
	}
	out = append(out,
		gauge("oracledb.sessions.usage", "{session}", "Active + inactive sessions", sessUsage, instAttr()),
		gauge("oracledb.sessions.limit", "{session}", "Maximum session limit", sessLimit, instAttr()),
		gauge("oracledb.processes.usage", "{process}", "Current processes", procUsage, instAttr()),
		gauge("oracledb.processes.limit", "{process}", "Maximum process limit", procLimit, instAttr()),
	)

	// Sessions active/inactive split — keyed on the `status` attribute,
	// matching queryOracleSessionsByStatus. Active ~ 35-55% of usage.
	active := sessUsage * (0.35 + mrand.Float64()*0.2)
	inactive := sessUsage - active
	out = append(out,
		gauge("oracledb.sessions", "{session}", "Sessions by status", active,
			instAttr(kvStr("status", "active"))),
		gauge("oracledb.sessions", "{session}", "Sessions by status", inactive,
			instAttr(kvStr("status", "inactive"))),
	)

	// ── Memory gauges ────────────────────────────────────────────────────────
	pga := 2.4*1024*1024*1024 + load*mrand.Float64()*512*1024*1024
	sga := 8.0 * 1024 * 1024 * 1024 // SGA max is a fixed allocation
	out = append(out,
		gauge("oracledb.pga_memory", "By", "PGA memory in use", pga, instAttr()),
		gauge("oracledb.sga_max_size", "By", "SGA maximum size", sga, instAttr()),
	)

	// ── Tablespace size gauges (dimensioned by tablespace_name) ───────────────
	for _, ts := range oracleTablespaces {
		used := ts.limitBytes * ts.usedFrac * (0.97 + mrand.Float64()*0.06)
		if used > ts.limitBytes {
			used = ts.limitBytes
		}
		out = append(out,
			gauge("oracledb.tablespace_size.usage", "By", "Tablespace bytes used",
				used, instAttr(kvStr("tablespace_name", ts.name))),
			gauge("oracledb.tablespace_size.limit", "By", "Tablespace max bytes",
				ts.limitBytes, instAttr(kvStr("tablespace_name", ts.name))),
		)
	}

	// ── Cumulative counters (monotonic sums) ─────────────────────────────────
	out = append(out,
		sum("oracledb.cpu_time", "s", "Cumulative DB CPU time", c.cpuTime, instAttr()),
		sum("oracledb.logical_reads", "{read}", "Cumulative logical reads", c.logicalReads, instAttr()),
		sum("oracledb.physical_reads", "{read}", "Cumulative physical reads", c.physicalReads, instAttr()),
		sum("oracledb.hard_parses", "{parse}", "Cumulative hard parses", c.hardParses, instAttr()),
		sum("oracledb.parse_calls", "{parse}", "Cumulative parse calls", c.parseCalls, instAttr()),
		sum("oracledb.executions", "{execution}", "Cumulative SQL executions", c.executions, instAttr()),
		sum("oracledb.user_commits", "{commit}", "Cumulative user commits", c.userCommits, instAttr()),
		sum("oracledb.user_rollbacks", "{rollback}", "Cumulative user rollbacks", c.userRollbacks, instAttr()),
		sum("oracledb.transactions", "{transaction}", "Cumulative transactions", c.transactions, instAttr()),
	)

	// ── Wait-class cumulative counters ────────────────────────────────────────
	for _, wc := range oracleWaitClasses {
		out = append(out, sum("oracledb.wait_time."+wc, "s",
			"Cumulative time waited in the "+wc+" class", c.waitTime[wc], instAttr()))
	}

	// ── Top SQL by elapsed (sql_id / sql_text / executions attrs) ─────────────
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
