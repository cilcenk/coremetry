package notify

// v0.9.1344 — "kimseye gitmedi" görünürlüğü.
//
// SEMPTOM (operatör): hiçbir bildirim kanalıyla eşleşmeyen bir problem
// SESSİZCE düşüyordu. `grep -rn "unmatched\|noChannel" internal/notify/`
// hiçbir şey döndürmüyordu: ne sayaç, ne log, ne işaret. Operatörün
// cümlesiyle: "Oracle doluyor ve kimse haber almıyor."
//
// İKİ YOL DA KAPALI BİTİYORDU:
//   1. Kanal döngüsü — MatchRules.Services ile daraltılmış bir kanal,
//      db-konulu bir problemle (db:oracle@corebank-scan.prod, v0.9.1338)
//      asla eşleşmez → `continue`, iz yok.
//   2. Ekip-yönlendirme maili — yorumu "kanal yokken bile koşar" diyor,
//      emniyet ağı gibi okunuyor. DEĞİL: alıcıları
//      GetServiceMetadata(p.Service)'ten çözüyor, db konusunun katalog
//      satırı yok → md nil → ekip yok → mail yok.
//
// Bu test yönlendirmeyi DEĞİL, o iki yolun sonucunun doğru
// SINIFLANDIRILDIĞINI pinler — ve en çok da "hiç yapılandırılmamış"
// ile "yapılandırılmış ama eşleşmedi" ayrımını: ikisi tek kovada
// olsaydı kanalsız taze bir kurulum ilk günden her problemde alarm
// verir, sinyal ilk hafta kapatılırdı.

import (
	"strings"
	"testing"
	"time"

	"github.com/cilcenk/coremetry/internal/chstore"
)

func TestDecideRouting(t *testing.T) {
	cases := []struct {
		name  string
		facts routingFacts
		want  routingVerdict
	}{
		// ── KUSUR DEĞİL: hiçbir yol teklif edilmedi ──────────────────
		{
			// Taze kurulum. Bu satır routingUnmatched'e kayarsa özellik
			// ilk günden gürültü üretir ve kapatılır.
			name:  "hiç kanal yok + ekip-yönlendirme kapalı → unconfigured",
			facts: routingFacts{ChannelsOffered: 0, Team: teamMailOff},
			want:  routingUnconfigured,
		},
		{
			// Ciddiyet süzgeci operatörün BEYANIdır: "bu ciddiyeti bana
			// getirme". EnabledChannelsForSeverity zaten eledi.
			name:  "kanallar var ama bu ciddiyeti almıyor + ekip kapalı → unconfigured",
			facts: routingFacts{ChannelsOffered: 0, Team: teamMailOff},
			want:  routingUnconfigured,
		},

		// ── KUSUR: yol teklif edildi, kimse almadı ───────────────────
		{
			// RAPORLANAN KUSUR, BİRİNCİ YARI. Services ile daraltılmış
			// üç kanal bu ciddiyeti alıyor, hiçbiri db konusuyla
			// eşleşmiyor.
			name:  "kanal teklif edildi, hiçbiri eşleşmedi, ekip kapalı → unmatched",
			facts: routingFacts{ChannelsOffered: 3, Team: teamMailOff},
			want:  routingUnmatched,
		},
		{
			// RAPORLANAN KUSUR, İKİNCİ YARI. Kanal hiç yok ama
			// ekip-yönlendirme AÇIK ve katalog satırı olmadığı için
			// kimseyi çözemedi. Emniyet ağı sanılan yol tam burada
			// kopuyor.
			name:  "kanal yok, ekip-yönlendirme açık ama alıcı çözülemedi → unmatched",
			facts: routingFacts{ChannelsOffered: 0, Team: teamMailNoRecipients},
			want:  routingUnmatched,
		},
		{
			name:  "kanal eşleşmedi VE ekip alıcı bulamadı → unmatched",
			facts: routingFacts{ChannelsOffered: 2, Team: teamMailNoRecipients},
			want:  routingUnmatched,
		},

		// ── Haber gitti ──────────────────────────────────────────────
		{
			name:  "bir kanal eşleşti ve gönderildi → delivered",
			facts: routingFacts{ChannelsOffered: 3, Sends: 1, Team: teamMailOff},
			want:  routingDelivered,
		},
		{
			// Kanal gitti; ekip-mailinin boş çıkması kusur DEĞİL —
			// operatör sayfayı aldı.
			name:  "kanal gönderildi + ekip alıcı bulamadı → delivered",
			facts: routingFacts{ChannelsOffered: 1, Sends: 1, Team: teamMailNoRecipients},
			want:  routingDelivered,
		},
		{
			// Hiç kanal yok ama ekip-maili gitti: emniyet ağı ÇALIŞTIĞI
			// hâl. Bu satır düşerse "delivered" dalından teamMailSent
			// çıkarılmış demektir.
			name:  "kanal yok ama ekip-maili gitti → delivered",
			facts: routingFacts{ChannelsOffered: 0, Team: teamMailSent},
			want:  routingDelivered,
		},

		// ── Bastırıldı: daha önce haber verilmişti ───────────────────
		{
			// Kritik: bu satır unmatched'e kayarsa aynı problem HER
			// evaluator tikinde "kimseye gitmedi" der.
			name:  "eşleşti ama tekrar-bastırma yuttu → suppressed",
			facts: routingFacts{ChannelsOffered: 2, Suppressed: 2, Team: teamMailOff},
			want:  routingSuppressed,
		},
		{
			// Ciddiyet yükselmesi: ekip-maili ilk açılışta gitmişti.
			name:  "kanal eşleşmedi ama ekip-maili zaten gitmişti → suppressed",
			facts: routingFacts{ChannelsOffered: 2, Team: teamMailAlreadySent},
			want:  routingSuppressed,
		},
		{
			// Sıra kontrolü: gerçek bir gönderim, bastırmayı YENER.
			name:  "biri bastırıldı biri gitti → delivered",
			facts: routingFacts{ChannelsOffered: 2, Sends: 1, Suppressed: 1, Team: teamMailOff},
			want:  routingDelivered,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := decideRouting(tc.facts); got != tc.want {
				t.Errorf("decideRouting(%+v) = %v, istenen %v", tc.facts, got, tc.want)
			}
		})
	}
}

// TestTeamMailReach — raporlanan kusurun ikinci yarısı. sendTeamMail'in
// geri kalanı (CH + SMTP + dedup) canlı Store istiyor; kararın kendisi
// bu saf fonksiyonda.
func TestTeamMailReach(t *testing.T) {
	full := chstore.TeamContacts{
		Enabled:     true,
		MinSeverity: "warning",
		Contacts:    map[string]string{"ug-corebank": "ug@bank.local", "sy-core": "sy@bank.local"},
	}
	svc := &chstore.ServiceMetadata{OwnerTeam: "ug-corebank", SRETeam: "sy-core"}

	cases := []struct {
		name     string
		tc       chstore.TeamContacts
		md       *chstore.ServiceMetadata
		severity string
		wantN    int
		want     teamMailOutcome
	}{
		{
			// DB KONUSU. `db:oracle@corebank-scan.prod` katalogda YOK →
			// GetServiceMetadata nil döner. Emniyet ağı sanılan yolun
			// koptuğu tam nokta.
			name: "katalog satırı yok (db konusu) → noRecipients",
			tc:   full, md: nil, severity: "critical",
			wantN: 0, want: teamMailNoRecipients,
		},
		{
			name: "katalog var ama ekip adı boş → noRecipients",
			tc:   full, md: &chstore.ServiceMetadata{}, severity: "critical",
			wantN: 0, want: teamMailNoRecipients,
		},
		{
			name: "ekip adı var ama adresi tanımsız → noRecipients",
			tc:   full, md: &chstore.ServiceMetadata{OwnerTeam: "ug-bilinmeyen"}, severity: "critical",
			wantN: 0, want: teamMailNoRecipients,
		},
		{
			// Vida kapalı: operatörün beyanı, kusur değil. Bu satır
			// noRecipients'a kayarsa ekip-yönlendirmeyi hiç kullanmayan
			// her kurulum kusurlu görünür.
			name: "vida kapalı → off (kusur değil)",
			tc:   chstore.TeamContacts{Enabled: false, Contacts: full.Contacts}, md: svc, severity: "critical",
			wantN: 0, want: teamMailOff,
		},
		{
			// Ciddiyet süzgeci de bir beyandır.
			name: "ciddiyet eşiğin altında → off (kusur değil)",
			tc:   chstore.TeamContacts{Enabled: true, MinSeverity: "critical", Contacts: full.Contacts},
			md:   svc, severity: "warning",
			wantN: 0, want: teamMailOff,
		},
		{
			name: "sahip + SRE çözüldü → ulaşılabilir",
			tc:   full, md: svc, severity: "critical",
			wantN: 2, want: teamMailSent,
		},
		{
			// Kapalı vida, çözülemeyen alıcıdan ÖNCE gelir: sıra
			// değişirse bu satır noRecipients döner.
			name: "vida kapalı VE alıcı yok → off kazanır",
			tc:   chstore.TeamContacts{Enabled: false}, md: nil, severity: "critical",
			wantN: 0, want: teamMailOff,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			to, got := teamMailReach(tc.tc, tc.md, tc.severity)
			if got != tc.want {
				t.Errorf("teamMailReach sonucu = %d, istenen %d", got, tc.want)
			}
			if len(to) != tc.wantN {
				t.Errorf("alıcı sayısı = %d (%v), istenen %d", len(to), to, tc.wantN)
			}
		})
	}
}

// TestRoutingReason — gerekçe operatörün TEK bağlamı (notification_log
// error sütunu → /problems triyaj paneli). "Eşleşme yok" demek yetmez;
// haberin NEREDE kaybolduğunu söylemeli.
func TestRoutingReason(t *testing.T) {
	cases := []struct {
		name  string
		facts routingFacts
		// mustContain — gerekçenin taşıması ZORUNLU parçalar.
		mustContain []string
	}{
		{
			name:        "kanallar eşleşmedi, ekip devrede değil",
			facts:       routingFacts{ChannelsOffered: 3, Team: teamMailOff},
			mustContain: []string{"3 kanal", "eşleşme kuralı", "devrede değil"},
		},
		{
			name:        "kanal yok, ekip alıcı bulamadı",
			facts:       routingFacts{ChannelsOffered: 0, Team: teamMailNoRecipients},
			mustContain: []string{"etkin kanal yok", "katalog kaydı"},
		},
		{
			name:        "her iki yol da kapalı bitti",
			facts:       routingFacts{ChannelsOffered: 2, Team: teamMailNoRecipients},
			mustContain: []string{"2 kanal", "katalog kaydı"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := routingReason(tc.facts)
			for _, want := range tc.mustContain {
				if !strings.Contains(got, want) {
					t.Errorf("gerekçe %q parçasını taşımıyor:\n  %s", want, got)
				}
			}
		})
	}
}

// TestBumpRouting — sayacın doğru kovaya düştüğü ve son eşleşmeyenin
// kimliğinin YALNIZ unmatched'te güncellendiği. /admin/stats operatöre
// "kaç problem kimseye gitmedi" sorusunu bu sayaçla cevaplıyor.
func TestBumpRouting(t *testing.T) {
	resetRoutingObservability()
	t.Cleanup(resetRoutingObservability)

	now := timeAt(1_700_000_000)
	bumpRouting(routingUnconfigured, "p1", "svc-a", "", now)
	bumpRouting(routingDelivered, "p2", "svc-b", "", now)
	bumpRouting(routingUnmatched, "p3", "db:oracle@corebank-scan.prod", "gerekçe-3", now)
	bumpRouting(routingSuppressed, "p4", "svc-c", "", now)
	bumpRouting(routingUnmatched, "p5", "db:oracle@corebank-scan.prod", "gerekçe-5", now)

	s := RoutingObservability()
	if s.Unmatched != 2 {
		t.Errorf("Unmatched = %d, istenen 2", s.Unmatched)
	}
	if s.Unconfigured != 1 || s.Delivered != 1 || s.Suppressed != 1 {
		t.Errorf("kovalar yanlış: %+v", s)
	}
	// Kovalar BİRBİRİNİ DIŞLAR: unconfigured, unmatched'i artırmamalı.
	// Bu kontrol düşerse "kusur değil" olan hâl kusur sayılıyor demektir.
	if s.Unconfigured+s.Delivered+s.Suppressed+s.Unmatched != 5 {
		t.Errorf("toplam 5 olmalı: %+v", s)
	}
	if s.LastUnmatchedID != "p5" || s.LastUnmatchedReason != "gerekçe-5" {
		t.Errorf("son eşleşmeyen p5 olmalı: %+v", s)
	}
	if s.LastUnmatchedService != "db:oracle@corebank-scan.prod" {
		t.Errorf("son eşleşmeyen konu yanlış: %q", s.LastUnmatchedService)
	}
	if s.LastUnmatchedUnix != 1_700_000_000 {
		t.Errorf("son eşleşmeyen anı = %d", s.LastUnmatchedUnix)
	}
}

// timeAt — test yardımcısı; unix saniyeden time.Time.
func timeAt(sec int64) time.Time { return time.Unix(sec, 0) }

// TestRoutingFunnelWiring — SendProblemAlert'in KABLOLAMA sözleşmesi.
//
// NEDEN KAYNAK OKUYAN BİR TEST: yönlendirme kararının kendisi saf ve
// yukarıda tablo testli, ama o kararı BESLEYEN sayımlar SendProblemAlert
// içinde ve o fonksiyon canlı bir *chstore.Store istiyor (kanal listesi,
// ekip ayarları, SMTP, CH yazımı) — davranışsal olarak koşturulamıyor.
//
// Mutasyon taraması bu boşluğu ölçtü: aşağıdaki beş artırım/çağrının
// HER BİRİ silindiğinde tüm paket testleri YEŞİL kalıyordu. Sonuçları
// sessizce yanlış olurdu:
//
//   - facts.Suppressed++ silinirse → tekrar bastırılan her problem
//     "kimseye gitmedi" sayılır; gürültü tam da kalabalık kurulumlarda
//     patlar.
//   - facts.Sends++ silinirse → BAŞARIYLA gönderilen her problem
//     "kimseye gitmedi" sayılır. Sinyalin tersine dönmesi.
//   - facts.ChannelsOffered silinirse → her şey "yapılandırılmamış"
//     görünür ve kusur BİR DAHA ASLA raporlanmaz (sessiz körlük —
//     düzeltmenin kendini iptal etmesi).
//   - recordRouting çağrılmazsa → özellik hiç çalışmaz.
//   - işaretin tekrar kapısı silinirse → her evaluator tikinde bir
//     notification_log satırı.
//
// Emsal: TestPriorityComputedOnceInTheFunnel (alert_title_test.go) —
// aynı funnel, aynı gerekçe, aynı idiom.
func TestRoutingFunnelWiring(t *testing.T) {
	src := readGoSourceNoComments(t, "notify.go")

	body := funcBody(t, src, "func (n *Notifier) SendProblemAlert(")
	loop := strings.Index(body, "for _, c := range channels")
	if loop < 0 {
		t.Fatal("kanal döngüsü tanınamadı — test bayatladı, funnel yeniden yazılmış olabilir")
	}

	// Sayımlar döngünün İÇİNDE olmalı: dışarıda tek sefer artan bir
	// sayaç kanal başına kararı temsil edemez.
	for _, want := range []string{"facts.Suppressed++", "facts.Sends++"} {
		at := strings.Index(body, want)
		if at < 0 {
			t.Errorf("%s funnel'da yok — sonuç sınıflandırması sessizce yanlışa döner", want)
			continue
		}
		if at < loop {
			t.Errorf("%s kanal döngüsünden ÖNCE — kanal başına karar temsil edilmiyor", want)
		}
	}

	// Sends, sendOne'dan ÖNCE artmalı: sayılan şey YÖNLENDİRME, teslimat
	// değil. Ölü bir SMTP rölesi "kimseye gitmedi" DEĞİLDİR — o ayrı bir
	// sinyal (ChannelHealth) ve iki arızayı tek kovaya atmak eşleşme
	// kusurunu ölü-kanal gürültüsünde kaybederdi.
	send, one := strings.Index(body, "facts.Sends++"), strings.Index(body, "n.sendOne(")
	if send >= 0 && one >= 0 && send > one {
		t.Error("facts.Sends++ sendOne'dan SONRA — gönderim hatası " +
			"'kimseye gitmedi' sayılır, iki farklı arıza tek kovaya düşer")
	}

	if !strings.Contains(body, "facts.ChannelsOffered = len(channels)") {
		t.Error("ChannelsOffered doldurulmuyor — her problem 'yapılandırılmamış' " +
			"görünür ve kusur BİR DAHA raporlanmaz (sessiz körlük)")
	}

	// İKİ çıkış yolu da kaydetmeli: kanal listesi boş olan erken dönüş
	// ve döngü sonu. Biri unutulursa o yoldaki problemler hiç sayılmaz.
	if n := strings.Count(body, "n.recordRouting("); n < 2 {
		t.Errorf("recordRouting funnel'da %d kez çağrılıyor, en az 2 olmalı "+
			"(kanal-yok erken dönüşü + döngü sonu)", n)
	}

	// İşaretin tekrar kapısı: sahte kanal kimliğiyle allowChannelSend.
	rec := funcBody(t, src, "func (n *Notifier) recordRouting(")
	if !strings.Contains(rec, "allowChannelSend(p.ID, unmatchedChannelID") {
		t.Error("işaret tekrar kapısız yazılıyor — aynı problem her evaluator " +
			"tikinde bir notification_log satırı daha üretir")
	}
	// İşaret, kalıcı dedup'ı ZEHİRLEMEMELİ: HasNotification yalnız ok=1
	// satırları sayar, o yüzden işaret ok=0 (sendErr dolu) yazılmalı.
	if !strings.Contains(rec, "errors.New(reason)") {
		t.Error("işaret satırı sendErr'siz yazılıyor — ok=1 olur ve " +
			"HasNotification'ı yanıltarak ekip-mailini kalıcı olarak susturur")
	}
	// Gerekçe GERÇEKTEN üretilip iki tüketiciye de gitmeli. routingReason
	// tablo testli, ama sabit bir dizgeyle değiştirilmesi hiçbir testi
	// düşürmüyordu (mutasyon M21): operatörün /problems'ta gördüğü tek
	// bağlam bu cümle, sabitlenirse her problem aynı şeyi söyler.
	if !strings.Contains(rec, "routingReason(f)") {
		t.Error("gerekçe routingReason'dan üretilmiyor — işaret satırı ve " +
			"sayaç, haberin NEREDE kaybolduğunu söyleyemez")
	}
	if !strings.Contains(rec, "bumpRouting(v, p.ID, p.Service, reason,") {
		t.Error("bumpRouting hesaplanan verdict/gerekçe ile beslenmiyor — " +
			"/admin/stats sayaçları uydurma bir kovaya düşebilir")
	}
	// Verdict decideRouting'den GELMELİ; sabitlenirse sınıflandırma
	// tamamen anlamını yitirir.
	if !strings.Contains(rec, "v := decideRouting(f)") {
		t.Error("verdict decideRouting'den alınmıyor — sınıflandırma sabitlenmiş olabilir")
	}
}

// funcBody — src içindeki bir fonksiyonun gövdesi (bir sonraki üst
// düzey `func`a kadar). Bulunamazsa test BAYAT sayılır ve düşer:
// sessizce boş dize döndürmek, her iddiayı otomatik geçirirdi.
func funcBody(t *testing.T, src, sig string) string {
	t.Helper()
	i := strings.Index(src, sig)
	if i < 0 {
		t.Fatalf("%q bulunamadı — test bayatladı", sig)
	}
	body := src[i:]
	if j := strings.Index(body[1:], "\nfunc "); j >= 0 {
		body = body[:j+1]
	}
	return body
}
