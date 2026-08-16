package chstore

import (
	"context"
	"time"
)

// CardinalityReport is the meta-observability "what's eating my CH"
// view. Surfaces the top emitters across three axes — service,
// metric name, attribute key — plus per-column storage attribution
// from system.columns. Powers /admin/cardinality.
//
// Why each section matters:
//
//   - Services: when a single service starts emitting 10x its usual
//     span rate (deploy gone wrong, infinite retry loop) it
//     surfaces here days before the disk fills up.
//
//   - Metrics: a misconfigured high-frequency metric can dominate
//     the metric_points table. This panel makes the offender
//     obvious so the admin knows which counter to drop.
//
//   - Attribute keys: unbounded label values (raw user-id, full URL
//     path with query string, request ID embedded as label) blow
//     up cardinality silently. The "distinct" column flags these
//     immediately.
//
//   - Columns: confirms the actual disk distribution post-
//     compression. ColumnA can be 10x more rows than ColumnB but
//     compress 50x better — only system.columns tells the truth.
type CardinalityReport struct {
	Services    []TopRow     `json:"services"` // top services by 24h span count
	Metrics     []TopRow     `json:"metrics"`  // top metrics by 24h point count
	AttrKeys    []AttrKeyRow `json:"attrKeys"` // top attribute keys by cardinality
	Columns     []ColumnRow  `json:"columns"`  // top columns by compressed bytes
	GeneratedAt int64        `json:"generatedAt"`
}

type TopRow struct {
	Name string `json:"name"`
	Rows uint64 `json:"rows"`
}

type AttrKeyRow struct {
	Key            string `json:"key"`
	DistinctValues uint64 `json:"distinctValues"`
	Occurrences    uint64 `json:"occurrences"`
	// Source labels which CH table the row was sampled from
	// (spans / logs / metric_points). Lets the admin grep the
	// emitting service for the offending label.
	Source string `json:"source"`
}

type ColumnRow struct {
	Table             string  `json:"table"`
	Column            string  `json:"column"`
	CompressedBytes   uint64  `json:"compressedBytes"`
	UncompressedBytes uint64  `json:"uncompressedBytes"`
	CompressionRatio  float64 `json:"compressionRatio"`
}

// GetCardinality runs four bounded queries serially. The HTTP
// handler caches the result for 5 minutes so the operator can
// refresh the page without hammering CH; sub-second cold cost on
// a typical CH at 100M spans/day, ~5s at 1B+ (system.columns is
// metadata-fast either way; the sampled scans dominate).
//
// Sampling caps:
//   - Service / metric counts: full 24h scan, partition-pruned by
//     toDate — cheap enough not to bother sampling.
//   - Attribute keys: LIMIT 100k rows of recent data, then
//     arrayJoin. 100k × ~20 attrs/row = 2M rows post-explode,
//     uniqExact handles that sub-second.
func (s *Store) GetCardinality(ctx context.Context) (*CardinalityReport, error) {
	// Initialise every slice to empty (not nil) so Go marshals
	// to `[]` rather than `null` when a sub-query fails or
	// returns no rows. The frontend defensively handles `null`
	// too, but `[]` is the correct on-the-wire shape and
	// avoids crashing any consumer that doesn't expect null.
	out := &CardinalityReport{
		Services:    []TopRow{},
		Metrics:     []TopRow{},
		AttrKeys:    []AttrKeyRow{},
		Columns:     []ColumnRow{},
		GeneratedAt: time.Now().UnixNano(),
	}

	// ── Top services (24h span count) ─────────────────────────
	if rows, err := s.conn.Query(ctx, `
		SELECT service_name AS name, count() AS rows
		FROM spans
		WHERE time >= now() - INTERVAL 24 HOUR
		  AND service_name != ''
		GROUP BY service_name
		ORDER BY rows DESC
		LIMIT 30
		SETTINGS max_execution_time = 25`); err == nil {
		for rows.Next() {
			var r TopRow
			if rows.Scan(&r.Name, &r.Rows) == nil {
				out.Services = append(out.Services, r)
			}
		}
		rows.Close()
	}

	// ── Top metrics (24h point count) ─────────────────────────
	if rows, err := s.conn.Query(ctx, `
		SELECT metric AS name, count() AS rows
		FROM metric_points
		WHERE time >= now() - INTERVAL 24 HOUR
		  AND metric != ''
		GROUP BY metric
		ORDER BY rows DESC
		LIMIT 30
		SETTINGS max_execution_time = 25`); err == nil {
		for rows.Next() {
			var r TopRow
			if rows.Scan(&r.Name, &r.Rows) == nil {
				out.Metrics = append(out.Metrics, r)
			}
		}
		rows.Close()
	}

	// ── Top attribute keys (sampled cardinality from spans) ──
	// We sample the most recent 100k spans, ARRAY JOIN over the
	// (attr_keys, attr_values) pair-arrays, then uniqExact the
	// distinct values per key. uniqExact > uniq because at this
	// scale the HLL approximation can hide a label that
	// transitioned from "controlled set" to "unbounded values"
	// just below the alarm threshold — false negatives are
	// expensive here.
	if rows, err := s.conn.Query(ctx, `
		WITH sampled AS (
		  SELECT attr_keys, attr_values
		  FROM spans
		  WHERE time >= now() - INTERVAL 1 HOUR
		  LIMIT 100000
		),
		exploded AS (
		  SELECT k AS key, v AS value
		  FROM sampled
		  ARRAY JOIN attr_keys AS k, attr_values AS v
		)
		SELECT key,
		       uniqExact(value) AS distinct_values,
		       count()          AS occurrences
		FROM exploded
		GROUP BY key
		ORDER BY distinct_values DESC
		LIMIT 30
		SETTINGS max_execution_time = 25`); err == nil {
		for rows.Next() {
			var r AttrKeyRow
			if rows.Scan(&r.Key, &r.DistinctValues, &r.Occurrences) == nil {
				r.Source = "spans"
				out.AttrKeys = append(out.AttrKeys, r)
			}
		}
		rows.Close()
	}

	// ── Top columns by compressed bytes (system.columns) ─────
	if rows, err := s.conn.Query(ctx, `
		SELECT
		  table,
		  name AS column,
		  sum(data_compressed_bytes)   AS compressed,
		  sum(data_uncompressed_bytes) AS uncompressed
		FROM system.columns
		WHERE database = currentDatabase()
		  AND table NOT LIKE '.inner%'
		GROUP BY table, name
		ORDER BY compressed DESC
		LIMIT 30
		SETTINGS max_execution_time = 25`); err == nil {
		for rows.Next() {
			var r ColumnRow
			if rows.Scan(&r.Table, &r.Column, &r.CompressedBytes, &r.UncompressedBytes) == nil {
				if r.CompressedBytes > 0 {
					r.CompressionRatio = float64(r.UncompressedBytes) / float64(r.CompressedBytes)
				}
				out.Columns = append(out.Columns, r)
			}
		}
		rows.Close()
	}

	return out, nil
}
