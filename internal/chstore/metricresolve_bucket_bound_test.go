// v0.9.1173 regresyon testleri — ÜST KOVA SINIRI, metrik çözümleyici +
// exemplar rollup'u. Sınıfın dokuzuncu ve son dalgası (823 · 1156 · 1167 ·
// 1168 · 1169 · 1170 · 1171 · 1172).
//
// Grup G, üç okuma:
//
//	ResolveMetricQuery / resolveBand (spanmetrics kademeleri) → seri
//	FindExemplarRollup (spanmetrics_1m)                       → örnek trace
//
// Seride belirti bir FANTOM NOKTA: `to` hem step'e hem MV grenine
// oturduğunda çıktı kovası toStartOfInterval(to, step) YALNIZ `to` etiketli
// MV kovasından beslenir — o da [to, to+gren) taşır. Yani grafiğin sağ
// ucundaki nokta tamamen pencere DIŞI veriden örülüyordu; eksik değil,
// UYDURMA bir nokta.
//
// Exemplar'da zarar KİMLİK (1167/1170 ile aynı): argMax pencere dışı bir
// kovadan trace seçebiliyordu.
//
// rollup_fastpath.go'daki `bucket <= to` BİLİNÇLİ OLARAK DIŞARIDA ve bu
// dosya onu çiviliyor (aşağıdaki son test): oradaki `bucket` ham kova
// etiketi DEĞİL, `_shift`li kayan pencerenin ÇIKTI konumu. B çıktı kovası
// [B-(k-1)·step, B+step) ham aralığından beslenir, yani pencere içi verinin
// büyük kısmını taşır; `<` yapmak son pencereli noktayı düşürür ve
// pencereli yol pencereli-olmayan yoldan bir adım kısa biter.
package chstore

import (
	"os"
	"strings"
	"testing"
	"time"
)

func metricResolveBucketReads(t *testing.T) []struct {
	name string
	sql  string
} {
	t.Helper()
	return []struct {
		name string
		sql  string
	}{
		{"ResolveMetricQuery (seri)",
			funcBody(t, "metricresolve.go", "func (s *Store) ResolveMetricQuery(")},
		{"resolveBand (bant serisi)",
			funcBody(t, "metricresolve.go", "func (s *Store) resolveBand(")},
		{"FindExemplarRollup (örnek trace)",
			funcBody(t, "exemplar.go", "func (s *Store) FindExemplarRollup(")},
	}
}

// TestMetricResolveReadsExcludeUpperBucket — DAVRANIŞ testi, 823'ün tablosu.
func TestMetricResolveReadsExcludeUpperBucket(t *testing.T) {
	const grain = time.Minute
	from := time.Date(2026, 8, 19, 10, 0, 0, 0, time.UTC)
	to := time.Date(2026, 8, 19, 11, 0, 0, 0, time.UTC)

	cases := []struct {
		name   string
		bucket time.Time
		want   bool
	}{
		{"pencereden ÖNCEKİ kova dışarıda", from.Add(-grain), false},
		{"tam from'daki kova içeride", from, true},
		{"ortadaki kova içeride", from.Add(30 * time.Minute), true},
		{"son tam kova içeride", to.Add(-grain), true},
		{"tam to'daki kova DIŞARIDA", to, false},
		{"to'dan sonraki kova dışarıda", to.Add(grain), false},
	}

	for _, r := range metricResolveBucketReads(t) {
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

// TestMetricSeriesHasNoPhantomTrailingPoint — YENİ sınıf: serinin HİÇBİR
// çıktı noktası yalnız pencere-dışı veriden beslenemez.
//
// Çıktı kovası toStartOfInterval(time_bucket, step). Bir çıktı noktasının
// "gerçek" olması için onu besleyen MV kovalarından EN AZ BİRİ pencereye ait
// olmalı. `<= to` ile step'e ve grene hizalı pencerede son nokta yalnız `to`
// etiketli kovadan besleniyordu — %100 uydurma.
//
// Hizasız pencere de tabloda: orada iki operatör aynı seriyi verir, yani
// test hizalı pencerelere özel muafiyet tanımıyor.
func TestMetricSeriesHasNoPhantomTrailingPoint(t *testing.T) {
	reads := metricResolveBucketReads(t)
	const grain = time.Minute

	for _, r := range reads[:2] { // yalnız seri üreten iki okuma
		op := bucketUpperOp(t, r.name, r.sql)
		for _, tc := range []struct {
			name    string
			step    time.Duration
			win     time.Duration
			skewSec int // to'yu hizadan kaydırma
		}{
			{"step=1dk pencere=1sa", time.Minute, time.Hour, 0},
			{"step=5dk pencere=6sa", 5 * time.Minute, 6 * time.Hour, 0},
			{"step=15dk pencere=24sa", 15 * time.Minute, 24 * time.Hour, 0},
			{"step=1sa pencere=7g", time.Hour, 7 * 24 * time.Hour, 0},
			{"hizasız to (step=5dk)", 5 * time.Minute, time.Hour, 137},
		} {
			t.Run(r.name+"/"+tc.name, func(t *testing.T) {
				to := time.Date(2026, 8, 19, 11, 0, 0, 0, time.UTC).
					Add(time.Duration(tc.skewSec) * time.Second)
				from := to.Add(-tc.win)

				// Çıktı noktası → onu besleyen pencere-İÇİ MV kovası var mı?
				fed := map[time.Time]bool{}
				for b := from.Truncate(grain); !b.After(to.Add(2 * tc.step)); b = b.Add(grain) {
					if !admitsBucket(op, b, from, to) {
						continue
					}
					out := b.Truncate(tc.step)
					// Kova pencereye AİT mi: başlangıcı to'dan önce olmalı.
					if b.Before(to) {
						fed[out] = true
					} else if !fed[out] {
						fed[out] = false
					}
				}
				for out, real := range fed {
					if !real {
						t.Errorf("%s: %v çıktı noktası YALNIZ pencere-dışı kovalardan "+
							"besleniyor (`time_bucket %s ?`, step=%v, to=%v) — grafiğin "+
							"sağ ucunda uydurma nokta",
							r.name, out.Format("15:04"), op, tc.step, to.Format("15:04:05"))
					}
				}
			})
		}
	}
}

// TestRollupFastpathWindowedBoundStaysInclusive — rollup_fastpath.go'daki
// `bucket <= %d` KASITLI. Bu test onu çiviliyor ki bir sonraki sınır
// taraması onu "kaçmış bir üye" sanıp düzeltmesin.
//
// Fark: oradaki `bucket` ham kova etiketi değil, `_shift`li kayan pencerenin
// ÇIKTI konumu. B çıktı kovası [B-(k-1)·step, B+step) ham aralığından
// beslenir — yani pencere içi verinin büyük kısmını taşır. `<` yapmak son
// pencereli noktayı düşürür ve pencereli yol, pencereli-OLMAYAN yoldan
// (winK == 0, hiç bucket guard'ı yok, son kısmi kova korunur) bir adım kısa
// biterdi: aynı sorgu, iki farklı seri uzunluğu.
func TestRollupFastpathWindowedBoundStaysInclusive(t *testing.T) {
	b, err := os.ReadFile("rollup_fastpath.go")
	if err != nil {
		t.Fatalf("rollup_fastpath.go okunamadı: %v", err)
	}
	src := string(b)
	if !strings.Contains(src, `fmt.Sprintf("bucket <= %d", to.UnixNano())`) {
		t.Error("kayan pencere üst sınırı `bucket <= to` olmalı — bu KASITLI " +
			"(çıktı konumu, ham kova etiketi değil). Değiştirmeden önce bu testin " +
			"doc bloğunu oku: v0.9.1173.")
	}
	// Ve `_shift` hâlâ orada olmalı, yoksa yukarıdaki gerekçe çöker.
	if !strings.Contains(src, "AS _shift") {
		t.Error("`_shift` ARRAY JOIN'i kaybolmuş — `bucket <= to` muafiyetinin " +
			"dayanağı buydu, sınırı yeniden değerlendir")
	}
}

// TestNoInclusiveUpperBucketBoundInMetricResolve — dosya-geneli kapı.
func TestNoInclusiveUpperBucketBoundInMetricResolve(t *testing.T) {
	for _, f := range []string{"metricresolve.go", "exemplar.go"} {
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatalf("%s okunamadı: %v", f, err)
		}
		if n := strings.Count(string(b), "time_bucket <= ?"); n > 0 {
			t.Errorf("%s: %d adet `time_bucket <= ?` — üst kova sınırı `< ?` olmalı "+
				"(v0.9.823→1173 sınıfı)", f, n)
		}
	}
}
