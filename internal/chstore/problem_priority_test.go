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
