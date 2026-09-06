package mcptools

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/cilcenk/coremetry/internal/chstore"
	"github.com/cilcenk/coremetry/internal/mcp"
)

// v0.10.468 (Faz 2, F2-1) — varlık kataloğu tool'ları: saf birleştiriciler,
// cluster çözümü, bayrak kapalı/yapılandırılmamış dürüstlük. Adlar SENTETİK.

var ecClusters = []ClusterRef{
	{ID: "c-1", Name: "eu-west", SpanValues: []string{"prod-eu-west", "dr-eu-west"}},
	{ID: "c-2", Name: "eu-central", SpanValues: []string{"prod-eu-central"}},
}

func ecDeps(enabled bool) Deps {
	return Deps{Clusters: func() []ClusterRef { return ecClusters }, EntityEnabled: func() bool { return enabled }}
}

func TestClustersFor(t *testing.T) {
	d := ecDeps(true)
	if cs, ok := clustersFor(d, ""); !ok || len(cs) != 2 {
		t.Fatalf("boş ref → tümü: %v %v", cs, ok)
	}
	for _, ref := range []string{"c-2", "EU-CENTRAL", "prod-eu-central"} {
		if cs, ok := clustersFor(d, ref); !ok || len(cs) != 1 || cs[0].ID != "c-2" {
			t.Errorf("%q → %v %v", ref, cs, ok)
		}
	}
	if _, ok := clustersFor(d, "zzz"); ok {
		t.Error("bilinmeyen ref ok olmamalı")
	}
	if cs, ok := clustersFor(Deps{}, ""); !ok || len(cs) != 0 {
		t.Errorf("yapılandırılmamış → boş, ok: %v %v", cs, ok)
	}
}

// Bayrak kapalıyken Store'a DOKUNMADAN disabled döner (Store nil).
func TestEntityToolsDisabledHonest(t *testing.T) {
	d := ecDeps(false)
	for _, tl := range []func(Deps) mcp.Tool{listNamespacesTool, listWorkloadsTool, listPodsTool} {
		tool := tl(d)
		out, err := tool.Handler(context.Background(), json.RawMessage(`{"namespace":"shop"}`))
		if err != nil {
			t.Fatalf("%s: hata değil dürüst cevap beklenir: %v", tool.Name, err)
		}
		m, _ := out.(map[string]any)
		if m == nil || m["disabled"] != true {
			t.Errorf("%s: disabled:true yok: %v", tool.Name, out)
		}
	}
	// namespace zorunlu — bayraktan ÖNCE (arg hatası her kurulumda aynı).
	if _, err := listWorkloadsTool(d).Handler(context.Background(), json.RawMessage(`{}`)); err == nil {
		t.Error("namespace boş → BadArgs")
	}
}

func TestNamespaceAndWorkloadRows(t *testing.T) {
	c := ecClusters[0]
	now := time.Date(2026, 9, 6, 12, 0, 0, 0, time.UTC)
	recs := []chstore.EntityRecord{{Name: "shop", Source: "thanos", LastSeen: now}, {Name: "core", Source: "span", LastSeen: now}}
	rows := namespaceRows(c, recs, map[string]int{"shop": 3}, map[string]int{"shop": 12, "core": 4})
	if len(rows) != 2 || rows[0].Workloads != 3 || rows[0].Pods != 12 || rows[1].Workloads != 0 || rows[0].Cluster != "eu-west" {
		t.Fatalf("namespace satırları: %+v", rows)
	}
	wl := []chstore.EntityRecord{
		{ID: "wl:c-1/shop/Deployment/api", Namespace: "shop", Name: "api", Source: "thanos", Labels: map[string]string{"kind": "Deployment"}},
		{ID: "wl:c-1/shop/StatefulSet/db", Namespace: "shop", Name: "db", Source: "thanos", Labels: map[string]string{"kind": "StatefulSet"}},
	}
	podParent := map[string]string{"api-1": "wl:c-1/shop/Deployment/api", "api-2": "wl:c-1/shop/Deployment/api"}
	seen := []chstore.EntitySeenAgg{{Pod: "api-1", Service: "shop-api", Spans: 100, Errors: 2}, {Pod: "api-2", Service: "shop-api", Spans: 50}, {Pod: "orphan-1", Service: "x", Spans: 9}}
	out := workloadRows(c, wl, map[string]int{"wl:c-1/shop/Deployment/api": 2, "wl:c-1/shop/StatefulSet/db": 1}, podParent, seen, "")
	if len(out) != 2 {
		t.Fatalf("workload satırları: %+v", out)
	}
	api, db := out[0], out[1]
	if !api.Telemetry || api.Pods != 2 || api.Spans != 150 || api.Errors != 2 || len(api.Services) != 1 || api.Services[0] != "shop-api" {
		t.Errorf("api satırı: %+v", api)
	}
	if db.Telemetry || db.Pods != 1 || len(db.Services) != 0 {
		t.Errorf("db satırı telemetrisiz olmalı: %+v", db)
	}
	if only := workloadRows(c, wl, nil, nil, nil, "statefulset"); len(only) != 1 || only[0].Workload != "db" {
		t.Errorf("kind süzgeci harfe duyarsız: %+v", only)
	}
}

func TestPodRows(t *testing.T) {
	c := ecClusters[0]
	last := time.Date(2026, 9, 6, 11, 59, 0, 0, time.UTC)
	pods := []chstore.EntityRecord{
		{Name: "api-1", Namespace: "shop", ParentID: "wl:c-1/shop/Deployment/api", Source: "thanos"},
		{Name: "api-2", Namespace: "shop", ParentID: "wl:c-1/shop/Deployment/api", Source: "thanos", Stale: true},
	}
	seen := []chstore.EntitySeenAgg{{Pod: "api-1", Service: "shop-api", Node: "node-a", Spans: 40, Errors: 1, LastSeen: last}}
	rows := podRows(c, pods, seen)
	if len(rows) != 2 || rows[0].Pod != "api-1" || rows[0].Workload != "api" || rows[0].Node != "node-a" || rows[0].Spans != 40 || rows[0].LastSpan == "" {
		t.Fatalf("pod satırları: %+v", rows)
	}
	if rows[1].Spans != 0 || rows[1].LastSpan != "" || !rows[1].Stale || len(rows[1].Services) != 0 {
		t.Errorf("telemetrisiz pod dürüst (0 span, boş servis): %+v", rows[1])
	}
}

func TestFuzzyPick(t *testing.T) {
	recs := []chstore.EntityRecord{{Name: "shop-payment"}, {Name: "shop-catalog"}, {Name: "core"}}
	if got := fuzzyPick("shop pay", recs); len(got) != 1 || got[0].Name != "shop-payment" {
		t.Errorf("bulanık: %+v", got)
	}
	if got := fuzzyPick("", recs); len(got) != 3 {
		t.Errorf("boş sorgu dokunmaz: %+v", got)
	}
}
