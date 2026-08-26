package mcptools

// operation_health.go — get_operation_health (v0.9.1227, CoSRE denetimi
// [5/S] bulgusu): katalogda ENDPOINT-bazlı sayı taşıyan tek okuma yoktu.
// list_operations bilinçli bir AD kataloğu (sayısız, ~90 gün); RED
// sorusu ("hangi endpoint yavaş/hatalı?") ancak render_chart ile
// GÖRSEL cevaplanıyordu — model üzerine akıl yürütecek sayı almıyordu.
//
// Okuma /endpoints sayfasının aynısı: chstore.GetEndpoints, Service-
// only sorguda spanmetrics_1m MV yolu (forcesRaw yok). Satırlar
// guided_parity doktrini: chstore tipi DEĞİL — alt küme + snake_case
// (UI tipinin doldurulmayan 20+ alanı modelde "ölçüm 0" okunur).

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/cilcenk/coremetry/internal/chstore"
	"github.com/cilcenk/coremetry/internal/mcp"
)

const (
	// opBucketS — spanmetrics_1m'in kova boyutu; range_s'in tabanı
	// (depBucketS gerekçesiyle aynı: daha dar pencere yine tam kova okur).
	opBucketS             = 60
	opHealthDefaultRangeS = 3600
	opHealthMaxRangeS     = 7 * 86400
	opHealthDefaultRows   = 20
	opHealthMaxRows       = 50
)

// opHealthWindowS — range_s → efektif saniye. Saf, tablo testli.
func opHealthWindowS(rangeS int) int {
	if rangeS <= 0 {
		return opHealthDefaultRangeS
	}
	if rangeS < opBucketS {
		return opBucketS
	}
	if rangeS > opHealthMaxRangeS {
		return opHealthMaxRangeS
	}
	return rangeS
}

// opHealthSort — tool enum'u → endpointsOrderBy UI id'si. Girdi SQL'e
// asla değmez: bilinmeyen değer calls'a düşer (whitelist zaten
// chstore tarafında da var; bu eşleme yalnız enum'u dar tutar). Saf.
func opHealthSort(s string) string {
	switch s {
	case "p99":
		return "p99Ms"
	case "error_rate":
		return "errorRate"
	case "impact":
		return "impact"
	default:
		return "calls"
	}
}

// OperationHealthRow — bir (path, method) satırı. Ondalıklar HAM
// (DBHealthRow gerekçesi: biçim tüketicinin işi).
type OperationHealthRow struct {
	Path   string `json:"path"`
	Method string `json:"method,omitempty"`
	Calls  uint64 `json:"calls"`
	Errors uint64 `json:"errors"`
	// ErrorRatePct 0..100.
	ErrorRatePct float64 `json:"error_rate_pct"`
	AvgMs        float64 `json:"avg_ms"`
	P50Ms        float64 `json:"p50_ms"`
	P95Ms        float64 `json:"p95_ms"`
	P99Ms        float64 `json:"p99_ms"`
	ReqPerMin    float64 `json:"req_per_min"`
}

// opHealthRows — chstore satırları → tool satırları. SAF, tablo testli.
// Sıralama chstore'dan geldiği gibi korunur (server-side whitelisted
// ORDER BY zaten uygulandı); burada yeniden sıralamak "top by p99"
// isteğini sessizce bozardı.
func opHealthRows(in []chstore.EndpointRow) []OperationHealthRow {
	out := make([]OperationHealthRow, 0, len(in))
	for _, r := range in {
		out = append(out, OperationHealthRow{
			Path:         r.Path,
			Method:       r.Method,
			Calls:        r.Calls,
			Errors:       r.Errors,
			ErrorRatePct: r.ErrorRate,
			AvgMs:        r.AvgMs,
			P50Ms:        r.P50Ms,
			P95Ms:        r.P95Ms,
			P99Ms:        r.P99Ms,
			ReqPerMin:    r.ReqPerMin,
		})
	}
	return out
}

type getOperationHealthArgs struct {
	Service string `json:"service"`
	RangeS  int    `json:"range_s,omitempty"`
	Sort    string `json:"sort,omitempty"`
	Limit   int    `json:"limit,omitempty"`
}

func getOperationHealthTool(d Deps) mcp.Tool {
	return mcp.Tool{
		Name:             "get_operation_health",
		ShortDescription: "Tek servisin ENDPOINT bazlı RED'i ((path, method) başına çağrı/hata/p50-p95-p99). 'Servisin NERESİ yavaş' sorusu; list_operations sayı vermez, bu verir.",
		Description: "Answer 'which ENDPOINT of this service is slow / erroring' — per-(path, method) RED rows for ONE service from the 1-minute " +
			"pre-aggregate: calls, errors, error rate, avg and p50/p95/p99 latency plus req/min throughput, ordered by `sort`. " +
			"This is the numeric companion to list_operations (which is a name catalogue with NO numbers) and to get_service_health (which is one " +
			"service-level row): use it after get_service_health says a service is unhealthy, to find WHERE inside it. " +
			"COST: one pre-aggregate read, cheap. BOUNDS: the window snaps to the 1-minute bucket; rows beyond `limit` are dropped and truncated=true " +
			"says so. An empty list is NOT an error (wrong service name, or the service has no http_route-bearing inbound spans — gRPC/consumer-only " +
			"services list under other surfaces); verify the name with list_services first. Never invent an endpoint path.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"service": map[string]any{
					"type":        "string",
					"description": "Exact service name (as list_services returns it). Required — endpoint rows are per-service.",
				},
				"range_s": map[string]any{
					"type":        "integer",
					"minimum":     0,
					"maximum":     opHealthMaxRangeS,
					"description": "Lookback seconds. Default 3600 (1h), max 604800 (7d). Minimum effective value 60 (the aggregate's 1-minute bucket).",
				},
				"sort": map[string]any{
					"type":        "string",
					"enum":        []string{"calls", "p99", "error_rate", "impact"},
					"description": "Row order, descending. Default calls (busiest first). impact = calls × p99 × (1 + error rate) — 'fix me first'.",
				},
				"limit": map[string]any{
					"type":        "integer",
					"minimum":     1,
					"maximum":     opHealthMaxRows,
					"description": "Max endpoint rows. Default 20, max 50.",
				},
			},
			"required": []string{"service"},
		},
		// Salt-okunur; REST eşi GET /api/endpoints viewer'a açık.
		MinRole: "",
		Handler: func(ctx context.Context, raw json.RawMessage) (any, error) {
			var a getOperationHealthArgs
			if len(raw) > 0 {
				if err := json.Unmarshal(raw, &a); err != nil {
					return nil, fmt.Errorf("decode args: %w", err)
				}
			}
			if a.Service == "" {
				return nil, fmt.Errorf("service is required — get it from list_services")
			}
			windowS := opHealthWindowS(a.RangeS)
			from, to := rangeWindow(ctx, windowS)
			limit := clampLimit(a.Limit, opHealthDefaultRows, opHealthMaxRows)
			// limit+1: truncated bayrağı için bir fazla iste — chstore
			// LIMIT'i sorguda uygular, "kesildi mi"yi başka türlü bilemeyiz.
			rows, err := d.Store.GetEndpoints(ctx, chstore.EndpointsQuery{
				From: from, To: to,
				Service: a.Service,
				Limit:   limit + 1,
				Sort:    opHealthSort(a.Sort),
				Dir:     "desc",
			})
			if err != nil {
				return nil, err
			}
			truncated := false
			if len(rows) > limit {
				rows = rows[:limit]
				truncated = true
			}
			out := map[string]any{
				"service":   a.Service,
				"window_s":  windowS,
				"sort":      opHealthSort(a.Sort),
				"rows":      opHealthRows(rows),
				"count":     len(rows),
				"truncated": truncated,
			}
			if len(rows) == 0 {
				out["reasons"] = []string{
					"service name may be wrong — verify with list_services",
					"service may have no http_route-bearing inbound spans in this window (gRPC/consumer-only)",
				}
			}
			return out, nil
		},
	}
}
