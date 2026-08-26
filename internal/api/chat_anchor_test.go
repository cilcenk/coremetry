package api

import (
	"os"
	"strings"
	"testing"
	"time"
)

// v0.10.33 — Copilot denetiminin #5 sıradaki sınırı: MUTLAK PENCERE
// KAYBOLUYORDU.
//
//	frontend: rangeS = round((to - from) / 1e9)     ← mutlak pencere çöker
//	sunucu:   to := time.Now(); from := to - rangeS ← her zaman ŞİMDİ
//
// Operatör dün gece 03:00-04:00'a zoom yapıp "burada ne oldu" diye
// sorduğunda, sohbet aynı UZUNLUKTA ama BUGÜNKÜ pencereyi cevaplıyordu.
// Cevap makul, sayılar gerçek, kaynak doğru — yalnız YANLIŞ ZAMAN
// DİLİMİNDEN. Operatör fark edemez ve o veriyle karar verir.
//
// v0.10.32 uzunluğu düzeltmişti; bu çıpa.

func TestChatAnchorTime(t *testing.T) {
	now := time.Date(2026, 8, 25, 12, 0, 0, 0, time.UTC)

	t.Run("OPERATÖRÜN DURUMU — dün geceye zoom", func(t *testing.T) {
		want := time.Date(2026, 8, 24, 4, 0, 0, 0, time.UTC)
		got, anchored := chatAnchorTime(want.UnixMilli(), now)
		if !anchored {
			t.Fatal("mutlak pencere çıpalanmadı — cevap yine bugünden gelir")
		}
		if !got.Equal(want) {
			t.Errorf("çıpa %v; %v bekleniyordu", got, want)
		}
	})

	// ⚠ GÖRELİ ARALIK ÇIPALANMAMALI. "Son 1 saat" seçiliyken çıpayı
	// sohbetin açıldığı ana sabitlemek, uzun bir soruşturmada cevabı
	// DONDURUR: operatör yirmi dakika sonra "şimdi nasıl" diye
	// sorduğunda hâlâ yirmi dakika önceki pencereyi görür.
	t.Run("göreli aralık — şimdiye çapalanır", func(t *testing.T) {
		for _, ms := range []int64{0, -1, -99999} {
			got, anchored := chatAnchorTime(ms, now)
			if anchored {
				t.Errorf("toMs=%d çıpalandı — göreli pencere DONAR", ms)
			}
			if !got.Equal(now) {
				t.Errorf("toMs=%d için çıpa %v; şimdi bekleniyordu", ms, got)
			}
		}
	})

	// İstemci saati kayabilir ya da istek elle kurulabilir. Geçersiz bir
	// çıpa BOŞ pencere üretir ve "veri yok" gibi görünür — oysa sebep
	// çıpadır. Sessizce kabul etmektense şimdiye düşmek doğru.
	t.Run("gelecekteki çıpa REDDEDİLİR", func(t *testing.T) {
		future := now.Add(time.Hour)
		got, anchored := chatAnchorTime(future.UnixMilli(), now)
		if anchored {
			t.Error("gelecekten çıpa kabul edildi — pencere boş döner")
		}
		if !got.Equal(now) {
			t.Errorf("çıpa %v; şimdiye düşmeliydi", got)
		}
	})

	t.Run("küçük ileri kayma TOLERE edilir", func(t *testing.T) {
		// İstemci saatinin birkaç dakika ileri olması olağan; bunu
		// reddetmek meşru bir zoom'u bozardı.
		skewed := now.Add(2 * time.Minute)
		if _, anchored := chatAnchorTime(skewed.UnixMilli(), now); !anchored {
			t.Error("2 dakikalık saat kayması reddedildi — meşru zoom bozulur")
		}
	})

	t.Run("çok eski çıpa REDDEDİLİR", func(t *testing.T) {
		ancient := now.Add(-500 * 24 * time.Hour)
		if _, anchored := chatAnchorTime(ancient.UnixMilli(), now); anchored {
			t.Error("saçma derecede eski çıpa kabul edildi")
		}
	})

	t.Run("saklama ufku içindeki eski pencere KABUL edilir", func(t *testing.T) {
		// Amaç eski pencereyi yasaklamak değil, saçma değeri elemek.
		old := now.Add(-60 * 24 * time.Hour)
		if _, anchored := chatAnchorTime(old.UnixMilli(), now); !anchored {
			t.Error("60 günlük meşru pencere reddedildi")
		}
	})
}

// TestAnchoredWindowIsDeclared — SESSİZ UYGULAMA YASAK.
//
// Çıpa sessizce uygulanırsa operatör cevabın geçmiş bir pencereden
// geldiğini bilemez — kusurun aynısını bir kez daha üretmiş oluruz,
// yalnız ters yönde.
func TestAnchoredWindowIsDeclared(t *testing.T) {
	anchor := time.Date(2026, 8, 24, 4, 0, 0, 0, time.UTC)
	got := screenContextPreambleTR(ChatScreenContext{
		Service: "svc", RangeS: 3600, AnchorTo: anchor, Anchored: true,
	})
	if !strings.Contains(got, "GEÇMİŞE sabitlenmiş") {
		t.Errorf("mutlak pencere modele ilan edilmiyor: %q", got)
	}
	if !strings.Contains(got, "2026-08-24 04:00") {
		t.Errorf("çıpa anı yazılmıyor: %q", got)
	}

	// Anchored=false iken ilan EDİLMEMELİ: göreli bir pencereyi
	// mutlakmış gibi yazmak yeni bir yanlış olurdu.
	rel := screenContextPreambleTR(ChatScreenContext{
		Service: "svc", RangeS: 3600, AnchorTo: time.Now(), Anchored: false,
	})
	if strings.Contains(rel, "GEÇMİŞE sabitlenmiş") {
		t.Errorf("göreli pencere mutlak gibi ilan edildi: %q", rel)
	}
}

// TestGuidedUsesTheAnchor — KABLOLAMA PİNİ.
//
// Bu bulgunun KENDİSİ "sunucu koşulsuz time.Now() kullanıyordu"ydu.
func TestGuidedUsesTheAnchor(t *testing.T) {
	b, err := os.ReadFile("copilot_guided.go")
	if err != nil {
		t.Fatalf("copilot_guided.go okunamadı: %v", err)
	}
	src := stripGoCommentsAPI(string(b))
	if !strings.Contains(src, "to := anchorTo") {
		t.Error("guided çıpayı kullanmıyor — mutlak pencere yine şimdiye kayar")
	}
	if strings.Contains(src, "\tto := time.Now()\n\tfrom := to.Add(-time.Duration(rangeS)") {
		t.Error("koşulsuz time.Now() çapası geri gelmiş — v0.10.33 regresyonu")
	}

	c, err := os.ReadFile("copilot_chat.go")
	if err != nil {
		t.Fatalf("copilot_chat.go okunamadı: %v", err)
	}
	csrc := stripGoCommentsAPI(string(c))
	// Çıpa KADEMELERDEN ÖNCE hesaplanmalı: guided de serbest döngü de
	// aynı pencereyi görmeli, yoksa aynı soru hangi kademeye düştüğüne
	// göre farklı bir zaman diliminden cevaplanır.
	iAnchor := strings.Index(csrc, "chatAnchorTime(req.Context.ToMs")
	iGuided := strings.Index(csrc, "s.copilotChatGuided(ctx, emit,")
	if iAnchor < 0 || iGuided < 0 {
		t.Fatal("çıpa ya da guided çağrısı bulunamadı")
	}
	if iAnchor > iGuided {
		t.Error("çıpa guided'dan SONRA hesaplanıyor — kademeler farklı pencere görür")
	}
}

// TestFreeLoopAppliesTheAnchorNotJustDeclaresIt — v0.10.50.
//
// ⚠ BU DOSYANIN EN ÖNEMLİ TESTİ. v0.10.33 çıpayı serbest döngüde İKİ
// yerde İLAN ediyordu — operatöre çip, modele önsöz — ama araç katmanına
// hiç geçirmiyordu: mcptools.rangeWindow koşulsuz time.Now() kuruyordu ve
// hiçbir tool mutlak pencere argümanı almıyor.
//
// Sonuç: model BUGÜNÜN sayısını okuyup, önsöze uyarak DÜNÜN penceresi
// diye yazıyordu; çip de o yanlışı operatöre TEYİT ediyordu.
//
// Bu, düzeltmenin kusuru KÖTÜLEŞTİRDİĞİ bir durumdu: öncesinde cevap
// sessizce yanlış pencereden geliyordu, sonrasında YANLIŞ ETİKETLİ hâle
// geldi. Etiketli yanlış sorgulanmaz.
//
// Bu depoda ilan ve uygulama AYRI yerlerde yaşıyor, yani biri sessizce
// gerileyebilir. Test ikisini BİRLİKTE pinliyor.
func TestFreeLoopAppliesTheAnchorNotJustDeclaresIt(t *testing.T) {
	src := readSourceFile(t, "copilot_chat.go")

	apply := strings.Index(src, "mcptools.WithAnchor(ctx, anchorTo)")
	if apply < 0 {
		t.Fatal("serbest döngü çıpayı araç context'ine GEÇİRMİYOR — çip ve önsöz " +
			"bir pencere ilan ederken araçlar time.Now() okur ve cevap YANLIŞ " +
			"ETİKETLENİR (v0.10.33 kusuru)")
	}
	// Çıpa tool döngüsünden ÖNCE kurulmalı; sonrasına konursa hiçbir
	// araç çağrısı onu görmez ve test yeşil kalırken kusur geri gelir
	// ([[feedback-tested-but-unreachable]]).
	loop := strings.Index(src, "for round := 0; round < chatMaxToolRounds")
	if loop < 0 {
		t.Fatal("tool döngüsü bulunamadı — test bayatlamış, elle doğrula")
	}
	if apply > loop {
		t.Error("çıpa tool döngüsünden SONRA kuruluyor — hiçbir araç çağrısı görmez")
	}
	// İlan ile uygulama AYNI değere dayanmalı: çip anchorTo'yu basıyorsa
	// araçlara giden de anchorTo olmalı, başka bir değişken değil.
	if !strings.Contains(src, `"pencere: " + anchorTo.UTC()`) {
		t.Error("operatöre basılan çip artık anchorTo'dan gelmiyor — ilan ve " +
			"uygulama ayrışmış olabilir")
	}
}
