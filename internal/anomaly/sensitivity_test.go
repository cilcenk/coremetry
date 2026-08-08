// v0.9.826 — dedektör hassasiyet vidalarının regresyon testleri.
package anomaly

import (
	"math"
	"testing"

	"github.com/cilcenk/coremetry/internal/chstore"
)

// Operatörün bildirdiği vaka, ham sayılarıyla. Bir p99 operasyonu:
// medyan 1.90ms, MAD 0.657, son bucket 9.69ms. Dedektör bunu 8.0σ
// critical anomali olarak açıyordu.
//
// 8 milisaniyelik bir sıçrama hiçbir kullanıcının fark etmediği bir şey.
// Sorun sapmanın istatistiksel büyüklüğü DEĞİL — o gerçekten 8σ — sapmanın
// BİRİMİNİN önemsizliği.
const (
	caseMedian  = 1.90
	caseMAD     = 0.657
	caseCurrent = 9.69
)

// zFor — üretimdeki zincirin aynısı: effectiveMAD → modified z-score.
func zFor(metric string, median, rawMAD, current, minMAD float64) float64 {
	return madScale * (current - median) / effectiveMAD(metric, median, rawMAD, minMAD)
}

// TestOperatorCase_1p90to9p69_NoLongerOpens — PİNLİ VAKA.
//
// Testin işi iki yönlü: önce vakanın GERÇEKTEN var olduğunu (vidalar
// kapalıyken açılıyor), sonra varsayılanların onu kapattığını göstermek.
// İlkini atlarsak, test yarın vidalar sessizce etkisizleşse de yeşil
// kalabilir — "hiçbir zaman açılmıyordu" ile "artık açılmıyor" ayırt
// edilemez olurdu.
func TestOperatorCase_1p90to9p69_NoLongerOpens(t *testing.T) {
	criticalZ := defCriticalZ()

	// ── 1. VAKA GERÇEKTİ: vidalar kapalıyken açılıyor ──
	off := tunedPolicy("p99_ms", func(s *chstore.AnomalyMetricSensitivity) {
		s.MinMAD, s.AbsFloor, s.MinAbsDelta, s.MinBaselineRate = 0, 0, 0, 0
	})
	zOff := zFor("p99_ms", caseMedian, caseMAD, caseCurrent, off.minMAD)
	if math.Abs(zOff-8.0) > 0.05 {
		t.Fatalf("ham z = %.2fσ, ~8.0σ bekleniyordu — test verisi operatörün "+
			"bildirdiği vakayla eşleşmiyor, tabanlar yanlış şeye ayarlanıyor olabilir", zOff)
	}
	if d := decideAnomaly("p99_ms", zOff, caseCurrent, caseMedian, off, criticalZ); !d.open {
		t.Fatal("vidalar KAPALIYKEN vaka açılmıyor — bu test artık hiçbir şey " +
			"kanıtlamıyor; ya veri ya da dedektör bu testin altından kaydı")
	}

	// ── 2. VARSAYILANLAR ONU KAPATIYOR ──
	def := defPolicy("p99_ms")
	zDef := zFor("p99_ms", caseMedian, caseMAD, caseCurrent, def.minMAD)
	if d := decideAnomaly("p99_ms", zDef, caseCurrent, caseMedian, def, criticalZ); d.open {
		t.Errorf("PİNLİ VAKA HÂLÂ AÇILIYOR (z=%.2fσ, severity=%q).\n\n"+
			"1.90ms medyanlı bir op'un 9.69ms'e çıkması operatör için bir OLAY "+
			"değil. Varsayılanlar (minMAD=1.0ms, absFloor=10ms) bu vakayı "+
			"kaynağında susturmalıydı.", zDef, d.severity)
	}
	// z gerçekten 8.0 → 5.25'e inmeli (keşif rekonstrüksiyonu).
	if math.Abs(zDef-5.25) > 0.05 {
		t.Errorf("minMAD sonrası z = %.2fσ, ~5.25σ bekleniyordu — minMAD MAX "+
			"olarak uygulanmıyor olabilir", zDef)
	}
}

// TestEachScrewClosesTheCaseAlone — vidaların TEK TEK etkisi.
//
// Varsayılanlarda iki vida birden açık; biri sessizce bozulsa öbürü
// vakayı kapatmaya devam eder ve testler yeşil kalırdı. Her vidayı YALNIZ
// BAŞINA denemek o maskelemeyi kaldırıyor.
func TestEachScrewClosesTheCaseAlone(t *testing.T) {
	criticalZ := defCriticalZ()

	t.Run("yalnız minMAD", func(t *testing.T) {
		// absFloor KAPALI: kapatan tek şey minMAD olmalı.
		pol := tunedPolicy("p99_ms", func(s *chstore.AnomalyMetricSensitivity) {
			s.MinMAD, s.AbsFloor = 1.0, 0
		})
		z := zFor("p99_ms", caseMedian, caseMAD, caseCurrent, pol.minMAD)
		if d := decideAnomaly("p99_ms", z, caseCurrent, caseMedian, pol, criticalZ); d.open {
			t.Errorf("minMAD tek başına vakayı kapatmadı (z=%.2f) — MAX yerine "+
				"yalnız-düz-baseline dalında uygulanıyor olabilir", z)
		}
	})

	t.Run("yalnız absFloor", func(t *testing.T) {
		// minMAD KAPALI: z 8.0'da kalır, kapatan tek şey absFloor olmalı.
		pol := tunedPolicy("p99_ms", func(s *chstore.AnomalyMetricSensitivity) {
			s.MinMAD, s.AbsFloor = 0, 10.0
		})
		z := zFor("p99_ms", caseMedian, caseMAD, caseCurrent, pol.minMAD)
		if d := decideAnomaly("p99_ms", z, caseCurrent, caseMedian, pol, criticalZ); d.open {
			t.Errorf("absFloor tek başına vakayı kapatmadı (z=%.2f, current=%.2f < 10ms)", z, caseCurrent)
		}
	})

	t.Run("yalnız minAbsDelta", func(t *testing.T) {
		// |9.69-1.90| = 7.79; 8ms'lik bir taban bunu eler.
		pol := tunedPolicy("p99_ms", func(s *chstore.AnomalyMetricSensitivity) {
			s.MinMAD, s.AbsFloor, s.MinAbsDelta = 0, 0, 8.0
		})
		z := zFor("p99_ms", caseMedian, caseMAD, caseCurrent, pol.minMAD)
		if d := decideAnomaly("p99_ms", z, caseCurrent, caseMedian, pol, criticalZ); d.open {
			t.Errorf("minAbsDelta tek başına vakayı kapatmadı (|Δ|=%.2f < 8.0)", caseCurrent-caseMedian)
		}
	})

	t.Run("yalnız criticalZ", func(t *testing.T) {
		// Vidalar kapalı, z 8.0; criticalZ 9'a çekilirse verdict warning
		// olur ve P1-only kapısı açılmayı engeller.
		pol := tunedPolicy("p99_ms", func(s *chstore.AnomalyMetricSensitivity) {
			s.MinMAD, s.AbsFloor = 0, 0
		})
		z := zFor("p99_ms", caseMedian, caseMAD, caseCurrent, pol.minMAD)
		if d := decideAnomaly("p99_ms", z, caseCurrent, caseMedian, pol, 9.0); d.open {
			t.Errorf("criticalZ=9 iken 8.0σ hâlâ açıldı — global vida decideAnomaly'ye ulaşmıyor")
		}
	})
}

// TestScrewsDoNotEatRealEvents — AŞIRI SUSTURMA kontrolü.
//
// Bir hassasiyet vidasının tehlikeli kipi gürültüyü kesmesi değil, GERÇEK
// olayları da kesmesidir: operatör hiçbir şey görmez ve biz de görmeyiz.
// Her vaka, varsayılan vidaların ARDINDAN hâlâ açılmalı.
func TestScrewsDoNotEatRealEvents(t *testing.T) {
	criticalZ := defCriticalZ()
	cases := []struct {
		name                    string
		metric                  string
		median, rawMAD, current float64
	}{
		// Gerçek gecikme olayı: 200ms → 1200ms. absFloor 10ms'nin çok
		// üstünde, minMAD 1.0 sıkı baseline'da bile z'yi bastıramaz.
		{"p99 200ms → 1200ms", "p99_ms", 200, 20, 1200},
		// Sıkı ama ANLAMLI baseline: 50ms medyan, 5ms MAD, 300ms'e sıçrama.
		{"p99 50ms → 300ms", "p99_ms", 50, 5, 300},
		// v0.9.48'in vakası: %0 tabandan %30 hataya. minMAD error_rate'te
		// varsayılan 0, yani flatMADFloor hâlâ devrede.
		{"error_rate %0 → %30", "error_rate", 0, 0, 30},
		// Gerçek hata patlaması, sıfır olmayan taban.
		{"error_rate %1 → %25", "error_rate", 1, 0.2, 25},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			pol := defPolicy(c.metric)
			z := zFor(c.metric, c.median, c.rawMAD, c.current, pol.minMAD)
			d := decideAnomaly(c.metric, z, c.current, c.median, pol, criticalZ)
			if !d.open {
				t.Errorf("GERÇEK OLAY YENDİ: %s (z=%.2fσ).\n\n"+
					"Hassasiyet vidasının tehlikeli kipi gürültüyü kesmesi değil, "+
					"gerçek olayları da kesmesidir — bunu operatör de biz de fark etmeyiz.",
					c.name, z)
			}
		})
	}
	// request_rate DÜŞÜŞÜ: absFloor yalnız yükselişe uygulanır, trafik
	// kaybı bu vidalardan etkilenmemeli (kural v0.9.180'den beri böyle).
	pol := defPolicy("request_rate")
	if d := decideAnomaly("request_rate", -4.0, 5, 1000, pol, criticalZ); !d.open || d.severity != "critical" {
		t.Errorf("trafik kaybı yendi: open=%v sev=%q — absFloor düşüşlere uygulanmamalı", d.open, d.severity)
	}
}

// TestEffectiveMAD — iki tabanın SIRASI ve etkileşimi.
func TestEffectiveMAD(t *testing.T) {
	cases := []struct {
		name                   string
		metric                 string
		median, rawMAD, minMAD float64
		want                   float64
	}{
		// minMAD kapalıyken davranış v0.9.48'deki gibi kalır.
		{"düz baseline, minMAD yok → flatMADFloor", "p99_ms", 100, 0, 0, 5}, // max(1, 0.05*100)
		{"düz baseline error_rate → sabit 0.5", "error_rate", 0, 0, 0, 0.5},
		{"canlı MAD, minMAD yok → dokunulmaz", "p99_ms", 100, 20, 0, 20},
		// minMAD MAX olarak uygulanır — asıl düzeltme.
		{"canlı MAD minMAD'in ALTINDA → yükselir", "p99_ms", 1.9, 0.657, 1.0, 1.0},
		{"canlı MAD minMAD'in ÜSTÜNDE → dokunulmaz", "p99_ms", 100, 20, 1.0, 20},
		// İki taban üst üste: flatMADFloor önce, sonra minMAD.
		{"düz baseline + büyük minMAD → minMAD kazanır", "p99_ms", 1, 0, 50, 50},
		{"düz baseline + küçük minMAD → flatMADFloor kazanır", "p99_ms", 100, 0, 2, 5},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := effectiveMAD(c.metric, c.median, c.rawMAD, c.minMAD); math.Abs(got-c.want) > 1e-9 {
				t.Errorf("effectiveMAD = %v, %v bekleniyordu", got, c.want)
			}
		})
	}
}

// TestVolumeGateOnlyBlocksOpening — hacim kapısının SINIRI.
//
// Kapı çözülmeye de uygulansaydı, susan bir servisin AÇIK problemi
// kapanamaz ve ekranda sonsuza dek takılı kalırdı — v0.9.449'un
// (donmuş kuyruk) düzelttiği sınıfın aynısı geri gelirdi.
func TestVolumeGateOnlyBlocksOpening(t *testing.T) {
	// Kapı kapalı (min=0) → her zaman açık geçer.
	if !hasEnoughVolume([]float64{0.001}, 0) {
		t.Error("kapı kapalıyken hacim engellememeli")
	}
	// Hacim serisi YOK → ölçemedik → açık geç. Sessiz kapanma bu depoda
	// tekrarlayan hata sınıfı; kapı ölçemediğinde ölçmeden geçirmeli.
	if !hasEnoughVolume(nil, 5) {
		t.Error("hacim serisi yokken kapı KAPANDI — bir okuma hatası dedektörü " +
			"sessizce susturabilirdi")
	}
	if !hasEnoughVolume([]float64{}, 5) {
		t.Error("boş hacim serisinde kapı KAPANDI — aynı sessiz-kapanma riski")
	}
	// SON bucket belirleyici (baseline'ın tamamı değil): olay ANINDAKİ
	// hacim önemli.
	if hasEnoughVolume([]float64{100, 100, 0.5}, 5) {
		t.Error("son bucket eşiğin altındayken geçti — kapı olay anındaki hacme bakmalı")
	}
	if !hasEnoughVolume([]float64{0.1, 0.1, 50}, 5) {
		t.Error("son bucket eşiğin üstündeyken engellendi — geçmiş sessizlik olayı gizlememeli")
	}
	// Tam sınır: >= geçer.
	if !hasEnoughVolume([]float64{5}, 5) {
		t.Error("tam eşikte engellendi — kapı >= olmalı")
	}
}

// TestSensitivityNormalize — kelepçeler.
func TestSensitivityNormalize(t *testing.T) {
	d := chstore.DefaultAnomalySensitivity()

	t.Run("bilinmeyen metrik düşer", func(t *testing.T) {
		got := chstore.NormalizeAnomalySensitivity(chstore.AnomalySensitivityConfig{
			Metrics: map[string]chstore.AnomalyMetricSensitivity{
				"error_rate":     {FloorPct: 0.2},
				"uydurma_metrik": {FloorPct: 0.9},
			},
			DwellBuckets: 3, CriticalZ: 6,
		})
		if _, ok := got.Metrics["uydurma_metrik"]; ok {
			t.Error("bilinmeyen metrik ayarda kaldı — elle düzenlenmiş bir satır " +
				"dedektöre politikası olmayan bir ad sokabilirdi")
		}
		if got.Metrics["error_rate"].FloorPct != 0.2 {
			t.Error("kanonik metriğin operatör değeri kayboldu")
		}
	})

	t.Run("eksik metrik varsayılanını devralır", func(t *testing.T) {
		got := chstore.NormalizeAnomalySensitivity(chstore.AnomalySensitivityConfig{
			Metrics:      map[string]chstore.AnomalyMetricSensitivity{"error_rate": {FloorPct: 0.2}},
			DwellBuckets: 3, CriticalZ: 6,
		})
		if got.Metrics["p99_ms"] != d.Metrics["p99_ms"] {
			t.Errorf("eksik metrik %+v aldı, varsayılan %+v bekleniyordu — eski "+
				"settings satırları yeni metriği varsayılanıyla devralmalı",
				got.Metrics["p99_ms"], d.Metrics["p99_ms"])
		}
	})

	t.Run("negatif → varsayılan, SIFIR korunur", func(t *testing.T) {
		got := chstore.NormalizeAnomalySensitivity(chstore.AnomalySensitivityConfig{
			Metrics: map[string]chstore.AnomalyMetricSensitivity{
				// absFloor SIFIR = "vidayı kapat", meşru bir istek.
				// floorPct NEGATİF = anlamsız, varsayılana dönmeli.
				"p99_ms": {FloorPct: -1, AbsFloor: 0, MinMAD: 2},
			},
			DwellBuckets: 3, CriticalZ: 6,
		})
		p := got.Metrics["p99_ms"]
		if p.FloorPct != d.Metrics["p99_ms"].FloorPct {
			t.Errorf("negatif floorPct varsayılana dönmedi: %v", p.FloorPct)
		}
		if p.AbsFloor != 0 {
			t.Errorf("operatörün açıkça yazdığı 0 ezildi (%v) — vidayı kapatmanın "+
				"yolu bu ve 'boş bıraktım' ile karıştırılmamalı", p.AbsFloor)
		}
		if p.MinMAD != 2 {
			t.Errorf("geçerli operatör değeri kayboldu: %v", p.MinMAD)
		}
	})

	t.Run("NaN/Inf → varsayılan", func(t *testing.T) {
		got := chstore.NormalizeAnomalySensitivity(chstore.AnomalySensitivityConfig{
			Metrics: map[string]chstore.AnomalyMetricSensitivity{
				"p99_ms": {FloorPct: math.NaN(), AbsFloor: math.Inf(1)},
			},
			DwellBuckets: 3, CriticalZ: 6,
		})
		p := got.Metrics["p99_ms"]
		if math.IsNaN(p.FloorPct) || math.IsInf(p.AbsFloor, 0) {
			t.Errorf("NaN/Inf ayara sızdı: %+v — z hesabı sessizce NaN üretir ve "+
				"dedektör hiçbir şey açmaz", p)
		}
	})

	t.Run("aralık dışı global vidalar", func(t *testing.T) {
		for _, c := range []struct {
			name  string
			in    chstore.AnomalySensitivityConfig
			wantD int
			wantZ float64
		}{
			{"sıfır dwell", chstore.AnomalySensitivityConfig{DwellBuckets: 0, CriticalZ: 6}, d.DwellBuckets, 6},
			{"negatif dwell", chstore.AnomalySensitivityConfig{DwellBuckets: -3, CriticalZ: 6}, d.DwellBuckets, 6},
			{"absürt dwell", chstore.AnomalySensitivityConfig{DwellBuckets: 5000, CriticalZ: 6}, d.DwellBuckets, 6},
			{"sıfır criticalZ", chstore.AnomalySensitivityConfig{DwellBuckets: 3, CriticalZ: 0}, 3, d.CriticalZ},
			{"absürt criticalZ", chstore.AnomalySensitivityConfig{DwellBuckets: 3, CriticalZ: 1e9}, 3, d.CriticalZ},
		} {
			got := chstore.NormalizeAnomalySensitivity(c.in)
			if got.DwellBuckets != c.wantD || got.CriticalZ != c.wantZ {
				t.Errorf("%s → dwell=%d z=%.1f; %d / %.1f bekleniyordu — kelepçesiz "+
					"bir değer dedektörü sessizce durdurabilirdi",
					c.name, got.DwellBuckets, got.CriticalZ, c.wantD, c.wantZ)
			}
		}
	})
}

// TestDefaultsMatchShippedBehaviour — varsayılanlar = bugünkü davranış
// ARTI iki cerrahi düzeltme. Başka hiçbir metrik değişmemeli; bu sürüm
// bir AYAR sürümü, bir davranış sürümü değil.
func TestDefaultsMatchShippedBehaviour(t *testing.T) {
	d := chstore.DefaultAnomalySensitivity()

	// v0.9.180'den beri gemide olan error_rate değerleri.
	if er := d.Metrics["error_rate"]; er.FloorPct != 0.10 || er.AbsFloor != 1.0 {
		t.Errorf("error_rate varsayılanı %+v — v0.9.180'in gemideki değerleri "+
			"{floorPct 0.10, absFloor 1.0}", er)
	}
	// request_rate aynen.
	if rr := d.Metrics["request_rate"]; rr.FloorPct != 0.15 || rr.AbsFloor != 0 {
		t.Errorf("request_rate varsayılanı %+v — {floorPct 0.15, absFloor 0} olmalı", rr)
	}
	// İKİ CERRAHİ DÜZELTME.
	if p := d.Metrics["p99_ms"]; p.MinMAD != 1.0 || p.AbsFloor != 10.0 {
		t.Errorf("p99_ms varsayılanı %+v — operatör vakasını kapatan iki vida "+
			"{minMAD 1.0ms, absFloor 10ms} olmalı", p)
	}
	// Yeni vidalar KAPALI: davranış değişmemeli.
	for _, m := range chstore.AnomalySensitivityMetrics {
		if s := d.Metrics[m]; s.MinAbsDelta != 0 || s.MinBaselineRate != 0 {
			t.Errorf("%s: yeni vidalar varsayılanda AÇIK (%+v) — bu sürüm "+
				"davranışı sessizce değiştirirdi", m, s)
		}
	}
	// Globaller v0.8.220 / v0.9.193 ile aynı.
	if d.DwellBuckets != 3 || d.CriticalZ != 6.0 {
		t.Errorf("global varsayılanlar dwell=%d criticalZ=%.1f — 3 / 6.0 olmalı",
			d.DwellBuckets, d.CriticalZ)
	}
}

// TestVolumeRidesTheSameQuery — MALİYET sözleşmesi.
//
// Hacim kapısı için AYRI bir sorgu açmak, v0.8.507'nin toparladığı N+1
// maliyetini metrik başına geri açardı. Ek kolon aynı granülleri okur;
// ayrı sorgu tam pencereyi bir kez daha tarar.
func TestVolumeRidesTheSameQuery(t *testing.T) {
	q := buildAllBucketsQuery("countMerge(error_count_state) AS x")
	if !containsAll(q, "countMerge(span_count_state) / 300.0", "AS rate") {
		t.Error("hacim kolonu değer sorgusunda yok — ayrı bir okuma açılıyor olabilir")
	}
	// Bölme request_rate ifadesiyle AYNI olmalı: ayar sayfası "istek/sn"
	// diyor ve operatörün /metrics'te gördüğü sayıyla aynı şey olmalı.
	rr, err := metricValueExpr("request_rate")
	if err != nil {
		t.Fatalf("request_rate ifadesi okunamadı: %v", err)
	}
	if rr != "countMerge(span_count_state) / 300.0" {
		t.Errorf("request_rate ifadesi %q — hacim kolonunun bölmesiyle ayrıştı; "+
			"ayar sayfasındaki 'istek/sn' artık başka bir şey ölçüyor", rr)
	}
	// Sınırlar korunmalı (CH okuma disiplini).
	if !containsAll(q, "LIMIT 1000 BY service_name", "max_execution_time") {
		t.Error("sorgu sınırları kayboldu")
	}
}

func containsAll(s string, subs ...string) bool {
	for _, sub := range subs {
		found := false
		for i := 0; i+len(sub) <= len(s); i++ {
			if s[i:i+len(sub)] == sub {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	return true
}
