// v0.9.1170 regresyon testleri — ÜST KOVA SINIRI, /endpoints ailesi.
//
// Sınıfın altıncı dalgası (823 /databases · 1156 dependencies+db_trends ·
// 1167 dbstmt/slow-queries · 1168 /api/services · 1169 /service operations).
// İki okuma:
//
//	GetEndpointsMV     → tablo + üç sparkline, TEK sorgu (per_bucket CTE)
//	EndpointExemplars  → çekmecedeki yavaş/hatalı örnek trace
//
// Kardeşlerden farkı: kova genişliği 5dk DEĞİL. endpointsSparkGrid iki
// spanmetrics katmanı arasında seçim yapıyor (1m / 10s), yani sınır hatası
// grain kadar yabancı veri sızdırır — ama tetikleyici aynı: `to` slot
// genişliğinin tam katıysa `<=` pencerenin tamamen dışındaki bir kovayı alır.
//
// Bu grubun kendine ait katkısı, 1169'un ızgara tutarsızlığının TEK SORGU
// hâli: fazla kova per_bucket'a girip `sum(bv)` toplamına katılıyor
// (calls/errors/quantiles → ReqPerMin), ama sparkline'lar
// `range(0, nBuckets)` ile örüldüğünden b = nBuckets olan satır seriye hiç
// girmiyor. İki yüzey aynı sorgudan farklı cevap veriyor ve hiçbir şey
// bunu söylemiyor.
package chstore

import (
	"strings"
	"testing"
	"time"
)

const (
	endpointsMVSig  = "func (s *Store) GetEndpointsMV("
	endpointsExeSig = "func (s *Store) EndpointExemplars("
)

func endpointsBucketReads(t *testing.T) []struct {
	name string
	sql  string
} {
	t.Helper()
	return []struct {
		name string
		sql  string
	}{
		{"GetEndpointsMV (tablo + sparkline)",
			funcBody(t, "endpoints.go", endpointsMVSig)},
		{"EndpointExemplars (örnek trace)",
			funcBody(t, "endpoints_detail.go", endpointsExeSig)},
	}
}

// TestEndpointsReadsExcludeUpperBucket — DAVRANIŞ testi, 823'ün tablosu,
// spanmetrics_1m greninde (60sn).
func TestEndpointsReadsExcludeUpperBucket(t *testing.T) {
	const grain = time.Minute
	from := time.Date(2026, 8, 19, 10, 0, 0, 0, time.UTC)
	to := time.Date(2026, 8, 19, 11, 0, 0, 0, time.UTC)

	cases := []struct {
		name   string
		bucket time.Time
		want   bool
	}{
		{"pencereden ÖNCEKİ kova dışarıda", from.Add(-grain), false},
		// From, Truncate(time.Minute)'dan geçiyor → bu kova içeride.
		{"tam from'daki kova içeride", from, true},
		{"ortadaki kova içeride", from.Add(30 * time.Minute), true},
		{"son tam kova içeride", to.Add(-grain), true},
		// [11:00, 11:01) — pencereden sıfır veri. BUG BUYDU.
		{"tam to'daki kova DIŞARIDA", to, false},
		{"to'dan sonraki kova dışarıda", to.Add(grain), false},
	}

	for _, r := range endpointsBucketReads(t) {
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

// TestEndpointsSparklineGridCoversEveryAdmittedBucket — YENİ sınıf, 1169'un
// tek-sorgu hâli. Yüklemin ALDIĞI her kova `range(0, nBuckets)` aralığına
// düşmek zorunda; düşmezse toplam onu sayar, seri saymaz.
//
// İki katman da dolaşılıyor: fromAge küçükken 10sn katmanı (kısa
// pencerelerde), büyükken 1dk katmanı — sınır hatası her ikisinde de aynı
// tetikleyiciye sahip ama slot genişlikleri farklı.
func TestEndpointsSparklineGridCoversEveryAdmittedBucket(t *testing.T) {
	reads := endpointsBucketReads(t)
	op := bucketUpperOp(t, reads[0].name, reads[0].sql)

	for _, age := range []struct {
		name string
		sec  int64
	}{
		{"taze pencere (10sn katmanı uygun)", 0},
		{"eski pencere (1dk katmanı)", 30 * 24 * 3600},
	} {
		for _, win := range []time.Duration{
			5 * time.Minute, 15 * time.Minute, time.Hour, 6 * time.Hour,
			24 * time.Hour, 7 * 24 * time.Hour,
			// Hizasız: `<` ile `<=` aynı sonucu verir, yani test hizalı
			// pencerelere özel muafiyet tanımıyor.
			time.Hour + 37*time.Second,
		} {
			t.Run(age.name+"/"+win.String(), func(t *testing.T) {
				to := time.Date(2026, 8, 19, 11, 0, 0, 0, time.UTC)
				// endpoints.go aritmetiği birebir.
				from := to.Add(-win).Truncate(time.Minute)
				windowSec := to.Unix() - from.Unix()
				if windowSec <= 0 {
					windowSec = 60
				}
				sourceMV, bucketSec, nBuckets := endpointsSparkGrid(windowSec, age.sec)
				grain := time.Minute
				if sourceMV == "spanmetrics_10s" {
					grain = 10 * time.Second
				}
				for b := from; !b.After(to.Add(grain)); b = b.Add(grain) {
					if !admitsBucket(op, b, from, to) {
						continue
					}
					idx := int64(b.Sub(from).Seconds()) / bucketSec
					if idx < 0 || int(idx) >= nBuckets {
						t.Errorf("`time_bucket %s ?` (%s): %v kovası alınıyor ama slot %d, "+
							"ızgara %d slot (bucketSec=%d) — sum(bv)'ye girip "+
							"sparkline'dan düşer",
							op, sourceMV, b.Format("15:04:05"), idx, nBuckets, bucketSec)
					}
				}
			})
		}
	}
}

// TestEndpointsReadsAlignLowerBound — alt sınır sözleşmesi ayakta: From
// grain'e iniyor (endpoints.go yorumu: "so the first bucket is wholly
// inside [from, to] rather than half-clipped"). Üst sınırı düzeltirken
// alt sınırın hizasını kaybetmek daha kötü bir bug olurdu.
func TestEndpointsReadsAlignLowerBound(t *testing.T) {
	if body := funcBody(t, "endpoints.go", endpointsMVSig); !strings.Contains(body, "q.From.Truncate(time.Minute)") {
		t.Error("GetEndpointsMV: From dakika ızgarasına inmiyor — ilk kova yarım kırpılır")
	}
	// Exemplar okumasının From hizası endpointExemplarArgs'ta ve zaten
	// TestEndpointExemplarArgs_FromFlooredToMVGrain tarafından çivili;
	// burada yalnız üst sınırın operatörünü sayıyoruz.
	for _, f := range []struct{ file, sig string }{
		{"endpoints.go", endpointsMVSig},
		{"endpoints_detail.go", endpointsExeSig},
	} {
		body := funcBody(t, f.file, f.sig)
		if n := strings.Count(body, "time_bucket <= ?"); n > 0 {
			t.Errorf("%s: %d adet `time_bucket <= ?` — üst kova sınırı `< ?` olmalı "+
				"(v0.9.823/1156/1167/1168/1169/1170 sınıfı)", f.sig, n)
		}
	}
}
