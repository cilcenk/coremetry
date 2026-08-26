package api

import (
	"github.com/cilcenk/coremetry/internal/copilot"
)

// chat_overflow.go — bağlam taşmasının sohbet yolunda TEŞHİSİ
// (v0.10.26, Copilot denetimi bulgusu).
//
// ── KUSUR ───────────────────────────────────────────────────────────────
//
// `isContextOverflowErr` YAZILMIŞ ve tablo-testliydi — ama tek çağrı yeri
// `copilot_code.go`. Sohbet yolu hatayı sınıflandırmadan olduğu gibi
// SSE'ye basıyordu, yani operatör şunu görüyordu:
//
//	openai-compat 400: This model's maximum context length is 8192 tokens…
//
// Ne olduğu (geçmiş mi, tool sonucu mu, kanıt mı taştı) söylenmiyor,
// otomatik küçültüp yeniden deneme yok. Oysa kod-explain yolunda TAM
// OLARAK bu var: bloğu yarıya indirip bir kez retry.
//
// Operatörün tek çaresi sohbeti sıfırdan açmaktı — yani kaybı kendisi
// keşfetmek zorundaydı.
//
// ── NEDEN GEÇMİŞ KÜÇÜLTÜLÜYOR ───────────────────────────────────────────
//
// İstek başında `assemble.ClampHistory` bir kez koşuyor; sonra döngü her
// turda iki mesaj daha ekliyor (asistanın tool-call turu + ToolResults
// turu) ve BİLEREK yeniden bütçelenmiyor. Tool sonucu tavanı sonuç
// BAŞINA 6000 rune ve tur başına çağrı sayısı sınırsız — yani taşmanın
// en olası kaynağı birikmiş tool turları.
//
// Küçültme bu yüzden EN ESKİDEN başlıyor: son kullanıcı sorusu ve ona en
// yakın kanıt korunuyor, eski turlar düşüyor. Tersi (yeniyi atmak)
// modelin cevaplayacağı soruyu silmek olurdu.

// shrinkConvForRetry — konuşmayı yarıya indirir.
//
// `cc.Halved()` emsalinin sohbet karşılığı. İkinci dönüş false ise
// küçültecek bir şey KALMADI ve çağıran yeniden denememeli — aksi hâlde
// aynı taşmayı bir çağrı daha yakarak tekrarlar (kod-explain'in
// `hb == block` kontrolüyle aynı gerekçe).
func shrinkConvForRetry(conv []copilot.ChatMessage) ([]copilot.ChatMessage, bool) {
	// Tek mesaj kaldıysa küçültmek onu silmek demek: cevaplanacak soru
	// ortadan kalkar.
	//
	// ⚠ Bu muhafız ile aşağıdaki `keep >= len(conv)` BİRBİRİNİ GÖLGELİYOR:
	// mutasyon denetiminde yalnız bunu bozmak testi kırmızıya çevirmedi,
	// çünkü diğeri aynı sonucu veriyor. Isırmayan mutasyon ölü dal demek
	// DEĞİL (hafıza: örtüşen muhafızlar mutasyonu gölgeler); ikisi de
	// bilerek duruyor, çünkü ikisi FARKLI şeyi anlatıyor — biri "silinecek
	// soru kalmasın", diğeri "küçültme gerçekten küçültsün".
	if len(conv) <= 1 {
		return conv, false
	}
	keep := len(conv) / 2
	if keep < 1 {
		keep = 1
	}
	if keep >= len(conv) {
		return conv, false
	}
	// EN YENİLER korunuyor: son kullanıcı sorusu ve ona en yakın kanıt.
	// Baştan atmak, modelin cevaplayacağı soruyu silmek olurdu.
	start := len(conv) - keep

	// ⚠ KESİM TOOL ÇİFTİNİ BÖLEMEZ (v0.10.52).
	//
	// Tool sonuçları taşıyan bir mesaj, sağlayıcıya her sonuç için ayrı bir
	// `{"role":"tool","tool_call_id":…}` mesajı olarak gidiyor
	// (provider/tools.go). OpenAI uyumlu sunucular böyle bir mesajı, aynı
	// id'yi taşıyan asistan `tool_calls` mesajı ÖNCESİNDE yoksa REDDEDİYOR.
	//
	// Ham `conv[len-keep:]` kesimi bu çifti kabaca yarı yarıya bölüyordu:
	// korunan ilk mesaj bir tool-sonucu olduğunda yeniden deneme 400 ile
	// ölüyor ve operatör, taşmayı dürüstçe anlatan cümle yerine ham bir
	// sağlayıcı hatası görüyordu — yani v0.10.26'nın kurtarma yolu tam da
	// ihtiyaç duyulduğu anda yarı yarıya çalışmıyordu.
	//
	// Ters yön sorun DEĞİL: asistan `tool_calls` mesajı korunursa sonuçları
	// zaten ARDINDAN geliyor (sonek kesiyoruz), yani öksüz kalmıyor.
	for start < len(conv) && len(conv[start].ToolResults) > 0 {
		start++
	}
	// İlerletme her şeyi yediyse küçültecek anlamlı bir şey kalmamıştır.
	if start >= len(conv) {
		return conv, false
	}
	return conv[start:], true
}

// chatOverflowMessageTR — taşma operatöre nasıl anlatılıyor.
//
// Ham sağlayıcı metni ("This model's maximum context length is 8192
// tokens") üç sebeple yetmez: İngilizce, hangi parçanın taştığını
// söylemiyor, ve operatöre NE YAPACAĞINI söylemiyor. Küçültme zaten
// denendiyse bunu da söylüyoruz — aksi hâlde operatör aynı soruyu tekrar
// sorar ve aynı duvara çarpar.
func chatOverflowMessageTR(retried bool) string {
	base := "Bu konuşma modelin bağlam penceresine sığmadı. " +
		"En olası sebep birikmiş araç sonuçları ve uzun sohbet geçmişi."
	if retried {
		return base + " Geçmiş yarıya indirilip bir kez daha denendi, yine sığmadı — " +
			"yeni bir sohbet açıp soruyu tek bir servise ya da daha dar bir " +
			"zaman aralığına daraltın."
	}
	return base + " Yeni bir sohbet açıp soruyu daraltın."
}
