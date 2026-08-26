package api

import (
	"strings"
	"testing"
)

// chat_roundcap_parity_test.go — v0.10.61. İKİ ÇIKIŞ, TEK SÖZLEŞME.
//
// Serbest tool döngüsünün İKİ nihai-cevap yolu var:
//
//	(a) model artık araç çağırmıyor  → normal yol
//	(b) tur tavanına dayanıldı       → "elindekiyle cevapla" turu
//
// (b) yolu üç düzeltmeyi birden ATLIYORDU:
//   - v0.10.29 kaynak künyesi
//   - v0.10.24 alışveriş-tavanı cümlesi
//   - v0.10.26 bağlam-taşması cümlesi
//
// ⚠ Ve atladığı yer en kötü yerdi: tavana ancak tur tur ARAÇ ÇAĞIRARAK
// varılıyor, yani atıfın en anlamlı olduğu cevap tek atıfsız cevaptı.
// Hata dalında da operatör ham İngilizce sağlayıcı gövdesi görüyor ve
// `context deadline exceeded` metnini "model zaman aşımına uğradı" diye
// okuyup MODELİ suçluyordu.
//
// Kusur sınıfı bu depoda tekrar ediyor: bir sözleşme İKİ yerde yaşıyor ve
// biri sessizce geriliyor. Bu kapı ikisini SAYIYOR.

func TestBothAnswerPathsCarryTheSourceNote(t *testing.T) {
	src := readSourceFile(t, "copilot_chat.go")

	if n := strings.Count(src, "chatSourceNoteTR(calledTools)"); n != 2 {
		t.Errorf("künye %d yerde ekleniyor, 2 olmalı (normal yol + tur tavanı) — "+
			"eksik olan yol, EN ÇOK araç çağıran ve atıfın en anlamlı olduğu yoldur", n)
	}
}

func TestBothErrorPathsClassifyTheFailure(t *testing.T) {
	src := readSourceFile(t, "copilot_chat.go")

	for _, pair := range []struct {
		call, why string
	}{
		{"chatDeadlineMessageTR(exchangeMax)",
			"ham `context deadline exceeded` metni operatöre MODELİ suçlatır"},
		{"chatOverflowMessageTR(overflowRetried)",
			"ham sağlayıcı gövdesi İngilizce ve ne yapılacağını söylemiyor"},
	} {
		if n := strings.Count(src, pair.call); n != 2 {
			t.Errorf("%s %d yerde çağrılıyor, 2 olmalı (normal yol + tur tavanı) — %s",
				pair.call, n, pair.why)
		}
	}

	// Ham hata metni DOĞRUDAN yayınlanmamalı: sınıflandırmadan geçmeli.
	if strings.Contains(flatWS(src), `emit("error", map[string]string{"error": err2.Error()})`) {
		t.Error("tur-tavanı dalı ham sağlayıcı hatasını doğrudan yayınlıyor")
	}
}
