// v0.9.588 — süreklilik kapısının regresyon testi.
//
// Operatör raporu: her rollout'ta ~6 problem "source silent"
// damgasıyla kapanıp hemen yeniden açılıyor.
//
// Testin iki yönü var ve ikisi de şart:
//   - rollout'tan hemen sonra süpürme koşmamalı (yanlış kapanış yok)
//   - süreklilik dolunca KOŞMALI (v0.5.352'nin çözdüğü asıl sorun —
//     susmuş kaynağın problemi sonsuza dek açık kalması — geri gelmesin)
//
// Bir kapının tehlikeli kipi, kapattığı şeyle birlikte gerekli olanı da
// kapatmasıdır.
package evaluator

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/cilcenk/coremetry/internal/cache"
)

const contInterval = time.Minute

func contEval() *Evaluator { return &Evaluator{interval: contInterval} }

// heartbeatCache — içinde verilen kalp atışı duran sahte Redis.
// stamps_test.go'daki fakeStampCache yeniden kullanılıyor; ikinci bir
// sahte cache yazmak aynı sözleşmenin iki kopyasını üretirdi.
func heartbeatCache(t *testing.T, hb Heartbeat) cache.Cache {
	t.Helper()
	b, err := json.Marshal(hb)
	if err != nil {
		t.Fatalf("kalp atışı serileştirilemedi: %v", err)
	}
	c := newFakeStampCache()
	if err := c.Set(context.Background(), HeartbeatKey, b, heartbeatTTL); err != nil {
		t.Fatalf("sahte cache yazılamadı: %v", err)
	}
	return c
}

// TestSweepBlockedRightAfterRestart — operatörün vakası.
// Yeni pod, boş bellek, ilk tik: her problem bayat görünür.
func TestSweepBlockedRightAfterRestart(t *testing.T) {
	e := contEval()
	now := time.Unix(1_760_000_000, 0)

	// stamps nil → readHeartbeat "kanıt yok" der; rollout'ta Redis'e
	// erişilemeyen ya da kalp atışı hiç yazılmamış hali.
	e.noteTickContinuity(now)

	if e.sweepIsTrustworthy(now) {
		t.Fatal("ilk tikte süpürme güvenilir sayıldı — rollout'ta HER problem " +
			"bayat görünür ve hepsi 'source silent' damgasıyla kapanırdı")
	}
	// İki tik sonra hâlâ hayır: eşik 3× aralık.
	if e.sweepIsTrustworthy(now.Add(2 * contInterval)) {
		t.Error("2 tik sonra güvenilir sayıldı — eşik 3× aralık olmalı, yoksa " +
			"süpürme kendi tazeleyemediği problemleri kapatır")
	}
}

// TestSweepAllowedOnceContinuous — kapı AÇILMALI da.
// v0.5.352'nin çözdüğü sorun (susmuş kaynağın problemi sonsuza dek
// açık kalır) geri gelmemeli.
func TestSweepAllowedOnceContinuous(t *testing.T) {
	e := contEval()
	now := time.Unix(1_760_000_000, 0)

	// Kesintisiz tikler.
	for i := 0; i <= 4; i++ {
		e.noteTickContinuity(now.Add(time.Duration(i) * contInterval))
	}
	at := now.Add(4 * contInterval)
	if !e.sweepIsTrustworthy(at) {
		t.Fatal("kesintisiz 4 tikten sonra süpürme hâlâ bloke — susmuş kaynağın " +
			"problemi sonsuza dek açık kalır (v0.5.352 geri gelir)")
	}
}

// TestGapResetsContinuity — asıl mekanizma.
// Uzun bir kesintiden sonra sayaç SIFIRLANMALI, yoksa kapı hiçbir şey
// yapmamış olur.
func TestGapResetsContinuity(t *testing.T) {
	e := contEval()
	now := time.Unix(1_760_000_000, 0)
	for i := 0; i <= 4; i++ {
		e.noteTickContinuity(now.Add(time.Duration(i) * contInterval))
	}
	// 10 dakikalık kesinti (rollout, lider kaybı, pod yeniden başlatma).
	back := now.Add(4*contInterval + 10*time.Minute)
	e.noteTickContinuity(back)

	if e.sweepIsTrustworthy(back) {
		t.Error("kesintiden hemen sonra süpürme güvenilir sayıldı — kesinti " +
			"boyunca hiçbir dedektör tazeleme yapamadı, bayatlık BİZDEN")
	}
	if !e.sweepIsTrustworthy(back.Add(3 * contInterval)) {
		t.Error("kesintiden 3 tik sonra hâlâ bloke — kapı kalıcı kapanmış")
	}
}

// TestSingleMissedTickToleratedd — tek kaçan tik (lider devri, ağ
// takılması) sürekliliği BOZMAMALI. Aksi halde kapı gereğinden sık
// kapanır ve susmuş kaynaklar birikir.
func TestSingleMissedTickTolerated(t *testing.T) {
	e := contEval()
	now := time.Unix(1_760_000_000, 0)
	e.noteTickContinuity(now)
	e.noteTickContinuity(now.Add(contInterval))
	// Bir tik kaçtı: 2× aralık sonra geldi.
	e.noteTickContinuity(now.Add(3 * contInterval))
	e.noteTickContinuity(now.Add(4 * contInterval))

	at := now.Add(4 * contInterval)
	if !e.sweepIsTrustworthy(at) {
		t.Error("tek kaçan tik sürekliliği sıfırladı — bu tolerans yoksa her " +
			"lider devrinde süpürme 3 tik boyunca durur")
	}
}

// TestContinuityInheritedAcrossPods — rollout'un ASIL kazancı.
//
// Yeni pod'un belleği boş ama fleet birkaç saniyedir kesintide. Redis'
// teki kalp atışı bunu bilen tek yer: devralınan ContinuousSince
// sayesinde yeni lider sıfırdan saymaz.
func TestContinuityInheritedAcrossPods(t *testing.T) {
	now := time.Unix(1_760_000_000, 0)
	// Önceki lider 10 dakikadır kesintisiz koşuyordu ve son tikini 5 sn
	// önce attı.
	prev := Heartbeat{
		StartedAt:       now.Add(-5 * time.Second).UnixNano(),
		ContinuousSince: now.Add(-10 * time.Minute).UnixNano(),
	}
	e := contEval()
	e.stamps = heartbeatCache(t, prev)

	e.noteTickContinuity(now)

	if !e.sweepIsTrustworthy(now) {
		t.Error("devralma çalışmadı — önceki lider 10 dk kesintisiz koşmuştu ve " +
			"boşluk 5 sn'ydi; yeni pod'un 3 tik beklemesi için sebep yok")
	}
}

// TestRealGapAcrossPodsStillResets — devralma bir ARKA KAPI olmamalı.
// Kalp atışı eskiyse (gerçek kesinti) sayaç yine sıfırlanır.
func TestRealGapAcrossPodsStillResets(t *testing.T) {
	now := time.Unix(1_760_000_000, 0)
	prev := Heartbeat{
		// Son tik 20 dakika önce: fleet gerçekten durmuştu.
		StartedAt:       now.Add(-20 * time.Minute).UnixNano(),
		ContinuousSince: now.Add(-3 * time.Hour).UnixNano(),
	}
	e := contEval()
	e.stamps = heartbeatCache(t, prev)

	e.noteTickContinuity(now)

	if e.sweepIsTrustworthy(now) {
		t.Error("20 dakikalık gerçek kesintiden sonra devralınan ContinuousSince " +
			"kabul edildi — bu kapıyı tamamen etkisiz kılar")
	}
}
