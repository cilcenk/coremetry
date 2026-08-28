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
