package mcptools

// trace_stats.go — v0.10.474 (CoSRE Telemetry Agent Faz 3, F3-3; audit G7):
// "hata var mı / ne kadar yavaş" sorularının UCUZ cevabı — ham trace
// çekmeden önce sayı: grup başına trace sayısı, hata oranı, p50/p95/p99.
// /api/traces/aggregate'in okuma çekirdeği (GetTraceAggregate: servis /
// operasyon gruplamasında MV hızlı yolu; süzgeç/env/cluster varsa sınırlı
// ham yol). Süzgeç kapısı search_traces ile AYNI (applySearchGate).

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/cilcenk/coremetry/internal/chstore"
	"github.com/cilcenk/coremetry/internal/mcp"
)

type traceStatsArgs struct {
	GroupBy    string         `json:"group_by,omitempty"`
	GroupAttr  string         `json:"group_attr,omitempty"`
	Service    string         `json:"service,omitempty"`
	Namespace  string         `json:"namespace,omitempty"`
	Cluster    string         `json:"cluster,omitempty"`
	Env        string         `json:"env,omitempty"`
	Search     string         `json:"search,omitempty"`
	Filters    []SearchFilter `json:"filters,omitempty"`
	ErrorsOnly bool           `json:"errors_only,omitempty"`
	MinMs      float64        `json:"min_ms,omitempty"`
	MaxMs      float64        `json:"max_ms,omitempty"`
	RangeS     int            `json:"range_s,omitempty"`
	Sort       string         `json:"sort,omitempty"`
	Limit      int            `json:"limit,omitempty"`
}

var traceStatsGroupBy = map[string]bool{"operation": true, "service": true, "status": true, "attr": true}
var traceStatsSort = map[string]bool{"count": true, "perMin": true, "errorRate": true, "avg": true, "p50": true, "p95": true, "p99": true, "max": true, "name": true}

// traceStatsRow — modele giden satır (AggregateRow'un kısa adları).
type traceStatsRow struct {
	Group     string  `json:"group"`
	Extra     string  `json:"extra,omitempty"`
	Traces    uint64  `json:"traces"`
	PerMin    float64 `json:"per_min"`
	Errors    uint64  `json:"errors"`
	ErrorRate float64 `json:"error_rate"`
	AvgMs     float64 `json:"avg_ms"`
	P50Ms     float64 `json:"p50_ms"`
	P95Ms     float64 `json:"p95_ms"`
	P99Ms     float64 `json:"p99_ms"`
	MaxMs     float64 `json:"max_ms"`
}

func traceStatsRows(in []chstore.AggregateRow) []traceStatsRow {
	out := make([]traceStatsRow, 0, len(in))
	for _, r := range in {
		out = append(out, traceStatsRow{Group: r.GroupKey, Extra: r.GroupExtra, Traces: r.TraceCount, PerMin: r.PerMin, Errors: r.ErrorCount, ErrorRate: r.ErrorRate, AvgMs: r.AvgMs, P50Ms: r.P50Ms, P95Ms: r.P95Ms, P99Ms: r.P99Ms, MaxMs: r.MaxMs})
	}
	return out
}

// aggregateDeepLink — /traces?view=aggregate sözleşmesi (audit §2.4).
func aggregateDeepLink(f chstore.AggregateFilter, from, to time.Time) string {
	q := url.Values{}
	q.Set("view", "aggregate")
	if f.GroupBy != "" {
		q.Set("groupBy", f.GroupBy)
	}
	if f.GroupAttr != "" {
		q.Set("groupAttr", f.GroupAttr)
	}
	if f.Service != "" {
		q.Set("service", f.Service)
	}
	if f.Search != "" {
		q.Set("search", f.Search)
	}
	if len(f.Filters) > 0 {
		b, _ := json.Marshal(f.Filters)
		q.Set("filters", string(b))
	}
	if f.HasError {
		q.Set("hasError", "true")
	}
	if f.Env != "" {
		q.Set("env", f.Env)
	}
	if f.Cluster != "" {
		q.Set("cluster", f.Cluster)
	}
	q.Set("range", fmt.Sprintf("custom:%d-%d", from.UnixMilli(), to.UnixMilli()))
	return "/traces?" + q.Encode()
}

func traceStatsTool(d Deps) mcp.Tool {
	return mcp.Tool{
		Name:             "trace_stats",
		ShortDescription: "Trace istatistiği: grup başına (operasyon/servis/durum/attribute) trace sayısı, dakika başı, hata oranı, p50/p95/p99. 'hata var mı / ne kadar yavaş' için ham trace'ten ÖNCE bunu çağır.",
		Description: "Aggregate traces in a scope instead of listing them: per group (operation | service | status | attr=<group_attr>) trace count, per-minute rate, error count and " +
			"rate, avg/p50/p95/p99/max latency (ms). Far cheaper than search_traces for 'are there errors', 'how slow is X', 'which endpoint is worst' — call it FIRST, " +
			"then search_traces only for the specific traces you need. Same filter contract and gate as search_traces: filters[] keys from describe_attributes only, " +
			"filters require a scope (service / namespace / cluster), window clamped (indexed ≤ 6h, non-indexed ≤ 1h; window_clamped_reason). Grouping by service/operation " +
			"without filters reads the trace pre-aggregate (cheap, any window ≤ 7d). `deep_link` opens the same aggregate view in the Traces UI.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"group_by":    map[string]any{"type": "string", "enum": []string{"operation", "service", "status", "attr"}, "description": "Grouping. Default operation. attr needs group_attr."},
				"group_attr":  map[string]any{"type": "string", "description": "Attribute key to group by when group_by=attr (from describe_attributes)."},
				"service":     map[string]any{"type": "string", "description": "Exact service.name. Empty = all (only without filters)."},
				"namespace":   map[string]any{"type": "string", "description": "Exact k8s namespace."},
				"cluster":     map[string]any{"type": "string", "description": "Remote Cluster id / name / span value."},
				"env":         map[string]any{"type": "string", "description": "deploy_env value."},
				"search":      map[string]any{"type": "string", "description": "Free-text match on span/operation names."},
				"filters":     map[string]any{"type": "array", "description": "Attribute filters as in search_traces ({key, op, value|values}).", "items": map[string]any{"type": "object"}},
				"errors_only": map[string]any{"type": "boolean", "description": "Only traces with ≥1 error span."},
				"min_ms":      map[string]any{"type": "number"},
				"max_ms":      map[string]any{"type": "number"},
				"range_s":     map[string]any{"type": "integer", "minimum": 60, "maximum": 604800, "description": "Lookback seconds. Default 1800."},
				"sort":        map[string]any{"type": "string", "enum": []string{"count", "perMin", "errorRate", "avg", "p50", "p95", "p99", "max", "name"}, "description": "Default count."},
				"limit":       map[string]any{"type": "integer", "minimum": 1, "maximum": 100, "description": "Max groups. Default 20."},
			},
		},
		MinRole: "",
		Handler: func(ctx context.Context, raw json.RawMessage) (any, error) {
			var a traceStatsArgs
			if len(raw) > 0 {
				if err := json.Unmarshal(raw, &a); err != nil {
					return nil, fmt.Errorf("decode args: %w", err)
				}
			}
			groupBy := strings.TrimSpace(a.GroupBy)
			if groupBy == "" {
				groupBy = "operation"
			}
			if !traceStatsGroupBy[groupBy] {
				return nil, fmt.Errorf("group_by %q: operation | service | status | attr", a.GroupBy)
			}
			groupAttr := strings.TrimSpace(a.GroupAttr)
			if groupBy == "attr" && groupAttr == "" {
				return nil, fmt.Errorf("group_by=attr için group_attr gerekli (describe_attributes)")
			}
			if groupBy != "attr" {
				groupAttr = ""
			}
			var filters []chstore.FilterExpr
			for _, sf := range a.Filters {
				fe, err := toFilterExpr(sf)
				if err != nil {
					return nil, err
				}
				filters = append(filters, fe)
			}
			userFilters := len(filters) // v0.10.491 — kapı yalnız operatör süzgeçlerine bakar
			ns := strings.TrimSpace(a.Namespace)
			if ns != "" {
				filters = append(filters, chstore.FilterExpr{Key: "k8s.namespace.name", Op: "=", Values: []string{ns}})
			}
			clusterVal := ""
			if c := strings.TrimSpace(a.Cluster); c != "" {
				cs, ok := clustersFor(d, c)
				if !ok || len(cs) == 0 {
					return nil, unknownClusterErr(d, c)
				}
				switch vals := cs[0].SpanValues; len(vals) {
				case 0:
					clusterVal = cs[0].Name
				case 1:
					clusterVal = vals[0]
				default:
					filters = append(filters, chstore.FilterExpr{Key: "cluster", Op: "IN", Values: vals})
				}
			}
			if err := chstore.ValidateFilters(filters); err != nil {
				return nil, fmt.Errorf("filters: %w", err)
			}
			hasScope := strings.TrimSpace(a.Service) != "" || ns != "" || strings.TrimSpace(a.Cluster) != ""
			gate, err := applySearchGate(a.RangeS, clampLimit(a.Limit, 20, 100), filters[:userFilters], hasScope, false, chstore.AttrIndexAvailable(), len(filters)-userFilters)
			if err != nil {
				return nil, err
			}
			from, to := rangeWindow(ctx, gate.RangeS)
			sortBy := strings.TrimSpace(a.Sort)
			if !traceStatsSort[sortBy] {
				sortBy = "count"
			}
			f := chstore.AggregateFilter{
				GroupBy: groupBy, GroupAttr: groupAttr, Service: strings.TrimSpace(a.Service), Search: strings.TrimSpace(a.Search),
				From: from, To: to, HasError: a.ErrorsOnly, MinMs: a.MinMs, MaxMs: a.MaxMs,
				Env: strings.TrimSpace(a.Env), Cluster: clusterVal, Filters: filters,
				Sort: sortBy, Order: "desc", Limit: gate.Limit + 1, // v0.10.491 (Astra #10) — has_more için bir fazlası
			}
			rows, err := d.Store.GetTraceAggregate(ctx, f)
			if err != nil {
				return nil, err
			}
			hasMore := len(rows) > gate.Limit
			if hasMore {
				rows = rows[:gate.Limit]
			}
			f.Limit = gate.Limit
			out := map[string]any{
				"group_by": groupBy, "rows": traceStatsRows(rows), "count": len(rows),
				"window_s": int(to.Sub(from).Seconds()), "has_more": hasMore,
				"deep_link": aggregateDeepLink(f, from, to),
			}
			if groupAttr != "" {
				out["group_attr"] = groupAttr
			}
			if len(filters) > 0 {
				out["filters_applied"] = filters
			}
			if gate.Reason != "" {
				out["window_clamped_reason"] = gate.Reason
			}
			if len(rows) == 0 {
				out["hint"] = "Bu kapsam/pencerede trace yok — kapsamı (resolve_entity) ve pencereyi doğrula; sayı UYDURMA."
			}
			return out, nil
		},
	}
}
