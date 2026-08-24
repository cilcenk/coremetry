package notify

import (
	"fmt"
	"sync/atomic"
	"time"

	"github.com/cilcenk/coremetry/internal/chstore"
)

// Bildirim yönlendirme sonucu (v0.9.1344) — "bu problem KİMSEYE
// gitmedi" olgusu.
//
// OPERATÖR RAPORU: bir problem hiçbir bildirim kanalıyla eşleşmezse
// SESSİZCE düşüyordu. İki yol da kapalı biter ve ikincisi sürpriz:
//
//  1. Kanal döngüsü — `if !c.MatchRules.MatchesProblem(in) { continue }`.
//     MatchRules.Services ile daraltılmış bir kanal, db-konulu bir
//     problemle (db:oracle@corebank-scan.prod, v0.9.1338) ASLA
//     eşleşmez.
//  2. Ekip-yönlendirme maili — yorumu "kanal yokken bile koşar" diyor,
//     yani bir emniyet ağı gibi OKUNUYOR. Değil: alıcıları
//     GetServiceMetadata(p.Service)'ten çözüyor ve db konusunun
//     katalog satırı yok → md nil → ekip yok → MAİL YOK.
//
// Sonuç, operatörün cümlesiyle: "Oracle doluyor ve kimse haber almıyor."
//
// Bu dosya YÖNLENDİRMEYİ DEĞİŞTİRMEZ (kapsam kararı). Yalnız olguyu
// kaydeder: bir problem kimseye gitmediyse bu görünür olur.
//
// ── "Hiçbir şey yapılandırılmamış" ile "yapılandırılmış ama
//
//	eşleşmedi" AYRIMI ───────────────────────────────────────────────
//
// Bu ayrım özelliğin kendisi kadar önemli: kanalı hiç olmayan bir
// kurulumda HER problem "kimseye gitmedi" derse bu sinyal değil
// GÜRÜLTÜdür ve ilk günden kapatılır.
//
// Kural: bir yol probleme TEKLİF EDİLDİYSE ve reddettiyse kusurdur.
// Hiç teklif edilmediyse bu bir YAPILANDIRMA DURUMUdur, kusur değil.
//
//   - ChannelsOffered = EnabledChannelsForSeverity(p.Severity) sonucu.
//     Sıfırsa ya hiç kanal yok ya da operatör her kanala "bu ciddiyeti
//     bana getirme" demiş. İkisi de operatörün BEYANIdır — eşleşme
//     hatası değil.
//   - Team = teamMailOff, ekip-yönlendirmenin bu problem için hiç
//     devreye girmediği hâl (vida kapalı / ciddiyet dışlanmış / problem
//     açılış değil).
//
// İkisi birden boşsa → routingUnconfigured, işaret YOK, sayaç ayrı.
// En az biri teklif edildi ve kimse almadıysa → routingUnmatched.
type routingVerdict int

const (
	// routingDelivered — en az bir gerçek gönderim denendi.
	routingDelivered routingVerdict = iota
	// routingSuppressed — eşleşme VARDI ama tekrar-bastırma yuttu, ya
	// da ekip-maili bu problem için zaten gitmişti. Kimse haber
	// almamış DEĞİL: daha önce almış. Kayıp değil.
	routingSuppressed
	// routingUnconfigured — probleme hiçbir yol teklif edilmedi.
	// KUSUR DEĞİL (yukarıdaki ayrım).
	routingUnconfigured
	// routingUnmatched — yol(lar) teklif edildi, hiçbiri almadı. KUSUR.
	routingUnmatched
)

func (v routingVerdict) String() string {
	switch v {
	case routingDelivered:
		return "delivered"
	case routingSuppressed:
		return "suppressed"
	case routingUnconfigured:
		return "unconfigured"
	case routingUnmatched:
		return "unmatched"
	}
	return "unknown"
}

// teamMailOutcome — sendTeamMail'in SendProblemAlert'e döndürdüğü sonuç.
// Daha önce hiçbir şey döndürmüyordu; "mail gitti mi" bilgisi çağıranda
// yoktu, dolayısıyla "kimseye gitmedi" sorusu SORULAMIYORDU bile.
type teamMailOutcome int

const (
	// teamMailOff — ekip-yönlendirme bu problem için hiç devrede değil:
	// vida kapalı, ciddiyet dışlanmış, ayar okunamadı, ya da problem
	// bir açılış değil (yalnız status=open mail atar).
	teamMailOff teamMailOutcome = iota
	// teamMailNoRecipients — vida AÇIK ve bu ciddiyeti alıyor, ama
	// alıcı kümesi boş çıktı: katalog satırı yok (db konuları!), ekip
	// adı boş, ya da ekibin adresi tanımsız. Raporlanan kusurun ikinci
	// yarısı tam olarak burası.
	teamMailNoRecipients
	// teamMailAlreadySent — kalıcı dedup (notification_log) ya da
	// süreç-içi talep: bu problemin maili ZATEN gitti/gidiyor.
	teamMailAlreadySent
	// teamMailSent — gönderim yapıldı ya da (AI-özet bekleyen dalda)
	// gönderime devredildi.
	teamMailSent
)

// unmatchedChannelID — "kimseye gitmedi" işaretinin tekrar-bastırma
// anahtarındaki sahte kanal kimliği. Gerçek kanal kimlikleri üretilmiş
// tanımlayıcılar; bu sabitle çakışmaz. Anahtar kanal kimliğini içerdiği
// için işaretin bastırma durumu gerçek kanallarınkine karışmaz.
const unmatchedChannelID = "__unmatched__"

// teamMailReach — SAF: ekip-yönlendirmenin bu problem için ULAŞABİLDİĞİ
// adres kümesi ve boş kümenin ANLAMI (v0.9.1344'te sendTeamMail'den
// çıkarıldı).
//
// Neden ayrı bir fonksiyon: raporlanan kusurun ikinci yarısı tam olarak
// bu karar ve sendTeamMail'in geri kalanı (CH okuması, SMTP, dedup)
// canlı bir Store olmadan çalıştırılamıyor. Kusuru pinleyen test
// buraya bakar.
//
// Dönüş sözleşmesi:
//   - (nil, teamMailOff)          → vida kapalı ya da ciddiyet dışlanmış.
//     Bu operatörün BEYANIdır, kusur değil.
//   - (nil, teamMailNoRecipients) → vida AÇIK, ciddiyet kabul ediliyor,
//     ama kimse çözülemedi. md nil (katalog satırı YOK — db konuları
//     tam olarak burada düşüyor), ekip adı boş, ya da ekibin adresi
//     tanımsız. Denendi ve kimse bulunamadı: kusur.
//   - (adresler, teamMailSent)    → ulaşılabilir; kapılar geçilirse
//     gidecek. Çağıran dedup kapılarında bunu teamMailAlreadySent'e
//     düşürebilir.
func teamMailReach(tc chstore.TeamContacts, md *chstore.ServiceMetadata, severity string) ([]string, teamMailOutcome) {
	if !tc.Enabled || !tc.SeverityAllows(severity) {
		return nil, teamMailOff
	}
	to := resolveTeamRecipients(md, tc)
	if len(to) == 0 {
		return nil, teamMailNoRecipients
	}
	return to, teamMailSent
}

// routingFacts — bir SendProblemAlert turunun sayılabilir sonucu.
// Hepsi çağrının kendi yerel değişkenleri; CH okuması YOK.
type routingFacts struct {
	// ChannelsOffered — bu ciddiyeti kabul eden ETKİN kanal sayısı
	// (eşleşme kuralları HENÜZ uygulanmadan).
	//
	// NOT — "kaç kanal EŞLEŞTİ" diye ayrı bir alan TAŞINMIYOR, çünkü
	// routingUnmatched'e varan her yolda o sayı zorunlu olarak sıfır:
	// eşleşen her kanal ya Suppressed'i ya Sends'i artırır ve ikisi de
	// kararı unmatched'ten uzaklaştırır. İlk yazımda böyle bir alan
	// vardı; mutasyon taraması artırımını silmenin HİÇBİR testi
	// düşürmediğini gösterdi ve nedeni buydu — okuyucusu erişilemez bir
	// daldı. Alan da dal da silindi (v0.9.1344).
	ChannelsOffered int
	// Sends — gerçekten denenen kanal gönderimi (dedup'tan SONRA).
	Sends int
	// Suppressed — eşleşti ama tekrar-bastırma yuttu.
	Suppressed int
	// Team — ekip-yönlendirme maili sonucu.
	Team teamMailOutcome
}

// decideRouting — SAF karar. Sıra ÖNEMLİ ve her dal ayrı test ediliyor.
func decideRouting(f routingFacts) routingVerdict {
	// 1. Biri haber aldı. Kanalın biri gittiyse ekip-mailinin boş
	//    çıkması kusur değildir — operatör sayfayı ALDI.
	if f.Sends > 0 || f.Team == teamMailSent {
		return routingDelivered
	}
	// 2. Eşleşme vardı ama bastırıldı, ya da ekip-maili bu problem için
	//    ZATEN gitmişti. Daha önce haber verilmiş: kayıp DEĞİL. Aksi
	//    hâlde aynı problem her tikte "kimseye gitmedi" derdi.
	if f.Suppressed > 0 || f.Team == teamMailAlreadySent {
		return routingSuppressed
	}
	// 3. Hiçbir yol teklif edilmedi → yapılandırma durumu, kusur değil.
	if f.ChannelsOffered == 0 && f.Team == teamMailOff {
		return routingUnconfigured
	}
	// 4. Teklif edildi, kimse almadı.
	return routingUnmatched
}

// routingReason — SAF gerekçe. notification_log'un `error` sütununa ve
// oradan /problems triyaj ekranına iner, yani operatörün TEK bağlamı
// bu cümle. "Eşleşme yok" demek yetmez; NEREDE kaybolduğunu söyler.
//
// Yalnız routingUnmatched için anlamlı; diğer sonuçlar satır yazmaz.
func routingReason(f routingFacts) string {
	var chPart, teamPart string
	if f.ChannelsOffered == 0 {
		chPart = "Bu ciddiyeti kabul eden etkin kanal yok"
	} else {
		// routingUnmatched + ChannelsOffered > 0 ⟹ eşleşen kanal SIFIR
		// (routingFacts.ChannelsOffered not'undaki gerekçe). Üçüncü bir
		// dal yazmak erişilemez kod olurdu.
		chPart = fmt.Sprintf("%d kanal bu ciddiyeti kabul ediyor ama hiçbirinin eşleşme kuralı bu konuyla örtüşmedi (servis/küme kapsamı dışında)", f.ChannelsOffered)
	}
	switch f.Team {
	case teamMailOff:
		teamPart = "ekip-yönlendirme bu problem için devrede değil"
	case teamMailNoRecipients:
		teamPart = "ekip-yönlendirme açık ama bu konunun katalog kaydı/ekip adresi yok"
	default:
		teamPart = "ekip-yönlendirme sonucu belirsiz"
	}
	return chPart + "; " + teamPart + ". Kimse haber almadı."
}

// ── Sayaç (/admin/stats) ────────────────────────────────────────────
//
// CLAUDE.md öz-gözlem kuralı: yeni bir yol = yeni bir sayaç. Operatör
// bir problemi AÇMADAN "kaç problem kimseye gitmedi" görebilmeli.
//
// Süreç-içi atomikler — Behavior / CodeFetch ile AYNI kablo (chstore
// notify'ı import EDEMEZ; ters yön zaten var). Yeniden başlatmada
// sıfırlanır ve bu DOĞRU cevaptır: kalıcı defter notification_log'dur,
// bu sayaç "şu anda bakılan pod ne gördü" sorusudur.
var routingObs struct {
	delivered    atomic.Int64
	suppressed   atomic.Int64
	unconfigured atomic.Int64
	unmatched    atomic.Int64

	lastUnmatchedUnix atomic.Int64
	lastUnmatchedID   atomic.Value // string
	lastUnmatchedSvc  atomic.Value // string
	lastReason        atomic.Value // string
}

// RoutingStats — /admin/stats görüntüsü. Alan adları
// chstore.NotifyRoutingStats ile birebir; ayrı tip çünkü chstore bu
// paketi import edemez.
type RoutingStats struct {
	Delivered    int64 `json:"delivered"`
	Suppressed   int64 `json:"suppressed"`
	Unconfigured int64 `json:"unconfigured"`
	Unmatched    int64 `json:"unmatched"`

	LastUnmatchedUnix    int64  `json:"lastUnmatchedUnix"`
	LastUnmatchedID      string `json:"lastUnmatchedId"`
	LastUnmatchedService string `json:"lastUnmatchedService"`
	LastUnmatchedReason  string `json:"lastUnmatchedReason"`
}

// RoutingObservability — anlık kopya.
func RoutingObservability() RoutingStats {
	s := RoutingStats{
		Delivered:         routingObs.delivered.Load(),
		Suppressed:        routingObs.suppressed.Load(),
		Unconfigured:      routingObs.unconfigured.Load(),
		Unmatched:         routingObs.unmatched.Load(),
		LastUnmatchedUnix: routingObs.lastUnmatchedUnix.Load(),
	}
	if v, ok := routingObs.lastUnmatchedID.Load().(string); ok {
		s.LastUnmatchedID = v
	}
	if v, ok := routingObs.lastUnmatchedSvc.Load().(string); ok {
		s.LastUnmatchedService = v
	}
	if v, ok := routingObs.lastReason.Load().(string); ok {
		s.LastUnmatchedReason = v
	}
	return s
}

// bumpRouting — sayacı ilerletir. Saf değil (global durum), ama tek
// satırlık ve verdict'i decideRouting üretiyor.
func bumpRouting(v routingVerdict, problemID, service, reason string, now time.Time) {
	switch v {
	case routingDelivered:
		routingObs.delivered.Add(1)
	case routingSuppressed:
		routingObs.suppressed.Add(1)
	case routingUnconfigured:
		routingObs.unconfigured.Add(1)
	case routingUnmatched:
		routingObs.unmatched.Add(1)
		routingObs.lastUnmatchedUnix.Store(now.Unix())
		routingObs.lastUnmatchedID.Store(problemID)
		routingObs.lastUnmatchedSvc.Store(service)
		routingObs.lastReason.Store(reason)
	}
}

// resetRoutingObservability — yalnız testler için.
func resetRoutingObservability() {
	routingObs.delivered.Store(0)
	routingObs.suppressed.Store(0)
	routingObs.unconfigured.Store(0)
	routingObs.unmatched.Store(0)
	routingObs.lastUnmatchedUnix.Store(0)
	routingObs.lastUnmatchedID.Store("")
	routingObs.lastUnmatchedSvc.Store("")
	routingObs.lastReason.Store("")
}
