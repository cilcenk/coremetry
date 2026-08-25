package api

import (
	"os"
	"strings"
	"testing"
)

// v0.10.32 — Copilot denetiminin #1 SIRADAKİ sınırı: serbest tool
// döngüsü bağlam-kördü.
//
// İlk üç kademe req.Context.* alıyordu; döngü hiçbirini almıyordu ve
// prompt kendi varsayılanını dayatıyordu ("aksini söylemedikçe 1800").
// Ekranda checkout-service açık, 6 saatlik pencere seçiliyken sorulan
// "hata oranı ne" sorusu FİLO GENELİNE ve 30 DAKİKAYA gidiyordu — cevap
// makul görünüyor, sayılar gerçek, ama SORULAN ŞEY DEĞİL.
//
// ⚠ Kademelerin en kötü yerinde: guided'ın ıskaladığı sorular buraya
// düşüyor, yani en zor sorularda model en az bağlama sahip.

func TestScreenContextPreamble(t *testing.T) {
	t.Run("OPERATÖRÜN DURUMU — servis + 6 saat", func(t *testing.T) {
		got := screenContextPreambleTR(ChatScreenContext{
			Service: "checkout-service", RangeS: 21600,
		})
		if !strings.Contains(got, "checkout-service") {
			t.Errorf("servis geçmiyor: %q", got)
		}
		if !strings.Contains(got, "range_s=21600") {
			t.Errorf("makine-okunur aralık yok: %q", got)
		}
		// ⚠ Prompt'un 1800 varsayılanı EZİLMELİ. Ezilmezse model iki
		// çelişik talimat alır ve küçük model çelişkide genelde İLK
		// gördüğünü izler — yani düzeltme etkisiz kalır.
		if !strings.Contains(got, "1800 YERİNE") {
			t.Errorf("prompt'un 30dk varsayılanı açıkça ezilmiyor: %q", got)
		}
		// Bağlam bir VARSAYILAN, kelepçe değil: operatör tek servise
		// bakarken pekâlâ filo sorusu sorabilir.
		if !strings.Contains(got, "AKSİNİ SÖYLEMEDİKÇE") {
			t.Errorf("bağlam kelepçe gibi dayatılıyor: %q", got)
		}
	})

	t.Run("yalnız verilen alanlar yazılır", func(t *testing.T) {
		got := screenContextPreambleTR(ChatScreenContext{Service: "a"})
		for _, absent := range []string{"operation:", "ortam:", "zaman aralığı:"} {
			if strings.Contains(got, absent) {
				t.Errorf("verilmemiş alan yazılmış (%q): %q", absent, got)
			}
		}
	})

	// ⚠ EN ÖNEMLİ DAL. Boş bağlamda önsöz YOK.
	//
	// Buradaki tuzak, boş alanları "(bilinmiyor)" diye yazmak: o an modele
	// doldurulacak bir boşluk sunulmuş olur ve uydurma yüzeyi AÇILIR.
	// Uydurma bir bağlam, hiç bağlam olmamasından kötüdür.
	t.Run("boş bağlam — önsöz YOK", func(t *testing.T) {
		for _, c := range []ChatScreenContext{
			{},
			{Service: "  ", Env: "\t"},
			{RangeS: 0},
			{RangeS: -5},
		} {
			if got := screenContextPreambleTR(c); got != "" {
				t.Errorf("boş bağlamda önsöz üretildi (%+v): %q", c, got)
			}
		}
	})

	t.Run("tüm alanlar", func(t *testing.T) {
		got := screenContextPreambleTR(ChatScreenContext{
			Service: "svc", Operation: "GET /x", Env: "uat", RangeS: 3600,
		})
		for _, want := range []string{"svc", "GET /x", "uat", "son 1 saat", "range_s=3600"} {
			if !strings.Contains(got, want) {
				t.Errorf("%q eksik: %q", want, got)
			}
		}
	})
}

// TestScreenContextChip — ŞEFFAFLIK.
//
// Bağlam sessizce uygulanırsa operatör cevabın neden o kapsamda olduğunu
// bilemez. v0.9.1259'da env için şeffaflık eklenmişti; servis ve aralık
// için eklenmemişti.
func TestScreenContextChip(t *testing.T) {
	got := screenContextChipTR(ChatScreenContext{Service: "svc", Env: "uat", RangeS: 21600})
	for _, want := range []string{"svc", "env=uat", "son 6 saat"} {
		if !strings.Contains(got, want) {
			t.Errorf("%q eksik: %q", want, got)
		}
	}
	if screenContextChipTR(ChatScreenContext{}) != "" {
		t.Error("boş bağlamda çip üretildi — operatöre boş bir kapsam ilan edilir")
	}
}

func TestFmtRangeTR(t *testing.T) {
	for _, tc := range []struct {
		in   int64
		want string
	}{
		{21600, "son 6 saat"},
		{3600, "son 1 saat"},
		{1800, "son 30 dakika"},
		{86400, "son 1 gün"},
		{172800, "son 2 gün"},
		{90, "son 1 dakika"},
		{45, "son 45 saniye"},
		// 5400 = 1.5 saat: saat'e tam bölünmüyor, dakikaya düşmeli.
		{5400, "son 90 dakika"},
	} {
		if got := fmtRangeTR(tc.in); got != tc.want {
			t.Errorf("fmtRangeTR(%d) = %q; want %q", tc.in, got, tc.want)
		}
	}
}

func TestScreenContextEmpty(t *testing.T) {
	for _, tc := range []struct {
		name string
		c    ChatScreenContext
		want bool
	}{
		{"tamamen boş", ChatScreenContext{}, true},
		{"yalnız boşluk", ChatScreenContext{Service: " ", Operation: "\t", Env: "  "}, true},
		{"sıfır aralık", ChatScreenContext{RangeS: 0}, true},
		{"servis var", ChatScreenContext{Service: "a"}, false},
		{"yalnız aralık", ChatScreenContext{RangeS: 60}, false},
		{"yalnız env", ChatScreenContext{Env: "prod"}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.c.Empty(); got != tc.want {
				t.Errorf("Empty() = %v; want %v", got, tc.want)
			}
		})
	}
}

// TestChatWiresScreenContext — KABLOLAMA PİNİ.
//
// Bu bulgunun KENDİSİ "bağlam hazırdı ama döngüye ulaşmıyordu"ydu;
// aynı şeyin tekrarını pinliyoruz.
func TestChatWiresScreenContext(t *testing.T) {
	b, err := os.ReadFile("copilot_chat.go")
	if err != nil {
		t.Fatalf("copilot_chat.go okunamadı: %v", err)
	}
	src := stripGoCommentsAPI(string(b))

	for _, must := range []string{
		"screenCtx := ChatScreenContext{",
		"screenContextPreambleTR(screenCtx)",
		"screenContextChipTR(screenCtx)",
		"Service:   req.Context.Service",
		"RangeS:    req.Context.RangeS",
	} {
		if !strings.Contains(src, must) {
			t.Errorf("ekran bağlamı kablolanmamış, kayıp: %s", must)
		}
	}
	// Önsöz prompt'un ÖNÜNE gelmeli: sonuna eklenirse prompt'un kendi
	// 1800 kuralı önce okunur ve küçük model çelişkide ilk gördüğünü
	// izleme eğilimindedir.
	iPre := strings.Index(src, "screenContextPreambleTR(screenCtx) +")
	if iPre < 0 {
		t.Error("önsöz prompt'un ÖNÜNE eklenmiyor")
	}
}
