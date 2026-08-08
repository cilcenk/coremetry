// v0.9.587 — problem bildirimlerinde tekrar tabanı (dedup floor).
//
// NEDEN: bu oturumda İKİ KEZ bildirim fırtınası oldu ve ikisi de aynı
// şekildeydi — bir dedektör, ZATEN AÇIK bir problemi "açık değil"
// sanıp her tikte yeniden açtı:
//
//	v0.9.575  OpenProblemsSnapshot ByID araması hep ıskalıyordu
//	v0.9.585  kronik tipler her tikte yeniden eskale oluyordu
//
// İkisinde de TETİKLEYİCİYİ kapattım. Ama koruma katmanı YOKTU: aynı
// şekilli ÜÇÜNCÜ bir hata, operatörün Slack'ine ve çağrı cihazına
// dakikada bir sayfa göndermeye devam ederdi. Bu dosya o tabanı koyar.
//
// NE DEĞİL: bu bir bildirim POLİTİKASI değil. Anahtar (problem, kanal,
// durum, ciddiyet) dörtlüsü olduğu için yalnızca BİREBİR AYNI gönderimi
// bastırabilir:
//
//   - eskalasyon warning→critical → ciddiyet farklı → GEÇER
//   - çözülme bildirimi           → durum farklı    → GEÇER
//   - başka bir kanal             → kanal farklı    → GEÇER
//
// Yani bastırdığı tek şey, 60 saniye önce gönderilmiş olanın aynısı —
// bunu kimse istemez.
//
// NEREDE: SendProblemAlert'in kanal döngüsünde, sendOne'da DEĞİL.
// sendOne "test gönder" ve runbook-tamamlandı yollarını da taşıyor ve
// onlar TEKRARLANAN DURUM BİLDİRİMİ değil, OPERATÖR EYLEMİ: admin
// "Test gönder"e iki kez basarsa iki test gitmeli. Kapıyı problem
// yayılımına koymak bu ayrımı yapı gereği korur.
//
// ─────────────────────────────────────────────────────────────────────
// v0.9.825 (operatör-raporlu, prod) — ÜÇÜNCÜ fırtına, ve bu kez taban
// vardı ama YETMEDİ: 6 haftalık bir shared_dependency problemi deploy
// sonrası 21:25'te 10+ kopya e-posta attı.
//
// Neden yetmedi: dörtlü anahtarın SON bileşeni CİDDİYET. Ciddiyet
// değişince anahtar değişir, yani gönderim GEÇER — ve bu bilinçliydi
// (meşru bir warning→critical eskalasyonu yutulmasın diye). Ama
// shared_exception.go:206 ciddiyeti HER TİKTE canlı servis sayısından
// yeniden hesaplıyor:
//
//	existing.Severity = sharedBurstSeverity(len(b.Services))
//
// Patlamaya servis girip çıktıkça ciddiyet warning↔critical SALINIYOR.
// Salınımın her yarım turu "yeni anahtar" demek, yani taban her turda
// baştan açılıyordu. Üstüne yaş-bazlı eskalasyon (escalateStaleProblems)
// her sıçramada SendProblemAlert'i yeniden çağırıyor. 10 kopyanın motoru
// buydu: TEKRAR değil, SALINIM.
//
// Ayrım şu ve iki katmanın gerekçesi de bu:
//   - EskalasyON (warning→critical, ciddiyet ARTIYOR) meşru haberdir.
//   - SalınıM (critical→warning→critical, ciddiyet artmıyor) haber değil,
//     aynı olayın yeniden etiketlenmesidir.
//
// Bu yüzden ikinci, CİDDİYETTEN BAĞIMSIZ bir kapı eklendi ve içine tek
// bir delik açıldı: ciddiyet basamağı GERÇEKTEN yükseldiyse geçer.
// Salınım geçemez, çünkü geri dönüşte basamak yükselmiyor.
//
// Ayrıca anahtara problem STARTED_AT damgası girdi: aynı kimlikle
// kapanıp yeniden açılan (flap) bir problemin ikinci açılışı ayrı bir
// olaydır. forgetProblem bunu çözülme bildirimi GİDERSE hallediyordu —
// ama çözülme kanal süzgecine takılırsa hiç gitmez ve damga kalırdı.
// started_at bunu yapısal olarak halleder.
//
// KALAN AÇIK NOKTA, dürüstçe: harita SÜREÇ-İÇİ. Deploy/restart onu
// siler, yani her yeniden başlatma bir kopyaya izin verir. Bu bilinçli
// — kalıcı taban için her gönderimde bir CH okuması gerekirdi ve
// restart başına BİR kopya, tikte bir kopyayla aynı sınıfta değil.
// Ekip-yönlendirme maili zaten notification_log üzerinden kalıcı
// dedup'lu (sendTeamMail), yani restart onu tekrarlamaz.
package notify

import (
	"strconv"
	"strings"
	"time"
)

const (
	// notifyDedupWindow — birebir aynı gönderim (ciddiyet DAHİL) bu süre
	// içinde tekrarlanmaz.
	//
	// 15 dakika: evaluator tiki 60 sn, yani takılı bir dedektör saatte
	// 60 sayfa üretiyordu; bu pencerede 4 üretir. 15× azalma.
	//
	// SIFIRA indirmek bilinçli olarak İSTENMEDİ: tam susturma, hatayı
	// da susturur. Operatör "bu şey hâlâ bağırıyor" diyebilmeli —
	// fırtına söndürülmeli ama gizlenmemeli.
	notifyDedupWindow = 15 * time.Minute

	// notifySameStateCooldown — CİDDİYETTEN BAĞIMSIZ taban (v0.9.825).
	//
	// Anahtar (problem, kanal, durum, started_at) — ciddiyet YOK. Aynı
	// problemin aynı kanala aynı durumda gönderimi saatte BİR kez geçer,
	// ciddiyet etiketi ne kadar salınırsa salınsın.
	//
	// TEK DELİK: ciddiyet basamağı bu pencerede görülmüş en yükseğin
	// ÜSTÜNE çıkarsa geçer (bkz. allowNotifySend). Yani gerçek bir
	// warning→critical eskalasyonu ANINDA sayfalar; critical→warning→
	// critical salınımı geçemez.
	//
	// 1 saat neden: yaş-bazlı eskalasyon merdiveninin (15 dk info→warning,
	// 30 dk warning→critical) TAMAMINDAN uzun. Daha kısa bir pencere,
	// merdivenin iki basamağını da aynı pencereye sığdıramaz ve salınım
	// basamak sıçramasıyla karışırdı.
	//
	// SABİT, system_settings vidası DEĞİL — bilinçli. Bu bir politika
	// değil bir TABAN: "aynı şeyi saatte bir defadan fazla söyleme"
	// filoya göre ayarlanacak bir sayı değil, ve vidalanabilir bir taban
	// er geç 0'a çekilip fırtınayı geri getirirdi.
	notifySameStateCooldown = time.Hour

	// notifyDedupMaxKeys — harita bu boyutu aşınca süresi dolmuşlar
	// temizlenir. Üst sınır ŞART: aksi halde uzun ömürlü bir worker'da
	// her problem kimliği sonsuza dek tutulur ve bu sessiz bir sızıntı
	// olurdu.
	notifyDedupMaxKeys = 8192
)

// severityRank — ciddiyet basamağı. Yalnız SIRALAMA için; bilinmeyen bir
// etiket 0 alır ve hiçbir zaman "yükselme" sayılmaz (bilinmeyen bir
// değerin fırtına deliğini açmasındansa bir bildirimi geciktirmesi
// yeğdir — kapının kendisi zaten 1 saatlik).
func severityRank(sev string) int {
	switch strings.ToLower(strings.TrimSpace(sev)) {
	case "critical":
		return 3
	case "warning":
		return 2
	case "info":
		return 1
	}
	return 0
}

// dedupStamp — bir anahtarın son gönderimi ve o gönderimden BERİ kaç
// tekrarın bastırıldığı.
//
// Sayaç yalnız muhasebe için değil: bastırmanın KENDİSİ loglanırsa
// v0.9.585'in log selini birebir yeniden üretirdik (dakikada bir satır).
// Sayaç sayesinde bir fırtına pencere başına İKİ satır maliyet çıkarır —
// bastırma başlarken bir kez, pencere kapanırken toplamla bir kez.
//
// topRank (v0.9.825) YALNIZ durum damgasında anlamlı: bu pencerede
// görülmüş en yüksek ciddiyet basamağı. Eskalasyon deliğinin belleği
// budur — salınım geri döndüğünde basamak zaten kayıtlı olduğu için
// "yükseldi" sayılmaz.
type dedupStamp struct {
	at         time.Time
	suppressed int
	topRank    int
}

// Katman etiketleri — iki anahtar uzayının çakışmasını yapısal olarak
// imkânsız kılar. Alan içerikleri (ciddiyet, durum) hiçbir zaman bu
// kontrol baytlarını taşıyamaz.
const (
	dedupLayerExact = "\x01" // (problem, kanal, durum, started_at, ciddiyet)
	dedupLayerState = "\x02" // (problem, kanal, durum, started_at)
)

// notifyDedupKey — BİREBİR AYNI gönderim anahtarı (ciddiyet dahil).
//
// problemID ÖNCE gelir ve bu bilinçli: forgetProblemNotifications
// önek taramasıyla tek bir problemin tüm kanallarını silebiliyor.
//
// Ayırıcı olarak NUL kullanılıyor çünkü hiçbir alan onu içeremez;
// ":" olsaydı kimliğinde iki nokta taşıyan bir problem (bizde var:
// "shared-exc:<tip>:<kova>") komşu bir anahtarla çakışabilirdi.
//
// startedAt (v0.9.825): aynı kimlikle kapanıp yeniden açılan bir problem
// AYRI bir olaydır ve ilk açılışın damgası onu yutmamalı.
func notifyDedupKey(problemID, channelID, status, severity string, startedAt int64) string {
	return notifyStateKey(problemID, channelID, status, startedAt) +
		"\x00" + severity + dedupLayerExact
}

// notifyStateKey — CİDDİYETTEN BAĞIMSIZ durum anahtarı (v0.9.825).
// Ciddiyet salınımı bu anahtarı DEĞİŞTİREMEZ; fırtınayı kesen katman bu.
func notifyStateKey(problemID, channelID, status string, startedAt int64) string {
	return problemID + "\x00" + channelID + "\x00" + status +
		"\x00" + strconv.FormatInt(startedAt, 10) + dedupLayerState
}

// allowNotifySend — SAF. Bu gönderime izin var mı?
//
// İKİ KAPI, ikisi de geçilmeli:
//
//  1. exact (ciddiyet DAHİL, window): birebir aynısı bu pencerede gitti mi?
//  2. state (ciddiyet HARİÇ, cooldown): aynı problem/kanal/durum bu
//     pencerede gitti mi? — TEK İSTİSNA: ciddiyet basamağı pencerede
//     görülmüş en yükseğin ÜSTÜNE çıktıysa geçer (gerçek eskalasyon).
//
// İki değer döner: izin, ve BASTIRMA SAYISI.
//   - izin yoksa → bu pencerede kaçıncı bastırma olduğu (1 = ilki)
//   - izin varsa → ÖNCEKİ pencerede kaç tekrarın yutulduğu (0 = sakin)
//
// Sayaç DURUM damgasında tutulur: exact damga ciddiyetle çoğaldığı için
// oradaki sayaç bir fırtınayı olduğundan küçük gösterirdi (tam da
// gizlemek istemediğimiz sayı).
//
// Damgalar test-et-ve-yaz tek adımda güncellenir; ayrılsaydı iki
// eşzamanlı çağıran arasında yarış açılırdı.
func allowNotifySend(seen map[string]dedupStamp, exactKey, stateKey string,
	rank int, now time.Time, window, cooldown time.Duration) (bool, int) {

	exact := seen[exactKey]
	state := seen[stateKey]

	exactBlocks := !exact.at.IsZero() && now.Sub(exact.at) < window
	stateBlocks := !state.at.IsZero() && now.Sub(state.at) < cooldown
	// Eskalasyon deliği: basamak GERÇEKTEN yükseldiyse durum kapısı açılır.
	// Salınımda (critical→warning→critical) topRank zaten 3'tür, yani
	// rank > topRank yanlıştır ve delik açılmaz.
	escalated := rank > state.topRank
	if escalated {
		exactBlocks = false // yeni basamak = yeni haber; exact anahtar da zaten farklı
	}

	if exactBlocks || (stateBlocks && !escalated) {
		state.suppressed++
		seen[stateKey] = state
		return false, state.suppressed
	}

	prev := state.suppressed
	seen[exactKey] = dedupStamp{at: now}
	// Durum damgasının SAATİ eskalasyonda da tazelenir: eskalasyon yeni
	// bir olaydır ve kendi cooldown penceresini hak eder. topRank ise
	// monoton — geri düşen bir ciddiyet basamağı sıfırlayamaz, yoksa
	// salınım deliği yeniden açardı.
	seen[stateKey] = dedupStamp{at: now, topRank: maxInt(state.topRank, rank)}
	return true, prev
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}

// forgetProblemNotifications — SAF. Bir problemin tüm bastırma
// durumunu siler.
//
// Çözülme bildirimi gönderildiğinde çağrılır. Neden gerekli: bir
// problem açılıp çözülüp AYNI kimlikle yeniden açılırsa (flap), ikinci
// açılış meşru bir haberdir ve ilk açılışın damgası onu yutmamalı.
// Çözülme, o problem hakkında bildiğimiz her şeyi geçersiz kılar.
func forgetProblemNotifications(seen map[string]dedupStamp, problemID string) {
	prefix := problemID + "\x00"
	for k := range seen {
		if strings.HasPrefix(k, prefix) {
			delete(seen, k)
		}
	}
}

// sweepNotifyDedup — SAF. Süresi dolmuş damgaları atar.
func sweepNotifyDedup(seen map[string]dedupStamp, now time.Time, window time.Duration) {
	for k, s := range seen {
		if now.Sub(s.at) >= window {
			delete(seen, k)
		}
	}
}

// allowChannelSend — Notifier üzerindeki kilitli sarmalayıcı.
//
// problemID boşsa AÇIK GEÇER: kimliğini bilmediğimiz bir şeyi
// bastırmak, yanlış tarafa düşmektir. Bu kapının işi gürültüyü
// kırpmak, haber kaybetmek değil.
func (n *Notifier) allowChannelSend(problemID, channelID, status, severity string, startedAt int64) (bool, int) {
	if problemID == "" {
		return true, 0
	}
	now := time.Now()
	n.dedupMu.Lock()
	defer n.dedupMu.Unlock()
	if n.dedupSeen == nil {
		n.dedupSeen = map[string]dedupStamp{}
	}
	if len(n.dedupSeen) > notifyDedupMaxKeys {
		// Süpürme UZUN pencereye göre: kısa pencereyle süpürmek, henüz
		// yaşayan bir durum damgasını (1 sa) atıp fırtına deliğini
		// yeniden açardı.
		sweepNotifyDedup(n.dedupSeen, now, notifySameStateCooldown)
	}
	return allowNotifySend(n.dedupSeen,
		notifyDedupKey(problemID, channelID, status, severity, startedAt),
		notifyStateKey(problemID, channelID, status, startedAt),
		severityRank(severity), now, notifyDedupWindow, notifySameStateCooldown)
}

// forgetProblem — çözülme sonrası bastırma durumunu temizler.
func (n *Notifier) forgetProblem(problemID string) {
	if problemID == "" {
		return
	}
	n.dedupMu.Lock()
	defer n.dedupMu.Unlock()
	if n.dedupSeen != nil {
		forgetProblemNotifications(n.dedupSeen, problemID)
	}
}
