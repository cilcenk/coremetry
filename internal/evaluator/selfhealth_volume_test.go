package evaluator

// selfhealth_volume_test.go — v0.9.1294, hacim sıçraması kuralının saf
// çekirdekleri.
//
// Bu kuralın YANLIŞ POZİTİF riski kardeşlerinden yüksek, çünkü tetikleyicisi
// bir arıza değil bir ORAN: her küçük servis günün birinde 10× dalgalanır.
// Kapıların üçü de ayrı ayrı pinlenir.
//
// MUTASYON KANITI (volumeSpikeDecision): `if cur < minSpans { return false }`
// satırı SİLİNDİĞİNDE (dosya derlenir hâlde kalır) "küçük servis 10× ama az
// hacim" ve "3 span → 30 span" vakaları kızarır. Çalıştırıldı, geri alındı.
//
// İKİNCİ MUTASYON (escalationExempt): `ruleID == selfVolumeRuleID` →
// `false` yapmak TestVolumeSpikeNeverEscalates'i kızartır — ve kızartan
// assert, korkuyu ölçen assert: yaş-eskalasyonu bu kuralı critical'a, oradan
// da P1'e taşıyordu.

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/cilcenk/coremetry/internal/chstore"
)

func TestVolumeSpikeDecision(t *testing.T) {
	const (
		minSpans = uint64(100_000)
		factor   = 4.0
	)
	tests := []struct {
		name      string
		cur, prev uint64
		want      bool
	}{
		// NORMAL BÜYÜME — yerel demoda ölçülen gerçek bant (1.19-1.30×).
		{"normal gün-içi büyüme (1.2×)", 115_936, 95_549, false},
		{"pencere ortasında doğan servis (2.42×)", 239_090, 98_630, false},
		{"eşiğin hemen altı (3.9×)", 390_000, 100_000, false},

		// ASIL VAKA.
		{"sıçrama (5.7×)", 1_200_000, 210_000, true},
		{"eşik TAM (4.0×)", 1_000_000, 250_000, true},

		// YENİ SERVİS: önceki pencerede HİÇ veri yok. Yerel ölçümde
		// gerçek vaka — portfolio-service 96.146 / 0. Kapı olmasa
		// 96.146× ile alarm açardı.
		{"yeni servis (önceki 0) → alarm YOK", 96_146, 0, false},
		{"yeni servis, devasa hacim → yine alarm YOK", 50_000_000, 0, false},

		// TABAN GÜRÜLTÜ KAPISI: oran kocaman, hacim önemsiz.
		{"küçük servis 10× ama az hacim", 30_000, 3_000, false},
		{"3 span → 30 span", 30, 3, false},
		{"tabanın bir altı, oran devasa", 99_999, 100, false},
		{"taban TAM + oran ihlali", 100_000, 25_000, true},

		// Hacim büyük ama oran yok: kural sessiz. (Bu kural "çok span
		// yazıyorsun" demiyor, "DÜN'e göre sıçradın" diyor.)
		{"devasa hacim, düz seyir", 900_000_000, 890_000_000, false},
		// Hacim düştü: sıçrama değil.
		{"hacim yarıya indi", 500_000, 1_000_000, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := volumeSpikeDecision(tt.cur, tt.prev, minSpans, factor); got != tt.want {
				t.Fatalf("volumeSpikeDecision(cur=%d,prev=%d) = %v, beklenen %v",
					tt.cur, tt.prev, got, tt.want)
			}
		})
	}

	// Katsayı 0/negatif = kural KAPALI. patchSelfHealth normalde 0'ı
	// varsayılana çeviriyor; bu, saf fonksiyonun kendi güvenliği
	// (spoolBreachDecision'ın sıfır-eşik dalıyla aynı duruş).
	if volumeSpikeDecision(10_000_000, 1, minSpans, 0) {
		t.Fatal("katsayı 0'da ihlal iddia edildi")
	}
	if volumeSpikeDecision(10_000_000, 1, minSpans, -1) {
		t.Fatal("negatif katsayıda ihlal iddia edildi")
	}
	// Taban 0: kapı kapalı demek DEĞİL — o zaman yalnız oran konuşur.
	// (Operatör vidasında 0 asla oluşmaz, patch varsayılana çevirir.)
	if !volumeSpikeDecision(30, 3, 0, 4) {
		t.Fatal("taban 0'da oran kapısı da susturuldu")
	}
}

// Problem tarifi: kimlik, şiddet ve öncelik merdivenini süren Value/
// Threshold çifti. Bu testin ASIL derdi ŞİDDET: warning yazılmazsa
// computePriority bu satırı P2 tabanına koyar ve 4 saat sonra P1 yapar.
func TestVolumeSpikeProblem(t *testing.T) {
	cfg := chstore.DefaultSelfHealth()
	p := volumeSpikeProblem(chstore.ServiceVolume{
		Service: "bsa-mobile-login-prod",
		Cur:     1_200_000,
		Prev:    210_000,
	}, cfg)

	if p.id != "self-volume-spike:bsa-mobile-login-prod" {
		t.Fatalf("kimlik = %q", p.id)
	}
	if p.ruleID != selfVolumeRuleID {
		t.Fatalf("ruleID = %q", p.ruleID)
	}
	// Servis DOLU: /problems satırı çalışan bir servis bağlantısı ve
	// takım sahipliği zenginleştirmesi kazanır.
	if p.service != "bsa-mobile-login-prod" {
		t.Fatalf("service = %q, servis adı taşınmadı", p.service)
	}
	if p.severity != "warning" {
		t.Fatalf("severity = %q — P1 OLMAMALI, bu bir maliyet uyarısı", p.severity)
	}
	// Value = ölçülen katsayı, Threshold = vidadaki eşik. Oran
	// (5.71/4.0 = 1.43) BigBreachRatio'nun altında → P3.
	if got := p.value / p.threshold; got < 1.4 || got > 1.5 {
		t.Fatalf("value/threshold oranı = %v, ~1.43 bekleniyordu (%v / %v)",
			got, p.value, p.threshold)
	}
	if p.threshold != cfg.VolumeSpikeFactor {
		t.Fatalf("threshold = %v, vidadaki eşik %v", p.threshold, cfg.VolumeSpikeFactor)
	}

	// Katsayı büyüdükçe oran büyür: merdivenin P2 basamağını süren tek
	// mekanizma bu çift. (P2 sınırının KENDİSİ okuma anında,
	// chstore.computePriority + BigBreachRatio vidasıyla belirlenir —
	// pini orada: TestVolumeSpikePriorityLadder.)
	big := volumeSpikeProblem(chstore.ServiceVolume{
		Service: "loglama-servisi", Cur: 8_000_000, Prev: 250_000,
	}, cfg)
	if big.severity != "warning" {
		t.Fatalf("32× sıçramada severity = %q — katsayı ne olursa olsun warning kalmalı", big.severity)
	}
	if big.value/big.threshold <= p.value/p.threshold {
		t.Fatal("büyük katsayı daha yüksek bir oran üretmedi")
	}
}

// Yaş-eskalasyonu bu kuralda KAPALI. Test önce TUZAĞIN GERÇEK olduğunu
// gösteriyor (mekanizma), sonra muafiyetin onu kestiğini.
func TestVolumeSpikeNeverEscalates(t *testing.T) {
	esc := chstore.DefaultProblemEscalation()

	// (1) Tuzak gerçek mi: eskalasyon açıkken 24 saat açık kalan bir
	// warning critical'a çıkıyor. Hacim sıçraması TANIMI GEREĞİ en az 24
	// saat açık kalır (pencereler ancak bir gün sonra eşitlenir).
	if got := nextSeverity("warning", 24*time.Hour, esc); got != "critical" {
		t.Fatalf("mekanizma değişmiş: nextSeverity(warning, 24h) = %q, "+
			"critical bekleniyordu — muafiyetin gerekçesini gözden geçir", got)
	}

	// (2) Muafiyet bu kuralı — ve YALNIZ bunu — kesiyor.
	if !escalationExempt(selfVolumeRuleID) {
		t.Fatal("self-volume-spike eskalasyondan muaf değil: birkaç saat içinde critical→P1 olur")
	}
	for _, rule := range []string{
		selfIngestRuleID, selfSpoolRuleID, selfDiskRuleID, selfChannelRuleID,
		"exception-storm", "anomaly-auto:abc", "",
	} {
		if escalationExempt(rule) {
			t.Fatalf("%q muaf sayıldı — muafiyet bu kurala özel olmalı", rule)
		}
	}
}

// Gerekçe: HAM İKİ SAYI + katsayı + eşik. Katsayı tek başına 210 bin ile
// 1,2 milyon arasındaki farkı söylemez ve müdahale kararı o farka bağlı.
func TestVolumeSpikeReason(t *testing.T) {
	got := volumeSpikeReason(chstore.ServiceVolume{
		Service: "bsa-mobile-login-prod", Cur: 1_200_000, Prev: 210_000,
	}, 5.714, 4.0, 24)
	for _, s := range []string{
		"bsa-mobile-login-prod",
		"son 24 saatte", "1.200.000 span",
		"önceki 24 saat: 210.000",
		"5.7×", "eşik 4.0×",
		"MALİYET", "kardinalite", "/system/cardinality",
	} {
		if !strings.Contains(got, s) {
			t.Fatalf("gerekçede %q yok:\n%s", s, got)
		}
	}

	// v0.6.36 birim-karışımı dersi: pencere uzunluğu şablona parametre
	// giriyor, sabit değil. Başka bir pencerede cümle de değişmeli —
	// gömülü "24" bırakan bir yazım burada yakalanır.
	six := volumeSpikeReason(chstore.ServiceVolume{
		Service: "x", Cur: 300_000, Prev: 50_000,
	}, 6.0, 4.0, 6)
	if !strings.Contains(six, "son 6 saatte") || !strings.Contains(six, "önceki 6 saat") {
		t.Fatalf("pencere uzunluğu şablona geçmedi:\n%s", six)
	}
	if strings.Contains(six, "24 saat") {
		t.Fatalf("gerekçede gömülü 24 kalmış:\n%s", six)
	}
}

// fmtFactor — değer+birim taşıyan HER şablon her dalıyla test edilir
// (fmtDays ile aynı sözleşme).
func TestFmtFactor(t *testing.T) {
	tests := []struct {
		f    float64
		want string
	}{
		{0, "0.0×"},
		{1, "1.0×"},
		{2.42, "2.4×"},    // yerel demonun en yüksek meşru değeri
		{4, "4.0×"},       // varsayılan eşik
		{5.714, "5.7×"},   // gerekçe örneği
		{9.94, "9.9×"},    // ondalık dalının üst ucu
		{9.99, "10.0×"},   // yuvarlama hâlâ ondalık dalda
		{10, "10×"},       // sınır: ondalıksız
		{42.6, "43×"},     // büyük katsayı
		{96146, "96146×"}, // yeni-servis kapısı olmasaydı görülecek büyüklük
	}
	for _, tt := range tests {
		t.Run(fmt.Sprintf("%.3f", tt.f), func(t *testing.T) {
			if got := fmtFactor(tt.f); got != tt.want {
				t.Fatalf("fmtFactor(%v) = %q, beklenen %q", tt.f, got, tt.want)
			}
		})
	}
}
