package api

import (
	"strings"
	"testing"
	"time"

	"github.com/cilcenk/coremetry/internal/chstore"
)

// v0.9.699 — PATLAMA ŞİDDETİ UÇURUMDAN DÜŞMESİN, BASAMAK İNSİN.
//
// Operatör-bildirimi: "cm-put-service problemi P1 ya da P2'ydi bence
// doğru, sonra P3'e düştü neden?"
//
// PROD SATIRI (05.08.2026, ekran görüntüsü 23:18):
//
//	cm-put-service · org.springframework… "Could not open JPA"
//	101.132 olay · first 22:09:09 · last 22:12:07  → 2dk58sn
//	hız ~34.000/dk = P1 eşiğinin (200/dk) 170 KATI
//	son görülme 66 dk önce → freshHour KAPANDI → P3 "steady"
//
// AYNI SINIF, İKİNCİ KEZ. v0.9.627'de şikâyet "12 dakikada 11.260 olay
// P2 göründü" idi ve tazelik penceresini 5 dk'dan 1 saate TAŞIDIM.
// Uçurumu ötelemek düzeltme değildi: operatör bir çentik ötede aynı
// duvara çarptı, üstelik bu sefer P2'ye bile uğramadan P3'e düştü
// (çünkü P2 kapıları da freshHour'a bağlıydı).
//
// KURAL: şiddet bir OLGU, tazelik ona ERİŞİM aciliyeti. Şiddet zamanla
// silinmez, yalnız aciliyet düşer — taze P1, aynı gün P2, sonrası P3.
// CLAUDE.md'nin kendi tanımı da bunu söylüyor: P2 = "bugün".

func ns(t time.Time) int64 { return t.UnixNano() }

// operatörün satırı, birebir sayılarla.
func opGroup(lastSeenAgo time.Duration) chstore.ExceptionGroup {
	last := time.Now().Add(-lastSeenAgo)
	return chstore.ExceptionGroup{
		Service:     "cm-put-service",
		Type:        "org.springframework.transaction.CannotCreateTransactionException",
		Occurrences: 101132,
		FirstSeen:   ns(last.Add(-(2*time.Minute + 58*time.Second))),
		LastSeen:    ns(last),
	}
}

func TestBurstDecaysByStepNotCliff(t *testing.T) {
	cases := []struct {
		name     string
		ago      time.Duration
		wantPrio string
	}{
		{"taze — hâlâ akıyor", 30 * time.Second, "P1"},
		{"59 dk — pencere içinde", 59 * time.Minute, "P1"},
		// ASIL VAKA: v0.9.698 ve öncesinde burası "P3" idi.
		{"66 dk — operatörün gördüğü an", 66 * time.Minute, "P2"},
		{"23 sa — hâlâ bugün", 23 * time.Hour, "P2"},
		{"25 sa — artık sırası gelince", 25 * time.Hour, "P3"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, reason := exceptionPriority(opGroup(tc.ago))
			if got != tc.wantPrio {
				t.Fatalf("101.132 olay / 2dk58sn (~34bin/dk), %v önce bitti:\n"+
					"  öncelik = %q, beklenen %q (gerekçe: %q)", tc.ago, got, tc.wantPrio, reason)
			}
		})
	}
}

// Gerekçe her basamakta patlamanın GERÇEK büyüklüğünü taşımalı.
// "steady" bir patlama için verinin şekli hakkında YANLIŞ bir iddiadır —
// öncelik düşebilir, cümle yalan olamaz.
func TestBurstReasonNeverSaysSteady(t *testing.T) {
	for _, ago := range []time.Duration{
		30 * time.Second, 66 * time.Minute, 25 * time.Hour, 40 * 24 * time.Hour,
	} {
		_, reason := exceptionPriority(opGroup(ago))
		if strings.Contains(reason, "steady") {
			t.Errorf("%v önce biten 101.132'lik patlamanın gerekçesi %q — "+
				"3 dakikada 101 bin olay 'steady' değildir", ago, reason)
		}
		if !strings.Contains(reason, "101.132") && !strings.Contains(reason, "101,132") {
			t.Errorf("%v: gerekçe hacmi söylemiyor: %q", ago, reason)
		}
		if !strings.Contains(reason, "/dk") {
			t.Errorf("%v: gerekçe hızı söylemiyor: %q", ago, reason)
		}
	}
}

// Ters yön — GENİŞLETME DEĞİL. Patlama olmayan eski bir grup P3 ve
// "steady" kalmalı; yoksa düzeltme her şeyi P2'ye terfi ettirir ve
// öncelik anlamını kaybeder (P1/P2 kutusu şişerse kimse bakmaz).
func TestNonBurstStillDecaysToP3(t *testing.T) {
	last := time.Now().Add(-3 * time.Hour)
	g := chstore.ExceptionGroup{
		Service:     "quiet-service",
		Occurrences: 120, // eşiğin (1000) altında
		FirstSeen:   ns(last.Add(-48 * time.Hour)),
		LastSeen:    ns(last),
	}
	got, reason := exceptionPriority(g)
	if got != "P3" || reason != "steady" {
		t.Fatalf("48 saate yayılmış 120 olay = kronik: (%q, %q), beklenen (P3, steady)", got, reason)
	}
}

// Taban hacim kapısı duruyor: hız yüksek ama toplam düşükse P1 değil.
// (5 saniyede 20 olay da 240/dk eder — exceptionBurstMinTotal bunu eler.)
func TestHighRateLowVolumeIsNotBurst(t *testing.T) {
	last := time.Now().Add(-2 * time.Minute)
	g := chstore.ExceptionGroup{
		Service:     "blip-service",
		Occurrences: 20,
		FirstSeen:   ns(last.Add(-5 * time.Second)),
		LastSeen:    ns(last),
	}
	if got, _ := exceptionPriority(g); got == "P1" {
		t.Fatal("5 saniyede 20 olay P1 olmamalı — taban hacim kapısı delinmiş")
	}
}
