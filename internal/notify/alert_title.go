// v0.9.828 — bildirim başlığı ve öncelik etiketi.
//
// OPERATÖRÜN İSTEĞİ: bir sayfa geldiğinde AÇMADAN önce "bu şimdi mi,
// bugün mü, müsait olunca mı" ayrımı görünsün. Coremetry bunu zaten
// hesaplıyor (P1/P2/P3 triyajı, chstore.computePriority) ama YALNIZ
// ekranda gösteriyordu; bildirime hiç geçmiyordu. Telefonundaki
// bildirimde "[CRITICAL]" gören nöbetçi, on tane critical arasından
// hangisinin gerçekten kalkmayı gerektirdiğini ayırt edemiyordu.
//
// NEDEN BURADA HESAPLANABİLİYOR: computePriority SAF ve bildirim
// anındaki beş alandan (severity, value, threshold, status, startedAt)
// tamamen hesaplanabilir — CH okuması gerektirmiyor. Bu yüzden
// SendProblemAlert funnel'ında bir kez hesaplanıp bütün kanallara
// aynı değerle iniyor.
//
// BAŞLIK TEK YERDE. Öncesinde ALTI ayrı yer aynı
// "[SEV] servis — kural" dizgisini kendi fmt.Sprintf'iyle kuruyordu
// (e-posta konusu, notification_log kaydı, Slack text, Teams summary,
// Teams title, Zoom header, WhatsApp). Etiketi altı yere ayrı ayrı
// eklemek, birinde unutulduğunda kimsenin fark etmeyeceği bir tutarsızlık
// üretirdi — üstelik notification_log'daki kopya ZATEN e-posta konusundan
// ayrışabilir durumdaydı ve /events'te gönderilenden farklı bir konu
// gösterebilirdi.
package notify

import (
	"fmt"
	"strings"

	"github.com/cilcenk/coremetry/internal/chstore"
)

// priorityTag — "[P1]" / "[P2]" / "[P3]", ya da öncelik hesaplanmamışsa "".
//
// BOŞ GEÇER, UYDURMAZ: Priority alanı dolmamışsa (bu funnel'dan
// geçmeyen bir çağrı, ya da SendTest'in sentetik problemi) etiketsiz
// başlık üretilir. Yanlış bir öncelik etiketi, hiç etiket olmamasından
// KÖTÜDÜR — nöbetçi ona bakıp karar veriyor.
func priorityTag(p chstore.Problem) string {
	switch strings.ToUpper(strings.TrimSpace(p.Priority)) {
	case "P1", "P2", "P3":
		return "[" + strings.ToUpper(strings.TrimSpace(p.Priority)) + "]"
	}
	return ""
}

// alertTitle — her kanalın kullandığı TEK başlık:
//
//	[CRITICAL][P1] payments — Error rate > 5%
//
// Öncelik etiketi ciddiyetin YANINDA ve ARDINDA duruyor. İkisi farklı
// şeyler söylüyor ve ikisi de gerekli: ciddiyet "ne kadar kötü",
// öncelik "ne kadar acil". Bir critical problem P2 olabilir (henüz
// eşiğin 2 katına çıkmamış) ve bir DOWN monitörü P1'dir (tamamen
// kayıp) — sıralamayı yapan ikincisi.
func alertTitle(p chstore.Problem) string {
	return fmt.Sprintf("[%s]%s %s — %s",
		strings.ToUpper(p.Severity), priorityTag(p), p.Service, p.RuleName)
}

// priorityField — Slack/Teams/Zoom alanlarının değeri: "P1 — critical +
// 3.2x threshold". Gerekçe olmadan "P1" bir otorite beyanı olurdu;
// operatör onunla tartışabilmeli.
//
// Öncelik hesaplanmamışsa "—": alanı büsbütün atlamak, kanal
// biçimlerinde alan sayısını değiştirir ve Teams kartında hizayı
// bozardı. Bir tire, "bilinmiyor"un dürüst gösterimi.
func priorityField(p chstore.Problem) string {
	if p.Priority == "" {
		return "—"
	}
	if p.PriorityReason == "" {
		return p.Priority
	}
	return p.Priority + " — " + p.PriorityReason
}

// priorityBadgeHTML — HTML e-postadaki ciddiyet rozetinin yanına giren
// öncelik parçası (" · P1"). Boş öncelikte hiçbir şey basılmaz, yani
// mevcut e-posta biçimi birebir korunur.
//
// AYNI HÜCREDE, ayrı <td>'de değil: Outlook HTML'i Word'ün motoruyla
// çiziyor ve ikinci bir hücre rozeti alt satıra kaydırırdı (v0.8.561'de
// öğrenilen ders). Ayraç olarak orta nokta, çünkü rozetin kendi arka
// planı zaten sınırı çiziyor.
//
// esc GEREKMİYOR ve bu güvenli: priorityTag yalnız üç sabit dizgiden
// ("[P1]"/"[P2]"/"[P3]") birini üretiyor, operatör metni taşımıyor.
func priorityBadgeHTML(p chstore.Problem) string {
	tag := priorityTag(p)
	if tag == "" {
		return ""
	}
	return `<span style="opacity:.75"> · </span>` + strings.Trim(tag, "[]")
}

// withPriority — problemi bildirim anındaki öncelikle damgalar.
//
// chstore.EnrichProblemsWithPriority'yi ÇAĞIRIYOR, kendi kopyasını
// yazmıyor: öncelik kuralları (2× eşik, 4sa açık kritik, tam kayıp)
// tek bir yerde yaşamalı, yoksa bildirimdeki etiket ile ekrandaki rozet
// zamanla ayrışır ve operatör hangisine güveneceğini bilemez.
func withPriority(p chstore.Problem) chstore.Problem {
	out := chstore.EnrichProblemsWithPriority([]chstore.Problem{p})
	if len(out) == 0 {
		return p
	}
	return out[0]
}
