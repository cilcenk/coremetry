// Davranış motoru AŞAMA 1 (v0.9.935) — saf çekirdeğin tablo testleri.
//
// Bu motorun tamamı iki şeye dayanıyor: (a) Go ve SQL tarafının AYNI
// haftanın-saati kovasını üretmesi, (b) "kalıcı kayma" ile "geçici
// sıçrama"nın ayrılması. İkisi de canlı CH olmadan test edilebilir ve
// ikisi de sessizce bozulabilir — v0.8.323 (TZ kayması: Go yerel, SQL
// sunucu varsayılanı → gündüz geçmişi gece "şimdi"siyle kıyaslanıyordu)
// bu depoda zaten bir kez oldu.
package anomaly

import (
	"encoding/json"
	"math"
	"strings"
	"testing"
	"time"

	"github.com/cilcenk/coremetry/internal/chstore"
)

// ── Kova hesabı ─────────────────────────────────────────────────────

// TestHourOfWeek — yedi günün tamamı + gün/hafta sınırları. SQL ikizi
// ((toDayOfWeek(tb,0,'UTC') - 1) * 24 + toHour(tb,'UTC')) mode 0'da
// 1=Pzt … 7=Paz verir; Go'nun Weekday()'i Sunday=0'dır, yani kaydırma
// yanlış yapılırsa pazar günü 168 saat kayar ve baseline SESSİZCE
// başka bir güne bakar.
func TestHourOfWeek(t *testing.T) {
	// 2026-08-10 pazartesi.
	cases := []struct {
		name string
		ts   time.Time
		want int
	}{
		{"pazartesi 00:00 → 0", time.Date(2026, 8, 10, 0, 0, 0, 0, time.UTC), 0},
		{"pazartesi 10:35 → 10", time.Date(2026, 8, 10, 10, 35, 0, 0, time.UTC), 10},
		{"pazartesi 23:59 → 23", time.Date(2026, 8, 10, 23, 59, 59, 0, time.UTC), 23},
		{"salı 00:00 → 24", time.Date(2026, 8, 11, 0, 0, 0, 0, time.UTC), 24},
		{"çarşamba 09:00 → 57", time.Date(2026, 8, 12, 9, 0, 0, 0, time.UTC), 2*24 + 9},
		{"perşembe 00:00 → 72", time.Date(2026, 8, 13, 0, 0, 0, 0, time.UTC), 72},
		{"cuma 17:00 → 113", time.Date(2026, 8, 14, 17, 0, 0, 0, time.UTC), 4*24 + 17},
		{"cumartesi 00:00 → 120", time.Date(2026, 8, 15, 0, 0, 0, 0, time.UTC), 120},
		{"pazar 23:00 → 167 (haftanın son kovası)", time.Date(2026, 8, 16, 23, 0, 0, 0, time.UTC), 167},
		{"pazar 00:00 → 144", time.Date(2026, 8, 16, 0, 0, 0, 0, time.UTC), 144},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := hourOfWeek(c.ts); got != c.want {
				t.Errorf("hourOfWeek(%s) = %d, want %d", c.ts.Format(time.RFC3339), got, c.want)
			}
		})
	}
}

// TestHourOfWeekIsUTCPinned — kova YEREL saatten değil UTC'den çıkmalı.
// v0.8.323'ün dersi: Go tarafı yerel saatte, SQL 'UTC' pinli olursa iki
// taraf farklı kovayı eşleştirir. Aynı ANI farklı konumlarda ifade edip
// aynı kovayı bekliyoruz.
func TestHourOfWeekIsUTCPinned(t *testing.T) {
	utc := time.Date(2026, 8, 10, 10, 30, 0, 0, time.UTC)
	ist := time.FixedZone("+03", 3*3600)
	local := utc.In(ist) // aynı an, 13:30 yerel
	if hourOfWeek(utc) != hourOfWeek(local) {
		t.Fatalf("aynı an iki konumda farklı kova: UTC=%d yerel=%d — "+
			"SQL 'UTC' pinli olduğu için baseline yanlış saate bakardı",
			hourOfWeek(utc), hourOfWeek(local))
	}
	if got := hourOfWeek(local); got != 10 {
		t.Errorf("yerel saatten türetilmiş: got %d, want 10 (UTC saati)", got)
	}
}

// TestHourOfWeekDSTSafe — yaz saati geçişi kovayı KAYDIRMAMALI.
// UTC'de DST yok; bu test o garantiyi pinliyor, çünkü biri ileride
// time.Local'a geçerse (ya da UTC() çağrısını düşürürse) 26 Ekim gibi
// bir tarihte kova bir saat kayar ve baseline bir daha hiç hizalanmaz.
func TestHourOfWeekDSTSafe(t *testing.T) {
	// Avrupa DST sonu 2026-10-25 (pazar) 01:00 UTC.
	before := time.Date(2026, 10, 25, 0, 30, 0, 0, time.UTC)
	after := time.Date(2026, 10, 25, 2, 30, 0, 0, time.UTC)
	if got, want := hourOfWeek(before), 144+0; got != want {
		t.Errorf("DST öncesi kova = %d, want %d", got, want)
	}
	if got, want := hourOfWeek(after), 144+2; got != want {
		t.Errorf("DST sonrası kova = %d, want %d — UTC'de kayma OLMAMALI", got, want)
	}
}

// TestHourOfWeekDistance — DAİRESEL uzaklık. Pazar 23:00 (167) ile
// pazartesi 00:00 (0) KOMŞUDUR; düz fark 167 der ve komşu penceresi
// hafta sınırında sessizce boşalır.
func TestHourOfWeekDistance(t *testing.T) {
	cases := []struct{ a, b, want int }{
		{10, 10, 0},
		{10, 11, 1},
		{11, 10, 1},
		{167, 0, 1},   // hafta sarması
		{0, 167, 1},   // simetrik
		{166, 1, 3},   // sarmanın ötesi
		{0, 84, 84},   // en uzak
		{0, 85, 83},   // 84'ü geçince kısa yol diğer taraf
		{144, 143, 1}, // cumartesi 23 ↔ pazar 00
	}
	for _, c := range cases {
		if got := hourOfWeekDistance(c.a, c.b); got != c.want {
			t.Errorf("hourOfWeekDistance(%d,%d) = %d, want %d", c.a, c.b, got, c.want)
		}
	}
}

// ── Metrik türetimi ─────────────────────────────────────────────────

// TestBehaviorMetricValue — üç metriğin tanımı, ani-sapma dedektörünün
// SQL ifadeleriyle (metricValueExpr) BİREBİR aynı olmalı. Ayrışırlarsa
// operatör /anomalies'te aynı seriye iki farklı sayı diyen iki satır
// görür ve hangisine güveneceğini bilemez.
func TestBehaviorMetricValue(t *testing.T) {
	cases := []struct {
		name   string
		metric string
		row    behaviorRow
		want   float64
	}{
		{"error_rate yüzde", "error_rate", behaviorRow{Spans: 1000, Errs: 25}, 2.5},
		{"error_rate payda sıfır → 0 (SQL nullIf karşılığı)", "error_rate", behaviorRow{Spans: 0, Errs: 0}, 0},
		{"error_rate tam hata", "error_rate", behaviorRow{Spans: 10, Errs: 10}, 100},
		{"request_rate = span/300", "request_rate", behaviorRow{Spans: 3000}, 10},
		{"request_rate sessizlik", "request_rate", behaviorRow{Spans: 0}, 0},
		{"p99 doğrudan", "p99_ms", behaviorRow{P99Ms: 187.25}, 187.25},
		{"bilinmeyen metrik → 0", "cpu", behaviorRow{Spans: 100}, 0},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := behaviorMetricValue(c.metric, c.row); math.Abs(got-c.want) > 1e-9 {
				t.Errorf("behaviorMetricValue(%q) = %v, want %v", c.metric, got, c.want)
			}
		})
	}
}

// TestBehaviorMetricValueMatchesDetectorSQL — türetimlerin SQL ikizinin
// HÂLÂ orada olduğunu pinler. metricValueExpr değişirse (ör. p99
// indeksinin [3]'ten [2]'ye kayması) bu test düşer ve iki motorun
// ayrışması derleme değil TEST hatası olur.
func TestBehaviorMetricValueMatchesDetectorSQL(t *testing.T) {
	for _, m := range behaviorMetrics {
		expr, err := metricValueExpr(m)
		if err != nil {
			t.Fatalf("davranış motoru %q ölçüyor ama ani-sapma dedektörünün "+
				"SQL ifadesi yok — iki motor aynı metriği farklı tanımlıyor: %v", m, err)
		}
		switch m {
		case "request_rate":
			if !strings.Contains(expr, "300") {
				t.Errorf("request_rate ifadesi 300'e bölmüyor: %s", expr)
			}
		case "p99_ms":
			if !strings.Contains(expr, "[3]") || !strings.Contains(expr, "1e6") {
				t.Errorf("p99 ifadesi üçüncü quantile / 1e6 değil: %s", expr)
			}
		}
	}
}

// ── SQL şekli (pin) ─────────────────────────────────────────────────

// TestBehaviorQueryShape — hard-constraint pini. Bu sorgu 28 GÜNLÜK bir
// filo taraması; sınırlarından biri düşerse prod'da CH'yi tek başına
// meşgul eder.
func TestBehaviorQueryShape(t *testing.T) {
	q := buildBehaviorBucketsQuery()

	if !strings.Contains(q, "FROM service_summary_5m") {
		t.Error("MV okumuyor — MV-first invariant (#3) ihlali")
	}
	if strings.Contains(q, "FROM spans") {
		t.Error("HAM SPANS okuyor — 28 günlük filo taraması milyar satır demek")
	}
	if !strings.Contains(q, "SETTINGS max_execution_time") {
		t.Error("max_execution_time yok — hard constraint")
	}
	if !strings.Contains(q, "LIMIT") {
		t.Error("LIMIT yok — hard constraint")
	}
	if !strings.Contains(q, "LIMIT 600 BY service_name") {
		t.Error("servis başına tavan yok — tek gürültülü servis tüm bütçeyi yer")
	}
	if !strings.Contains(q, "time_bucket >= ?") || !strings.Contains(q, "time_bucket < ?") {
		t.Error("zaman-sınırlı WHERE yok — partition budaması olmaz")
	}
	if !strings.Contains(q, "GROUP BY service_name") {
		t.Error("servis başına sorgu şekli — v0.8.507'nin topladığı N+1 geri gelmiş")
	}
	if strings.Contains(q, "service_name = ?") {
		t.Error("servis filtresi var — tik başına 1400 sorgu demek")
	}
	// UTC pini: iki taraf aynı saati konuşmalı (v0.8.323).
	if strings.Count(q, "'UTC'") < 2 {
		t.Error("toDayOfWeek/toHour UTC pinli değil — CH sunucusunun saat "+
			"dilimi Go tarafından farklıysa baseline yanlış kovaya bakar")
	}
	// Dairesel kova eşleşmesi (hafta sarması).
	if !strings.Contains(q, "least(abs(") || !strings.Contains(q, "168 - abs(") {
		t.Error("dairesel kova uzaklığı yok — pazar 23:00 ile pazartesi 00:00 komşu sayılmaz")
	}
	// Son-pencere dalı: "şimdi" AYNI sorgudan gelmeli, ikinci sorgu açılmamalı.
	if !strings.Contains(q, "OR time_bucket >= ?") {
		t.Error("son-pencere dalı yok — 'şimdi' için ikinci bir tarama gerekirdi")
	}
	// Bağ sayısı: cutoff, upper, kova, kova, yarıçap, son-pencere = 6.
	if n := strings.Count(q, "?"); n != 6 {
		t.Errorf("beklenen 6 bağ, %d bulundu — çağıranla sıra bozulmuş olabilir", n)
	}
}

// TestBehaviorPerServiceCapCannotBite — servis başına LIMIT'in
// ISIRAMAYACAĞININ kanıtı. Isırsaydı ORDER BY t nedeniyle EN YENİ
// bucket'lar düşerdi, yani sessizce "şimdi"yi kaybederdik: motor
// baseline'a sahip ama pencereyi kaybetmiş olurdu ve hiç sinyal
// üretmezdi. Bu, log'da hiçbir iz bırakmayan bir sessiz kapanma olurdu.
func TestBehaviorPerServiceCapCannotBite(t *testing.T) {
	buckets := 12 // saat başına 5-dk bucket
	occurrences := behaviorBaselineDays / 7
	howBuckets := 2*behaviorNeighborHours + 1
	maxBaseline := buckets * occurrences * howBuckets
	maxRecent := behaviorRecentHours * buckets
	worst := maxBaseline + maxRecent
	const cap = 600 // buildBehaviorBucketsQuery'deki LIMIT … BY service_name
	if worst >= cap {
		t.Fatalf("servis başına en kötü satır sayısı %d, tavan %d — tavan ISIRIR "+
			"ve en yeni bucket'lar düşer", worst, cap)
	}
	if !strings.Contains(buildBehaviorBucketsQuery(), "LIMIT 600 BY service_name") {
		t.Fatal("tavan değişmiş ama bu test güncellenmemiş — hesabı yeniden yap")
	}
}

// ── Seri ayrımı ─────────────────────────────────────────────────────

// TestSplitBehaviorSeries — kesim noktası. Bugünün sapması kendi
// baseline'ına KARIŞMAMALI, yoksa motor kendi bulduğu şeyi
// normalleştirir (6 saattir süren bir kayma medyanı kendine çeker).
func TestSplitBehaviorSeries(t *testing.T) {
	cut := int64(1_000_000)
	rows := []behaviorRow{
		{Unix: cut - 600, HOW: 10, Spans: 3000, Errs: 30},
		{Unix: cut - 300, HOW: 10, Spans: 3000, Errs: 30},
		{Unix: cut, HOW: 10, Spans: 3000, Errs: 300},     // kesim DAHİL → pencere
		{Unix: cut + 300, HOW: 10, Spans: 3000, Errs: 300},
	}
	baseline, recent := splitBehaviorSeries(rows, "error_rate", cut)
	if len(recent) != 2 {
		t.Fatalf("pencere %d satır, want 2 (kesim dahil)", len(recent))
	}
	if got := len(baseline[10].Values); got != 2 {
		t.Fatalf("baseline %d örnek, want 2", got)
	}
	for _, v := range baseline[10].Values {
		if v > 5 {
			t.Errorf("sapma baseline'a sızmış (%.1f%%) — motor kendi bulduğunu normalleştirir", v)
		}
	}
}

// ── Karar mantığı ───────────────────────────────────────────────────

// baselineFor — n örnekli, `val` merkezli, hafif gürültülü bir kova.
// Gürültü ŞART: sıfır MAD'de effectiveMAD taban devreye girer ve test
// gerçek dünyayı temsil etmez.
//
// Repeats VARSAYILAN OLARAK DOLU (v0.9.957): bu yardımcıyı kullanan
// testlerin konusu kıtlık kapısı DEĞİL, karar mantığı. Kıtlığı sınayan
// testler baselineWithRepeats kullanır.
func baselineFor(how, n int, val float64) map[int]behaviorBucket {
	return baselineWithRepeats(how, n, val, behaviorTestFullRepeats)
}

// behaviorTestFullRepeats — 28 günlük pencerenin gerçek tekrar sayısı
// (28/7). "Geçmişi dolu kurulum" demek.
const behaviorTestFullRepeats = 4

// baselineWithRepeats — n örnek, `repeats` FARKLI günden geldi.
// v0.9.957'nin kıtlık kapısı iki sayıya birden bakıyor; testlerin
// ikisini de ayrı ayrı kıstırabilmesi gerek.
func baselineWithRepeats(how, n int, val float64, repeats int) map[int]behaviorBucket {
	out := map[int]behaviorBucket{}
	b := behaviorBucket{Repeats: repeats}
	for i := 0; i < n; i++ {
		delta := float64(i%5-2) * 0.02 * val
		b.Values = append(b.Values, val+delta)
	}
	out[how] = b
	return out
}

// rowsAt — HOW kovasında, artan zamanlı `n` bucket; hepsi aynı değerde.
func rowsAt(how, n int, spans, errs uint64, p99 float64) []behaviorRow {
	out := make([]behaviorRow, 0, n)
	for i := 0; i < n; i++ {
		out = append(out, behaviorRow{
			Unix: int64(1_700_000_000 + i*300), HOW: how,
			Spans: spans, Errs: errs, P99Ms: p99,
		})
	}
	return out
}

func p99Policy() metricPolicy {
	return policyFor("p99_ms", chstore.DefaultAnomalySensitivity())
}

func behaviorDefaults() chstore.AnomalyBehaviorConfig {
	return chstore.DefaultAnomalyBehavior()
}

// TestBehaviorNoBaselineNoSignal — DÜRÜSTLÜK. Yeni bir servisin (ya da
// seyrek bir kovanın) baseline'ı yoktur; "veri yok" bir anomali
// değildir. Bu depoda tekrarlayan hata sınıfı, boş bir kümeden emin bir
// cevap üretmek.
func TestBehaviorNoBaselineNoSignal(t *testing.T) {
	cfg := behaviorDefaults()
	recent := rowsAt(10, 8, 3000, 0, 400) // devasa p99, baseline yok

	cases := []struct {
		name     string
		baseline map[int]behaviorBucket
	}{
		{"baseline hiç yok (yeni servis)", map[int]behaviorBucket{}},
		{"kova boş (seyrek servis)", map[int]behaviorBucket{
			11: {Values: []float64{100, 100, 100}, Repeats: 4},
		}},
		{"eşiğin bir altı örnek", baselineFor(10, cfg.MinSamplesPerBucket-1, 100)},
		// v0.9.957 — ÖLÇÜLMÜŞ vaka: örnek sayısı BOL (24) ama hepsi iki
		// günden geliyor. Eski kapı (yalnız sayı) bunu geçiriyordu ve
		// dejenere MAD tek tikte 178 aday üretmişti.
		{"örnek bol, gün çeşitliliği kıt", baselineWithRepeats(10, 24, 100, cfg.MinBucketRepeats-1)},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if _, ok := evalBehavior("svc", "p99_ms", c.baseline, recent, p99Policy(), cfg); ok {
				t.Error("baseline'sız aday üretti — uydurma baseline")
			}
		})
	}

	// Eşiğe TAM ulaşınca sinyal gelmeli, yoksa kapı fiilen sonsuza kadar
	// kapalı kalır ve motor prod'da hiç konuşmaz. İKİ eşik de sınırda.
	ok3 := baselineWithRepeats(10, cfg.MinSamplesPerBucket, 100, cfg.MinBucketRepeats)
	if _, ok := evalBehavior("svc", "p99_ms", ok3, recent, p99Policy(), cfg); !ok {
		t.Error("asgari örnek+tekrar sayısında sinyal ÜRETİLMEDİ — kapı fiilen kapalı")
	}
}

// TestBehaviorRegimeShift — kalıcı kayma. 100ms medyanlı bir servis
// 200ms'e oturuyor ve 6 dilim (30 dk) orada kalıyor → rejim adayı.
func TestBehaviorRegimeShift(t *testing.T) {
	cfg := behaviorDefaults()
	baseline := baselineFor(10, 48, 100)
	recent := rowsAt(10, 6, 3000, 0, 200)

	c, ok := evalBehavior("checkout", "p99_ms", baseline, recent, p99Policy(), cfg)
	if !ok {
		t.Fatal("2× kalıcı kayma aday üretmedi")
	}
	if c.Signal != "regime" {
		t.Errorf("Signal = %q, want regime — 6 dilim süren kayma mevsimsel değil rejimdir", c.Signal)
	}
	if c.Direction != "up" {
		t.Errorf("Direction = %q, want up", c.Direction)
	}
	if math.Abs(c.Ratio-2.0) > 0.05 {
		t.Errorf("Ratio = %.2f, want ~2.0", c.Ratio)
	}
	if c.Dwell != cfg.DwellRegime {
		t.Errorf("Dwell = %d, want %d", c.Dwell, cfg.DwellRegime)
	}
	if c.OnsetUnix != recent[0].Unix {
		t.Errorf("OnsetUnix = %d, want %d (sürekli pencerenin ilk dilimi)", c.OnsetUnix, recent[0].Unix)
	}
	if c.Spans == 0 {
		t.Error("Spans 0 — terfi kapısının hacim tabanı (MinCount) beslenmiyor")
	}
}

// TestBehaviorRegimeDrop — DÜŞÜŞ tarafı. Hacmi yarıya inen bir servis
// (1/1.5 = 0.67 eşiğinin altı) rejim kaymasıdır. Yükseliş kolu
// çalışıp düşüş kolu çalışmazsa "trafik kayboldu" sinyali sessizce
// düşer — v0.9.449'un düzelttiği sınıfın komşusu.
func TestBehaviorRegimeDrop(t *testing.T) {
	cfg := behaviorDefaults()
	baseline := baselineFor(10, 48, 10) // 10 istek/sn
	recent := rowsAt(10, 6, 900, 0, 0)  // 900/300 = 3 istek/sn

	pol := policyFor("request_rate", chstore.DefaultAnomalySensitivity())
	c, ok := evalBehavior("checkout", "request_rate", baseline, recent, pol, cfg)
	if !ok {
		t.Fatal("hacim düşüşü aday üretmedi")
	}
	if c.Direction != "down" {
		t.Errorf("Direction = %q, want down", c.Direction)
	}
	if c.Ratio >= 1 {
		t.Errorf("Ratio = %.2f — düşüşte 1'in altında olmalı", c.Ratio)
	}
}

// TestBehaviorTransientSpikeRejected — GEÇİCİ SIÇRAMA REDDİ. Motorun
// var oluş sebebi bu ayrım: 2 dilim (10 dk) süren bir sıçrama ani-sapma
// dedektörünün işi; davranış motoru yalnız KALICI olanı söyler.
func TestBehaviorTransientSpikeRejected(t *testing.T) {
	cfg := behaviorDefaults()
	baseline := baselineFor(10, 48, 100)

	// 6 dilimlik pencere: ilk 4'ü normal, son 2'si sıçramış.
	recent := rowsAt(10, 4, 3000, 0, 100)
	recent = append(recent, rowsAt(10, 2, 3000, 0, 300)...)
	for i := range recent {
		recent[i].Unix = int64(1_700_000_000 + i*300)
	}

	if c, ok := evalBehavior("checkout", "p99_ms", baseline, recent, p99Policy(), cfg); ok {
		t.Errorf("geçici sıçrama aday üretti (%s, dwell=%d) — kalıcılık kapısı çalışmıyor",
			c.Signal, c.Dwell)
	}
}

// TestBehaviorSeasonalDeviation — mevsimsel sapma: rejim eşiğini
// geçmeyen ama kendi kovasına göre çok uzak bir değer. Sıkı bir
// baseline'da %30'luk bir kayma 1.5× değildir ama 4σ'dır.
func TestBehaviorSeasonalDeviation(t *testing.T) {
	cfg := behaviorDefaults()
	// MAD'i küçük tutmak için gürültüsüz-yakın baseline; effectiveMAD
	// p99_ms için minMAD=1.0 tabanını uygular.
	b := behaviorBucket{Repeats: behaviorTestFullRepeats}
	for i := 0; i < 48; i++ {
		b.Values = append(b.Values, 100+float64(i%3))
	}
	baseline := map[int]behaviorBucket{10: b}
	recent := rowsAt(10, 4, 3000, 0, 130) // 1.3× → rejim eşiği (1.5) ALTINDA

	c, ok := evalBehavior("checkout", "p99_ms", baseline, recent, p99Policy(), cfg)
	if !ok {
		t.Fatal("kendi kovasından çok uzak bir değer aday üretmedi")
	}
	if c.Signal != "seasonal" {
		t.Errorf("Signal = %q, want seasonal — 1.3× rejim eşiğinin altında", c.Signal)
	}
	if math.Abs(c.Z) < cfg.SeasonalZ {
		t.Errorf("|z| = %.2f, seasonalZ = %.2f — eşiğin altındaki bir değer açılmamalıydı", c.Z, cfg.SeasonalZ)
	}
}

// TestBehaviorRegimeWinsOverSeasonal — kalıcı bir kayma İKİ sinyali de
// tetikler (6 dilim ⊃ 3 dilim). İkisini de yazmak aynı olayı iki satır
// göstermek olurdu — bu depoda "anomali fırtınası"nın tam kaynağı.
func TestBehaviorRegimeWinsOverSeasonal(t *testing.T) {
	cfg := behaviorDefaults()
	baseline := baselineFor(10, 48, 100)
	recent := rowsAt(10, 10, 3000, 0, 250) // hem 2.5× hem çok yüksek z

	c, ok := evalBehavior("checkout", "p99_ms", baseline, recent, p99Policy(), cfg)
	if !ok {
		t.Fatal("aday üretmedi")
	}
	if c.Signal != "regime" {
		t.Errorf("Signal = %q, want regime — daha spesifik ifade kazanmalı", c.Signal)
	}
}

// TestBehaviorOperationalFloorsShared — v0.9.826'nın vidaları BU
// motorda da geçerli. Operatörün vakası: medyanı 1.90ms olan bir op
// 2.90ms'e "oturuyor". 1.53× kalıcı kaymadır, istatistiksel olarak
// gerçektir ve hiç kimseyi ilgilendirmez. absFloor=10ms onu eler.
func TestBehaviorOperationalFloorsShared(t *testing.T) {
	cfg := behaviorDefaults()
	baseline := baselineFor(10, 48, 1.90)
	recent := rowsAt(10, 8, 3000, 0, 2.90)

	if c, ok := evalBehavior("checkout", "p99_ms", baseline, recent, p99Policy(), cfg); ok {
		t.Errorf("1.90ms → 2.90ms aday üretti (%s, ratio %.2f) — v0.9.826'nın "+
			"kapattığı sınıf bu motorda yeniden açılmış", c.Signal, c.Ratio)
	}

	// Taban KAPALIYKEN aynı vaka geçmeli: elenmesinin sebebi operatörün
	// vidası, motorun kör noktası değil.
	pol := p99Policy()
	pol.absFloor, pol.minAbsDelta, pol.floorPct = 0, 0, 0
	if _, ok := evalBehavior("checkout", "p99_ms", baseline, recent, pol, cfg); !ok {
		t.Error("tabanlar kapalıyken de elendi — eleme sebebi operatörün vidası değilmiş")
	}
}

// TestBehaviorVolumeGate — düşük hacimde yüzdeler gürültüdür. Kapı
// yalnız AÇILMAYA uygulanır (bu motorda çözülme yok, olaylar last_seen
// tazeliğiyle temizlenir).
func TestBehaviorVolumeGate(t *testing.T) {
	cfg := behaviorDefaults()
	baseline := baselineFor(10, 48, 1.0) // %1 hata
	recent := rowsAt(10, 8, 60, 6, 0)    // 60 span/5dk = 0.2 istek/sn, %10 hata

	pol := policyFor("error_rate", chstore.DefaultAnomalySensitivity())
	pol.minBaselineRate = 1.0 // operatör 1 istek/sn taban koymuş
	if _, ok := evalBehavior("cron", "error_rate", baseline, recent, pol, cfg); ok {
		t.Error("0.2 istek/sn'lik serviste aday üretti — hacim kapısı çalışmıyor")
	}
	pol.minBaselineRate = 0
	if _, ok := evalBehavior("cron", "error_rate", baseline, recent, pol, cfg); !ok {
		t.Error("kapı kapalıyken de üretmedi — eleme sebebi hacim değilmiş")
	}
}

// TestBehaviorWindowSpansHourBoundary — dwell penceresi saat sınırını
// aşabilir (10:05'teki 6 dilim 09:35'te başlar) ve HER DİLİM KENDİ
// kovasına karşı puanlanmalı. Tek bir kova baseline'ı kullanmak sabah
// rampasında sistematik yanlılık üretirdi.
func TestBehaviorWindowSpansHourBoundary(t *testing.T) {
	cfg := behaviorDefaults()
	// 09 kovası normalde 300ms, 10 kovası normalde 100ms (sabah rampası
	// tersine: gece yavaş, gündüz hızlı).
	baseline := map[int]behaviorBucket{}
	for k, v := range baselineFor(9, 48, 300) {
		baseline[k] = v
	}
	for k, v := range baselineFor(10, 48, 100) {
		baseline[k] = v
	}

	// Pencere: 3 dilim 09 kovasında 300ms (NORMAL), 3 dilim 10 kovasında
	// 100ms (NORMAL). Tek-kova baseline'ı kullanılsaydı 09 dilimleri
	// 100ms'e karşı 3× görünüp yanlış aday üretirdi.
	var recent []behaviorRow
	for i := 0; i < 3; i++ {
		recent = append(recent, behaviorRow{Unix: int64(1_700_000_000 + i*300), HOW: 9, Spans: 3000, P99Ms: 300})
	}
	for i := 3; i < 6; i++ {
		recent = append(recent, behaviorRow{Unix: int64(1_700_000_000 + i*300), HOW: 10, Spans: 3000, P99Ms: 100})
	}
	if c, ok := evalBehavior("checkout", "p99_ms", baseline, recent, p99Policy(), cfg); ok {
		t.Errorf("normal bir saat geçişi aday üretti (%s, ratio %.2f) — dilimler "+
			"kendi kovalarına karşı puanlanmıyor", c.Signal, c.Ratio)
	}
}

// ── Fırtına tavanı ──────────────────────────────────────────────────

// TestCapBehaviorCandidates — filo geneli bir olayda her servis aday
// üretir. Tavan EN GÜÇLÜ N'i geçirmeli ve kesim DETERMİNİSTİK olmalı:
// aynı tik iki podda farklı kesim yaparsa "hangi anomali gitti"
// sorusunun cevabı yoktur.
func TestCapBehaviorCandidates(t *testing.T) {
	in := []behaviorCandidate{
		{Service: "b", Metric: "p99_ms", Score: 5},
		{Service: "a", Metric: "p99_ms", Score: 50},
		{Service: "c", Metric: "error_rate", Score: 20},
		{Service: "a", Metric: "error_rate", Score: 20}, // eşitlik
	}
	got := capBehaviorCandidates(append([]behaviorCandidate(nil), in...), 3)
	if len(got) != 3 {
		t.Fatalf("tavan uygulanmadı: %d aday", len(got))
	}
	if got[0].Service != "a" || got[0].Metric != "p99_ms" {
		t.Errorf("en güçlü aday başta değil: %+v", got[0])
	}
	// Eşitlikte (servis, metrik) — deterministik.
	if got[1].Service != "a" || got[1].Metric != "error_rate" {
		t.Errorf("eşitlik deterministik kırılmadı: %+v", got[1])
	}
	// Kesilenler kaybolmaz: tavanın üstü, sıralamanın SONU olmalı.
	if got[2].Service != "c" {
		t.Errorf("üçüncü aday beklenmedik: %+v", got[2])
	}

	// max<=0 → kelepçe dışı çağrı; kesme yapılmamalı (motor susmasın).
	if n := len(capBehaviorCandidates(append([]behaviorCandidate(nil), in...), 0)); n != 4 {
		t.Errorf("max=0'da kesim yapıldı (%d) — motor sessizce susardı", n)
	}
}

// ── Deploy korelasyonu ──────────────────────────────────────────────

// TestPickBehaviorDeploy — korelasyon penceresi. Onset'ten SONRAKİ bir
// deploy önceki kaymayı AÇIKLAYAMAZ; pencerenin dışındaki bir deploy
// tesadüftür.
func TestPickBehaviorDeploy(t *testing.T) {
	onset := int64(1_700_000_000)
	ns := func(offsetSec int64) int64 { return (onset + offsetSec) * int64(time.Second) }

	deploys := []chstore.RecentDeployEntry{
		{Service: "checkout", Version: "v1", FirstSeenNs: ns(-3600)}, // 1 saat önce — pencere DIŞI
		{Service: "checkout", Version: "v2", FirstSeenNs: ns(-600)},  // 10 dk önce — pencere içi
		{Service: "checkout", Version: "v3", FirstSeenNs: ns(-120)},  // 2 dk önce — EN YAKIN
		{Service: "checkout", Version: "v4", FirstSeenNs: ns(300)},   // onset'ten SONRA
	}
	got := pickBehaviorDeploy(deploys, onset)
	if got == nil {
		t.Fatal("pencere içindeki deploy bulunamadı")
	}
	if got.Version != "v3" {
		t.Errorf("Version = %q, want v3 (onset'e en yakın, öncesinde)", got.Version)
	}

	// Deploy'suz kalıcı kayma da yakalanmalı — nil dönmek DOĞRU cevap.
	if pickBehaviorDeploy(nil, onset) != nil {
		t.Error("deploy yokken nil dönmedi")
	}
	only := []chstore.RecentDeployEntry{{Version: "v9", FirstSeenNs: ns(-7200)}}
	if pickBehaviorDeploy(only, onset) != nil {
		t.Error("pencere dışı deploy iliştirildi — tesadüfi korelasyon")
	}
}

// ── Ayrıntı JSON'u + kimlik ─────────────────────────────────────────

// TestEncodeBehaviorDetails — sample alanının şekli. /anomalies
// çekmecesi bunu ayrıştırıp insan cümlesine çeviriyor; şekil sessizce
// değişirse UI ham JSON'a düşer.
func TestEncodeBehaviorDetails(t *testing.T) {
	onset := int64(1_700_000_000)
	c := behaviorCandidate{
		Service: "checkout", Metric: "p99_ms", Signal: "regime",
		Direction: "up", Ratio: 2.1436, Z: 7.2891,
		Baseline: 118.4321, Current: 253.5678,
		HOW: 34, Dwell: 6, OnsetUnix: onset,
		Deploy: &chstore.RecentDeployEntry{Version: "v1.2.3", FirstSeenNs: (onset - 740) * int64(time.Second)},
	}
	var d behaviorDetails
	if err := json.Unmarshal([]byte(encodeBehaviorDetails(c)), &d); err != nil {
		t.Fatalf("üretilen sample geçerli JSON değil: %v", err)
	}
	if d.Metric != "p99_ms" || d.Signal != "regime" || d.Direction != "up" {
		t.Errorf("kimlik alanları yanlış: %+v", d)
	}
	if d.Ratio != 2.14 || d.Z != 7.29 {
		t.Errorf("yuvarlama uygulanmamış: ratio=%v z=%v", d.Ratio, d.Z)
	}
	if d.Unit != "ms" {
		t.Errorf("Unit = %q, want ms — UI sayıyı birimsiz basardı", d.Unit)
	}
	if d.HourOfWeek != 34 || d.Dwell != 6 {
		t.Errorf("kova/dwell taşınmamış: %+v", d)
	}
	if d.OnsetNs != onset*int64(time.Second) {
		t.Errorf("OnsetNs = %d, want %d (ns)", d.OnsetNs, onset*int64(time.Second))
	}
	if d.Deploy == nil || d.Deploy.Version != "v1.2.3" || d.Deploy.AgeSeconds != 740 {
		t.Errorf("deploy ilişkisi taşınmamış: %+v", d.Deploy)
	}

	// Deploy yoksa alan HİÇ olmamalı (omitempty) — "aranmadı" ile
	// "bulunamadı" karışmasın diye motor her adayda arıyor.
	c.Deploy = nil
	if strings.Contains(encodeBehaviorDetails(c), `"deploy"`) {
		t.Error("deploy yokken bile alan yazılmış")
	}
}

// TestBehaviorEventIdentity — fingerprint (servis, metrik) başına TEK
// satır vermeli ve SİNYALDEN bağımsız olmalı: mevsimsel başlayıp rejime
// dönüşen bir kayma AYNI olaydır. Ayrıca insan etiketinin değişmesi
// mevcut satırları bölmemeli.
func TestBehaviorEventIdentity(t *testing.T) {
	a := behaviorEventID("checkout", "p99_ms")
	b := behaviorEventID("checkout", "p99_ms")
	if a != b {
		t.Fatal("fingerprint deterministik değil — her tik yeni satır açardı")
	}
	if a == behaviorEventID("checkout", "error_rate") {
		t.Error("farklı metrikler aynı satırı paylaşıyor")
	}
	if a == behaviorEventID("payments", "p99_ms") {
		t.Error("farklı servisler aynı satırı paylaşıyor")
	}
	// Fingerprint HAM METRİKTEN, insan etiketinden değil.
	if a == chstore.FingerprintAnomaly(behaviorKind, behaviorPattern("p99_ms"), "checkout") {
		t.Error("fingerprint insan etiketinden hesaplanmış — etiket değişince " +
			"tüm açık davranış olayları bölünürdü")
	}
	if !strings.Contains(behaviorPattern("p99_ms"), "P99") {
		t.Errorf("pattern insan-okunur değil: %q", behaviorPattern("p99_ms"))
	}
}

// TestBehaviorScore — sıralama ölçütü. |z| TEK BAŞINA yetmez: sıkı bir
// baseline'da %10 kıpırtı 20σ üretir, gevşek bir baseline'da 3× kalıcı
// kayma 4σ kalır ve operatör için ikincisi daha önemlidir.
func TestBehaviorScore(t *testing.T) {
	tightNoise := behaviorScore(20, 1.10) // 20σ ama %10
	realShift := behaviorScore(4, 3.0)    // 4σ ama 3×
	if realShift <= tightNoise {
		t.Errorf("3× kalıcı kayma (%.2f) %%10 kıpırtının (%.2f) altında sıralandı",
			realShift, tightNoise)
	}
	// Simetri: 2× ve 0.5× aynı büyüklükte değişimdir.
	if math.Abs(behaviorScore(0, 2)-behaviorScore(0, 0.5)) > 1e-9 {
		t.Error("yükseliş ve düşüş asimetrik puanlanıyor")
	}
}
