// v0.9.566 regresyon testi — metrik panelinde filtreler SQL'e inmeli.
//
// Bug: dashboard'un toplu (bundle) yolu, MetricQueryFilter'ı kurarken
// Filters alanını HİÇ doldurmuyordu. İstemci filtreyi gönderiyor,
// gövdede duruyor, ama sorguya girmiyordu.
//
// Sonuç boş panel DEĞİL — sessizce YANLIŞ SAYI. Bir
// jvm.memory.type="heap" filtresi uygulanmayınca panel heap + non-heap
// (Metaspace, CodeCache, Compressed Class Space) toplamını "heap" diye
// çiziyordu. Yanlış ama makul görünen bir sayı, boş panelden
// TEHLİKELİDİR: kimse sorgulamaz.
//
// Kardeş handler (/api/metrics/query) filtreyi zaten geçiriyordu; bu
// dal ondan ayrışmıştı — aynı sınıf ayrışma bu oturumda dört kez çıktı.
package api

import (
	"os"
	"strings"
	"testing"
)

func TestDashboardBundleMetricBranchPassesFilters(t *testing.T) {
	b, err := os.ReadFile("api.go")
	if err != nil {
		t.Fatalf("api.go okunamadı: %v", err)
	}
	src := string(b)

	// Toplu yolun metric dalını bul: QueryMetric + MetricQueryFilter.
	i := strings.Index(src, "s.store.QueryMetric(r.Context(), chstore.MetricQueryFilter{")
	if i < 0 {
		t.Fatal("dashboard bundle metric dalı bulunamadı")
	}
	body := src[i:]
	if end := strings.Index(body, "})"); end > 0 {
		body = body[:end]
	}

	if !strings.Contains(body, "Filters:") {
		t.Error("metric dalı Filters geçirmiyor — panel filtresi SQL'e inmez ve " +
			"panel FİLTRESİZ veriyi filtreliymiş gibi çizer (boş panel değil, " +
			"sessizce yanlış sayı)")
	}
	if !strings.Contains(body, "req.Filters") {
		t.Error("Filters gövdedeki req.Filters'tan gelmiyor — sabit ya da boş " +
			"bir değer geçiriliyor olabilir")
	}
}
