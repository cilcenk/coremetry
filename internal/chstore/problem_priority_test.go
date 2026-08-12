package chstore

import (
	"strings"
	"testing"
	"time"
)

// v0.8.321 — regression: the P1/P2 "×threshold" reason text printed the RAW
// Value/Threshold ratio, while bigBreach correctly used the FLIPPED ratio
// for below-threshold ("<"/"<=") rules. An uptime rule (value 40 vs
// threshold 99) ranked P1 correctly but the operator-facing tooltip read
// "critical + 0.4x threshold" instead of ~2.5x — an inverted magnitude on
// every less-than rule, serialized into the cached /api/problems payloads.
func TestComputePriorityReasonUsesFlippedRatio(t *testing.T) {
	now := time.Now().UnixNano()
	fresh := now - int64(10*time.Minute) // young problem: no stale-critical path

	// v0.9.976 — vaka artık Comparator TAŞIYOR. Ters çevirme o günden beri
	// kuralın yönüne bağlı: comparator'sız bir 40/99 satırı (ki eski satırlar
	// ve anomali/monitor üreticileri öyle) ARTIK çevrilmez. Bu testin konusu
	// çevrilen dalın gerekçe metni olduğu için vakaya "<" eklendi — pin
	// aynı davranışı tutmaya devam ediyor.
	t.Run("below-threshold breach reports the flipped magnitude", func(t *testing.T) {
		p := Problem{Severity: "critical", Value: 40, Threshold: 99, Comparator: "<", Status: "open", StartedAt: fresh}
		pri, reason := computePriority(p, now, DefaultProblemPriority())
		if pri != "P1" {
			t.Fatalf("priority = %s, want P1", pri)
		}
		if !strings.Contains(reason, "2.5x") {
			t.Fatalf("reason %q must carry the flipped ~2.5x magnitude, not the raw 0.4x", reason)
		}
	})

	t.Run("above-threshold breach text unchanged", func(t *testing.T) {
		p := Problem{Severity: "warning", Value: 30, Threshold: 10, Status: "open", StartedAt: fresh}
		pri, reason := computePriority(p, now, DefaultProblemPriority())
		if pri != "P2" || !strings.Contains(reason, "3.0x") {
			t.Fatalf("got (%s, %q), want P2 with 3.0x", pri, reason)
		}
	})

	t.Run("zero threshold still falls back to severity alone", func(t *testing.T) {
		p := Problem{Severity: "critical", Value: 5, Threshold: 0, Status: "open", StartedAt: fresh}
		pri, _ := computePriority(p, now, DefaultProblemPriority())
		if pri != "P2" {
			t.Fatalf("priority = %s, want P2 (no ratio computable)", pri)
		}
	})
}

// TestTotalLossIsP1 — v0.9.825, operatör-raporlu.
//
// computePriority'nin ters-çevirme kapısı `ratio < 1 && ratio > 0`
// diyordu. TAM SIFIR bu aralığın DIŞINDA (0 > 0 yanlış), yani ratio 0'da
// kalıyor, `ratio >= 2` hiç tutmuyor ve bigBreach false oluyordu.
//
// Sonuç: monitor DOWN problemi (runner.go — Value 0, Threshold 1,
// critical) 4 saat boyunca P2'ye kilitliydi; ancak stale-critical
// kuralıyla P1'e çıkabiliyordu. Yani TAMAMEN ERİŞİLEMEZ bir servis,
// öncelik listesinde "%50 yavaşlamış" bir servisin ALTINDA duruyordu.
//
// Tablo her "<" ailesini geziyor: sıfır o ailede ihlalin EN AĞIR hâli.
func TestTotalLossIsP1(t *testing.T) {
	now := time.Now().UnixNano()
	fresh := now - int64(10*time.Minute) // genç: stale-critical yolu kapalı

	cases := []struct {
		name             string
		sev              string
		value, threshold float64
		// cmp (v0.9.976) — ters çevirme artık kuralın yönüne bağlı. TAM
		// KAYIP kolu comparator'dan BAĞIMSIZ olduğu için aşağıdaki 0/X
		// vakaları bilerek comparator'sız: monitor DOWN'ın P1 kalması
		// (v0.9.825 operatör kararı) yeni kapıdan etkilenmemeli.
		cmp        string
		wantPri    string
		wantReason string
	}{
		// FIRTINANIN VAKASI: monitor DOWN, birebir runner.go'daki alanlar.
		{"monitor DOWN (0/1)", "critical", 0, 1, "", "P1", "tamamen kayıp"},
		{"uptime tamamen düştü (0/99)", "critical", 0, 99, "<", "P1", "tamamen kayıp"},
		{"sağlıklı pod kalmadı (0/3)", "critical", 0, 3, "", "P1", "tamamen kayıp"},
		{"warning seviyesinde tam kayıp (0/95)", "warning", 0, 95, "<", "P2", "tamamen kayıp"},

		// KOMŞU DALLAR — düzeltme bunları BOZMAMALI.
		{"kısmi düşüş hâlâ oranla (40/99)", "critical", 40, 99, "<", "P1", "2.5x"},
		{"sınırın altında kalan düşüş (60/99)", "critical", 60, 99, "<", "P2", ""},
		{"eşik sıfır → oran yok", "critical", 0, 0, "<", "P2", ""},
		{"negatif eşik: sıfır tam kayıp DEĞİL", "critical", 0, -5, "<", "P2", ""},
		{"yükselen ihlal etkilenmedi (30/10)", "warning", 30, 10, ">", "P2", "3.0x"},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			p := Problem{
				Severity: c.sev, Status: "open",
				Value: c.value, Threshold: c.threshold,
				Comparator: c.cmp,
				StartedAt:  fresh,
			}
			pri, reason := computePriority(p, now, DefaultProblemPriority())
			if pri != c.wantPri {
				t.Errorf("öncelik = %s (%q), %s bekleniyordu.\n\n"+
					"Tam kayıp (value 0, '<' sınıfı eşik) ihlalin EN AĞIR hâlidir; "+
					"P2'ye kilitlenmesi TAMAMEN ERİŞİLEMEZ bir servisi yavaşlamış "+
					"bir servisin altına koyar.", pri, reason, c.wantPri)
			}
			if c.wantReason != "" && !strings.Contains(reason, c.wantReason) {
				t.Errorf("gerekçe %q, %q içermeliydi — operatör SIRAYI gerekçeden "+
					"okuyor; '0.0x threshold' ya da '+Inf' yanlış bilgi olurdu",
					reason, c.wantReason)
			}
		})
	}

	// Gerekçe metninde oran GÖRÜNMEMELİ: 0/1 için "0.0x" ya da "+Inf"
	// ikisi de yanlış olurdu.
	_, reason := computePriority(Problem{
		Severity: "critical", Status: "open", Value: 0, Threshold: 1, StartedAt: fresh,
	}, now, DefaultProblemPriority())
	if strings.Contains(reason, "x threshold") {
		t.Errorf("tam kayıp gerekçesi hâlâ oran yazıyor: %q", reason)
	}
	if !strings.Contains(reason, "0/1") {
		t.Errorf("gerekçe eşiği göstermiyor: %q — operatör neyin kaybolduğunu görmeli", reason)
	}
}

// TestTrimFloatKeepsThresholdsReadable — gerekçe metni operatörün
// gözüyle okunuyor; "0/1.00000" gürültüdür, "0/99.5" bilgidir.
func TestTrimFloatKeepsThresholdsReadable(t *testing.T) {
	cases := []struct {
		in   float64
		want string
	}{
		{1, "1"}, {99, "99"}, {99.5, "99.5"}, {0.25, "0.25"}, {3, "3"},
	}
	for _, c := range cases {
		if got := trimFloat(c.in); got != c.want {
			t.Errorf("trimFloat(%v) = %q, %q bekleniyordu", c.in, got, c.want)
		}
	}
}

// TestFreshDeployDoesNotDrivePriority — v0.9.612, operatör kararı.
//
// Önceki kural "critical + son 5 dk içinde deploy → P1" idi. Prod'da
// deploy sıklığı yüksek olduğu için tetikleyici sürekli ateşliyor ve
// P1 kavramını sulandırıyordu.
//
// Bu test iki yönü birden tutuyor:
//  1. taze deploy artık ÖNCELİĞE karışmıyor
//  2. ama problemin KENDİ şiddetinden gelen kapılar (2× eşik,
//     4+ saat açık) hâlâ P1 üretiyor — tetikleyiciyi kaldırmak
//     P1'i büsbütün kapatmak DEĞİL
func TestFreshDeployDoesNotDrivePriority(t *testing.T) {
	now := time.Now().UnixNano()
	fresh := &RecentDeploy{Version: "v1.2.3", AgeSeconds: 30}

	// critical + taze deploy, başka tetikleyici YOK → P2 (P1 değil).
	p := Problem{
		Severity: "critical", Status: "open",
		Value: 1, Threshold: 1, // oran 1 → büyük ihlal değil
		StartedAt:    now - int64(time.Minute),
		RecentDeploy: fresh,
	}
	if pri, reason := computePriority(p, now, DefaultProblemPriority()); pri != "P2" {
		t.Errorf("taze deploy + critical → %s (%s); P2 bekleniyordu.\n\n"+
			"Deploy sıklığı yüksek bir prod'da bu tetikleyici sürekli ateşler "+
			"ve P1 kavramını sulandırır. Deploy bilgisi kaybolmuyor — "+
			"ProblemDetail'de görünüyor — yalnız SIRAYA sokmuyor.", pri, reason)
	}

	// warning + taze deploy → P3 (P2 değil): aynı kural.
	w := p
	w.Severity = "warning"
	if pri, _ := computePriority(w, now, DefaultProblemPriority()); pri != "P3" {
		t.Errorf("taze deploy + warning → %s; P3 bekleniyordu", pri)
	}

	// AMA: problemin kendi şiddeti hâlâ P1 üretmeli.
	big := p
	big.Value, big.Threshold = 10, 3 // oran 3.33 → büyük ihlal
	if pri, _ := computePriority(big, now, DefaultProblemPriority()); pri != "P1" {
		t.Errorf("2× eşik ihlali → %s; P1 bekleniyordu — deploy tetikleyicisini "+
			"kaldırmak P1'i büsbütün kapatmak DEĞİL", pri)
	}
	stale := p
	stale.StartedAt = now - int64(5*time.Hour)
	if pri, _ := computePriority(stale, now, DefaultProblemPriority()); pri != "P1" {
		t.Errorf("5 saattir açık kritik → %s; P1 bekleniyordu", pri)
	}
}

// ── v0.9.838 — öncelik merdiveninin vidaları ────────────────────────
//
// Operatör-bildirimli: "hâlâ çok fazla alert rule'dan P1 geliyor".
// Prod'da 29 açık critical'in 22'si P1'di; örnek bir error_rate problemi
// 3.93/1.31 = 3.0× oranıyla gömülü "2× ihlal" kapısından geçip otomatik
// P1 oluyordu.
//
// Bu sürüm DAVRANIŞ DEĞİŞTİRMİYOR — varsayılanlar eski sabitlerin
// birebir aynısı. Yukarıdaki testlerin hepsi DefaultProblemPriority()
// ile çağrılıyor ve beklentileri DEĞİŞMEDİ; aşağıdakiler vidanın
// gerçekten çevirdiğini ve sınırlarda doğru tarafa düştüğünü tutuyor.

// TestBigBreachRatioIsConfigurable — ihlal katı kapısı, sınırlarıyla.
//
// Sınır testi şart: `>=` mi `>` mü sorusu bu depoda birim-karışması
// sınıfının kardeşi — tam eşitlikte yanlış tarafa düşen bir kapı, bir
// kural setinin tamamını sessizce bir basamak kaydırır.
func TestBigBreachRatioIsConfigurable(t *testing.T) {
	now := time.Now().UnixNano()
	fresh := now - int64(10*time.Minute) // genç: stale-critical yolu kapalı

	cases := []struct {
		name             string
		cfg              ProblemPriorityConfig
		sev              string
		value, threshold float64
		want             string
	}{
		// VARSAYILAN (2.0×) — v0.9.838 ÖNCESİ davranışın aynısı.
		{"varsayılan: 1.9× büyük ihlal DEĞİL", DefaultProblemPriority(), "critical", 19, 10, "P2"},
		{"varsayılan: tam 2.0× büyük ihlal", DefaultProblemPriority(), "critical", 20, 10, "P1"},
		{"varsayılan: 2.1× büyük ihlal", DefaultProblemPriority(), "critical", 21, 10, "P1"},

		// OPERATÖRÜN CANLI VAKASI: 3.93/1.31 = 3.0×. Varsayılanda P1,
		// kat 4.0'a çekildiğinde P2 — istenen tam olarak bu.
		{"3.0× varsayılanda P1", DefaultProblemPriority(), "critical", 3.93, 1.31, "P1"},
		{"3.0× kat 4.0'da P2", ProblemPriorityConfig{BigBreachRatio: 4, StaleCriticalHours: 4},
			"critical", 3.93, 1.31, "P2"},
		{"4.0× kat 4.0'da yine P1", ProblemPriorityConfig{BigBreachRatio: 4, StaleCriticalHours: 4},
			"critical", 40, 10, "P1"},

		// Kat GEVŞETİLEBİLİR de: 1.5 diyen bir operatör daha çok P1 ister.
		{"1.6× kat 1.5'te P1", ProblemPriorityConfig{BigBreachRatio: 1.5, StaleCriticalHours: 4},
			"critical", 16, 10, "P1"},

		// warning kolu AYNI vidayı kullanır (P2 kapısı).
		{"warning 3.0× kat 4.0'da P3", ProblemPriorityConfig{BigBreachRatio: 4, StaleCriticalHours: 4},
			"warning", 30, 10, "P3"},
		{"warning 3.0× varsayılanda P2", DefaultProblemPriority(), "warning", 30, 10, "P2"},

		// TAM KAYIP (v0.9.825) vidaya BAĞLI DEĞİL: 0/X her katta P1.
		{"tam kayıp kat 10'da bile P1", ProblemPriorityConfig{BigBreachRatio: 10, StaleCriticalHours: 4},
			"critical", 0, 1, "P1"},
		// info pini de vidaya bağlı değil.
		{"info her katta P3", ProblemPriorityConfig{BigBreachRatio: 1.1, StaleCriticalHours: 4},
			"info", 100, 1, "P3"},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			p := Problem{
				Severity: c.sev, Status: "open",
				Value: c.value, Threshold: c.threshold,
				StartedAt: fresh,
			}
			if got, reason := computePriority(p, now, c.cfg); got != c.want {
				t.Errorf("öncelik = %s (%q), %s bekleniyordu (kat %.2f)",
					got, reason, c.want, c.cfg.BigBreachRatio)
			}
		})
	}

	// "<" ailesi: oran ters çevrilir, vida ÇEVRİLMİŞ orana uygulanır.
	// Tablodan AYRI, çünkü v0.9.976'dan beri comparator şart: yukarıdaki
	// satırların hiçbiri comparator taşımıyor ve taşımamalı da (hepsi ">"
	// ailesi). Comparator'sız bir 40/99 vakası "kat 4.0'da P2" sonucunu
	// DOĞRU SEBEPLE değil, ters çevirme hiç olmadığı için verirdi.
	uptime := Problem{Severity: "critical", Status: "open", Value: 40, Threshold: 99,
		Comparator: "<", StartedAt: fresh}
	if got, reason := computePriority(uptime, now, DefaultProblemPriority()); got != "P1" {
		t.Errorf("uptime 40/99 (2.5×) varsayılan katta %s (%q); P1 bekleniyordu", got, reason)
	}
	tight := ProblemPriorityConfig{BigBreachRatio: 4, StaleCriticalHours: 4}
	if got, reason := computePriority(uptime, now, tight); got != "P2" {
		t.Errorf("uptime 40/99 (2.5×) kat 4.0'da %s (%q); P2 bekleniyordu — vida "+
			"ÇEVRİLMİŞ orana uygulanmalı", got, reason)
	}
}

// TestStaleCriticalHoursIsConfigurable — bayat-critical terfisi, 0 dahil.
//
// 0 ANLAMLI bir değer ("terfi kapalı"), yani "0 ise varsayılana dön"
// diyemiyoruz. Operatörün P1 selini kesmek için isteyebileceği ilk vida
// bu olabilir: uzun süre açık kalan bir critical "ilgilenilmedi" demek,
// "şu an daha acil" demek değil.
func TestStaleCriticalHoursIsConfigurable(t *testing.T) {
	now := time.Now().UnixNano()

	cases := []struct {
		name    string
		cfg     ProblemPriorityConfig
		openFor time.Duration
		status  string
		want    string
	}{
		// VARSAYILAN (4h) — v0.9.838 ÖNCESİ davranışın aynısı.
		{"varsayılan: 3.9 saat henüz bayat değil", DefaultProblemPriority(),
			3*time.Hour + 54*time.Minute, "open", "P2"},
		{"varsayılan: tam 4 saat bayat", DefaultProblemPriority(), 4 * time.Hour, "open", "P1"},
		{"varsayılan: 5 saat bayat", DefaultProblemPriority(), 5 * time.Hour, "open", "P1"},

		// 0 = TERFİ KAPALI. 5 saat de 500 saat de P2 kalır.
		{"kapalı: 5 saat açık hâlâ P2",
			ProblemPriorityConfig{BigBreachRatio: 2, StaleCriticalHours: 0}, 5 * time.Hour, "open", "P2"},
		{"kapalı: 500 saat açık hâlâ P2",
			ProblemPriorityConfig{BigBreachRatio: 2, StaleCriticalHours: 0}, 500 * time.Hour, "open", "P2"},

		// SIKILAŞTIRMA: 12 saate çekilirse 5 saat artık P1 değil.
		{"12 saat: 5 saat açık P2",
			ProblemPriorityConfig{BigBreachRatio: 2, StaleCriticalHours: 12}, 5 * time.Hour, "open", "P2"},
		{"12 saat: 13 saat açık P1",
			ProblemPriorityConfig{BigBreachRatio: 2, StaleCriticalHours: 12}, 13 * time.Hour, "open", "P1"},

		// GEVŞETME: 1 saate çekilirse daha erken P1.
		{"1 saat: 90 dakika açık P1",
			ProblemPriorityConfig{BigBreachRatio: 2, StaleCriticalHours: 1}, 90 * time.Minute, "open", "P1"},

		// ÇÖZÜLMÜŞ kayıt hiçbir ayarda bayat sayılmaz.
		{"resolved: 500 saat sonra bile P2", DefaultProblemPriority(), 500 * time.Hour, "resolved", "P2"},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			p := Problem{
				Severity: "critical", Status: c.status,
				Value: 1, Threshold: 1, // oran 1 → büyük ihlal kapısı KAPALI
				StartedAt: now - int64(c.openFor),
			}
			if got, reason := computePriority(p, now, c.cfg); got != c.want {
				t.Errorf("öncelik = %s (%q), %s bekleniyordu (bayat eşiği %.1fsa)",
					got, reason, c.want, c.cfg.StaleCriticalHours)
			}
		})
	}

	// Bayat terfisi KAPALIYKEN bile problemin KENDİ şiddeti P1 üretmeli —
	// vidayı kapatmak P1'i büsbütün kapatmak DEĞİL.
	off := ProblemPriorityConfig{BigBreachRatio: 2, StaleCriticalHours: 0}
	big := Problem{Severity: "critical", Status: "open", Value: 30, Threshold: 10,
		StartedAt: now - int64(10*time.Minute)}
	if got, _ := computePriority(big, now, off); got != "P1" {
		t.Errorf("bayat terfisi kapalıyken 3× ihlal → %s; P1 bekleniyordu", got)
	}
}

// TestNormalizeProblemPriority — kelepçeler tek kaynaktan geçer: API
// doğrulaması ve okuma yolu AYNI kuralları kullanır ki elle düzenlenmiş
// bir system_settings satırı da güvenli bir şekle düşsün.
func TestNormalizeProblemPriority(t *testing.T) {
	d := DefaultProblemPriority()

	cases := []struct {
		name string
		in   ProblemPriorityConfig
		want ProblemPriorityConfig
	}{
		{"tamamen boş = varsayılanlar", ProblemPriorityConfig{}, d},
		{"varsayılanlar sabit kalır", d, d},
		{"kat 1.0 kelepçeyle 1.1", ProblemPriorityConfig{BigBreachRatio: 1.0, StaleCriticalHours: 4},
			ProblemPriorityConfig{BigBreachRatio: MinBigBreachRatio, StaleCriticalHours: 4}},
		{"kat 0.5 kelepçeyle 1.1", ProblemPriorityConfig{BigBreachRatio: 0.5, StaleCriticalHours: 6},
			ProblemPriorityConfig{BigBreachRatio: MinBigBreachRatio, StaleCriticalHours: 6}},
		{"kat 0 (alan yok) varsayılana döner", ProblemPriorityConfig{BigBreachRatio: 0, StaleCriticalHours: 6},
			ProblemPriorityConfig{BigBreachRatio: 2, StaleCriticalHours: 6}},
		{"negatif kat varsayılana döner", ProblemPriorityConfig{BigBreachRatio: -5, StaleCriticalHours: 6},
			ProblemPriorityConfig{BigBreachRatio: 2, StaleCriticalHours: 6}},
		// 0 saat "kapalı" demek ve KORUNUR — 0'ı varsayılana çevirmek,
		// operatörün açıkça kapattığı terfiyi sessizce geri açardı.
		{"bayat 0 KORUNUR (kapalı)", ProblemPriorityConfig{BigBreachRatio: 3, StaleCriticalHours: 0},
			ProblemPriorityConfig{BigBreachRatio: 3, StaleCriticalHours: 0}},
		{"negatif bayat 0'a kelepçelenir", ProblemPriorityConfig{BigBreachRatio: 3, StaleCriticalHours: -2},
			ProblemPriorityConfig{BigBreachRatio: 3, StaleCriticalHours: 0}},
		{"geçerli değerler dokunulmaz", ProblemPriorityConfig{BigBreachRatio: 4.5, StaleCriticalHours: 12},
			ProblemPriorityConfig{BigBreachRatio: 4.5, StaleCriticalHours: 12}},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := NormalizeProblemPriority(c.in); got != c.want {
				t.Errorf("Normalize(%+v) = %+v, %+v bekleniyordu", c.in, got, c.want)
			}
		})
	}
}

// TestZeroConfigDoesNotFloodP1 — güvenlik ağı.
//
// Kelepçe olmasaydı `ProblemPriorityConfig{}` ile çağıran bir kod yolu
// `ratio >= 0` üretirdi ve eşiği aşan HER problem (hatta aşmayan da)
// büyük ihlal sayılırdı — yani tam olarak operatörün şikâyet ettiği
// P1 seli, ayarın KENDİSİNDEN doğardı.
func TestZeroConfigDoesNotFloodP1(t *testing.T) {
	now := time.Now().UnixNano()
	// Eşiği ancak %10 aşan bir critical: hiçbir makul katta büyük ihlal değil.
	p := Problem{Severity: "critical", Status: "open", Value: 11, Threshold: 10,
		StartedAt: now - int64(10*time.Minute)}
	if got, reason := computePriority(p, now, ProblemPriorityConfig{}); got != "P2" {
		t.Fatalf("sıfır config ile %s (%q); P2 bekleniyordu — kelepçesiz "+
			"`ratio >= 0` her problemi büyük ihlal yapardı", got, reason)
	}
}

// TestEnrichUsesTheConfiguredKnob — vidanın gerçekten OKUMA ve BİLDİRİM
// hatlarına indiğinin kanıtı. notify/alert_title.go bu fonksiyonu
// çağırıyor, yani kopya kural yok: e-postadaki P1 ile ekrandaki P1 aynı
// hesabın sonucu.
func TestEnrichUsesTheConfiguredKnob(t *testing.T) {
	prev := CurrentProblemPriority()
	t.Cleanup(func() { SetProblemPriority(prev) })

	now := time.Now().UnixNano()
	// 3.0× ihlal, taze (bayat yolu kapalı).
	mk := func() []Problem {
		return []Problem{{
			Severity: "critical", Status: "open", Value: 30, Threshold: 10,
			StartedAt: now - int64(10*time.Minute),
		}}
	}

	SetProblemPriority(DefaultProblemPriority())
	if got := EnrichProblemsWithPriority(mk())[0].Priority; got != "P1" {
		t.Errorf("varsayılan katta 3.0× → %s; P1 bekleniyordu", got)
	}

	SetProblemPriority(ProblemPriorityConfig{BigBreachRatio: 4, StaleCriticalHours: 4})
	if got := EnrichProblemsWithPriority(mk())[0].Priority; got != "P2" {
		t.Errorf("kat 4.0'da 3.0× → %s; P2 bekleniyordu — vida okuma "+
			"hattına inmiyor demektir", got)
	}
}
