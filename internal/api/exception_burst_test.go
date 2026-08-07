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
	cases := []struct {
		name   string
		occ    uint64
		spanNs int64
		want   bool
	}{
		{"operatörün olayı", 11260, int64(12*time.Minute + 5*time.Second), true},
		// Hacim yüksek ama üç güne yayılmış: kronik, olay değil.
		{"kronik birikim", 11260, int64(72 * time.Hour), false},
		// Hız yüksek ama hacim düşük: 5 saniyede 20 olay = 240/dk,
		// yine de P1 değil — taban bunun için var.
		{"küçük ama hızlı", 20, int64(5 * time.Second), false},
		{"taban hacmin hemen altı", 999, int64(time.Minute), false},
		{"taban hacim + taban hız", 1000, int64(5 * time.Minute), true},
		{"tam eşikte hız", 1000, int64(5 * time.Minute), true},
		{"eşiğin hemen altında hız", 1000, int64(6 * time.Minute), false},
	}
	for _, c := range cases {
		if got := exceptionIsBurst(c.occ, 0, c.spanNs); got != c.want {
			t.Errorf("%s: isBurst=%v, beklenen %v (hız %.0f/dk)",
				c.name, got, c.want, exceptionBurstRate(c.occ, 0, c.spanNs))
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

// Bayat bir patlama P1 OLMAMALI: bir hafta önce patlayıp susmuş grup
// oncall'ı şimdi ilgilendirmiyor. Tazelik kapısı duruyor — v0.9.775'te
// varsayılanı 4 saat, ama bir kapı olmaya devam ediyor. Bu vaka (6 sa)
// bilerek pencerenin dışında seçildi.
func TestStaleBurstIsNotP1(t *testing.T) {
	now := time.Now()
	last := now.Add(-6 * time.Hour)
	g := chstore.ExceptionGroup{
		State:       "new",
		FirstSeen:   last.Add(-12 * time.Minute).UnixNano(),
		LastSeen:    last.UnixNano(),
		Occurrences: 11260,
	}
	if prio, reason := exceptionPriority(g); prio == "P1" {
		t.Fatalf("6 saat önce susmuş patlama P1 olmamalı (%s)", reason)
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
