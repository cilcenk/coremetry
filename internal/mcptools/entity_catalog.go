package mcptools

// entity_catalog.go — v0.10.468 (CoSRE Telemetry Agent Faz 2, F2-1): varlık
// KATALOĞU tool'ları — list_namespaces / list_workloads / list_pods. Kaynak
// varlık katmanı (entities / entity_relations, 60 s Thanos+span syncer) +
// entity_seen_5m (telemetri tarafı). Canlı Thanos sorgusu YOK.
//
// Sözleşme (audit §4): bayrak kapalıysa DÜRÜST {disabled:true} (hata değil,
// uydurma değil); cluster verilmezse tüm etkin cluster'lar, satır başına
// cluster; zaman penceresi telemetri sütunları için (varsayılan 30 dk); her
// okuma LIMIT + max_execution_time (chstore/entity_catalog.go). "Telemetri
// yok" ile "workload yok" AYRI alanlar: telemetry=false + source=thanos ⇒
// workload var, span göndermiyor; kataloğun sadece span kaynaklı olması
// (Thanos yok) ⇒ workload listesi boş, pods_without_workload dolu.

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/cilcenk/coremetry/internal/chstore"
	"github.com/cilcenk/coremetry/internal/entity"
	"github.com/cilcenk/coremetry/internal/mcp"
)

// ClusterRef — Remote Cluster kimliği, thanos paketi import edilmeden
// (api → mcptools yönü; mcp_deps.go doldurur).
type ClusterRef struct {
	ID              string
	Name            string
	SpanValues      []string // span `cluster` kolonundaki değerler (SpanClusterKeys)
	NamespaceFilter string
}

const entityCatalogLimit = 200

func entityDisabled() map[string]any {
	return map[string]any{
		"disabled": true,
		"hint":     "Varlık katmanı kapalı (Settings → Entities). Cluster/namespace/workload kataloğu bu kurulumda okunamıyor; servis düzeyinde list_services kullan.",
	}
}

func entityLayerOn(d Deps) bool { return d.EntityEnabled != nil && d.EntityEnabled() }

// clustersFor — ref boş → tümü; değilse id / ad / span değeri (harfe duyarsız).
func clustersFor(d Deps, ref string) ([]ClusterRef, bool) {
	var all []ClusterRef
	if d.Clusters != nil {
		all = d.Clusters()
	}
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return all, true
	}
	for _, c := range all {
		if strings.EqualFold(c.ID, ref) || strings.EqualFold(c.Name, ref) {
			return []ClusterRef{c}, true
		}
		for _, v := range c.SpanValues {
			if strings.EqualFold(v, ref) {
				return []ClusterRef{c}, true
			}
		}
	}
	return nil, false
}

func clusterNames(cs []ClusterRef) []string {
	out := make([]string, 0, len(cs))
	for _, c := range cs {
		out = append(out, c.Name)
	}
	return out
}

func unknownClusterErr(d Deps, ref string) error {
	all, _ := clustersFor(d, "")
	return fmt.Errorf("cluster %q yapılandırılmış Remote Cluster'lara oturmuyor — adaylar: %s (list_clusters)", ref, strings.Join(clusterNames(all), ", "))
}

// ─── list_namespaces ───────────────────────────────────────────

type listNamespacesArgs struct {
	Cluster string `json:"cluster,omitempty"`
	Query   string `json:"query,omitempty"`
	Limit   int    `json:"limit,omitempty"`
}

type NamespaceRow struct {
	Cluster   string `json:"cluster"`
	ClusterID string `json:"cluster_id"`
	Namespace string `json:"namespace"`
	Workloads int    `json:"workloads"`
	Pods      int    `json:"pods"`
	Source    string `json:"source"`
	LastSeen  string `json:"last_seen"`
}

// namespaceRows — SAF birleştirme: namespace kayıtları + sayımlar → satırlar.
func namespaceRows(c ClusterRef, recs []chstore.EntityRecord, wl, pods map[string]int) []NamespaceRow {
	out := make([]NamespaceRow, 0, len(recs))
	for _, r := range recs {
		out = append(out, NamespaceRow{
			Cluster: c.Name, ClusterID: c.ID, Namespace: r.Name,
			Workloads: wl[r.Name], Pods: pods[r.Name], Source: r.Source,
			LastSeen: r.LastSeen.UTC().Format(time.RFC3339),
		})
	}
	return out
}

// fuzzyPick — alt-dize (ILIKE) sonuç vermezse ad listesinde bulanık eşleşme
// (NearNames); q boşsa dokunmaz.
func fuzzyPick(q string, recs []chstore.EntityRecord) []chstore.EntityRecord {
	if strings.TrimSpace(q) == "" || len(recs) == 0 {
		return recs
	}
	names := make([]string, 0, len(recs))
	for _, r := range recs {
		names = append(names, r.Name)
	}
	keep := map[string]bool{}
	for _, n := range NearNames(q, names, 20) {
		keep[n] = true
	}
	out := recs[:0:0]
	for _, r := range recs {
		if keep[r.Name] {
			out = append(out, r)
		}
	}
	return out
}

func listNamespacesTool(d Deps) mcp.Tool {
	return mcp.Tool{
		Name:             "list_namespaces",
		ShortDescription: "K8s namespace kataloğu (cluster başına workload/pod sayısı). 'X namespace'i hangi cluster'da?' sorusunun ilk adımı; ad yaklaşıksa bulanık eşleşir.",
		Description: "List Kubernetes namespaces from the entity catalogue (kube-state-metrics via Thanos + spans, refreshed every ~60s), one row per cluster × namespace " +
			"with workload and pod counts. Use it before list_workloads / list_pods when the operator names a namespace in free text: pass the phrase VERBATIM as `query` — " +
			"substring match first, then token/fuzzy fallback (\"shop pay\" → \"shop-payment\"). The same namespace name may exist in several clusters; " +
			"say which cluster in your answer. Rows are catalogue truth (a namespace with pods=0 exists but is empty); telemetry presence is NOT implied — use list_workloads. " +
			"Returns {disabled:true} when the entity layer is switched off.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"cluster": map[string]any{"type": "string", "description": "Remote Cluster id, name or span cluster value (from list_clusters). Empty = all enabled clusters."},
				"query":   map[string]any{"type": "string", "description": "Namespace name or fragment, verbatim from the operator. Empty = all."},
				"limit":   map[string]any{"type": "integer", "minimum": 1, "maximum": 500, "description": "Max rows per cluster. Default 200."},
			},
		},
		MinRole: "",
		Handler: func(ctx context.Context, raw json.RawMessage) (any, error) {
			var a listNamespacesArgs
			if len(raw) > 0 {
				if err := json.Unmarshal(raw, &a); err != nil {
					return nil, fmt.Errorf("decode args: %w", err)
				}
			}
			if !entityLayerOn(d) {
				return entityDisabled(), nil
			}
			rows, searched, err := ReadNamespaces(ctx, d, a.Cluster, a.Query, clampLimit(a.Limit, entityCatalogLimit, 500))
			if err != nil {
				return nil, err
			}
			res := map[string]any{"namespaces": rows, "count": len(rows), "clusters_searched": searched}
			if len(searched) == 0 {
				res["hint"] = "Yapılandırılmış etkin Remote Cluster yok (Settings → Remote Clusters)."
			}
			return res, nil
		},
	}
}

// ReadNamespaces — v0.10.470 (F2-3): list_namespaces'in okuma çekirdeği,
// guided rota ile paylaşılır. Bayrak kapısı çağıranda.
func ReadNamespaces(ctx context.Context, d Deps, cluster, query string, limit int) ([]NamespaceRow, []string, error) {
	clusters, ok := clustersFor(d, cluster)
	if !ok {
		return nil, nil, unknownClusterErr(d, cluster)
	}
	if limit <= 0 {
		limit = entityCatalogLimit
	}
	rows := []NamespaceRow{}
	for _, c := range clusters {
		recs, err := d.Store.EntityList(ctx, chstore.EntityListQuery{ClusterID: c.ID, Type: entity.TypeNamespace, Search: query, Limit: limit})
		if err != nil {
			return nil, nil, err
		}
		if len(recs) == 0 && strings.TrimSpace(query) != "" {
			all, err := d.Store.EntityList(ctx, chstore.EntityListQuery{ClusterID: c.ID, Type: entity.TypeNamespace, Limit: 500})
			if err != nil {
				return nil, nil, err
			}
			recs = fuzzyPick(query, all)
		}
		if len(recs) == 0 {
			continue
		}
		wl, err := d.Store.EntityCountsByNamespace(ctx, c.ID, entity.TypeWorkload, time.Time{})
		if err != nil {
			return nil, nil, err
		}
		pods, err := d.Store.EntityCountsByNamespace(ctx, c.ID, entity.TypePod, time.Time{})
		if err != nil {
			return nil, nil, err
		}
		rows = append(rows, namespaceRows(c, recs, wl, pods)...)
	}
	sort.SliceStable(rows, func(i, j int) bool {
		if rows[i].Namespace != rows[j].Namespace {
			return rows[i].Namespace < rows[j].Namespace
		}
		return rows[i].Cluster < rows[j].Cluster
	})
	return rows, clusterNames(clusters), nil
}

// EntityLayerOn — dışa açık bayrak kapısı (guided rota).
func EntityLayerOn(d Deps) bool { return entityLayerOn(d) }

// ─── list_workloads ────────────────────────────────────────────

type listWorkloadsArgs struct {
	Namespace string `json:"namespace"`
	Cluster   string `json:"cluster,omitempty"`
	Kind      string `json:"kind,omitempty"`
	Query     string `json:"query,omitempty"`
	RangeS    int    `json:"range_s,omitempty"`
}

type WorkloadRow struct {
	Cluster   string   `json:"cluster"`
	ClusterID string   `json:"cluster_id"`
	Namespace string   `json:"namespace"`
	Workload  string   `json:"workload"`
	Kind      string   `json:"kind"`
	Pods      int      `json:"pods"`
	Services  []string `json:"services"`
	Telemetry bool     `json:"telemetry"`
	Spans     int64    `json:"spans"`
	Errors    int64    `json:"errors"`
	Source    string   `json:"source"`
}

// workloadRows — SAF: workload kayıtları + pod sayıları + pod→parent + seen
// satırları → satırlar (telemetry = ≥1 seen satırı).
func workloadRows(c ClusterRef, recs []chstore.EntityRecord, podCounts map[string]int, podParent map[string]string, seen []chstore.EntitySeenAgg, kind string) []WorkloadRow {
	svcByWL := map[string]map[string]bool{}
	spans, errs := map[string]int64{}, map[string]int64{}
	for _, a := range seen {
		wl := podParent[a.Pod]
		if wl == "" {
			continue
		}
		if svcByWL[wl] == nil {
			svcByWL[wl] = map[string]bool{}
		}
		if a.Service != "" {
			svcByWL[wl][a.Service] = true
		}
		spans[wl] += a.Spans
		errs[wl] += a.Errors
	}
	out := make([]WorkloadRow, 0, len(recs))
	for _, r := range recs {
		k := r.Labels["kind"]
		if kind != "" && !strings.EqualFold(k, kind) {
			continue
		}
		svcs := make([]string, 0, len(svcByWL[r.ID]))
		for s := range svcByWL[r.ID] {
			svcs = append(svcs, s)
		}
		sort.Strings(svcs)
		out = append(out, WorkloadRow{
			Cluster: c.Name, ClusterID: c.ID, Namespace: r.Namespace, Workload: r.Name, Kind: k,
			Pods: podCounts[r.ID], Services: svcs, Telemetry: len(svcByWL[r.ID]) > 0,
			Spans: spans[r.ID], Errors: errs[r.ID], Source: r.Source,
		})
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Workload < out[j].Workload })
	return out
}

func listWorkloadsTool(d Deps) mcp.Tool {
	return mcp.Tool{
		Name:             "list_workloads",
		ShortDescription: "Namespace'in workload'ları (Deployment/StatefulSet/DaemonSet): pod sayısı, telemetri var/yok, karşılık gelen service.name'ler. 'X namespace'indeki servisler' sorusu.",
		Description: "List workloads (Deployment / StatefulSet / DaemonSet / Job) of a namespace from the entity catalogue with pod count, whether they emitted spans in the window " +
			"(`telemetry`), the OTel service.name values seen from their pods, and span/error counts. Also returns `services_in_namespace` — every service.name seen in the " +
			"namespace in the window (telemetry side, includes pods without a catalogued workload) — and `pods_without_workload` (pods known only from spans; " +
			"no kube-state-metrics owner, e.g. clusters without Thanos). `namespace` must be the exact name (resolve with list_namespaces first). " +
			"Empty `cluster` = every enabled cluster; rows carry the cluster. Window: range_s (default 1800, max 86400). Returns {disabled:true} when the entity layer is off.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"namespace": map[string]any{"type": "string", "description": "Exact namespace name (from list_namespaces)."},
				"cluster":   map[string]any{"type": "string", "description": "Remote Cluster id / name / span value. Empty = all enabled clusters."},
				"kind":      map[string]any{"type": "string", "description": "Optional kind filter: Deployment, StatefulSet, DaemonSet, Job."},
				"query":     map[string]any{"type": "string", "description": "Workload name or fragment (substring; fuzzy fallback)."},
				"range_s":   map[string]any{"type": "integer", "minimum": 60, "maximum": 86400, "description": "Telemetry window in seconds. Default 1800."},
			},
			"required": []string{"namespace"},
		},
		MinRole: "",
		Handler: func(ctx context.Context, raw json.RawMessage) (any, error) {
			var a listWorkloadsArgs
			if len(raw) > 0 {
				if err := json.Unmarshal(raw, &a); err != nil {
					return nil, fmt.Errorf("decode args: %w", err)
				}
			}
			ns := strings.TrimSpace(a.Namespace)
			if ns == "" {
				return nil, fmt.Errorf("namespace zorunlu — önce list_namespaces")
			}
			if !entityLayerOn(d) {
				return entityDisabled(), nil
			}
			from, to := rangeWindow(ctx, a.RangeS)
			ov, err := ReadNamespaceOverview(ctx, d, ns, a.Cluster, a.Query, a.Kind, from, to)
			if err != nil {
				return nil, err
			}
			res := map[string]any{
				"namespace": ns, "workloads": ov.Workloads, "count": len(ov.Workloads),
				"services_in_namespace": ov.Services, "pods_without_workload": ov.OrphanPods,
				"window_s": ov.WindowS, "clusters_searched": ov.Clusters,
			}
			if len(ov.Workloads) == 0 && ov.OrphanPods > 0 {
				res["hint"] = "Katalogda workload yok ama span kaynaklı pod'lar var: bu cluster için Thanos/KSM tanımlı değil ya da namespace süzgeci dışında; services_in_namespace telemetri tarafını yine gösterir."
			}
			return res, nil
		},
	}
}

// NamespaceServiceRow — namespace'te görülen bir service.name (telemetri tarafı).
type NamespaceServiceRow struct {
	Cluster  string `json:"cluster"`
	Service  string `json:"service"`
	Pods     int    `json:"pods"`
	Spans    int64  `json:"spans"`
	Errors   int64  `json:"errors"`
	LastSeen string `json:"last_seen"`
}

// NamespaceOverview — v0.10.470 (F2-3): list_workloads'un okuma çekirdeği,
// guided rota (api/namespace_guided.go) ile PAYLAŞILIR — tool ve sohbet
// kartı aynı satırları görür.
type NamespaceOverview struct {
	Namespace  string
	Clusters   []string
	Workloads  []WorkloadRow
	Services   []NamespaceServiceRow
	OrphanPods int
	WindowS    int
}

// ReadNamespaceOverview — cluster boş → tüm etkin cluster'lar; bilinmeyen
// cluster → hata (adaylar mesajda). Bayrak kapısı ÇAĞIRANDA.
func ReadNamespaceOverview(ctx context.Context, d Deps, ns, cluster, query, kind string, from, to time.Time) (NamespaceOverview, error) {
	clusters, ok := clustersFor(d, cluster)
	if !ok {
		return NamespaceOverview{}, unknownClusterErr(d, cluster)
	}
	ov := NamespaceOverview{Namespace: ns, Clusters: clusterNames(clusters), Workloads: []WorkloadRow{}, Services: []NamespaceServiceRow{}, WindowS: int(to.Sub(from).Seconds())}
	for _, c := range clusters {
		recs, err := d.Store.EntityList(ctx, chstore.EntityListQuery{ClusterID: c.ID, Type: entity.TypeWorkload, Namespace: ns, Search: query, Limit: 500})
		if err != nil {
			return ov, err
		}
		if len(recs) == 0 && strings.TrimSpace(query) != "" {
			all, err := d.Store.EntityList(ctx, chstore.EntityListQuery{ClusterID: c.ID, Type: entity.TypeWorkload, Namespace: ns, Limit: 500})
			if err != nil {
				return ov, err
			}
			recs = fuzzyPick(query, all)
		}
		pods, err := d.Store.EntityList(ctx, chstore.EntityListQuery{ClusterID: c.ID, Type: entity.TypePod, Namespace: ns, Limit: 500})
		if err != nil {
			return ov, err
		}
		podParent := map[string]string{}
		podNames := make([]string, 0, len(pods))
		wlIDs := map[string]bool{}
		for _, r := range recs {
			wlIDs[r.ID] = true
		}
		for _, p := range pods {
			podNames = append(podNames, p.Name)
			if wlIDs[p.ParentID] {
				podParent[p.Name] = p.ParentID
			} else {
				ov.OrphanPods++
			}
		}
		parents := make([]string, 0, len(recs))
		for _, r := range recs {
			parents = append(parents, r.ID)
		}
		podCounts, err := d.Store.EntityChildrenCountsByParents(ctx, c.ID, entity.TypePod, parents, time.Time{})
		if err != nil {
			return ov, err
		}
		var seen []chstore.EntitySeenAgg
		if len(podNames) > 0 && len(c.SpanValues) > 0 {
			seen, err = d.Store.EntitySeenForPods(ctx, c.SpanValues, ns, podNames, from, to)
			if err != nil {
				return ov, err
			}
		}
		ov.Workloads = append(ov.Workloads, workloadRows(c, recs, podCounts, podParent, seen, kind)...)
		svcs, err := d.Store.EntitySeenServicesByNamespace(ctx, c.SpanValues, ns, from, to)
		if err != nil {
			return ov, err
		}
		for _, sv := range svcs {
			ov.Services = append(ov.Services, NamespaceServiceRow{Cluster: c.Name, Service: sv.Service, Pods: sv.Pods, Spans: sv.Spans, Errors: sv.Errors, LastSeen: sv.LastSeen.UTC().Format(time.RFC3339)})
		}
	}
	return ov, nil
}

// ─── list_pods ─────────────────────────────────────────────────

type listPodsArgs struct {
	Namespace string `json:"namespace"`
	Workload  string `json:"workload,omitempty"`
	Cluster   string `json:"cluster,omitempty"`
	RangeS    int    `json:"range_s,omitempty"`
	Limit     int    `json:"limit,omitempty"`
}

type podRow struct {
	Cluster   string   `json:"cluster"`
	Namespace string   `json:"namespace"`
	Pod       string   `json:"pod"`
	Workload  string   `json:"workload,omitempty"`
	Node      string   `json:"node,omitempty"`
	Services  []string `json:"services"`
	Spans     int64    `json:"spans"`
	Errors    int64    `json:"errors"`
	LastSpan  string   `json:"last_span,omitempty"`
	Source    string   `json:"source"`
	Stale     bool     `json:"stale,omitempty"`
}

// podRows — SAF: pod kayıtları + seen satırları → satırlar.
func podRows(c ClusterRef, pods []chstore.EntityRecord, seen []chstore.EntitySeenAgg) []podRow {
	type acc struct {
		svcs        map[string]bool
		spans, errs int64
		node        string
		last        time.Time
	}
	by := map[string]*acc{}
	for _, a := range seen {
		x := by[a.Pod]
		if x == nil {
			x = &acc{svcs: map[string]bool{}}
			by[a.Pod] = x
		}
		if a.Service != "" {
			x.svcs[a.Service] = true
		}
		x.spans += a.Spans
		x.errs += a.Errors
		if a.Node != "" {
			x.node = a.Node
		}
		if a.LastSeen.After(x.last) {
			x.last = a.LastSeen
		}
	}
	out := make([]podRow, 0, len(pods))
	for _, p := range pods {
		row := podRow{Cluster: c.Name, Namespace: p.Namespace, Pod: p.Name, Source: p.Source, Stale: p.Stale, Services: []string{}}
		if ref, ok := entity.ParseID(p.ParentID); ok && ref.Type == entity.TypeWorkload {
			row.Workload = ref.Name
		}
		if x := by[p.Name]; x != nil {
			for s := range x.svcs {
				row.Services = append(row.Services, s)
			}
			sort.Strings(row.Services)
			row.Spans, row.Errors, row.Node = x.spans, x.errs, x.node
			if !x.last.IsZero() {
				row.LastSpan = x.last.UTC().Format(time.RFC3339)
			}
		}
		out = append(out, row)
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Spans != out[j].Spans {
			return out[i].Spans > out[j].Spans
		}
		return out[i].Pod < out[j].Pod
	})
	return out
}

func listPodsTool(d Deps) mcp.Tool {
	return mcp.Tool{
		Name:             "list_pods",
		ShortDescription: "Namespace ya da workload'ın pod'ları: node, telemetri gönderdiği service.name'ler, span/hata sayısı, son span zamanı. 'bunun pod'larını göster' sorusu.",
		Description: "List pods of a namespace (optionally one workload) from the entity catalogue, enriched from spans in the window: node, service.name values emitted, " +
			"span/error counts and the last span time. A pod with spans=0 exists in the catalogue but emitted nothing in the window — say so, do not call it missing. " +
			"`workload` is the exact workload name (from list_workloads); `namespace` is required. Empty `cluster` = every enabled cluster. " +
			"Window: range_s (default 1800, max 86400). Returns {disabled:true} when the entity layer is off.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"namespace": map[string]any{"type": "string", "description": "Exact namespace name."},
				"workload":  map[string]any{"type": "string", "description": "Exact workload name to narrow to. Empty = whole namespace."},
				"cluster":   map[string]any{"type": "string", "description": "Remote Cluster id / name / span value. Empty = all enabled clusters."},
				"range_s":   map[string]any{"type": "integer", "minimum": 60, "maximum": 86400, "description": "Telemetry window in seconds. Default 1800."},
				"limit":     map[string]any{"type": "integer", "minimum": 1, "maximum": 500, "description": "Max pods per cluster. Default 200."},
			},
			"required": []string{"namespace"},
		},
		MinRole: "",
		Handler: func(ctx context.Context, raw json.RawMessage) (any, error) {
			var a listPodsArgs
			if len(raw) > 0 {
				if err := json.Unmarshal(raw, &a); err != nil {
					return nil, fmt.Errorf("decode args: %w", err)
				}
			}
			ns := strings.TrimSpace(a.Namespace)
			if ns == "" {
				return nil, fmt.Errorf("namespace zorunlu — önce list_namespaces")
			}
			if !entityLayerOn(d) {
				return entityDisabled(), nil
			}
			clusters, ok := clustersFor(d, a.Cluster)
			if !ok {
				return nil, unknownClusterErr(d, a.Cluster)
			}
			from, to := rangeWindow(ctx, a.RangeS)
			limit := clampLimit(a.Limit, entityCatalogLimit, 500)
			rows := []podRow{}
			wl := strings.TrimSpace(a.Workload)
			for _, c := range clusters {
				q := chstore.EntityListQuery{ClusterID: c.ID, Type: entity.TypePod, Namespace: ns, Limit: limit}
				if wl != "" {
					wls, err := d.Store.EntityList(ctx, chstore.EntityListQuery{ClusterID: c.ID, Type: entity.TypeWorkload, Namespace: ns, Name: wl, Limit: 5})
					if err != nil {
						return nil, err
					}
					if len(wls) == 0 {
						continue // bu cluster'da böyle workload yok
					}
					q.ParentID = wls[0].ID
				}
				pods, err := d.Store.EntityList(ctx, q)
				if err != nil {
					return nil, err
				}
				if len(pods) == 0 {
					continue
				}
				names := make([]string, 0, len(pods))
				for _, p := range pods {
					names = append(names, p.Name)
				}
				var seen []chstore.EntitySeenAgg
				if len(c.SpanValues) > 0 {
					seen, err = d.Store.EntitySeenForPods(ctx, c.SpanValues, ns, names, from, to)
					if err != nil {
						return nil, err
					}
				}
				rows = append(rows, podRows(c, pods, seen)...)
			}
			res := map[string]any{"namespace": ns, "pods": rows, "count": len(rows), "window_s": int(to.Sub(from).Seconds()), "clusters_searched": clusterNames(clusters)}
			if wl != "" {
				res["workload"] = wl
				if len(rows) == 0 {
					res["hint"] = "Bu workload aranan cluster'larda katalogda yok (adı list_workloads ile doğrula)."
				}
			}
			return res, nil
		},
	}
}
