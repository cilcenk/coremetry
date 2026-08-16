package mcptools

// Faz 4'ün son iki köprüsü (v0.9.1092):
//
//   - get_correlated_changes — "o anda başka ne değişti" sorusunun MV
//     okuması (GetCorrelatedChangesMV, v0.9.1062'de ham taramadan MV'ye
//     geçti — Faz 2.1; bu tool o ucuzlamanın MCP karşılığı).
//   - get_deploy_diff — bir deploy'un önce/sonra RED kıyası
//     (ComputeDeployImpact: p99/avg/err/rate deltaları). Model "deploy
//     mu bozdu"ya artık kendi pencere aritmetiğini kurmadan cevap verir.

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/cilcenk/coremetry/internal/mcp"
)

type getCorrelatedChangesArgs struct {
	AtISO     string `json:"at_iso,omitempty"`
	WindowS   int    `json:"window_s,omitempty"`
	BaselineS int    `json:"baseline_s,omitempty"`
}

func getCorrelatedChangesTool(d Deps) mcp.Tool {
	return mcp.Tool{
		Name: "get_correlated_changes",
		Description: "Rank services whose traffic/error-rate/p99 CHANGED most in a window vs the preceding baseline " +
			"(per service: baseline vs current rate, error rate, p99 + signed delta percentages and a combined score). " +
			"Use it to answer 'what else changed around this incident' — pass at_iso = the incident start. " +
			"Reads the 5-minute pre-aggregate, so it is cheap; window_s is clamped to [300, 21600] (the aggregate's floor/budget).",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"at_iso": map[string]any{
					"type":        "string",
					"description": "Window START as RFC3339 (e.g. incident start). Empty = now - window_s.",
				},
				"window_s": map[string]any{
					"type":        "integer",
					"minimum":     300,
					"maximum":     21600,
					"description": "Comparison window seconds starting at at_iso. Default 600.",
				},
				"baseline_s": map[string]any{
					"type":        "integer",
					"minimum":     300,
					"maximum":     86400,
					"description": "Baseline seconds BEFORE the window. Default 4× window_s.",
				},
			},
		},
		Handler: func(ctx context.Context, raw json.RawMessage) (any, error) {
			var a getCorrelatedChangesArgs
			if len(raw) > 0 {
				if err := json.Unmarshal(raw, &a); err != nil {
					return nil, fmt.Errorf("decode args: %w", err)
				}
			}
			window := a.WindowS
			if window <= 0 {
				window = 600
			}
			if window < 300 {
				window = 300 // MV tabanı — 5dk altı bu aggregate'te yok
			}
			if window > 21600 {
				window = 21600
			}
			baseline := a.BaselineS
			if baseline <= 0 {
				baseline = window * 4
			}
			at := time.Now().Add(-time.Duration(window) * time.Second)
			if a.AtISO != "" {
				t, err := time.Parse(time.RFC3339, a.AtISO)
				if err != nil {
					return nil, fmt.Errorf("at_iso RFC3339 değil: %w", err)
				}
				at = t
			}
			changes, err := d.Store.GetCorrelatedChangesMV(ctx, at, window, baseline)
			if err != nil {
				return nil, err
			}
			return map[string]any{
				"changes":    changes,
				"count":      len(changes),
				"window_s":   window,
				"baseline_s": baseline,
				"at_iso":     at.UTC().Format(time.RFC3339),
			}, nil
		},
	}
}

type getDeployDiffArgs struct {
	Service string `json:"service"`
	Version string `json:"version,omitempty"`
	WindowS int    `json:"window_s,omitempty"`
	RangeS  int    `json:"range_s,omitempty"`
}

func getDeployDiffTool(d Deps) mcp.Tool {
	return mcp.Tool{
		Name: "get_deploy_diff",
		Description: "Before/after RED comparison for one deploy of a service: p99, avg latency, error rate and " +
			"request rate over a symmetric window around the version's first-seen time, with signed delta percentages " +
			"(positive = worse). Use after list_problems/get_correlated_changes point at a service to answer 'did the deploy break it'. " +
			"Omit version to diff the NEWEST deploy found in range_s. Bounded span scan around a single timestamp — keep window_s modest.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"service": map[string]any{
					"type":        "string",
					"description": "Exact service name (from list_services). Required.",
				},
				"version": map[string]any{
					"type":        "string",
					"description": "Deploy version to diff. Empty = the newest deploy in range_s.",
				},
				"window_s": map[string]any{
					"type":        "integer",
					"minimum":     60,
					"maximum":     21600,
					"description": "Before/after window seconds on EACH side of the deploy. Default 600.",
				},
				"range_s": map[string]any{
					"type":        "integer",
					"minimum":     0,
					"maximum":     604800,
					"description": "How far back to look for the deploy marker. Default 86400 (24h).",
				},
			},
			"required": []string{"service"},
		},
		Handler: func(ctx context.Context, raw json.RawMessage) (any, error) {
			var a getDeployDiffArgs
			if len(raw) > 0 {
				if err := json.Unmarshal(raw, &a); err != nil {
					return nil, fmt.Errorf("decode args: %w", err)
				}
			}
			if a.Service == "" {
				return nil, fmt.Errorf("service zorunlu — önce list_services")
			}
			rangeS := a.RangeS
			if rangeS <= 0 {
				rangeS = 86400
			}
			to := time.Now()
			from := to.Add(-time.Duration(rangeS) * time.Second)
			deploys, err := d.Store.GetServiceDeploys(ctx, a.Service, from, to)
			if err != nil {
				return nil, err
			}
			if len(deploys) == 0 {
				return map[string]any{
					"found": false,
					"note":  fmt.Sprintf("pencerede %s için deploy marker'ı yok (placeholder sürümler elenir)", a.Service),
				}, nil
			}
			// GetServiceDeploys zaman sıralı döner; istenen sürümü ara,
			// yoksa EN YENİsini al.
			pick := deploys[len(deploys)-1]
			if a.Version != "" {
				found := false
				for _, dep := range deploys {
					if dep.Version == a.Version {
						pick, found = dep, true
						break
					}
				}
				if !found {
					return map[string]any{
						"found": false,
						"note":  fmt.Sprintf("sürüm %q pencerede yok; mevcutlar için version'ı boş bırak", a.Version),
					}, nil
				}
			}
			impact, err := d.Store.ComputeDeployImpact(ctx, a.Service, pick.Version, pick.TimeUnixNs, a.WindowS)
			if err != nil {
				return nil, err
			}
			return map[string]any{"found": true, "impact": impact}, nil
		},
	}
}
