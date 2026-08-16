package mcptools

// list_slo_status (v0.9.1089, Faz 4) — SLO tanımları + canlı durum +
// deterministik tükenme yörüngesi TEK çağrıda. Modelin "bütçe ne
// zaman biter"i uydurma alanı yok: HoursToExhaust/SafeBurn sunucunun
// projectBurnHours matematiğinden gelir (v0.9.1083 explain girdisiyle
// aynı kaynak).
//
// Maliyet şekli: her SLO için status+forecast birer sınırlı MV/rollup
// okuması. SLO'lar operatör-tanımlı ve azdır (onlarca); limit yine de
// tavanlıdır ve kesme count/total ile İFŞA edilir.

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/cilcenk/coremetry/internal/mcp"
)

type listSLOStatusArgs struct {
	Service string `json:"service,omitempty"`
	Limit   int    `json:"limit,omitempty"`
}

func listSLOStatusTool(d Deps) mcp.Tool {
	return mcp.Tool{
		Name: "list_slo_status",
		Description: "List SLOs with their definition (service, target, rolling window days, latency threshold), " +
			"live status (SLI, error-budget remaining, burn rate, healthy flag) and a DETERMINISTIC exhaustion forecast " +
			"(hours_to_exhaust, will_breach_within_24h, safe_burn). Use this to answer 'are we meeting our SLOs / which budget dies first' — " +
			"never estimate exhaustion yourself, the forecast field is the server's projection. " +
			"Each SLO costs one bounded pre-aggregate read; cheap at typical SLO counts.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"service": map[string]any{
					"type":        "string",
					"description": "Only SLOs of this service (exact name). Empty = all.",
				},
				"limit": map[string]any{
					"type":        "integer",
					"minimum":     1,
					"maximum":     100,
					"description": "Max SLOs to return. Default 50.",
				},
			},
		},
		Handler: func(ctx context.Context, raw json.RawMessage) (any, error) {
			var a listSLOStatusArgs
			if len(raw) > 0 {
				if err := json.Unmarshal(raw, &a); err != nil {
					return nil, fmt.Errorf("decode args: %w", err)
				}
			}
			limit := clampLimit(a.Limit, 50, 100)
			slos, err := d.Store.ListSLOs(ctx)
			if err != nil {
				return nil, err
			}
			total := 0
			out := make([]map[string]any, 0, limit)
			for _, o := range slos {
				if a.Service != "" && o.Service != a.Service {
					continue
				}
				total++
				if len(out) >= limit {
					continue // sayaç dolsun — kesme dürüstçe ifşa edilir
				}
				row := map[string]any{"slo": o}
				// Durum/yörünge okumaları soft-fail: tek SLO'nun okuma
				// hatası listeyi düşürmez, yokluk alan yokluğuyla görünür.
				if st, err := d.Store.ComputeSLOStatus(ctx, o); err == nil && st != nil {
					row["status"] = st
				}
				if fc, err := d.Store.ComputeSLOForecast(ctx, o, time.Hour); err == nil && fc != nil {
					row["forecast"] = map[string]any{
						"burn_rate":              fc.BurnRate,
						"hours_to_exhaust":       fc.HoursToExhaust,
						"will_breach_within_24h": fc.WillBreachWithin24h,
						"safe_burn":              fc.SafeBurn,
						"budget_remaining":       fc.BudgetRemaining,
					}
				}
				out = append(out, row)
			}
			return map[string]any{"slos": out, "count": len(out), "total": total}, nil
		},
	}
}
