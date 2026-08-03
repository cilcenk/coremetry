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
package notify

import (
	"strings"
	"time"
)

const (
	// notifyDedupWindow — birebir aynı gönderim bu süre içinde
	// tekrarlanmaz.
	//
	// 15 dakika: evaluator tiki 60 sn, yani takılı bir dedektör saatte
	// 60 sayfa üretiyordu; bu pencerede 4 üretir. 15× azalma.
	//
	// SIFIRA indirmek bilinçli olarak İSTENMEDİ: tam susturma, hatayı
	// da susturur. Operatör "bu şey hâlâ bağırıyor" diyebilmeli —
	// fırtına söndürülmeli ama gizlenmemeli.
	//
	// Üst sınır olarak eskalasyon pencereleri (15 dk info→warning,
	// 30 dk warning→critical) referans alındı: daha uzun bir pencere,
	// meşru bir eskalasyonun ardından gelen tekrarı da yiyebilirdi.
	notifyDedupWindow = 15 * time.Minute

	// notifyDedupMaxKeys — harita bu boyutu aşınca süresi dolmuşlar
	// temizlenir. Üst sınır ŞART: aksi halde uzun ömürlü bir worker'da
	// her problem kimliği sonsuza dek tutulur ve bu sessiz bir sızıntı
	// olurdu.
	notifyDedupMaxKeys = 8192
)

// dedupStamp — bir anahtarın son gönderimi ve o gönderimden BERİ kaç
// tekrarın bastırıldığı.
//
// Sayaç yalnız muhasebe için değil: bastırmanın KENDİSİ loglanırsa
// v0.9.585'in log selini birebir yeniden üretirdik (dakikada bir satır).
// Sayaç sayesinde bir fırtına pencere başına İKİ satır maliyet çıkarır —
// bastırma başlarken bir kez, pencere kapanırken toplamla bir kez.
type dedupStamp struct {
	at         time.Time
	suppressed int
}

// notifyDedupKey — bastırma anahtarı.
//
// problemID ÖNCE gelir ve bu bilinçli: forgetProblemNotifications
// önek taramasıyla tek bir problemin tüm kanallarını silebiliyor.
//
// Ayırıcı olarak NUL kullanılıyor çünkü hiçbir alan onu içeremez;
// ":" olsaydı kimliğinde iki nokta taşıyan bir problem (bizde var:
// "shared-exc:<tip>:<kova>") komşu bir anahtarla çakışabilirdi.
func notifyDedupKey(problemID, channelID, status, severity string) string {
	return problemID + "\x00" + channelID + "\x00" + status + "\x00" + severity
}

// allowNotifySend — SAF. Bu anahtar için gönderime izin var mı?
//
// İki değer döner: izin, ve BASTIRMA SAYISI.
//   - izin yoksa → bu pencerede kaçıncı bastırma olduğu (1 = ilki)
//   - izin varsa → ÖNCEKİ pencerede kaç tekrarın yutulduğu (0 = sakin)
//
// Damga test-et-ve-yaz tek adımda güncellenir; ayrılsaydı iki eşzamanlı
// çağıran arasında yarış açılırdı.
func allowNotifySend(seen map[string]dedupStamp, key string, now time.Time, window time.Duration) (bool, int) {
	prev, ok := seen[key]
	if ok && now.Sub(prev.at) < window {
		prev.suppressed++
		seen[key] = prev
		return false, prev.suppressed
	}
	seen[key] = dedupStamp{at: now}
	return true, prev.suppressed
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
func (n *Notifier) allowChannelSend(problemID, channelID, status, severity string) (bool, int) {
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
		sweepNotifyDedup(n.dedupSeen, now, notifyDedupWindow)
	}
	return allowNotifySend(n.dedupSeen, notifyDedupKey(problemID, channelID, status, severity), now, notifyDedupWindow)
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
