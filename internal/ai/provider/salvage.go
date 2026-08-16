package provider

import (
	"errors"
	"strings"
)

// salvage.go — "model boş içerik döndürdü" kurtarma zinciri.
//
// FAZ 1.2'den beri TEK YAZILIŞ. Faz 1.1'de burası copilot.go'daki
// ikizin bilinçli geçici kopyasıydı; denetim (2026-08-16) zincirin ÜÇ
// kopyasını saymıştı (copilot.go buffered, stream.go SSE, bu paket).
// Faz 1.2'de copilot.go'daki üreticiler silindi ve stream.go/chat.go
// buradaki fonksiyonları çağırıyor.
//
// Kopya sayısı bir YAPISAL kapıyla korunuyor:
// salvage_singleton_test.go. Davranış testi ikinci bir kopyayı
// yakalayamaz — iki kopya bugün aynı şeyi yapar, sonra biri düzeltilir
// öteki unutulur (1024 bütçesi tam böyle ~1000 sürüm yaşadı).
//
// Zincirin geçmişi (v0.8.138 → 155 → 384): yerel reasoning modelleri
// (Qwen3, deepseek-r1, …) cevabı content'e koymayabilir —
// reasoning_content'e, reasoning'e ya da yalnız <think>…</think>
// içine koyar; bütçe düşükse hiç cevap üretmeden "length" ile biter.
// Bunların hepsi kullanıcıya BOŞ panel olarak yansıyordu.

// StripThinking, bazı yerel reasoning modellerinin content'e gömdüğü
// <think>…</think> bloğunu atar; SON </think> sonrasını döndürür.
// Blok yoksa yalnız trim eder.
func StripThinking(s string) string {
	if i := strings.LastIndex(s, "</think>"); i != -1 {
		s = s[i+len("</think>"):]
	}
	return strings.TrimSpace(s)
}

// ThinkingContent, İLK <think>…</think> bloğunun İÇİNİ döndürür —
// StripThinking hiçbir şey bırakmadığında son çare. Bazı modeller
// yalnız düşünce bloğu üretir ve o düşünce genelde açıklamanın
// kendisidir; kurtarmak, isteği başarısız saymaktan iyidir.
func ThinkingContent(s string) string {
	open := strings.Index(s, "<think>")
	if open == -1 {
		return ""
	}
	rest := s[open+len("<think>"):]
	if c := strings.Index(rest, "</think>"); c != -1 {
		rest = rest[:c]
	}
	return strings.TrimSpace(rest)
}

// SalvageAnswer — cevabı modelin koyduğu yerden çeker, öncelik
// sırasıyla:
//
//  1. son </think> sonrasındaki content (normal hâl),
//  2. ayrılmış reasoning alanı (reasoning_content → reasoning),
//  3. son çare: <think> bloğunun İÇİ.
//
// Hiçbiri yoksa boş döner — çağıran EmptyAnswerError ile teşhis eder.
func SalvageAnswer(content, reasoningContent, reasoning string) string {
	if out := StripThinking(content); out != "" {
		return out
	}
	if out := StripThinking(reasoningContent); out != "" {
		return out
	}
	if out := StripThinking(reasoning); out != "" {
		return out
	}
	return ThinkingContent(content)
}

// EmptyAnswerError — kurtarılacak hiçbir şey kalmadığında operatöre
// EYLEME DÖNÜK hata. finish_reason="length" ayrı bir teşhistir:
// bütçe düşünme fazında tükenmiştir, çözüm max_tokens'ı yükseltmek
// ya da düşünmeyi kapatmaktır.
//
// Metinler copilot.go'daki ikizleriyle BİREBİR aynı tutuluyor —
// canary sırasında aynı yüzey iki yoldan da aynı cümleyi
// döndürmeli (parity testi bunu pinler).
func EmptyAnswerError(finishReason string) error {
	if finishReason == "length" {
		return errors.New("model returned no answer — token budget exhausted by reasoning; raise max_tokens or disable thinking (e.g. Qwen3 /no_think)")
	}
	return errors.New("openai-compat: model returned empty content — no answer in content/reasoning. Check the model name + endpoint; a reasoning model may need /no_think. See the [copilot] pod log for the raw response")
}
