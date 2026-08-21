package api

// v0.9.1230 (AI perf) — tool SONUCUNUN model tarafındaki bütçesi.
//
// chat_step_preview.go'daki 4 KB kırpma YALNIZ TELE binen önizlemeyi
// (⚙ çipinin "veriyi göster" bloğu) sınırlar; modelin GÖRDÜĞÜ metin
// bugüne dek TAMAMEN sınırsızdı: copilot_chat.go tool çıktısının ham
// JSON'unu olduğu gibi ToolResult.Content'e koyuyor, sağlayıcı da onu
// aynen gönderiyordu. mcptools'taki klamplar SATIR sayısına bakar
// (limit ≤ 500), BOYUTA değil — 500 tam RED satırı ya da 200 span'lik
// bir trace, geçmişin TAMAMI için ayrılmış 6000 rune'luk bütçeyi
// (assemble.HistoryMaxRunes) tek başına 10-20 kat aşabiliyordu. Üstelik
// konuşma her turda BÜTÜN olarak yeniden gönderiliyor, yani tek bir
// şişkin sonucun bedeli kalan turların hepsinde yeniden ödeniyordu.
//
// Küçük model (gemma4, hava-boşluklu, Türkçe) için bu, "schema soup"un
// kardeşi: taze kanıt, eski kanıt tarafından bağlamdan atılır.
//
// KIRPMA SÖYLENİR. Sessiz kırpma yasak sınıf (assemble.HistoryTrimNote,
// drawerEvidenceTruncNote, clipStepPreview'ün truncated bayrağı — hepsi
// aynı doktrin): kırpıldığını bilmeyen model, elindeki listeyi TAM
// sanıp "toplam 12 servis var" gibi sayım cümleleri kurar ve operatör
// bunu veri sanır. Not, modele hem eksikliği hem de ÇARESİNİ söyler
// (daha dar limit/filtre ile tekrar çağır).

import (
	"fmt"
	"unicode/utf8"
)

// chatToolResultMaxRunes — modele giden tek bir tool sonucunun tavanı
// (rune, bayt değil — Türkçe içerik bu yolda sürekli geçiyor).
//
// 6000: bilinçli olarak assemble.HistoryMaxRunes ve
// drawerEvidenceMaxRunes ile AYNI sayı. Oran kuralı tek cümle — "tek
// bir kanıt parçası, geçmişin tamamına ayrılan yerden fazlasını
// kaplayamaz". Pratikte tipik bir list_services / get_service_health /
// get_topology cevabı bunun altında TAMAMEN sığar; sığmayan şey zaten
// modelin okuyamayacağı kadar geniş bir listedir ve doğru cevabı onu
// daraltmaktır, göndermek değil.
const chatToolResultMaxRunes = 6000

// chatToolResultTruncNote — kırpmanın modele söylenen hâli. %d'ler:
// gösterilen rune sayısı, atlanan rune sayısı.
const chatToolResultTruncNote = "\n\n[kırpıldı: sonucun ilk %d karakteri verildi, %d karakter atlandı. " +
	"Eksik satırlara dayanarak sayım yapma; gerekiyorsa daha dar bir limit/filtre ile tekrar çağır.]"

// clampToolResultForModel — tool sonucunu model bütçesine indirir ve
// kırpıldıysa bunu İÇERİĞİN İÇİNDE söyler.
//
// Kesim RUNE sınırında yapılır. Bayt dilimleme çok baytlı bir runeyi
// ortadan böler ve JSON kodlayıcı onu U+FFFD'ye çevirir; kanıtın
// içinde bozuk karakter gören model (ve operatör) veriye haklı olarak
// güvenmez — clipStepPreview'ün v0.9.1181'de aynı gerekçeyle rune
// sınırına inmesinin sebebi bu.
//
// SAF, tablo-testli.
func clampToolResultForModel(s string) (out string, truncated bool) {
	n := utf8.RuneCountInString(s)
	if n <= chatToolResultMaxRunes {
		return s, false
	}
	r := []rune(s)
	kept := chatToolResultMaxRunes
	return string(r[:kept]) + sprintfTruncNote(kept, n-kept), true
}

// sprintfTruncNote — notu üretir. Ayrı fonksiyon, çünkü test notu AD
// ile (biçim dizesini elle kurmadan) üretip arayabilsin.
func sprintfTruncNote(kept, dropped int) string {
	return fmt.Sprintf(chatToolResultTruncNote, kept, dropped)
}
