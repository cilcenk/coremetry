package chstore

import (
	"strings"
	"testing"
	"time"
)

// v0.10.130 — entity sorgu katmanı (design §4). Sözleşme: her sorgu
// cluster_id + zaman filtresi + LIMIT taşır, FINAL okur; "o an geçerli"
// = valid_from <= t AND (valid_to = 0 OR valid_to >= t); at sıfırsa
// "şu an açık" (valid_to = 0). Pivotlar entity_seen_5m'i service_name
// önekinden okur (servis → pod'lar), pod → servisler ilişkiden.

func TestEntityValidAtSQL(t *testing.T) {
	sql, args := entityValidAtSQL(time.Time{})
	if !strings.Contains(sql, "valid_to = toDateTime(0)") || len(args) != 0 {
		t.Fatalf("at sıfır → yalnız açık ömürler: %s %v", sql, args)
	}
	at := time.Date(2026, 8, 28, 12, 0, 0, 0, time.UTC)
	sql, args = entityValidAtSQL(at)
	if !strings.Contains(sql, "valid_from <= ?") || !strings.Contains(sql, "valid_to = toDateTime(0) OR valid_to >= ?") || len(args) != 2 {
		t.Fatalf("at dolu → aralık: %s %v", sql, args)
	}
}

func TestEntityListSQLShape(t *testing.T) {
	q := EntityListQuery{ClusterID: "c-1", Type: "pod", Namespace: "pay", Search: "api", Limit: 50}
	sql, args := entityListSQL(q)
	for _, want := range []string{"FROM entities FINAL", "cluster_id = ?", "entity_type = ?", "namespace = ?", "ILIKE", "LIMIT ?", "max_execution_time"} {
		if !strings.Contains(sql, want) {
			t.Fatalf("liste SQL %q içermeli:\n%s", want, sql)
		}
	}
	// Arama joker'i sunucu tarafında sarılır; kullanıcı '%' ve '_' kaçışlanır.
	found := false
	for _, a := range args {
		if s, ok := a.(string); ok && s == "%api%" {
			found = true
		}
	}
	if !found {
		t.Fatalf("arama %%api%% olarak bind edilmeli: %v", args)
	}
	if _, a2 := entityListSQL(EntityListQuery{ClusterID: "c", Search: "100%_x"}); !containsArg(a2, `%100\%\_x%`) {
		t.Fatalf("joker karakterleri kaçışlanmalı: %v", a2)
	}
	// Limit kelepçesi: 0 → 100, >500 → 500.
	if _, a := entityListSQL(EntityListQuery{ClusterID: "c"}); !containsArg(a, 100) {
		t.Fatalf("varsayılan limit 100: %v", a)
	}
	if _, a := entityListSQL(EntityListQuery{ClusterID: "c", Limit: 9999}); !containsArg(a, 500) {
		t.Fatalf("limit tavanı 500: %v", a)
	}
}

func containsArg(args []any, want any) bool {
	for _, a := range args {
		if a == want {
			return true
		}
	}
	return false
}

// Ebeveyn zinciri: parent_id ile yukarı; döngü/eksik ebeveynde durur, en çok 8 seviye.
func TestWalkEntityParents(t *testing.T) {
	m := map[string]EntityRecord{
		"pod:c/ns/a":             {ID: "pod:c/ns/a", ParentID: "wl:c/ns/Deployment/api"},
		"wl:c/ns/Deployment/api": {ID: "wl:c/ns/Deployment/api", ParentID: "ns:c/ns"},
		"ns:c/ns":                {ID: "ns:c/ns", ParentID: "cluster:c"},
		"cluster:c":              {ID: "cluster:c"},
		"loop:1":                 {ID: "loop:1", ParentID: "loop:2"},
		"loop:2":                 {ID: "loop:2", ParentID: "loop:1"},
	}
	get := func(id string) (EntityRecord, bool) { r, ok := m[id]; return r, ok }
	chain := walkEntityParents(get, "pod:c/ns/a")
	if len(chain) != 3 || chain[0].ID != "wl:c/ns/Deployment/api" || chain[2].ID != "cluster:c" {
		t.Fatalf("zincir: %+v", chain)
	}
	if got := walkEntityParents(get, "loop:1"); len(got) > 8 {
		t.Fatalf("döngü sınırlanmalı: %d", len(got))
	}
	if got := walkEntityParents(get, "yok"); len(got) != 0 {
		t.Fatalf("bilinmeyen id boş zincir: %+v", got)
	}
}

// v0.10.135 — DETAY SAYFALARI adım 1 (pod detay). Zaman geçerliliği: tarihsel
// bir trace'ten gelen ?at= o an geçerli ömrü seçer; kapsayan ömür yoksa en
// yeni kayıt + match=false (sayfa "o an geçerli değildi / artık yok" der).
func TestPickLifetimeTimeValidity(t *testing.T) {
	ts := func(h int) time.Time { return time.Date(2026, 8, 28, h, 0, 0, 0, time.UTC) }
	closedTo := ts(10)
	old := EntityRecord{ID: "pod:c/ns/api-1", ValidFrom: ts(1), ValidTo: &closedTo}
	cur := EntityRecord{ID: "pod:c/ns/api-1", ValidFrom: ts(12)}
	all := []EntityRecord{cur, old} // valid_from DESC — sorgunun sırası
	cases := []struct {
		name      string
		all       []EntityRecord
		at        time.Time
		wantIdx   int
		wantMatch bool
	}{
		{"at sıfır → açık ömür", all, time.Time{}, 0, true},
		{"eski ömrün içi → eski kayıt", all, ts(5), 1, true},
		{"iki ömür arası boşluk → en yeni, eşleşme yok", all, ts(11), 0, false},
		{"sınır: valid_to anı dahil", all, ts(10), 1, true},
		{"sınır: valid_from anı dahil", all, ts(12), 0, true},
		{"gelecek an → açık ömür kapsar", all, ts(20), 0, true},
		{"hepsi kapalı + at sıfır → en yeni, ölü", []EntityRecord{old}, time.Time{}, 0, false},
		{"boş → -1", nil, ts(5), -1, false},
	}
	for _, c := range cases {
		idx, match := pickLifetime(c.all, c.at)
		if idx != c.wantIdx || match != c.wantMatch {
			t.Errorf("%s: (%d,%v), beklenen (%d,%v)", c.name, idx, match, c.wantIdx, c.wantMatch)
		}
	}
}

// Kardeş/çocuk/tam-ad yan tümceleri; aynı pod adı iki cluster'da iki ayrı
// sorgu argümanıdır (cluster_id daima ilk koşul).
func TestEntityListSQLSiblingsAndExact(t *testing.T) {
	sql, args := entityListSQL(EntityListQuery{ClusterID: "c-1", Type: "pod", ParentID: "wl:c-1/ns/Deployment/api", ExcludeID: "pod:c-1/ns/api-1", Name: "api-2"})
	for _, want := range []string{"cluster_id = ?", "parent_id = ?", "entity_id != ?", "name = ?"} {
		if !strings.Contains(sql, want) {
			t.Fatalf("liste SQL %q içermeli:\n%s", want, sql)
		}
	}
	for _, want := range []any{"wl:c-1/ns/Deployment/api", "pod:c-1/ns/api-1", "api-2"} {
		if !containsArg(args, want) {
			t.Fatalf("arg %v bind edilmeli: %v", want, args)
		}
	}
	sqlA, argsA := entityListSQL(EntityListQuery{ClusterID: "c-a", Type: "pod", Name: "api-1"})
	sqlB, argsB := entityListSQL(EntityListQuery{ClusterID: "c-b", Type: "pod", Name: "api-1"})
	if sqlA != sqlB {
		t.Fatal("SQL şekli cluster'dan bağımsız olmalı; ayrım yalnız argümanda")
	}
	if !containsArg(argsA, "c-a") || !containsArg(argsB, "c-b") || containsArg(argsA, "c-b") {
		t.Fatalf("cluster argümanı karışmış: %v / %v", argsA, argsB)
	}
}

// v0.10.136 — adım 2 (servis detay): pod başına latency SQL sözleşmesi —
// ham spans yalnız servis + pencere + giriş-span sınırıyla, terfi k8s_pod
// kolonuyla gruplu, LIMIT + max_execution_time; cluster yan tümcesi yalnız
// değer verilince (boş = cluster'lar AYRI satır, birleşmez).
func TestPodLatencySQLShape(t *testing.T) {
	sql, n := podLatencySQL("")
	for _, want := range []string{"FROM spans", "service_name = ?", "time >= ? AND time <= ?", "k8s_pod != ''",
		"kind IN ('server', 'consumer')", "quantilesTDigest(0.5, 0.95, 0.99)", "GROUP BY cluster, k8s_namespace, k8s_pod",
		"LIMIT 200", "max_execution_time"} {
		if !strings.Contains(sql, want) {
			t.Fatalf("latency SQL %q içermeli:\n%s", want, sql)
		}
	}
	if n != 3 || strings.Contains(sql, "cluster = ?") {
		t.Fatalf("cluster'sız: 3 arg ve cluster yan tümcesi yok; n=%d", n)
	}
	sql2, n2 := podLatencySQL("prod-eu")
	if n2 != 4 || !strings.Contains(sql2, "AND cluster = ?") {
		t.Fatalf("cluster'lı: 4 arg + cluster = ?; n=%d", n2)
	}
}
