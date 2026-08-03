// v0.9.598 — halüsinasyon kalkanı, GÖSTERDİĞİMİZ veriyi uydurma
// sanıyordu.
//
// Kalkanın tanımı: "çıktıda olup GİRDİDE olmayan servis-biçimli ad".
// v0.9.580 snapshot'a iş-boyutu kırılımı ve örnek korelasyon kimlikleri
// basmaya başladı ama bilinen küme güncellenmedi — yani girdinin bir
// kısmı kalkan açısından hiç var olmadı.
//
// Sonuç ters yönde zarar veriyordu: model DOĞRU davranıp kendisine
// verilen somut kimliği alıntıladığında "⚠ doğrulanamadı" rozeti
// çıkıyordu. Bir halüsinasyon kalkanının en kötü kipi budur — yanlış
// alarm, operatörün kalkana olan güvenini de cevaba olan güvenini de
// birlikte götürür. Üstelik tam da operatörün İSTEDİĞİ davranışı
// (v0.9.580: "örnek request_id, CHANNEL_CODE değerlerini de söylesin")
// cezalandırıyordu.
package api

import (
	"strings"
	"testing"

	"github.com/cilcenk/coremetry/internal/chstore"
)

func shownCtx() *aiServiceContext {
	return &aiServiceContext{
		Service:   "checkout-service",
		TopErrors: []aiErrCount{},
		Deploys:   []aiDeploy{},
		Upstream:  []string{}, Downstream: []string{},
		Business: map[string][]chstore.BusinessSlice{
			"CHANNEL_CODE": {{Value: "internet-banking", Calls: 100, Errors: 8}},
		},
		Correlation: []chstore.CorrelationSample{
			{Key: "request_id", Values: []string{"8f3c-4a2b-91de"}},
		},
	}
}

// TestQuotedBusinessValueIsNotFlagged — operatörün istediği davranış
// cezalandırılmamalı.
func TestQuotedBusinessValueIsNotFlagged(t *testing.T) {
	a := &serviceAnalysis{
		Ozet:       "Hatalar internet-banking kanalında yoğunlaşıyor.",
		OlasiNeden: "internet-banking trafiği artmış olabilir.",
		Kanit:      []string{"CHANNEL_CODE internet-banking: 100 çağrı, 8 hata"},
		Oneriler:   []string{},
	}
	pc := postCheckServiceAnalysis(a, shownCtx())
	if !pc.Verified {
		t.Errorf("modele BİZİM verdiğimiz CHANNEL_CODE uydurma sayıldı: %v\n\n"+
			"Bu, halüsinasyon kalkanının en kötü kipi: model doğru davrandığı "+
			"için cezalandırılıyor ve operatör doğru cevabı şüpheli görüyor. "+
			"Üstelik v0.9.580'in getirdiği özelliğin ta kendisi bu.",
			pc.UnknownServices)
	}
}

// TestQuotedCorrelationIDIsNotFlagged — request_id örneği de gösterildi.
//
// Ham değeri tek anahtar olarak eklemek YETMEZ: tarayıcı jeton
// düzeyinde eşleşiyor ve "8f3c-4a2b-91de" içinden "f3c-4a2b-91de" gibi
// bir jeton çıkarabiliyor (regex `[a-z]` ile başlıyor, rakamla değil).
func TestQuotedCorrelationIDIsNotFlagged(t *testing.T) {
	a := &serviceAnalysis{
		Ozet:     "Örnek istek 8f3c-4a2b-91de bu hatayı gösteriyor.",
		Kanit:    []string{},
		Oneriler: []string{},
	}
	pc := postCheckServiceAnalysis(a, shownCtx())
	if !pc.Verified {
		t.Errorf("gösterilen korelasyon kimliği uydurma sayıldı: %v — ham değeri "+
			"tek anahtar olarak eklemek yetmez, tarayıcı jeton düzeyinde eşleşir",
			pc.UnknownServices)
	}
}

// TestErrorMessageContentIsNotFlagged — hata metni de gösterildi.
// Type boşken etikete mesaj basılıyor (renderServiceSnapshot).
func TestErrorMessageContentIsNotFlagged(t *testing.T) {
	cx := shownCtx()
	cx.TopErrors = []aiErrCount{
		{Type: "", Message: "connection refused: payment-gateway:5432", Count: 12},
	}
	a := &serviceAnalysis{
		Ozet:     "payment-gateway bağlantısı reddediliyor.",
		Kanit:    []string{},
		Oneriler: []string{},
	}
	pc := postCheckServiceAnalysis(a, cx)
	if !pc.Verified {
		t.Errorf("hata mesajında GÖSTERİLEN ad uydurma sayıldı: %v", pc.UnknownServices)
	}
}

// TestGenuinelyInventedNameStillFlagged — kalkan ZAYIFLAMAMALI.
//
// Bu testin ikinci yönü birincisinden önemli: yanlış alarmı susturmanın
// kolay yolu kalkanı büsbütün kapatmaktır ve o hâlde hiçbir test
// kırılmazdı. Gerçekten uydurulmuş bir ad HÂLÂ yakalanmalı.
func TestGenuinelyInventedNameStillFlagged(t *testing.T) {
	a := &serviceAnalysis{
		Ozet:     "Sorun büyük ihtimalle legacy-billing-adapter servisinde.",
		Kanit:    []string{},
		Oneriler: []string{},
	}
	pc := postCheckServiceAnalysis(a, shownCtx())
	if pc.Verified {
		t.Fatal("hiçbir yerde geçmeyen ad yakalanmadı — yanlış alarmı düzeltirken " +
			"kalkanı büsbütün kapatmışız")
	}
	if !strings.Contains(strings.Join(pc.UnknownServices, ","), "legacy-billing-adapter") {
		t.Errorf("yakalanan ad beklenen değil: %v", pc.UnknownServices)
	}
}

// TestAddShownTokensIsTokenLevel — yardımcının sözleşmesi.
func TestAddShownTokensIsTokenLevel(t *testing.T) {
	known := map[string]bool{}
	addShownTokens(known, "CHANNEL_CODE internet-banking / mobile-app")
	for _, want := range []string{"internet-banking", "mobile-app"} {
		if !known[want] {
			t.Errorf("%q jetonu eklenmedi — tarayıcı jeton düzeyinde eşleşiyor, "+
				"ham metni tek anahtar olarak eklemek yetmez", want)
		}
	}
	if len(known) == 0 {
		t.Fatal("hiçbir şey eklenmedi")
	}
}
