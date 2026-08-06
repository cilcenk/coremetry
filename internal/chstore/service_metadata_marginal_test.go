package chstore

import "testing"

// v0.9.715 — birleşik türetmenin SAF marjinal çekirdeği.
//
// Rapor kısıtları (perf taraması #5, bağlayıcı):
//   - mod EXACT kalmalı, topK'ya sessiz düşüş YOK → marjinal, kombo
//     sayımlarının (service, değer) başına TOPLANMASIYLA hesaplanır;
//     kombo üzerinden argMax almak marjinali bozar (ilk vaka tam bunu
//     sınar).
//   - flapping azaltımı: eşit sayımda sözlükçe küçük değer — tikler
//     arası deterministik.

func TestMarginalModes(t *testing.T) {
	t.Run("kombo-argMax tuzağı: marjinal kazanır", func(t *testing.T) {
		// ns=A iki komboya bölünmüş (3+3=6), ns=B tek komboda 4.
		// Kombo argMax'ı B derdi (4 > 3); EXACT marjinal A der (6 > 4).
		rows := []metaComboRow{
			{Svc: "s", NS: "A", Dep: "d1", C: 3},
			{Svc: "s", NS: "A", Dep: "d2", C: 3},
			{Svc: "s", NS: "B", Dep: "d1", C: 4},
		}
		_, ns, _ := marginalModes(rows)
		if ns["s"] != "A" {
			t.Fatalf("ns = %q — kombo argMax tuzağına düşülmüş (EXACT marjinal A demeli)", ns["s"])
		}
	})

	t.Run("boş değer marjinale girmez", func(t *testing.T) {
		rows := []metaComboRow{
			{Svc: "s", NS: "", C: 100}, // ns yaymayan span'ler
			{Svc: "s", NS: "prod", C: 2},
		}
		_, ns, _ := marginalModes(rows)
		if ns["s"] != "prod" {
			t.Fatalf("ns = %q — boş değer 100 sayımla kazanmış", ns["s"])
		}
	})

	t.Run("eşitlikte sözlükçe küçük — tikler arası kararlı (flapping)", func(t *testing.T) {
		rows := []metaComboRow{
			{Svc: "s", Dep: "b-dep", C: 5},
			{Svc: "s", Dep: "a-dep", C: 5},
		}
		_, _, dep := marginalModes(rows)
		if dep["s"] != "a-dep" {
			t.Fatalf("dep = %q — eşitlik kırıcı deterministik değil", dep["s"])
		}
	})

	t.Run("teams: iki bağımsız marjinal, kısmi boşluk korunur", func(t *testing.T) {
		rows := []metaComboRow{
			{Svc: "s", UG: "owners", SY: "", C: 7},
			{Svc: "s", UG: "", SY: "sre-1", C: 2},
		}
		teams, _, _ := marginalModes(rows)
		if teams["s"].OwnerTeam != "owners" || teams["s"].SRETeam != "sre-1" {
			t.Fatalf("teams = %+v", teams["s"])
		}
	})

	t.Run("hiç değer yaymayan servis üç haritada da YOK", func(t *testing.T) {
		teams, ns, dep := marginalModes([]metaComboRow{{Svc: "s", C: 9}})
		if len(teams)+len(ns)+len(dep) != 0 {
			t.Fatalf("boş kombo değer üretti: %v %v %v", teams, ns, dep)
		}
	})

	t.Run("trim: boşluklu değer kırpılır ve birleşir", func(t *testing.T) {
		rows := []metaComboRow{
			{Svc: "s", NS: " prod ", C: 2},
			{Svc: "s", NS: "prod", C: 2},
			{Svc: "s", NS: "stage", C: 3},
		}
		_, ns, _ := marginalModes(rows)
		if ns["s"] != "prod" { // 2+2=4 > 3
			t.Fatalf("ns = %q — trim birleşimi çalışmıyor", ns["s"])
		}
	})
}

// SQL sözleşme pinleri: multiIf anahtar SIRASI (resource önce, semconv
// önce) eski üç sorguyla birebir — sıra değişirse tercih semantiği
// değişir ve bu sessiz olur.
func TestDeriveAllSQLKeyOrder(t *testing.T) {
	sql := deriveMetadataAllSQL
	ordered := [][2]string{
		{"has(res_keys, 'ug-team')", "has(attr_keys, 'ug-team')"},
		{"has(res_keys, 'service.namespace')", "has(res_keys, 'k8s.namespace.name')"},
		{"has(res_keys, 'k8s.namespace.name')", "has(attr_keys, 'service.namespace')"},
		{"has(res_keys, 'k8s.deployment.name')", "has(attr_keys, 'k8s.deployment.name')"},
	}
	for _, pair := range ordered {
		i, j := indexOfStr(sql, pair[0]), indexOfStr(sql, pair[1])
		if i < 0 || j < 0 || i > j {
			t.Errorf("anahtar sırası bozulmuş: %q, %q (i=%d j=%d)", pair[0], pair[1], i, j)
		}
	}
	for _, want := range []string{"LIMIT 2000000", "max_execution_time = 25", "GROUP BY service_name, ug_val, sy_val, ns_val, dep_val"} {
		if indexOfStr(sql, want) < 0 {
			t.Errorf("SQL'de %q yok", want)
		}
	}
}

func indexOfStr(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}
