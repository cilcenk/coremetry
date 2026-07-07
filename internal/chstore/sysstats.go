package chstore

import (
	"context"
	"fmt"
	"log"
	"strings"
	"time"
)

// SystemStats is the meta-observability snapshot the /admin/stats
// page renders: today's KPIs, per-table storage, and a 30-day
// history bar chart. All data points are read from cheap sources —
// system.parts metadata for storage, the service_summary_5m
// aggregate MV for daily span / error rollups, and bounded recent
// scans for distinct service / operation counts. Designed to stay
// sub-second even at 40M traces / day.
type SystemStats struct {
	Snapshot  SystemSnapshot `json:"snapshot"`
	Tables    []TableStat    `json:"tables"`
	History   []DayStat      `json:"history"`
	Ingest    IngestRates    `json:"ingest"`
	Drops     IngestDrops    `json:"drops"`
	Health    SystemHealth   `json:"health"`
	Exemplars ExemplarIngest `json:"exemplars"`
	SpanLinks SpanLinkIngest `json:"spanLinks"`
}

// ExemplarIngest — the two OTLP metric-exemplar ingest totals (cumulative
// since process start, v0.8.328). DroppedNoTrace counts the require-trace-
// context policy gate (INTENTIONAL, like the pipeline drops — never in the
// loss alarm); buffer/write loss for accepted exemplars is visible through
// the exemplars consumer like every other signal. Populated by the API
// getSystemStats handler from the live Ingester atomics; GetSystemStats
// (CH-only) leaves it zero so chstore keeps no otlp dependency.
type ExemplarIngest struct {
	Ingested       int64 `json:"ingested"`
	DroppedNoTrace int64 `json:"droppedNoTrace"`
}

// SpanLinkIngest — the two OTel span-link ingest totals (cumulative since
// process start, v0.8.329). DroppedInvalid counts links whose linked trace
// id arrived empty/all-zero — MALFORMED per the OTel spec (link trace_id is
// required), so like the exemplar policy gate it's an intentional drop,
// never in the loss alarm. Populated by the API getSystemStats handler from
// the live Ingester atomics; GetSystemStats (CH-only) leaves it zero so
// chstore keeps no otlp dependency.
type SpanLinkIngest struct {
	Ingested       int64 `json:"ingested"`
	DroppedInvalid int64 `json:"droppedInvalid"`
}

// SystemHealth surfaces config/boot conditions that silently degrade reads, so
// the operator sees them on /admin/stats instead of debugging empty dashboards.
// v0.8.211.
type SystemHealth struct {
	// ExternalDistributedSpansUnset is true when `spans` is an external
	// Distributed table but COREMETRY_CH_CLUSTER_NAME is unset — so adaptDDL
	// can't rewrite MV bodies to FROM spans_local ON CLUSTER, their per-shard
	// insert trigger never fires, and every summary MV (service_summary_5m,
	// trace_service_index_5m, …) stays EMPTY → reads return no/partial results.
	ExternalDistributedSpansUnset bool `json:"externalDistributedSpansUnset"`
	// SuggestedClusterName is the cluster the external `spans` Distributed table
	// fans to (parsed from its engine def) — set COREMETRY_CH_CLUSTER_NAME to
	// this to make the MVs populate. Empty if unparseable.
	SuggestedClusterName string `json:"suggestedClusterName,omitempty"`
	// LockDegraded is true when COREMETRY_REDIS_URL was set (the operator wants
	// a distributed leader lock for multi-pod HA) but the Redis connection
	// failed, so the pod fell back to the always-leader Noop lock. In a
	// multi-pod deployment EVERY pod then becomes leader and background jobs
	// (alerts, notifications, topology aggregation, retention) run DUPLICATED.
	// Populated by the API getSystemStats handler (main.go knows the lock state).
	LockDegraded bool `json:"lockDegraded"`
	// ESQueryErrors — cumulative failed Elasticsearch queries since process
	// start (transport + non-2xx), when the logs backend is external ES.
	// Non-zero = check /admin/elastic → Recent query errors for the exact
	// requests Coremetry sent. Populated by the API getSystemStats handler
	// (chstore stays free of any logstore dependency). v0.8.230.
	ESQueryErrors int64 `json:"esQueryErrors,omitempty"`
}

// IngestDrops surfaces the in-process ingest data-loss counters (cumulative
// since process start) on /admin/stats — previously invisible: an operator
// could only see spans_dropped on /api/health and nothing about logs/metrics
// or write-path loss. Two loss classes per signal:
//   - QueueFull: the receiver buffer was full when the item arrived
//     (producer outran the CH writer — backpressure overflow).
//   - WriteFailed: the ClickHouse insert errored and the batch was dropped,
//     not retried (silent loss the flusher only logged before v0.8.x).
// Populated by the API getSystemStats handler from the live consumers;
// GetSystemStats (CH-only) leaves it zero so chstore keeps no otlp dependency.
type IngestDrops struct {
	SpansQueueFull     int64 `json:"spansQueueFull"`
	LogsQueueFull      int64 `json:"logsQueueFull"`
	MetricsQueueFull   int64 `json:"metricsQueueFull"`
	SpansWriteFailed   int64 `json:"spansWriteFailed"`
	LogsWriteFailed    int64 `json:"logsWriteFailed"`
	MetricsWriteFailed int64 `json:"metricsWriteFailed"`
	// Pipeline — records discarded by an operator-defined ingest rule
	// (drop / sample) before the consumer buffer (v0.8.282). These are
	// INTENTIONAL, not data loss, so the UI renders them separately from
	// the queue-full / write-failed counters and never in the loss alarm.
	// Populated by the API getSystemStats handler from the live Ingester.
	SpansPipeline   int64 `json:"spansPipeline"`
	LogsPipeline    int64 `json:"logsPipeline"`
	MetricsPipeline int64 `json:"metricsPipeline"`
}

type SystemSnapshot struct {
	Spans24h        uint64 `json:"spans24h"`
	Spans7d         uint64 `json:"spans7d"`
	SpansAllTime    uint64 `json:"spansAllTime"`
	Errors24h       uint64 `json:"errors24h"`
	Logs24h         uint64 `json:"logs24h"`
	LogsAllTime     uint64 `json:"logsAllTime"`
	Metrics24h      uint64 `json:"metrics24h"`
	MetricsAllTime  uint64 `json:"metricsAllTime"`
	Profiles24h     uint64 `json:"profiles24h"`
	ProfilesAllTime uint64 `json:"profilesAllTime"`
	Services24h     uint64 `json:"services24h"`
	Operations24h   uint64 `json:"operations24h"`
	TotalDiskBytes  uint64 `json:"totalDiskBytes"`
}

type TableStat struct {
	Table            string `json:"table"`
	Rows             uint64 `json:"rows"`
	BytesOnDisk      uint64 `json:"bytesOnDisk"`
	CompressedBytes  uint64 `json:"compressedBytes"`
	UncompressedBytes uint64 `json:"uncompressedBytes"`
	Parts            uint32 `json:"parts"`
	OldestNs         int64  `json:"oldestNs"`
	NewestNs         int64  `json:"newestNs"`
}

// DayStat is one bucket in the 30-day history chart. Spans / errors
// come from service_summary_5m (5-minute rollups summed over the
// day), traces from trace_summary_1d (HLL-state per day), so we
// never re-aggregate the raw spans table for this view.
type DayStat struct {
	Day      string `json:"day"`
	Spans    uint64 `json:"spans"`
	Errors   uint64 `json:"errors"`
	Traces   uint64 `json:"traces"`   // approximate, HLL-merged from trace_summary_1d
	Services uint64 `json:"services"` // distinct service_names that contributed that day
}

// IngestRates is the live "what's happening right now" view —
// last 5 minutes per signal kind, expressed as items / second.
type IngestRates struct {
	SpansPerSec   float64 `json:"spansPerSec"`
	LogsPerSec    float64 `json:"logsPerSec"`
	MetricsPerSec float64 `json:"metricsPerSec"`
}

// GetSystemStats returns the full meta-observability payload. All
// queries are independent so we run them serially with bounded SQL
// — no fan-out goroutines: the calling HTTP handler caches the
// result for 60s, so the full one-shot cost is amortised cheaply.
func (s *Store) GetSystemStats(ctx context.Context) (*SystemStats, error) {
	out := &SystemStats{}

	// v0.5.319 — Operator-reported: prod returned "Failed to
	// load system stats" because ANY query failure here
	// propagated to the handler. Each panel now soft-fails: if
	// its CH query errors / times out, the field stays zero or
	// the slice empty, but the page renders. Operator sees the
	// panels that succeeded + a 0 where CH couldn't finish in
	// the budget, instead of a blanket error card.

	// v0.8.211 — empty-MV-risk health flag (soft, best-effort): external
	// Distributed spans + cluster_name unset = MVs never populate.
	if s.spansIsExternalDistributed(ctx) {
		out.Health.ExternalDistributedSpansUnset = true
		out.Health.SuggestedClusterName = s.discoverSpansCluster(ctx)
	}

	// ── Storage (system.parts is metadata-only, instant) ────────
	// v0.8.165 — system.parts is a LOCAL system table. In an external
	// Distributed cluster the storage lives on per-shard <table>_local
	// tables, so a plain read sees only the connected shard (≈1/N of the
	// cluster) AND labels every row <table>_local — which made the
	// all-time switch below (it matches the BARE names) read 0. When
	// cluster_name is set we fan the read across one replica per shard via
	// cluster() for the true cluster total; without it (operator-managed
	// external cluster, name unset) we read the connected shard. Either
	// way we normalise the _local suffix so the labels + all-time counts
	// resolve to the bare signal names.
	partsSource := "system.parts"
	if cn := strings.TrimSpace(s.cfg.ClusterName); cn != "" {
		partsSource = "cluster('" + cn + "', system.parts)"
	}
	rows, err := s.conn.Query(ctx, fmt.Sprintf(`
		SELECT
		  table,
		  sum(rows)                       AS rows,
		  sum(bytes_on_disk)              AS bytes_on_disk,
		  sum(data_compressed_bytes)      AS compressed,
		  sum(data_uncompressed_bytes)    AS uncompressed,
		  toUInt32(count())               AS parts,
		  toUnixTimestamp64Nano(toDateTime64(min(min_time), 9)) AS oldest_ns,
		  toUnixTimestamp64Nano(toDateTime64(max(max_time), 9)) AS newest_ns
		FROM %s
		WHERE database = currentDatabase()
		  AND active = 1
		  AND table NOT LIKE '.inner%%'
		GROUP BY table
		ORDER BY bytes_on_disk DESC
		SETTINGS max_execution_time = 8`, partsSource))
	if err == nil {
		for rows.Next() {
			var t TableStat
			if err := rows.Scan(&t.Table, &t.Rows, &t.BytesOnDisk,
				&t.CompressedBytes, &t.UncompressedBytes, &t.Parts,
				&t.OldestNs, &t.NewestNs); err != nil {
				break
			}
			// Distributed shards store under <table>_local; normalise to the
			// bare name so labels read spans/logs/… and the all-time switch
			// matches (otherwise SpansAllTime/… stay 0 on a distributed read).
			t.Table = normalizeStorageTableName(t.Table)
			out.Tables = append(out.Tables, t)
			out.Snapshot.TotalDiskBytes += t.BytesOnDisk
		}
		rows.Close()
	} else {
		log.Printf("[sysstats] storage query: %v — surfacing dashboard with empty Tables", err)
	}

	// ── Span / error counts via the 5m aggregate MV ─────────────
	// countMerge over AggregateFunction state is cheap; partition
	// pruning + LowCardinality grouping keeps this sub-second on
	// 30 days of demo data and stays bounded at 40M traces / day.
	_ = s.conn.QueryRow(ctx, `
		SELECT countMerge(span_count_state),
		       countMerge(error_count_state)
		FROM service_summary_5m
		WHERE time_bucket >= now() - toIntervalDay(1)
		SETTINGS max_execution_time = 5`).
		Scan(&out.Snapshot.Spans24h, &out.Snapshot.Errors24h)
	_ = s.conn.QueryRow(ctx, `
		SELECT countMerge(span_count_state)
		FROM service_summary_5m
		WHERE time_bucket >= now() - toIntervalDay(7)
		SETTINGS max_execution_time = 5`).
		Scan(&out.Snapshot.Spans7d)

	// All-time spans is the table-level row count from system.parts
	// — already collected (and _local-normalised above). Same for logs /
	// metrics / profiles.
	out.Snapshot.SpansAllTime, out.Snapshot.LogsAllTime,
		out.Snapshot.MetricsAllTime, out.Snapshot.ProfilesAllTime =
		allTimeRowCounts(out.Tables)

	// v0.5.319 — Operator-reported: System page "What's inside"
	// loaded glacially at production scale. Root cause: every
	// QueryRow below was unbounded (no max_execution_time) and
	// they ran SEQUENTIALLY in this function. A naked count() on
	// a billion-row logs / metric_points table at peak ingest
	// pegged the handler waiting for each one. Bounded + softer
	// fallback below: ignore the count error so the dashboard
	// renders with zero for that field rather than hanging.
	//
	// 8s ceiling per query — generous enough that idle clusters
	// finish naturally, tight enough that the System page never
	// blocks a tab for >>8s on any single signal.
	_ = s.conn.QueryRow(ctx,
		`SELECT count() FROM logs WHERE time >= now() - toIntervalDay(1)
		 SETTINGS max_execution_time = 8`).
		Scan(&out.Snapshot.Logs24h)
	_ = s.conn.QueryRow(ctx,
		`SELECT count() FROM metric_points WHERE time >= now() - toIntervalDay(1)
		 SETTINGS max_execution_time = 8`).
		Scan(&out.Snapshot.Metrics24h)
	_ = s.conn.QueryRow(ctx,
		`SELECT count() FROM profiles WHERE start_time >= now() - toIntervalDay(1)
		 SETTINGS max_execution_time = 8`).
		Scan(&out.Snapshot.Profiles24h)

	// Distinct services / operations over the last 24h. uniq is HLL
	// — bounded memory, negligible cost on LowCardinality columns.
	_ = s.conn.QueryRow(ctx, `
		SELECT uniq(service_name)
		FROM service_summary_5m
		WHERE time_bucket >= now() - toIntervalDay(1)
		SETTINGS max_execution_time = 8`).
		Scan(&out.Snapshot.Services24h)
	_ = s.conn.QueryRow(ctx, `
		SELECT uniq(name)
		FROM spans
		WHERE time >= now() - toIntervalDay(1)
		SETTINGS max_execution_time = 8`).
		Scan(&out.Snapshot.Operations24h)

	// ── 30-day history (per-day spans / errors / traces / services) ──
	// LEFT JOIN trace_summary_1d so days without distinct-trace
	// data (MV not populated yet) still appear with traces=0.
	// v0.5.319 — bounded + soft-fail. Heaviest query in this
	// function; if it can't finish, the dashboard renders
	// without the history strip rather than failing the whole
	// page.
	histRows, err := s.conn.Query(ctx, `
		WITH spans_daily AS (
		  SELECT
		    toDate(time_bucket)            AS day,
		    countMerge(span_count_state)   AS spans,
		    countMerge(error_count_state)  AS errors,
		    uniq(service_name)             AS services
		  FROM service_summary_5m
		  WHERE time_bucket >= now() - toIntervalDay(30)
		  GROUP BY day
		),
		traces_daily AS (
		  SELECT day, uniqMerge(trace_count_state) AS traces
		  FROM trace_summary_1d
		  WHERE day >= today() - 30
		  GROUP BY day
		)
		SELECT s.day, s.spans, s.errors, ifNull(t.traces, 0), s.services
		FROM spans_daily s
		LEFT JOIN traces_daily t ON s.day = t.day
		ORDER BY s.day
		SETTINGS max_execution_time = 12`)
	if err == nil {
		for histRows.Next() {
			var d DayStat
			var t time.Time
			if err := histRows.Scan(&t, &d.Spans, &d.Errors, &d.Traces, &d.Services); err != nil {
				break
			}
			d.Day = t.Format("2006-01-02")
			out.History = append(out.History, d)
		}
		histRows.Close()
	} else {
		log.Printf("[sysstats] 30-day history query: %v — dashboard renders without history strip", err)
	}

	// ── Live ingest rates (last 5 min, items / sec) ─────────────
	// v0.5.319 — same 5s bound + soft-fail as the 24h panels.
	_ = s.conn.QueryRow(ctx,
		`SELECT count() / 300.0 FROM spans WHERE time >= now() - toIntervalMinute(5)
		 SETTINGS max_execution_time = 5`).
		Scan(&out.Ingest.SpansPerSec)
	_ = s.conn.QueryRow(ctx,
		`SELECT count() / 300.0 FROM logs WHERE time >= now() - toIntervalMinute(5)
		 SETTINGS max_execution_time = 5`).
		Scan(&out.Ingest.LogsPerSec)
	_ = s.conn.QueryRow(ctx,
		`SELECT count() / 300.0 FROM metric_points WHERE time >= now() - toIntervalMinute(5)
		 SETTINGS max_execution_time = 5`).
		Scan(&out.Ingest.MetricsPerSec)

	// v0.5.319 — always return (out, nil). Any partial-result
	// scenario (a single query timing out at scale) leaves its
	// field at zero. The dashboard renders; the operator sees
	// the panels that succeeded. The previous "return out, err"
	// path emitted 500 on the slightest CH hiccup and the
	// frontend rendered the bare "Failed to load system stats"
	// Empty card — far less useful than partial data.
	return out, nil
}

// normalizeStorageTableName maps a system.parts table label to the bare
// signal name. In an external Distributed cluster the actual storage is
// on <table>_local shards, so system.parts reports "spans_local" etc.;
// stripping the suffix lets the storage labels + the all-time row-count
// switch resolve to spans/logs/metric_points/profiles. The inverse of
// LocalTableName(). No-op on a single-node install (bare names already).
func normalizeStorageTableName(table string) string {
	return strings.TrimSuffix(table, "_local")
}

// allTimeRowCounts pulls the per-signal all-time row totals out of the
// (already _local-normalised) storage table stats. Pure so the
// distributed _local-label regression is unit-tested without a live CH.
func allTimeRowCounts(tables []TableStat) (spans, logs, metrics, profiles uint64) {
	for _, t := range tables {
		switch t.Table {
		case "spans":
			spans = t.Rows
		case "logs":
			logs = t.Rows
		case "metric_points":
			metrics = t.Rows
		case "profiles":
			profiles = t.Rows
		}
	}
	return
}
