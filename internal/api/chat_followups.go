package api

// chat_followups.go — v0.10.479 (CoSRE Telemetry Agent Faz 4, F4-2; audit G10;
// Ek A bağlam kuralları): TAKİP MUTASYONLARI. Operatörün "son 1 saate
// genişlet", "sadece hatalı olanlar", "bunun pod'larını göster", "aynı
// filtreyle loglara bak" gibi cümleleri yeni bir soru DEĞİL, aktif çalışma
// kümesinin (chat_context.go) bir alanını değiştirip son cevabı yeniden
// üretme isteğidir. Router'dan ÖNCE, deterministik: cümle bir kipe oturuyor
// ve bağlamda yeniden üretilecek bir son rota (LastRoute) varsa, rota
// klonlanır, alan değiştirilir, aynı dispatch koşar; LLM yok.
//
// Kapı: mesajda açık servis/namespace adı YOKSA (adlı soru normal router'a
// gider), kip sözcükleri dışında başka sinyal yoksa, LastRoute varsa.

import (
	"strings"
)

type contextMutation struct {
	Kind   string // window | errors | pods | logs
	RangeS int64
	Label  string
}

var (
	windowVerbs = []string{"genişlet", "genislet", "daralt", "çıkar", "cikar", "bak", "al", "yap", "büyüt", "buyut", "küçült", "kucult", "değiştir", "degistir", "widen", "extend", "narrow", "set", "expand"}
	onlyWords   = []string{"sadece", "yalnız", "yalniz", "yalnızca", "yalnizca", "only", "just"}
	logWords    = []string{"log", "logs", "loglar", "loglara", "logları", "loglari", "logunda", "loglarında", "loglarina"}
	podWords    = []string{"pod", "pods", "podlar", "pod'lar", "podları", "podlari", "podlarını", "podlarini", "podlarına"}
)

// questionWords — v0.10.491 (Astra #8): bunlar varken "pod"/"log" sözcüğü bir
// KİP değil yeni bir sorudur ("pod restart sayısı nedir", "log seviyesi ne",
// "logları nasıl indiririm") — router'a bırakılır.
var questionWords = []string{"nedir", "ne", "neden", "niye", "nasıl", "nasil", "kaç", "kac", "sayısı", "sayisi", "seviyesi", "mi", "mı", "mu", "mü", "midir", "mıdır", "hangi", "kim", "what", "why", "how", "many", "level"}

// mutationVerbs — kip için beklenen fiil/işaret: göster/listele/bak/getir/aç
// ya da işaret zamiri (bunun/onun/aynı/şunun).
var mutationVerbs = []string{"göster", "goster", "listele", "bak", "getir", "aç", "ac", "ver", "çıkar", "cikar", "bunun", "onun", "aynı", "ayni", "şunun", "sunun", "show", "list", "open"}

func anyToken(toks []string, words ...string) bool {
	for _, t := range toks {
		for _, w := range words {
			if t == w {
				return true
			}
		}
	}
	return false
}

// detectContextMutation — SAF. norm: normalizeGuidedMsg(question); toks:
// guidedTokens(norm). Adlı mesajlar (explicitEntity=true) asla kip sayılmaz.
func detectContextMutation(norm string, toks []string, c ChatContext, explicitEntity bool) (contextMutation, bool) {
	if c.LastRoute == nil || explicitEntity || len(toks) == 0 || len(toks) > 8 {
		return contextMutation{}, false
	}
	// Pencere: açık aralık + pencere fiili ("son 1 saate genişlet", "24 saate bak",
	// "pencereyi 6 saate çıkar"); "son 1 saatte hata var mı" değil (hata sinyali var).
	if v, ok := guidedRangeSExplicit(norm); ok && anyToken(toks, windowVerbs...) &&
		!hasErrorSignal(toks) && !hasLogSignal(toks) && !hasPodSignal(toks) && !hasSlowTraceSignal(norm) {
		return contextMutation{Kind: "window", RangeS: v, Label: "pencere " + fmtRangeTR(v)}, true
	}
	// Yalnız hatalı: "sadece hatalı olanlar", "yalnız hatalılar", "sadece error'lar".
	if anyToken(toks, onlyWords...) && hasErrorSignal(toks) && !hasLogSignal(toks) {
		return contextMutation{Kind: "errors", Label: "yalnız hatalı"}, true
	}
	// v0.10.491 (Astra #8) — soru/analiz sözcüğü varsa kip DEĞİL; kip için
	// fiil ya da işaret zamiri şart, ya da mesaj yalnız pod/log sözcüğünden
	// ibaret ("pod'ları", "logları").
	if anyToken(toks, questionWords...) {
		return contextMutation{}, false
	}
	// bare: ek artıkları sayılmaz ("pod'ları" → pod + ları).
	substantive := 0
	for _, t := range toks {
		if !findSuffixDebris[t] && len([]rune(t)) > 2 {
			substantive++
		}
	}
	bare := substantive == 1
	// Pod'lar: "bunun pod'larını göster", "pod'ları", "podları göster".
	if anyToken(toks, podWords...) && !hasErrorSignal(toks) && (bare || anyToken(toks, mutationVerbs...)) {
		return contextMutation{Kind: "pods", Label: "pod'lar"}, true
	}
	// Loglar: "aynı filtreyle loglara bak", "loglara bak", "logları".
	if anyToken(toks, logWords...) && (bare || anyToken(toks, mutationVerbs...)) {
		return contextMutation{Kind: "logs", Label: "loglar"}, true
	}
	return contextMutation{}, false
}

// applyContextMutation — SAF: bağlam + kip → (yeni rota, pencere, yeni bağlam).
// ok=false → kip bu bağlamda anlamsız (çağıran normal yola bırakır).
func applyContextMutation(c ChatContext, m contextMutation) (guidedRoute, int64, ChatContext, bool) {
	last := *c.LastRoute
	rangeS := c.RangeS
	if rangeS <= 0 {
		rangeS = 1800
	}
	switch m.Kind {
	case "window":
		c.RangeS, c.RangeExplicit = m.RangeS, true
		return last, m.RangeS, c, true
	case "errors":
		c.ErrorsOnly = true
		switch last.Intent {
		case guidedTraceSearch, guidedFamilyTraces:
			last.TraceErrorsOnly = true
			return last, rangeS, c, true
		case guidedSlowTraces:
			if last.Service == "" {
				return guidedRoute{}, 0, c, false
			}
			return guidedRoute{Intent: guidedFamilyTraces, Service: last.Service, Family: []string{last.Service}, Env: last.Env, TraceErrorsOnly: true}, rangeS, c, true
		case guidedServiceHealth, guidedFindEntity:
			if last.Service == "" {
				return guidedRoute{}, 0, c, false
			}
			return guidedRoute{Intent: guidedFamilyTraces, Service: last.Service, Family: []string{last.Service}, Env: last.Env, TraceErrorsOnly: true}, rangeS, c, true
		}
		return guidedRoute{}, 0, c, false
	case "pods":
		switch {
		case c.Service != "":
			return guidedRoute{Intent: guidedPodHealth, Service: c.Service, Env: last.Env}, rangeS, c, true
		case c.Namespace != "":
			return guidedRoute{Intent: guidedNamespaceServices, FindQuery: c.Namespace, FindPods: true, Env: last.Env}, rangeS, c, true
		}
		return guidedRoute{}, 0, c, false
	case "logs":
		if c.Service == "" {
			return guidedRoute{}, 0, c, false
		}
		r := guidedRoute{Intent: guidedLogErrors, Service: c.Service, Env: last.Env}
		if strings.TrimSpace(c.SearchText) != "" {
			// Aynı değeri loglarda ara (message alanı içerik eşleşmesi).
			r = guidedRoute{Intent: guidedLogField, Service: c.Service, Env: last.Env, LogField: "message", LogValue: c.SearchText, LogContains: true}
		}
		return r, rangeS, c, true
	}
	return guidedRoute{}, 0, c, false
}
