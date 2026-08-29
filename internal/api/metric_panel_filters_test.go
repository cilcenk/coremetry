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
	// v0.10.146 — handler api.go'dan dashboards_data.go'ya taşındı ve dal
	// bundleSlot'a çıktı; çapa yine GÜNCELLENDİ, test silinmedi. İki
	// düzeltme daha: (a) yorumlar soyulur — eski pencere "req.Filters"ı bir
	// YORUMDAN buluyordu (gate kendi metnini ısırıyordu), (b) pencere
	// `case "metric":` ile `case "spanMetric":` arası — parseFilters çağrısı
	// da içinde kalır.
	b, err := os.ReadFile("dashboards_data.go")
	if err != nil {
		t.Fatalf("dashboards_data.go okunamadı: %v", err)
	}
	src := stripGoComments(string(b))

	const anchor = `case "metric":`
	i := strings.Index(src, anchor)
	if i < 0 {
		t.Fatalf("dashboard bundle metric dalı bulunamadı (çapa: %q) — dal yeniden "+
			"yazıldıysa çapayı güncelle, testi silme", anchor)
	}
	body := src[i:]
	if end := strings.Index(body, `case "spanMetric":`); end > 0 {
		body = body[:end]
	}
	if !strings.Contains(body, "queryMetricNoted(ctx, metricSrc, chstore.MetricQueryFilter{") {
		t.Fatalf("metric dalı queryMetricNoted seam'inden geçmiyor — pencere:\n%s", body)
	}
	if !strings.Contains(body, "parseFilters(string(req.Filters))") {
		t.Error("Filters gövdedeki req.Filters'tan derlenmiyor — sabit ya da boş " +
			"bir değer geçiriliyor olabilir")
	}
	if !strings.Contains(body, "Filters:     mfilters,") && !strings.Contains(body, "Filters: mfilters,") {
		t.Error("metric dalı derlenen filtreleri MetricQueryFilter.Filters'a geçirmiyor — " +
			"panel filtresi SQL'e inmez ve panel FİLTRESİZ veriyi filtreliymiş gibi " +
			"çizer (boş panel değil, sessizce yanlış sayı)")
	}
}
