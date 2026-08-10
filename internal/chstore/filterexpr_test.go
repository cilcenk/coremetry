package chstore

import (
	"regexp"
	"strings"
	"testing"
)

// v0.9.532 — metricClusterExpr, spans tarafındaki clusterDeriveExpr'in
// metric_points ikizi. İki ifade AYNI attr anahtar kümesini AYNI sırayla
// taramalı — ayrışırlarsa aynı pod spans yüzeyinde bir cluster'da,
// metrics yüzeyinde başka/boş cluster'da görünür (sessiz tutarsızlık).
// v0.9.52 dersi de pinli: yalnız openshift.cluster.name basan cluster
// var; tek-anahtar bir ifade o cluster'ın pod'larını öneksiz bırakır.
func TestMetricClusterExprMirrorsClusterDeriveExpr(t *testing.T) {
	keyRe := regexp.MustCompile(`indexOf\((res_keys|attr_keys), '([^']+)'\)`)
	extract := func(expr string) []string {
		var out []string
		for _, m := range keyRe.FindAllStringSubmatch(expr, -1) {
			out = append(out, m[1]+":"+m[2])
		}
		return out
	}
	spansKeys := extract(clusterDeriveExpr)
	metricKeys := extract(metricClusterExpr)
	if len(spansKeys) == 0 {
		t.Fatal("clusterDeriveExpr'den anahtar çıkarılamadı — regex bozuk")
	}
	if len(spansKeys) != len(metricKeys) {
		t.Fatalf("anahtar sayıları ayrıştı: spans %d, metrics %d", len(spansKeys), len(metricKeys))
	}
	for i := range spansKeys {
		if spansKeys[i] != metricKeys[i] {
			t.Errorf("anahtar %d ayrıştı: spans %q, metrics %q — SIRA da sözleşme", i, spansKeys[i], metricKeys[i])
		}
	}
	// "cluster" well-known anahtarı bu ifadeye çözümlenmeli.
	if metricPointsWellKnown["cluster"] != metricClusterExpr {
		t.Error(`metricPointsWellKnown["cluster"] metricClusterExpr olmalı`)
	}
}

// v0.9.942 (UX denetimi B2) — `cluster` çipi SPANS tarafında da derive
// ifadesine çözümlenmeli.
//
// Orijinal belirti: /endpoints'te bir cluster seçip "Explore →" ile
// pivotlayan operatör TÜM cluster'ların verisini görüyordu. Kök neden
// tam olarak burada: `cluster` spans wellKnown haritasında YOKTU, yani
// çip `attr_values[indexOf(attr_keys, 'cluster')]` dizi aramasına
// düşüyordu — k8s.cluster.name / openshift.cluster.name basan (yani
// çoğu) kurulumda HİÇBİR satır eşleşmez. metric_points ikizi v0.9.532'de
// düzeltilmişti; spans yarısı geride kalmıştı.
func TestSpansClusterFilterUsesDerive(t *testing.T) {
	if wellKnown["cluster"] != clusterDeriveExpr {
		t.Fatal(`wellKnown["cluster"] clusterDeriveExpr olmalı — çip dizi aramasına düşerse cluster filtresi sessizce boş küme döndürür`)
	}
	sql, args, err := (FilterExpr{Key: "cluster", Op: "=", Values: []string{"ocp-a"}}).SQL()
	if err != nil {
		t.Fatalf("SQL(): %v", err)
	}
	// Dizi aramasına DÜŞMEMELİ: anahtar bir bind arg olarak geçmez.
	if len(args) != 1 || args[0] != "ocp-a" {
		t.Fatalf("yalnız DEĞER bind edilmeli, anahtar değil; got %v", args)
	}
	for _, want := range []string{"k8s.cluster.name", "openshift.cluster.name", "res_keys", "attr_keys"} {
		if !strings.Contains(sql, want) {
			t.Errorf("SQL %q içermiyor:\n%s", want, sql)
		}
	}
	// Terfi edilmiş `cluster` KOLONUNA referans YOK: FilterExpr.SQL() saf
	// ve Store'un hasClusterCol probunu göremez. Kolonu olmayan harici
	// Distributed kurulumda çıplak kolon adı code 47 verirdi (v0.8.162).
	if regexp.MustCompile(`(^|[^.\w'])cluster\s*=`).MatchString(sql) {
		t.Errorf("çıplak `cluster` kolonuna referans var — harici Distributed'da code 47:\n%s", sql)
	}
}

// Mevcut davranışın ÜST KÜMESİ olduğunun kanıtı: eski dizi-arama yolu
// (attr_keys 'cluster') derive'in altı bacağından biri olarak duruyor,
// yani bugüne dek eşleşen hiçbir span kaybolmuyor.
func TestSpansClusterDeriveIsSupersetOfLegacyAttrLookup(t *testing.T) {
	if !strings.Contains(clusterDeriveExpr, "attr_values[indexOf(attr_keys, 'cluster')]") {
		t.Error("derive eski dizi-arama bacağını KORUMALI — yoksa mevcut eşleşmeler kaybolur")
	}
}
