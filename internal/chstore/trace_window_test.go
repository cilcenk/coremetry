package chstore

// v0.9.1185 regresyon testleri — /traces'te SEÇİM ↔ TOPLAMA penceresi.
//
// v0.9.823→1175 kova-sınırı taraması 44 okumayı düzeltti ve traces
// ailesinin 10 sitesini GEREKÇELİ olarak dışarıda bıraktı: bir trace
// kovaları aşıyor, üst sınırı `<`e çekmek süresini/span sayısını DAHA çok
// budardı, `<=` o budamayı kazara bir kova kadar telafi ediyordu.
//
// Kazara telafi tasarım değildir. Bu dilim iki pencereyi ayırıyor:
//
//	SEÇİM   (hangi trace'ler pencerede?) → `< to`
//	TOPLAMA (süresi/span sayısı nedir?)  → `< to + slack`
//
// Testlerin ölçtüğü şey ikisinin AYRI ve DOĞRU İLİŞKİDE olması. Tek tek
// "sınır `<` mi" diye bakmak burada yetmez — ikisi de `<` ama farklı ANA
// bakıyorlar ve karıştırmak iki ayrı sessiz hata üretir: seçim tavanını
// kaydırmak pencerede başlamamış trace'leri listeler, toplama tavanını
// daraltmak süreleri budar.

import (
	"os"
	"strings"
	"testing"
	"time"
)

func TestAggWindowEnd(t *testing.T) {
	from := time.Date(2026, 8, 19, 10, 0, 0, 0, time.UTC)

	cases := []struct {
		name    string
		to      time.Time
		bounded bool
		want    time.Duration // to'dan İTİBAREN eklenen slack
	}{
		{
			// `trace_id IN (…)` var: slack yeni trace getirmez, yalnız
			// seçilmiş trace'lerin kuyruk kovalarını getirir. Maliyeti ~sıfır,
			// o yüzden tam slack.
			"kısıtlı sorgu — dar pencerede de TAM slack",
			from.Add(15 * time.Minute), true, traceAggSlack,
		},
		{"kısıtlı sorgu — geniş pencere", from.Add(24 * time.Hour), true, traceAggSlack},
		{
			// Kısıtsız: tüm pencere GROUP BY ediliyor, slack doğrudan taranan
			// kovaya biner. 15dk pencerede 1sa slack 5× tarama olurdu.
			"kısıtsız — dar pencere KELEPÇELİ",
			from.Add(15 * time.Minute), false, 15 * time.Minute,
		},
		{
			"kısıtsız — slack'ten geniş pencerede tam slack",
			from.Add(6 * time.Hour), false, traceAggSlack,
		},
		{
			"kısıtsız — tam slack genişliğinde pencere",
			from.Add(traceAggSlack), false, traceAggSlack,
		},
		// Dejenere pencereler: kelepçe negatif/sıfır genişlikte devreye
		// GİRMEZ (aksi hâlde tavan pencereden geriye düşer ve toplama
		// tamamen boşalırdı).
		{"kısıtsız — sıfır pencere", from, false, traceAggSlack},
		{"kısıtsız — ters pencere", from.Add(-time.Hour), false, traceAggSlack},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := aggWindowEnd(from, c.to, c.bounded)
			if want := c.to.Add(c.want); !got.Equal(want) {
				t.Errorf("aggWindowEnd = %v, beklenen %v (slack %v)",
					got.Format("15:04:05"), want.Format("15:04:05"), c.want)
			}
			// Değişmez: toplama tavanı seçim tavanından GERİ düşemez.
			if got.Before(c.to) {
				t.Errorf("toplama tavanı %v, seçim tavanı %v'nin GERİSİNDE — "+
					"süreler budanır", got, c.to)
			}
		})
	}
}

// TestSelectionBoundsAreExclusive — SEÇİM soran her okuma `< to` demeli.
//
// Sayım ile liste ayrıca BİRBİRİYLE eşleşmek zorunda: ayrışırlarsa
// sayfalama, listenin döndürmediği bir evrenin sayfa sayısını gösterir ve
// her iki sorgu tek başına doğru görünür (v0.9.1168'in yakaladığı sınıf).
func TestSelectionBoundsAreExclusive(t *testing.T) {
	base := time.Date(2026, 8, 19, 11, 0, 0, 0, time.UTC)
	f := TraceFilter{From: base.Add(-time.Hour), To: base, Service: "payments-api"}

	// Sayım yolu — saf planlayıcı.
	_, preds, _, reason := traceCountPlan(f)
	if reason != "" {
		t.Fatalf("sayım MV yolunu seçmedi (%s) — test kurgusu bozuk", reason)
	}
	joined := strings.Join(preds, " ")
	if !strings.Contains(joined, "time_bucket < ?") || strings.Contains(joined, "time_bucket <= ?") {
		t.Errorf("sayım seçim sınırı DIŞLAYICI olmalı: %v", preds)
	}

	// Liste stage-1 — saf builder.
	sql, ok := traceStage1LightSQL(f, nil)
	if !ok {
		t.Fatal("stage-1 light yolu kapalı — test kurgusu bozuk")
	}
	if !strings.Contains(sql, "time_bucket < ?") || strings.Contains(sql, "time_bucket <= ?") {
		t.Errorf("stage-1 seçim sınırı DIŞLAYICI olmalı:\n%s", sql)
	}
}

// TestAggregationCeilingExceedsSelection — asıl sözleşme: toplama penceresi
// seçim penceresinden GENİŞ. Eşit olsalar sınırı aşan trace'in kuyruğu
// düşerdi (bu dilimin varlık sebebi), dar olsa daha da beter.
func TestAggregationCeilingExceedsSelection(t *testing.T) {
	from := time.Date(2026, 8, 19, 10, 0, 0, 0, time.UTC)
	to := from.Add(2 * time.Hour) // slack'ten geniş: kelepçe devrede değil

	for _, bounded := range []bool{true, false} {
		aggTo := aggWindowEnd(from, to, bounded)
		if !aggTo.After(to) {
			t.Errorf("bounded=%v: toplama tavanı %v, seçim tavanı %v'yi AŞMIYOR — "+
				"sınırı aşan trace'in süresi budanır", bounded, aggTo, to)
		}
	}
}

// TestTraceFamilyHasNoInclusiveBucketBound — dosya-geneli kapı.
//
// Yorum satırları HARİÇ: trace_slice.go'nun başlığı v0.9.277'nin
// DÜZELTTİĞİ eski SQL'i tarihsel kayıt olarak taşıyor ve orada durmalı.
func TestTraceFamilyHasNoInclusiveBucketBound(t *testing.T) {
	for _, file := range []string{
		"repo.go", "trace_count.go", "trace_slice.go", "tracemetric.go",
	} {
		b, err := os.ReadFile(file)
		if err != nil {
			t.Fatalf("%s okunamadı: %v", file, err)
		}
		for i, ln := range strings.Split(string(b), "\n") {
			trimmed := strings.TrimSpace(ln)
			if strings.HasPrefix(trimmed, "//") {
				continue
			}
			if strings.Contains(ln, "time_bucket <= ?") {
				t.Errorf("%s:%d — `time_bucket <= ?`: seçim `< to`, toplama "+
					"`< aggWindowEnd(...)` olmalı (v0.9.1185)", file, i+1)
			}
		}
	}
}

// TestStage2UsesAggWindow / TestTraceAggregateUsesBothWindows — BAĞLANMA.
// aggWindowEnd'in var olması yetmez; toplama sorgularının onu GERÇEKTEN
// bağlaması gerek. Sabiti tanımlayıp bağlamayı unutmak, testleri yeşil
// bırakıp davranışı değiştirmeyen bir "düzeltme" olurdu.
func TestStage2UsesAggWindow(t *testing.T) {
	body := funcBody(t, "trace_slice.go", "func (s *Store) runTraceStage2(")
	if !strings.Contains(body, "aggWindowEnd(f.From, f.To,") {
		t.Error("runTraceStage2 aggWindowEnd bağlamıyor — stage 2 hâlâ seçim " +
			"tavanıyla topluyor olabilir")
	}
	if !strings.Contains(body, "args = append(args, from, aggTo,") {
		t.Error("stage 2 argümanı aggTo değil — hesaplanan tavan sorguya girmiyor")
	}
}

func TestTraceAggregateUsesBothWindows(t *testing.T) {
	body := funcBody(t, "repo.go", "func (s *Store) getTraceAggregateFromMV(")
	// TOPLAMA tarafı slack'li.
	if !strings.Contains(body, "aggWindowEnd(f.From, f.To, f.Service != \"\")") {
		t.Error("aggregate iç sorgusu aggWindowEnd bağlamıyor")
	}
	// SEÇİM tarafı (servis indeksi alt sorgusu) ham f.To ile kalmalı:
	// slack'i oraya da vermek pencerede başlamamış trace'leri seçerdi.
	if !strings.Contains(body, "innerArgs = append(innerArgs, f.Service, f.From, f.To)") {
		t.Error("servis-indeksi alt sorgusu ham f.To kullanmalı (SEÇİM penceresi)")
	}
}
