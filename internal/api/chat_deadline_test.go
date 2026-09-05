package api

import (
	"os"
	"strings"
	"testing"
	"time"
)

// v0.10.24 — Copilot denetimi: sohbet yolunda UÇTAN UCA tavan yoktu.
// handler yalnız tool başına 20s, http.Server'da hiç tavan yok (SSE için
// doğru), istemci ham fetch. Bağlantı pratikte sonsuza kadar asılı
// kalabiliyordu.
//
// ⚠ İLK ANALİZİM YANLIŞTI ve düzeltilmesi tasarımı değiştirdi: "5 tur ×
// 180s" diye başladım, ama döngü sağlayıcı hatasında `break` ediyor —
// dolu bir timeout turu döngüyü sürdürmüyor, BİTİRİYOR. Gerçekten
// sınırsız olan eksen tur başına tool çağrısı SAYISI (her biri 20s,
// adedi tavansız). Tek deadline ikisini birden kapatıyor çünkü tool
// bağlamı ondan türüyor.

func TestChatExchangeTimeout(t *testing.T) {
	for _, tc := range []struct {
		name    string
		perCall time.Duration
		want    time.Duration
	}{
		{"varsayılan 180s → 3×", 180 * time.Second, 540 * time.Second},
		{"orta 120s → 3×", 120 * time.Second, 360 * time.Second},
		// Alt sınır: operatör tavanı 10s'ye indirse 30s'lik bir uçtan uca
		// pencere alışverişi daha başlamadan öldürürdü.
		{"en düşük 10s → alt sınıra çekilir", 10 * time.Second, chatDeadlineMin},
		{"59s → alt sınıra çekilir", 59 * time.Second, chatDeadlineMin},
		{"tam alt sınırda", 60 * time.Second, chatDeadlineMin},
		// Üst sınır: 600 × 3 = 30 dk "sonsuz"dan ayırt edilemez.
		{"en yüksek 600s → üst sınıra kırpılır", 600 * time.Second, chatDeadlineMax},
		{"400s → üst sınıra kırpılır", 400 * time.Second, chatDeadlineMax},
		// Yapılandırma okunamadıysa ölçülmemiş bir çarpan üretme.
		{"sıfır → alt sınır", 0, chatDeadlineMin},
		{"negatif → alt sınır", -5 * time.Second, chatDeadlineMin},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := chatExchangeTimeout(tc.perCall); got != tc.want {
				t.Errorf("chatExchangeTimeout(%v) = %v; want %v", tc.perCall, got, tc.want)
			}
		})
	}
}

// TestDeadlineNeverShorterThanOneCall — düzeltmenin çalışan kurulumu
// BOZMAMASI.
//
// Sabit bir sayı (ör. 300s) seçseydim, operatör çağrı-başı tavanı 600s'ye
// çektiğinde uçtan uca tavan TEK bir meşru çağrıdan kısa kalırdı: her
// yavaş cevap kesilir, ürün çalışmaz hâle gelirdi. Tavanın çağrı-başı
// değerden küçük olamayacağı, bu düzeltmenin en önemli sözleşmesi.
func TestDeadlineNeverShorterThanOneCall(t *testing.T) {
	for s := 10; s <= 600; s += 10 {
		perCall := time.Duration(s) * time.Second
		if got := chatExchangeTimeout(perCall); got < perCall {
			t.Fatalf("perCall=%v için tavan %v — TEK bir meşru çağrıdan kısa", perCall, got)
		}
	}
}

func TestChatDeadlineMessage(t *testing.T) {
	msg := chatDeadlineMessageTR(540 * time.Second)
	// Operatöre NE OLDUĞUNU ve NE YAPACAĞINI söylemeli.
	// "dar" hem "daralt" hem "daha dar" biçimini kapsıyor; ilk yazımda
	// tam biçimi çivilemiştim ve test kırmızı oldu — mesajda eylem ZATEN
	// vardı, iddia fazla dardı.
	for _, want := range []string{"9 dakika", "tavan", "dar"} {
		if !strings.Contains(msg, want) {
			t.Errorf("mesaj %q içermiyor: %q", want, msg)
		}
	}
	// Ham Go metni sızmamalı: operatör "context deadline exceeded"i
	// "model zaman aşımına uğradı" diye okur ve MODELİ suçlar, oysa olan
	// alışverişin tavana dayanmasıdır — farklı bir eylem gerektiriyor.
	if strings.Contains(strings.ToLower(msg), "context") {
		t.Errorf("ham Go hata metni sızıyor: %q", msg)
	}
}

func TestFmtChatDeadline(t *testing.T) {
	for _, tc := range []struct {
		d    time.Duration
		want string
	}{
		{540 * time.Second, "9 dakika"},
		{180 * time.Second, "3 dakika"},
		{900 * time.Second, "15 dakika"},
		{45 * time.Second, "45 saniye"},
	} {
		if got := fmtChatDeadlineTR(tc.d); got != tc.want {
			t.Errorf("fmtChatDeadlineTR(%v) = %q; want %q", tc.d, got, tc.want)
		}
	}
}

// TestHandlerAppliesTheDeadline — KABLOLAMA PİNİ.
//
// Saf çekirdek yeşil ama handler onu kurmuyorsa kusur yerinde kalır —
// bu depoda tekrar eden sınıf (v0.9.1334, v0.10.11).
//
// Ayrıca SIRA önemli: deadline `copilot.WithMeta` SARMALANMADAN önce
// kurulmalı ki tool bağlamı (ctx'ten türeyen 20s'lik WithTimeout) da
// tavana tabi olsun. Türemezse tur başına tavansız tool çağrısı ekseni
// açık kalır — yani asıl kusur düzelmez.
func TestHandlerAppliesTheDeadline(t *testing.T) {
	b, err := os.ReadFile("copilot_chat.go")
	if err != nil {
		t.Fatalf("copilot_chat.go okunamadı: %v", err)
	}
	src := stripGoCommentsAPI(string(b))

	for _, must := range []string{
		"chatExchangeTimeout(s.copilot.ClientTimeout())",
		"context.WithTimeout(r.Context(), exchangeMax)",
		"defer cancelExchange()",
		"copilot.WithMeta(dctx,",
	} {
		if !strings.Contains(src, must) {
			t.Errorf("handler deadline'ı kurmuyor, kayıp: %s", must)
		}
	}
	// Tool bağlamı deadline'lı ctx'ten türemeli.
	iDeadline := strings.Index(src, "context.WithTimeout(r.Context(), exchangeMax)")
	// v0.10.401 — araç bütçesi mcp.ToolCallBudget (telle aynı sayı).
	iTool := strings.Index(src, "context.WithTimeout(ctx, mcp.ToolCallBudget)")
	if iDeadline < 0 || iTool < 0 {
		t.Fatal("deadline ya da tool timeout'u bulunamadı")
	}
	if iTool < iDeadline {
		t.Error("tool bağlamı deadline'dan ÖNCE kuruluyor — tavana tabi olmaz")
	}
	// Tavan dolduğunda ham Go metni değil, eyleme dönük cümle.
	if !strings.Contains(src, "chatDeadlineMessageTR(exchangeMax)") {
		t.Error("tavan dolduğunda operatöre ham `context deadline exceeded` gidiyor")
	}
}

func stripGoCommentsAPI(src string) string {
	var b strings.Builder
	for _, line := range strings.Split(src, "\n") {
		if i := strings.Index(line, "//"); i >= 0 {
			line = line[:i]
		}
		b.WriteString(line)
		b.WriteByte('\n')
	}
	return b.String()
}
