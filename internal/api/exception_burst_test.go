package api

import (
	"strings"
	"testing"
	"time"

	"github.com/cilcenk/coremetry/internal/chstore"
)

// v0.9.627 — operatör-bildirimli: tek servisten 12 dakikada 11.260 olay
// üreten bir exception grubu P2 göründü.
//
// P1'in TEK kapısı "son 5 dakika içinde görülmüş" idi; patlama 13:01'de
// bitti, operatör 13:22'de baktı, 21 dakikalık yaş kapıyı kapattı.
// Ayrıca hız hiç ölçülmüyordu: üç günde birikmiş 11.260 ile on iki
// dakikada patlayan 11.260 aynı kovaya düşüyordu.

func TestExceptionBurstRate(t *testing.T) {
	min := int64(time.Minute)
	cases := []struct {
		name           string
		occ            uint64
		spanNs         int64
		wantRateApprox float64
	}{
		// Operatörün gerçek olayı: 12:49:31 → 13:01:36 = 12dk 5sn.
		{"operatörün olayı", 11260, int64(12*time.Minute + 5*time.Second), 932},
		{"kronik: 3 günde aynı toplam", 11260, int64(72 * time.Hour), 2.6},
		{"tam bir dakika", 600, min, 600},
		// first_seen == last_seen: sıfıra bölme YOK, hız abartılmıyor.
		{"anlık grup (ömür sıfır)", 1500, 0, 1500},
		{"ömür tabandan kısa", 300, int64(10 * time.Second), 300},
	}
	for _, c := range cases {
		got := exceptionBurstRate(c.occ, 0, c.spanNs)
		if diff := got - c.wantRateApprox; diff > 5 || diff < -5 {
			t.Errorf("%s: hız %.1f, beklenen ~%.1f", c.name, got, c.wantRateApprox)
		}
	}
}

func TestExceptionIsBurst(t *testing.T) {
	// v0.9.1188 — tablo artık EŞİĞE GÖRELİ. Öncesi 200/dk'ya çakılıydı ve
	// varsayılan 100'e inince üç vaka kırıldı; oysa bu bug'ın dersi tam
	// olarak "eşik bir sabit değil, bir ayar". Süreleri eşikten türetince
	// test bir sonraki değişiklikte de anlamını koruyor.
	cfg := chstore.DefaultExceptionTriage()
	rate := cfg.BurstMinRate
	total := uint64(cfg.BurstMinTotal)
	// spanFor — `occ` olayı tam olarak `perMin` hızında üretecek süre.
	spanFor := func(occ uint64, perMin float64) int64 {
		return int64(float64(occ) / perMin * float64(time.Minute))
	}

	cases := []struct {
		name   string
		occ    uint64
		spanNs int64
		want   bool
	}{
		{"operatörün olayı (v0.9.627)", 11260, int64(12*time.Minute + 5*time.Second), true},
		// v0.9.1188'in bildirdiği satır: 2.374 olay / 13dk09sn = 180,5/dk.
		// Eski 200'lük kapıyı %10 farkla kaçırıyordu.
		{"v0.9.1188 satırı", 2374, int64(13*time.Minute + 9*time.Second), true},
		// Hacim yüksek ama üç güne yayılmış: kronik, olay değil.
		{"kronik birikim", 11260, int64(72 * time.Hour), false},
		// Hız yüksek ama hacim düşük: taban bunun için var.
		{"küçük ama hızlı", 20, int64(5 * time.Second), false},
		{"taban hacmin hemen altı", total - 1, int64(time.Minute), false},
		{"tam eşikte hız", total, spanFor(total, rate), true},
		// Eşiğin hemen ALTI: %10 aşağıda.
		{"eşiğin hemen altında hız", total, spanFor(total, rate*0.9), false},
		// Eşiğin hemen ÜSTÜ: %10 yukarıda.
		{"eşiğin hemen üstünde hız", total, spanFor(total, rate*1.1), true},
	}
	for _, c := range cases {
		if got := exceptionIsBurst(c.occ, 0, c.spanNs, cfg); got != c.want {
			t.Errorf("%s: isBurst=%v, beklenen %v (hız %.0f/dk, eşik %.0f/dk)",
				c.name, got, c.want, exceptionBurstRate(c.occ, 0, c.spanNs), rate)
		}
	}
}

// Uçtan uca: operatörün ekranındaki satır artık P1 olmalı.
func TestExceptionPriorityOperatorCase(t *testing.T) {
	now := time.Now()
	// Patlama 21 dakika önce BİTTİ — yani eski kuralın freshMin (5dk)
	// kapısı kapalı. Yeni kural onu yine de P1 yapmalı.
	last := now.Add(-21 * time.Minute)
	first := last.Add(-(12*time.Minute + 5*time.Second))

	g := chstore.ExceptionGroup{
		Type:        "com.ibm.msg.client.jakarta.jms.DetailedInvalidDestinationException",
		Service:     "cashflow-prod",
		State:       "new",
		FirstSeen:   first.UnixNano(),
		LastSeen:    last.UnixNano(),
		Occurrences: 11260,
	}
	prio, reason := exceptionPriority(g)
	if prio != "P1" {
		t.Fatalf("12 dakikada 11.260 olay P1 olmalıydı, alınan %s (%s)", prio, reason)
	}
	// Gerekçe DOĞRU olmalı: v0.9.524'ün dersi — sahip olmadığımız
	// pencereli bir sayı uydurmuyoruz. "12dk" grubun kendi ömrü.
	if !strings.Contains(reason, "11,260") || !strings.Contains(reason, "12dk") {
		t.Fatalf("gerekçe hacmi ve gerçek ömrü söylemeli, alınan: %q", reason)
	}
	if strings.Contains(reason, "5min") || strings.Contains(reason, "last 5") {
		t.Fatalf("gerekçe sahip olmadığımız bir pencereyi iddia ediyor: %q", reason)
	}
}

// `regressed` etiketi bir patlamayı P2'de TUTMAMALI: etiket problemin
// geçmişini anlatır, şiddetini değil. Eski sırada regressed erken-dönüşü
// her şeyi gölgeliyordu.
func TestRegressedDoesNotShadowBurst(t *testing.T) {
	now := time.Now()
	last := now.Add(-10 * time.Minute)
	g := chstore.ExceptionGroup{
		State:       "regressed",
		FirstSeen:   last.Add(-12 * time.Minute).UnixNano(),
		LastSeen:    last.UnixNano(),
		Occurrences: 11260,
	}
	if prio, reason := exceptionPriority(g); prio != "P1" {
		t.Fatalf("patlayan regressed grup P1 olmalı, alınan %s (%s)", prio, reason)
	}

	// Patlamayan regressed eski davranışta kalmalı.
	g.Occurrences = 12
	g.FirstSeen = now.Add(-72 * time.Hour).UnixNano()
	if prio, _ := exceptionPriority(g); prio != "P2" {
		t.Fatalf("patlamayan regressed P2 kalmalı, alınan %s", prio)
	}
}

// v0.9.1205 — bu testin ESKİ hâli tam tersini pinliyordu ("bayat
// patlama P1 olmamalı") ve o felsefe operatörce REDDEDİLDİ (sınıfın 5.
// bildirimi): P1'i hak etmiş patlama, susmuş olsa da ele alınana
// (resolve/ignore) dek P1 kalır — kuyruktan düşürmenin yolu zaman
// değil, operatör aksiyonu. Gerekçe bittiğini dürüstçe söyler.
func TestStaleBurstStaysP1(t *testing.T) {
	now := time.Now()
	last := now.Add(-6 * time.Hour)
	g := chstore.ExceptionGroup{
		State:       "new",
		FirstSeen:   last.Add(-12 * time.Minute).UnixNano(),
		LastSeen:    last.UnixNano(),
		Occurrences: 11260,
	}
	prio, reason := exceptionPriority(g)
	if prio != "P1" {
		t.Fatalf("susmuş patlama P1 kalmalı (v0.9.1205 direktifi), alınan %s (%s)", prio, reason)
	}
	if !strings.Contains(reason, "önce bitti") {
		t.Fatalf("gerekçe bittiğini söylemeli: %q", reason)
	}
}

// Var olan P1 kapısı korunmalı: hâlâ AKTİF ama henüz hacim biriktirmemiş
// bir patlama (son 5dk, ≥500) yine P1.
func TestFreshHighVolumeStillP1(t *testing.T) {
	now := time.Now()
	g := chstore.ExceptionGroup{
		State:       "new",
		FirstSeen:   now.Add(-90 * time.Minute).UnixNano(),
		LastSeen:    now.Add(-1 * time.Minute).UnixNano(),
		Occurrences: 600, // 90 dakikaya yayılmış → patlama DEĞİL
	}
	if prio, reason := exceptionPriority(g); prio != "P1" {
		t.Fatalf("taze + yüksek hacim P1 kalmalı, alınan %s (%s)", prio, reason)
	}
}

func TestShortDur(t *testing.T) {
	cases := map[time.Duration]string{
		45 * time.Second:                            "45sn",
		12*time.Minute + 5*time.Second:              "12dk",
		90 * time.Minute:                            "1sa 30dk",
		2 * time.Hour:                               "2sa",
		-5 * time.Second:                            "0sn",
		time.Hour + 59*time.Minute + 30*time.Second: "1sa 59dk",
	}
	for d, want := range cases {
		if got := shortDur(d); got != want {
			t.Errorf("shortDur(%v) = %q, beklenen %q", d, got, want)
		}
	}
}

// ── v0.9.1188 — operatör-bildirimli satır (2026-08-20) ────────────────
//
// Bildirilen: 2.374 olaylık bir java.net.UnknownHostException grubu, 8
// saat önce bitmiş 13 dakikalık bir patlama, ekranda P3 · "steady".
//
//	first 01:02:26 · last 01:15:35 → 13dk 09sn
//	2.374 / 13,15dk = 180,5/dk     → eski gömülü kapı 200/dk
//
// Kapıyı %10 farkla kaçırdı → burst=false → 8 saatlik yaş diğer bütün
// kapıları kapattı → son satır "steady".
//
// İki ayrı kusur ve ikisi de burada çivileniyor:
//
//	(1) EŞİK GÖMÜLÜYDÜ. Bu sınıfın DÖRDÜNCÜ bildirimi; v0.9.775 pencereleri
//	    ayarlanabilir yapmış ama patlamanın TANIMINI kodda bırakmıştı, yani
//	    duvarı kaldırmamış yerini değiştirmişti.
//	(2) GEREKÇE YALAN SÖYLÜYORDU. 13 dakikada 2.374 olay hiçbir okumada
//	    "steady" değildir. Deponun kuralı: öncelik düşebilir, cümle yalan
//	    olamaz (v0.9.524, v0.9.699).
//
// Servis/exception adları SENTETİK — kurulum adları depoya girmez.
func TestExceptionPriorityReportedRow20260820(t *testing.T) {
	const first = int64(1_700_000_000_000_000_000)
	last := first + int64(13*time.Minute+9*time.Second)
	g := chstore.ExceptionGroup{
		Service:     "checkout-bff",
		Type:        "java.net.UnknownHostException",
		Occurrences: 2374,
		FirstSeen:   first,
		LastSeen:    last,
		State:       "new",
	}
	cfg := chstore.DefaultExceptionTriage()
	now := time.Unix(0, last).Add(8*time.Hour + 16*time.Minute)

	prio, reason := exceptionPriorityAt(g, cfg, now)

	// Patlama artık TANINIYOR (180,5/dk ≥ 100/dk varsayılan kapı).
	if !exceptionIsBurst(g.Occurrences, g.FirstSeen, g.LastSeen, cfg) {
		t.Fatalf("patlama tanınmadı: %.1f/dk, kapı %.0f/dk",
			exceptionBurstRate(g.Occurrences, g.FirstSeen, g.LastSeen), cfg.BurstMinRate)
	}
	// v0.9.1205 (operatör direktifi, sınıfın 5. bildirimi) — eski
	// beklenti P2 idi ve gerekçesi "bitmiş patlamayı 'şimdi' saymak
	// merdivenin felsefesine aykırı" diyordu. Operatör o felsefeyi
	// P1 için REDDETTİ: patlama şiddetinde bir olay, akışı bitse de
	// ele alınana dek P1 kalır. Yaş yalnız gerekçe cümlesine girer.
	if prio != "P1" {
		t.Errorf("öncelik %s, beklenen P1 (patlama bitmiş olsa da düşmez — v0.9.1205)", prio)
	}
	// Gerekçe patlamanın GERÇEK büyüklüğünü taşımalı, "steady" DEMEMELİ.
	if strings.Contains(reason, "steady") {
		t.Errorf("gerekçe hâlâ yalan söylüyor: %q", reason)
	}
	for _, want := range []string{"2,374", "13dk", "/dk"} {
		if !strings.Contains(reason, want) {
			t.Errorf("gerekçe %q içermeli: %q", want, reason)
		}
	}
}

// TestExceptionReasonNeverLiesAboutSteady — kapıyı KAÇIRAN gruplar da
// "steady" diyemez.
//
// Eşik ayarlanabilir olduğuna göre kapının hemen altındaki her grup bir
// sonraki ayar değişikliğinin adayı; gerekçe bunu söylemeli ki operatör
// vidayı nereye çevireceğini görsün. Gerçekten kronik olanlar (düşük hız)
// eskisi gibi "steady" kalır — aksi hâlde kelime anlamını yitirirdi.
func TestExceptionReasonNeverLiesAboutSteady(t *testing.T) {
	cfg := chstore.DefaultExceptionTriage()
	const first = int64(1_700_000_000_000_000_000)
	mk := func(occ uint64, span time.Duration) chstore.ExceptionGroup {
		return chstore.ExceptionGroup{
			Service: "checkout-bff", Type: "java.lang.IllegalStateException",
			Occurrences: occ, FirstSeen: first, LastSeen: first + int64(span), State: "new",
		}
	}
	// Eski/uzak yaş: bütün tazelik kapıları kapalı, son satıra düşülüyor.
	age := 30 * time.Hour

	t.Run("kapının hemen altı steady DEMEZ", func(t *testing.T) {
		// 900 olay / 10dk = 90/dk → patlama kapısı 100'ün altında ama
		// yarısının üstünde (yoğun) ve hacim eşiğinin (500) üstünde.
		// v0.9.1205: böyle bir grup P1'i hak etmiştir ve akışı bitse de
		// P1 kalır — eski beklenti (P3 + eşik-vida cümlesi) direktifle
		// terfi etti; vida cümlesi artık yalnız hacim eşiğinin altında
		// kalan gruplarda görülür.
		g := mk(900, 10*time.Minute)
		prio, reason := exceptionPriorityAt(g, cfg, time.Unix(0, g.LastSeen).Add(age))
		if prio != "P1" {
			t.Errorf("yoğun+hacimli bitmiş grup P1 kalmalı, alınan %s (%q)", prio, reason)
		}
		if strings.Contains(reason, "steady") {
			t.Errorf("90/dk için 'steady' yalan: %q", reason)
		}
		if !strings.Contains(reason, "900") || !strings.Contains(reason, "stopped") {
			t.Errorf("gerekçe hacmi ve bittiğini söylemeli: %q", reason)
		}
	})

	t.Run("gerçekten kronik olan steady KALIR", func(t *testing.T) {
		// 11.260 olay / 72 saat = ~2,6/dk — kelime burada doğru.
		g := mk(11260, 72*time.Hour)
		_, reason := exceptionPriorityAt(g, cfg, time.Unix(0, g.LastSeen).Add(age))
		if reason != "steady" {
			t.Errorf("kronik grup için 'steady' beklenirdi: %q", reason)
		}
	})
}

// ── v0.9.1189 — HACİM KAPISININ 5 DAKİKALIK UÇURUMU ───────────────────
//
// Operatör bildirimi (2026-08-20): "Mobile loginde 888 hata alınmış ama
// bu P1 olmamış."
//
//	888 olay · son görülme 1sa12dk önce
//	  burst?            888 < 1000 (BurstMinTotal)      → HAYIR
//	  freshMin && ≥500? 1sa12dk > 5dk                    → HAYIR
//	  fresh   && ≥100?  EVET                             → P2
//
// Yani 500+ hacimli bir grup, beş dakikayı aşar aşmaz P1 olamıyordu.
// v0.9.699 bu uçurumu ZATEN yanlış ilan etmişti ("şiddet bir OLGU,
// tazelik ONA ERİŞİM aciliyeti") ama düzeltmeyi yalnız PATLAMA yolunda
// yaptı; hacim yolu 5 dakikada kaldı ve sınıf oradan geri döndü.
//
// Servis adları SENTETİK.
func TestExceptionPriorityVolumeGateHasNoFiveMinuteCliff(t *testing.T) {
	cfg := chstore.DefaultExceptionTriage()
	const first = int64(1_700_000_000_000_000_000)
	// Bildirilen satırın şekli: patlama eşiğinin ALTINDA hacim (888<1000),
	// ama P1 hacim eşiğinin ÜSTÜNDE (888≥500).
	g := chstore.ExceptionGroup{
		Service: "mobile-login", Type: "java.sql.SQLTimeoutException",
		Occurrences: 888, FirstSeen: first, LastSeen: first + int64(20*time.Minute),
		State: "new",
	}
	if exceptionIsBurst(g.Occurrences, g.FirstSeen, g.LastSeen, cfg) {
		t.Fatal("test kurgusu: bu grup patlama SAYILMAMALI (hacim tabanının altında)")
	}

	// Uçurumun iki yakası: 5 dakikanın altı ve üstü. İKİSİ DE P1 olmalı —
	// aradaki tek fark gerekçenin cümlesi.
	for _, c := range []struct {
		name string
		age  time.Duration
	}{
		{"hâlâ akıyor (2dk)", 2 * time.Minute},
		{"uçurumun hemen ötesi (6dk)", 6 * time.Minute},
		{"bildirilen vaka (1sa12dk)", 72 * time.Minute},
		{"pencerenin hemen içi (3sa59dk)", 3*time.Hour + 59*time.Minute},
	} {
		t.Run(c.name, func(t *testing.T) {
			prio, reason := exceptionPriorityAt(g, cfg, time.Unix(0, g.LastSeen).Add(c.age))
			if prio != "P1" {
				t.Errorf("öncelik %s, beklenen P1 (888 olay, yaş %v, pencere %v) — gerekçe %q",
					prio, c.age, cfg.P1Window(), reason)
			}
			if !strings.Contains(reason, "888") {
				t.Errorf("gerekçe hacmi söylemeli: %q", reason)
			}
		})
	}

	// Pencere KAPANINCA düşmeli — kapı kaldırılmadı, yeri düzeltildi.
	prio, _ := exceptionPriorityAt(g, cfg, time.Unix(0, g.LastSeen).Add(5*time.Hour))
	if prio == "P1" {
		t.Error("P1 penceresi kapandıktan sonra hâlâ P1 — kapı kaldırılmış olurdu")
	}
}

// TestExceptionVolumeGateIsConfigurable — eşik ayardan gelmeli; sabiti
// bir çentik ötelemek bu sınıfta dört kez başarısız oldu.
func TestExceptionVolumeGateIsConfigurable(t *testing.T) {
	const first = int64(1_700_000_000_000_000_000)
	g := chstore.ExceptionGroup{
		Service: "mobile-login", Type: "java.sql.SQLTimeoutException",
		Occurrences: 300, FirstSeen: first, LastSeen: first + int64(20*time.Minute),
		State: "new",
	}
	now := time.Unix(0, g.LastSeen).Add(time.Hour)

	// Varsayılan eşik (500): 300 olay P1 DEĞİL.
	if prio, _ := exceptionPriorityAt(g, chstore.DefaultExceptionTriage(), now); prio == "P1" {
		t.Error("300 olay varsayılan eşikte (500) P1 olmamalı")
	}
	// Eşik düşürülünce P1 OLMALI — vida gerçekten bağlı mı.
	low := chstore.DefaultExceptionTriage()
	low.P1MinOccurrences = 200
	if prio, _ := exceptionPriorityAt(g, low, now); prio != "P1" {
		t.Errorf("eşik 200'e indi ama 300 olay hâlâ P1 değil (%s) — vida bağlı değil", prio)
	}
}
