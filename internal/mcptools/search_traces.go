package mcptools

// search_traces (v0.9.1087, Faz 4) — id'siz giriş: zincirin ilk halkası.
//
// get_trace bir trace ID ister; ID'yi bulmanın MCP'de yolu yoktu —
// model ya get_exemplar_traces'in dar penceresine sıkışıyor ya da ID
// uyduruyordu. Bu tool /traces sayfasının okumasına (GetTraces — MV
// fast-path + ham fallback, sınırlar içeride) ince köprüdür.
//
// Dürüstlük zarfları aynen taşınır (no-silent-caps): sonuç dolu geldiyse
// has_more; MV top-N'i yenilik dilimi içinde sıraladıysa ranked_within;
// kaynak yetmeyip pencere yarılandıysa narrowed_from_iso — üçü de
// sessiz kalsaydı model "hepsini gördüm" sanırdı.

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/cilcenk/coremetry/internal/chstore"
	"github.com/cilcenk/coremetry/internal/mcp"
)

type searchTracesArgs struct {
	Service    string `json:"service,omitempty"`
	Search     string `json:"search,omitempty"`
	ErrorsOnly bool   `json:"errors_only,omitempty"`
	RootOnly   bool   `json:"root_only,omitempty"`
	Sort       string `json:"sort,omitempty"`
	RangeS     int    `json:"range_s,omitempty"`
	Limit      int    `json:"limit,omitempty"`
}

func searchTracesTool(d Deps) mcp.Tool {
	return mcp.Tool{
		Name:             "search_traces",
		ShortDescription: "ID'SİZ trace arama → özet liste (trace_id, kök operasyon, giriş servisi, süre, hata). Zincirin ilk halkası: önce trace id'yi BUL, sonra get_trace. sort=duration en yavaşlar.",
		Description: "Search traces WITHOUT a trace id and get summaries " +
			"(trace_id, root operation, entry service, start time, duration ms, span count, error flag). " +
			"This is the entry point of the trace chain: use it to FIND a trace id, then drill with get_trace / get_logs_for_trace. " +
			"sort=duration surfaces the slowest traces first (use with errors_only=false to hunt latency); " +
			"errors_only=true narrows to failed traces. " +
			"Reads the trace-summary pre-aggregate when possible, falls back to bounded raw scans — keep range_s modest (default 1800s). " +
			"Honesty envelope: has_more (page full), ranked_within (duration sort ranked inside the newest-N slice only), " +
			"narrowed_from_iso (window was halved under resource pressure — the answer covers LESS than you asked).",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"service": map[string]any{
					"type":        "string",
					"description": "Exact service name (from list_services). Empty = all services.",
				},
				"search": map[string]any{
					"type":        "string",
					"description": "Free-text match on span/operation names inside the trace. Empty = no text filter.",
				},
				"errors_only": map[string]any{
					"type":        "boolean",
					"description": "Only traces containing at least one error span. Default false.",
				},
				"root_only": map[string]any{
					"type":        "boolean",
					"description": "Only traces whose root span was ingested (hides fragmented traces). Default false.",
				},
				"sort": map[string]any{
					"type":        "string",
					"enum":        []string{"time", "duration"},
					"description": "time = newest first (default); duration = slowest first.",
				},
				"range_s": map[string]any{
					"type":        "integer",
					"minimum":     0,
					"maximum":     604800,
					"description": "Lookback seconds. Default 1800 (30 min).",
				},
				"limit": map[string]any{
					"type":        "integer",
					"minimum":     1,
					"maximum":     100,
					"description": "Max traces to return. Default 20.",
				},
			},
		},
		Handler: func(ctx context.Context, raw json.RawMessage) (any, error) {
			var a searchTracesArgs
			if len(raw) > 0 {
				if err := json.Unmarshal(raw, &a); err != nil {
					return nil, fmt.Errorf("decode args: %w", err)
				}
			}
			from, to := rangeWindow(a.RangeS)
			limit := clampLimit(a.Limit, 20, 100)
			sortBy := "time"
			if a.Sort == "duration" {
				sortBy = "duration"
			}
			var ranked int
			var narrowed time.Time
			f := chstore.TraceFilter{
				Service: a.Service, Search: a.Search,
				From: from, To: to,
				HasError: a.ErrorsOnly, RootOnly: a.RootOnly,
				Sort: sortBy, Order: "desc",
				Limit:        limit,
				CountMode:    "skip", // sayım pahalı ve modele gerekmez; has_more yeter
				RankedWithin: &ranked,
				NarrowedFrom: &narrowed,
			}
			rows, _, hasMore, err := d.Store.GetTraces(ctx, f)
			if err != nil {
				return nil, err
			}
			out := map[string]any{
				"traces":   rows,
				"count":    len(rows),
				"window_s": int(to.Sub(from).Seconds()),
				"has_more": hasMore,
			}
			if ranked > 0 {
				out["ranked_within"] = ranked
			}
			if !narrowed.IsZero() {
				out["narrowed_from_iso"] = narrowed.UTC().Format(time.RFC3339)
			}
			return out, nil
		},
	}
}
