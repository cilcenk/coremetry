package entity

import (
	"testing"
	"time"
)

// v0.10.471 (F2-4, G14) — pod adından workload; KSM varsa çalışmaz.

func TestDerivedWorkload(t *testing.T) {
	cases := []struct{ pod, kind, name string }{
		{"payment-api-7d9f8c6b5-x2k9q", "Deployment", "payment-api"},
		{"db-0", "StatefulSet", "db"},
		{"kafka-12", "StatefulSet", "kafka"},
		{"node-exporter-abc12", DerivedKind, "node-exporter"},
		{"standalone", "", ""},
	}
	for _, c := range cases {
		kind, name, ok := DerivedWorkload(c.pod)
		if (c.kind == "") != !ok || kind != c.kind || name != c.name {
			t.Errorf("%q → %s/%s/%v; want %s/%s", c.pod, kind, name, ok, c.kind, c.name)
		}
	}
}

func TestSpanSeenDerivesWorkloadOnlyWithoutKSM(t *testing.T) {
	t0 := time.Date(2026, 9, 6, 12, 0, 0, 0, time.UTC)
	rows := []SeenRow{
		{ClusterValue: "ocp-a", Namespace: "shop", Pod: "api-7d9f8c6b5-x2k9q", Service: "shop-api", Spans: 10, LastSeen: t0},
		{ClusterValue: "ocp-a", Namespace: "shop", Pod: "api-7d9f8c6b5-abcde", Service: "shop-api", Spans: 10, LastSeen: t0},
		{ClusterValue: "ocp-a", Namespace: "shop", Pod: "known-5c8d9-zzzzz", Service: "known", Spans: 1, LastSeen: t0},
	}
	known := map[string]Entity{PodID("c-1", "shop", "known-5c8d9-zzzzz"): {ID: PodID("c-1", "shop", "known-5c8d9-zzzzz"), Type: TypePod, Source: SourceThanos}}
	ents, rels := SpanSeenToEntities("c-1", rows, known)
	byID := map[string]Entity{}
	for _, e := range ents {
		byID[e.ID] = e
	}
	wlID := WorkloadID("c-1", "shop", "Deployment", "api")
	wl, ok := byID[wlID]
	if !ok || wl.Source != SourceSpan || wl.Labels["derived"] != "pod-name" || wl.Labels["kind"] != "Deployment" {
		t.Fatalf("türetilmiş workload: %+v", wl)
	}
	for _, pod := range []string{"api-7d9f8c6b5-x2k9q", "api-7d9f8c6b5-abcde"} {
		if p := byID[PodID("c-1", "shop", pod)]; p.ParentID != wlID {
			t.Errorf("pod %s parent %q, want %q", pod, p.ParentID, wlID)
		}
	}
	if _, dup := byID[WorkloadID("c-1", "shop", "Deployment", "known")]; dup {
		t.Error("KSM'nin bildiği pod için workload türetilmemeli")
	}
	nsWl, wlPod := false, 0
	for _, r := range rels {
		if r.Type == RelParent && r.ParentID == NamespaceID("c-1", "shop") && r.ChildID == wlID {
			nsWl = true
		}
		if r.Type == RelParent && r.ParentID == wlID {
			wlPod++
		}
	}
	if !nsWl || wlPod != 2 {
		t.Errorf("ilişkiler: ns→wl=%v wl→pod=%d", nsWl, wlPod)
	}
}
