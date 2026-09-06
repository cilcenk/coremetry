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
	FirstSeen, LastSeen                         time.Time
}

// SeenReader — chstore.EntitySeenRecent adaptörü.
type SeenReader interface {
	RecentSeen(ctx context.Context, since time.Time) ([]SeenRow, error)
	// RecentSeenFor — v0.10.141: yalnız bir span cluster değerinin satırları
	// (backfill; kesim tavanı o değere uygulanır).
	RecentSeenFor(ctx context.Context, since time.Time, clusterValue string) ([]SeenRow, error)
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
				// v0.10.471 (F2-4, G14) — KSM sahibi yok: workload'ı pod adından türet
				// (derived_workload.go); türetilemezse pod doğrudan namespace'in altında.
				parent := nsID
				if kind, name, ok := DerivedWorkload(r.Pod); ok {
					wlID := WorkloadID(cid, r.Namespace, kind, name)
					if _, known := known[wlID]; !known {
						if _, dup := ents[wlID]; !dup {
							ents[wlID] = Entity{Type: TypeWorkload, ClusterID: cid, ID: wlID, Namespace: r.Namespace, Name: name, ParentID: nsID,
								Labels: map[string]string{"kind": kind, "derived": "pod-name"}, Source: SourceSpan}
							rel(RelParent, nsID, wlID)
						}
					}
					parent = wlID
				}
				ents[podID] = Entity{Type: TypePod, ClusterID: cid, ID: podID, Namespace: r.Namespace, Name: r.Pod, ParentID: parent, Labels: map[string]string{}, Source: SourceSpan}
				rel(RelParent, parent, podID)
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

// ── v0.10.141 — atama sonrası geriye dönük geçiş (backfill) ──
//
// İnceleme dersi: 24 saatlik pencereyi TÜM tick'e uygulamak her cluster'ın
// ölü pod'larını "şimdi açıldı" diye yeniden açıyordu. Doğrusu: yalnız
// ATANAN değerin satırları; canlı olanlar (son görülme ≤ podGap) normal
// diff'e katılır, ölü olanlar KAPALI ömür olarak yazılır (valid_from =
// ilk görülme, valid_to = son görülme) — ve yalnız hiç kaydı olmayan
// entity'ler için (Existing süzgeci), böylece yeniden koşum idempotent.

// SplitBackfillRows — saf: değere süz, canlı/ölü ayır. Karar POD başına:
// aynı pod'un bir satırı (servis) canlıysa TÜM satırları canlıdır — satır
// başına karar, canlı pod'a eski bir servis satırı yüzünden kapalı ömür
// yazdırırdı.
func SplitBackfillRows(rows []SeenRow, value string, liveCut time.Time) (live, dead []SeenRow) {
	alive := map[string]bool{}
	for _, r := range rows {
		if r.ClusterValue == value && r.Pod != "" && r.Namespace != "" && !r.LastSeen.Before(liveCut) {
			alive[r.Namespace+"/"+r.Pod] = true
		}
	}
	for _, r := range rows {
		if r.ClusterValue != value || r.Pod == "" || r.Namespace == "" {
			continue
		}
		if alive[r.Namespace+"/"+r.Pod] {
			live = append(live, r)
		} else {
			dead = append(dead, r)
		}
	}
	return live, dead
}

// ClosedRowsForDead — saf: ölü satırlardan yalnız POD entity'leri (kapalı
// ömür) + ilişkileri (parent ns→pod, runs pod→svc, runs_on pod→node; hepsi
// kapalı). Namespace/servis/node kayıtları canlı yoldan gelir; burada
// üretilmez. Aynı pod'un birden çok satırı (servis başına) birleşir.
func ClosedRowsForDead(cid string, dead []SeenRow) ([]EntityRow, []RelationRow) {
	type agg struct {
		ns, pod, node string
		first, last   time.Time
		services      map[string]bool
	}
	pods := map[string]*agg{}
	for _, r := range dead {
		id := PodID(cid, r.Namespace, r.Pod)
		a := pods[id]
		if a == nil {
			a = &agg{ns: r.Namespace, pod: r.Pod, first: r.FirstSeen, last: r.LastSeen, services: map[string]bool{}}
			pods[id] = a
		}
		if a.node == "" && r.Node != "" {
			a.node = r.Node
		}
		if !r.FirstSeen.IsZero() && (a.first.IsZero() || r.FirstSeen.Before(a.first)) {
			a.first = r.FirstSeen
		}
		if r.LastSeen.After(a.last) {
			a.last = r.LastSeen
		}
		if r.Service != "" {
			a.services[r.Service] = true
		}
	}
	ids := make([]string, 0, len(pods))
	for id := range pods {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	var rows []EntityRow
	var rels []RelationRow
	for _, id := range ids {
		a := pods[id]
		from := a.first
		if from.IsZero() || from.After(a.last) {
			from = a.last
		}
		nsID := NamespaceID(cid, a.ns)
		rows = append(rows, EntityRow{
			Type: TypePod, ClusterID: cid, ID: id, Namespace: a.ns, Name: a.pod, ParentID: nsID,
			ValidFrom: from, ValidTo: a.last, FirstSeen: from, LastSeen: a.last,
			LabelKeys: []string{}, LabelValues: []string{}, Source: SourceSpan,
		})
		rels = append(rels, RelationRow{Type: RelParent, ClusterID: cid, ParentID: nsID, ChildID: id, ValidFrom: from, ValidTo: a.last, LastSeen: a.last, Source: SourceSpan})
		svcs := make([]string, 0, len(a.services))
		for sname := range a.services {
			svcs = append(svcs, sname)
		}
		sort.Strings(svcs)
		for _, sname := range svcs {
			rels = append(rels, RelationRow{Type: RelRuns, ClusterID: cid, ParentID: id, ChildID: ServiceID(sname), ValidFrom: from, ValidTo: a.last, LastSeen: a.last, Source: SourceSpan})
		}
		if a.node != "" {
			rels = append(rels, RelationRow{Type: RelRunsOn, ClusterID: cid, ParentID: id, ChildID: NodeID(cid, a.node), ValidFrom: from, ValidTo: a.last, LastSeen: a.last, Source: SourceSpan})
		}
	}
	return rows, rels
}
