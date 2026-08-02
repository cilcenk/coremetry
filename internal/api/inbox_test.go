package api

import (
	"strings"
	"testing"
	"time"

	"github.com/cilcenk/coremetry/internal/chstore"
)


// v0.9.524 — operatör-bildirimli: /inbox'ta her satırda "N in last 5min"
// yazıyordu ve N, ÖMÜR BOYU toplam occurrence'tı. 28 Haziran'da ilk
// görülen bir grup, 35 günlük 18.217 occurrence'ının tamamını son 5
// dakikada olmuş gibi gösteriyordu.
//
// Kök sebep: freshMin grubun SON GÖRÜLME zamanını ölçer, g.Occurrences
// toplamı taşır — ikisini tek cümlede birleştirmek yalan üretiyordu.
// Gerçek pencereli sayı veri modelinde YOK; onu üretmek grup başına yeni
// sorgu demek (v0.9.522/523'te tam o sınıfı azalttık). Doğru çözüm sayıyı
// uydurmak değil, iki gerçeği ayrı ayrı söylemek.
func TestExceptionPriorityReasonDoesNotClaimWindowedCount(t *testing.T) {
	now := time.Now().UnixNano()
	old := chstore.ExceptionGroup{
		Fingerprint: "fp",
		FirstSeen:   now - int64(35*24*time.Hour), // 35 gün önce
		LastSeen:    now - int64(30*time.Second),  // az önce yine görüldü
		Occurrences: 18217,
	}

	prio, reason := exceptionPriority(old)
	if prio != "P1" {
		t.Fatalf("taze + yüksek hacim P1 olmalı, got %q", prio)
	}
	// YASAK: toplamı pencereli sayı gibi sunmak.
	if strings.Contains(reason, "18217 in last") || strings.Contains(reason, "18,217 in last") {
		t.Errorf("gerekçe toplamı pencereli sayı gibi sunuyor (v0.9.524 gerilemesi): %q", reason)
	}
	// Gerekçe İKİ gerçeği de taşımalı: tazelik + toplamın toplam olduğu.
	if !strings.Contains(reason, "last 5min") {
		t.Errorf("tazelik bilgisi kaybolmuş: %q", reason)
	}
	if !strings.Contains(reason, "total") {
		t.Errorf("sayının TOPLAM olduğu söylenmiyor: %q", reason)
	}
	if !strings.Contains(reason, "18,217") {
		t.Errorf("sayı binlik ayraçlı okunmalı: %q", reason)
	}
}

func TestFmtThousands(t *testing.T) {
	for in, want := range map[uint64]string{
		0: "0", 7: "7", 999: "999", 1000: "1,000",
		18217: "18,217", 137656: "137,656", 1234567: "1,234,567",
	} {
		if got := fmtThousands(in); got != want {
			t.Errorf("fmtThousands(%d) = %q, beklenen %q", in, got, want)
		}
	}
}
