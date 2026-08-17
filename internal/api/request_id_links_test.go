package api

import (
	"os"
	"strings"
	"testing"
)

// v0.9.709 — CoSRE sohbet cevabındaki request_id → log köprüsü çipi.
//
// Operator-reported: "Request'i buluyor loglardan CoSRE ama onu log
// köprüsü veya logizleme linki ile vermiyor." İki ekran görüntüsündeki
// GERÇEK cevap biçimleri aşağıda tabloda — düzeltme her ikisini de
// linklemek zorunda.

func TestExtractRequestIDCandidates(t *testing.T) {
	// Operatörün ekranındaki kimliğin şekli (uzun alfanümerik).
	const rid = "DEPM16506020328K8001783586420260806131156100900"
	cases := []struct {
		name string
		text string
		want []string
	}{
		{
			// Ekran 1: "* Request ID:\nDEPM..." — başlık ve değer AYRI satırda.
			"ekran 1 — başlık + yeni satır",
			"Bu trace'e ait request id log köprüsü üzerinden tespit edilmiştir:\n\n* Request ID:\n" + rid + "\n* Kanıtlar: ...",
			[]string{rid},
		},
		{
			// Ekran 2: "Logizleme Linki:" başlığı ama id yine "request id"
			// anmasının penceresinde.
			"ekran 2 — logizleme başlığı",
			"İlgili trace'in request id'si aşağıdadır:\n\n* Logizleme Linki:\n" + rid,
			[]string{rid},
		},
		{
			// Trace id (32 hex) request penceresinde bile olsa ELENİR —
			// onun kendi linki var, logizleme linki değil.
			"32-hex trace id elenir",
			"request id şu trace'te: 9fc37145182089354c2c20a1c63e0817",
			nil,
		},
		{
			"anahtar kelime yoksa YAKALAMA — serbest uzun token avlanmaz",
			"Deploy v1.4.0-build.20260806 sonrası hata arttı; pod bsa-cc-7d9f84c6b4-x2v1 etkilendi.",
			nil,
		},
		{
			"rakamsız gövde kelimeleri elenir",
			"Request ID değeri aşağıda açıklanmıştır: " + rid,
			[]string{rid},
		},
		{
			"tekrar tekilleşir, sıra korunur",
			"request_id " + rid + " ve yine request id " + rid,
			[]string{rid},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := extractRequestIDCandidates(tc.text)
			if len(got) != len(tc.want) {
				t.Fatalf("aday = %v, beklenen %v", got, tc.want)
			}
			for i := range got {
				if got[i] != tc.want[i] {
					t.Fatalf("aday[%d] = %q, beklenen %q", i, got[i], tc.want[i])
				}
			}
		})
	}
}

func TestRequestIDLinks(t *testing.T) {
	const rid = "DEPM16506020328K8001783586420260806131156100900"
	tpls := correlationTemplates{
		"default": "https://logs.example.com/masterlog?requestId={value}",
		"uat":     "https://logs-uat.example.com/masterlog?requestId={value}",
	}
	text := "Request ID:\n" + rid

	t.Run("prod servis → default şablon, etiket ortamı söyler", func(t *testing.T) {
		links := requestIDLinks(text, "bsa-creditcard-ccmanagement-prod", tpls, nil)
		if len(links) != 1 {
			t.Fatalf("link sayısı %d", len(links))
		}
		if !strings.Contains(links[0].Href, "logs.example.com") || !strings.Contains(links[0].Href, rid) {
			t.Fatalf("href: %q", links[0].Href)
		}
		if !strings.HasPrefix(links[0].Label, "Log (prod)") {
			t.Fatalf("etiket ortamı söylemiyor: %q", links[0].Label)
		}
	})

	t.Run("-uat soneki uat şablonuna gider", func(t *testing.T) {
		links := requestIDLinks(text, "bsa-chatbot-uat", tpls, nil)
		if len(links) != 1 || !strings.Contains(links[0].Href, "logs-uat.example.com") {
			t.Fatalf("uat yönlendirmesi: %+v", links)
		}
	})

	t.Run("şablon yoksa LINK YOK — kırık link yokluktan kötü", func(t *testing.T) {
		if got := requestIDLinks(text, "svc", nil, nil); got != nil {
			t.Fatalf("şablonsuz link üretildi: %+v", got)
		}
	})
}

// ÇAĞRI-YERİ KAPISI: üç answer emit'i de links taşımalı. Saf testler
// yardımcı hiç ÇAĞRILMASA da geçer (v0.9.660 resetLayout dersi; bugün
// yedinci kez aynı kalıp).
func TestAllAnswerEmitsCarryRequestIDLinks(t *testing.T) {
	for file, want := range map[string]int{
		"copilot_chat.go":   2, // serbest döngü: normal + retry yolu
		"copilot_guided.go": 1,
	} {
		raw, err := os.ReadFile(file)
		if err != nil {
			t.Fatalf("%s: %v", file, err)
		}
		b := stripAPILineComments(string(raw))
		if got := strings.Count(b, "answerRequestIDLinks("); got != want {
			t.Errorf("%s: answerRequestIDLinks %d yerde, beklenen %d — bir answer emit'i çıplak kalmış olabilir",
				file, got, want)
		}
	}
}
