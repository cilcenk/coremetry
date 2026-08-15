// v0.9.828 — bildirimde öncelik etiketi + tek başlık kaynağı.
package notify

import (
	"os"
	"strings"
	"testing"
	"time"

	"github.com/cilcenk/coremetry/internal/chstore"
)

func probe(sev, priority string) chstore.Problem {
	return chstore.Problem{
		ID: "p-1", Service: "payments", RuleName: "Error rate > 5%",
		Severity: sev, Priority: priority, Metric: "error_rate",
		Value: 12, Threshold: 5, Status: "open",
		StartedAt: time.Now().Add(-time.Minute).UnixNano(),
	}
}

// TestAlertTitleCarriesBothTags — konu satırının SÖZLEŞMESİ.
//
// Ciddiyet ve öncelik FARKLI şeyler söylüyor ve ikisi de gerekli:
// ciddiyet "ne kadar kötü", öncelik "ne kadar acil". Nöbetçi telefonda
// on tane [CRITICAL] arasından hangisinin kalkmayı gerektirdiğini
// ikincisinden ayırt ediyor.
func TestAlertTitleCarriesBothTags(t *testing.T) {
	cases := []struct {
		sev, priority, want string
	}{
		{"critical", "P1", "[CRITICAL][P1] payments — Error rate > 5%"},
		{"critical", "P2", "[CRITICAL][P2] payments — Error rate > 5%"},
		{"warning", "P3", "[WARNING][P3] payments — Error rate > 5%"},
		// Öncelik hesaplanmamış → ETİKETSİZ, uydurulmuş değil. Yanlış bir
		// öncelik etiketi hiç etiket olmamasından KÖTÜDÜR: nöbetçi ona
		// bakıp karar veriyor.
		{"critical", "", "[CRITICAL] payments — Error rate > 5%"},
		{"critical", "bilinmeyen", "[CRITICAL] payments — Error rate > 5%"},
	}
	for _, c := range cases {
		if got := alertTitle(probe(c.sev, c.priority)); got != c.want {
			t.Errorf("alertTitle(%q,%q) = %q, %q bekleniyordu", c.sev, c.priority, got, c.want)
		}
	}
}

// TestEveryChannelFormatCarriesThePriority — TÜM KANAL BİÇİMLERİ.
//
// Etiketi kanal başına ayrı ayrı eklemek, birinde unutulduğunda
// kimsenin fark etmeyeceği bir tutarsızlık üretirdi — üstelik en çok
// bakılan kanalda (Slack) eksik olabilirdi. Bu test her biçimi tek tek
// geziyor.
func TestEveryChannelFormatCarriesThePriority(t *testing.T) {
	p := probe("critical", "P1")
	p.PriorityReason = "critical + 2.4x threshold"
	n := &Notifier{}

	t.Run("e-posta konusu", func(t *testing.T) {
		if !strings.Contains(alertTitle(p), "[P1]") {
			t.Error("konu satırında öncelik yok")
		}
	})
	t.Run("e-posta düz metin gövdesi", func(t *testing.T) {
		body := n.buildEmailBody(p, nil)
		if !strings.Contains(body, "P1") {
			t.Error("düz metin gövdesinde öncelik yok")
		}
		// Gerekçe de gitmeli: "P1" tek başına bir otorite beyanı,
		// operatör onunla tartışabilmeli.
		if !strings.Contains(body, "2.4x threshold") {
			t.Errorf("gövdede öncelik GEREKÇESİ yok:\n%s", body)
		}
	})
	t.Run("e-posta HTML gövdesi", func(t *testing.T) {
		if !strings.Contains(n.buildEmailHTML(p, nil), "P1") {
			t.Error("HTML gövdesinde öncelik yok")
		}
	})
	t.Run("WhatsApp", func(t *testing.T) {
		txt := buildWhatsAppText(p)
		if !strings.Contains(txt, "[P1]") {
			t.Errorf("WhatsApp metninde öncelik yok:\n%s", txt)
		}
	})
	t.Run("notification_log kaydı", func(t *testing.T) {
		// /events'te görünen konu, GÖNDERİLEN konuyla aynı olmalı.
		if notificationSubject(p) != alertTitle(p) {
			t.Errorf("kayıt konusu %q, gönderilen %q — /events yanlış konu gösterir",
				notificationSubject(p), alertTitle(p))
		}
	})
	t.Run("Slack/Teams/Zoom alan değeri", func(t *testing.T) {
		f := priorityField(p)
		if !strings.Contains(f, "P1") || !strings.Contains(f, "2.4x threshold") {
			t.Errorf("alan değeri %q — öncelik + gerekçe taşımalı", f)
		}
	})
}

// TestPriorityFieldDegradesHonestly — öncelik yoksa alan kaybolmamalı.
// Teams kartında alan sayısının değişmesi hizayı bozar; bir tire
// "bilinmiyor"un dürüst gösterimi.
func TestPriorityFieldDegradesHonestly(t *testing.T) {
	if got := priorityField(probe("critical", "")); got != "—" {
		t.Errorf("önceliksiz alan %q, tire bekleniyordu", got)
	}
	// Gerekçesiz ama öncelikli: yalnız basamak.
	if got := priorityField(probe("critical", "P2")); got != "P2" {
		t.Errorf("gerekçesiz alan %q, 'P2' bekleniyordu", got)
	}
	// HTML rozeti önceliksizken hiçbir şey basmamalı (mevcut biçim korunur).
	if got := priorityBadgeHTML(probe("critical", "")); got != "" {
		t.Errorf("önceliksiz HTML rozeti %q, boş bekleniyordu", got)
	}
}

// TestTitleHasOneSource — TEK KAYNAK sözleşmesi.
//
// Öncesinde ALTI ayrı yer aynı "[SEV] servis — kural" dizgisini kendi
// fmt.Sprintf'iyle kuruyordu. Biri geri eklenirse etiket orada sessizce
// kaybolur ve hiçbir davranış testi yakalamaz (o kanalın testi yoksa).
func TestTitleHasOneSource(t *testing.T) {
	for _, f := range []string{"notify.go", "notification_log.go"} {
		src := readGoSourceNoComments(t, f)
		if strings.Contains(src, `"[%s] %s — %s"`) {
			t.Errorf("%s içinde elle kurulan başlık dizgisi var — alertTitle "+
				"tek kaynak olmalı, yoksa öncelik etiketi o kanalda sessizce kaybolur", f)
		}
	}
}

// TestPriorityComputedOnceInTheFunnel — KONUM sözleşmesi.
//
// Kanal döngüsünün İÇİNDE hesaplansaydı aynı bildirimin iki kanalda
// farklı öncelik göstermesi mümkün olurdu (dört saatlik "açık kritik"
// sınırının tam üstünde, döngü sırasında saat ilerlerken).
func TestPriorityComputedOnceInTheFunnel(t *testing.T) {
	src := readGoSourceNoComments(t, "notify.go")
	i := strings.Index(src, "func (n *Notifier) SendProblemAlert(")
	if i < 0 {
		t.Fatal("SendProblemAlert bulunamadı — test bayatladı")
	}
	body := src[i:]
	if j := strings.Index(body[1:], "\nfunc "); j >= 0 {
		body = body[:j+1]
	}
	calc := strings.Index(body, "withPriority(p)")
	loop := strings.Index(body, "for _, c := range channels")
	team := strings.Index(body, "n.sendTeamMail(")
	if calc < 0 {
		t.Fatal("funnel'da öncelik hesaplanmıyor — etiket hiçbir kanala ulaşmaz")
	}
	if loop < 0 || team < 0 {
		t.Fatal("funnel'ın gövdesi tanınamadı — test bayatladı")
	}
	if calc > loop {
		t.Error("öncelik kanal döngüsünden SONRA/İÇİNDE hesaplanıyor — aynı " +
			"bildirim iki kanalda farklı öncelik gösterebilir")
	}
	if calc > team {
		t.Error("öncelik ekip-mailinden SONRA hesaplanıyor — takıma giden " +
			"mail etiketsiz kalır")
	}
	if strings.Count(body, "withPriority(") != 1 {
		t.Error("öncelik funnel'da birden çok kez hesaplanıyor — tek kaynak olmalı")
	}
}

// TestSendTestIsUnaffected — "Test gönder" yolu bozulmamalı.
// Sentetik test problemi funnel'dan geçmiyor, yani önceliği yok ve
// başlığı etiketsiz olmalı (uydurma bir P etiketi taşımamalı).
func TestSendTestIsUnaffected(t *testing.T) {
	src, err := os.ReadFile("notify.go")
	if err != nil {
		t.Fatalf("notify.go okunamadı: %v", err)
	}
	if !strings.Contains(string(src), `RuleName:    "Test alert from Coremetry"`) {
		t.Skip("SendTest'in sentetik problemi değişmiş — test bayatladı")
	}
	test := chstore.Problem{
		Severity: "warning", Service: "coremetry", RuleName: "Test alert from Coremetry",
	}
	if got := alertTitle(test); strings.Contains(got, "[P") {
		t.Errorf("test bildirimi uydurma öncelik etiketi taşıyor: %q", got)
	}
}
