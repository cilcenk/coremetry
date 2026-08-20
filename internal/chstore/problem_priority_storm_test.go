package chstore

// v0.9.1194 — fırtına satırı TANIM GEREĞİ P1 (operatör-bildirimli + spec
// onayı: 25 saniyede 9 servis, hiçbiri tek başına eşik geçmiyor).
//
// Genel merdiven bu satıra YANLIŞ cevap verir ve bu test tam o yanlışı
// çivileyerek başlar: Value=9 / Threshold=5 → 1.8× < BigBreachRatio(2.0)
// → "critical, P2" çıkardı. Dedektör eşiği geçmeden satırı hiç açmıyor —
// satırın varlığı ihlalin kanıtı; oran kapısı aynı eşiği ikinci kez, daha
// katı sormak olurdu.

import (
	"strings"
	"testing"
	"time"
)

func TestComputePriorityExceptionStorm(t *testing.T) {
	now := time.Now().UnixNano()
	storm := Problem{
		RuleID: "exception-storm", Severity: "critical",
		Value: 9, Threshold: 5, Comparator: ">",
		Status: "open", StartedAt: now,
	}
	prio, reason := computePriority(storm, now, DefaultProblemPriority())
	if prio != "P1" {
		t.Fatalf("fırtına %s çıktı, P1 olmalı (gerekçe %q) — 1.8× oran kapısına takılmış", prio, reason)
	}
	for _, want := range []string{"fırtına", "9", "5"} {
		if !strings.Contains(reason, want) {
			t.Errorf("gerekçe %q içermeli: %q", want, reason)
		}
	}

	// KANIT: aynı sayılar fırtına kimliği OLMADAN gerçekten P2 verir —
	// yani dal gerçek bir farkı kapatıyor, süs değil.
	generic := storm
	generic.RuleID = "rule-x"
	if p, _ := computePriority(generic, now, DefaultProblemPriority()); p != "P2" {
		t.Fatalf("kimliksiz aynı satır %s — testin öncülü değişmiş, dalı yeniden değerlendir", p)
	}
}
