package chstore

import (
	"os"
	"strings"
	"testing"
)

// v0.9.694 — VARLIK SORUSU KATALOGDAN.
//
// v0.9.693'te receiver ÖNEK kapısını metric_catalog'a aldım ama TAM AD
// yolunu (MetricExists) atladım. Deploy sonrası ölçüm yakaladı: db-önekli
// varlık probları 15 dakikada 26 çağrı / 218 MiB + dağıtık alt-sorgu 45
// çağrı / 82 MiB ≈ 300 MiB. Aynı soru katalogda 50 çağrı / 198 KiB.
//
// Eski yorum "indexed point-existence probe" diyordu. DEĞİL:
// metric_points sıralama anahtarı (service_name, metric, time) ve burada
// service_name filtresi yok → `metric` ikinci kolon, budama yok.
// `LIMIT 1` de count()'u kesmiyor.
//
// Üç çağıranı var (evaluator db_capacity, runtime_pods, ve bugün
// yazdığım metrik-adı çözümlemesi — istek başına 5 aday).

func capSrc(t *testing.T) string {
	t.Helper()
	b, err := os.ReadFile("db_capacity.go")
	if err != nil {
		t.Fatal(err)
	}
	return stripGoLineComments(string(b))
}

func fnBody(t *testing.T, src, name string, n int) string {
	t.Helper()
	i := strings.Index(src, name)
	if i < 0 {
		t.Fatalf("%s bulunamadı", name)
	}
	if end := i + n; end < len(src) {
		return src[i:end]
	}
	return src[i:]
}

// ASIL KAPI: katalog yolu VAR ve önce deneniyor.
func TestMetricExistsPrefersCatalog(t *testing.T) {
	fn := fnBody(t, capSrc(t), "func (s *Store) MetricExists", 1600)
	if !strings.Contains(fn, "metric_catalog") {
		t.Error("MetricExists kataloğu kullanmıyor — metric_points'te budamasız count() " +
			"(ölçüm: 15 dk'da ~300 MiB)")
	}
	iCat := strings.Index(fn, "metric_catalog")
	iRaw := strings.Index(fn, "FROM metric_points")
	if iRaw >= 0 && iCat > iRaw {
		t.Error("ham metric_points yolu katalogdan ÖNCE geliyor — ucuz yol önce denenmeli")
	}
}

// TAZELİK ŞART: katalogda TTL yok. Olmadan, bir kez görülen metrik
// sonsuza kadar "var" görünür ve kapı anlamsızlaşır — bugün üç kez
// görülen "kapı var, hiçbir şey kapatmıyor" sınıfı.
func TestMetricExistsCatalogChecksFreshness(t *testing.T) {
	fn := fnBody(t, capSrc(t), "func (s *Store) MetricExists", 1600)
	if !strings.Contains(fn, "maxMerge(last_seen_state)") {
		t.Error("katalog yolu tazelik kontrol etmiyor — TTL'siz katalogda metrik sonsuza kadar 'var' kalır")
	}
}

// KATALOG BOŞ / OKUNAMIYORSA HAM YOLA DÜŞÜLMELİ. Varlık sorusuna
// "hayır" demek özelliği sessizce kapatır; taze kurulumda MV henüz
// dolmamış olabilir.
func TestMetricExistsFallsBackToRawScan(t *testing.T) {
	fn := fnBody(t, capSrc(t), "func (s *Store) MetricExists", 1600)
	if !strings.Contains(fn, "metricCatalogHasRows(ctx)") {
		t.Error("katalog doluluk kontrolü yok — taze kurulumda varlık sorusu yanlış 'hayır' döner")
	}
	if !strings.Contains(fn, "FROM metric_points") {
		t.Error("ham yol tamamen silinmiş — katalog boşken düşecek yer kalmıyor")
	}
}
