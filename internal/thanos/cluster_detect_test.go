package thanos

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

// v0.10.140 — etiket algılama saf seçicisi: tercih sırası, ad eşleşmesi
// (tam > kısmi tek), tek değer, belirsizlik (yazılmaz), etiket yok.
func TestPickClusterLabel(t *testing.T) {
	rows := func(label string, vals ...string) []map[string]string {
		out := []map[string]string{}
		for _, v := range vals {
			out = append(out, map[string]string{label: v})
		}
		return out
	}
	cases := []struct {
		name      string
		rows      []map[string]string
		recName   string
		wantLabel string
		wantValue string
		ambiguous bool
	}{
		{"tam ad eşleşmesi", rows("cluster", "prod-eu", "prod-us"), "prod-eu", "cluster", "prod-eu", false},
		{"kısmi tek eşleşme", rows("cluster", "ocp-prod-eu-west-1", "ocp-prod-us"), "prod-eu", "cluster", "ocp-prod-eu-west-1", false},
		{"tek değer, ad uymasa da (güçlü etiket)", rows("cluster_id", "abc123"), "prod-eu", "cluster_id", "abc123", false},
		{"zayıf etikette tek değer AYIRMAZ → belirsiz", rows("prometheus", "openshift-monitoring/k8s"), "prod-eu", "prometheus", "", true},
		{"kapsama: cluster=mgmt tek değer ama başka seri yalnız cluster_id taşıyor → belirsiz", append(rows("cluster", "mgmt"), rows("cluster_id", "a1b2c3")...), "prod-eu", "cluster", "", true},
		{"belirsiz: iki değer, ad uymuyor", rows("cluster", "a", "b"), "prod-eu", "cluster", "", true},
		{"ters yön kısmi eşleşme KABUL EDİLMEZ (prod-eu ⊂ prod-eu-west adı)", rows("cluster", "prod-eu", "prod-us"), "prod-eu-west", "cluster", "", true},
		{"tercih sırası cluster > prometheus", append(rows("cluster", "prod-eu"), rows("prometheus", "openshift-monitoring/k8s")...), "prod-eu", "cluster", "prod-eu", false},
		{"aday etiket yok → matcher gerekmez", []map[string]string{{"node": "w1"}}, "prod-eu", "", "", false},
		{"boş sonuç", nil, "prod-eu", "", "", false},
	}
	for _, c := range cases {
		d := PickClusterLabel(c.rows, c.recName)
		if d.Label != c.wantLabel || d.Value != c.wantValue || d.Ambiguous != c.ambiguous {
			t.Errorf("%s: got (%q,%q,amb=%v) want (%q,%q,amb=%v)", c.name, d.Label, d.Value, d.Ambiguous, c.wantLabel, c.wantValue, c.ambiguous)
		}
	}
	if !strings.Contains(detectQuery(), "count by (cluster, cluster_id") || !strings.Contains(detectQuery(), "(kube_node_info)") {
		t.Fatalf("algılama sorgusu: %s", detectQuery())
	}
}

func TestApplyDetection(t *testing.T) {
	now := time.Date(2026, 8, 28, 12, 0, 0, 0, time.UTC)
	c, err := ApplyDetection(ClusterConfig{Name: "prod-eu"}, Detection{Label: "cluster", Value: "prod-eu"}, now)
	if err != nil || c.ThanosLabelName != "cluster" || c.ThanosLabelValue != "prod-eu" || c.ThanosLabelSource != "auto" || c.ThanosLabelDetectedAt != now.UnixMilli() {
		t.Fatalf("apply: %+v %v", c, err)
	}
	if _, err := ApplyDetection(ClusterConfig{Name: "x"}, Detection{Label: "cluster", Ambiguous: true, Candidates: map[string][]string{"cluster": {"a", "b"}}}, now); err == nil {
		t.Fatal("belirsizlik yazılmamalı")
	}
	if _, err := ApplyDetection(ClusterConfig{Name: "x", ThanosLabelName: "cluster", ThanosLabelValue: "eu"}, Detection{}, now); err == nil {
		t.Fatal("aday yok → MEVCUT matcher silinmez, hata döner (inceleme)")
	}
	none, err := ApplyDetection(ClusterConfig{Name: "x"}, Detection{}, now)
	if err != nil || none.ThanosLabelName != "" || none.ThanosLabelSource != "auto" {
		t.Fatalf("etiketsiz kayıt + aday yok → 'algılandı: etiket yok' (auto): %+v %v", none, err)
	}
}

// Denetim sonuçları pod'lar arasında blob ile taşınır: yaz → yükle round-trip;
// Reset blobu boşaltır.
type memLabelStore struct{ m map[string][]byte }

func (m *memLabelStore) GetSetting(_ context.Context, k string) ([]byte, error) { return m.m[k], nil }
func (m *memLabelStore) PutSetting(_ context.Context, k string, v []byte) error {
	if m.m == nil {
		m.m = map[string][]byte{}
	}
	m.m[k] = v
	return nil
}

func TestLabelChecksPersistRoundTrip(t *testing.T) {
	st := &memLabelStore{}
	a := &Service{}
	a.labelChecks = map[string]LabelCheck{"c-1": {OK: false, Error: "eşleşmiyor", CheckedAt: time.Date(2026, 8, 28, 12, 0, 0, 0, time.UTC)}}
	raw, _ := json.Marshal(a.labelChecks)
	if err := st.PutSetting(context.Background(), labelChecksKey, raw); err != nil {
		t.Fatal(err)
	}
	b := &Service{}
	if err := b.LoadLabelChecks(context.Background(), st); err != nil {
		t.Fatal(err)
	}
	if lc := b.labelCheckFor("c-1"); lc == nil || lc.OK || lc.Error != "eşleşmiyor" {
		t.Fatalf("round-trip: %+v", lc)
	}
	b.ResetLabelChecks(context.Background(), st)
	if b.labelCheckFor("c-1") != nil || string(st.m[labelChecksKey]) != "{}" {
		t.Fatal("reset bellek + blob")
	}
}
