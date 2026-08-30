package api

import (
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/cilcenk/coremetry/internal/chstore"
)

// rollouts_test.go — v0.10.200 sözleşmesi (rollouts.go + rollout_keys.go).
//
// (1) Kayıt pini: registerRolloutRoutes api.go'da ÇAĞRILIR, rota dizeleri
//     api.go'da DEĞİLDİR (kayıtsız rota 404 değil boş sayfa — SPA catch-all;
//     otomatik kapısı yok). anomaly_verdicts_test.go emsali.
// (2) Anahtar invaryantları: her girdi anahtarı ayırır (cache_key_test.go),
//     ayraç saldırısı aynı ön-görüntüyü üretemez, aynı 30 s kova tek girdi.

func TestRolloutRoutesRegisteredOnceInApiGo(t *testing.T) {
	b, err := os.ReadFile("api.go")
	if err != nil {
		t.Fatal(err)
	}
	src := string(b)
	if strings.Count(src, "s.registerRolloutRoutes(mux)") != 1 {
		t.Fatal("registerRolloutRoutes(mux) api.go'da tam bir kez çağrılmalı")
	}
	for _, route := range []string{`"GET /api/rollouts"`, `"GET /api/rollout"`, `"GET /api/rollouts/stats"`, `"GET /api/rollouts/runs"`, `"GET /api/settings/rollouts"`, `"PUT /api/settings/rollouts"`} {
		if strings.Contains(src, route) {
			t.Fatalf("rota api.go'ya sızmış (kendi dosyasında kalmalı): %s", route)
		}
	}
}

// TestRolloutRoutesResolveOnMux — kayıt pini metin değil GERÇEK mux'la:
// HandleFunc satırı silinirse SPA catch-all 200+boş sayfa döner ve metin
// pini bunu göremezdi (inceleme #13).
func TestRolloutRoutesResolveOnMux(t *testing.T) {
	mux := http.NewServeMux()
	(&Server{}).registerRolloutRoutes(mux)
	for _, p := range []struct{ m, path string }{
		{"GET", "/api/rollouts"}, {"GET", "/api/rollout"}, {"GET", "/api/rollouts/stats"},
		{"GET", "/api/rollouts/runs"}, {"GET", "/api/settings/rollouts"}, {"PUT", "/api/settings/rollouts"},
	} {
		if _, pat := mux.Handler(httptest.NewRequest(p.m, p.path, nil)); pat == "" {
			t.Fatalf("%s %s mux'ta çözülmüyor", p.m, p.path)
		}
	}
}

func TestRolloutKeysCarryEveryInput(t *testing.T) {
	from := time.Date(2026, 8, 30, 10, 0, 0, 0, time.UTC)
	to := from.Add(time.Hour)
	base := chstore.RolloutFilter{ClusterID: "c-1", Namespace: "pay", Workload: "api", Status: "completed", Kind: "Deployment"}
	k0 := rolloutsListKey(base, 100, from, to)
	variants := []chstore.RolloutFilter{
		{ClusterID: "c-2", Namespace: "pay", Workload: "api", Status: "completed", Kind: "Deployment"},
		{ClusterID: "c-1", Namespace: "core", Workload: "api", Status: "completed", Kind: "Deployment"},
		{ClusterID: "c-1", Namespace: "pay", Workload: "web", Status: "completed", Kind: "Deployment"},
		{ClusterID: "c-1", Namespace: "pay", Workload: "api", Status: "rolled_back", Kind: "Deployment"},
		{ClusterID: "c-1", Namespace: "pay", Workload: "api", Status: "completed", Kind: "StatefulSet"},
	}
	for _, v := range variants {
		if rolloutsListKey(v, 100, from, to) == k0 {
			t.Fatalf("girdi anahtarı ayırmadı: %+v", v)
		}
	}
	if rolloutsListKey(base, 50, from, to) == k0 {
		t.Fatal("limit anahtarda olmalı")
	}
	if rolloutsListKey(base, 100, from.Add(time.Hour), to.Add(time.Hour)) == k0 {
		t.Fatal("pencere anahtarda olmalı")
	}
	// aynı 30 s kova → tek girdi (FE her tick'te yeniden hesaplar)
	if rolloutsListKey(base, 100, from.Add(7*time.Second), to.Add(7*time.Second)) != k0 {
		t.Fatal("aynı 30 s kovası tek girdi olmalı")
	}
	// ayraç saldırısı: parçalar ayrı ayrı özetlenir
	a := chstore.RolloutFilter{ClusterID: "c\x00pay", Namespace: ""}
	bf := chstore.RolloutFilter{ClusterID: "c", Namespace: "\x00pay"}
	if rolloutsListKey(a, 100, from, to) == rolloutsListKey(bf, 100, from, to) {
		t.Fatal("NUL kaydırması anahtar çakıştırmamalı")
	}
	// kararlılık
	if rolloutsListKey(base, 100, from, to) != k0 {
		t.Fatal("anahtar kararlı olmalı")
	}
	id := chstore.RolloutID{ClusterID: "c-1", Namespace: "pay", Workload: "api", Revision: "api-abc", StartedAt: from}
	if rolloutKey(id) == rolloutKey(chstore.RolloutID{ClusterID: "c-1", Namespace: "pay", Workload: "api", Revision: "api-abc", StartedAt: from.Add(time.Minute)}) {
		t.Fatal("startedAt anahtarda olmalı")
	}
	if rolloutStatsKey("c-1", "pay", 10, from, to) == rolloutStatsKey("c-1", "pay", 20, from, to) {
		t.Fatal("topN anahtarda olmalı")
	}
}
