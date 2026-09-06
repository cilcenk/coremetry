package mcptools

// resolve_entity.go — v0.10.469 (CoSRE Telemetry Agent Faz 2, F2-2; audit G3):
// serbest metin → {service, namespace, workload, pod} adayları. "shop-payment"
// bir namespace mi, workload mu, service.name öneki mi — TAHMİN ETME, kataloğa
// sor. Skorlu (tam > önek > alt-dize > jeton kapsaması > yazım hatası —
// NearNames'in aynı merdiveni, tür başına), çok-cluster farkındalıklı (aynı ad
// iki cluster'da iki aday; cevapta cluster söylenir).
//
// Katalog indeksi: servis adları (picker DISTINCT'i, ≤2000), namespace'ler ve
// workload'lar (entities FINAL, cluster başına ≤500) — süreç içi 60 s cache
// (guided servis-adı cache'inin ikizi). Pod'lar YALNIZ metin pod-şekilliyse
// (RS hash'i / sıra numarası soneki) ve alt-dize aramasıyla (≤50).
// Bayrak kapalıysa yalnız servis ekseni çalışır ve cevap bunu söyler.

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/cilcenk/coremetry/internal/chstore"
	"github.com/cilcenk/coremetry/internal/entity"
	"github.com/cilcenk/coremetry/internal/mcp"
)

// EntityCandidate — çözüm adayı.
type EntityCandidate struct {
	Kind      string `json:"kind"` // service | namespace | workload | pod
	Cluster   string `json:"cluster,omitempty"`
	ClusterID string `json:"cluster_id,omitempty"`
	Namespace string `json:"namespace,omitempty"`
	Name      string `json:"name"`
	WlKind    string `json:"workload_kind,omitempty"`
	ID        string `json:"id,omitempty"` // entity_id (servis için svc:<ad>)
	Score     int    `json:"score"`
	Match     string `json:"match"` // exact | prefix | substring | tokens | fuzzy
}

// EntityCatalogIndex — çözümün baktığı küme (SAF; test edilir).
type EntityCatalogIndex struct {
	Services   []string
	Namespaces []EntityCandidate // Kind=namespace, Cluster/ClusterID/Name
	Workloads  []EntityCandidate // Kind=workload, + Namespace/WlKind
	Pods       []EntityCandidate // yalnız pod-şekilli metinde doldurulur
	// EntityLayer=false → yalnız servis ekseni (cevap ilan eder).
	EntityLayer bool
}

// scoreName — NearNames merdiveni tek ad için (0 = eşleşme yok).
func scoreName(q string, qt []string, name string) (int, string) {
	ln := strings.ToLower(name)
	switch {
	case ln == q:
		return 100, "exact"
	case strings.HasPrefix(ln, q):
		return 80, "prefix"
	case strings.Contains(ln, q):
		return 70, "substring"
	}
	nt := NameTokens(ln)
	matched := 0
	for _, t := range qt {
		for _, x := range nt {
			if x == t || (len(t) >= 3 && strings.HasPrefix(x, t)) {
				matched++
				break
			}
		}
	}
	score, match := 0, ""
	switch {
	case len(qt) > 0 && matched == len(qt):
		score, match = 60+matched, "tokens"
	case matched > 0 && matched*2 >= len(qt):
		score, match = 30+matched, "tokens"
	}
	if len(q) >= 4 && Levenshtein(q, ln) <= 2 && score < 50 {
		score, match = 50, "fuzzy"
	}
	return score, match
}

// ResolveEntities — SAF: metin → skorlu adaylar (tür bağımsız sıralama;
// eşit skorda servis > namespace > workload > pod, sonra ad). Katman kuralı
// NearNames ile aynı: tam eş varsa yalnız tam eşler; ≥50 güçlü aday varsa
// zayıf kısmi kapsamalar düşer. max tavanı.
func ResolveEntities(text string, idx EntityCatalogIndex, max int) []EntityCandidate {
	q := strings.ToLower(strings.TrimSpace(text))
	if len(q) < 3 || max <= 0 {
		return nil
	}
	qt := NameTokens(q)
	var out []EntityCandidate
	add := func(c EntityCandidate) {
		s, m := scoreName(q, qt, c.Name)
		if s > 0 {
			c.Score, c.Match = s, m
			out = append(out, c)
		}
	}
	for _, s := range idx.Services {
		add(EntityCandidate{Kind: entity.TypeService, Name: s, ID: entity.ServiceID(s)})
	}
	for _, n := range idx.Namespaces {
		add(n)
	}
	for _, w := range idx.Workloads {
		add(w)
	}
	for _, p := range idx.Pods {
		add(p)
	}
	kindRank := map[string]int{entity.TypeService: 0, entity.TypeNamespace: 1, entity.TypeWorkload: 2, entity.TypePod: 3}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Score != out[j].Score {
			return out[i].Score > out[j].Score
		}
		if kindRank[out[i].Kind] != kindRank[out[j].Kind] {
			return kindRank[out[i].Kind] < kindRank[out[j].Kind]
		}
		if out[i].Name != out[j].Name {
			return out[i].Name < out[j].Name
		}
		return out[i].Cluster < out[j].Cluster
	})
	if len(out) > 0 {
		switch {
		case out[0].Score >= 100:
			n := 0
			for n < len(out) && out[n].Score >= 100 {
				n++
			}
			out = out[:n]
		case out[0].Score >= 50:
			n := 0
			for n < len(out) && out[n].Score >= 50 {
				n++
			}
			out = out[:n]
		}
	}
	if len(out) > max {
		out = out[:max]
	}
	return out
}

// ResolvedOne — tek GÜÇLÜ aday varsa o (tam eş tek, ya da tek ≥50 aday); yoksa nil.
func ResolvedOne(cands []EntityCandidate) *EntityCandidate {
	if len(cands) == 1 && cands[0].Score >= 50 {
		c := cands[0]
		return &c
	}
	return nil
}

// podShapedRe — Deployment pod'u (rs hash + 5 karakter) ya da StatefulSet
// sıra numarası soneki.
var podShapedRe = regexp.MustCompile(`(-[a-f0-9]{5,10}-[a-z0-9]{5}|-\d{1,3})$`)

func podShaped(text string) bool {
	return podShapedRe.MatchString(strings.ToLower(strings.TrimSpace(text)))
}

// ── katalog indeksi (60 s süreç içi cache) ─────────────────────

const entityIndexTTL = 60 * time.Second

var entityIndexCache struct {
	sync.Mutex
	at  time.Time
	idx EntityCatalogIndex
}

// loadEntityIndex — servisler + (bayrak açıksa) namespace/workload'lar; 60 s cache.
func loadEntityIndex(ctx context.Context, d Deps) (EntityCatalogIndex, error) {
	entityIndexCache.Lock()
	if cacheNow().Sub(entityIndexCache.at) < entityIndexTTL && entityIndexCache.at.After(time.Time{}) {
		idx := entityIndexCache.idx
		entityIndexCache.Unlock()
		return idx, nil
	}
	entityIndexCache.Unlock()
	idx := EntityCatalogIndex{EntityLayer: entityLayerOn(d)}
	if d.Store != nil {
		names, _, err := d.Store.ListServiceNames(ctx, "", serviceCatalogueMax, 0)
		if err != nil {
			return idx, err
		}
		idx.Services = names
	}
	if idx.EntityLayer && d.Store != nil {
		clusters, _ := clustersFor(d, "")
		for _, c := range clusters {
			nss, err := d.Store.EntityList(ctx, chstore.EntityListQuery{ClusterID: c.ID, Type: entity.TypeNamespace, Limit: 500})
			if err != nil {
				return idx, err
			}
			for _, r := range nss {
				idx.Namespaces = append(idx.Namespaces, EntityCandidate{Kind: entity.TypeNamespace, Cluster: c.Name, ClusterID: c.ID, Name: r.Name, ID: r.ID})
			}
			wls, err := d.Store.EntityList(ctx, chstore.EntityListQuery{ClusterID: c.ID, Type: entity.TypeWorkload, Limit: 500})
			if err != nil {
				return idx, err
			}
			for _, r := range wls {
				idx.Workloads = append(idx.Workloads, EntityCandidate{Kind: entity.TypeWorkload, Cluster: c.Name, ClusterID: c.ID, Namespace: r.Namespace, Name: r.Name, WlKind: r.Labels["kind"], ID: r.ID})
			}
		}
	}
	entityIndexCache.Lock()
	entityIndexCache.at, entityIndexCache.idx = cacheNow(), idx
	entityIndexCache.Unlock()
	return idx, nil
}

// ResetEntityIndexCache — test / ayar değişimi.
func ResetEntityIndexCache() {
	entityIndexCache.Lock()
	entityIndexCache.at = time.Time{}
	entityIndexCache.Unlock()
}

// loadPodCandidates — yalnız pod-şekilli metin; alt-dize (ILIKE), cluster başına ≤50.
func loadPodCandidates(ctx context.Context, d Deps, text, namespace string) ([]EntityCandidate, error) {
	var out []EntityCandidate
	clusters, _ := clustersFor(d, "")
	for _, c := range clusters {
		pods, err := d.Store.EntityList(ctx, chstore.EntityListQuery{ClusterID: c.ID, Type: entity.TypePod, Namespace: namespace, Search: text, Limit: 50})
		if err != nil {
			return nil, err
		}
		for _, p := range pods {
			out = append(out, EntityCandidate{Kind: entity.TypePod, Cluster: c.Name, ClusterID: c.ID, Namespace: p.Namespace, Name: p.Name, ID: p.ID})
		}
	}
	return out, nil
}

// ResolveEntityText — tool ve guided'ın ortak girişi: indeks + (gerekirse)
// pod adayları + saf çözüm. namespace verilirse namespace/workload/pod
// adayları ona daraltılır.
func ResolveEntityText(ctx context.Context, d Deps, text, cluster, namespace string) ([]EntityCandidate, EntityCatalogIndex, error) {
	idx, err := loadEntityIndex(ctx, d)
	if err != nil {
		return nil, idx, err
	}
	if idx.EntityLayer && podShaped(text) && d.Store != nil {
		pods, err := loadPodCandidates(ctx, d, strings.TrimSpace(text), namespace)
		if err != nil {
			return nil, idx, err
		}
		idx.Pods = pods
	}
	if cluster != "" || namespace != "" {
		keep := func(c EntityCandidate) bool {
			if cluster != "" && !strings.EqualFold(c.Cluster, cluster) && !strings.EqualFold(c.ClusterID, cluster) {
				return false
			}
			if namespace != "" && c.Kind != entity.TypeNamespace && !strings.EqualFold(c.Namespace, namespace) {
				return false
			}
			return true
		}
		filt := func(in []EntityCandidate) []EntityCandidate {
			out := in[:0:0]
			for _, c := range in {
				if keep(c) {
					out = append(out, c)
				}
			}
			return out
		}
		idx.Namespaces, idx.Workloads, idx.Pods = filt(idx.Namespaces), filt(idx.Workloads), filt(idx.Pods)
	}
	return ResolveEntities(text, idx, 12), idx, nil
}

// ─── resolve_entity tool ───────────────────────────────────────

type resolveEntityArgs struct {
	Text      string `json:"text"`
	Cluster   string `json:"cluster,omitempty"`
	Namespace string `json:"namespace,omitempty"`
}

func resolveEntityTool(d Deps) mcp.Tool {
	return mcp.Tool{
		Name:             "resolve_entity",
		ShortDescription: "Serbest metni {servis, namespace, workload, pod} adaylarına çözer (skorlu, cluster'lı). Operatör bir ad andığında ÖNCE bunu çağır; tahmin etme.",
		Description: "Resolve free text the operator typed (\"shop-payment\", \"shop namespace\", \"api-7d9f\") into catalogue candidates: OTel service.name values, Kubernetes namespaces, " +
			"workloads and (only when the text looks like a pod name) pods — each scored (exact > prefix > substring > tokens > fuzzy) and tagged with cluster + namespace. " +
			"Call this FIRST whenever the operator names an entity in free text; never guess whether a string is a namespace, a workload or a service. " +
			"`resolved` is set only when exactly one strong candidate exists — proceed silently then. Several candidates → show them with cluster + namespace + kind and ask which one; " +
			"none → say so, offer nothing invented. `cluster` / `namespace` narrow the catalogue side. When the entity layer is off, only the service axis is searched and " +
			"`entity_layer` is false — say that instead of claiming a namespace does not exist.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"text":      map[string]any{"type": "string", "description": "The operator's phrase, verbatim (name or fragment)."},
				"cluster":   map[string]any{"type": "string", "description": "Optional Remote Cluster id/name to narrow namespace/workload/pod candidates."},
				"namespace": map[string]any{"type": "string", "description": "Optional exact namespace to narrow workload/pod candidates."},
			},
			"required": []string{"text"},
		},
		MinRole: "",
		Handler: func(ctx context.Context, raw json.RawMessage) (any, error) {
			var a resolveEntityArgs
			if len(raw) > 0 {
				if err := json.Unmarshal(raw, &a); err != nil {
					return nil, fmt.Errorf("decode args: %w", err)
				}
			}
			text := strings.TrimSpace(a.Text)
			if len(text) < 3 {
				return nil, fmt.Errorf("text en az 3 karakter olmalı")
			}
			cands, idx, err := ResolveEntityText(ctx, d, text, strings.TrimSpace(a.Cluster), strings.TrimSpace(a.Namespace))
			if err != nil {
				return nil, err
			}
			res := map[string]any{"text": text, "candidates": cands, "count": len(cands), "entity_layer": idx.EntityLayer}
			if one := ResolvedOne(cands); one != nil {
				res["resolved"] = one
			}
			switch {
			case len(cands) == 0 && !idx.EntityLayer:
				res["hint"] = "Servis ekseninde yakın ad yok; varlık katmanı KAPALI olduğu için namespace/workload/pod aranmadı (Settings → Entities)."
			case len(cands) == 0:
				res["hint"] = "Katalogda yakın ad yok — operatörden tam adı iste; ad UYDURMA."
			case len(cands) > 1:
				res["hint"] = "Birden çok aday: cluster + namespace + tür ile listele ve hangisini kastettiğini sor; hepsinde birden pahalı arama yapma."
			}
			return res, nil
		},
	}
}
