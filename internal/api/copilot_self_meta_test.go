package api

import (
	"strings"
	"testing"
)

// copilot_self_meta_test.go — v0.10.13, operatör bildirimi:
// "sen hangi modelsin" → "Yüklü dokümanlarda bu bilgi yok."
//
// AYNI SINIFIN ÜÇÜNCÜ TEKRARI. Bir soru hiçbir guided intent'e uymayınca
// RAG doküman yoluna savruluyor ve cevap orada olmadığı için ölü dönüyor:
//   • v0.9.537 — sohbete yapıştırılan 32-hex trace ID'si
//   • v0.9.1142 — "kuyrukta birikme var mı"
//   • v0.10.13 — asistanın KENDİSİ hakkındaki soru
//
// Üçünde de cevap elimizdeydi; eksik olan yönlendirmeydi. Bu test o
// yönlendirmeyi çiviliyor.

func TestIsSelfMetaQuestion(t *testing.T) {
	yes := []string{
		// OPERATÖRÜN YAZDIĞI cümle — bu testin var olma sebebi.
		"sen hangi modelsin",
		"sen hangi modelsin?",
		"hangi modeli kullanıyorsun",
		"kimsin",
		"sen kimsin",
		"siz hangi modelsiniz",
		"which model are you",
		"who are you",
		"hangi llm",
		"sen hangi yapay zeka modelisin",
	}
	for _, q := range yes {
		t.Run("EVET/"+q, func(t *testing.T) {
			if !isSelfMetaQuestion(guidedTokens(normalizeGuidedMsg(q))) {
				t.Errorf("%q self-meta sayılmadı — RAG'a düşer ve ölü cevap alır", q)
			}
		})
	}

	// ── AYIRT EDİCİ VAKALAR ───────────────────────────────────────────
	// Kapı BİRLEŞİM olduğu için bunlar geçmemeli. Tek kelimeye dayansaydı
	// aşağıdaki telemetri soruları asistan-meta sanılır ve GERÇEK cevabı
	// olan sorular ölürdü — yani düzeltme yeni bir kusur üretirdi.
	no := []string{
		"model servisinin p99'u ne",        // "model" bir SERVİS adı olabilir
		"hangi servisler yavaş",            // özne var, kimlik yok
		"sen bana açık problemleri göster", // özne var, kimlik yok
		"checkout servisinin durumu ne",
		"dün gece neler oldu",
		"kuyrukta birikme var mı",
		"en yavaş trace'ler",
		"assistant-api servisinin hataları", // "assistant" bir servis adında
	}
	for _, q := range no {
		t.Run("HAYIR/"+q, func(t *testing.T) {
			if isSelfMetaQuestion(guidedTokens(normalizeGuidedMsg(q))) {
				t.Errorf("%q self-meta sayıldı — telemetri sorusu asistan-meta'ya kaçırıldı", q)
			}
		})
	}
}

// TestSelfMetaLosesToAnExplicitTraceID — SIRA sözleşmesi.
//
// Operatör bir trace ID yapıştırdıysa niyeti odur, cümlede "sen" geçse
// bile. Somut bir öznenin adresi, asistan hakkındaki bir merakı ezer.
func TestSelfMetaLosesToAnExplicitTraceID(t *testing.T) {
	const id = "4bf92f3577b34da6a3ce929d0e0e4736"
	r := routeGuidedIntent("sen bu trace'e bakar mısın "+id, nil, nil, nil, "")
	if r.Intent != guidedTraceByID {
		t.Errorf("intent = %q; trace ID her şeyi ezmeliydi", r.Intent)
	}
	if r.TraceID != id {
		t.Errorf("TraceID = %q; %q bekleniyordu", r.TraceID, id)
	}
}

func TestSelfMetaRoutes(t *testing.T) {
	r := routeGuidedIntent("sen hangi modelsin", nil, nil, nil, "")
	if r.Intent != guidedSelfMeta {
		t.Errorf("intent = %q; guidedSelfMeta bekleniyordu — RAG'a düşüyor demektir", r.Intent)
	}
}

// TestSelfMetaBundleIsDeterministic — kanıt YAPILANDIRMADAN gelir.
//
// AI yapılandırılmamışken bile ölü bir cevap dönmemeli: "bilgi yok"
// yerine "yapılandırılmamış" demek, operatöre ne yapacağını söyler.
func TestSelfMetaBundleIsDeterministic(t *testing.T) {
	var s Server // copilot nil — yapılandırılmamış kurulum
	ev, src, err := s.guidedSelfMetaBundle(func(string, any) {})
	if err != nil {
		t.Fatalf("hata: %v", err)
	}
	if src != "" {
		t.Errorf("kaynak = %q; bu rota ClickHouse'a gitmiyor, kaynak olmamalı", src)
	}
	if ev == "" {
		t.Fatal("kanıt boş — RAG'daki ölü cevabın aynısı")
	}
	// "Yüklü dokümanlarda bu bilgi yok" cevabının TERSİ: ne olduğunu ve
	// ne yapılacağını söylüyor.
	for _, want := range []string{"YAPILANDIRILMAMIŞ", "Ayarlar"} {
		if !strings.Contains(ev, want) {
			t.Errorf("yapılandırılmamış kanıtı %q içermiyor: %q", want, ev)
		}
	}
}
