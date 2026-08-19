package api

// v0.9.1175 regresyon testi — ÜST KOVA SINIRI, /api/services spanmetrics
// bloğu. chstore'daki dokuz dalganın (v0.9.823 → 1174) api paketindeki son
// dört üyesi:
//
//	spanmetrics_calls_5m    → satır sayıları (Stage 1)
//	spanmetrics_calls_5m    → sparkline (Stage 2)
//	spanmetrics_duration_5m → avg/max (Stage 3)
//	spanmetrics_hist_5m     → p50/p99 (Stage 4)
//
// Sınıf: MV kovaları BAŞLANGIÇLARIYLA etiketli, dolayısıyla `time_bucket <=
// to` başlangıcı tam `to` olan kovayı da alır ve o kova [to, to+5dk)
// aralığını taşır — istenen pencereden SIFIR veri. Fark yalnız `to` kova
// sınırına tam otururken görünür, bu yüzden hatalı sınır çoğu pencerede
// doğru cevap verir ve on dalga boyunca saklanabildi.
//
// Bu dört okuma AYNI (bucketStart, to) çiftiyle koşar ve tek bir satırın
// dört sütununu doldurur: sayı, süre, yüzdelik, trend. Ayrışmaları "aynı
// satırda dört farklı pencere" demek olurdu — o yüzden kapı dosya-geneli
// değil, hepsinin BİRDEN aynı operatörü taşıdığını ölçüyor.
//
// Test kaynağa bakıyor: yüklemler SQL dizelerinin içinde yaşıyor,
// çalıştırılabilir bir Go ifadesinde değil (chstore'daki kardeşlerinin
// deseni — funcSource/funcBody, v0.9.823/1168).

import (
	"os"
	"regexp"
	"strings"
	"testing"
)

// spanmetricsMVs — bu bloğun okuduğu MV'ler. Yeni bir tanesi eklenirse
// listeye girmeli; girmezse aşağıdaki toplam-sayı kapısı zaten patlar.
var spanmetricsMVs = []string{
	"spanmetrics_calls_5m",
	"spanmetrics_duration_5m",
	"spanmetrics_hist_5m",
}

var apiBucketPredRe = regexp.MustCompile(`FROM\s+(spanmetrics_\w+)\s*\n\s*WHERE time_bucket >= \? AND time_bucket (<=?) \?`)

func apiSourceMustRead(t *testing.T) string {
	t.Helper()
	b, err := os.ReadFile("api.go")
	if err != nil {
		t.Fatalf("api.go okunamadı: %v", err)
	}
	return string(b)
}

// TestSpanmetricsReadsExcludeUpperBucket — dört okumanın da üst sınırı
// DIŞLAYICI olmalı, ve dördü de AYNI operatörü taşımalı.
func TestSpanmetricsReadsExcludeUpperBucket(t *testing.T) {
	ms := apiBucketPredRe.FindAllStringSubmatch(apiSourceMustRead(t), -1)
	if len(ms) != 4 {
		t.Fatalf("4 spanmetrics kova okuması bekleniyordu, %d bulundu — yeni bir "+
			"okuma eklendiyse sınırını bu teste de bağla", len(ms))
	}
	seen := map[string]bool{}
	for _, m := range ms {
		table, op := m[1], m[2]
		seen[table] = true
		if op != "<" {
			t.Errorf("%s: `time_bucket %s ?` — üst kova sınırı `< ?` olmalı; "+
				"`<= to` başlangıcı tam to olan kovayı alır ve o kova "+
				"[to, to+5dk) aralığını, yani pencereden SIFIR veri taşır "+
				"(v0.9.823→1175 sınıfı)", table, op)
		}
	}
	for _, mv := range spanmetricsMVs {
		if !seen[mv] {
			t.Errorf("%s okuması bulunamadı — sorgu taşındıysa testi güncelle", mv)
		}
	}
}

// TestSpanmetricsStagesShareOneWindow — dört aşama TEK satırın dört
// sütununu doldurur (sayı / trend / süre / yüzdelik). Sınırları ayrışırsa
// operatör aynı satırda dört farklı pencereye bakar ve hiçbir sütun
// diğerini yalanlamaz — hepsi tek başına makul görünür. Tekil sınır testi
// bu sınıfı yakalayamaz; ölçülmesi gereken EŞİTLİK.
func TestSpanmetricsStagesShareOneWindow(t *testing.T) {
	ms := apiBucketPredRe.FindAllStringSubmatch(apiSourceMustRead(t), -1)
	if len(ms) < 2 {
		t.Fatalf("en az 2 okuma bekleniyordu, %d bulundu", len(ms))
	}
	first := ms[0][2]
	for _, m := range ms[1:] {
		if m[2] != first {
			t.Fatalf("%s `%s` / %s `%s` — aşamaların sınırı AYRIŞMIŞ: aynı satırın "+
				"sayısı, süresi ve yüzdeliği farklı pencerelerden gelir",
				ms[0][1], first, m[1], m[2])
		}
	}
}

// TestSpanmetricsBlockHasNoInclusiveBucketBound — kaba kapı: bu blokta
// hiçbir spanmetrics okuması `<= ?` üst sınırı taşıyamaz. Yukarıdaki
// regex yapıya bağlı; bu sayım ise düz metin, yani sorgu biçimlenmesi
// değişse bile kaçağı yakalar.
func TestSpanmetricsBlockHasNoInclusiveBucketBound(t *testing.T) {
	src := apiSourceMustRead(t)
	for _, mv := range spanmetricsMVs {
		i := strings.Index(src, "FROM "+mv)
		for i >= 0 {
			// Okumanın WHERE'i FROM'dan hemen sonra gelir; 200 karakterlik
			// pencere hem tek hem çok satırlı biçimlenmeyi kapsar.
			end := i + 200
			if end > len(src) {
				end = len(src)
			}
			if strings.Contains(src[i:end], "time_bucket <= ?") {
				t.Errorf("%s okumasında `time_bucket <= ?` — üst kova sınırı `< ?` "+
					"olmalı (v0.9.823→1175 sınıfı)", mv)
			}
			next := strings.Index(src[i+1:], "FROM "+mv)
			if next < 0 {
				break
			}
			i = i + 1 + next
		}
	}
}
