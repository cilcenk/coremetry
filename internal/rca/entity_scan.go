// v0.9.559 — paylaşılan varlık-adı tarayıcısı.
//
// Bir LLM yüzeyinin ürettiği SERBEST METİN, şema doğrulamasından
// geçse bile uydurma bir servis adı taşıyabilir: şema "string" der,
// içeriğe bakmaz. Enum'la kısıtlanan alanlar (root_cause.entity gibi)
// güvenlidir; ama özet, nedensellik zinciri ve önerilen aksiyon
// serbest metindir ve operatör asıl onları okur.
//
// Somut kaçış: model `root_cause.entity` alanına beyaz listeden geçen
// gerçek bir servis yazar, sonra özette ve remediation.action'da HİÇ
// var olmayan bir servisi anlatır. Ekranda gerçek görünen bir
// nedensellik zinciri ve somut bir "şunu yeniden başlat" aksiyonu
// belirir. Enum kalkanı bunu görmez.
//
// Tarayıcı YENİDEN YAZILMADI: copilot_aianalyze.go'daki
// postCheckServiceAnalysis bunu 2026'dan beri yapıyor. Buraya taşındı
// ki iki yüzey aynı kuralı paylaşsın — ikinci bir kopya yazmak,
// birinin diğerinden sessizce ayrışması demekti (bu oturumda tam bu
// sınıftan üç hata çıktı: v0.9.552/553/554).
package rca

import (
	"regexp"
	"strings"
)

// serviceTokenRe matches hyphenated lowercase service-style identifiers
// (payment-service, openbanking-gateway, mobile-bff). ascii-only, so Turkish
// words with diacritics (kök-neden) never match.
var serviceTokenRe = regexp.MustCompile(`[a-z][a-z0-9]+(?:-[a-z0-9]+)+`)

// nonServiceHyphenated are common hyphenated technical terms that look like a
// service token but aren't — excluded from the hallucination check.
var nonServiceHyphenated = map[string]bool{
	"error-rate": true, "error-count": true, "request-rate": true, "response-time": true,
	"root-cause": true, "status-code": true, "time-range": true, "real-time": true,
	"end-to-end": true, "two-phase": true, "p99-latency": true, "p50-latency": true,
	"baseline-window": true, "trace-id": true, "span-id": true,
}

// ScanUnknownEntities — verilen metinlerde, bilinen küme DIŞINDA kalan
// servis-biçimli adları döndürür. Sıra: ilk görülme (deterministik).
//
// `known` anahtarları küçük harfe indirgenmiş olmalı; çağıran
// LowerKnownSet ile kurar.
func ScanUnknownEntities(known map[string]bool, texts ...string) []string {
	seen := map[string]bool{}
	var unknown []string
	for _, text := range texts {
		for _, m := range serviceTokenRe.FindAllString(strings.ToLower(text), -1) {
			if known[m] || seen[m] || nonServiceHyphenated[m] {
				continue
			}
			seen[m] = true
			unknown = append(unknown, m)
		}
	}
	return unknown
}

// LowerKnownSet — bilinen adlardan küçük-harfli küme. Boş değerler
// atlanır: boş bir anahtar her metni "bilinen" yapmaz ama kümeyi
// gereksiz kirletir.
func LowerKnownSet(names ...string) map[string]bool {
	known := map[string]bool{}
	for _, n := range names {
		if n = strings.TrimSpace(n); n != "" {
			known[strings.ToLower(n)] = true
		}
	}
	return known
}

// AddShownTokens — modele GÖSTERİLEN bir metindeki servis-biçimli
// jetonları bilinen kümeye ekler (v0.9.598).
//
// İlke basit ve kalkanın kendi tanımından çıkıyor: kalkan "çıktıda
// olup GİRDİDE olmayan" adı yakalar. Öyleyse girdide GEÇEN her jeton,
// tanımı gereği bilinendir — nerede geçtiği (servis adı mı, bir
// CHANNEL_CODE değeri mi, bir hata mesajının içi mi) fark etmez.
//
// Neden gerekliydi: v0.9.580 snapshot'a iş-boyutu kırılımı
// (CHANNEL_CODE/FUNCTION_CODE) ve örnek korelasyon kimlikleri basmaya
// başladı ama bilinen küme güncellenmedi. Model kendisine VERDİĞİMİZ
// bir değeri alıntıladığında ("internet-banking" gibi tireli-küçük
// harfe inen her değer) kalkan onu uydurma sanıyordu: halüsinasyon
// koruması yanlış alarm üretip DOĞRU cevabı şüpheli gösteriyordu.
//
// Bu, kalkanı zayıflatmaz. Aynı tarayıcıyla aynı metinden çıkarılan
// jetonlar ekleniyor, yani "gösterildi mi" sorusuna tam olarak
// tarayıcının sorduğu biçimde cevap veriliyor. Ham metni tek anahtar
// olarak eklemek YETMEZDİ: tarayıcı jeton düzeyinde eşleşiyor ve bir
// request_id'nin ("8f3c-4a2b") içinden ayrı bir jeton çıkarabiliyor.
func AddShownTokens(known map[string]bool, texts ...string) {
	for _, text := range texts {
		low := strings.ToLower(text)
		if low != "" {
			known[low] = true
		}
		for _, m := range serviceTokenRe.FindAllString(low, -1) {
			known[m] = true
		}
	}
}
