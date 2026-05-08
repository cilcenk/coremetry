package chstore

import (
	"context"
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
// day), so we never re-aggregate the raw spans table for this view.
type DayStat struct {
	Day      string `json:"day"`
	Spans    uint64 `json:"spans"`
	Errors   uint64 `json:"errors"`
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
		ORDER BY bytes_on_disk DESC`)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var t TableStat
		if err := rows.Scan(&t.Table, &t.Rows, &t.BytesOnDisk,
			&t.CompressedBytes, &t.UncompressedBytes, &t.Parts,
			&t.OldestNs, &t.NewestNs); err != nil {
			rows.Close()
			return nil, err
		}
		out.Tables = append(out.Tables, t)
		out.Snapshot.TotalDiskBytes += t.BytesOnDisk
	}
	rows.Close()

	// ── Span / error counts via the 5m aggregate MV ─────────────
	// countMerge over AggregateFunction state is cheap; partition
	// pruning + LowCardinality grouping keeps this sub-second on
	// 30 days of demo data and stays bounded at 40M traces / day.
	_ = s.conn.QueryRow(ctx, `
		SELECT countMerge(span_count_state),
		       countMerge(error_count_state)
		FROM service_summary_5m
		WHERE time_bucket >= now() - toIntervalDay(1)`).
		Scan(&out.Snapshot.Spans24h, &out.Snapshot.Errors24h)
	_ = s.conn.QueryRow(ctx, `
		SELECT countMerge(span_count_state)
		FROM service_summary_5m
		WHERE time_bucket >= now() - toIntervalDay(7)`).
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

	// 24h volumes for logs / metrics / profiles via cheap counts.
	_ = s.conn.QueryRow(ctx,
		`SELECT count() FROM logs WHERE time >= now() - toIntervalDay(1)`).
		Scan(&out.Snapshot.Logs24h)
	_ = s.conn.QueryRow(ctx,
		`SELECT count() FROM metric_points WHERE time >= now() - toIntervalDay(1)`).
		Scan(&out.Snapshot.Metrics24h)
	_ = s.conn.QueryRow(ctx,
		`SELECT count() FROM profiles WHERE start_time >= now() - toIntervalDay(1)`).
		Scan(&out.Snapshot.Profiles24h)

	// Distinct services / operations over the last 24h. uniq is HLL
	// — bounded memory, negligible cost on LowCardinality columns.
	_ = s.conn.QueryRow(ctx, `
		SELECT uniq(service_name)
		FROM service_summary_5m
		WHERE time_bucket >= now() - toIntervalDay(1)`).
		Scan(&out.Snapshot.Services24h)
	_ = s.conn.QueryRow(ctx, `
		SELECT uniq(name)
		FROM spans
		WHERE time >= now() - toIntervalDay(1)`).
		Scan(&out.Snapshot.Operations24h)

	// ── 30-day history (per-day spans / errors / services) ──────
	histRows, err := s.conn.Query(ctx, `
		SELECT
		  toDate(time_bucket)            AS day,
		  countMerge(span_count_state)   AS spans,
		  countMerge(error_count_state)  AS errors,
		  uniq(service_name)             AS services
		FROM service_summary_5m
		WHERE time_bucket >= now() - toIntervalDay(30)
		GROUP BY day
		ORDER BY day`)
	if err != nil {
		return out, err
	}
	for histRows.Next() {
		var d DayStat
		var t time.Time
		if err := histRows.Scan(&t, &d.Spans, &d.Errors, &d.Services); err != nil {
			histRows.Close()
			return nil, err
		}
		d.Day = t.Format("2006-01-02")
		out.History = append(out.History, d)
	}
	histRows.Close()

	// ── Live ingest rates (last 5 min, items / sec) ─────────────
	_ = s.conn.QueryRow(ctx,
		`SELECT count() / 300.0 FROM spans WHERE time >= now() - toIntervalMinute(5)`).
		Scan(&out.Ingest.SpansPerSec)
	_ = s.conn.QueryRow(ctx,
		`SELECT count() / 300.0 FROM logs WHERE time >= now() - toIntervalMinute(5)`).
		Scan(&out.Ingest.LogsPerSec)
	_ = s.conn.QueryRow(ctx,
		`SELECT count() / 300.0 FROM metric_points WHERE time >= now() - toIntervalMinute(5)`).
		Scan(&out.Ingest.MetricsPerSec)

	return out, nil
}
