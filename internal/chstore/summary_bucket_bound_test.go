// v0.9.1168 regresyon testleri — ÜST KOVA SINIRI, /api/services ailesi.
//
// Sınıfın dördüncü dalgası (v0.9.823 /databases · v0.9.1156 dependencies +
// db_trends · v0.9.1167 dbstmt/slow-queries). Bu dilim service_summary_5m'in
// dört okumasını kapatıyor:
//
//	CountServicesAgg          → /api/services SAYIM (sayfalama)
//	GetServicesAggFiltered2   → /api/services LİSTE
//	GetServiceSummary5mFor    → çok-servis sparkline
//	GetServiceSummary5m       → tek-servis grafik
//
// Neden `<`, `<=` değil — ve neden bu, v0.9.555'in "fazla göstermek daha
// iyi" takasıyla ÇELİŞMİYOR (alignBucketStart'ın doc bloğunda uzun hâli):
// alt sınırda from'u İÇEREN kova pencere verisi taşır, almamak kayıptır.
// Üst sınırda `to` ETİKETLİ kova [to, to+5dk) aralığını taşır, yani
// pencereden SIFIR veri — almak sadece yabancı trafik ekler.
//
// Bu dosyanın kendine ait katkısı SAYIM↔LİSTE eşleşmesi: ikisi aynı evreni
// saymak zorunda. Sınır birinde değişip diğerinde kalırsa sayfalama, listenin
// asla döndürmediği bir evrenin sayfa sayısını gösterir — ve bu, dört
// dalgada da hiç ölçülmemiş bir kırılganlıktı.
//
// admitsBucket db_bucket_bound_test.go'dan (v0.9.823), bucketUpperOp
// dbstmt_bucket_bound_test.go'dan (v0.9.1167) — üç dalga aynı doğruluk
// tanımını paylaşır.
package chstore

import (
	"os"
	"strings"
	"testing"
	"time"
)

// funcBody — bir dosyadaki tek fonksiyonun gövdesi, KAPANIŞ SÜSLÜSÜNE dek.
//
// funcSource (v0.9.823) bir sonraki `\nfunc `e kadar keser, yani araya giren
// doc yorumlarını da içine alır. Burada bu yetmiyor: alignBucketStart'ın doc
// bloğu bilerek `<` ve `<=` operatörlerini TARTIŞIYOR ve GetServiceSummary5m
// tam onun üstünde duruyor. Sözleşmeyi anlatan yorumun, sözleşmeyi ölçen
// testi bozması saçma bir kırılganlık olurdu.
func funcBody(t *testing.T, file, sig string) string {
	t.Helper()
	b, err := os.ReadFile(file)
	if err != nil {
		t.Fatalf("%s okunamadı: %v", file, err)
	}
	src := string(b)
	i := strings.Index(src, sig)
	if i < 0 {
		t.Fatalf("%s içinde %q bulunamadı — imza değiştiyse testi güncelle", file, sig)
	}
	rest := src[i+len(sig):]
	// Üst düzey kapanış süslüsü: gofmt'li Go'da sütun 0'da "}" + satır sonu.
	j := strings.Index(rest, "\n}\n")
	if j < 0 {
		t.Fatalf("%s içinde %q gövdesinin kapanışı bulunamadı", file, sig)
	}
	return rest[:j]
}

// servicesBucketReads — dört okuma, kaynaktan. Hiçbiri saf builder değil
// (yüklem metot gövdesindeki SQL dizesinde yaşıyor), bu yüzden kaynak
// dilimi — TestSummaryReadersAlign (v0.9.555) ve v0.9.823'ün deseni.
func servicesBucketReads(t *testing.T) []struct {
	name string
	sql  string
} {
	t.Helper()
	return []struct {
		name string
		sql  string
	}{
		{"CountServicesAgg (sayım)",
			funcBody(t, "summary.go", "func (s *Store) CountServicesAgg(")},
		{"GetServicesAggFiltered2 (liste)",
			funcBody(t, "summary.go", "func (s *Store) GetServicesAggFiltered2(")},
		{"GetServiceSummary5mFor (çok-servis sparkline)",
			funcBody(t, "summary.go", "func (s *Store) GetServiceSummary5mFor(")},
		// v0.10.269 — SQL saf kurucuda (serviceSummarySlotsSQL), hizalama
		// yöntemde: iki gövde birlikte sözleşmeyi taşır.
		{"GetServiceSummarySlots (v0.10.269 sparkline slot)",
			funcBody(t, "summary.go", "func (s *Store) GetServiceSummarySlots(") +
				funcBody(t, "summary.go", "func serviceSummarySlotsSQL(")},
		{"GetServiceSummary5m (tek-servis grafik)",
			funcBody(t, "summary.go", "func (s *Store) GetServiceSummary5m(")},
	}
}

// TestServicesReadsExcludeUpperBucket — DAVRANIŞ testi, 823'ün tablosu.
func TestServicesReadsExcludeUpperBucket(t *testing.T) {
	from := time.Date(2026, 8, 19, 10, 0, 0, 0, time.UTC)
	to := time.Date(2026, 8, 19, 11, 0, 0, 0, time.UTC)

	cases := []struct {
		name   string
		bucket time.Time
		want   bool
	}{
		{"pencereden ÖNCEKİ kova dışarıda", from.Add(-5 * time.Minute), false},
		// alignBucketStart from'u kova başına indirdiği için bu kova İÇERİDE.
		{"tam from'daki kova içeride", from, true},
		{"ortadaki kova içeride", from.Add(30 * time.Minute), true},
		{"son tam kova içeride", to.Add(-5 * time.Minute), true},
		// [11:00, 11:05) — pencereden sıfır veri taşır. BUG BUYDU.
		{"tam to'daki kova DIŞARIDA", to, false},
		{"to'dan sonraki kova dışarıda", to.Add(5 * time.Minute), false},
	}

	for _, r := range servicesBucketReads(t) {
		op := bucketUpperOp(t, r.name, r.sql)
		for _, c := range cases {
			t.Run(r.name+"/"+c.name, func(t *testing.T) {
				if got := admitsBucket(op, c.bucket, from, to); got != c.want {
					t.Errorf("%s: `time_bucket %s ?` ile %v kovası alındı=%v, beklenen %v — "+
						"kova [%v, +5dk) aralığını kapsıyor",
						r.name, op, c.bucket.Format("15:04"), got, c.want,
						c.bucket.Format("15:04"))
				}
			})
		}
	}
}

// TestServicesCountMatchesListBound — YENİ sınıf: SAYIM ile LİSTE aynı
// evreni ölçmek zorunda.
//
// /api/services sayfalamayı CountServicesAgg'ın sayısı üzerine kuruyor,
// satırları GetServicesAggFiltered2'den alıyor. Sınırlar ayrışırsa sayı bir
// pencereyi, satırlar başka bir pencereyi anlatır: son sayfa boş çıkar ya
// da "N servis" başlığı listede hiç görünmeyen bir servisi sayar. İkisi de
// tek başına "doğru" görünür — hata YALNIZ ikisi kıyaslanınca ortaya çıkar,
// bu yüzden ayrı ayrı sınır testleri bu sınıfı yakalayamaz.
func TestServicesCountMatchesListBound(t *testing.T) {
	reads := servicesBucketReads(t)
	countOp := bucketUpperOp(t, reads[0].name, reads[0].sql)
	listOp := bucketUpperOp(t, reads[1].name, reads[1].sql)
	if countOp != listOp {
		t.Fatalf("sayım `%s` / liste `%s` — üst sınırlar AYRIŞMIŞ; sayfalama "+
			"listenin döndürmediği bir evreni sayar", countOp, listOp)
	}
	// Ve grafik okumaları da aynı pencereyi anlatmalı: satırdaki p99 ile
	// yanındaki sparkline'ın son noktası aynı kovadan gelir.
	for _, r := range reads[2:] {
		if op := bucketUpperOp(t, r.name, r.sql); op != listOp {
			t.Errorf("%s `%s` — liste `%s` ile aynı olmalı; satır ile grafik "+
				"farklı pencereyi anlatır", r.name, op, listOp)
		}
	}
}

// TestNoInclusiveUpperBucketBoundInSummary — DOSYA-GENELİ kapı, 1156/1167
// kardeşleri. summary.go'ya yeni bir `<= ?` kova sorgusu giremez.
func TestNoInclusiveUpperBucketBoundInSummary(t *testing.T) {
	b, err := os.ReadFile("summary.go")
	if err != nil {
		t.Fatalf("summary.go okunamadı: %v", err)
	}
	if n := strings.Count(string(b), "time_bucket <= ?"); n > 0 {
		t.Errorf("summary.go: %d adet `time_bucket <= ?` — üst kova sınırı `< ?` "+
			"olmalı (v0.9.823/1156/1167/1168 sınıfı; tam to'daki kova "+
			"pencerenin dışındadır)", n)
	}
}

// TestServicesReadsAlignLowerBound — v0.9.555 sözleşmesi hâlâ ayakta.
// Üst sınırı düzeltirken alt sınırı kaybetmek, düzelttiğimizden DAHA kötü
// bir bug olurdu: `>= from` (hizasız) olayın ilk dakikalarını gizler ve
// yüzey tam patlama anında "anomali yok" der.
func TestServicesReadsAlignLowerBound(t *testing.T) {
	for _, r := range servicesBucketReads(t) {
		if !strings.Contains(r.sql, "alignBucketStart(from)") {
			t.Errorf("%s: from alignBucketStart'tan geçmiyor — baştaki kısmi kova "+
				"elenir (v0.9.555)", r.name)
		}
		if !strings.Contains(r.sql, "time_bucket >= ?") {
			t.Errorf("%s: alt sınır `>= ?` yok", r.name)
		}
	}
}
