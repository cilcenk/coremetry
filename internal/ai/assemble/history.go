// Package assemble — prompt bağlamının DETERMİNİSTİK kurulumu
// (K3 / AI Faz 4.5, v0.9.1187).
//
// Planın K3 kararı: bağlam bir öncelik merdiveniyle ve kademeli budamayla
// kurulur; her katman "≤N satır + [kırpıldı] işareti" kuralıyla girer.
// Seçenek (b) (LLM ile özetleyerek yuvarlanan bağlam) ve (c) (ham JSON'u
// modele bırak) elendi: biri küçük modelde özet kalitesi riski + ek çağrı
// maliyeti, diğeri guided router'ın VARLIK SEBEBİ olan başarısızlık modu.
//
// Bu dosya merdivenin EN ALT basamağı: konuşma geçmişi. Diğer katmanlar
// (hipotez kataloğu, DeepEvidence, korelasyon, KB chunk'ları) buraya
// taşındıkça paket büyür.
//
// Paket bilerek stdlib-only ve şekil-bağımsız: `copilot.ChatMessage` tipini
// içeri almak, saf bir bütçe hesabını transport şekline bağlardı (ve
// copilot → ai/provider bağımlılığı zaten var; ters yönü açmak döngü
// riskini davet eder). Çağıran dilimleme indeksini alır, kendi tipiyle
// keser.
package assemble

import (
	"strings"
	"unicode/utf8"
)

// HistoryMaxRunes — geçmişin rune bütçesi.
//
// 6000, çekmecenin KANIT bütçesiyle (drawerEvidenceMaxRunes) aynı sayı ve
// bu bilinçli bir oran: geçmiş, kanıttan DAHA fazla yer kaplamamalı. Bir
// sohbet turu prompt'ta üç şeyi birden taşıyor — sistem prompt'u, kanıt
// paketi ve geçmiş — ve küçük modelde en pahalı hata, taze kanıtın eski
// konuşmayla bağlamdan atılmasıdır. Eşitlik "geçmiş kanıtı ezemez"
// kuralının en sade hâli.
const HistoryMaxRunes = 6000

// HistoryTrimNote — modele söylenen kırpma işareti.
//
// Sessiz kırpma en pahalı seçenek olurdu: model, olmayan bir konuşmayı
// hatırlıyormuş gibi davranır ("az önce dediğin gibi…") ve operatör
// modelin uydurduğunu sanır. İşaret, "hatırlamıyorum" demenin dürüst yolu.
const HistoryTrimNote = "[Not: konuşmanın daha eski kısmı bağlam bütçesi nedeniyle kırpıldı. Kırpılan turlara atıfta bulunma; gerekiyorsa operatöre yeniden sor.]"

// ClampHistory — geçmişin KAÇ turunun prompt'a gireceği.
//
// EN YENİDEN geriye doğru yürür ve bütçe dolunca durur; döndürdüğü `keep`,
// çağıranın kuyruktan alacağı tur sayısıdır (`msgs[len(msgs)-keep:]`).
// `trimmed` düşen tur sayısı — sıfırdan büyükse çağıran HistoryTrimNote'u
// başa eklemeli.
//
// Sözleşmenin üç köşesi:
//
//   - EN AZ BİR tur her zaman kalır. Son tur aktif sorunun kendisi; bütçeye
//     tek başına sığmasa bile düşürmek, cevaplanacak soruyu silmek olurdu.
//     (Aşırı büyük tek bir turu KESMEK bu fonksiyonun işi değil — çağıran
//     kendi alan-özel klamplarını uygular; burada karar "kaç tur".)
//   - Sayı tavanı da korunur (maxTurns): rune bütçesi bol olsa bile 40
//     kısa turluk bir konuşma küçük modelde odak kaybettirir.
//   - Karar yalnız GİRDİYE bağlı: aynı konuşma her zaman aynı kesimi verir.
//     Rastgelelik ya da zaman bağımlılığı, "dün çalışıyordu" sınıfı hataların
//     kaynağıdır.
//
// SAF, tablo-testli.
func ClampHistory(runeLens []int, maxTurns, maxRunes int) (keep, trimmed int) {
	n := len(runeLens)
	if n == 0 {
		return 0, 0
	}
	if maxTurns <= 0 {
		maxTurns = n
	}
	if maxRunes <= 0 {
		maxRunes = HistoryMaxRunes
	}
	used := 0
	for keep < n && keep < maxTurns {
		ln := runeLens[n-1-keep]
		if ln < 0 {
			ln = 0
		}
		// İlk tur bütçeyi aşsa bile alınır (yukarıdaki "en az bir tur"
		// köşesi); sonrakiler aşarsa durulur.
		if keep > 0 && used+ln > maxRunes {
			break
		}
		used += ln
		keep++
	}
	return keep, n - keep
}

// RuneLens — metin dilimini ClampHistory'nin beklediği uzunluk dizisine
// çevirir. Ayrı fonksiyon, çünkü çağıranın tipi (copilot.ChatMessage,
// provider.ChatMessage…) burada bilinmiyor ve bilinmemeli.
func RuneLens(texts []string) []int {
	out := make([]int, len(texts))
	for i, t := range texts {
		out[i] = utf8.RuneCountInString(t)
	}
	return out
}

// TrimNoteIfNeeded — kırpma varsa işaret, yoksa boş. Çağıranın `if`
// yazmaması için; işareti unutmak sessiz kırpmaya geri dönmek demek.
func TrimNoteIfNeeded(trimmed int) string {
	if trimmed <= 0 {
		return ""
	}
	return HistoryTrimNote
}

// HasTrimNote — bir metnin kırpma işaretini taşıyıp taşımadığı. Testler ve
// çağıranın idempotenslik kontrolü için (aynı konuşma iki kez klamplanırsa
// ikinci bir işaret eklenmemeli).
func HasTrimNote(s string) bool {
	return strings.Contains(s, HistoryTrimNote)
}
