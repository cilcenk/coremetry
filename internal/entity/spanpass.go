package entity

import (
	"context"
	"sort"
	"time"
)

// spanpass.go — SPAN-TÜREVLİ çıkarım (design §3). entity_seen_5m'in son
// dilimi (span cluster değeri, namespace, pod, node, servis) → Remote
// Cluster kaydına eşlenir (SpanClusterValue) → o cluster'ın AYNI tick'teki
// `seen` kümesine eklenir: pod (Thanos bilmiyorsa, source=span), `svc:`
// varlığı, `runs` (pod→svc), node biliniyorsa `runs_on`. Eşlenemeyen
// cluster değeri düşmez, "(unmapped)" koşu satırına `değer → n` sayaçlanır.
//
// Çakışma kuralı (design §3): yapı Thanos'un, etkinlik span'in — aynı
// id'yi ikisi de üretirse Thanos'un varlığı kalır, span yalnız ilişki ekler.

// SeenRow — entity_seen_5m'den bir (cluster, ns, pod, servis) satırı.
type SeenRow struct {
	ClusterValue, Namespace, Pod, Node, Service string
	Spans                                       int
	LastSeen                                    time.Time
}

// SeenReader — chstore.EntitySeenRecent adaptörü.
type SeenReader interface {
	RecentSeen(ctx context.Context, since time.Time) ([]SeenRow, error)
}

// UnmappedClusterID — eşlenemeyen span cluster değerlerinin koşu satırı.
const UnmappedClusterID = "(unmapped)"

// GroupSeenByCluster — satırları cluster_id'ye göre böler; eşlenemeyenleri sayar.
func GroupSeenByCluster(rows []SeenRow, refs []ClusterRef) (map[string][]SeenRow, map[string]int) {
	byValue := map[string]string{}
	for _, r := range refs {
		if r.ID == "" {
			continue
		}
		vals := append([]string{r.SpanClusterValue}, r.SpanClusterValues...)
		if r.SpanClusterValue == "" && len(r.SpanClusterValues) == 0 {
			vals = []string{r.Name}
		}
		for _, v := range vals {
			if v != "" {
				byValue[v] = r.ID // v0.10.139 — bir kayıt birden çok değer
			}
		}
	}
	out := map[string][]SeenRow{}
	unmapped := map[string]int{}
	for _, row := range rows {
		cid, ok := byValue[row.ClusterValue]
		if !ok {
			unmapped[row.ClusterValue]++
			continue
		}
		out[cid] = append(out[cid], row)
	}
	return out, unmapped
}

// SpanSeenToEntities — bir cluster'ın span satırları → ek varlık/ilişki.
// known: Thanos'un bu tick ürettiği varlıklar (pod çakışmasında kazanır).
func SpanSeenToEntities(cid string, rows []SeenRow, known map[string]Entity) ([]Entity, []Relation) {
	ents := map[string]Entity{}
	rels := map[string]Relation{}
	rel := func(typ, parent, child string) {
		rels[typ+"|"+parent+"|"+child] = Relation{Type: typ, ClusterID: cid, ParentID: parent, ChildID: child, Source: SourceSpan}
	}
	for _, r := range rows {
		if r.Namespace == "" || r.Pod == "" {
			continue
		}
		podID := PodID(cid, r.Namespace, r.Pod)
		if _, thanosKnows := known[podID]; !thanosKnows {
			if _, dup := ents[podID]; !dup {
				nsID := NamespaceID(cid, r.Namespace)
				if _, ok := known[nsID]; !ok {
					ents[nsID] = Entity{Type: TypeNamespace, ClusterID: cid, ID: nsID, Name: r.Namespace, ParentID: ClusterID(cid), Labels: map[string]string{}, Source: SourceSpan}
					rel(RelParent, ClusterID(cid), nsID)
				}
				ents[podID] = Entity{Type: TypePod, ClusterID: cid, ID: podID, Namespace: r.Namespace, Name: r.Pod, ParentID: nsID, Labels: map[string]string{}, Source: SourceSpan}
				rel(RelParent, nsID, podID)
			}
		}
		if r.Service != "" {
			svcID := ServiceID(r.Service)
			if _, ok := known[svcID]; !ok {
				ents[svcID] = Entity{Type: TypeService, ClusterID: cid, ID: svcID, Name: r.Service, Labels: map[string]string{}, Source: SourceSpan}
			}
			rel(RelRuns, podID, svcID)
		}
		if r.Node != "" {
			nodeID := NodeID(cid, r.Node)
			if _, ok := known[nodeID]; !ok {
				if _, dup := ents[nodeID]; !dup {
					ents[nodeID] = Entity{Type: TypeNode, ClusterID: cid, ID: nodeID, Name: r.Node, ParentID: ClusterID(cid), Labels: map[string]string{}, Source: SourceSpan}
					rel(RelParent, ClusterID(cid), nodeID)
				}
			}
			rel(RelRunsOn, podID, nodeID)
		}
	}
	outE := make([]Entity, 0, len(ents))
	for _, e := range ents {
		outE = append(outE, e)
	}
	sort.Slice(outE, func(i, j int) bool { return outE[i].ID < outE[j].ID })
	outR := make([]Relation, 0, len(rels))
	for _, r := range rels {
		outR = append(outR, r)
	}
	sort.Slice(outR, func(i, j int) bool {
		if outR[i].Type != outR[j].Type {
			return outR[i].Type < outR[j].Type
		}
		if outR[i].ParentID != outR[j].ParentID {
			return outR[i].ParentID < outR[j].ParentID
		}
		return outR[i].ChildID < outR[j].ChildID
	})
	return outE, outR
}

// seenLookback — span geçişinin penceresi: iki sync aralığı, en az 10 dk
// (entity_seen_5m kovası + gecikme), en çok 1 saat.
func seenLookback(interval time.Duration) time.Duration {
	d := 2 * interval
	if d < 10*time.Minute {
		d = 10 * time.Minute
	}
	if d > time.Hour {
		d = time.Hour
	}
	return d
}

// sortedUnmapped — deterministik Array çifti.
func sortedUnmapped(m map[string]int) ([]string, []uint32) {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	counts := make([]uint32, len(keys))
	for i, k := range keys {
		counts[i] = uint32(m[k])
	}
	return keys, counts
}
