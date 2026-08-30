package api

// entity_pod_match.go — v0.10.190 (operatör-bildirimi, prod: Service → Pods
// tablosunda NAMESPACE boş, STATUS/CPU «—», WORKLOAD boş; pod sayfasında
// «pod'dan geçen servis yok» ama trace listesi dolu; span detayında «link
// yok»). Tek kök neden: bir cluster'ın collector'ı k8s.namespace.name
// BASMIYOR. entity_seen satırları k8s_namespace='' ile duruyor; Thanos
// eşleşmesi "ns/pod" anahtarıyla yapıldığından hiç tutmuyordu. Kural:
//   - ns/pod tam eşleşme → exact;
//   - namespace boş + Thanos'ta o pod adı TEK namespace'te → filled
//     (namespace Thanos'tan tamamlanır, çağıran İLAN eder);
//   - namespace boş + pod adı birden çok namespace'te → ambiguous
//     (yanlış kapsam yerine «—»; çağıran ilan eder);
//   - yoksa none.
// Saf; entity_pod_match_test.go pinler.

import (
	"github.com/cilcenk/coremetry/internal/chstore"
	"github.com/cilcenk/coremetry/internal/thanos"
)

type thanosPodIndex struct {
	byPod  map[string]thanos.PodRow   // "ns/pod"
	byName map[string][]thanos.PodRow // pod → satırlar (namespace'ler)
}

func indexThanosPods(rows []thanos.PodRow) thanosPodIndex {
	idx := thanosPodIndex{byPod: make(map[string]thanos.PodRow, len(rows)), byName: make(map[string][]thanos.PodRow, len(rows))}
	for _, p := range rows {
		idx.byPod[p.Namespace+"/"+p.Pod] = p
		idx.byName[p.Pod] = append(idx.byName[p.Pod], p)
	}
	return idx
}

type thanosMatch int

const (
	thanosMatchNone thanosMatch = iota
	thanosMatchExact
	thanosMatchFilled
	thanosMatchAmbiguous
)

func matchThanosPod(idx thanosPodIndex, namespace, pod string) (thanos.PodRow, thanosMatch) {
	if p, ok := idx.byPod[namespace+"/"+pod]; ok {
		return p, thanosMatchExact
	}
	if namespace != "" {
		return thanos.PodRow{}, thanosMatchNone
	}
	switch cands := idx.byName[pod]; len(cands) {
	case 0:
		return thanos.PodRow{}, thanosMatchNone
	case 1:
		return cands[0], thanosMatchFilled
	default:
		return thanos.PodRow{}, thanosMatchAmbiguous
	}
}

// servicePodRow — GET /api/services/{name}/pods satırı (v0.10.136 şekli;
// v0.10.190'da paket düzeyine çıktı: mergeServicePodRows saf/testli).
type servicePodRow struct {
	chstore.EntitySeenAgg
	ClusterID string                `json:"clusterId,omitempty"`
	EntityID  string                `json:"entityId,omitempty"`
	Entity    *chstore.EntityRecord `json:"entity,omitempty"`
	// v0.10.136 — adım 2: durum (Thanos KSM anlık) + giriş-span latency (ham spans).
	Phase           string  `json:"phase,omitempty"`
	Restarts        int     `json:"restarts"`
	RestartsUnknown bool    `json:"restartsUnknown,omitempty"`
	LastTermReason  string  `json:"lastTermReason,omitempty"`
	CPUCores        float64 `json:"cpuCores,omitempty"`
	MemBytes        float64 `json:"memBytes,omitempty"`
	StatusKnown     bool    `json:"statusKnown,omitempty"`
	// v0.10.190 — namespace span'de yoktu, Thanos pod listesinden tamamlandı.
	NamespaceFromThanos bool    `json:"namespaceFromThanos,omitempty"`
	EntrySpans          int64   `json:"entrySpans,omitempty"`
	P50Ms               float64 `json:"p50Ms,omitempty"`
	P95Ms               float64 `json:"p95Ms,omitempty"`
	P99Ms               float64 `json:"p99Ms,omitempty"`
}

// mergeServicePodRows — aynı (clusterId, namespace, pod) tek satır: seen
// agregası toplanır, Thanos/latency alanları BİLİNEN satırdan taşınır
// (v0.10.139 çoklu-değer yinelemesi; v0.10.190 namespace'siz + namespace'li
// kova çakışması). Eşlenmemiş cluster (ClusterID ”) satırları olduğu gibi.
func mergeServicePodRows(rows []servicePodRow) []servicePodRow {
	type key struct{ cid, ns, pod string }
	idx := map[key]int{}
	merged := make([]servicePodRow, 0, len(rows))
	for _, row := range rows {
		if row.ClusterID == "" {
			merged = append(merged, row)
			continue
		}
		k := key{row.ClusterID, row.Namespace, row.Pod}
		if j, ok := idx[k]; ok {
			m := &merged[j]
			m.EntitySeenAgg = mergeSeenAgg(m.EntitySeenAgg, row.EntitySeenAgg)
			if !m.StatusKnown && row.StatusKnown {
				m.Phase, m.Restarts, m.RestartsUnknown, m.LastTermReason = row.Phase, row.Restarts, row.RestartsUnknown, row.LastTermReason
				m.CPUCores, m.MemBytes, m.StatusKnown = row.CPUCores, row.MemBytes, true
			}
			if m.EntrySpans == 0 && row.EntrySpans > 0 {
				m.EntrySpans, m.P50Ms, m.P95Ms, m.P99Ms = row.EntrySpans, row.P50Ms, row.P95Ms, row.P99Ms
			}
			// İkisinden biri span'den namespace'li geldiyse "Thanos'tan" işareti düşer.
			m.NamespaceFromThanos = m.NamespaceFromThanos && row.NamespaceFromThanos
			if m.EntityID == "" {
				m.EntityID, m.Entity = row.EntityID, row.Entity
			}
			continue
		}
		idx[k] = len(merged)
		merged = append(merged, row)
	}
	return merged
}
