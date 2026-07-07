package chstore

// OTel span links — v0.8.329, cross-signal pivot Phase 1b.
//
// This file owns the write + read paths for the `span_links` table and its
// reverse-PK copy `span_links_reverse` (populated by span_links_reverse_mv,
// never written directly — see the DDL in store.go). Until v0.8.329
// convertSpan dropped sp.Links entirely (pivot-audit §2), so span→related-
// trace traversal was structural parent/child only.
//
// Read shape by construction (the operator-approved Phase 1b storage call):
// both pivot directions are point-lookups by ONE trace id, so each direction
// gets its own table with that id as the ORDER BY prefix —
//   - LinksFromTrace: "what does this trace link TO" — span_links,
//     WHERE trace_id = ? — a pure primary-key scan.
//   - LinksToTrace: "what links TO this trace" (backlinks) — span_links_
//     reverse, WHERE linked_trace_id = ? — the reverse direction as its own
//     PK, no full scan, no JOIN. A nested column on `spans` was rejected in
//     the audit: the reverse direction would need a spans full-scan or a
//     separate index table anyway, and link rows are ~1-5% of span volume —
//     cheap to duplicate.

import (
	"context"
	"fmt"
	"strings"
)

// SpanLink is one span-link row as served to the pivot read path (both
// directions project the same columns).
type SpanLink struct {
	TraceID       string            `json:"traceId"`
	SpanID        string            `json:"spanId"`
	LinkedTraceID string            `json:"linkedTraceId"`
	LinkedSpanID  string            `json:"linkedSpanId"`
	TimeUnixNs    int64             `json:"timeUnixNs"`
	ServiceName   string            `json:"serviceName"`
	Attrs         map[string]string `json:"attrs,omitempty"`
}

const (
	// spanLinkDefaultLimit — a trace view shows linked traces as a short
	// list; 100 across both directions is generous (a trace declaring more
	// links than that is a batch consumer, where the FIRST links carry the
	// signal).
	spanLinkDefaultLimit = 100
	// spanLinkMaxLimit caps caller-supplied limits (bounded read, house rule).
	spanLinkMaxLimit = 1000
)

func clampSpanLinkLimit(limit int) int {
	if limit <= 0 {
		return spanLinkDefaultLimit
	}
	if limit > spanLinkMaxLimit {
		return spanLinkMaxLimit
	}
	return limit
}

// spanLinksInsertSQL is the named column list of the span_links INSERT
// (v0.8.186 discipline: fails loudly on a stale schema instead of writing
// into the wrong column). Kept as a const so the alignment regression test
// can pin columns ↔ spanLinkAppendArgs positions. Only the FORWARD table is
// written — span_links_reverse fills via span_links_reverse_mv.
const spanLinksInsertSQL = `INSERT INTO span_links
	(trace_id, span_id, linked_trace_id, linked_span_id,
	 time, service_name, attr_keys, attr_values)`

// spanLinkAppendArgs emits one row's values in EXACTLY the column order of
// spanLinksInsertSQL (pinned by TestSpanLinksInsertAlignment). Nil attr
// arrays normalise to empty slices — Array columns reject nil.
func spanLinkAppendArgs(l *SpanLinkRow) []any {
	ak, av := l.AttrKeys, l.AttrVals
	if ak == nil {
		ak = []string{}
	}
	if av == nil {
		av = []string{}
	}
	return []any{
		l.TraceID, l.SpanID, l.LinkedTraceID, l.LinkedSpanID,
		l.Time, l.ServiceName, ak, av,
	}
}

// InsertSpanLinks is the batched span-link write — the flush function of the
// `span_links` consumer (main.go), riding the same asyncInsertCtx coalescing
// as every other ingest INSERT (v0.5.346 settings, untouched).
func (s *Store) InsertSpanLinks(ctx context.Context, rows []*SpanLinkRow) error {
	if len(rows) == 0 {
		return nil
	}
	ctx = asyncInsertCtx(ctx)
	batch, err := s.conn.PrepareBatch(ctx, spanLinksInsertSQL)
	if err != nil {
		return fmt.Errorf("prepare span_links: %w", err)
	}
	for _, l := range rows {
		if err := batch.Append(spanLinkAppendArgs(l)...); err != nil {
			return fmt.Errorf("append span_link: %w", err)
		}
	}
	return batch.Send()
}

// LinksFromTrace is the FORWARD pivot read: every link declared by the spans
// of one trace ("this trace links TO …"). WHERE trace_id = ? is a pure
// primary-key scan on span_links' ORDER BY (trace_id, time) — no time-window
// argument needed, the PK equality already prunes to the trace's granules.
func (s *Store) LinksFromTrace(ctx context.Context, traceID string, limit int) ([]SpanLink, error) {
	return s.queryLinks(ctx, "span_links", "trace_id", traceID, limit)
}

// LinksToTrace is the REVERSE pivot read: every link that points AT one
// trace ("… links TO this trace" — backlinks). Served by span_links_reverse,
// whose ORDER BY (linked_trace_id, time) makes THIS direction the primary-key
// scan; the forward table would need a table scan (bloom-assisted) for it.
func (s *Store) LinksToTrace(ctx context.Context, traceID string, limit int) ([]SpanLink, error) {
	return s.queryLinks(ctx, "span_links_reverse", "linked_trace_id", traceID, limit)
}

// queryLinks is the shared reader — the two tables carry identical columns,
// only the table and the PK-prefix column differ (both from the fixed pairs
// above, never caller input). LIMIT + max_execution_time per house rule.
func (s *Store) queryLinks(ctx context.Context, table, keyCol, traceID string, limit int) ([]SpanLink, error) {
	if strings.TrimSpace(traceID) == "" {
		return nil, fmt.Errorf("traceID is required")
	}
	rows, err := s.conn.Query(ctx, fmt.Sprintf(`
		SELECT trace_id, span_id, linked_trace_id, linked_span_id,
		       toUnixTimestamp64Nano(time) AS ts, service_name,
		       attr_keys, attr_values
		FROM %s
		WHERE %s = ?
		ORDER BY time
		LIMIT ?
		SETTINGS max_execution_time = 10`, table, keyCol),
		traceID, clampSpanLinkLimit(limit))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []SpanLink
	for rows.Next() {
		var l SpanLink
		var keys, vals []string
		if err := rows.Scan(&l.TraceID, &l.SpanID, &l.LinkedTraceID, &l.LinkedSpanID,
			&l.TimeUnixNs, &l.ServiceName, &keys, &vals); err != nil {
			return nil, err
		}
		// Zip the storage arrays into a map for the JSON surface — link attr
		// sets are tiny and read whole (same call as exemplar attrs).
		if len(keys) > 0 {
			l.Attrs = make(map[string]string, len(keys))
			for i, k := range keys {
				if i < len(vals) {
					l.Attrs[k] = vals[i]
				}
			}
		}
		out = append(out, l)
	}
	return out, rows.Err()
}
