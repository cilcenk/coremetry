package evaluator

// selfhealth_test.go — v0.9.1279 self-health alarm ailesinin saf
// çekirdekleri. Bu ailenin en büyük riski YANLIŞ POZİTİF: alarmlar
// operatörün "sisteme güvenebilir miyim" sorusunun cevabı, bu yüzden
// her kapı ayrı ayrı pinlenir.
//
// MUTASYON KANITI (ingestStallDecision): `return prevActive` satırını
// `return true` yapmak taze-kurulum vakasını ("hiç veri gelmemiş
// kurulum alarm ÇALMAZ") kızartır. Kanıt, çalıştırılıp geri alındı.

import (
	"fmt"
	"math"
	"strings"
	"testing"

	"github.com/cilcenk/coremetry/internal/chstore"
)

func TestIngestStallDecision(t *testing.T) {
	tests := []struct {
		name                        string
		curActive, prevActive, open bool
		want                        bool
	}{
		// Akan sistem: hiçbir kombinasyonda alarm yok.
		{"akıyor, önce de akıyordu", true, true, false, false},
		{"akıyor, önce yoktu (yeni kurulum ısındı)", true, false, false, false},
		{"akıyor, açık problem vardı → KAPANIR", true, false, true, false},
		{"akıyor, açık problem + önce de akıyordu → KAPANIR", true, true, true, false},

		// ASIL VAKA: akış kesildi ve öncesinde vardı.
		{"durdu, önce vardı → AÇILIR", false, true, false, true},

		// TAZE KURULUM: hiç veri gelmemiş. Mutasyon kapısı burada.
		{"hiç veri yok, taze kurulum → alarm YOK", false, false, false, false},

		// Süregelen arıza: ikinci tikte önceki pencere de boştur.
		// "Önce vardı" AÇILIŞ kapısıdır, TUTMA kapısı değil.
		{"durdu, önceki pencere de boş, problem AÇIK → açık kalır", false, false, true, true},
		{"durdu, önce vardı, problem açık → açık kalır", false, true, true, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := ingestStallDecision(tt.curActive, tt.prevActive, tt.open); got != tt.want {
				t.Fatalf("ingestStallDecision(%v,%v,%v) = %v, beklenen %v",
					tt.curActive, tt.prevActive, tt.open, got, tt.want)
			}
		})
	}
}

func TestSpoolBreachDecision(t *testing.T) {
	const (
		maxFiles = uint64(100_000)
		maxBytes = uint64(10 << 30)
	)
	tests := []struct {
		name         string
		files, bytes uint64
		want         selfSpoolDim
	}{
		{"temiz spool", 0, 0, spoolDimNone},
		{"sağlıklı çalkantı", 42, 1 << 20, spoolDimNone},
		{"eşiğin bir altı", maxFiles - 1, 1 << 20, spoolDimNone},
		{"dosya eşiği TAM", maxFiles, 1 << 20, spoolDimFiles},
		{"dosya eşiği aşıldı", 132_418, 12 << 30, spoolDimFiles},
		// Az sayıda ama devasa dosya: bayt kapısı yakalar.
		{"az dosya, devasa bayt", 12, maxBytes, spoolDimBytes},
		{"bayt eşiğinin bir altı", 12, maxBytes - 1, spoolDimNone},
		// 2026-08-20 prod olayının gerçek büyüklüğü.
		{"prod olayı 3M dosya / 965 GiB", 3_000_000, 965 << 30, spoolDimFiles},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := spoolBreachDecision(tt.files, tt.bytes, maxFiles, maxBytes); got != tt.want {
				t.Fatalf("spoolBreachDecision(%d,%d) = %q, beklenen %q",
					tt.files, tt.bytes, got, tt.want)
			}
		})
	}

	// Eşik 0 = o boyut KAPALI (kısmi config yazan operatör tüm aileyi
	// yanlışlıkla tetiklemesin). patchSelfHealth normalde 0'ı varsayılana
	// çeviriyor; bu, saf fonksiyonun kendi güvenliği.
	if got := spoolBreachDecision(9_999_999, 9_999_999, 0, 0); got != spoolDimNone {
		t.Fatalf("sıfır eşiklerde ihlal iddia edildi: %q", got)
	}
}

// diskSeries — düz bir seri kurar: startUsed'dan başlayıp saniyede
// perSec bayt büyüyen n örnek, stepS saniye aralıklı.
func diskSeries(n int, stepS int64, startUsed, perSec float64) []diskSample {
	out := make([]diskSample, 0, n)
	for i := 0; i < n; i++ {
		tSec := int64(i) * stepS
		out = append(out, diskSample{tSec: tSec, used: startUsed + perSec*float64(tSec)})
	}
	return out
}

func TestDiskETADays(t *testing.T) {
	const cap100G = uint64(100) << 30
	gib := float64(uint64(1) << 30)

	tests := []struct {
		name      string
		points    []diskSample
		capacity  uint64
		wantOK    bool
		wantDays  float64
		tolerance float64
	}{
		{
			// Seri 50 GiB'de BAŞLAR ve günde 10 GiB büyür. ETA, serinin
			// SONUNDAN ölçülür (regresyon doğrusunun son noktadaki değeri),
			// başından değil: 8×600 sn = 70 dk boyunca disk zaten
			// 10×(4200/86400) = 0.486 GiB doldu. Kalan 49.514 GiB →
			// 4.9514 gün. "5 gün" yazıp gevşek tolerans vermek, tam da
			// bu 70 dakikayı görmezden gelen bir testtir.
			name:      "doğrusal büyüme → doğru ETA",
			points:    diskSeries(8, 600, 50*gib, 10*gib/86400),
			capacity:  cap100G,
			wantOK:    true,
			wantDays:  4.95139,
			tolerance: 0.001,
		},
		{
			name:     "düz seri → ETA YOK",
			points:   diskSeries(8, 600, 50*gib, 0),
			capacity: cap100G,
			wantOK:   false,
		},
		{
			name:     "küçülüyor (retention sildi) → ETA YOK",
			points:   diskSeries(8, 600, 50*gib, -5*gib/86400),
			capacity: cap100G,
			wantOK:   false,
		},
		{
			name:     "tek nokta → ETA YOK",
			points:   diskSeries(1, 600, 50*gib, 10*gib/86400),
			capacity: cap100G,
			wantOK:   false,
		},
		{
			name:     "boş seri → ETA YOK",
			points:   nil,
			capacity: cap100G,
			wantOK:   false,
		},
		{
			// 4 nokta var ama 3×5dk = 15 dk < 30 dk: aralık yetersiz.
			name:     "yeterli nokta, YETERSİZ zaman aralığı → ETA YOK",
			points:   diskSeries(4, 300, 50*gib, 10*gib/86400),
			capacity: cap100G,
			wantOK:   false,
		},
		{
			// 3 nokta (min 4'ün altı), aralık bol.
			name:     "yetersiz nokta sayısı → ETA YOK",
			points:   diskSeries(3, 1800, 50*gib, 10*gib/86400),
			capacity: cap100G,
			wantOK:   false,
		},
		{
			name:     "kapasite 0 (okunamayan disk) → ETA YOK",
			points:   diskSeries(8, 600, 50*gib, 10*gib/86400),
			capacity: 0,
			wantOK:   false,
		},
		{
			// Doğru zaten tavanın üstünde: cevap sıfır gün, "tahmin yok"
			// DEĞİL — dolu bir diskte sessiz kalmak alarmı kaçırmaktı.
			name:      "zaten tavanda → 0 gün",
			points:    diskSeries(8, 600, 101*gib, 10*gib/86400),
			capacity:  cap100G,
			wantOK:    true,
			wantDays:  0,
			tolerance: 0.0001,
		},
		{
			// Hızlı dolma: günde 100 GiB. Aynı 70 dakikalık kayma
			// (4.861 GiB) burada çok daha görünür — kalan 45.139 GiB →
			// 0.4514 gün (fmtDays bunu "11 saat" yazar).
			name:      "hızlı dolma → gün altı ETA",
			points:    diskSeries(8, 600, 50*gib, 100*gib/86400),
			capacity:  cap100G,
			wantOK:    true,
			wantDays:  0.45139,
			tolerance: 0.001,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			days, ok := diskETADays(tt.points, tt.capacity)
			if ok != tt.wantOK {
				t.Fatalf("ok = %v, beklenen %v (days=%v)", ok, tt.wantOK, days)
			}
			if !tt.wantOK {
				return
			}
			if math.Abs(days-tt.wantDays) > tt.tolerance {
				t.Fatalf("days = %v, beklenen %v (±%v)", days, tt.wantDays, tt.tolerance)
			}
		})
	}
}

// Gürültülü ama gerçekten büyüyen bir seri hâlâ tahmin üretmeli;
// tamamen gürültü (eğilimsiz zıplama) üretmemeli. R² kapısının iki
// yönünü de pinler — tek yönlü bir test kapıyı kapatmayı fark etmez.
func TestDiskETAR2Gate(t *testing.T) {
	gib := float64(uint64(1) << 30)
	capBytes := uint64(100) << 30

	// Hafif gürültülü doğrusal büyüme: eğilim baskın → tahmin VAR.
	noisy := diskSeries(12, 600, 50*gib, 10*gib/86400)
	for i := range noisy {
		if i%2 == 0 {
			noisy[i].used += 0.05 * gib
		} else {
			noisy[i].used -= 0.05 * gib
		}
	}
	if _, ok := diskETADays(noisy, capBytes); !ok {
		t.Fatal("hafif gürültülü büyüme tahmin üretmeliydi (R² kapısı fazla katı)")
	}

	// Testere dişi: net eğim pozitif ama uyum berbat → tahmin YOK.
	saw := make([]diskSample, 0, 12)
	for i := 0; i < 12; i++ {
		u := 50 * gib
		if i%2 == 1 {
			u += 20 * gib
		}
		saw = append(saw, diskSample{tSec: int64(i) * 600, used: u})
	}
	if days, ok := diskETADays(saw, capBytes); ok {
		t.Fatalf("testere dişi seri tahmin üretti (%v gün) — R² kapısı ısırmıyor", days)
	}
}

func TestChannelBrokenDecision(t *testing.T) {
	tests := []struct {
		name                string
		consec, threshold   int
		configured, enabled bool
		want                bool
	}{
		{"sağlıklı kanal", 0, 3, true, true, false},
		{"tek hata", 1, 3, true, true, false},
		{"eşiğin bir altı", 2, 3, true, true, false},
		{"eşik TAM → ölü", 3, 3, true, true, true},
		{"eşiğin üstü → ölü", 9, 3, true, true, true},
		// Silinmiş kanal: notification_log geçmişi kalıcı, alarm OLMAZ.
		// (Lokal kurulumda v0.9.1278 testinden kalan iki kanal tam
		// olarak bu durumdaydı — kapı olmasa ölümsüz problem üretirdi.)
		{"silinmiş kanal, geçmişte hatalar", 9, 3, false, false, false},
		{"silinmiş ama enabled bayrağı true gelmiş", 9, 3, false, true, false},
		// Kapatılan kanal da geçerli bir düzeltmedir.
		{"kapatılmış kanal", 9, 3, true, false, false},
		// Eşik 0/negatif: kural KAPALI, her şeyi alarm yapma.
		{"eşik 0 → kapalı", 9, 0, true, true, false},
		{"eşik negatif → kapalı", 9, -1, true, true, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := channelBrokenDecision(tt.consec, tt.threshold, tt.configured, tt.enabled)
			if got != tt.want {
				t.Fatalf("channelBrokenDecision(%d,%d,%v,%v) = %v, beklenen %v",
					tt.consec, tt.threshold, tt.configured, tt.enabled, got, tt.want)
			}
		})
	}
}

// fmtDays — v0.6.36 birim-karışımı dersi: değer+birim taşıyan HER
// şablon her birimiyle test edilir. Off-axis dal sessizce bozulur.
func TestFmtDays(t *testing.T) {
	tests := []struct {
		days float64
		want string
	}{
		{0, "0 dakika"},
		{0.5 / 24, "30 dakika"}, // yarım saat
		{1.0 / 24, "1 saat"},    // tam bir saat → SAAT dalı
		{9.0 / 24, "9 saat"},    // gün altı
		{23.9 / 24, "24 saat"},  // hâlâ gün altı, yuvarlama
		{1, "1.0 gün"},          // sınır: gün dalı
		{1.94, "1.9 gün"},       // P1 bölgesi
		{6.25, "6.2 gün"},       // eşiğe yakın
		{9.99, "10.0 gün"},      // 10'un altı → ondalık
		{10, "10 gün"},          // sınır: ondalıksız
		{42.6, "43 gün"},        // uzun koşu payı
	}
	for _, tt := range tests {
		t.Run(fmt.Sprintf("%.4f", tt.days), func(t *testing.T) {
			if got := fmtDays(tt.days); got != tt.want {
				t.Fatalf("fmtDays(%v) = %q, beklenen %q", tt.days, got, tt.want)
			}
		})
	}
}

func TestFmtInt(t *testing.T) {
	tests := []struct {
		in   uint64
		want string
	}{
		{0, "0"},
		{7, "7"},
		{999, "999"},
		{1000, "1.000"},
		{12702, "12.702"},
		{132418, "132.418"},
		{3000000, "3.000.000"},
	}
	for _, tt := range tests {
		if got := fmtInt(tt.in); got != tt.want {
			t.Fatalf("fmtInt(%d) = %q, beklenen %q", tt.in, got, tt.want)
		}
	}
}

// Gerekçe metinleri (Reason) HER problemde ŞART ve neden-özelinde dolu
// olmalı — triage kuralının yazılı gereği. Boş/genelgeçer bir cümle
// operatöre hiçbir şey söylemez.
func TestIngestStallReason(t *testing.T) {
	tests := []struct {
		name        string
		w           chstore.IngestWindows
		metricsPrev bool
		wantSubstrs []string
	}{
		{
			name:        "span + metrik akıyordu",
			w:           chstore.IngestWindows{SpansPrev: 42318},
			metricsPrev: true,
			wantSubstrs: []string{"INGEST DURDU", "son 10 dakikada", "42.318 span + metrik akıyordu"},
		},
		{
			name:        "yalnız span akıyordu",
			w:           chstore.IngestWindows{SpansPrev: 501},
			metricsPrev: false,
			wantSubstrs: []string{"501 span akıyordu"},
		},
		{
			name:        "yalnız metrik akıyordu",
			w:           chstore.IngestWindows{},
			metricsPrev: true,
			wantSubstrs: []string{"önceki 10 dk: metrik akıyordu"},
		},
		{
			name:        "önceki pencere de boş (süregelen arıza)",
			w:           chstore.IngestWindows{},
			metricsPrev: false,
			wantSubstrs: []string{"önceki pencere de boş"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ingestStallReason(tt.w, 10, tt.metricsPrev)
			for _, s := range tt.wantSubstrs {
				if !strings.Contains(got, s) {
					t.Fatalf("gerekçede %q yok:\n%s", s, got)
				}
			}
		})
	}
}

func TestSpoolReason(t *testing.T) {
	cfg := chstore.DefaultSelfHealth()
	q := &chstore.DistributionQueue{
		Measured: true,
		Files:    132418,
		Bytes:    12 << 30,
		Tables: []chstore.DistributionQueueEntry{
			{Table: "spans", Files: 4217},
			{Table: "metric_points", Files: 128201, LastError: "Code: 241. Memory limit (total) exceeded"},
		},
	}
	got := spoolReason(q, spoolDimFiles, cfg)
	for _, s := range []string{
		"132.418 dosya", "12.0 GiB", "eşik 100.000 dosya",
		"en derini metric_points (128.201 dosya)",
		"Code: 241", "/admin/stats",
	} {
		if !strings.Contains(got, s) {
			t.Fatalf("spool gerekçesinde %q yok:\n%s", s, got)
		}
	}

	// Bayt dalı: eşik metni BAYT eşiğini yazmalı, dosya eşiğini değil.
	gotBytes := spoolReason(q, spoolDimBytes, cfg)
	if !strings.Contains(gotBytes, "eşik 10.0 GiB") {
		t.Fatalf("bayt dalında bayt eşiği yazılmadı:\n%s", gotBytes)
	}
	if strings.Contains(gotBytes, "eşik 100.000 dosya") {
		t.Fatalf("bayt dalında dosya eşiği yazıldı:\n%s", gotBytes)
	}

	// Kısmi ölçüm İTİRAF EDİLİR (v0.9.986 kapsam kilidi disiplini).
	partial := *q
	partial.Partial = true
	if !strings.Contains(spoolReason(&partial, spoolDimFiles, cfg), "yalnız bu düğüm") {
		t.Fatal("kısmi ölçüm gerekçede itiraf edilmedi")
	}
}

func TestDiskReason(t *testing.T) {
	got := diskReason("chc-0", "default", 1.9, 87.4, 41<<30, 7)
	for _, s := range []string{"chc-0 · default", "1.9 gün", "%87", "41.0 GiB", "eşik 7.0 gün"} {
		if !strings.Contains(got, s) {
			t.Fatalf("disk gerekçesinde %q yok:\n%s", s, got)
		}
	}
	// Tek düğümde host boş: "· " ayracı ORTADA KALMAMALI.
	single := diskReason("", "default", 3, 60, 10<<30, 7)
	if strings.Contains(single, "· default") {
		t.Fatalf("tek düğümde boş host ayraç bıraktı:\n%s", single)
	}
}

func TestChannelReason(t *testing.T) {
	got := channelReason(chstore.ChannelHealthRow{
		ChannelKind: "webhook",
		ChannelName: "oncall-pager",
		ConsecFails: 5,
		LastError:   `post: Post "https://hooks.example/x": dial tcp: lookup failed`,
	}, 3)
	for _, s := range []string{
		"webhook", "oncall-pager", "son 5 gönderimin hepsi başarısız",
		"eşik 3", "KİMSEYE ULAŞMIYOR", "dial tcp", "Test'e basın",
	} {
		if !strings.Contains(got, s) {
			t.Fatalf("kanal gerekçesinde %q yok:\n%s", s, got)
		}
	}
}
