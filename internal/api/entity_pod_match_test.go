package api

// entity_pod_match_test.go — v0.10.190 sözleşmesi (entity_pod_match.go başlığı).

import (
	"os"
	"strings"
	"testing"

	"github.com/cilcenk/coremetry/internal/chstore"
	"github.com/cilcenk/coremetry/internal/thanos"
)

func TestMatchThanosPod(t *testing.T) {
	idx := indexThanosPods([]thanos.PodRow{
		{Namespace: "pay", Pod: "api-1", CPUCores: 0.5},
		{Namespace: "pay", Pod: "kafka-0", CPUCores: 1},
		{Namespace: "log", Pod: "kafka-0", CPUCores: 2},
	})
	cases := []struct {
		ns, pod string
		want    thanosMatch
		wantNS  string
	}{
		{"pay", "api-1", thanosMatchExact, "pay"},
		{"", "api-1", thanosMatchFilled, "pay"},   // namespace'siz span → Thanos tek → tamamlanır
		{"", "kafka-0", thanosMatchAmbiguous, ""}, // iki namespace'te aynı ad → eşlenmez
		{"", "ghost", thanosMatchNone, ""},        // Thanos'ta yok
		{"other", "api-1", thanosMatchNone, ""},   // namespace VAR ama yanlış → ad eşlemesi YAPILMAZ
		{"log", "kafka-0", thanosMatchExact, "log"},
	}
	for _, c := range cases {
		p, got := matchThanosPod(idx, c.ns, c.pod)
		if got != c.want || p.Namespace != c.wantNS {
			t.Fatalf("(%q,%q): got %v ns=%q, want %v ns=%q", c.ns, c.pod, got, p.Namespace, c.want, c.wantNS)
		}
	}
}

// Pod → servisler ve pod adları sorguları namespace'siz MV satırlarını da
// eşlemeli (`k8s_namespace IN (?, ”)`) — kaynak pini: iki sorguda da.
func TestEntitySeenPodLookupsAcceptNamespacelessRows(t *testing.T) {
	b, err := os.ReadFile("../chstore/entity_queries.go")
	if err != nil {
		t.Fatal(err)
	}
	src := string(b)
	i := strings.Index(src, "func (s *Store) EntitySeenForPods(")
	if i < 0 {
		t.Fatal("EntitySeenForPods yok")
	}
	if !strings.Contains(src[i:], "k8s_namespace IN (?, '')") {
		t.Fatal("EntitySeenForPods namespace'siz satırı eşlemiyor (v0.10.190)")
	}
}

func TestMergeServicePodRows(t *testing.T) {
	seen := func(ns string, spans int64) chstore.EntitySeenAgg {
		return chstore.EntitySeenAgg{Cluster: "c", Namespace: ns, Pod: "api-1", Service: "svc", Spans: spans}
	}
	rows := []servicePodRow{
		{EntitySeenAgg: seen("pay", 10), ClusterID: "cid", EntityID: "pod:cid/pay/api-1"},                              // span namespace'li
		{EntitySeenAgg: seen("pay", 5), ClusterID: "cid", NamespaceFromThanos: true, StatusKnown: true, CPUCores: 0.5}, // '' idi, Thanos doldurdu
		{EntitySeenAgg: seen("", 3), ClusterID: ""},                                                                    // eşlenmemiş cluster, olduğu gibi
	}
	out := mergeServicePodRows(rows)
	if len(out) != 2 {
		t.Fatalf("aynı pod tek satır olmalı: %d", len(out))
	}
	m := out[0]
	if m.Spans != 15 || !m.StatusKnown || m.CPUCores != 0.5 || m.NamespaceFromThanos || m.EntityID != "pod:cid/pay/api-1" {
		t.Fatalf("birleşim: %+v", m)
	}
}
