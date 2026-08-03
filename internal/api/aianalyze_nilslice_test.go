// v0.9.597 — dizi alanları JSON'da ASLA null olmamalı.
//
// Go'da nil dilim `null` serileşir. Frontend'in TS tipi ise bu alanları
// NULLABLE DEĞİL diye tanımlıyor (`deploys: AiDeploy[]`), yani derleyici
// hiçbir koruma vermiyor ve kod doğrudan `.length` okuyor. Sözleşme
// yalandı ve yalan sessiz değildi: TypeError → route ErrorBoundary →
// operatör AI kartını değil TÜM Servis Overview sayfasını kaybediyordu.
//
// Tetikleyici NADİR DEĞİL, NORMAL: penceresinde deploy olmayan bir
// servis. Varsayılan 30 dakikada servislerin çoğu böyle.
//
// Test JSON DÜZEYİNDE yazıldı, ilklemeyi kontrol ederek değil — çünkü
// frontend'in gerçekten gördüğü şey bu. Biri ilklemeyi kaldırıp yerine
// omitempty koysa "nil değil" testi geçerdi ama alan tamamen düşer,
// TS'te undefined olur ve `.length` yine patlardı; daha sinsi bir
// biçimde.
package api

import (
	"encoding/json"
	"strings"
	"testing"
)

// arrayFieldsNeverNull — gövdedeki her dizi alanı `[]` mi?
func assertNoNullArrays(t *testing.T, label string, v any, fields ...string) {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("%s serileştirilemedi: %v", label, err)
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatalf("%s çözümlenemedi: %v", label, err)
	}
	for _, f := range fields {
		raw, ok := m[f]
		if !ok {
			t.Errorf("%s.%s alanı JSON'da HİÇ YOK — frontend `undefined.length` "+
				"okur ve sayfa ErrorBoundary'ye düşer. omitempty bu hatanın "+
				"çözümü değil, daha sinsi hâli.", label, f)
			continue
		}
		if string(raw) == "null" {
			t.Errorf("%s.%s = null. Frontend tipi bu alanı NULLABLE DEĞİL diye "+
				"tanımlıyor ve doğrudan `.length` okuyor → TypeError → route "+
				"ErrorBoundary TÜM sayfayı hata ekranıyla değiştirir.", label, f)
		}
	}
}

// TestServiceContextArraysNeverNull — SUNUCU kaynaklı hat.
//
// Hiçbir okuma yapılmamış bir bağlam (boş pencere, susmuş servis) bile
// dizileri `[]` olarak vermeli.
func TestServiceContextArraysNeverNull(t *testing.T) {
	// buildServiceContext'in ürettiği ŞEKLİ birebir taklit eder:
	// yalnız Service/RangeS + dizi ilklemesi. Store'a dokunmuyor.
	cx := &aiServiceContext{
		Service: "checkout-service", RangeS: 1800,
		TopErrors: []aiErrCount{}, Deploys: []aiDeploy{},
		Upstream: []string{}, Downstream: []string{},
	}
	assertNoNullArrays(t, "context", cx, "topErrors", "deploys", "upstream", "downstream")

	// Ve asıl sözleşme: ilkleme YAPILMAZSA test kırmızı vermeli.
	// Bu satır o kontrolün kendisi — aşağıdaki nil bağlam null üretir.
	var nilCx aiServiceContext
	b, _ := json.Marshal(&nilCx)
	if !strings.Contains(string(b), `"deploys":null`) {
		t.Fatal("ilklenmemiş bağlam `null` ÜRETMİYOR — testin dayandığı " +
			"varsayım çürümüş, vaka artık hiçbir şey kanıtlamıyor")
	}
}

// TestBuildServiceContextInitialisesArrays — İLKLEME gerçekten yapılıyor mu?
//
// Yukarıdaki test şekli doğruluyor; bu test KAYNAĞI. İkisi ayrı çünkü
// biri sözleşmeyi, öteki o sözleşmeyi sağlayan yeri koruyor.
func TestBuildServiceContextInitialisesArrays(t *testing.T) {
	src := readAPISourceNoComments(t, "copilot_aianalyze.go")
	i := strings.Index(src, "func (s *Server) buildServiceContext(")
	if i < 0 {
		t.Fatal("buildServiceContext bulunamadı — test bayatladı")
	}
	body := src[i:]
	if j := strings.Index(body[1:], "\nfunc "); j >= 0 {
		body = body[:j+1]
	}
	for _, want := range []string{
		"TopErrors: []aiErrCount{}", "Deploys: []aiDeploy{}",
		"Upstream: []string{}", "Downstream: []string{}",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("%s ilklemesi yok — o alan nil kalır, JSON'da null olur ve "+
				"frontend'de sayfayı düşürür", want)
		}
	}
}

// TestParsedAnalysisArraysNeverNull — MODEL kaynaklı hat.
//
// Şema bu alanları required kılıyor ama şema YALNIZ OpenAI-uyumlu yolda
// gönderiliyor; anthropic sağlayıcıda ya da json_schema reddedilip
// merdiven json_object'e düştüğünde tek güvence prompt'un ricası
// kalıyor. Bir modelin ricayı tutacağına güvenmek, çökmeyi modele
// emanet etmektir.
func TestParsedAnalysisArraysNeverNull(t *testing.T) {
	cases := map[string]string{
		"model kanit'i atladı":    `{"ozet":"o","olasi_neden":"n","oneriler":["a"],"guven":"orta"}`,
		"model oneriler'i atladı": `{"ozet":"o","olasi_neden":"n","kanit":["a"],"guven":"orta"}`,
		"model ikisini de atladı": `{"ozet":"o","olasi_neden":"n","guven":"dusuk"}`,
		"model açıkça null yazdı": `{"ozet":"o","kanit":null,"oneriler":null,"guven":"orta"}`,
		"kod çiti içinde eksik":   "```json\n{\"ozet\":\"o\",\"guven\":\"orta\"}\n```",
	}
	for name, raw := range cases {
		t.Run(name, func(t *testing.T) {
			a := parseServiceAnalysis(raw)
			if a == nil {
				t.Fatal("ayrıştırma nil döndü — vaka geçersiz")
			}
			assertNoNullArrays(t, "analysis", a, "kanit", "oneriler")
		})
	}
}
