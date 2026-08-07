package api

import (
	"strings"
	"testing"
	"time"

	"github.com/cilcenk/coremetry/internal/chstore"
)

// v0.9.775 — operatör-bildirimli, BU SINIFTA ÜÇÜNCÜ vaka.
//
// PROD EKRANI (2026-08-08, 00:22'de bakıldı):
//
//	1.575 olay / 7dk patlama · 22:37'de bitti  → 1sa45dk yaş
//	  görülen: P2 · beklenen: P1
//	191 olaylık SQLTimeout grubu · 1sa50dk yaş
//	  görülen: P3 · beklenen: P2
//
// v0.9.627 penceresi 5dk → 1sa yaptı, v0.9.699 uçurumu basamağa
// çevirdi; ikisi de doğruydu ama basamağın YERİ hâlâ dardı ve KODDA
// gömülüydü. Bu test hem yeni varsayılan pencereyi (4 saat) hem de
// pencerenin ayarlanabilir olduğunu çiviliyor.

// exGroup — verilen yaş + ömür + hacimle bir grup.
func exGroup(occ uint64, span, lastSeenAgo time.Duration) chstore.ExceptionGroup {
	last := time.Now().Add(-lastSeenAgo)
	return chstore.ExceptionGroup{
		Service:     "cm-put-service",
		Type:        "org.springframework.jdbc.CannotGetJdbcConnectionException",
		State:       "new",
		Occurrences: occ,
		FirstSeen:   last.Add(-span).UnixNano(),
		LastSeen:    last.UnixNano(),
	}
}

// Operatörün iki satırı + basamağın tamamı, VARSAYILAN ayarla.
func TestExceptionPriorityDefaultLadder(t *testing.T) {
	def := chstore.DefaultExceptionTriage()
	if def.P1FreshHours != 4 {
		t.Fatalf("varsayılan P1 penceresi %dsa; v0.9.775 kararı 4sa "+
			"(problem tarafındaki 'critical open ≥4h → P1' ile simetrik)", def.P1FreshHours)
	}

	cases := []struct {
		name string
		occ  uint64
		span time.Duration
		ago  time.Duration
		want string
	}{
		// ── PATLAMA DALI ────────────────────────────────────────────
		// Operatörün 1. satırı: 1.575 olay / 7dk = ~225/dk, eşiğin
		// (200/dk + 1000 taban) üstünde → patlama. 22:37'de bitti,
		// 00:22'de bakıldı.
		{"operatörün patlaması — 1sa45dk", 1575, 7 * time.Minute, 1*time.Hour + 45*time.Minute, "P1"},
		{"hâlâ akıyor", 1575, 7 * time.Minute, 30 * time.Second, "P1"},
		// v0.9.699 vakası: 66 dk. ESKİDEN P2 idi (v0.9.699 onu P3'ten
		// kurtarmıştı); yeni pencerede P1 — operatörün o gün de
		// beklediği buydu ("P1 ya da P2'ydi bence doğru").
		{"v0.9.699 vakası — 66dk", 101132, 2*time.Minute + 58*time.Second, 66 * time.Minute, "P1"},
		{"pencerenin hemen içi — 3sa59dk", 1575, 7 * time.Minute, 3*time.Hour + 59*time.Minute, "P1"},
		// Pencere kapandı ama gün kapanmadı: P2, uçurum yok.
		{"5sa — aciliyet düştü, gün sürüyor", 1575, 7 * time.Minute, 5 * time.Hour, "P2"},
		{"23sa — hâlâ bugün", 1575, 7 * time.Minute, 23 * time.Hour, "P2"},
		{"25sa — sırası gelince", 1575, 7 * time.Minute, 25 * time.Hour, "P3"},

		// ── PATLAMA-DEĞİL DALI ──────────────────────────────────────
		// Operatörün 2. satırı: 191 olay = patlama tabanının (1000)
		// altında, ama hacim kapısını (≥100) geçiyor. 1sa50dk yaş
		// eski 1 saatlik pencerede P3'e düşüyordu; yeni pencerede P2.
		{"operatörün SQLTimeout'u — 1sa50dk", 191, 3 * time.Hour, 1*time.Hour + 50*time.Minute, "P2"},
		{"aynı grup pencere dışında — 5sa", 191, 3 * time.Hour, 5 * time.Hour, "P3"},
		// Taban hacmin altı hiçbir zaman terfi etmez — düzeltme
		// P1/P2 kutusunu şişirmemeli.
		{"99 olay — hacim kapısı kapalı", 99, 3 * time.Hour, 10 * time.Minute, "P3"},
		// Son 5 dakika + ≥500: v0.9.524'ten beri duran kapı.
		{"taze + 600 hacim", 600, 90 * time.Minute, 1 * time.Minute, "P1"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, reason := exceptionPriorityAt(exGroup(tc.occ, tc.span, tc.ago), def, time.Now())
			if got != tc.want {
				t.Fatalf("%d olay / %v, %v önce bitti: öncelik %q, beklenen %q (gerekçe %q)",
					tc.occ, tc.span, tc.ago, got, tc.want, reason)
			}
		})
	}
}

// `regressed` etiketi basamağı gölgelemez (v0.9.699 pini) ve patlamayan
// bir regressed grup P2 kalır.
func TestExceptionPriorityRegressedUnchanged(t *testing.T) {
	def := chstore.DefaultExceptionTriage()

	burst := exGroup(101132, 3*time.Minute, 90*time.Minute)
	burst.State = "regressed"
	if got, reason := exceptionPriorityAt(burst, def, time.Now()); got != "P1" {
		t.Fatalf("patlayan regressed grup P1 olmalı, alınan %s (%s)", got, reason)
	}

	quiet := exGroup(12, 72*time.Hour, 30*time.Hour)
	quiet.State = "regressed"
	if got, _ := exceptionPriorityAt(quiet, def, time.Now()); got != "P2" {
		t.Fatalf("patlamayan regressed P2 kalmalı, alınan %s", got)
	}
}

// Ters yön — GENİŞLETME DEĞİL: penceresi kapanmış kronik bir grup hâlâ
// P3 ve gerekçesi "steady". Terfiyi sınırlayan kapı hacim tabanı (≥100)
// ve pencerenin KAPANMASI; ikisi de yerinde duruyor.
func TestExceptionPriorityChronicStillP3(t *testing.T) {
	got, reason := exceptionPriorityAt(exGroup(120, 48*time.Hour, 6*time.Hour),
		chstore.DefaultExceptionTriage(), time.Now())
	if got != "P3" || reason != "steady" {
		t.Fatalf("48 saate yayılmış 120 olay kronik: (%q, %q), beklenen (P3, steady)", got, reason)
	}
}

// Gerekçe cümlesi pencereyi UYDURMAZ. Pencere ayarlanabilir olduğu an
// sabit "seen in last hour" metni bir yalana dönüşürdü — bu depoda
// kural açık: öncelik düşebilir, cümle yalan olamaz (v0.9.524/699).
func TestExceptionReasonTellsTheRealWindow(t *testing.T) {
	cfg := chstore.ExceptionTriageConfig{P1FreshHours: 6, P2SameDayHours: 24, StaleResolveHours: 24}
	_, reason := exceptionPriorityAt(exGroup(191, 3*time.Hour, 2*time.Hour), cfg, time.Now())
	if !strings.Contains(reason, "6sa") {
		t.Fatalf("gerekçe gerçek pencereyi söylemeli (6sa), alınan: %q", reason)
	}
	if strings.Contains(reason, "last hour") {
		t.Fatalf("gerekçe 1 saatlik sabit bir pencere iddia ediyor: %q", reason)
	}
	if !strings.Contains(reason, "191") {
		t.Fatalf("gerekçe hacmi söylemiyor: %q", reason)
	}
}

// Pencereler GERÇEKTEN ayardan geliyor mu — dördüncü vaka bir sürüm
// değil bir ayar değişikliği olsun diye.
func TestExceptionPriorityHonorsConfiguredWindows(t *testing.T) {
	g := exGroup(1575, 7*time.Minute, 6*time.Hour)

	// Varsayılan 4sa pencerede 6 saatlik yaş P1 DEĞİL.
	if got, _ := exceptionPriorityAt(g, chstore.DefaultExceptionTriage(), time.Now()); got != "P2" {
		t.Fatalf("varsayılan pencerede 6sa yaş P2 olmalı, alınan %s", got)
	}
	// Operatör 12 saate açarsa AYNI satır P1 olur.
	wide := chstore.ExceptionTriageConfig{P1FreshHours: 12, P2SameDayHours: 24, StaleResolveHours: 24}
	if got, _ := exceptionPriorityAt(g, wide, time.Now()); got != "P1" {
		t.Fatalf("12sa pencerede 6sa yaş P1 olmalı, alınan %s", got)
	}
	// Operatör 1 saate daraltırsa v0.9.698 davranışına döner — vida
	// iki yöne de çalışmalı, yoksa vida değil.
	narrow := chstore.ExceptionTriageConfig{P1FreshHours: 1, P2SameDayHours: 24, StaleResolveHours: 24}
	recent := exGroup(1575, 7*time.Minute, 90*time.Minute)
	if got, _ := exceptionPriorityAt(recent, narrow, time.Now()); got != "P2" {
		t.Fatalf("1sa pencerede 90dk yaş P2 olmalı, alınan %s", got)
	}
}

// Paket-global vida: hidrate edilmemişken varsayılan, set edildikten
// sonra yeni değer — ve set NORMALIZE edilmiş hâli yayınlar, yani
// okuyucular hiçbir zaman ters basamaklı bir config görmez.
func TestCurrentExceptionTriageGlobal(t *testing.T) {
	t.Cleanup(func() { exceptionTriageCfg.Store(nil) })

	exceptionTriageCfg.Store(nil)
	if got := currentExceptionTriage(); got != chstore.DefaultExceptionTriage() {
		t.Fatalf("hidrate edilmemiş global varsayılanı vermeli, alınan %+v", got)
	}

	// Ters basamak + sıfırlar: yayınlanan hâl normalize olmalı.
	setExceptionTriage(chstore.ExceptionTriageConfig{P1FreshHours: 8, P2SameDayHours: 2, StaleResolveHours: 0})
	got := currentExceptionTriage()
	if got.P1FreshHours != 8 {
		t.Errorf("P1FreshHours = %d, beklenen 8", got.P1FreshHours)
	}
	if got.P2SameDayHours != 8 {
		t.Errorf("P2SameDayHours = %d, beklenen 8 (P1'in altına inemez)", got.P2SameDayHours)
	}
	if got.StaleResolveHours != 24 {
		t.Errorf("StaleResolveHours = %d, beklenen 24 (0 → varsayılan)", got.StaleResolveHours)
	}
}
