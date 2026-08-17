package mcptools

// list_metric_names (v0.9.1090, Faz 4) — query_metric'in eksik eşi.
//
// query_metric bir metrik ADI ister; adı bulmanın MCP'de yolu yoktu —
// model semconv ezberinden ad uydurup boş seriye bakıyordu. Bu tool
// MetricNamePicker'ın sunucu aramasıyla aynı okumaya (ListMetricNames:
// katalog-önce, v0.9.285 probe; ham tarama fallback'i sınırlı) ince
// köprüdür. Kesme total ile İFŞA edilir.

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/cilcenk/coremetry/internal/mcp"
)

type listMetricNamesArgs struct {
	Service string `json:"service,omitempty"`
	Pattern string `json:"pattern,omitempty"`
	Limit   int    `json:"limit,omitempty"`
}

func listMetricNamesTool(d Deps) mcp.Tool {
	return mcp.Tool{
		Name: "list_metric_names",
		Description: "List metric names known to Coremetry (name, unit, instrument type, description, last-seen time), " +
			"optionally narrowed to one service and/or a substring pattern. " +
			"ALWAYS use this before query_metric when you are not certain of the exact metric name — " +
			"guessed semconv names silently return empty series. " +
			"Reads the metric catalog (cheap); `total` exposes how many matched beyond the returned page.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"service": map[string]any{
					"type":        "string",
					"description": "Only metrics emitted by this service (exact name from list_services). Empty = all.",
				},
				"pattern": map[string]any{
					"type":        "string",
					"description": "Case-insensitive substring of the metric name, e.g. \"jvm\" or \"http.server\". Empty = all.",
				},
				"limit": map[string]any{
					"type":        "integer",
					"minimum":     1,
					"maximum":     500,
					"description": "Max names to return. Default 100.",
				},
			},
		},
		Handler: func(ctx context.Context, raw json.RawMessage) (any, error) {
			var a listMetricNamesArgs
			if len(raw) > 0 {
				if err := json.Unmarshal(raw, &a); err != nil {
					return nil, fmt.Errorf("decode args: %w", err)
				}
			}
			limit := clampLimit(a.Limit, 100, 500)
			// v0.9.1150 — metrik okuma ROUTER'ından (CH ya da VM). Katalog
			// ve query_metric AYNI kaynaktan okumak ZORUNDA: model buradan
			// aldığı adı query_metric'e verir, iki uç ayrı store'a bakarsa
			// ad var ama seri boş çıkar ve model kendi hatası sanır.
			names, total, err := d.metrics().ListMetricNames(ctx, a.Service, a.Pattern, limit, 0)
			if err != nil {
				return nil, err
			}
			return map[string]any{"metrics": names, "count": len(names), "total": total}, nil
		},
	}
}
