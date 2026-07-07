package mcptools

// pivots.go — cross-signal pivot tools (v0.8.333, pivot Phase 4: MCP
// parity). The UI got the pivot surfaces in Phases 1-3 (exemplars, span
// links, trace-scoped logs, span-window RED metrics); these four tools give
// the MCP server AND the in-app copilot the same moves, so an LLM can walk
// trace ↔ log ↔ metric without hand-chaining raw queries:
//
//   get_logs_for_trace   — trace/span → logs (LogsForTrace/LogsForSpan under
//                          the 3s pivot timeout; slow backend → structured
//                          degraded result, never a tool error).
//   get_exemplar_traces  — metric spike → real trace ids (OTLP exemplars via
//                          ExemplarsForMetric).
//   get_linked_traces    — trace → OTel span-link neighbours, BOTH directions
//                          (LinksFromTrace + LinksToTrace, each a PK scan).
//   get_metrics_for_span — span timestamp → the service's RED series around
//                          it (chstore.ServiceREDSeries — the SAME composition
//                          the /api/spans/window-metrics endpoint serves).
//
// House conventions per the /mcp-tools skill: Deps closures, range_s (never
// from/to nanos — the ONE exception is get_metrics_for_span's at_unix_ns,
// which the LLM COPIES from a get_trace span's startTime rather than
// constructing), clampLimit caps, per-field schema descriptions. All four
// are read-only; registered via ToolList so MCP + copilot can't drift.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/cilcenk/coremetry/internal/chstore"
	"github.com/cilcenk/coremetry/internal/logstore"
	"github.com/cilcenk/coremetry/internal/mcp"
)

// isHexLen reports whether s is exactly n lowercase-hex chars. Trace ids are
// 32 (OTel 16-byte, hex.EncodeToString output), span ids 16. Handlers
// lowercase input first, so uppercase from the LLM is accepted at the edge;
// anything else 400s here instead of running PK lookups that can only miss.
func isHexLen(s string, n int) bool {
	if len(s) != n {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}

// normalizeTraceID lowercases + validates a 32-hex trace id argument.
func normalizeTraceID(raw string) (string, error) {
	id := strings.ToLower(strings.TrimSpace(raw))
	if !isHexLen(id, 32) {
		return "", fmt.Errorf("trace_id must be 32 hex chars, got %q", raw)
	}
	return id, nil
}

// clampWindowS resolves get_metrics_for_span's window_s (HALF-window seconds
// around the anchor): default 900 (±15m — the same bracket the correlate
// bundle widens by), clamped to [60, 3600] so the LLM can neither shrink the
// read below one summary bucket nor drag a multi-hour scan. Mirrors the
// /api/spans/window-metrics clamp (api/pivot.go pivotWindowClamp).
func clampWindowS(n int) int {
	if n <= 0 {
		return 900
	}
	if n < 60 {
		return 60
	}
	if n > 3600 {
		return 3600
	}
	return n
}

// ─── get_logs_for_trace ────────────────────────────────────────

type getLogsForTraceArgs struct {
	TraceID string `json:"trace_id"`
	SpanID  string `json:"span_id,omitempty"`
	RangeS  int    `json:"range_s,omitempty"`
	Limit   int    `json:"limit,omitempty"`
}

func getLogsForTraceTool(d Deps) mcp.Tool {
	return mcp.Tool{
		Name:        "get_logs_for_trace",
		Description: "Fetch the log lines that carry one trace's context — the trace→log pivot. Pass span_id to narrow to a single span's logs. Runs under a 3-second budget: if the log backend is slow or unreachable the result comes back with degraded=true and empty logs instead of an error, so treat degraded=true as 'logs unavailable right now', not 'no logs exist'. Use after get_trace to see what the failing span logged; chain interesting log attributes into search_logs for a wider look.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"trace_id": map[string]any{
					"type":        "string",
					"description": "32-char hex trace ID (as returned by get_trace / get_exemplar_traces / search_logs).",
				},
				"span_id": map[string]any{
					"type":        "string",
					"description": "Optional 16-char hex span ID to narrow to one span's log lines.",
				},
				"range_s": map[string]any{
					"type":        "integer",
					"minimum":     0,
					"maximum":     604800,
					"description": "Lookback window in seconds bracketing the trace. Default 1800 (30min), max 604800 (7d). Widen it when the trace is older than 30 minutes.",
				},
				"limit": map[string]any{
					"type":        "integer",
					"minimum":     1,
					"maximum":     500,
					"description": "Max log lines to return. Default 100, max 500.",
				},
			},
			"required": []string{"trace_id"},
		},
		Handler: func(ctx context.Context, raw json.RawMessage) (any, error) {
			var a getLogsForTraceArgs
			if err := json.Unmarshal(raw, &a); err != nil {
				return nil, fmt.Errorf("decode args: %w", err)
			}
			if d.LogStore == nil {
				return nil, fmt.Errorf("log backend not configured")
			}
			traceID, err := normalizeTraceID(a.TraceID)
			if err != nil {
				return nil, err
			}
			spanID := strings.ToLower(strings.TrimSpace(a.SpanID))
			if spanID != "" && !isHexLen(spanID, 16) {
				return nil, fmt.Errorf("span_id must be 16 hex chars, got %q", a.SpanID)
			}
			from, to := rangeWindow(a.RangeS)
			limit := clampLimit(a.Limit, 100, 500)
			var page *logstore.Page
			if spanID != "" {
				page, err = logstore.LogsForSpan(ctx, d.LogStore, traceID, spanID, from, to, limit)
			} else {
				page, err = logstore.LogsForTrace(ctx, d.LogStore, traceID, from, to, limit)
			}
			if err != nil {
				// Slow/unreachable backend is a CONDITION the LLM should
				// reason about (retry later, fall back to span events), not a
				// tool failure — structured degraded result, no error.
				if errors.Is(err, logstore.ErrBackendSlow) {
					return map[string]any{
						"degraded": true,
						"reason":   err.Error(),
						"logs":     []any{},
						"count":    0,
					}, nil
				}
				return nil, err
			}
			return map[string]any{
				"degraded": false,
				"trace_id": traceID,
				"logs":     page.Logs,
				"count":    len(page.Logs),
				"total":    page.Total,
			}, nil
		},
	}
}

// ─── get_exemplar_traces ───────────────────────────────────────

type getExemplarTracesArgs struct {
	Metric  string `json:"metric"`
	Service string `json:"service"`
	RangeS  int    `json:"range_s,omitempty"`
	Limit   int    `json:"limit,omitempty"`
}

func getExemplarTracesTool(d Deps) mcp.Tool {
	return mcp.Tool{
		Name:        "get_exemplar_traces",
		Description: "THE metric→trace pivot: given a metric spike, returns real trace ids recorded as OTLP exemplars in the window — producer-captured trace context for individual measurements, so each item is {ts, value, trace_id, span_id} tying a concrete data point to the exact request that produced it. Follow with get_trace on an interesting trace_id (e.g. the highest value) to see the full waterfall. Bounded primary-key/granule read on the exemplars table — cheap. Empty result means the instrumentation exports no exemplars for this metric, not that the metric is healthy.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"metric": map[string]any{
					"type":        "string",
					"description": "OTel metric name the spike is on (as used by query_metric, e.g. 'http.server.request.duration').",
				},
				"service": map[string]any{
					"type":        "string",
					"description": "Exact service name emitting the metric.",
				},
				"range_s": map[string]any{
					"type":        "integer",
					"minimum":     0,
					"maximum":     604800,
					"description": "Lookback window in seconds — bracket the spike. Default 1800 (30min), max 604800 (7d).",
				},
				"limit": map[string]any{
					"type":        "integer",
					"minimum":     1,
					"maximum":     100,
					"description": "Max exemplars to return. Default 20, max 100.",
				},
			},
			"required": []string{"metric", "service"},
		},
		Handler: func(ctx context.Context, raw json.RawMessage) (any, error) {
			var a getExemplarTracesArgs
			if err := json.Unmarshal(raw, &a); err != nil {
				return nil, fmt.Errorf("decode args: %w", err)
			}
			if strings.TrimSpace(a.Metric) == "" {
				return nil, fmt.Errorf("metric is required")
			}
			if strings.TrimSpace(a.Service) == "" {
				return nil, fmt.Errorf("service is required")
			}
			from, to := rangeWindow(a.RangeS)
			limit := clampLimit(a.Limit, 20, 100)
			rows, err := d.Store.ExemplarsForMetric(ctx, a.Metric, a.Service, from, to, limit)
			if err != nil {
				return nil, err
			}
			items := make([]map[string]any, 0, len(rows))
			for _, e := range rows {
				items = append(items, map[string]any{
					"ts":       e.TimeUnixNs,
					"value":    e.Value,
					"trace_id": e.TraceID,
					"span_id":  e.SpanID,
				})
			}
			return map[string]any{"items": items, "count": len(items)}, nil
		},
	}
}

// ─── get_linked_traces ─────────────────────────────────────────

type getLinkedTracesArgs struct {
	TraceID string `json:"trace_id"`
}

func getLinkedTracesTool(d Deps) mcp.Tool {
	return mcp.Tool{
		Name:        "get_linked_traces",
		Description: "Traverse OTel span links for one trace, BOTH directions in one call: 'outgoing' = links this trace's spans declare (causal predecessors — e.g. the producer trace a consumer span links back to), 'incoming' = links other traces declare pointing AT this one (its downstream consumers/batch followers). Each link carries trace/span ids on both ends plus link attributes. This finds async/batch relationships the parent-child waterfall can't show. Both directions are primary-key point-lookups — cheap. Follow an interesting linked trace id with get_trace.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"trace_id": map[string]any{
					"type":        "string",
					"description": "32-char hex trace ID to traverse links from/to.",
				},
			},
			"required": []string{"trace_id"},
		},
		Handler: func(ctx context.Context, raw json.RawMessage) (any, error) {
			var a getLinkedTracesArgs
			if err := json.Unmarshal(raw, &a); err != nil {
				return nil, fmt.Errorf("decode args: %w", err)
			}
			traceID, err := normalizeTraceID(a.TraceID)
			if err != nil {
				return nil, err
			}
			// 0 → chstore's default (100 per direction). Sequential on
			// purpose — both are sub-ms PK point-lookups (api/pivot.go call).
			outgoing, err := d.Store.LinksFromTrace(ctx, traceID, 0)
			if err != nil {
				return nil, err
			}
			incoming, err := d.Store.LinksToTrace(ctx, traceID, 0)
			if err != nil {
				return nil, err
			}
			if outgoing == nil {
				outgoing = []chstore.SpanLink{}
			}
			if incoming == nil {
				incoming = []chstore.SpanLink{}
			}
			return map[string]any{
				"trace_id":       traceID,
				"outgoing":       outgoing,
				"incoming":       incoming,
				"outgoing_count": len(outgoing),
				"incoming_count": len(incoming),
			}, nil
		},
	}
}

// ─── get_metrics_for_span ──────────────────────────────────────

type getMetricsForSpanArgs struct {
	Service  string `json:"service"`
	AtUnixNs int64  `json:"at_unix_ns"`
	WindowS  int    `json:"window_s,omitempty"`
}

func getMetricsForSpanTool(d Deps) mcp.Tool {
	return mcp.Tool{
		Name:        "get_metrics_for_span",
		Description: "The span→metric pivot: the service's RED series (rate, error_rate, p99 latency) bracketing one span's timestamp — 'was the whole service degraded when this span ran, or is this span an outlier?'. COPY at_unix_ns from a get_trace span's startTime field (already unix nanoseconds) — do not construct the timestamp yourself. Reads the 5-minute pre-aggregate, cheap to call. Returns up to three series of {time, value} points covering ±window_s around the anchor. Use after get_trace when deciding whether a slow/error span reflects a service-wide problem.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"service": map[string]any{
					"type":        "string",
					"description": "Exact service name (the span's serviceName from get_trace).",
				},
				"at_unix_ns": map[string]any{
					"type":        "integer",
					"description": "Anchor instant as unix nanoseconds — copy a span's startTime from get_trace verbatim.",
				},
				"window_s": map[string]any{
					"type":        "integer",
					"minimum":     60,
					"maximum":     3600,
					"description": "Half-window in seconds around the anchor. Default 900 (±15min), clamped to [60, 3600].",
				},
			},
			"required": []string{"service", "at_unix_ns"},
		},
		Handler: func(ctx context.Context, raw json.RawMessage) (any, error) {
			var a getMetricsForSpanArgs
			if err := json.Unmarshal(raw, &a); err != nil {
				return nil, fmt.Errorf("decode args: %w", err)
			}
			if strings.TrimSpace(a.Service) == "" {
				return nil, fmt.Errorf("service is required")
			}
			if a.AtUnixNs <= 0 {
				return nil, fmt.Errorf("at_unix_ns is required (copy a span's startTime from get_trace)")
			}
			windowS := clampWindowS(a.WindowS)
			at := time.Unix(0, a.AtUnixNs)
			from := at.Add(-time.Duration(windowS) * time.Second)
			to := at.Add(time.Duration(windowS) * time.Second)
			// SAME composition /api/spans/window-metrics serves —
			// chstore.ServiceREDSeries (service_summary_5m MV fast-path).
			series := d.Store.ServiceREDSeries(ctx, a.Service, from, to)
			if series == nil {
				series = []chstore.SpanMetricSeries{}
			}
			return map[string]any{
				"service":  a.Service,
				"from_ns":  from.UnixNano(),
				"to_ns":    to.UnixNano(),
				"window_s": windowS,
				"metrics":  series,
				"count":    len(series),
			}, nil
		},
	}
}
