package chstore

import (
	"context"
	"log"
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
	Snapshot SystemSnapshot `json:"snapshot"`
	Tables   []TableStat    `json:"tables"`
	History  []DayStat      `json:"history"`
	Ingest   IngestRates    `json:"ingest"`
	Drops    IngestDrops    `json:"drops"`
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

	// ── Storage (system.parts is metadata-only, instant) ────────
	rows, err := s.conn.Query(ctx, `
		SELECT
		  table,
		  sum(rows)                       AS rows,
		  sum(bytes_on_disk)              AS bytes_on_disk,
		  sum(data_compressed_bytes)      AS compressed,
		  sum(data_uncompressed_bytes)    AS uncompressed,
		  toUInt32(count())               AS parts,
		  toUnixTimestamp64Nano(toDateTime64(min(min_time), 9)) AS oldest_ns,
		  toUnixTimestamp64Nano(toDateTime64(max(max_time), 9)) AS newest_ns
		FROM system.parts
		WHERE database = currentDatabase()
		  AND active = 1
		  AND table NOT LIKE '.inner%'
		GROUP BY table
		ORDER BY bytes_on_disk DESC
		SETTINGS max_execution_time = 8`)
	if err == nil {
		for rows.Next() {
			var t TableStat
			if err := rows.Scan(&t.Table, &t.Rows, &t.BytesOnDisk,
				&t.CompressedBytes, &t.UncompressedBytes, &t.Parts,
				&t.OldestNs, &t.NewestNs); err != nil {
				break
			}
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
	// — already collected. Same for logs / metrics / profiles.
	for _, t := range out.Tables {
		switch t.Table {
		case "spans":
			out.Snapshot.SpansAllTime = t.Rows
		case "logs":
			out.Snapshot.LogsAllTime = t.Rows
		case "metric_points":
			out.Snapshot.MetricsAllTime = t.Rows
		case "profiles":
			out.Snapshot.ProfilesAllTime = t.Rows
		}
	}

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
