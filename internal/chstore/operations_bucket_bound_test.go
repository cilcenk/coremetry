// v0.9.1169 regresyon testleri — ÜST KOVA SINIRI, /service operations.
//
// Sınıfın beşinci dalgası (823 /databases · 1156 dependencies+db_trends ·
// 1167 dbstmt/slow-queries · 1168 /api/services). queryOperationsFromMV'in
// İKİ okuması: birinci geçiş satır toplamı (operation_summary_5m ya da
// normalized modda operation_group_summary_5m), ikinci geçiş aynı pencere
// üzerinden sparkline.
//
// Bu grubun kendine ait katkısı IZGARA TUTARLILIĞI. `<= winEnd` fazladan
// aldığı kovayı birinci geçişin toplamına KATIYOR, ama ikinci geçişte o
// kovanın slot indeksi
//
//	bidx = intDiv(winEnd - bucketStart, bucketSec) = nBuckets
//
// çıkıyor ve repo.go'daki `int(bidx) < nBuckets` kapısına takılıp sessizce
// düşüyordu. Sonuç: satırda "1.200 çağrı", yanındaki sparkline 1.150
// topluyor — tek sorgu çiftinden iki farklı cevap, hiçbir hata mesajı yok.
// Aşağıdaki TestOperationsSparklineGridCoversEveryAdmittedBucket bu ayrımı
// ölçüyor; saf sınır testi onu YAKALAYAMAZ çünkü sorun sınırla ızgaranın
// BİRLİKTE ürettiği tutarsızlık.
//
// admitsBucket db_bucket_bound_test.go'dan (823), bucketUpperOp
// dbstmt_bucket_bound_test.go'dan (1167), funcBody
// summary_bucket_bound_test.go'dan (1168).
package chstore

import (
	"strings"
	"testing"
	"time"
)

const opsMVSig = "func (s *Store) queryOperationsFromMV("

// operationsBucketReads — iki okuma, tek gövdeden. Gövde İKİ kova yüklemi
// taşıdığı için bucketUpperOp'a bütün hâlde verilemez (o "tam 1 yüklem"
// şartı koşar, ki yeni bir denetlenmeyen sorgu eklenirse yakalasın diye
// öyle); GROUP BY satırından ikiye bölünüyor.
func operationsBucketReads(t *testing.T) []struct {
	name string
	sql  string
} {
	t.Helper()
	body := funcBody(t, "repo.go", opsMVSig)
	// İkinci sorgu sparkline slot indeksini hesaplayan tek yer.
	split := strings.Index(body, "AS bidx")
	if split < 0 {
		t.Fatalf("%s içinde sparkline sorgusu (AS bidx) bulunamadı — yapı "+
			"değiştiyse testi güncelle", opsMVSig)
	}
	return []struct {
		name string
		sql  string
	}{
		{"birinci geçiş (satır toplamı)", body[:split]},
		{"ikinci geçiş (sparkline)", body[split:]},
	}
}

// TestOperationsReadsExcludeUpperBucket — DAVRANIŞ testi, 823'ün tablosu.
func TestOperationsReadsExcludeUpperBucket(t *testing.T) {
	from := time.Date(2026, 8, 19, 10, 0, 0, 0, time.UTC)
	to := time.Date(2026, 8, 19, 11, 0, 0, 0, time.UTC)

	cases := []struct {
		name   string
		bucket time.Time
		want   bool
	}{
		{"pencereden ÖNCEKİ kova dışarıda", from.Add(-5 * time.Minute), false},
		// v0.5.299: winStart Truncate'ten geçtiği için bu kova İÇERİDE
		// (operatör-bildirimli "No operations" bug'ının düzeltmesi).
		{"tam from'daki kova içeride", from, true},
		{"ortadaki kova içeride", from.Add(30 * time.Minute), true},
		{"son tam kova içeride", to.Add(-5 * time.Minute), true},
		{"tam to'daki kova DIŞARIDA", to, false},
		{"to'dan sonraki kova dışarıda", to.Add(5 * time.Minute), false},
	}

	for _, r := range operationsBucketReads(t) {
		op := bucketUpperOp(t, r.name, r.sql)
		for _, c := range cases {
			t.Run(r.name+"/"+c.name, func(t *testing.T) {
				if got := admitsBucket(op, c.bucket, from, to); got != c.want {
					t.Errorf("%s: `time_bucket %s ?` ile %v kovası alındı=%v, beklenen %v",
						r.name, op, c.bucket.Format("15:04"), got, c.want)
				}
			})
		}
	}
}

// TestOperationsBothPassesShareTheBound — satır toplamı ile sparkline aynı
// pencereyi okumak zorunda. Ayrışırlarsa satırdaki skaler ile yanındaki
// seri farklı veriyi anlatır ve ikisi de tek başına "doğru" görünür.
func TestOperationsBothPassesShareTheBound(t *testing.T) {
	reads := operationsBucketReads(t)
	aggOp := bucketUpperOp(t, reads[0].name, reads[0].sql)
	sparkOp := bucketUpperOp(t, reads[1].name, reads[1].sql)
	if aggOp != sparkOp {
		t.Fatalf("satır toplamı `%s` / sparkline `%s` — sınırlar AYRIŞMIŞ",
			aggOp, sparkOp)
	}
}

// TestOperationsSparklineGridCoversEveryAdmittedBucket — YENİ sınıf.
//
// Yüklemin ALDIĞI her MV kovası, sparkline ızgarasında geçerli bir slota
// düşmek zorunda: aksi hâlde kova birinci geçişin toplamına girer,
// sparkline'da `int(bidx) < nBuckets` kapısında düşer ve iki yüzey
// ayrışır. `to` ızgara adımının tam katıysa (yani her hizalı pencerede)
// `<=` bu şartı ihlal eder.
//
// Pencere genişlikleri sparklineGrid'in iki rejimini de dolaşıyor:
// bucketSec=300 (dar) ve coarsened (geniş).
func TestOperationsSparklineGridCoversEveryAdmittedBucket(t *testing.T) {
	reads := operationsBucketReads(t)
	op := bucketUpperOp(t, reads[1].name, reads[1].sql)

	for _, win := range []time.Duration{
		15 * time.Minute, 30 * time.Minute, time.Hour, 3 * time.Hour,
		6 * time.Hour, 24 * time.Hour, 7 * 24 * time.Hour,
		// Hizasız pencere: `<` ile `<=` burada AYNI sonucu verir, yani
		// test hizalı pencerelere özel bir muafiyet tanımıyor.
		time.Hour + 2*time.Minute,
	} {
		t.Run(win.String(), func(t *testing.T) {
			// repo.go'nun aritmetiği: bucketStart hizalı, winSec ondan ölçülür.
			winEnd := time.Date(2026, 8, 19, 11, 0, 0, 0, time.UTC)
			bucketStart := winEnd.Add(-win).Truncate(5 * time.Minute)
			winSec := int64(winEnd.Sub(bucketStart).Seconds())
			bucketSec, nBuckets := sparklineGrid(winSec, 300)

			for b := bucketStart; !b.After(winEnd.Add(5 * time.Minute)); b = b.Add(5 * time.Minute) {
				if !admitsBucket(op, b, bucketStart, winEnd) {
					continue
				}
				bidx := int64(b.Sub(bucketStart).Seconds()) / bucketSec
				if bidx < 0 || int(bidx) >= nBuckets {
					t.Errorf("`time_bucket %s ?`: %v kovası alınıyor ama slot %d, "+
						"ızgara %d slot (bucketSec=%d, winSec=%d) — satır toplamına "+
						"girip sparkline'dan düşer",
						op, b.Format("15:04"), bidx, nBuckets, bucketSec, winSec)
				}
			}
		})
	}
}

// TestOperationsReadsAlignLowerBound — v0.5.299 sözleşmesi (operatör
// bildirimi: trafiği OLAN serviste "No operations"). Üst sınırı düzeltirken
// alt sınırın hizasını kaybetmek daha kötü bir bug olurdu.
func TestOperationsReadsAlignLowerBound(t *testing.T) {
	body := funcBody(t, "repo.go", opsMVSig)
	if !strings.Contains(body, "winStart.Truncate(5 * time.Minute)") {
		t.Error("winStart 5dk ızgarasına inmiyor — winStart'ı İÇEREN kova elenir (v0.5.299)")
	}
	// Fonksiyon-kapsamlı kapı: repo.go'nun tamamı henüz temiz DEĞİL
	// (traces ailesi ayrı bir dilim — orada `<=` kasıtlı olabilir, çünkü bir
	// trace kovaları AŞAR ve üst sınırı kısmak süreyi budar), bu yüzden
	// dosya-geneli değil gövde-geneli sayım.
	if n := strings.Count(body, "time_bucket <= ?"); n > 0 {
		t.Errorf("queryOperationsFromMV: %d adet `time_bucket <= ?` — üst kova "+
			"sınırı `< ?` olmalı (v0.9.823/1156/1167/1168/1169 sınıfı)", n)
	}
}
