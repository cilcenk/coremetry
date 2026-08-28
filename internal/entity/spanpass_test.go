package entity

import (
	"context"
	"testing"
	"time"
)

// v0.10.129 — SPAN-TÜREVLİ çıkarım (design §3 "spanpass", görev: "Cluster
// değeri eşlenemeyen veri: düşürülmediği ve sayaçlandığı doğrulanmalı").
//
// Sözleşme:
//   - entity_seen_5m satırları (span cluster değeri, ns, pod, node, servis)
//     Remote Cluster kaydının SpanClusterValue'suna eşlenir → cluster_id.
//     Eşlenemeyen değer SESSİZCE DÜŞMEZ: `değer → satır sayısı` sayaçlanır.
//   - Eşlenen satırlar cluster'ın `seen` kümesine eklenir: pod varlığı
//     (source=span, Thanos zaten yazdıysa Thanos'unki kazanır), `svc:`
//     varlığı, `runs` (pod→svc) ilişkisi, node biliniyorsa `runs_on`.

func TestGroupSeenByCluster(t *testing.T) {
	refs := []ClusterRef{{ID: "c-1", Name: "ocp-a", SpanClusterValue: "ocp-a"}, {ID: "c-2", Name: "b", SpanClusterValue: "cluster-b-spans"}}
	rows := []SeenRow{
		{ClusterValue: "ocp-a", Namespace: "pay", Pod: "api-x1", Service: "api"},
		{ClusterValue: "cluster-b-spans", Namespace: "pay", Pod: "api-y1", Service: "api"},
		{ClusterValue: "unknown-1", Namespace: "pay", Pod: "z", Service: "z"},
		{ClusterValue: "unknown-1", Namespace: "pay", Pod: "z2", Service: "z"},
		{ClusterValue: "", Namespace: "pay", Pod: "noclu", Service: "n"},
	}
	byCID, unmapped := GroupSeenByCluster(rows, refs)
	if len(byCID["c-1"]) != 1 || len(byCID["c-2"]) != 1 {
		t.Fatalf("eşleme: %+v", byCID)
	}
	if unmapped["unknown-1"] != 2 || unmapped[""] != 1 {
		t.Fatalf("eşlenemeyen değerler sayaçlanmalı (unknown-1→2, ''→1): %+v", unmapped)
	}
}

func TestSpanSeenToEntities(t *testing.T) {
	t0 := time.Date(2026, 8, 28, 12, 0, 0, 0, time.UTC)
	rows := []SeenRow{
		{ClusterValue: "ocp-a", Namespace: "pay", Pod: "api-x1", Node: "w1", Service: "api", Spans: 120, LastSeen: t0},
		{ClusterValue: "ocp-a", Namespace: "pay", Pod: "api-x1", Node: "w1", Service: "sidecar-agent", Spans: 3, LastSeen: t0},
		{ClusterValue: "ocp-a", Namespace: "pay", Pod: "db-0", Node: "", Service: "db", Spans: 9, LastSeen: t0},
	}
	thanosPods := map[string]Entity{PodID("c-1", "pay", "api-x1"): {ID: PodID("c-1", "pay", "api-x1"), Type: TypePod, Source: SourceThanos, UID: "u1"}}
	ents, rels := SpanSeenToEntities("c-1", rows, thanosPods)
	byID := map[string]Entity{}
	for _, e := range ents {
		byID[e.ID] = e
	}
	// Thanos'un bildiği pod yeniden ÜRETİLMEZ (Thanos kazanır); db-0 span kaynaklı pod olur.
	if _, dup := byID[PodID("c-1", "pay", "api-x1")]; dup {
		t.Fatal("Thanos'un bildiği pod span geçişinde yeniden üretilmemeli")
	}
	if p, ok := byID[PodID("c-1", "pay", "db-0")]; !ok || p.Source != SourceSpan || p.Namespace != "pay" {
		t.Fatalf("span-kaynaklı pod: %+v", p)
	}
	for _, svc := range []string{"api", "sidecar-agent", "db"} {
		if e, ok := byID[ServiceID(svc)]; !ok || e.Type != TypeService || e.ClusterID != "c-1" {
			t.Fatalf("servis varlığı %s: %+v", svc, e)
		}
	}
	has := func(typ, p, c string) bool {
		for _, r := range rels {
			if r.Type == typ && r.ParentID == p && r.ChildID == c {
				return r.Source == SourceSpan
			}
		}
		return false
	}
	if !has(RelRuns, PodID("c-1", "pay", "api-x1"), ServiceID("api")) || !has(RelRuns, PodID("c-1", "pay", "api-x1"), ServiceID("sidecar-agent")) {
		t.Fatal("pod→servis runs ilişkileri (iki servis) yazılmalı, source=span")
	}
	if !has(RelRunsOn, PodID("c-1", "pay", "api-x1"), NodeID("c-1", "w1")) {
		t.Fatal("node biliniyorsa span yolundan runs_on üretilmeli")
	}
	if has(RelRunsOn, PodID("c-1", "pay", "db-0"), NodeID("c-1", "")) {
		t.Fatal("node'suz satır runs_on üretmemeli")
	}
	// Deterministik sıra.
	for i := 1; i < len(ents); i++ {
		if ents[i-1].ID > ents[i].ID {
			t.Fatal("varlıklar sıralı olmalı")
		}
	}
}

// Syncer'a entegre: span satırları Thanos anlık görüntüsüyle AYNI tick'te
// aynı diff'e girer (ayrı diff her şeyi kapatırdı); eşlenemeyen değerler
// '(unmapped)' koşu satırına sayaçlanır.
func TestSyncerSpanPass(t *testing.T) {
	t0 := time.Date(2026, 8, 28, 12, 0, 0, 0, time.UTC)
	src := &fakeSource{clusters: []ClusterRef{{ID: "c-a", Name: "a", SpanClusterValue: "a"}}, sets: map[string]SampleSets{"c-a": podSet("db-0")}}
	st := newMemStore()
	sy := NewSyncer(src, st, func() Resolved { return Resolved{Enabled: true, PodGap: 10 * time.Minute, ParallelClusters: 1} })
	sy.now = func() time.Time { return t0 }
	sy.seen = fakeSeen{rows: []SeenRow{
		{ClusterValue: "a", Namespace: "ns", Pod: "db-0", Node: "w1", Service: "db", Spans: 5, LastSeen: t0},
		{ClusterValue: "a", Namespace: "ns", Pod: "span-only", Node: "w1", Service: "batch", Spans: 2, LastSeen: t0},
		{ClusterValue: "ghost", Namespace: "ns", Pod: "g", Service: "g", Spans: 7, LastSeen: t0},
	}}
	sy.Tick(context_bg())
	if _, ok := st.open["c-a"][relKey(RelRuns, PodID("c-a", "ns", "db-0"), ServiceID("db"))]; !ok {
		t.Fatal("Thanos pod'u için span runs ilişkisi açık olmalı")
	}
	if _, ok := st.open["c-a"][PodID("c-a", "ns", "span-only")]; !ok {
		t.Fatal("span-only pod varlık olmalı")
	}
	var unmappedRun *Run
	for i := range st.runs {
		if st.runs[i].ClusterID == UnmappedClusterID {
			unmappedRun = &st.runs[i]
		}
	}
	if unmappedRun == nil || len(unmappedRun.UnmappedKeys) != 1 || unmappedRun.UnmappedKeys[0] != "ghost" || unmappedRun.UnmappedCounts[0] != 1 {
		t.Fatalf("eşlenemeyen cluster değeri koşu satırına sayaçlanmalı: %+v", unmappedRun)
	}
	// İkinci tick: span-only pod artık görülmüyor → kapanır (Thanos ulaşıldı).
	sy.now = func() time.Time { return t0.Add(time.Minute) }
	sy.seen = fakeSeen{rows: []SeenRow{{ClusterValue: "a", Namespace: "ns", Pod: "db-0", Node: "w1", Service: "db", Spans: 5, LastSeen: t0.Add(time.Minute)}}}
	sy.Tick(context_bg())
	if _, still := st.open["c-a"][PodID("c-a", "ns", "span-only")]; still {
		t.Fatal("görülmeyen span-only pod kapanmalı")
	}
}

type fakeSeen struct{ rows []SeenRow }

func (f fakeSeen) RecentSeen(ctx context.Context, since time.Time) ([]SeenRow, error) {
	return f.rows, nil
}

func context_bg() context.Context { return context.Background() }
