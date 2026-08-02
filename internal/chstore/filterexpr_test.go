package chstore

import (
	"regexp"
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
