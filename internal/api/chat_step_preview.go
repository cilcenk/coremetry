package api

// v0.9.1181 (AI Faz 4.3) — sohbetteki tool sonucunun DENETLENEBİLİR hâli.
//
// Bugüne dek ⚙ çipi yalnız tool ADINI gösteriyordu. Model "topolojiye
// baktım, şu servis suçlu" dediğinde operatörün elinde o iddiayı
// sınayacak hiçbir şey yoktu: veriyi gördü mü, ne gördü, kırpıldı mı —
// hepsi görünmezdi. Bir APM'de "modele güven" kabul edilebilir bir cevap
// değil; kanıt zinciri görünür olmalı.
//
// Bu dosya o kanıtın TEL ÜZERİNDEKİ sınırını tanımlar. İki şey aynı anda
// doğru olmalı:
//   - önizleme model ne gördüyse ONUN başlangıcı olmalı (uydurulmuş bir
//     özet değil — özetlenmiş kanıt, kanıt değildir),
//   - ve sohbet akışını şişirmemeli; tool çıktıları megabaytlara çıkabilir.
//
// Bu yüzden kırpma var ve kırpma İLAN EDİLİYOR. Sessiz kırpma en kötü
// seçenek olurdu: operatör eksik veriye tam veri sanıp bakar.

import "unicode/utf8"

// chatStepPreviewMax — tel üzerindeki tool çıktısı tavanı (bayt).
//
// 4 KB, "bir tool cevabının şeklini görmeye yeter, akışı boğmaya yetmez"
// noktası: tipik bir list_operations / get_blast_radius cevabı bunun
// altında tamamen sığar, get_topology gibi geniş olanlar kırpılır ve
// kırpıldığını söyler. Sohbetin kendisi kalıcı değil (turlar yalnız
// {role,text} olarak arşivleniyor, chatPersist.ts), yani bu bayt bütçesi
// yalnız canlı akışa binen bir maliyet.
const chatStepPreviewMax = 4096

// clipStepPreview — çıktıyı tavana kırpar ve kırpılıp kırpılmadığını
// söyler. SAF, tablo testli.
//
// Kırpma UTF-8 SINIRINDA yapılır: ham bayt kesimi çok baytlı bir runeyi
// ortadan ikiye böler ve JSON kodlayıcı onu U+FFFD'ye çevirir. Türkçe
// servis/operasyon adları bu yolda sürekli geçiyor, yani bu teorik bir
// incelik değil — "ödeme-servisi" bir baytından kesilirse operatör
// kanıtta bozuk karakter görür ve haklı olarak veriye güvenmez.
func clipStepPreview(s string) (preview string, truncated bool) {
	if len(s) <= chatStepPreviewMax {
		return s, false
	}
	cut := chatStepPreviewMax
	// Geriye doğru en yakın rune sınırına in.
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut], true
}
