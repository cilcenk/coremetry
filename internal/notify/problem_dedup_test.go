// v0.9.587 — tekrar tabanının regresyon testi.
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

// TestDedupSuppressesIdenticalRepeat — v0.9.575 / v0.9.585 fırtınası.
// Takılı bir dedektör aynı problemi her tikte (60 sn) yeniden açar.
func TestDedupSuppressesIdenticalRepeat(t *testing.T) {
	seen := map[string]dedupStamp{}
	key := notifyDedupKey("p-1", dedupTestChan, "open", "warning")
	t0 := time.Unix(1_760_000_000, 0)

	if ok, _ := allowNotifySend(seen, key, t0, notifyDedupWindow); !ok {
		t.Fatal("ilk gönderim geçmeli")
	}
	// 60 sn'lik tiklerle 15 dakika: pencere içinde 14 tekrar.
	sent := 0
	for i := 1; i <= 14; i++ {
		if ok, _ := allowNotifySend(seen, key, t0.Add(time.Duration(i)*time.Minute), notifyDedupWindow); ok {
			sent++
		}
	}
	if sent != 0 {
		t.Errorf("pencere içinde %d tekrar geçti, 0 bekleniyordu — fırtına kesilmiyor", sent)
	}
	// Pencere kapanınca YENİDEN geçmeli: tam susturma, hatayı da
	// susturur. Operatör "bu şey hâlâ bağırıyor" diyebilmeli.
	if ok, _ := allowNotifySend(seen, key, t0.Add(16*time.Minute), notifyDedupWindow); !ok {
		t.Error("pencere kapandıktan sonra gönderim yeniden geçmeli — " +
			"kalıcı susturma hatayı gizler")
	}
}

// TestDedupNeverEatsLegitimateAlerts — kapının YEMEMESİ gerekenler.
// Her vaka, dörtlü anahtarın bir bileşenini değiştiriyor.
func TestDedupNeverEatsLegitimateAlerts(t *testing.T) {
	t0 := time.Unix(1_760_000_000, 0)
	// Hepsi ilk gönderimden 1 dk sonra — yani pencere TAM İÇİNDE.
	// Geçiyorlarsa, geçme sebepleri zamanaşımı değil, anahtarın kendisi.
	t1 := t0.Add(time.Minute)

	cases := []struct {
		name                       string
		problem, chID, status, sev string
	}{
		{"eskalasyon warning→critical", "p-1", dedupTestChan, "open", "critical"},
		{"çözülme bildirimi", "p-1", dedupTestChan, "resolved", "warning"},
		{"ikinci kanal", "p-1", "ch-2", "open", "warning"},
		{"başka problem", "p-2", dedupTestChan, "open", "warning"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			seen := map[string]dedupStamp{}
			base := notifyDedupKey("p-1", dedupTestChan, "open", "warning")
			allowNotifySend(seen, base, t0, notifyDedupWindow)

			k := notifyDedupKey(c.problem, c.chID, c.status, c.sev)
			if ok, _ := allowNotifySend(seen, k, t1, notifyDedupWindow); !ok {
				t.Errorf("MEŞRU bildirim yendi: %s. Dedup katmanının tehlikeli "+
					"kipi susturma değil AŞIRI susturmadır — bu haber "+
					"operatöre hiç ulaşmazdı.", c.name)
			}
		})
	}
}

// TestResolveClearsSuppression — flap. Aynı kimlikle açıl-çözül-yeniden
// açıl: ikinci açılış MEŞRU bir haberdir.
func TestResolveClearsSuppression(t *testing.T) {
	seen := map[string]dedupStamp{}
	open := notifyDedupKey("p-1", dedupTestChan, "open", "warning")
	t0 := time.Unix(1_760_000_000, 0)

	allowNotifySend(seen, open, t0, notifyDedupWindow)
	// Çözülme geldi → problemin tüm bastırma durumu geçersiz.
	forgetProblemNotifications(seen, "p-1")

	if ok, _ := allowNotifySend(seen, open, t0.Add(2*time.Minute), notifyDedupWindow); !ok {
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
	mine := notifyDedupKey("shared-exc:java.sql.SQLException:100", dedupTestChan, "open", "warning")
	// Önek olarak BENZEŞEN komşu: ilkinin adı ikincisinin başlangıcı.
	neighbour := notifyDedupKey("shared-exc:java.sql.SQLException:1000", dedupTestChan, "open", "warning")
	allowNotifySend(seen, mine, t0, notifyDedupWindow)
	allowNotifySend(seen, neighbour, t0, notifyDedupWindow)

	forgetProblemNotifications(seen, "shared-exc:java.sql.SQLException:100")

	if _, still := seen[neighbour]; !still {
		t.Error("komşu problemin bastırma durumu da silindi — önek taraması " +
			"sızıyor; NUL ayırıcı bunu engellemeliydi")
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
	key := notifyDedupKey("p-1", dedupTestChan, "open", "warning")
	t0 := time.Unix(1_760_000_000, 0)

	allowNotifySend(seen, key, t0, notifyDedupWindow)
	for i, want := range []int{1, 2, 3} {
		ok, got := allowNotifySend(seen, key, t0.Add(time.Duration(i+1)*time.Minute), notifyDedupWindow)
		if ok {
			t.Fatalf("%d. tekrar geçmemeliydi", i+1)
		}
		if got != want {
			t.Errorf("bastırma sayacı %d, %d bekleniyordu — çağıran yalnız "+
				"1'de logluyor; sayaç bozuksa ya hiç log çıkmaz ya da sel geri gelir", got, want)
		}
	}
	ok, prev := allowNotifySend(seen, key, t0.Add(20*time.Minute), notifyDedupWindow)
	if !ok {
		t.Fatal("pencere kapandı, geçmeliydi")
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
		allowNotifySend(seen, notifyDedupKey(string(rune('a'+i%26))+string(rune(i)), dedupTestChan, "open", "warning"), t0, notifyDedupWindow)
	}
	fresh := notifyDedupKey("taze", dedupTestChan, "open", "warning")
	allowNotifySend(seen, fresh, t0.Add(20*time.Minute), notifyDedupWindow)

	sweepNotifyDedup(seen, t0.Add(20*time.Minute), notifyDedupWindow)

	if _, ok := seen[fresh]; !ok {
		t.Error("süpürme TAZE damgayı da attı — bir sonraki gönderim yanlışlıkla geçer")
	}
	if len(seen) != 1 {
		t.Errorf("süpürmeden sonra %d damga kaldı, 1 bekleniyordu — süresi dolmuşlar birikiyor", len(seen))
	}
}

// TestEmptyProblemIDFailsOpen — kimliğini bilmediğimiz bir şeyi
// bastırmak yanlış tarafa düşmektir. Kapının işi gürültüyü kırpmak,
// haber kaybetmek değil.
func TestEmptyProblemIDFailsOpen(t *testing.T) {
	n := &Notifier{}
	for i := 0; i < 5; i++ {
		if ok, _ := n.allowChannelSend("", dedupTestChan, "open", "warning"); !ok {
			t.Fatal("kimliksiz problem bastırıldı — açık geçmeliydi")
		}
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
