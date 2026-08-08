// v0.9.587 — tekrar tabanının regresyon testi.
// v0.9.825 — ikinci katman (ciddiyetten bağımsız cooldown) + started_at.
//
// Bu testin işi iki yönlü ve İKİNCİSİ daha önemli:
//  1. fırtınanın gerçekten kesildiğini,
//  2. kapının MEŞRU hiçbir bildirimi yemediğini
//
// pinlemek. Bir dedup katmanının tehlikeli kipi susturma değil,
// AŞIRI susturmadır: eskalasyonu ya da çözülmeyi yutarsa operatör
// hiç fark etmez ve biz de fark etmeyiz.
package notify

import (
	"os"
	"regexp"
	"strings"
	"testing"
	"time"
)

const dedupTestChan = "ch-1"

// dedupTestStart — sabit bir started_at damgası. Anahtarın parçası
// olduğu için testlerin hepsinde AYNI olmalı; farklı olduğu tek yer
// flap vakası (TestNewStartedAtIsANewEvent).
const dedupTestStart int64 = 1_700_000_000_000_000_000

// try — allowNotifySend'i üretimdeki sarmalayıcıyla AYNI şekilde çağırır
// (iki anahtar + basamak + iki pencere). Testler kapıyı üretimin
// gördüğü hâliyle görsün diye; anahtarları elle kurmak, üretim
// sarmalayıcısı değiştiğinde testin sessizce ayrışmasına yol açardı.
func try(seen map[string]dedupStamp, problemID, chID, status, sev string,
	startedAt int64, now time.Time) (bool, int) {
	return allowNotifySend(seen,
		notifyDedupKey(problemID, chID, status, sev, startedAt),
		notifyStateKey(problemID, chID, status, startedAt),
		severityRank(sev), now, notifyDedupWindow, notifySameStateCooldown)
}

// TestDedupSuppressesIdenticalRepeat — v0.9.575 / v0.9.585 fırtınası.
// Takılı bir dedektör aynı problemi her tikte (60 sn) yeniden açar.
func TestDedupSuppressesIdenticalRepeat(t *testing.T) {
	seen := map[string]dedupStamp{}
	t0 := time.Unix(1_760_000_000, 0)

	if ok, _ := try(seen, "p-1", dedupTestChan, "open", "warning", dedupTestStart, t0); !ok {
		t.Fatal("ilk gönderim geçmeli")
	}
	// 60 sn'lik tiklerle 59 dakika: cooldown içinde 59 tekrar.
	sent := 0
	for i := 1; i <= 59; i++ {
		if ok, _ := try(seen, "p-1", dedupTestChan, "open", "warning", dedupTestStart,
			t0.Add(time.Duration(i)*time.Minute)); ok {
			sent++
		}
	}
	if sent != 0 {
		t.Errorf("pencere içinde %d tekrar geçti, 0 bekleniyordu — fırtına kesilmiyor", sent)
	}
	// Pencere kapanınca YENİDEN geçmeli: tam susturma, hatayı da
	// susturur. Operatör "bu şey hâlâ bağırıyor" diyebilmeli.
	if ok, _ := try(seen, "p-1", dedupTestChan, "open", "warning", dedupTestStart,
		t0.Add(61*time.Minute)); !ok {
		t.Error("pencere kapandıktan sonra gönderim yeniden geçmeli — " +
			"kalıcı susturma hatayı gizler")
	}
}

// TestSeverityOscillationCannotStorm — v0.9.825 FIRTINASININ MOTORU.
//
// Prod: 6 haftalık bir shared_dependency problemi deploy sonrası 21:25'te
// 10+ kopya e-posta attı. shared_exception.go ciddiyeti HER TİKTE canlı
// servis sayısından yeniden hesaplıyor, yani patlamaya servis girip
// çıktıkça ciddiyet warning↔critical SALINIYOR. Eski dörtlü anahtarın
// son bileşeni ciddiyet olduğu için her yarım tur "yeni anahtar" =
// tabanı baştan açıyordu.
//
// Bu test o senaryoyu birebir sürüyor.
func TestSeverityOscillationCannotStorm(t *testing.T) {
	seen := map[string]dedupStamp{}
	t0 := time.Unix(1_760_000_000, 0)

	sevs := []string{"warning", "critical"}
	sent := 0
	// 60 tik × 60 sn = 1 saat; ciddiyet her tikte salınıyor.
	for i := 0; i < 60; i++ {
		if ok, _ := try(seen, "shared-exc:java.sql.SQLException:100", dedupTestChan,
			"open", sevs[i%2], dedupTestStart, t0.Add(time.Duration(i)*time.Minute)); ok {
			sent++
		}
	}
	// Beklenen 2: ilk warning gönderimi, ve ONU İZLEYEN ilk critical
	// (basamak GERÇEKTEN yükseldi — meşru eskalasyon). Sonraki her
	// salınım turu basamağı yükseltmediği için geçemez.
	if sent != 2 {
		t.Errorf("ciddiyet salınımı %d kopya geçirdi, 2 bekleniyordu "+
			"(ilk açılış + ilk gerçek eskalasyon). Eski dörtlü anahtar burada "+
			"30 kopya geçiriyordu — v0.9.825 fırtınasının motoru budur.", sent)
	}
}

// TestGenuineEscalationStillPages — deliğin AÇIK kalması gereken hâli.
//
// Salınımı kesen kapı, GERÇEK bir warning→critical eskalasyonunu da
// yerse operatör kötüleşen bir olayı hiç duymaz. Bu, aşırı-susturma
// tarafındaki en pahalı hata olurdu.
func TestGenuineEscalationStillPages(t *testing.T) {
	t0 := time.Unix(1_760_000_000, 0)

	// Merdiven: info → warning → critical, her basamak cooldown'ın TAM
	// İÇİNDE (1 ve 2 dakika sonra). Geçiyorlarsa sebebi zamanaşımı değil,
	// basamak yükselmesi.
	seen := map[string]dedupStamp{}
	if ok, _ := try(seen, "p-1", dedupTestChan, "open", "info", dedupTestStart, t0); !ok {
		t.Fatal("ilk gönderim geçmeli")
	}
	if ok, _ := try(seen, "p-1", dedupTestChan, "open", "warning", dedupTestStart,
		t0.Add(time.Minute)); !ok {
		t.Error("info→warning eskalasyonu yendi — kötüleşen bir olay sessiz kaldı")
	}
	if ok, _ := try(seen, "p-1", dedupTestChan, "open", "critical", dedupTestStart,
		t0.Add(2*time.Minute)); !ok {
		t.Error("warning→critical eskalasyonu yendi — sayfalanması gereken " +
			"basamak sıçraması yutuldu")
	}
	// …ama aynı critical'ın TEKRARI geçmemeli.
	if ok, _ := try(seen, "p-1", dedupTestChan, "open", "critical", dedupTestStart,
		t0.Add(3*time.Minute)); ok {
		t.Error("aynı critical ikinci kez geçti — merdivenin tepesinde taban yok")
	}
	// …ve geri düşüp yeniden çıkmak da geçmemeli (salınım).
	if ok, _ := try(seen, "p-1", dedupTestChan, "open", "warning", dedupTestStart,
		t0.Add(4*time.Minute)); ok {
		t.Error("critical→warning geri düşüşü geçti — bu bir haber değil, yeniden etiketleme")
	}
	if ok, _ := try(seen, "p-1", dedupTestChan, "open", "critical", dedupTestStart,
		t0.Add(5*time.Minute)); ok {
		t.Error("salınım critical'a dönünce yeniden geçti — topRank monoton " +
			"değil; fırtına deliği geri açıldı")
	}
}

// TestDedupNeverEatsLegitimateAlerts — kapının YEMEMESİ gerekenler.
func TestDedupNeverEatsLegitimateAlerts(t *testing.T) {
	t0 := time.Unix(1_760_000_000, 0)
	// Hepsi ilk gönderimden 1 dk sonra — yani pencere TAM İÇİNDE.
	// Geçiyorlarsa, geçme sebepleri zamanaşımı değil, anahtarın kendisi.
	t1 := t0.Add(time.Minute)

	cases := []struct {
		name                       string
		problem, chID, status, sev string
		startedAt                  int64
	}{
		{"eskalasyon warning→critical", "p-1", dedupTestChan, "open", "critical", dedupTestStart},
		{"çözülme bildirimi", "p-1", dedupTestChan, "resolved", "warning", dedupTestStart},
		{"ikinci kanal", "p-1", "ch-2", "open", "warning", dedupTestStart},
		{"başka problem", "p-2", dedupTestChan, "open", "warning", dedupTestStart},
		{"aynı kimlik, YENİ açılış (flap)", "p-1", dedupTestChan, "open", "warning", dedupTestStart + 1},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			seen := map[string]dedupStamp{}
			try(seen, "p-1", dedupTestChan, "open", "warning", dedupTestStart, t0)

			if ok, _ := try(seen, c.problem, c.chID, c.status, c.sev, c.startedAt, t1); !ok {
				t.Errorf("MEŞRU bildirim yendi: %s. Dedup katmanının tehlikeli "+
					"kipi susturma değil AŞIRI susturmadır — bu haber "+
					"operatöre hiç ulaşmazdı.", c.name)
			}
		})
	}
}

// TestNewStartedAtIsANewEvent — v0.9.825.
//
// forgetProblem, flap'i çözülme bildirimi GİDERSE hallediyordu. Ama
// çözülme kanal süzgecine (quietHours / minSeverity / matchRules)
// takılırsa hiç gitmez ve ilk açılışın damgası yerinde kalırdı — yeniden
// açılış sessizce yutulurdu. started_at anahtarda olduğu için bu artık
// forgetProblem'e BAĞLI DEĞİL.
func TestNewStartedAtIsANewEvent(t *testing.T) {
	seen := map[string]dedupStamp{}
	t0 := time.Unix(1_760_000_000, 0)

	try(seen, "p-1", dedupTestChan, "open", "critical", dedupTestStart, t0)
	// forgetProblem ÇAĞRILMIYOR — çözülme bildirimi hiç gitmemiş sayılıyor.
	if ok, _ := try(seen, "p-1", dedupTestChan, "open", "critical",
		dedupTestStart+int64(time.Minute), t0.Add(2*time.Minute)); !ok {
		t.Error("yeni started_at ile yeniden açılış bastırıldı — flap, çözülme " +
			"bildirimi kanal süzgecine takıldığında da görünmeli")
	}
}

// TestResolveClearsSuppression — flap. Aynı kimlikle açıl-çözül-yeniden
// açıl: ikinci açılış MEŞRU bir haberdir.
func TestResolveClearsSuppression(t *testing.T) {
	seen := map[string]dedupStamp{}
	t0 := time.Unix(1_760_000_000, 0)

	try(seen, "p-1", dedupTestChan, "open", "warning", dedupTestStart, t0)
	// Çözülme geldi → problemin tüm bastırma durumu geçersiz.
	forgetProblemNotifications(seen, "p-1")

	if ok, _ := try(seen, "p-1", dedupTestChan, "open", "warning", dedupTestStart,
		t0.Add(2*time.Minute)); !ok {
		t.Error("çözülmeden sonra AYNI kimlikle yeniden açılış bastırıldı — " +
			"flap operatörün görmesi gereken bir olaydır")
	}
}

// TestForgetOnlyTouchesOneProblem — önek taraması komşuyu silmemeli.
// Kimliklerimizde iki nokta var ("shared-exc:<tip>:<kova>"); ayırıcı
// NUL olmasaydı bu vaka çakışırdı.
func TestForgetOnlyTouchesOneProblem(t *testing.T) {
	seen := map[string]dedupStamp{}
	t0 := time.Unix(1_760_000_000, 0)
	// Önek olarak BENZEŞEN komşu: ilkinin adı ikincisinin başlangıcı.
	try(seen, "shared-exc:java.sql.SQLException:100", dedupTestChan, "open", "warning", dedupTestStart, t0)
	try(seen, "shared-exc:java.sql.SQLException:1000", dedupTestChan, "open", "warning", dedupTestStart, t0)
	neighbour := notifyStateKey("shared-exc:java.sql.SQLException:1000", dedupTestChan, "open", dedupTestStart)

	forgetProblemNotifications(seen, "shared-exc:java.sql.SQLException:100")

	if _, still := seen[neighbour]; !still {
		t.Error("komşu problemin bastırma durumu da silindi — önek taraması " +
			"sızıyor; NUL ayırıcı bunu engellemeliydi")
	}
}

// TestKeyLayersNeverCollide — iki anahtar uzayı çakışırsa bir katman
// diğerinin damgasını ezer ve kapı sessizce yanlış çalışır. Kontrol
// baytları (\x01 / \x02) tam olarak bunu engelliyor.
func TestKeyLayersNeverCollide(t *testing.T) {
	// Ciddiyet BOŞ olduğunda exact anahtar, durum anahtarına en çok
	// benzediği hâle gelir — çakışacaksa burada çakışır.
	exact := notifyDedupKey("p-1", dedupTestChan, "open", "", dedupTestStart)
	state := notifyStateKey("p-1", dedupTestChan, "open", dedupTestStart)
	if exact == state {
		t.Fatal("exact ve durum anahtarları çakıştı — bir katman diğerinin " +
			"damgasını ezer ve taban sessizce yanlış çalışır")
	}
}

// TestSuppressionCountDrivesLogging — log seli KARŞI ÖNLEMİ.
//
// Her bastırmayı loglamak, tam da söndürdüğümüz v0.9.585 selini log
// tarafında yeniden kurardı. Sayaç sözleşmesi: bastırmada 1,2,3…
// (çağıran yalnız 1'de loglar), izin verilen gönderimde ÖNCEKİ
// pencerede yutulan sayı (çağıran >0 ise loglar).
func TestSuppressionCountDrivesLogging(t *testing.T) {
	seen := map[string]dedupStamp{}
	t0 := time.Unix(1_760_000_000, 0)

	try(seen, "p-1", dedupTestChan, "open", "warning", dedupTestStart, t0)
	for i, want := range []int{1, 2, 3} {
		ok, got := try(seen, "p-1", dedupTestChan, "open", "warning", dedupTestStart,
			t0.Add(time.Duration(i+1)*time.Minute))
		if ok {
			t.Fatalf("%d. tekrar geçmemeliydi", i+1)
		}
		if got != want {
			t.Errorf("bastırma sayacı %d, %d bekleniyordu — çağıran yalnız "+
				"1'de logluyor; sayaç bozuksa ya hiç log çıkmaz ya da sel geri gelir", got, want)
		}
	}
	ok, prev := try(seen, "p-1", dedupTestChan, "open", "warning", dedupTestStart,
		t0.Add(70*time.Minute))
	if !ok {
		t.Fatal("cooldown kapandı, geçmeliydi")
	}
	if prev != 3 {
		t.Errorf("geçen gönderim önceki pencerede yutulan sayıyı taşımalı: %d, 3 bekleniyordu", prev)
	}
}

// TestSweepBoundsTheMap — uzun ömürlü worker'da sessiz sızıntı olmasın.
func TestSweepBoundsTheMap(t *testing.T) {
	seen := map[string]dedupStamp{}
	t0 := time.Unix(1_760_000_000, 0)
	for i := 0; i < 100; i++ {
		try(seen, string(rune('a'+i%26))+string(rune(i)), dedupTestChan, "open", "warning", dedupTestStart, t0)
	}
	try(seen, "taze", dedupTestChan, "open", "warning", dedupTestStart, t0.Add(70*time.Minute))
	fresh := notifyStateKey("taze", dedupTestChan, "open", dedupTestStart)

	sweepNotifyDedup(seen, t0.Add(70*time.Minute), notifySameStateCooldown)

	if _, ok := seen[fresh]; !ok {
		t.Error("süpürme TAZE damgayı da attı — bir sonraki gönderim yanlışlıkla geçer")
	}
	// Taze gönderim İKİ damga bırakır (exact + durum); eskilerin hepsi gitmeli.
	if len(seen) != 2 {
		t.Errorf("süpürmeden sonra %d damga kaldı, 2 bekleniyordu (taze exact + durum) "+
			"— süresi dolmuşlar birikiyor", len(seen))
	}
}

// TestSweepUsesTheLongWindow — SÜPÜRME PENCERESİ sözleşmesi.
//
// Süpürme kısa pencereyle (15 dk) koşarsa henüz YAŞAYAN bir durum
// damgasını (1 sa) atar ve fırtına deliği yeniden açılır. Bu, harita
// notifyDedupMaxKeys'i aştığında sessizce olur — hiçbir davranış testi
// yakalamaz çünkü kimse 8192 anahtarlı bir senaryo kurmuyor.
func TestSweepUsesTheLongWindow(t *testing.T) {
	src := readGoSourceNoComments(t, "problem_dedup.go")
	i := strings.Index(src, "func (n *Notifier) allowChannelSend(")
	if i < 0 {
		t.Fatal("allowChannelSend bulunamadı — test bayatladı")
	}
	body := src[i:]
	if j := strings.Index(body[1:], "\nfunc "); j >= 0 {
		body = body[:j+1]
	}
	if !strings.Contains(body, "sweepNotifyDedup(n.dedupSeen, now, notifySameStateCooldown)") {
		t.Error("süpürme UZUN pencereyle çağrılmıyor — harita sınırı aşıldığında " +
			"yaşayan durum damgaları atılır ve ciddiyet salınımı yeniden fırtına üretir")
	}
}

// TestEmptyProblemIDFailsOpen — kimliğini bilmediğimiz bir şeyi
// bastırmak yanlış tarafa düşmektir. Kapının işi gürültüyü kırpmak,
// haber kaybetmek değil.
func TestEmptyProblemIDFailsOpen(t *testing.T) {
	n := &Notifier{}
	for i := 0; i < 5; i++ {
		if ok, _ := n.allowChannelSend("", dedupTestChan, "open", "warning", dedupTestStart); !ok {
			t.Fatal("kimliksiz problem bastırıldı — açık geçmeliydi")
		}
	}
}

// TestSeverityRankOrdering — basamak sırası eskalasyon deliğinin tek
// dayanağı. Bilinmeyen etiket 0 alır: hiçbir zaman "yükselme" sayılmaz,
// yani bir yazım hatası fırtına deliğini açamaz.
func TestSeverityRankOrdering(t *testing.T) {
	cases := []struct {
		sev  string
		want int
	}{
		{"critical", 3}, {"CRITICAL", 3}, {" critical ", 3},
		{"warning", 2}, {"info", 1},
		{"", 0}, {"bilinmeyen", 0},
	}
	for _, c := range cases {
		if got := severityRank(c.sev); got != c.want {
			t.Errorf("severityRank(%q) = %d, %d bekleniyordu", c.sev, got, c.want)
		}
	}
}

// TestTeamMailClaimIsHeldAcrossTheSend — v0.9.825 ÇİFT EKİP-MAİLİ.
//
// Kalıcı kapı (notification_log) kontrol-et-sonra-yap; talep okumadan
// ÖNCE alınmazsa iki eşzamanlı çağrı da "gitmemiş" görür. v0.9.513
// yalnız AI-bekleme dalını kilitlemişti; senkron dal korumasızdı.
func TestTeamMailClaimIsHeldAcrossTheSend(t *testing.T) {
	n := &Notifier{}
	if !n.claimTeamMail("p-1") {
		t.Fatal("ilk talep alınmalı")
	}
	if n.claimTeamMail("p-1") {
		t.Error("ikinci eşzamanlı talep GEÇTİ — çift ekip-maili tam buradan çıkıyordu")
	}
	// Farklı problem etkilenmemeli.
	if !n.claimTeamMail("p-2") {
		t.Error("başka bir problemin talebi engellendi — kilit fazla geniş")
	}
	n.releaseTeamMail("p-1")
	if !n.claimTeamMail("p-1") {
		t.Error("bırakıldıktan sonra yeniden alınamadı — bırakılmayan talep " +
			"o problemin mailini süreç ömrü boyunca sessizce öldürür")
	}
	// Kimliksiz asla kilitlenmez: haber kaybetmektense geç.
	if !n.claimTeamMail("") || !n.claimTeamMail("") {
		t.Error("kimliksiz problem kilitlendi — açık geçmeliydi")
	}
}

// TestTeamMailClaimTakenBeforeTheLogRead — KONUM sözleşmesi.
//
// Talep HasNotification'dan SONRA alınırsa yarış penceresi aynen durur
// ve bu hiçbir davranış testinde görünmez (tek-goroutine testinde ikisi
// de doğru sırayla çalışır gibi görünür).
func TestTeamMailClaimTakenBeforeTheLogRead(t *testing.T) {
	src := readGoSourceNoComments(t, "notify.go")
	i := strings.Index(src, "func (n *Notifier) sendTeamMail(")
	if i < 0 {
		t.Fatal("sendTeamMail bulunamadı — test bayatladı")
	}
	body := src[i:]
	if j := strings.Index(body[1:], "\nfunc "); j >= 0 {
		body = body[:j+1]
	}
	claim := strings.Index(body, "claimTeamMail(")
	read := strings.Index(body, "HasNotification(")
	if claim < 0 {
		t.Fatal("sendTeamMail süreç-içi talebi almıyor — çift mail yarışı açık")
	}
	if read < 0 {
		t.Fatal("sendTeamMail kalıcı kapıyı okumuyor — test bayatladı")
	}
	if claim > read {
		t.Error("talep, notification_log okumasından SONRA alınıyor — yarış " +
			"penceresi aynen duruyor; iki eşzamanlı çağrı da 'gitmemiş' görür")
	}
}

// TestDedupGateIsOnTheProblemFanoutNotSendOne — KONUM sözleşmesi.
//
// sendOne "Test gönder" ve runbook-tamamlandı yollarını da taşıyor.
// Onlar tekrarlanan DURUM BİLDİRİMİ değil, OPERATÖR EYLEMİ: admin
// düğmeye iki kez basarsa iki test gitmeli. Kapı sendOne'a kayarsa bu
// sessizce bozulur — hiçbir davranış testi yakalamaz, çünkü kimse
// "düğmeye iki kez bas"ı test etmiyor.
func TestDedupGateIsOnTheProblemFanoutNotSendOne(t *testing.T) {
	src := readGoSourceNoComments(t, "notify.go")

	// sendOne gövdesi: kapı ORADA olmamalı.
	i := strings.Index(src, "func (n *Notifier) sendOne(")
	if i < 0 {
		t.Fatal("sendOne bulunamadı — test bayatladı")
	}
	body := src[i:]
	if j := strings.Index(body[1:], "\nfunc "); j >= 0 {
		body = body[:j+1]
	}
	if strings.Contains(body, "allowChannelSend") {
		t.Error("tekrar kapısı sendOne'a kaymış. Oradan 'Test gönder' ve " +
			"runbook-tamamlandı da geçiyor; admin düğmeye iki kez bastığında " +
			"ikinci test sessizce yutulur.")
	}

	// SendProblemAlert gövdesi: kapı BURADA olmalı.
	i = strings.Index(src, "func (n *Notifier) SendProblemAlert(")
	if i < 0 {
		t.Fatal("SendProblemAlert bulunamadı — test bayatladı")
	}
	body = src[i:]
	if j := strings.Index(body[1:], "\nfunc "); j >= 0 {
		body = body[:j+1]
	}
	if !strings.Contains(body, "allowChannelSend") {
		t.Error("kanal yayılımında tekrar kapısı YOK — takılı bir dedektör " +
			"yine dakikada bir sayfa gönderir (v0.9.575, v0.9.585)")
	}
	if !strings.Contains(body, "forgetProblem(") {
		t.Error("çözülmede bastırma durumu temizlenmiyor — aynı kimlikle " +
			"yeniden açılan (flap) problem yutulur")
	}
	// started_at anahtarın parçası olmalı (v0.9.825).
	if !strings.Contains(body, "p.StartedAt") {
		t.Error("allowChannelSend'e started_at geçilmiyor — kapanıp yeniden " +
			"açılan bir problemin ikinci açılışı ilk açılışın damgasına takılır")
	}
}

var goLineComment = regexp.MustCompile(`(?m)^\s*//.*$`)

// readGoSourceNoComments — kaynağı YORUMSUZ okur.
//
// Bu oturumda iki kez, kaynak-tarama testi kendi düzeltmesinin
// açıklayıcı yorumunda alıntılanan ESKİ kodla eşleşip yanlış yeşil
// verdi. Yorumları atmak o sınıfı kapatıyor.
func readGoSourceNoComments(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile(name)
	if err != nil {
		t.Fatalf("%s okunamadı: %v", name, err)
	}
	return goLineComment.ReplaceAllString(string(b), "")
}
