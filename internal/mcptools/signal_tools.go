package mcptools

// signal_tools.go — v0.10.556 (CoSRE v2 Faz 4b, docs/audit/cosre-agent-v2.md §Faz 4
// madde 3): iki sinyal aracı.
//
//   log_patterns   — /api/logs/patterns paritesi: anomaly.DetectLogPatterns
//                    (ÖRNEKLEM tabanlı, pencere basamaklı, sessize alınmış
//                    parmak izleri düşer); "yeni" ve "patlayan" log desenleri.
//   cluster_metric — Thanos SABİT handler'larının parametrik aynası
//                    (pod | namespace | deploy | network); ham PromQL YOK,
//                    kalkanlar handler tarafında (cluster çözümü, 30 g pencere
//                    tavanı, 10 s zaman aşımı, nokta basamağı) — ClusterMetricReader
//                    adaptörü api/mcp_deps.go'da. Thanos yoksa disabled döner.
//
// Bağlam bütçesi: desen listesi ≤50 satır (örnek gövde 200 rune), trend
// noktaları ≤60 (seyreltme her k'ıncı nokta; toplam ilan edilir).

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/cilcenk/coremetry/internal/anomaly"
	"github.com/cilcenk/coremetry/internal/chstore"
	"github.com/cilcenk/coremetry/internal/mcp"
	"github.com/cilcenk/coremetry/internal/thanos"
)

// ClusterMetricReader — Thanos okumalarının MCP'ye açılan dar yüzü. Cluster
// bir REFERANSTIR (id ya da ad); adaptör çözer, pencereyi kırpar, zaman aşımı
// koyar. Uygulama api/mcp_deps.go (mcpClusterMetrics).
type ClusterMetricReader interface {
	PodTrend(ctx context.Context, cluster, namespace, pod string, from, to time.Time) ([]thanos.TrendPoint, error)
	NamespaceTrend(ctx context.Context, cluster, namespace string, from, to time.Time) ([]thanos.TrendPoint, error)
	DeployTrend(ctx context.Context, cluster, namespace, deploy, metric string, byPod bool, from, to time.Time, mdp int) ([]thanos.NamedSeries, int, error)
	NetworkTrend(ctx context.Context, cluster string, from, to time.Time) ([]thanos.NetTrendPoint, error)
}

const (
	logPatternsDefault   = 20
	logPatternsMax       = 50
	logPatternSampleMax  = 200
	clusterMetricPoints  = 60
	clusterMetricRangeMx = 7 * 24 * 3600
)

// logPatternWindowRungs — api/anomaly_window.go ile aynı basamaklar (ES cost:
// pencere cache anahtarına girer, kardinalite sınırlı).
var logPatternWindowRungs = []time.Duration{time.Minute, 5 * time.Minute, 15 * time.Minute, 30 * time.Minute}

func snapLogPatternWindow(sec int) time.Duration {
	d := time.Duration(sec) * time.Second
	if d <= 0 {
		return 5 * time.Minute
	}
	for _, r := range logPatternWindowRungs {
		if d <= r {
			return r
		}
	}
	return logPatternWindowRungs[len(logPatternWindowRungs)-1]
}

type logPatternsArgs struct {
	WindowS int    `json:"window_s,omitempty"`
	Service string `json:"service,omitempty"`
	Kind    string `json:"kind,omitempty"`
	Limit   int    `json:"limit,omitempty"`
}

// filterLogPatterns — SAF: servis (ana ya da topServices'te) + tür süzgeci,
// sessize alınmış parmak izleri düşer, oran azalan, tavan.
func filterLogPatterns(hits []anomaly.LogPatternAnomaly, service, kind string, muted map[string]bool, limit int) ([]anomaly.LogPatternAnomaly, int) {
	out := make([]anomaly.LogPatternAnomaly, 0, len(hits))
	for _, h := range hits {
		if muted[chstore.FingerprintAnomaly("log_pattern", h.Pattern, h.Service)] {
			continue
		}
		if kind != "" && kind != "all" && h.Kind != kind {
			continue
		}
		if service != "" && h.Service != service {
			found := false
			for _, ts := range h.TopServices {
				if ts.Service == service {
					found = true
					break
				}
			}
			if !found {
				continue
			}
		}
		h.Sample = truncateRunes(h.Sample, logPatternSampleMax)
		out = append(out, h)
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Ratio > out[j].Ratio })
	total := len(out)
	if len(out) > limit {
		out = out[:limit]
	}
	return out, total
}

func logPatternsTool(d Deps) mcp.Tool {
	return mcp.Tool{
		Name:             "log_patterns",
		ShortDescription: "Son pencerede yeni/patlayan log desenleri (örneklem; servis süzgeci).",
		Description:      "Return log patterns that are NEW (absent from the trailing baseline) or SPIKING (ratio vs baseline) in the last window — the same detector the /logs page strip uses: pattern name, kind new|spike, current/baseline counts, ratio, dominant service, per-service breakdown and one sample body (200 chars). Use it to answer 'what changed in the logs' before search_logs; filter with service. SAMPLE-BASED (Drain templating over a bounded sample, never a full scan) and cached 60 s server-side; window snaps to 1/5/15/30 min rungs. Silenced patterns are excluded. Rows sorted by ratio; default 20, max 50.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"window_s": map[string]any{"type": "integer", "minimum": 60, "maximum": 1800, "description": "Current window in seconds; snaps to 60/300/900/1800. Default 300."},
				"service":  map[string]any{"type": "string", "description": "Keep patterns whose dominant or top services include this service. Empty = all."},
				"kind":     map[string]any{"type": "string", "enum": []string{"all", "new", "spike"}, "description": "new = absent from baseline, spike = ratio jump. Default all."},
				"limit":    map[string]any{"type": "integer", "minimum": 1, "maximum": logPatternsMax, "description": "Rows. Default 20, max 50."},
			},
		},
		Handler: func(ctx context.Context, raw json.RawMessage) (any, error) {
			var a logPatternsArgs
			if len(raw) > 0 {
				if err := json.Unmarshal(raw, &a); err != nil {
					return nil, fmt.Errorf("decode args: %w", err)
				}
			}
			if d.LogStore == nil {
				return map[string]any{"disabled": true, "hint": "Log deposu yapılandırılmamış."}, nil
			}
			kind := strings.ToLower(strings.TrimSpace(a.Kind))
			if kind != "" && kind != "all" && kind != "new" && kind != "spike" {
				return nil, fmt.Errorf("kind must be all | new | spike")
			}
			window := snapLogPatternWindow(a.WindowS)
			limit := clampLimit(a.Limit, logPatternsDefault, logPatternsMax)
			hits, err := anomaly.DetectLogPatterns(ctx, d.LogStore, window)
			if err != nil {
				return nil, err
			}
			var muted map[string]bool
			if d.Store != nil {
				muted, _ = d.Store.ActiveSilencedFingerprints(ctx)
			}
			rows, total := filterLogPatterns(hits, strings.TrimSpace(a.Service), kind, muted, limit)
			return map[string]any{
				"windowS": int(window.Seconds()), "service": a.Service, "kind": kind,
				"rows": rows, "count": len(rows), "total": total,
				"note": "Sample-based detector (Drain over a bounded sample); counts are estimates of the sampled window, not exact totals.",
			}, nil
		},
	}
}

type clusterMetricArgs struct {
	Kind          string `json:"kind"`
	Cluster       string `json:"cluster"`
	Namespace     string `json:"namespace,omitempty"`
	Pod           string `json:"pod,omitempty"`
	Workload      string `json:"workload,omitempty"`
	Metric        string `json:"metric,omitempty"`
	ByPod         bool   `json:"by_pod,omitempty"`
	RangeS        int    `json:"range_s,omitempty"`
	MaxDataPoints int    `json:"max_data_points,omitempty"`
}

// thinEvery — SAF: ≤max nokta, eşit adımlı seyreltme (ilk ve son korunur).
func thinEvery[T any](xs []T, max int) []T {
	if max <= 0 || len(xs) <= max {
		return xs
	}
	out := make([]T, 0, max)
	step := float64(len(xs)-1) / float64(max-1)
	for i := 0; i < max; i++ {
		out = append(out, xs[int(float64(i)*step+0.5)])
	}
	return out
}

// validateClusterMetricArgs — SAF: tür başına zorunlu alanlar, metrik enum'u.
func validateClusterMetricArgs(a clusterMetricArgs) error {
	if strings.TrimSpace(a.Cluster) == "" {
		return fmt.Errorf("cluster is required (id or name from get_capabilities / list_clusters)")
	}
	switch a.Kind {
	case "pod":
		if a.Namespace == "" || a.Pod == "" {
			return fmt.Errorf("kind=pod requires namespace and pod")
		}
	case "namespace":
		if a.Namespace == "" {
			return fmt.Errorf("kind=namespace requires namespace")
		}
	case "deploy":
		if a.Namespace == "" || a.Workload == "" {
			return fmt.Errorf("kind=deploy requires namespace and workload (deployment name)")
		}
		switch a.Metric {
		case "", "cpu", "mem", "netin", "netout":
		default:
			return fmt.Errorf("metric must be cpu | mem | netin | netout")
		}
	case "network":
	default:
		return fmt.Errorf("kind must be pod | namespace | deploy | network")
	}
	if a.RangeS < 0 || a.RangeS > clusterMetricRangeMx {
		return fmt.Errorf("range_s must be 0..%d", clusterMetricRangeMx)
	}
	return nil
}

func clusterMetricTool(d Deps) mcp.Tool {
	return mcp.Tool{
		Name:             "cluster_metric",
		ShortDescription: "Thanos pod/namespace/deployment CPU-bellek ya da cluster ağ trendi; ham PromQL yok.",
		Description:      "Read Kubernetes resource trends from the configured Thanos clusters WITHOUT writing PromQL — a parametric mirror of the fixed /api/clusters handlers: kind=pod (namespace+pod → CPU cores + memory bytes per bucket), kind=namespace (namespace totals), kind=deploy (namespace+workload → cpu|mem|netin|netout, optionally by_pod → one series per pod, server-capped), kind=network (cluster in/out bytes/s). cluster is the Remote Cluster id or name (get_capabilities → thanos lists names). Window range_s ≤ 7 days (server clamps to its own 30-day ceiling), points thinned to ≤60 per series; totals are reported. Use after get_correlation_evidence names affected pods / rollouts, or for 'is the pod saturated' questions. Returns disabled=true when no Thanos cluster is configured.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"kind":            map[string]any{"type": "string", "enum": []string{"pod", "namespace", "deploy", "network"}, "description": "Which trend. Required."},
				"cluster":         map[string]any{"type": "string", "description": "Remote Cluster id or name. Required."},
				"namespace":       map[string]any{"type": "string", "description": "K8s namespace (pod / namespace / deploy)."},
				"pod":             map[string]any{"type": "string", "description": "Pod name (kind=pod)."},
				"workload":        map[string]any{"type": "string", "description": "Deployment name (kind=deploy)."},
				"metric":          map[string]any{"type": "string", "enum": []string{"cpu", "mem", "netin", "netout"}, "description": "kind=deploy only. Default cpu."},
				"by_pod":          map[string]any{"type": "boolean", "description": "kind=deploy: one series per pod instead of the total."},
				"range_s":         map[string]any{"type": "integer", "minimum": 0, "maximum": clusterMetricRangeMx, "description": "Lookback seconds. Default 3600."},
				"max_data_points": map[string]any{"type": "integer", "minimum": 1, "maximum": clusterMetricPoints, "description": "Points per series (snapped to server rungs). Default 60."},
			},
			"required": []string{"kind", "cluster"},
		},
		Handler: func(ctx context.Context, raw json.RawMessage) (any, error) {
			var a clusterMetricArgs
			if len(raw) > 0 {
				if err := json.Unmarshal(raw, &a); err != nil {
					return nil, fmt.Errorf("decode args: %w", err)
				}
			}
			if d.ClusterMetrics == nil || d.Clusters == nil || len(d.Clusters()) == 0 {
				return map[string]any{"disabled": true, "hint": "Thanos Remote Cluster yapılandırılmamış (Settings → Remote clusters); pod/namespace metrikleri bu kurulumda okunamıyor."}, nil
			}
			if err := validateClusterMetricArgs(a); err != nil {
				return nil, err
			}
			from, to := rangeWindow(ctx, a.RangeS)
			if a.RangeS == 0 {
				from = to.Add(-time.Hour)
			}
			mdp := a.MaxDataPoints
			if mdp <= 0 || mdp > clusterMetricPoints {
				mdp = clusterMetricPoints
			}
			out := map[string]any{"kind": a.Kind, "cluster": a.Cluster, "fromNs": from.UnixNano(), "toNs": to.UnixNano()}
			switch a.Kind {
			case "pod":
				pts, err := d.ClusterMetrics.PodTrend(ctx, a.Cluster, a.Namespace, a.Pod, from, to)
				if err != nil {
					return nil, err
				}
				out["namespace"], out["pod"] = a.Namespace, a.Pod
				out["points"], out["totalPoints"] = thinEvery(pts, mdp), len(pts)
			case "namespace":
				pts, err := d.ClusterMetrics.NamespaceTrend(ctx, a.Cluster, a.Namespace, from, to)
				if err != nil {
					return nil, err
				}
				out["namespace"] = a.Namespace
				out["points"], out["totalPoints"] = thinEvery(pts, mdp), len(pts)
			case "deploy":
				metric := a.Metric
				if metric == "" {
					metric = "cpu"
				}
				series, total, err := d.ClusterMetrics.DeployTrend(ctx, a.Cluster, a.Namespace, a.Workload, metric, a.ByPod, from, to, mdp)
				if err != nil {
					return nil, err
				}
				for i := range series {
					series[i].Points = thinEvery(series[i].Points, mdp)
				}
				out["namespace"], out["workload"], out["metric"], out["byPod"] = a.Namespace, a.Workload, metric, a.ByPod
				out["series"], out["count"] = series, len(series)
				if total > len(series) {
					out["totalSeries"] = total
				}
			case "network":
				pts, err := d.ClusterMetrics.NetworkTrend(ctx, a.Cluster, from, to)
				if err != nil {
					return nil, err
				}
				out["points"], out["totalPoints"] = thinEvery(pts, mdp), len(pts)
			}
			return out, nil
		},
	}
}
