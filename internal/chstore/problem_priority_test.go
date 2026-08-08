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

	t.Run("below-threshold breach reports the flipped magnitude", func(t *testing.T) {
		p := Problem{Severity: "critical", Value: 40, Threshold: 99, Status: "open", StartedAt: fresh}
		pri, reason := computePriority(p, now)
		if pri != "P1" {
			t.Fatalf("priority = %s, want P1", pri)
		}
		if !strings.Contains(reason, "2.5x") {
			t.Fatalf("reason %q must carry the flipped ~2.5x magnitude, not the raw 0.4x", reason)
		}
	})

	t.Run("above-threshold breach text unchanged", func(t *testing.T) {
		p := Problem{Severity: "warning", Value: 30, Threshold: 10, Status: "open", StartedAt: fresh}
		pri, reason := computePriority(p, now)
		if pri != "P2" || !strings.Contains(reason, "3.0x") {
			t.Fatalf("got (%s, %q), want P2 with 3.0x", pri, reason)
		}
	})

	t.Run("zero threshold still falls back to severity alone", func(t *testing.T) {
		p := Problem{Severity: "critical", Value: 5, Threshold: 0, Status: "open", StartedAt: fresh}
		pri, _ := computePriority(p, now)
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
		wantPri          string
		wantReason       string
	}{
		// FIRTINANIN VAKASI: monitor DOWN, birebir runner.go'daki alanlar.
		{"monitor DOWN (0/1)", "critical", 0, 1, "P1", "tamamen kayıp"},
		{"uptime tamamen düştü (0/99)", "critical", 0, 99, "P1", "tamamen kayıp"},
		{"sağlıklı pod kalmadı (0/3)", "critical", 0, 3, "P1", "tamamen kayıp"},
		{"warning seviyesinde tam kayıp (0/95)", "warning", 0, 95, "P2", "tamamen kayıp"},

		// KOMŞU DALLAR — düzeltme bunları BOZMAMALI.
		{"kısmi düşüş hâlâ oranla (40/99)", "critical", 40, 99, "P1", "2.5x"},
		{"sınırın altında kalan düşüş (60/99)", "critical", 60, 99, "P2", ""},
		{"eşik sıfır → oran yok", "critical", 0, 0, "P2", ""},
		{"negatif eşik: sıfır tam kayıp DEĞİL", "critical", 0, -5, "P2", ""},
		{"yükselen ihlal etkilenmedi (30/10)", "warning", 30, 10, "P2", "3.0x"},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			p := Problem{
				Severity: c.sev, Status: "open",
				Value: c.value, Threshold: c.threshold,
				StartedAt: fresh,
			}
			pri, reason := computePriority(p, now)
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
	}, now)
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
	if pri, reason := computePriority(p, now); pri != "P2" {
		t.Errorf("taze deploy + critical → %s (%s); P2 bekleniyordu.\n\n"+
			"Deploy sıklığı yüksek bir prod'da bu tetikleyici sürekli ateşler "+
			"ve P1 kavramını sulandırır. Deploy bilgisi kaybolmuyor — "+
			"ProblemDetail'de görünüyor — yalnız SIRAYA sokmuyor.", pri, reason)
	}

	// warning + taze deploy → P3 (P2 değil): aynı kural.
	w := p
	w.Severity = "warning"
	if pri, _ := computePriority(w, now); pri != "P3" {
		t.Errorf("taze deploy + warning → %s; P3 bekleniyordu", pri)
	}

	// AMA: problemin kendi şiddeti hâlâ P1 üretmeli.
	big := p
	big.Value, big.Threshold = 10, 3 // oran 3.33 → büyük ihlal
	if pri, _ := computePriority(big, now); pri != "P1" {
		t.Errorf("2× eşik ihlali → %s; P1 bekleniyordu — deploy tetikleyicisini "+
			"kaldırmak P1'i büsbütün kapatmak DEĞİL", pri)
	}
	stale := p
	stale.StartedAt = now - int64(5*time.Hour)
	if pri, _ := computePriority(stale, now); pri != "P1" {
		t.Errorf("5 saattir açık kritik → %s; P1 bekleniyordu", pri)
	}
}
