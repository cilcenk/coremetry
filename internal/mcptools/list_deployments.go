package mcptools

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/cilcenk/coremetry/internal/chstore"
	"github.com/cilcenk/coremetry/internal/mcp"
)

// list_deployments.go — v0.10.545 (CoSRE agent Faz 2, ilk dikey dilimin 5.
// tool'u): "şu pencerede bu namespace'te ne deploy edildi?"
//
// Bugüne kadar cevaplanamıyordu: list_deploys SERVİS ister,
// chstore.GetDeploysInWindow (deploys.go:1120) uçsuz/tool'suzdu, rollouts
// katmanının MCP yüzeyi yoktu. Üç kaynak tek listede, her satır `source`
// etiketli: inferred (span service.version geçişi, MV), event (operatör/
// pipeline kaydı), rollout (K8s ReplicaSet, rollouts katmanı — bayrak
// kapalıysa "unavailable" notu, satır yok).
//
// DÜRÜSTLÜK: inferred/event satırları cluster/namespace TAŞIMAZ (span
// çıkarımı servis-düzeyi). namespace ya da cluster daraltması verilmişse ve
// servis verilmemişse inferred satırlar LİSTELENMEZ (filo genelini "bu
// namespace" diye sunmak yanlış olurdu); yalnız rollouts cevaplar ve payload
// bunu `scope` ile söyler. HTTP eşi: GET /api/changes (api/changes_routes.go).

// ListDeploymentsArgs — HTTP eşi de aynı yapıyı kurar.
type ListDeploymentsArgs struct {
	Cluster   string `json:"cluster,omitempty"`
	Namespace string `json:"namespace,omitempty"`
	Service   string `json:"service,omitempty"`
	RangeS    int    `json:"range_s,omitempty"`
	Limit     int    `json:"limit,omitempty"`
}

type changeRow struct {
	Source     string `json:"source"` // inferred | event | rollout
	TimeISO    string `json:"time_iso"`
	TimeUnixNs int64  `json:"time_unix_ns"`
	Cluster    string `json:"cluster,omitempty"`
	Namespace  string `json:"namespace,omitempty"`
	Workload   string `json:"workload,omitempty"`
	Service    string `json:"service,omitempty"`
	Version    string `json:"version,omitempty"` // sürüm / revizyon / imaj etiketi
	Status     string `json:"status,omitempty"`
	SpanCount  int64  `json:"span_count,omitempty"`
}

// mergeChanges — SAF birleşim: inferred/event (servis-düzeyi) + rollout
// (cluster/namespace/workload), zamana göre azalan, limit. service verildiyse
// inferred tam ad eşleşmesi, rollout workload eşleşmesi. includeInferred=false
// (namespace/cluster daraltması + servissiz) inferred/event satırlarını düşürür.
func mergeChanges(inferred []chstore.RecentDeployEntry, rollouts []chstore.RolloutRow, service string, includeInferred bool, limit int) []changeRow {
	rows := make([]changeRow, 0, len(inferred)+len(rollouts))
	if includeInferred {
		for _, d := range inferred {
			if service != "" && d.Service != service {
				continue
			}
			src := "inferred"
			if d.Source == "event" {
				src = "event"
			}
			rows = append(rows, changeRow{
				Source: src, TimeUnixNs: d.FirstSeenNs, TimeISO: time.Unix(0, d.FirstSeenNs).UTC().Format(time.RFC3339),
				Service: d.Service, Version: d.Version, SpanCount: int64(d.SpanCount),
			})
		}
	}
	for _, r := range rollouts {
		if service != "" && r.Workload != service {
			continue
		}
		ver := r.Revision
		if r.ImageTag != "" {
			ver = r.ImageTag
		}
		rows = append(rows, changeRow{
			Source: "rollout", TimeUnixNs: r.StartedAt.UnixNano(), TimeISO: r.StartedAt.UTC().Format(time.RFC3339),
			Cluster: r.ClusterID, Namespace: r.Namespace, Workload: r.Workload, Version: ver, Status: r.Status, SpanCount: r.SpanCount,
		})
	}
	sort.SliceStable(rows, func(i, j int) bool { return rows[i].TimeUnixNs > rows[j].TimeUnixNs })
	if limit > 0 && len(rows) > limit {
		rows = rows[:limit]
	}
	return rows
}

// ListDeploymentsWindow — tool ve HTTP ucunun ortak gövdesi.
func ListDeploymentsWindow(ctx context.Context, d Deps, a ListDeploymentsArgs, from, to time.Time) (map[string]any, error) {
	if d.Store == nil {
		return nil, fmt.Errorf("list_deployments: store yok")
	}
	limit := clampLimit(a.Limit, 20, 100)
	service := strings.TrimSpace(a.Service)
	scoped := strings.TrimSpace(a.Namespace) != "" || strings.TrimSpace(a.Cluster) != ""
	includeInferred := service != "" || !scoped
	var notes []string
	var inferred []chstore.RecentDeployEntry
	if includeInferred {
		deps, err := d.Store.GetDeploysInWindow(ctx, from, to, limit*2)
		if err != nil {
			return nil, err
		}
		inferred = deps
	} else {
		notes = append(notes, "namespace/cluster daraltması: span-çıkarımlı deploy'lar namespace taşımaz, yalnız rollouts katmanı listelendi (servis verirsen inferred de gelir)")
	}
	rollouts, rerr := d.Store.RolloutList(ctx, chstore.RolloutFilter{
		ClusterID: strings.TrimSpace(a.Cluster), Namespace: strings.TrimSpace(a.Namespace), Workload: service,
	}, from, to, limit)
	rolloutState := "ok"
	if rerr != nil {
		rolloutState = "unavailable"
		notes = append(notes, "rollouts katmanı erişilemedi/kapalı (Settings → Rollouts): "+rerr.Error())
		rollouts = nil
	}
	rows := mergeChanges(inferred, rollouts, service, includeInferred, limit)
	counts := map[string]int{}
	for _, r := range rows {
		counts[r.Source]++
	}
	scope := "fleet"
	switch {
	case service != "":
		scope = "service"
	case scoped:
		scope = "namespace/cluster (rollouts)"
	}
	return map[string]any{
		"rows": rows, "count": len(rows), "limit": limit,
		"from_iso": from.UTC().Format(time.RFC3339), "to_iso": to.UTC().Format(time.RFC3339),
		"window_s": int64(to.Sub(from).Seconds()),
		"scope":    scope,
		"sources":  map[string]any{"inferred": counts["inferred"], "event": counts["event"], "rollout": counts["rollout"], "rollouts_layer": rolloutState},
		"notes":    notes,
		"next":     "Bir satırın etkisi için get_deploy_diff(service, version); rollout ayrıntısı için /rollouts sayfası (build_link).",
	}, nil
}

func listDeploymentsTool(d Deps) mcp.Tool {
	return mcp.Tool{
		Name:             "list_deployments",
		ShortDescription: "Pencerede NE DEPLOY EDİLDİ: cluster/namespace/servis daraltmalı; span-çıkarımlı deploy + operatör olayı + K8s rollout tek listede (source etiketli). 'Neden arttı' sorusunda hata penceresiyle çağır.",
		Description: "List what changed in a time window: inferred deploys (service.version transitions), operator deploy events and Kubernetes rollouts, " +
			"merged newest-first with a `source` tag. Narrow by cluster and/or namespace (rollouts carry them; inferred deploys do NOT, so a namespace-only " +
			"question is answered from rollouts and the payload says so in `scope`/`notes`) or by exact service. This is the shortest path for " +
			"'why did errors rise at 14:05' — call it with the same window as the trace/metric question and correlate by time; correlation is not causation, " +
			"say so. Then hand the version to get_deploy_diff for before/after RED impact.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"cluster":   map[string]any{"type": "string", "description": "Cluster id/name (from list_clusters). Applies to rollouts."},
				"namespace": map[string]any{"type": "string", "description": "Kubernetes namespace. Applies to rollouts."},
				"service":   map[string]any{"type": "string", "description": "Exact service / workload name. Enables inferred deploys even when cluster/namespace are set."},
				"range_s":   map[string]any{"type": "integer", "minimum": 0, "maximum": 604800, "description": "Lookback seconds ending at now (or the anchored window). Default/min 21600 (6h), max 604800 (7d)."},
				"limit":     map[string]any{"type": "integer", "minimum": 1, "maximum": 100, "description": "Max rows, newest first. Default 20."},
			},
		},
		MinRole: "",
		Handler: func(ctx context.Context, raw json.RawMessage) (any, error) {
			var a ListDeploymentsArgs
			if len(raw) > 0 {
				if err := json.Unmarshal(raw, &a); err != nil {
					return nil, fmt.Errorf("decode args: %w", err)
				}
			}
			windowS := deployEnumWindowS(a.RangeS)
			now := nowOrAnchor(ctx)
			return ListDeploymentsWindow(ctx, d, a, now.Add(-time.Duration(windowS)*time.Second), now)
		},
	}
}
