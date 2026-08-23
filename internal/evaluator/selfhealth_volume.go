// selfhealth_volume.go — v0.9.1294: self-health ailesinin BEŞİNCİ kuralı,
// hacim sıçraması (Dynatrace-parite EK A / A1'in (d) maddesi).
//
// v0.9.1279 bu maddeyi BİLEREK ertelemişti ve gerekçesi selfhealth.go'nun
// başlığında duruyor: "sıçrama tespiti baseline/mevsimsellik gerektiriyor
// — davranış motorunun işi". O gerekçe, sıçramayı LATENCY/HATA sinyali
// gibi okuduğu için doğruydu. Buradaki soru başka: hacim sıçraması bir
// PERFORMANS anomalisi değil, bir MALİYET olayı. "Dün bu servis 210 bin
// span yazıyordu, bugün 1,2 milyon" cümlesi mevsimsellik modeli
// gerektirmez — iki sayı ve bir oran gerektirir, ve yanlış olma riski
// modelin değil TABANIN meselesidir (aşağıya bak).
//
// KURALIN YAŞAM DÖNGÜSÜ İCAT EDİLMEDİ: dört kardeşiyle birebir aynı hat
// (leader-locked tik → deterministik id → tik başındaki OpenProblems
// snapshot'ıyla dedup → UpsertProblem → açılışta notify → koşul geçince
// auto-resolve). Bu dosyanın kendine ait ÜÇ kararı var:
//
//	(1) TABAN GÜRÜLTÜ KAPISI. Küçük serviste 3 span → 30 span 10×'tir ve
//	    hiçbir şey ifade etmez. Kapı olmadan bu kural yanlış-pozitif
//	    fabrikasıdır — ailenin en büyük riski tam olarak bu, çünkü bu
//	    alarmlar operatörün "sisteme güvenebilir miyim" sorusunun cevabı.
//	(2) YENİ SERVİS ≠ SIÇRAMA. Önceki pencerede hiç veri yoksa alarm YOK.
//	    self-ingest-stall'un "önce vardı" şartının simetrik ikizi: orada
//	    yokluk "durdu" iddiasını kuramaz, burada "sıçradı" iddiasını.
//	    Yerel ölçümde bu kapının bedeli somut: portfolio-service 24 saatte
//	    96.146 span / önceki 24 saatte 0 → kapı olmasa 96.146× ile açılan
//	    bir alarm, oysa servis dün henüz yoktu.
//	(3) P1 OLAMAZ. Severity DAİMA warning. Bu bir maliyet uyarısı, kesinti
//	    değil; computePriority'de warning dalı P1 üretmez (en fazla, büyük
//	    ihlalde P2). Yaş-eskalasyonu da bu kuralda KAPALI —
//	    escalationExempt, gerekçesi orada.
package evaluator

import (
	"context"
	"fmt"
	"log"
	"time"

	"github.com/cilcenk/coremetry/internal/chstore"
)

const (
	selfVolumeRuleID = "self-volume-spike"

	// selfVolumeWindow — kıyaslanan iki pencerenin uzunluğu. 24 saat,
	// çünkü gün içi eğri (gece/gündüz) tam bir devir yaptığında sıçrama
	// mevsimsellikten ayrışır: 1 saatlik pencerelerde sabah trafiği her
	// gün "4× sıçrama" olurdu.
	selfVolumeWindow = 24 * time.Hour

	// selfVolumeScanEvery — ÖLÇÜM sıklığı. 24 saatlik iki pencereyi 60
	// saniyede bir yeniden hesaplamak, bir dakikada %0,07 değişebilen bir
	// sayı için tik başına bir MV taraması demekti. Saatte bir yeter.
	//
	// DİKKAT — seyreltilen ÖLÇÜM, yaşam döngüsü DEĞİL: iki tik arasında
	// son taramanın sonucu bellekten yeniden sunulur. Aksi hâlde
	// sweepStaleProblems (3 × tik = 3 dakika tazelenmeyen açık problemi
	// "kaynak sustu" diye kapatır) bu kuralın satırlarını üç dakikada bir
	// kapatır, saat başı yeniden açardı: her saat bir bildirim, her saat
	// sıfırlanan StartedAt.
	selfVolumeScanEvery = time.Hour

	// selfVolumeMaxServices — tik başına aday satır tavanı. Sorgu zaten
	// hacim tabanının ÜSTÜNDEKİLERİ ciro sırasına diziyor; bu, binlerce
	// servisli bir kurulumda dönen satırın sınırı.
	selfVolumeMaxServices = 500
)

// ── Saf karar çekirdeği (tablo testli — selfhealth_volume_test.go) ──────

// volumeSpikeDecision — bir servisin telemetri hacmi SIÇRADI mı?
//
// Dört dal, üçü ayrı bir yanlış-pozitif sınıfını kapatıyor:
//
//  1. factor <= 0 → kural KAPALI. (patchSelfHealth normalde 0'ı
//     varsayılana çeviriyor; bu, saf fonksiyonun kendi güvenliği —
//     spoolBreachDecision'ın sıfır-eşik dalıyla aynı duruş.)
//  2. cur < minSpans → TABAN. Maliyet, ŞU AN gelenle orantılıdır: güncel
//     pencere mutlak olarak küçükse oran ne olursa olsun ne disk dolar
//     ne kardinalite patlar. Bu satırı silen mutasyon, küçük-servis
//     vakasını kızartır.
//  3. prev == 0 → YENİ SERVİS. Oran tanımsız (sonsuz); "sıçradı" demek
//     için önce bir "önce" gerekir.
//  4. Asıl karar: cur >= prev × factor. `>=` bilinçli — eşiğin TAM
//     üstünde durmak ihlaldir (aile geneli: spoolBreachDecision,
//     channelBrokenDecision).
func volumeSpikeDecision(cur, prev, minSpans uint64, factor float64) bool {
	if factor <= 0 {
		return false
	}
	if cur < minSpans {
		return false
	}
	if prev == 0 {
		return false
	}
	return float64(cur) >= float64(prev)*factor
}

// escalationExempt — bu kural YAŞLANMAYLA ŞİDDET KAZANMAZ.
//
// Neden gerekli: yaş-eskalasyonu (varsayılan açık) 30 dakika açık kalan
// bir warning'i critical'a çıkarıyor, computePriority ise 4 saati geçen
// critical'ı P1'e. Hacim sıçraması TANIMI GEREĞİ en az 24 saat açık kalır
// (pencereler ancak bir gün sonra eşitlenir), yani eskalasyon bu kuralda
// bir olasılık değil KESİNLİK olurdu: her sıçrama birkaç saat içinde P1'e
// tırmanır ve "maliyet uyarısı" bir kesinti alarmının yerini kapardı.
// Kardeş dört kural bu tuzağa düşmüyor çünkü hepsi zaten critical
// doğuyor ve nextSeverity critical'ı yükseltmiyor.
//
// İki yerde okunuyor — reconcileSelfHealth'in tazeleme dalı ve
// escalateStaleProblems süpürmesi. Biri unutulursa diğeri tek başına
// yetmez: süpürme satırı yükseltir, tazeleme geri indirir ve v0.8.309'un
// bildirim seli aynen geri gelir.
func escalationExempt(ruleID string) bool { return ruleID == selfVolumeRuleID }

// ── Tik ─────────────────────────────────────────────────────────────────

// selfVolumeSpike — evaluateSelfHealth geçişi.
//
// Dönen ikinci değer "bu kural bu tikte KAPSANDI mı": yalnız kapsanan
// kuralların açık problemleri kapatılabilir (v0.9.984 fail-open dersi).
// Ölçemediğimiz bir tikte hüküm vermiyoruz — ama son geçerli ölçümü
// sunmaya devam ediyoruz, çünkü "ölçemedim" ne "temiz" ne de "sustu"
// demektir.
func (e *Evaluator) selfVolumeSpike(ctx context.Context, cfg chstore.SelfHealthConfig) ([]selfProblem, bool) {
	e.volMu.Lock()
	cached, scannedAt := e.volCache, e.volAt
	e.volMu.Unlock()

	if !scannedAt.IsZero() && time.Since(scannedAt) < selfVolumeScanEvery {
		return cached, true // ucuz tik: ölçüm saatlik, yaşam döngüsü her tikte
	}

	rows, err := e.store.ReadServiceVolumeWindows(ctx, selfVolumeWindow,
		cfg.VolumeSpikeMinSpans, selfVolumeMaxServices)
	if err != nil {
		log.Printf("[evaluator] self-health hacim penceresi okunamadı: %v", err)
		if scannedAt.IsZero() {
			return nil, false // hiç ölçüm yapılmadı: hüküm YOK
		}
		// volAt İLERLETİLMİYOR → bir sonraki tik yeniden dener.
		return cached, true
	}

	var out []selfProblem
	for _, r := range rows {
		if !volumeSpikeDecision(r.Cur, r.Prev, cfg.VolumeSpikeMinSpans, cfg.VolumeSpikeFactor) {
			continue
		}
		out = append(out, volumeSpikeProblem(r, cfg))
	}

	e.volMu.Lock()
	e.volCache, e.volAt = out, time.Now()
	e.volMu.Unlock()
	return out, true
}

// volumeSpikeProblem — SAF problem tarifi (tablo testli).
//
// Value/Threshold çifti KATSAYI taşıyor (5.7 / 4.0), ham sayım değil.
// İki sebep: (a) kuralın ölçtüğü büyüklük katsayıdır — Threshold sütununa
// `prev × factor` gibi türetilmiş bir sayı koymak operatöre hiçbir vidada
// karşılığı olmayan bir eşik gösterirdi; (b) öncelik merdiveni oranı
// Value/Threshold'dan türetiyor, yani katsayı çifti P3→P2 basamağını
// doğrudan ve okunabilir biçimde sürüyor. Ham sayımlar gerekçenin içinde,
// tam çözünürlükte.
//
// Service alanı DOLU — kardeş dört kuraldan ayrıldığı tek yer. Onlarda
// özne Coremetry'nin kendisiydi ve sahte bir servis adı uydurmak kırık
// bir bağlantı üretirdi; burada özne GERÇEK bir servis (span yazıyor,
// yani katalogda var), dolayısıyla /problems satırı çalışan bir servis
// bağlantısı ve takım sahipliği zenginleştirmesi kazanıyor.
func volumeSpikeProblem(v chstore.ServiceVolume, cfg chstore.SelfHealthConfig) selfProblem {
	factor := 0.0
	if v.Prev > 0 {
		factor = float64(v.Cur) / float64(v.Prev)
	}
	return selfProblem{
		id:       selfVolumeRuleID + ":" + v.Service,
		ruleID:   selfVolumeRuleID,
		ruleName: "Coremetry · servis hacmi sıçradı",
		metric:   "self.volume_spike_factor",
		service:  v.Service,
		// DAİMA warning. Bu bir maliyet uyarısı: kimse gece kalkmasın,
		// ama sabah listede dursun. critical yazmak P2 tabanı + 4 saatte
		// P1 demekti (bkz. escalationExempt).
		severity:    "warning",
		value:       factor,
		threshold:   cfg.VolumeSpikeFactor,
		description: volumeSpikeReason(v, factor, cfg.VolumeSpikeFactor, int(selfVolumeWindow.Hours())),
	}
}

// volumeSpikeReason — SAF gerekçe (tablo testli).
//
// Operatörün ilk sorusu "ne kadar, neye göre" — cümle bu yüzden HAM İKİ
// SAYIYI da yazar, yalnız katsayıyı değil: 5,7× tek başına 210 bin ile
// 1,2 milyon arasındaki farkı söylemez ve müdahale kararı o farka bağlı.
//
// windowH parametre, sabit değil: değer+birim taşıyan her şablon her
// birimiyle tablo-testli (v0.6.36 birim-karışımı dersi).
func volumeSpikeReason(v chstore.ServiceVolume, factor, threshold float64, windowH int) string {
	return fmt.Sprintf(
		"%s son %d saatte %s span üretti (önceki %d saat: %s — %s; eşik %s). "+
			"Bu bir kesinti değil MALİYET olayı: ingest yolu, disk ve kardinalite "+
			"bu servise orantılı büyüyor. Sırayla: bozuk bir deploy mu (retry döngüsü, "+
			"debug span'leri açık kalmış), yeni bir attribute mü eklendi (sınırsız değer = "+
			"kardinalite patlaması), yoksa gerçek trafik mi arttı. "+
			"Müdahale: /system/cardinality → top emitter'lar + attribute anahtarları.",
		v.Service, windowH, fmtInt(v.Cur), windowH, fmtInt(v.Prev),
		fmtFactor(factor), fmtFactor(threshold))
}

// fmtFactor — katsayının okunur hâli. 5.7× ile 96146× aynı şablonla
// yazılamaz: `%.1f` ikincisini "96146.0×" yapar (ondalık, o büyüklükte
// gürültüdür), `%.0f` ilkini "6×" yapar (eşiğin 4 olduğu bir cümlede
// yuvarlama kararın kendisini gizler). fmtDays ile aynı desen, aynı
// gerekçe — ve aynı şekilde HER dalıyla tablo-testli.
func fmtFactor(f float64) string {
	if f < 10 {
		return fmt.Sprintf("%.1f×", f)
	}
	return fmt.Sprintf("%.0f×", f)
}
