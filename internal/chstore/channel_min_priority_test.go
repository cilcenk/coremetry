// v0.9.828 — kanal başına en düşük triyaj basamağı süzgeci.
package chstore

import (
	"testing"
	"time"
)

// TestMinPriorityFilter — süzgecin ANA sözleşmesi.
//
// "P2" seçilen bir kanal P1 ve P2 alır, P3 almaz: eşik BU BASAMAK VE
// ÜSTÜ demek, "tam olarak bu basamak" değil. Operatörün beklentisi
// budur ve tersi (yalnız-eşleşen) sessizce P1'leri düşürürdü — tam da
// kaçırılmaması gereken sınıfı.
func TestMinPriorityFilter(t *testing.T) {
	cases := []struct {
		min, problem string
		want         bool
	}{
		// Süzgeç yok = hepsi geçer (bugünkü davranış, mevcut kanallar).
		{"", "P1", true}, {"", "P2", true}, {"", "P3", true}, {"", "", true},

		// P1 kanalı: yalnız P1.
		{"P1", "P1", true}, {"P1", "P2", false}, {"P1", "P3", false},
		// P2 kanalı: P1 ve P2.
		{"P2", "P1", true}, {"P2", "P2", true}, {"P2", "P3", false},
		// P3 kanalı: hepsi (fiilen süzgeçsiz).
		{"P3", "P1", true}, {"P3", "P2", true}, {"P3", "P3", true},

		// ÖLÇEMEDİĞİMİZ HALLER — AÇIK GEÇER.
		// Bir sayfayı hesaplanmamış bir alan yüzünden YEMEK, bu kapının
		// çözdüğü sorundan çok daha pahalı. Süzgecin işi gürültüyü
		// kırpmak, haber kaybetmek değil.
		{"P1", "", true},
		{"P1", "bilinmeyen", true},
		{"bilinmeyen", "P3", true},

		// Büyük/küçük harf ve boşluk toleransı: elle düzenlenmiş bir
		// match_rules JSON'ı "p1" yazabilir.
		{"p1", "P1", true}, {"p1", "P2", false}, {" P2 ", "P3", false},
	}
	for _, c := range cases {
		m := ChannelMatchRules{MinPriority: c.min}
		if got := m.allowsPriority(c.problem); got != c.want {
			t.Errorf("minPriority=%q problem=%q → %v, %v bekleniyordu",
				c.min, c.problem, got, c.want)
		}
	}
}

// TestMinPriorityRidesMatchesProblem — yüklem GERÇEKTEN funnel'ın
// kullandığı yolda olmalı. allowsPriority doğru olup MatchesProblem'e
// bağlanmamış olsaydı, süzgeç ayar sayfasında görünür ama hiçbir şey
// yapmazdı — bu paketin en pahalı hata biçimi (v0.9.827'nin düzelttiği
// sınıfın aynısı).
func TestMinPriorityRidesMatchesProblem(t *testing.T) {
	m := ChannelMatchRules{MinPriority: "P1"}
	if m.MatchesProblem(MatchInput{Service: "payments", Priority: "P2"}) {
		t.Error("P1 kanalı P2 problemi ALDI — süzgeç MatchesProblem'e bağlı değil")
	}
	if !m.MatchesProblem(MatchInput{Service: "payments", Priority: "P1"}) {
		t.Error("P1 kanalı P1 problemi ALMADI")
	}
	// Diğer yüklemlerle AND'lenmeli: öncelik geçse bile servis eşleşmezse
	// kanal ateşlememeli.
	m2 := ChannelMatchRules{MinPriority: "P2", Services: []string{"orders"}}
	if m2.MatchesProblem(MatchInput{Service: "payments", Priority: "P1"}) {
		t.Error("servis eşleşmemesine rağmen ateşledi — yüklemler AND'lenmiyor")
	}
	if !m2.MatchesProblem(MatchInput{Service: "orders", Priority: "P1"}) {
		t.Error("her iki yüklem de tutarken ateşlemedi")
	}
}

// TestMinPriorityNeedsNoDDL — şema sözleşmesi.
//
// Alan match_rules JSON kolonunun içinde; mevcut satırlarda YOK ve
// "yok" = süzgeç yok = bugünkü davranış. Bir migration gerekmemesinin
// nedeni bu ve testi de bu: eski bir kanal kaydı yükseltmeden sonra
// sessizce bildirim almayı bırakmamalı.
func TestMinPriorityNeedsNoDDL(t *testing.T) {
	// v0.9.828 öncesi kaydedilmiş bir kanal: alan hiç yok.
	var old ChannelMatchRules
	for _, pri := range []string{"P1", "P2", "P3", ""} {
		if !old.MatchesProblem(MatchInput{Service: "payments", Priority: pri}) {
			t.Errorf("ESKİ kanal kaydı %q önceliğini süzdü — yükseltme, "+
				"operatörün hiç istemediği bir sessizlik üretirdi", pri)
		}
	}
}

// TestDownProblemReachesAP1Channel — SÜRÜM 1.3 İLE BİRLİKTE.
//
// Monitor DOWN problemi (Value 0, Threshold 1, critical) v0.9.825'e
// kadar kalıcı P2'ydi: ters-çevirme kapısı tam sıfırı dışarıda
// bırakıyordu. minPriority="P1" olan bir çağrı-cihazı kanalı, o
// düzeltme olmadan TAMAMEN ERİŞİLEMEZ bir servisin sayfasını süzüp
// atardı — yani bu iki sürüm birbirine bağlı ve test o bağı tutuyor.
func TestDownProblemReachesAP1Channel(t *testing.T) {
	down := Problem{
		Severity: "critical", Status: "open",
		Value: 0, Threshold: 1, // monitor/runner.go'daki birebir alanlar
		StartedAt: time.Now().Add(-10 * time.Minute).UnixNano(),
	}
	pri, reason := computePriority(down, time.Now().UnixNano(), DefaultProblemPriority())
	if pri != "P1" {
		t.Fatalf("DOWN problemi %s (%s) — P1 olmalıydı (v0.9.825)", pri, reason)
	}
	pager := ChannelMatchRules{MinPriority: "P1"}
	if !pager.MatchesProblem(MatchInput{Service: "payments", Priority: pri}) {
		t.Error("P1 çağrı-cihazı kanalı DOWN sayfasını SÜZDÜ — tamamen " +
			"erişilemez bir servis kimseyi uyandırmazdı")
	}
}
