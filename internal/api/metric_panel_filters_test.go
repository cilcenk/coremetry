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
	//
	// v0.9.1150 — çapa `s.store.QueryMetric` idi; okuma metrik kaynağı
	// SEAM'ine taşındığı (metricsource.go, CH ya da VictoriaMetrics) için
	// `metricSrc.QueryMetric` olmuştu. v0.9.1157'da bir daha kaydı:
	// çağrı `queryMetricNoted(...)` yardımcısına geçti (VM yolunda boş bir
	// yüzdeliğin SEBEBİNİ de taşıyor).
	//
	// Kapının SÖZLEŞMESİ iki kez de değişmedi — dal hâlâ req.Filters'ı
	// geçirmek zorunda — değişen çapa. Sayıyı düşürmek ya da testi silmek
	// yerine çapa GÜNCELLENDİ: bayat bir çapa t.Fatal ile bağırıyor, ki
	// doğru davranış bu (sessizce sıfır dosya tarayıp yeşil kalmak
	// korumanın görüntüsü olurdu).
	const anchor = "queryMetricNoted(r.Context(), metricSrc, chstore.MetricQueryFilter{"
	i := strings.Index(src, anchor)
	if i < 0 {
		t.Fatalf("dashboard bundle metric dalı bulunamadı (çapa: %q) — dal yeniden "+
			"yazıldıysa çapayı güncelle, testi silme", anchor)
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
