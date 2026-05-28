// Package mcptools wires Coremetry's telemetry surfaces as MCP
// tools (v0.6.5). Lives in its own package — not inside `mcp` —
// so the protocol layer stays storage-agnostic and we don't risk
// a chstore↔mcp import dance.
//
// Each tool closes over a Deps struct containing the chstore +
// logstore handles. Register(srv, deps) is called once at boot
// after the MCP server is constructed, before SetMCP is called
// on the api.Server.
//
// Design choices:
//
//   • Args are decoded into a typed struct per tool. JSON Schema
//     in the registration matches that struct field-for-field so
//     a Claude Desktop-style inspector renders the right form.
//
//   • Time windows are expressed as `range_s` (seconds back from
//     now) instead of from/to nanoseconds. LLMs are notoriously
//     bad at constructing big nanosecond integers; "give me the
//     last 30 minutes" → range_s=1800 is a much more reliable
//     prompt for them than two unix-nano timestamps.
//
//   • Every tool caps Limit at a tool-specific sane default. The
//     LLM can ask for 10 or 100 but not 10000 — context windows
//     are precious and an oversized list_problems response
//     trashes downstream reasoning. Server-side cap is the
//     backstop.
//
//   • Errors are returned as Go errors; the mcp package wraps
//     them into MCP isError=true content. No need to format
//     them here.
//
// Tool catalogue (v0.6.5):
//   - list_services
//   - get_service_health
//   - list_problems
//   - list_anomalies
//   - search_logs
//   - get_trace
//   - query_metric
package mcptools

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/cilcenk/coremetry/internal/chstore"
	"github.com/cilcenk/coremetry/internal/logstore"
	"github.com/cilcenk/coremetry/internal/mcp"
)

// Deps bundles the data-access handles concrete tools close over.
// Kept here (rather than passed through Register) so test setups
// can construct a Deps with mocks for just the surfaces under
// test instead of building a full chstore.
type Deps struct {
	Store    *chstore.Store
	LogStore logstore.Store
}

// Register installs every v0.6.5/v0.6.6 tool and resource on
// the given MCP server. Idempotent — calling twice overwrites
// with the latest closures, but that's a logic-error pattern
// the mcp package logs about.
// ToolList returns the 7 telemetry tools as plain mcp.Tool values
// closed over the given Deps, WITHOUT registering them on an MCP
// server. v0.6.53 — the in-app chatbot reuses this exact set as its
// function-calling backend: it maps each tool's Name / Description /
// InputSchema into a copilot.ToolSpec for the LLM, and invokes
// Handler(ctx, args) directly (the handler signature is transport-
// agnostic — no JSON-RPC envelope needed). Register() delegates here
// so the MCP server and the chatbot can never drift to different
// tool sets.
func ToolList(d Deps) []mcp.Tool {
	return []mcp.Tool{
		listServicesTool(d),
		getServiceHealthTool(d),
		listProblemsTool(d),
		listAnomaliesTool(d),
		searchLogsTool(d),
		getTraceTool(d),
		queryMetricTool(d),
	}
}

func Register(srv *mcp.Server, d Deps) {
	for _, t := range ToolList(d) {
		srv.RegisterTool(t)
	}
	// v0.6.6 — resources: pinned references the LLM can attach
	// to its context or browse. Same data tools surface, but
	// addressable by stable URI so an inspector / Claude Desktop
	// can show "open problems" as a single pin instead of
	// re-issuing the tool call every time.
	registerResources(srv, d)
	// v0.6.7 — prompts: curated system+user message pairs that
	// surface Coremetry's in-app ✨ Explain templates over MCP.
	// Client invokes prompts/get with an id, server fetches the
	// data and returns a complete prompt the LLM can run.
	registerPrompts(srv, d)
}

// registerResources installs concrete + templated resources.
// Concrete URIs are pinned summaries; templates expose per-id
// fetches (one trace, one service, one problem). Reader closures
// share the same Deps the tools do — no new dependency wires.
func registerResources(srv *mcp.Server, d Deps) {
	// ── Static resources ───────────────────────────────────
	srv.RegisterResource(mcp.Resource{
		URI:         "coremetry://services",
		Name:        "Services",
		Description: "All Coremetry services with current RED metrics over the last 30 minutes. Refreshes on each read.",
		MimeType:    "application/json",
		Reader: func(ctx context.Context, _ string) (string, error) {
			from, to := rangeWindow(1800)
			rows, err := d.Store.GetServicesFiltered(ctx, 0, from, to, "", "rps", "desc", 200, 0)
			if err != nil {
				return "", err
			}
			return marshalJSON(map[string]any{"services": rows, "window_s": 1800})
		},
	})
	srv.RegisterResource(mcp.Resource{
		URI:         "coremetry://problems/open",
		Name:        "Open problems",
		Description: "Currently-open Coremetry Problems (alerts that have fired and not been resolved). Sorted by priority then recency.",
		MimeType:    "application/json",
		Reader: func(ctx context.Context, _ string) (string, error) {
			rows, err := d.Store.ListProblems(ctx, chstore.ProblemFilter{Status: "open", Limit: 100})
			if err != nil {
				return "", err
			}
			return marshalJSON(map[string]any{"problems": rows, "count": len(rows)})
		},
	})
	srv.RegisterResource(mcp.Resource{
		URI:         "coremetry://anomalies/recent",
		Name:        "Recent anomalies",
		Description: "Anomaly events from the last hour — log patterns + trace operations + ML detectors that exceeded their baseline.",
		MimeType:    "application/json",
		Reader: func(ctx context.Context, _ string) (string, error) {
			from, _ := rangeWindow(3600)
			rows, err := d.Store.ListAnomalyEvents(ctx, chstore.ListAnomalyEventsFilter{
				SinceNs: from.UnixNano(),
				Limit:   100,
			})
			if err != nil {
				return "", err
			}
			return marshalJSON(map[string]any{"anomalies": rows, "count": len(rows)})
		},
	})

	// ── Templated resources ─────────────────────────────────
	// Service detail by name.
	const serviceTpl = "coremetry://service/{name}"
	srv.RegisterResourceTemplate(mcp.ResourceTemplate{
		URITemplate: serviceTpl,
		Name:        "Service detail",
		Description: "RED summary + open-problem count for one service over the last 30 minutes.",
		MimeType:    "application/json",
		Reader: func(ctx context.Context, uri string) (string, error) {
			name := mcp.ExtractURITemplateValue(serviceTpl, uri)
			if name == "" {
				return "", fmt.Errorf("missing service name in URI %q", uri)
			}
			from, to := rangeWindow(1800)
			rows, err := d.Store.GetServicesFiltered(ctx, 0, from, to, name, "rps", "desc", 1, 0)
			if err != nil {
				return "", err
			}
			if len(rows) == 0 {
				return marshalJSON(map[string]any{"found": false, "service": name})
			}
			probs, _ := d.Store.CountProblems(ctx, chstore.ProblemFilter{Status: "open", Service: name})
			return marshalJSON(map[string]any{
				"found":         true,
				"summary":       rows[0],
				"open_problems": probs,
			})
		},
	})

	// Trace detail by trace_id.
	const traceTpl = "coremetry://trace/{trace_id}"
	srv.RegisterResourceTemplate(mcp.ResourceTemplate{
		URITemplate: traceTpl,
		Name:        "Trace detail",
		Description: "All spans for one trace ID — full waterfall.",
		MimeType:    "application/json",
		Reader: func(ctx context.Context, uri string) (string, error) {
			tid := mcp.ExtractURITemplateValue(traceTpl, uri)
			if tid == "" {
				return "", fmt.Errorf("missing trace_id in URI %q", uri)
			}
			spans, err := d.Store.GetTrace(ctx, tid)
			if err != nil {
				return "", err
			}
			return marshalJSON(map[string]any{
				"trace_id":   tid,
				"spans":      spans,
				"span_count": len(spans),
			})
		},
	})
}

// marshalJSON keeps the resource Reader closures one-liner-ish.
func marshalJSON(v any) (string, error) {
	b, err := json.Marshal(v)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

// rangeWindow turns a range_s argument (seconds back from now)
// into a (from, to) pair. Used by every list/query tool below.
// Caps the lookback at 7 days so an over-eager LLM can't ask for
// 90 days of spans and trigger a CH scan timeout.
func rangeWindow(rangeS int) (from, to time.Time) {
	to = time.Now()
	if rangeS <= 0 {
		rangeS = 1800 // default: last 30 min
	}
	if rangeS > 7*86400 {
		rangeS = 7 * 86400
	}
	from = to.Add(-time.Duration(rangeS) * time.Second)
	return
}

// clampLimit makes the LLM's `limit` request stay inside a
// per-tool reasonable band.
func clampLimit(req, defaultLim, maxLim int) int {
	if req <= 0 {
		return defaultLim
	}
	if req > maxLim {
		return maxLim
	}
	return req
}

// ─── list_services ─────────────────────────────────────────────

type listServicesArgs struct {
	NameContains string `json:"name_contains,omitempty"`
	RangeS       int    `json:"range_s,omitempty"`
	Limit        int    `json:"limit,omitempty"`
}

func listServicesTool(d Deps) mcp.Tool {
	return mcp.Tool{
		Name:        "list_services",
		Description: "List Coremetry services with their current RPS, error rate, and p99 latency. Reads the 5-minute pre-aggregate so it's cheap to call repeatedly. Use this as the entry point when investigating an incident: 'which services are unhealthy right now?'",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"name_contains": map[string]any{
					"type":        "string",
					"description": "Substring match against service name (case-insensitive). Empty = all services.",
				},
				"range_s": map[string]any{
					"type":        "integer",
					"description": "Lookback window in seconds. Default 1800 (30min), max 604800 (7d).",
					"minimum":     0,
					"maximum":     604800,
				},
				"limit": map[string]any{
					"type":        "integer",
					"description": "Max services to return. Default 50, max 500.",
					"minimum":     1,
					"maximum":     500,
				},
			},
		},
		Handler: func(ctx context.Context, raw json.RawMessage) (any, error) {
			var a listServicesArgs
			if len(raw) > 0 {
				if err := json.Unmarshal(raw, &a); err != nil {
					return nil, fmt.Errorf("decode args: %w", err)
				}
			}
			from, to := rangeWindow(a.RangeS)
			limit := clampLimit(a.Limit, 50, 500)
			rows, err := d.Store.GetServicesFiltered(ctx, 0, from, to, a.NameContains, "rps", "desc", limit, 0)
			if err != nil {
				return nil, err
			}
			return map[string]any{"services": rows, "count": len(rows)}, nil
		},
	}
}

// ─── get_service_health ────────────────────────────────────────

type getServiceHealthArgs struct {
	Service string `json:"service"`
	RangeS  int    `json:"range_s,omitempty"`
}

func getServiceHealthTool(d Deps) mcp.Tool {
	return mcp.Tool{
		Name:        "get_service_health",
		Description: "Get RED metrics (rate, errors, duration p99) + open problem count for one service over a window. Use after list_services to drill into a specific service's recent health.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"service": map[string]any{
					"type":        "string",
					"description": "Exact service name. Required.",
				},
				"range_s": map[string]any{
					"type":        "integer",
					"description": "Lookback window seconds. Default 1800.",
				},
			},
			"required": []string{"service"},
		},
		Handler: func(ctx context.Context, raw json.RawMessage) (any, error) {
			var a getServiceHealthArgs
			if err := json.Unmarshal(raw, &a); err != nil {
				return nil, fmt.Errorf("decode args: %w", err)
			}
			if a.Service == "" {
				return nil, fmt.Errorf("service is required")
			}
			from, to := rangeWindow(a.RangeS)
			rows, err := d.Store.GetServicesFiltered(ctx, 0, from, to, a.Service, "rps", "desc", 1, 0)
			if err != nil {
				return nil, err
			}
			if len(rows) == 0 {
				return map[string]any{"found": false, "service": a.Service}, nil
			}
			probs, _ := d.Store.CountProblems(ctx, chstore.ProblemFilter{
				Status:  "open",
				Service: a.Service,
			})
			return map[string]any{
				"found":         true,
				"summary":       rows[0],
				"open_problems": probs,
			}, nil
		},
	}
}

// ─── list_problems ─────────────────────────────────────────────

type listProblemsArgs struct {
	Status   string `json:"status,omitempty"`
	Service  string `json:"service,omitempty"`
	Severity string `json:"severity,omitempty"`
	Priority string `json:"priority,omitempty"`
	Limit    int    `json:"limit,omitempty"`
}

func listProblemsTool(d Deps) mcp.Tool {
	return mcp.Tool{
		Name:        "list_problems",
		Description: "List Coremetry Problems (alerts that fired). Default filters to status=open. Use priority=P1 for the most urgent. Each problem has rule_id + service + severity + first_seen + a priority reason explaining why it's at its current P1/P2/P3 tier.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"status":   map[string]any{"type": "string", "enum": []string{"", "open", "resolved"}, "description": "Default 'open'."},
				"service":  map[string]any{"type": "string", "description": "Filter to one service."},
				"severity": map[string]any{"type": "string", "enum": []string{"", "critical", "warning", "info"}},
				"priority": map[string]any{"type": "string", "enum": []string{"", "P1", "P2", "P3"}, "description": "Triage tier. P1=handle now."},
				"limit":    map[string]any{"type": "integer", "minimum": 1, "maximum": 200, "description": "Default 25."},
			},
		},
		Handler: func(ctx context.Context, raw json.RawMessage) (any, error) {
			var a listProblemsArgs
			if len(raw) > 0 {
				if err := json.Unmarshal(raw, &a); err != nil {
					return nil, fmt.Errorf("decode args: %w", err)
				}
			}
			status := a.Status
			if status == "" {
				status = "open"
			}
			f := chstore.ProblemFilter{
				Status:   status,
				Service:  a.Service,
				Severity: a.Severity,
				Limit:    clampLimit(a.Limit, 25, 200),
			}
			if a.Priority != "" {
				f.Priority = []string{a.Priority}
			}
			rows, err := d.Store.ListProblems(ctx, f)
			if err != nil {
				return nil, err
			}
			return map[string]any{"problems": rows, "count": len(rows)}, nil
		},
	}
}

// ─── list_anomalies ────────────────────────────────────────────

type listAnomaliesArgs struct {
	Service string `json:"service,omitempty"`
	RangeS  int    `json:"range_s,omitempty"`
	Limit   int    `json:"limit,omitempty"`
}

func listAnomaliesTool(d Deps) mcp.Tool {
	return mcp.Tool{
		Name:        "list_anomalies",
		Description: "List recent anomaly events (log-pattern + trace-op + ML detectors). Anomalies are 'something changed against baseline' notices — not hard alerts. Use this when investigating a service whose RED metrics look normal but the operator suspects a behavior shift.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"service": map[string]any{"type": "string"},
				"range_s": map[string]any{"type": "integer", "description": "Default 3600 (1h)."},
				"limit":   map[string]any{"type": "integer", "minimum": 1, "maximum": 200, "description": "Default 25."},
			},
		},
		Handler: func(ctx context.Context, raw json.RawMessage) (any, error) {
			var a listAnomaliesArgs
			if len(raw) > 0 {
				if err := json.Unmarshal(raw, &a); err != nil {
					return nil, fmt.Errorf("decode args: %w", err)
				}
			}
			rs := a.RangeS
			if rs == 0 {
				rs = 3600
			}
			from, _ := rangeWindow(rs)
			lim := clampLimit(a.Limit, 25, 200)
			// ListAnomalyEventsFilter has no Service slot — when
			// the LLM asks for one we fetch a wider slice (4x the
			// asked limit, capped) and post-filter. Sufficient for
			// the use case (one service inside a 1h window rarely
			// produces more than the cap).
			fetchLim := lim
			if a.Service != "" {
				fetchLim = clampLimit(lim*4, 100, 200)
			}
			rows, err := d.Store.ListAnomalyEvents(ctx, chstore.ListAnomalyEventsFilter{
				SinceNs: from.UnixNano(),
				Limit:   fetchLim,
			})
			if err != nil {
				return nil, err
			}
			if a.Service != "" {
				filtered := rows[:0]
				for _, r := range rows {
					if r.Service == a.Service {
						filtered = append(filtered, r)
						if len(filtered) >= lim {
							break
						}
					}
				}
				rows = filtered
			}
			return map[string]any{"anomalies": rows, "count": len(rows)}, nil
		},
	}
}

// ─── search_logs ───────────────────────────────────────────────

type searchLogsArgs struct {
	Query       string `json:"query,omitempty"`
	Service     string `json:"service,omitempty"`
	Cluster     string `json:"cluster,omitempty"`
	TraceID     string `json:"trace_id,omitempty"`
	SeverityMin int    `json:"severity_min,omitempty"`
	RangeS      int    `json:"range_s,omitempty"`
	Limit       int    `json:"limit,omitempty"`
}

func searchLogsTool(d Deps) mcp.Tool {
	return mcp.Tool{
		Name:        "search_logs",
		Description: "Full-text + structured search across logs. Routes to whichever backend Coremetry is configured for (ClickHouse or Elasticsearch). Use trace_id to pull every log line belonging to one trace. Use severity_min=17 for errors only (OTel severity number; 17=ERROR, 21=FATAL).",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"query":        map[string]any{"type": "string", "description": "Free-text or structured query (ES query_string when ES is the backend)."},
				"service":      map[string]any{"type": "string"},
				"cluster":      map[string]any{"type": "string", "description": "k8s cluster name from resource attrs."},
				"trace_id":     map[string]any{"type": "string", "description": "Pull all logs for one trace."},
				"severity_min": map[string]any{"type": "integer", "minimum": 0, "maximum": 24, "description": "OTel severity number floor. 17=ERROR."},
				"range_s":      map[string]any{"type": "integer", "description": "Default 1800."},
				"limit":        map[string]any{"type": "integer", "minimum": 1, "maximum": 500, "description": "Default 50."},
			},
		},
		Handler: func(ctx context.Context, raw json.RawMessage) (any, error) {
			var a searchLogsArgs
			if len(raw) > 0 {
				if err := json.Unmarshal(raw, &a); err != nil {
					return nil, fmt.Errorf("decode args: %w", err)
				}
			}
			from, to := rangeWindow(a.RangeS)
			page, err := d.LogStore.Search(ctx, logstore.Filter{
				Service:     a.Service,
				Cluster:     a.Cluster,
				Search:      a.Query,
				TraceID:     a.TraceID,
				From:        from,
				To:          to,
				SeverityMin: uint8(a.SeverityMin),
				Limit:       clampLimit(a.Limit, 50, 500),
			})
			if err != nil {
				return nil, err
			}
			return page, nil
		},
	}
}

// ─── get_trace ─────────────────────────────────────────────────

type getTraceArgs struct {
	TraceID string `json:"trace_id"`
}

func getTraceTool(d Deps) mcp.Tool {
	return mcp.Tool{
		Name:        "get_trace",
		Description: "Fetch every span belonging to one trace ID. Returns the full waterfall: service, operation, duration, error status, parent_span_id. Use after search_logs surfaces a trace ID, or directly from a problem's correlated traces.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"trace_id": map[string]any{"type": "string", "description": "32-char hex trace ID."},
			},
			"required": []string{"trace_id"},
		},
		Handler: func(ctx context.Context, raw json.RawMessage) (any, error) {
			var a getTraceArgs
			if err := json.Unmarshal(raw, &a); err != nil {
				return nil, fmt.Errorf("decode args: %w", err)
			}
			if a.TraceID == "" {
				return nil, fmt.Errorf("trace_id is required")
			}
			spans, err := d.Store.GetTrace(ctx, a.TraceID)
			if err != nil {
				return nil, err
			}
			return map[string]any{"trace_id": a.TraceID, "spans": spans, "span_count": len(spans)}, nil
		},
	}
}

// ─── query_metric ──────────────────────────────────────────────

type queryMetricArgs struct {
	Name        string `json:"name"`
	Service     string `json:"service,omitempty"`
	Aggregation string `json:"aggregation,omitempty"`
	GroupBy     string `json:"group_by,omitempty"`
	RangeS      int    `json:"range_s,omitempty"`
	StepS       int    `json:"step_s,omitempty"`
}

func queryMetricTool(d Deps) mcp.Tool {
	return mcp.Tool{
		Name:        "query_metric",
		Description: "Run a time-bucketed query against ingested OTel metrics. Returns one or more series of {time, value} points. Use aggregation='p99' for latency histograms, 'sum' for counters, 'avg' for gauges. Pair with the OTel semantic conventions (e.g. http.server.request.duration → p99 / ms).",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"name":        map[string]any{"type": "string", "description": "OTel metric name."},
				"service":     map[string]any{"type": "string"},
				"aggregation": map[string]any{"type": "string", "enum": []string{"avg", "sum", "min", "max", "last", "p50", "p95", "p99"}, "description": "Default 'avg'."},
				"group_by":    map[string]any{"type": "string", "description": "Comma-separated attribute keys (e.g. 'http.route,http.status_code')."},
				"range_s":     map[string]any{"type": "integer", "description": "Default 1800."},
				"step_s":      map[string]any{"type": "integer", "description": "Bucket size seconds. 0 = auto."},
			},
			"required": []string{"name"},
		},
		Handler: func(ctx context.Context, raw json.RawMessage) (any, error) {
			var a queryMetricArgs
			if err := json.Unmarshal(raw, &a); err != nil {
				return nil, fmt.Errorf("decode args: %w", err)
			}
			if a.Name == "" {
				return nil, fmt.Errorf("name is required")
			}
			agg := a.Aggregation
			if agg == "" {
				agg = "avg"
			}
			from, to := rangeWindow(a.RangeS)
			var groups []string
			if a.GroupBy != "" {
				for _, p := range splitCSV(a.GroupBy) {
					groups = append(groups, p)
				}
			}
			series, err := d.Store.QueryMetric(ctx, chstore.MetricQueryFilter{
				Name:        a.Name,
				Service:     a.Service,
				Aggregation: agg,
				GroupBy:     groups,
				From:        from,
				To:          to,
				StepSeconds: a.StepS,
			})
			if err != nil {
				return nil, err
			}
			return map[string]any{"series": series, "count": len(series)}, nil
		},
	}
}

// splitCSV: tiny helper kept private so we don't pull in
// strings just for one Split call further away.
func splitCSV(s string) []string {
	var out []string
	start := 0
	for i := 0; i < len(s); i++ {
		if s[i] == ',' {
			if i > start {
				out = append(out, s[start:i])
			}
			start = i + 1
		}
	}
	if start < len(s) {
		out = append(out, s[start:])
	}
	return out
}
