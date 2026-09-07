// Package modelcaps — v0.10.534 (CoSRE v2 Faz 2.1b): MODEL-FARKINDA yetenek
// tablosu. Yaprak paket (bağımlılık yok); copilot ve provider buradan okur.
//
// Neden var: Qwen3 olayı — düşünme fazı sabit max_tokens'ı yiyip boş içerik
// döndürdü; yama 4096 completion tavanı + salvage zinciriydi (semptom), ama
// "bu model düşünür, düşünmesi nasıl kapatılır, temperature kabul eder mi"
// bilgisi hiçbir yerde yoktu. Burada tek tabloda: model adı desenine göre
// aile + düşünme anahtarı. Varsayılanlar BUGÜNKÜ davranışı korur (bütçe ve
// temperature değişmez); yalnız operatör profilde `thinking: off|on` derse
// gövdeye ailenin anahtarı iner.
package modelcaps

import "strings"

// Family — model ailesi (ad desenine göre, küçük harf `contains`).
type Family string

const (
	FamilyQwen3      Family = "qwen3"
	FamilyQwQ        Family = "qwq"
	FamilyDeepSeekR1 Family = "deepseek-r1"
	FamilyGemma      Family = "gemma"
	FamilyLlama      Family = "llama"
	FamilyMistral    Family = "mistral"
	FamilyGPT        Family = "gpt"
	FamilyOSeries    Family = "o-series"
	FamilyClaude     Family = "claude"
	FamilyUnknown    Family = "unknown"
)

// Thinking — profil ayarı: "" (dokunma) | "off" | "on".
type Thinking string

const (
	ThinkingDefault Thinking = ""
	ThinkingOff     Thinking = "off"
	ThinkingOn      Thinking = "on"
)

func ValidThinking(s string) bool {
	switch Thinking(s) {
	case ThinkingDefault, ThinkingOff, ThinkingOn:
		return true
	}
	return false
}

// SwitchChatTemplate — vLLM / SGLang / Ollama'nın Qwen3 şablon anahtarı:
// {"chat_template_kwargs": {"enable_thinking": bool}} (openai-uyumlu gövde).
const SwitchChatTemplate = "chat_template_kwargs"

// Caps — bir modelin bilinen yetenekleri.
type Caps struct {
	Family Family
	// Reasoning — cevaptan önce düşünme fazı üretir (salvage zinciri ilgili;
	// küçük completion bütçesinde boş cevap riski).
	Reasoning bool
	// ThinkingSwitch — düşünmeyi açıp kapatan gövde anahtarı; "" = bilinmiyor
	// (operatör "off" dese de gövdeye bir şey inmez, çağıran loglar).
	ThinkingSwitch string
}

// For — model adından yetenek. Bilinmeyen ad: Unknown, Reasoning=false.
func For(model string) Caps {
	m := strings.ToLower(strings.TrimSpace(model))
	switch {
	case m == "":
		return Caps{Family: FamilyUnknown}
	case strings.Contains(m, "qwen3"):
		return Caps{Family: FamilyQwen3, Reasoning: true, ThinkingSwitch: SwitchChatTemplate}
	case strings.Contains(m, "qwq"):
		return Caps{Family: FamilyQwQ, Reasoning: true}
	case strings.Contains(m, "deepseek-r1") || strings.Contains(m, "deepseek_r1") || strings.Contains(m, "deepseek-reasoner"):
		return Caps{Family: FamilyDeepSeekR1, Reasoning: true}
	case strings.Contains(m, "gemma"):
		return Caps{Family: FamilyGemma}
	case strings.Contains(m, "llama"):
		return Caps{Family: FamilyLlama}
	case strings.Contains(m, "mistral") || strings.Contains(m, "mixtral"):
		return Caps{Family: FamilyMistral}
	case strings.HasPrefix(m, "o1") || strings.HasPrefix(m, "o3") || strings.HasPrefix(m, "o4"):
		return Caps{Family: FamilyOSeries, Reasoning: true}
	case strings.HasPrefix(m, "gpt-5"):
		return Caps{Family: FamilyGPT, Reasoning: true}
	case strings.Contains(m, "gpt"):
		return Caps{Family: FamilyGPT}
	case strings.Contains(m, "claude"):
		return Caps{Family: FamilyClaude}
	}
	return Caps{Family: FamilyUnknown}
}

// ExtraBody — openai-uyumlu gövdeye eklenecek alanlar. ok=false: ayar var
// ama aile anahtarı bilinmiyor (çağıran uyarır); ThinkingDefault'ta (nil,
// true) — gövde değişmez.
func ExtraBody(c Caps, th Thinking) (map[string]any, bool) {
	if th == ThinkingDefault {
		return nil, true
	}
	switch c.ThinkingSwitch {
	case SwitchChatTemplate:
		return map[string]any{SwitchChatTemplate: map[string]any{"enable_thinking": th == ThinkingOn}}, true
	}
	return nil, false
}
