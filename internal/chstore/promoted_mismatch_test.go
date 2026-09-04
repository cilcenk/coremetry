package chstore

import (
	"strings"
	"testing"
)

// promoted_mismatch_test.go — v0.10.339. Operator-reported (prod): filtre
// terfi kolonuna derleniyor, kolon o replikada değeri taşımıyor → boş
// liste. Çivilenen: NoPromoted dizi yoluna derler; terfi anahtarı tespiti;
// host-bazlı uyuşmazlık kararı; askıya alma haritayı yeniden yayınlar.

func TestNoPromotedCompilesToArrayPath(t *testing.T) {
	withPromoted(t, "channel_code", "attr_channel_code")
	f := TraceFilter{Filters: []FilterExpr{{Key: "channel_code", Op: "=", Values: []string{"060203"}}}}
	wcCol := buildGetTracesWhere(f, "")
	col := wcCol.sql()
	if !strings.Contains(col, "attr_channel_code = ?") || strings.Contains(col, "indexOf(attr_keys") {
		t.Fatalf("varsayılan yol terfi kolonu olmalı: %s", col)
	}
	f.NoPromoted = true
	wcArr := buildGetTracesWhere(f, "")
	arr := wcArr.sql()
	if strings.Contains(arr, "attr_channel_code") || !strings.Contains(arr, "indexOf(attr_keys") {
		t.Fatalf("NoPromoted dizi yoluna derlenmeli: %s", arr)
	}
	// Grup kökü de aynı anahtarı dinler.
	g := TraceFilter{FilterRoot: &FilterGroup{Join: "OR", Filters: []FilterExpr{
		{Key: "channel_code", Op: "=", Values: []string{"a"}}, {Key: "function_code", Op: "=", Values: []string{"b"}}}}}
	wg := buildGetTracesWhere(g, "")
	if s := wg.sql(); !strings.Contains(s, "attr_channel_code") {
		t.Fatalf("grup varsayılanı terfi: %s", s)
	}
	g.NoPromoted = true
	wg2 := buildGetTracesWhere(g, "")
	if s := wg2.sql(); strings.Contains(s, "attr_channel_code") {
		t.Fatalf("grup NoPromoted dizi: %s", s)
	}
}

func TestPromotedFilterKeys(t *testing.T) {
	withPromoted(t, "channel_code", "attr_channel_code")
	f := TraceFilter{Filters: []FilterExpr{
		{Key: "channel_code", Op: "=", Values: []string{"x"}},
		{Key: "span.channel_code", Op: "=", Values: []string{"y"}}, // aynı anahtar, önekli
		{Key: "http.method", Op: "=", Values: []string{"POST"}},    // terfi değil
	}}
	got := PromotedFilterKeys(f)
	if len(got) != 1 || got[0] != "channel_code" {
		t.Fatalf("terfi anahtarları: %v", got)
	}
	if got := PromotedFilterKeys(TraceFilter{Search: "x"}); len(got) != 0 {
		t.Fatalf("filtresiz istekte boş: %v", got)
	}
	g := TraceFilter{FilterRoot: &FilterGroup{Join: "OR", Groups: []FilterGroup{{Join: "AND", Filters: []FilterExpr{{Key: "channel_code", Op: "=", Values: []string{"z"}}}}}}}
	if got := PromotedFilterKeys(g); len(got) != 1 {
		t.Fatalf("alt grup yaprağı görülmeli: %v", got)
	}
}

func TestPromotedMismatchDetectedAndSQL(t *testing.T) {
	if PromotedMismatchDetected(nil) || PromotedMismatchDetected([]PromotedHostCount{{Host: "a", Col: 5, Arr: 5}}) {
		t.Fatal("eşit sayım uyuşmazlık değil")
	}
	if !PromotedMismatchDetected([]PromotedHostCount{{Host: "a", Col: 0, Arr: 0}, {Host: "b", Col: 0, Arr: 812}}) {
		t.Fatal("bir host'ta dizi > kolon → uyuşmazlık")
	}
	sql := promotedMismatchSQL("WHERE time >= ? AND service_name = ?")
	for _, w := range []string{"hostName() AS h", "count() FROM spans WHERE", "GROUP BY h", "max_execution_time = 10"} {
		if !strings.Contains(sql, w) {
			t.Fatalf("%q yok: %s", w, sql)
		}
	}
}

func TestSuspendPromotedKeysRepublishesMap(t *testing.T) {
	prev := promotedColsPtr.Load()
	registerTraceAttrMaterialized(map[string]string{
		"channel_code": "attr_channel_code", "span.channel_code": "attr_channel_code",
		"function_code": "attr_function_code",
		"k8s.pod.name":  "k8s_pod", "resource.k8s.pod.name": "k8s_pod",
	})
	t.Cleanup(func() { promotedColsPtr.Store(prev) })
	unregisterTraceAttrMaterialized([]string{"channel_code", "k8s.pod.name"})
	m := promotedCols()
	for _, gone := range []string{"channel_code", "span.channel_code", "k8s.pod.name", "resource.k8s.pod.name"} {
		if _, ok := m[gone]; ok {
			t.Fatalf("%q askıya alınmalıydı: %v", gone, m)
		}
	}
	if m["function_code"] != "attr_function_code" {
		t.Fatalf("dokunulmayan anahtar kalmalı: %v", m)
	}
	// Filtre artık dizi yolunda — düşüşün kalıcı yarısı.
	f := FilterExpr{Key: "channel_code", Op: "=", Values: []string{"060203"}}
	if sql, _, _ := f.SQL(); strings.Contains(sql, "attr_channel_code") {
		t.Fatalf("askı sonrası dizi yolu bekleniyordu: %s", sql)
	}
	unregisterTraceAttrMaterialized(nil) // no-op
}
