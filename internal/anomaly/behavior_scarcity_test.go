// Davranış motoru — ÖRNEK-KITLIĞI KAPISI (v0.9.957).
//
// ─── Hangi olayı zorunlu kılıyor ─────────────────────────────────────
// v0.9.936 gemiye indikten sonra lokal ölçümde motor TEK TİKTE 178 aday
// üretti; ezici çoğunluğu "istek hızı · mevsimsel sapma ↓" ve hepsi aynı
// haftanın-saati kovasında. Kalite kanıtı değildi, kıtlık ürünüydü.
//
// ÖLÇÜM (2026-08-11, lokal küme, service_summary_5m):
//
//	pencere         : 2026-08-01 → 2026-08-10, 9 GÜN (spec 28 varsayıyor)
//	hedef kova (21) : her servis için TAM 24 örnek — 12'lik eski kapı
//	                  rahat geçiyor
//	o 24 örneğin geldiği FARKLI GÜN sayısı: 2
//
// Yani kapının baktığı sayı (örnek) yeterliydi; eksik olan ÇEŞİTLİLİKTİ.
// Mevsimsel z'nin ölçtüğü şey haftadan haftaya YAYILIM ve iki gözlemden
// yayılım kestirilemez: MAD dejenere oluyor, z patlıyor, her normal
// dalgalanma "sapma" görünüyor. v0.8.250'nin sınıfı, yeni bir kılıkta.
//
// Bu dosya kapının İKİ boyutunu da (örnek sayısı + gün çeşitliliği)
// zorunlu kılıyor ve kıtlık telemetrisinin karar yoluyla AYNI ifadeyi
// kullandığını pinliyor — kart "0 kova atlandı" derken motorun sessizce
// atlaması, sessiz kapanma sınıfının ta kendisi olurdu.
package anomaly

import (
	"testing"

	"github.com/cilcenk/coremetry/internal/chstore"
)

// TestSplitBehaviorSeriesCountsRepeats — tekrar sayımı GÜN değişimini
// sayar, satır sayısını değil. Satırlar zamana göre artan geldiği için
// (SQL ORDER BY service_name, t) küme tutmaya gerek yok; bu test o
// varsayımı da pinliyor.
func TestSplitBehaviorSeriesCountsRepeats(t *testing.T) {
	const day = int64(86400)
	// Üç gün, her gün aynı kovada 12 bucket → 36 örnek / 3 tekrar.
	var rows []behaviorRow
	for d := int64(0); d < 3; d++ {
		for i := int64(0); i < 12; i++ {
			rows = append(rows, behaviorRow{
				Unix: d*day + i*300, HOW: 10, Spans: 3000, P99Ms: 100,
			})
		}
	}
	baseline, recent := splitBehaviorSeries(rows, "p99_ms", 10*day)
	if len(recent) != 0 {
		t.Fatalf("pencere %d satır, want 0 (hepsi kesimden eski)", len(recent))
	}
	b := baseline[10]
	if len(b.Values) != 36 {
		t.Errorf("örnek = %d, want 36", len(b.Values))
	}
	if b.Repeats != 3 {
		t.Errorf("tekrar = %d, want 3 — gün değişimi sayılmıyor", b.Repeats)
	}
}

// TestSplitBehaviorSeriesSameDayIsOneRepeat — ÖLÇÜLEN vakanın çekirdeği:
// bol örnek, tek gün. Eski kapı (yalnız sayı) bunu "yeterli baseline"
// sayıyordu.
func TestSplitBehaviorSeriesSameDayIsOneRepeat(t *testing.T) {
	var rows []behaviorRow
	for i := int64(0); i < 24; i++ {
		rows = append(rows, behaviorRow{Unix: i * 300, HOW: 10, Spans: 3000, P99Ms: 100})
	}
	b, _ := splitBehaviorSeries(rows, "p99_ms", 1_000_000)
	if got := b[10]; got.Repeats != 1 {
		t.Errorf("tekrar = %d, want 1 — 24 örnek TEK günden geliyor", got.Repeats)
	}
}

// TestBehaviorSufficient — kapının tablo testi. İKİ boyut da bağımsız
// olarak reddedebilmeli; birini geçmek diğerini affetmez.
func TestBehaviorSufficient(t *testing.T) {
	cfg := chstore.DefaultAnomalyBehavior() // MinSamples 12, MinRepeats 3
	mk := func(n, repeats int) behaviorBucket {
		return behaviorBucket{Values: make([]float64, n), Repeats: repeats}
	}
	cases := []struct {
		name   string
		bucket behaviorBucket
		want   bool
	}{
		{"boş kova", mk(0, 0), false},
		{"örnek kıt, tekrar dolu", mk(11, 4), false},
		{"örnek dolu, tekrar kıt", mk(48, 2), false},
		{"ölçülen lokal hâl (24 örnek / 2 gün)", mk(24, 2), false},
		{"ikisi de sınırda", mk(12, 3), true},
		{"28 günlük dolu pencere", mk(48, 4), true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := behaviorSufficient(c.bucket, cfg); got != c.want {
				t.Errorf("behaviorSufficient(%d örnek, %d tekrar) = %v, want %v",
					len(c.bucket.Values), c.bucket.Repeats, got, c.want)
			}
		})
	}
}

// TestCountScarceBucketsMatchesGate — telemetri ile karar AYNI ifadeyi
// kullanmalı. Ayrışırlarsa kart "0 kova atlandı" derken motor sessizce
// atlar; bu depoda sessiz kapanma tekrarlayan hata sınıfı.
func TestCountScarceBucketsMatchesGate(t *testing.T) {
	cfg := chstore.DefaultAnomalyBehavior()
	baseline := map[int]behaviorBucket{
		9:  {Values: make([]float64, 48), Repeats: 4}, // yeterli
		10: {Values: make([]float64, 24), Repeats: 2}, // kıt (çeşitlilik)
		11: {Values: make([]float64, 6), Repeats: 4},  // kıt (örnek)
	}
	if got := countScarceBuckets(baseline, cfg); got != 2 {
		t.Fatalf("atlanan kova = %d, want 2", got)
	}
	// Aynı kümeyi kapının kendisiyle say — iki taraf ayrışamaz.
	manual := 0
	for _, b := range baseline {
		if !behaviorSufficient(b, cfg) {
			manual++
		}
	}
	if manual != countScarceBuckets(baseline, cfg) {
		t.Error("telemetri sayacı karar kapısıyla ayrışmış")
	}
}

// TestBehaviorShortHistoryProducesNoCandidates — brief'in asıl talebi:
// MV penceresi henüz dolmamış bir kurulumda (lokal 9 gün, ya da yeni
// kurulmuş prod) motor SIFIR aday üretmeli.
//
// Senaryo ÖLÇÜLEN hâlin birebir kopyası: 24 örnek, 2 gün, ve "şimdi"de
// devasa bir sapma. Kapı olmasaydı bu aday üretirdi — ve tam olarak
// üretmişti.
func TestBehaviorShortHistoryProducesNoCandidates(t *testing.T) {
	cfg := chstore.DefaultAnomalyBehavior()
	recent := rowsAt(10, 8, 3000, 0, 400) // 4× sapma, kesinlikle ateşlerdi

	short := baselineWithRepeats(10, 24, 100, 2)
	if c, ok := evalBehavior("checkout", "p99_ms", short, recent, p99Policy(), cfg); ok {
		t.Errorf("9 günlük geçmişle aday üretti (%s %.2f×) — kıtlık kapısı kesmiyor",
			c.Signal, c.Ratio)
	}
	if got := countScarceBuckets(short, cfg); got != 1 {
		t.Errorf("atlanan kova = %d, want 1 — sessizliğin gerekçesi raporlanmıyor", got)
	}

	// KONTROL: aynı sapma, geçmiş dolu → aday GELMELİ. Kapının fiilen
	// motoru kapatmadığını kanıtlar.
	full := baselineWithRepeats(10, 48, 100, 4)
	if _, ok := evalBehavior("checkout", "p99_ms", full, recent, p99Policy(), cfg); !ok {
		t.Error("geçmişi dolu kurulumda da aday YOK — kapı motoru susturmuş")
	}
}

// TestBehaviorDayIndex — tekrar sayımının birimi. UTC gün indeksi;
// kova zaten UTC pinli (v0.8.323), yani yaz saati sıçraması yok.
func TestBehaviorDayIndex(t *testing.T) {
	cases := []struct {
		unix int64
		want int64
	}{
		{0, 0},
		{86399, 0},
		{86400, 1},
		{1_700_000_000, 19675},
	}
	for _, c := range cases {
		if got := behaviorDayIndex(c.unix); got != c.want {
			t.Errorf("behaviorDayIndex(%d) = %d, want %d", c.unix, got, c.want)
		}
	}
}
