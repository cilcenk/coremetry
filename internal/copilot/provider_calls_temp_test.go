package copilot

// provider_calls_temp_test.go — v0.9.1261 pinleri: KATI-JSON çağrıları
// sıcaklık 0'da koşar (yapısal çıktıda determinizm); prose yüzeyleri
// tuned/varsayılan sıcaklıkta kalır. Kural jsonLevelRequested'a bağlı —
// merdiven servis desteğiyle kısıtsıza düşse bile niyet yapısalsa 0.

import (
	"context"
	"os"
	"strings"
	"testing"
)

func TestStrictJSONRunsAtZeroTemperature(t *testing.T) {
	// Saf yarı: bağlam bayrağı + karar. explainOpenAI'nin gövdesine
	// girmeden, kuralın dayandığı iki yapı taşını pinliyoruz:
	// (1) WithJSONMode bayrağı jsonLevelRequested'tan okunur;
	// (2) bayraklıyken istek sıcaklığı 0'a çekilir (kod yolu
	//     provider_calls.go — kaynak-pin aşağıda).
	ctx := WithJSONMode(context.Background())
	if jsonLevelRequested(ctx) <= jsonNone {
		t.Fatal("WithJSONMode bayrağı okunamadı")
	}
	if jsonLevelRequested(context.Background()) != jsonNone {
		t.Fatal("bayraksız bağlam jsonNone olmalı")
	}
}


// Kaynak-pin: sıfır-sıcaklık bloğu iki sağlayıcı yolunda da duruyor.
// Mutasyon (bloğu sil / koşulu ters çevir) bu testi kırmızı yapar;
// derleme yeşil kalırdı — string-pin tam bu boşluk için (v0.6.36 dersi
// sınıfının kaynak-taraflı aynası).
func TestZeroTempBlockWiredInBothProviderPaths(t *testing.T) {
	src, err := os.ReadFile("provider_calls.go")
	if err != nil {
		t.Fatalf("provider_calls.go okunamadı: %v", err)
	}
	body := string(src)
	if n := strings.Count(body, "if jsonLevelRequested(ctx) > jsonNone {"); n < 2 {
		t.Fatalf("sıfır-sıcaklık kapısı 2 yolda beklenirdi (openai + anthropic), %d bulundu", n)
	}
	if !strings.Contains(body, "req.Temperature = &zero") {
		t.Fatal("sıfır-sıcaklık ataması kayıp — katı-JSON determinizmi (v0.9.1261) sökülmüş")
	}
}
