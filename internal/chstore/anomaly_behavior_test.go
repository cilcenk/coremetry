// Davranış motoru AŞAMA 1 (v0.9.935) — ayar blob'unun kelepçe testleri.
//
// Bu bölümün kelepçe duruşu ani-sapma alanlarından FARKLI ve fark
// bilinçli: orada 0 meşrudur ("vidayı kapat"), burada değildir.
// seasonalZ=0 ya da dwell=0 motoru KAPATMAZ, sonuna kadar AÇAR — her
// bucket aday olur. Elle düzenlenmiş bir settings satırının ya da eksik
// bir alanın filoyu anomali fabrikasına çevirmesi mümkün olmamalı.
package chstore

import (
	"encoding/json"
	"math"
	"strings"
	"testing"
)

// TestDefaultAnomalyBehavior — spec'te onaylanan varsayılanlar
// (2026-08-10). Sayılar burada pinli çünkü üçü de operatörün
// göreceği ilk davranışı belirliyor.
func TestDefaultAnomalyBehavior(t *testing.T) {
	d := DefaultAnomalyBehavior()
	if !d.IsEnabled() {
		t.Error("varsayılan KAPALI — motor gemiye sessiz iner ve kimse fark etmez")
	}
	if d.SeasonalZ != 4.0 {
		t.Errorf("SeasonalZ = %v, want 4.0", d.SeasonalZ)
	}
	if d.RegimeRatio != 1.5 {
		t.Errorf("RegimeRatio = %v, want 1.5", d.RegimeRatio)
	}
	if d.DwellSeasonal != 3 || d.DwellRegime != 6 {
		t.Errorf("dwell = %d/%d, want 3/6 (15 dk / 30 dk)", d.DwellSeasonal, d.DwellRegime)
	}
	if d.MaxCandidatesPerTick != 50 {
		t.Errorf("MaxCandidatesPerTick = %d, want 50", d.MaxCandidatesPerTick)
	}
	// Rejim dwell'i mevsimselden BÜYÜK olmalı: "kalıcı" ifadesi
	// "sapıyor" ifadesinden daha uzun bir gözlem gerektirir, yoksa iki
	// sinyalin anlamı çakışır ve rejim mevsimseli hep bastırır.
	if d.DwellRegime <= d.DwellSeasonal {
		t.Error("rejim dwell'i mevsimselden küçük/eşit — iki sinyalin anlamı çakışır")
	}
}

// TestNormalizeAnomalyBehavior — kelepçe tablosu. HER alanın aralık
// dışı hâli varsayılanına dönmeli; sıfır DAHİL (bu bölümde 0 meşru
// değil, gerekçe dosya başlığında).
func TestNormalizeAnomalyBehavior(t *testing.T) {
	d := DefaultAnomalyBehavior()
	cases := []struct {
		name string
		in   AnomalyBehaviorConfig
		want AnomalyBehaviorConfig
	}{
		{"tamamen boş → varsayılanlar", AnomalyBehaviorConfig{}, d},
		{
			"geçerli değerler korunur",
			AnomalyBehaviorConfig{SeasonalZ: 5.5, RegimeRatio: 2, DwellSeasonal: 4, DwellRegime: 12, MaxCandidatesPerTick: 200},
			AnomalyBehaviorConfig{Enabled: boolPtr(true), SeasonalZ: 5.5, RegimeRatio: 2, DwellSeasonal: 4, DwellRegime: 12, MaxCandidatesPerTick: 200},
		},
		{
			"sıfırlar varsayılana döner (0 = motoru AÇMAK olurdu)",
			AnomalyBehaviorConfig{SeasonalZ: 0, RegimeRatio: 0, DwellSeasonal: 0, DwellRegime: 0, MaxCandidatesPerTick: 0},
			d,
		},
		{
			"negatifler varsayılana döner",
			AnomalyBehaviorConfig{SeasonalZ: -3, RegimeRatio: -1, DwellSeasonal: -5, DwellRegime: -1, MaxCandidatesPerTick: -10},
			d,
		},
		{
			"üst sınır aşımı varsayılana döner",
			AnomalyBehaviorConfig{SeasonalZ: 500, RegimeRatio: 1e6, DwellSeasonal: 999, DwellRegime: 100, MaxCandidatesPerTick: 1 << 20},
			d,
		},
		{
			"regimeRatio 1.0 reddedilir — 'her kıpırtı rejim kayması' demek",
			AnomalyBehaviorConfig{SeasonalZ: 4, RegimeRatio: 1.0, DwellSeasonal: 3, DwellRegime: 6, MaxCandidatesPerTick: 50},
			d,
		},
		{
			"NaN / Inf (elle düzenlenmiş JSON) varsayılana döner",
			AnomalyBehaviorConfig{SeasonalZ: math.NaN(), RegimeRatio: math.Inf(1), DwellSeasonal: 3, DwellRegime: 6, MaxCandidatesPerTick: 50},
			AnomalyBehaviorConfig{Enabled: boolPtr(true), SeasonalZ: d.SeasonalZ, RegimeRatio: d.RegimeRatio, DwellSeasonal: 3, DwellRegime: 6, MaxCandidatesPerTick: 50},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := NormalizeAnomalyBehavior(c.in)
			if got.IsEnabled() != c.want.IsEnabled() {
				t.Errorf("Enabled = %v, want %v", got.IsEnabled(), c.want.IsEnabled())
			}
			if got.SeasonalZ != c.want.SeasonalZ || got.RegimeRatio != c.want.RegimeRatio ||
				got.DwellSeasonal != c.want.DwellSeasonal || got.DwellRegime != c.want.DwellRegime ||
				got.MaxCandidatesPerTick != c.want.MaxCandidatesPerTick {
				t.Errorf("got %+v, want %+v", got, c.want)
			}
		})
	}
}

// TestNormalizeAnomalyBehaviorRespectsDisable — operatörün KAPATMA
// kararı ezilmemeli. AttachToIncident'la aynı tuzak: *bool olmasaydı
// bu satır ayırt edilemezdi.
func TestNormalizeAnomalyBehaviorRespectsDisable(t *testing.T) {
	off := false
	got := NormalizeAnomalyBehavior(AnomalyBehaviorConfig{Enabled: &off, SeasonalZ: 4, RegimeRatio: 1.5, DwellSeasonal: 3, DwellRegime: 6, MaxCandidatesPerTick: 50})
	if got.IsEnabled() {
		t.Error("Normalize operatörün kapatma kararını ezdi")
	}
	// Normalize SOMUTLAŞTIRMALI: nil bayrak açık hâliyle yazılır ki
	// kaydedilen blob ne olduğunu açıkça söylesin.
	if NormalizeAnomalyBehavior(AnomalyBehaviorConfig{}).Enabled == nil {
		t.Error("Normalize bayrağı nil bıraktı — kaydedilen blob kendini anlatmıyor")
	}
}

// TestAnomalySensitivityCarriesBehavior — bölüm ÜST blob'un normalize
// yolundan geçmeli. Geçmezse admin PUT'u ham (kelepçesiz) değerleri
// kaydeder ve dedektör onlarla koşar.
func TestAnomalySensitivityCarriesBehavior(t *testing.T) {
	if !DefaultAnomalySensitivity().Behavior.IsEnabled() {
		t.Error("varsayılan blob davranış motorunu kapalı getiriyor")
	}
	// Bu sürümden ESKİ bir settings satırı: behavior alanı hiç yok.
	var legacy AnomalySensitivityConfig
	if err := json.Unmarshal([]byte(`{"dwellBuckets":3,"criticalZ":6,"metrics":{}}`), &legacy); err != nil {
		t.Fatal(err)
	}
	n := NormalizeAnomalySensitivity(legacy)
	if n.Behavior.SeasonalZ != DefaultAnomalyBehavior().SeasonalZ {
		t.Errorf("eski satır davranış varsayılanlarını devralmadı: %+v", n.Behavior)
	}
	if !n.Behavior.IsEnabled() {
		t.Error("eski satırda motor kapalı geldi — yokluk davranış değiştirdi")
	}

	// Aralık dışı bir PUT üst normalize'dan geçince kelepçelenmeli.
	bad := AnomalySensitivityConfig{Behavior: AnomalyBehaviorConfig{SeasonalZ: -1, RegimeRatio: 1.0, DwellRegime: 999}}
	nb := NormalizeAnomalySensitivity(bad).Behavior
	d := DefaultAnomalyBehavior()
	if nb.SeasonalZ != d.SeasonalZ || nb.RegimeRatio != d.RegimeRatio || nb.DwellRegime != d.DwellRegime {
		t.Errorf("üst normalize davranış kelepçesini uygulamadı: %+v", nb)
	}
}

// TestAnomalyBehaviorRoundTrip — kaydet → oku aynı ayarı vermeli.
// JSON etiketleri frontend'in okuduğu sözleşme; bir alan adı sessizce
// değişirse ayar sayfası boş kutu gösterir ve operatör kaydettiğinde
// varsayılana döndürür.
func TestAnomalyBehaviorRoundTrip(t *testing.T) {
	in := NormalizeAnomalySensitivity(AnomalySensitivityConfig{
		DwellBuckets: 3, CriticalZ: 6,
		Behavior: AnomalyBehaviorConfig{SeasonalZ: 5, RegimeRatio: 2.5, DwellSeasonal: 2, DwellRegime: 8, MaxCandidatesPerTick: 120},
	})
	raw, err := json.Marshal(in)
	if err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{`"behavior"`, `"seasonalZ"`, `"regimeRatio"`, `"dwellSeasonal"`, `"dwellRegime"`, `"maxCandidatesPerTick"`, `"enabled"`} {
		if !strings.Contains(string(raw), field) {
			t.Errorf("JSON'da %s alanı yok — frontend sözleşmesi kırık: %s", field, raw)
		}
	}
	var back AnomalySensitivityConfig
	if err := json.Unmarshal(raw, &back); err != nil {
		t.Fatal(err)
	}
	got := NormalizeAnomalySensitivity(back).Behavior
	if got.SeasonalZ != 5 || got.RegimeRatio != 2.5 || got.DwellSeasonal != 2 ||
		got.DwellRegime != 8 || got.MaxCandidatesPerTick != 120 {
		t.Errorf("round-trip ayarı değiştirdi: %+v", got)
	}
}

// TestNormalizeBehaviorScarcityKnobs — v0.9.957'nin iki yeni vidası.
//
// AYRI TEST çünkü ikisinin kelepçe DURUŞU üstteki alanlardan farklı:
// üstte 0 "motoru sonuna kadar aç" demek ve reddediliyor; burada 0
// "kapıyı kaldır" demek ve yine reddediliyor, ama üst sınır da anlamlı
// — MinBucketRepeats pencerede FİİLEN mümkün olandan (28/7 = 4) büyük
// olursa motor KALICI olarak susar ve bunu hiçbir ekran açıklayamaz.
func TestNormalizeBehaviorScarcityKnobs(t *testing.T) {
	d := DefaultAnomalyBehavior()
	cases := []struct {
		name        string
		inSamples   int
		inRepeats   int
		wantSamples int
		wantRepeats int
	}{
		{"boş → varsayılan", 0, 0, d.MinSamplesPerBucket, d.MinBucketRepeats},
		{"geçerli korunur", 24, 4, 24, 4},
		{"negatif → varsayılan", -5, -1, d.MinSamplesPerBucket, d.MinBucketRepeats},
		{
			// 4'ün üstü = "hiç açma": 28 günlük pencerede bir haftanın-saati
			// en fazla 4 kez tekrar eder.
			"tekrar penceresi aşıyor → varsayılan", 24, 5, 24, d.MinBucketRepeats,
		},
		{"örnek üst sınırı aşıyor → varsayılan", 100000, 3, d.MinSamplesPerBucket, 3},
		{"alt sınır 1 meşru", 1, 1, 1, 1},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := NormalizeAnomalyBehavior(AnomalyBehaviorConfig{
				SeasonalZ: 4, RegimeRatio: 1.5, DwellSeasonal: 3, DwellRegime: 6,
				MaxCandidatesPerTick: 50,
				MinSamplesPerBucket:  c.inSamples,
				MinBucketRepeats:     c.inRepeats,
			})
			if got.MinSamplesPerBucket != c.wantSamples {
				t.Errorf("MinSamplesPerBucket = %d, want %d", got.MinSamplesPerBucket, c.wantSamples)
			}
			if got.MinBucketRepeats != c.wantRepeats {
				t.Errorf("MinBucketRepeats = %d, want %d", got.MinBucketRepeats, c.wantRepeats)
			}
		})
	}
}

// TestBehaviorScarcityDefaultsAreMeasured — varsayılanlar PİNLİ.
//
// 12: v0.9.935'in koddaki sabitinin aynısı (vidalaşırken davranış
// değişmedi). 3: ÖLÇÜLMÜŞ kapı — lokal küme 9 günlük geçmişle her
// kovada 24 örnek / 2 GÜN taşıyordu, 12'lik örnek kapısını rahat
// geçiyordu ve dejenere MAD tek tikte 178 aday üretmişti. Bu sayıyı
// 2'ye düşürmek o fırtınayı geri getirir.
func TestBehaviorScarcityDefaultsAreMeasured(t *testing.T) {
	d := DefaultAnomalyBehavior()
	if d.MinSamplesPerBucket != 12 {
		t.Errorf("MinSamplesPerBucket = %d, want 12 (v0.9.935 sabitinin aynısı)", d.MinSamplesPerBucket)
	}
	if d.MinBucketRepeats != 3 {
		t.Errorf("MinBucketRepeats = %d, want 3 (ölçülmüş kapı — 2 fırtınayı geri getirir)", d.MinBucketRepeats)
	}
}
