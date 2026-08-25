package api

import "strings"

// rag_declines.go — RAG cevaplayamadığı soruyu BIRAKMALI (v0.10.14).
//
// Operatör sordu: "guided intent uymasa da normal chat gibi
// konuşulamaz mı kullanıcılar LLM ile?" Cevap: EVET, ve o yol zaten
// var — `copilot_chat.go`'nun sırası şu:
//
//     guided (canlı telemetri) > çekmece bağlamı > DOKÜMANLAR > serbest döngü
//
// Serbest tool döngüsü tam da "normal sohbet"; sorun onun yokluğu değil,
// SIRA ALAMAMASI. `ragChatAnswer` model cevabından sonra KOŞULSUZ
// `handled=true` dönüyordu — cevap "Yüklü dokümanlarda bu bilgi yok."
// olsa bile. Yani RAG, cevaplayamadığı soruyu da sahipleniyor ve altındaki
// döngü hiç çalışmıyordu.
//
// Belirti üç kez ayrı ayrı bildirildi ve her seferinde TEK BİR VAKA
// yamandı (v0.9.537 trace ID · v0.9.1142 kuyruk birikmesi · v0.10.13
// "sen hangi modelsin"). Üçü de aynı deseni gösteriyordu; asıl kusur
// intent listesinin eksikliği DEĞİL, reddedilen cevabın son söz
// sayılmasıydı.
//
// Bu dosya o sözü geri alıyor: RAG "bilmiyorum" diyorsa soru
// SAHİPLENİLMEZ ve serbest döngü sırasını alır.

// ragDeclineMarkers — prompt'un modele söylettiği reddetme cümlesi ve
// yakın varyantları.
//
// Prompt (systemRAGChat) şunu emrediyor: cevap bağlamda yoksa "Yüklü
// dokümanlarda bu bilgi yok." de. Model bunu birebir ya da ufak
// sapmalarla üretiyor, o yüzden eşleşme NORMALLEŞTİRİLMİŞ ve alt-dize.
//
// Liste DAR tutuldu: "bilmiyorum" gibi genel ifadeler BURAYA GİRMEZ.
// Model bir doküman cevabını "kesin bilmiyorum ama §2'ye göre…" diye
// başlatabilir ve o GEÇERLİ bir cevaptır; onu reddetme sayıp serbest
// döngüye atmak, iyi bir cevabı çöpe atmak olurdu.
var ragDeclineMarkers = []string{
	"dokumanlarda bu bilgi yok",
	"dokümanlarda bu bilgi yok",
	"yuklu dokumanlarda bu bilgi",
	"yüklü dokümanlarda bu bilgi",
}

// ragDeclined — model "bu bağlamda cevap yok" mu dedi?
//
// Saf ve tablo-testli: bu yüklem bir cevabın operatöre GÖSTERİLİP
// gösterilmeyeceğine karar veriyor. Yanlış pozitif geçerli bir doküman
// cevabını atar, yanlış negatif ölü cevabı geri getirir — iki yön de
// pahalı.
func ragDeclined(answer string) bool {
	a := strings.ToLower(strings.TrimSpace(answer))
	if a == "" {
		// Boş cevap da bir reddetmedir: gösterilecek bir şey yok, ve
		// serbest döngü hiç değilse deneyebilir.
		return true
	}
	for _, m := range ragDeclineMarkers {
		if strings.Contains(a, m) {
			return true
		}
	}
	return false
}
