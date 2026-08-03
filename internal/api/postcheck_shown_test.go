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

// ── Few-shot ↔ snapshot hizası (v0.9.599) ────────────────────────────

// TestFewShotMatchesTheRealSnapshotShape — örnek GİRDİ, gerçek
// snapshot'ın ürettiği satır türlerini içermeli.
//
// v0.9.580 snapshot'a iki yeni satır türü ekledi (kırılım + örnek
// kimlik) ama few-shot örneği güncellenmedi. Küçük lokal model
// gösterilen örneği taklit eder: örnekte o satırlar yoksa cevapta da
// olmaz. Veri prompt'a gidiyor, cevaba gelmiyordu — operatörün açıkça
// istediği özellik sessizce yarım çalışıyordu.
//
// Ne derleme, ne şema, ne mevcut testler bunu yakalayabilirdi: içerik
// serbest metin alanlarının İÇİNDE yaşıyor.
func TestFewShotMatchesTheRealSnapshotShape(t *testing.T) {
	// Beklenen biçimler ELLE YAZILMIYOR: gerçek renderer'dan
	// türetiliyor. Elle yazsaydık aynı cümle iki yerde yaşardı ve
	// renderer değiştiğinde test yeşil kalıp few-shot bayatlardı —
	// bu oturumun tekrar eden hata sınıfı ("iki yer, iki kural").
	rendered := renderServiceSnapshot(shownCtx())

	var shapes []string
	for _, line := range strings.Split(rendered, "\n") {
		// Yalnız v0.9.580'in eklediği iki satır türü ilgilendiriyor.
		if i := strings.Index(line, "kırılımı"); i >= 0 {
			if j := strings.Index(line, ":"); j > i {
				shapes = append(shapes, line[i:j+1]) // "kırılımı (…):"
			}
		}
		if strings.HasPrefix(line, "Örnek ") {
			if j := strings.Index(line, ":"); j > 0 {
				shapes = append(shapes, line[:j+1]) // "Örnek request_id:"
			}
		}
	}
	if len(shapes) != 2 {
		t.Fatalf("renderer'dan 2 satır türü bekleniyordu, %d bulundu:\n%s\n\n"+
			"Snapshot biçimi değişmiş olabilir — o hâlde few-shot da gözden "+
			"geçirilmeli (testin bayatlaması bunu haber vermek için).",
			len(shapes), rendered)
	}
	for _, shape := range shapes {
		if !strings.Contains(serviceAnalysisPrompt, shape) {
			t.Errorf("few-shot ÖRNEK GİRDİ'si %q satır türünü içermiyor.\n\n"+
				"Küçük model gösterilen örneği taklit eder; örnekte olmayan "+
				"satır türü cevaba da yansımaz. Veri prompt'a gider, cevaba "+
				"gelmez — özellik sessizce yarım çalışır.", shape)
		}
	}
}

// TestFewShotOutputCarriesConcreteIdentifiers — örnek ÇIKTI da
// göstermeli. Girdide olup çıktıda olmayan bir sinyal, modele
// "bunu kullanma" demenin örtük yoludur.
func TestFewShotOutputCarriesConcreteIdentifiers(t *testing.T) {
	i := strings.Index(serviceAnalysisPrompt, "ÖRNEK ÇIKTI:")
	if i < 0 {
		t.Fatal("ÖRNEK ÇIKTI bölümü bulunamadı — test bayatladı")
	}
	out := serviceAnalysisPrompt[i:]
	for _, want := range []string{"mobile-app", "request_id"} {
		if !strings.Contains(out, want) {
			t.Errorf("örnek ÇIKTI %q taşımıyor — girdide gösterilip çıktıda "+
				"kullanılmayan sinyal, modele örtük olarak 'bunu kullanma' der",
				want)
		}
	}
}

// TestPromptAsksForConcreteIdentifiers — kural da yazılı olmalı.
// Yalnız örnekle öğretmek zayıf: örnek tek bir vakayı gösterir,
// KURALLAR bloğu her vakayı bağlar.
func TestPromptAsksForConcreteIdentifiers(t *testing.T) {
	i := strings.Index(serviceAnalysisPrompt, "ÖRNEK GİRDİ:")
	if i < 0 {
		t.Fatal("ÖRNEK GİRDİ bulunamadı — test bayatladı")
	}
	rules := serviceAnalysisPrompt[:i]
	for _, want := range []string{"KIRILIM", "AYNEN geçir", "UYDURMA"} {
		if !strings.Contains(rules, want) {
			t.Errorf("KURALLAR bloğunda %q yok — yalnız örnekle öğretmek zayıf, "+
				"örnek tek vakayı gösterir, kural her vakayı bağlar", want)
		}
	}
}
