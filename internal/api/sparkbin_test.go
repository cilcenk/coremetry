package api

// v0.9.1176 regresyon testi — /api/spanmetrics/services sparkline'ının
// SATIR TOPLAMIYLA aynı kova kümesini örtmesi.
//
// v0.9.1175 bu bloğun dört okumasının ÜST sınırını düzeltmişti ama alt uçta
// ayrı bir ayrışma açık kalmıştı ve o commit'te yazılı olarak bırakılmıştı:
// sorgu `bucketStart`tan okuyor, binleme ham `from`dan sayıyordu. from
// hizasızken (tipik durum — pencere "son 1 saat" ise from bir kova sınırına
// oturmaz) bucketStart'taki kova b = -1 üretip `range(0, bins)` dışında
// kalıyor, sparkline'dan düşüyor, ama Stage-1'in `calls` toplamına
// giriyordu. Satırda "1.200 çağrı", altındaki seri daha azını topluyor —
// v0.9.1169/1170'te ÜST uçta düzeltilen ayrışmanın alt uç ikizi, ve hiçbir
// hata mesajı üretmiyor.
//
// Ölçülen sözleşme tek cümle: WHERE'in ALDIĞI her MV kovası geçerli bir
// bine düşer. Test bunu hem hizalı hem hizasız pencerelerde, hem de
// dejenere (sıfır/negatif/çok dar) girdilerde koşturur — çünkü asıl bug
// tam olarak "hizasız pencere" dalıydı ve yalnız hizalı bir örnekle
// yazılmış bir test onu göremezdi ([[feedback-unit-mixing-needs-both-branches]]
// sınıfı: her iki dal da denenmeli).

import (
	"testing"
	"time"
)

const sparkBinsDefault = 30

// admittedBuckets — WHERE `time_bucket >= bucketStart AND time_bucket < to`
// yükleminin aldığı 5dk kovaları.
func admittedBuckets(bucketStart, to time.Time) []time.Time {
	var out []time.Time
	for b := bucketStart; b.Before(to); b = b.Add(5 * time.Minute) {
		out = append(out, b)
	}
	return out
}

func TestSparkBinCoversEveryAdmittedBucket(t *testing.T) {
	base := time.Date(2026, 8, 19, 11, 0, 0, 0, time.UTC)

	cases := []struct {
		name string
		from time.Time
		to   time.Time
	}{
		// Hizalı pencereler.
		{"1sa hizalı", base.Add(-time.Hour), base},
		{"15dk hizalı", base.Add(-15 * time.Minute), base},
		{"24sa hizalı", base.Add(-24 * time.Hour), base},
		{"7g hizalı", base.Add(-7 * 24 * time.Hour), base},
		// HİZASIZ from — bug'ın yaşadığı dal. bucketStart < from olur ve
		// eski aritmetikte o kova b = -1 üretirdi.
		{"1sa, from :03", base.Add(-time.Hour).Add(3 * time.Minute), base},
		{"1sa, from :04:59", base.Add(-time.Hour).Add(4*time.Minute + 59*time.Second), base},
		{"15dk, from :01", base.Add(-15 * time.Minute).Add(time.Minute), base},
		// Hizasız to.
		{"1sa, to :02:17", base.Add(-time.Hour), base.Add(2*time.Minute + 17*time.Second)},
		{"her iki uç hizasız", base.Add(-time.Hour).Add(3 * time.Minute), base.Add(137 * time.Second)},
		// Dar pencereler: bin sayısından az kova.
		{"5dk", base.Add(-5 * time.Minute), base},
		{"1dk", base.Add(-time.Minute), base},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			bucketStart := c.from.Truncate(5 * time.Minute)
			originNs, widthNs := sparkBinPlan(bucketStart, c.to, sparkBinsDefault)

			buckets := admittedBuckets(bucketStart, c.to)
			if len(buckets) == 0 {
				t.Fatalf("hiç kova alınmıyor — test kurgusu bozuk (%v→%v)", bucketStart, c.to)
			}
			for _, b := range buckets {
				idx := sparkBinIndex(b, originNs, widthNs)
				if idx < 0 {
					t.Errorf("%v kovası bin %d — NEGATİF: satır toplamına girer, "+
						"sparkline'dan düşer (v0.9.1176 bug'ının ta kendisi)",
						b.Format("15:04"), idx)
				}
				if idx >= sparkBinsDefault {
					t.Errorf("%v kovası bin %d — ızgara %d bin: satır toplamına girer, "+
						"sparkline'dan düşer", b.Format("15:04"), idx, sparkBinsDefault)
				}
			}
		})
	}
}

// TestSparkBinOriginMatchesQueryLowerBound — orijin, WHERE'in alt sınırıyla
// AYNI an olmalı. Ayrışmanın tek sebebi buydu; sabitlemek, ileride birinin
// "binleri kullanıcının from'una göre hizalayalım" diye geri almasını
// engeller.
func TestSparkBinOriginMatchesQueryLowerBound(t *testing.T) {
	from := time.Date(2026, 8, 19, 10, 3, 27, 0, time.UTC)
	to := time.Date(2026, 8, 19, 11, 0, 0, 0, time.UTC)
	bucketStart := from.Truncate(5 * time.Minute)

	originNs, _ := sparkBinPlan(bucketStart, to, sparkBinsDefault)
	if originNs != bucketStart.UnixNano() {
		t.Errorf("origin %d, bucketStart %d — binleme sorgunun okuduğu andan "+
			"BAŞKA bir ana göre sayıyor", originNs, bucketStart.UnixNano())
	}
	if originNs == from.UnixNano() {
		t.Error("origin ham `from` — hizasız pencerede ilk kova b = -1 üretir " +
			"(v0.9.1176'nın düzelttiği bug)")
	}
}

// TestSparkBinTestDiscriminates — testin AYIRT EDEBİLDİĞİNİN kanıtı.
//
// Bir regresyon testinin en sinsi başarısızlığı, düzeltmeden ÖNCE de yeşil
// olmasıdır. Bu yüzden ESKİ aritmetiği (origin = ham from, genişlik =
// (to-from)/bins) burada yeniden kurup, alınan kovalardan en az birinin
// ızgara dışına düştüğünü ZORUNLU kılıyorum. Yukarıdaki sözleşme testi
// yeşil, bu test de eski hâlin kırmızı olacağını gösteriyorsa kapı
// gerçektir.
func TestSparkBinTestDiscriminates(t *testing.T) {
	// Hizasız from — bug'ın yaşadığı dal.
	from := time.Date(2026, 8, 19, 10, 3, 27, 0, time.UTC)
	to := time.Date(2026, 8, 19, 11, 0, 0, 0, time.UTC)
	bucketStart := from.Truncate(5 * time.Minute)

	oldOrigin := from.UnixNano()
	oldWidth := (to.UnixNano() - oldOrigin) / int64(sparkBinsDefault)
	if oldWidth <= 0 {
		t.Fatal("test kurgusu bozuk: eski genişlik pozitif olmalı")
	}

	escaped := false
	for _, b := range admittedBuckets(bucketStart, to) {
		idx := sparkBinIndex(b, oldOrigin, oldWidth)
		if idx < 0 || idx >= sparkBinsDefault {
			escaped = true
		}
	}
	if !escaped {
		t.Error("eski aritmetikte hiçbir kova ızgara dışına düşmedi — bu test " +
			"düzeltmeyi ÖLÇMÜYOR demektir, senaryoyu gözden geçir")
	}
}

// TestSparkBinDegenerateWindows — sıfır/negatif/çok dar pencerelerde bile
// genişlik pozitif kalmalı, yoksa intDiv sıfıra bölerdi.
func TestSparkBinDegenerateWindows(t *testing.T) {
	at := time.Date(2026, 8, 19, 11, 0, 0, 0, time.UTC)
	for _, c := range []struct {
		name            string
		bucketStart, to time.Time
		bins            int
	}{
		{"sıfır pencere", at, at, sparkBinsDefault},
		{"ters pencere", at, at.Add(-time.Hour), sparkBinsDefault},
		{"bin sayısı sıfır", at.Add(-time.Hour), at, 0},
		{"bin sayısı negatif", at.Add(-time.Hour), at, -5},
		{"binden dar pencere", at.Add(-20 * time.Nanosecond), at, sparkBinsDefault},
	} {
		t.Run(c.name, func(t *testing.T) {
			_, widthNs := sparkBinPlan(c.bucketStart, c.to, c.bins)
			if widthNs <= 0 {
				t.Fatalf("genişlik %d — intDiv sıfıra bölerdi", widthNs)
			}
		})
	}
}
