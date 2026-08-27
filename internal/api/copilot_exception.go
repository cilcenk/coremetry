package api

import (
	"net/http"

	"github.com/cilcenk/coremetry/internal/anomaly"
	"github.com/cilcenk/coremetry/internal/copilot"
	"github.com/cilcenk/coremetry/internal/devops"
)

// copilot_exception.go — exception grubu kök-sebep açıklaması
// (v0.9.414, operatör istegi: "anonslu exception'ları otomatik log +
// trace ile sorgulayıp kök sebep çıkarsın — aynı Explain trace gibi").
//
// v0.9.415: prefetch + prompt montajı anomaly.BuildExceptionExplainInput'a
// taşındı — proaktif ExceptionExplainer işçisi AYNI girdiyi kurar (iki
// kopya sürüklenmez). Bu handler artık yalnız: gate → grup → girdi →
// s.copilotExplain (surface path'ten "explain-exception" olur, /ai
// attribution) → v0.9.408 kanıt sözleşmesiyle cevap. Cache yok:
// interaktif explain her tıkta taze; kayıt ai_calls'ta.
func (s *Server) copilotExplainException(w http.ResponseWriter, r *http.Request) {
	r, xid := withExchange(r)
	g, err := s.store.GetExceptionGroup(r.Context(), r.PathValue("fp"))
	if err != nil {
		writeErr(w, err)
		return
	}
	if g == nil {
		http.Error(w, "exception group not found", http.StatusNotFound)
		return
	}
	opts := decodeExplainOptions(r)
	in := anomaly.BuildExceptionExplainInput(r.Context(), s.store, s.logs, g)

	// v0.9.831 — "Kodu da incele". Varsayılan KAPALI: kod çekmek bir
	// depo listelemesi + dosya okuması demek, her Explain tıkında
	// ödenecek bir maliyet değil. İşaretlendiğinde de fail-open —
	// kod gelmezse açıklama yine üretilir, cc.Reason yanıtta döner.
	var cc devops.CodeContext
	run := s.explainPrompt(r, copilot.SystemPromptException(), in.User)
	// v0.10.83 — anahtar GERÇEK prompt'tan; kod dalında blok da kimlik.
	cacheKey := explainCacheKey(copilot.SystemPromptException(), in.User, "")
	if opts.IncludeCode {
		// v0.9.1225 — stack log-fallback'ten geldiyse depo çözümü logu
		// atan servise gider (bsa- önek deseni servis adından türetilir).
		codeSvc := g.Service
		if in.StackService != "" {
			codeSvc = in.StackService
		}
		cc = s.buildCodeContext(r.Context(), codeSvc, in.Stack)
		cacheKey = explainCacheKey(copilot.SystemPromptExceptionWithCode(), in.User, cc.PromptBlock())
		run = explainPromptBuffered(func() (string, error) {
			return s.copilotExplainCode(r,
				copilot.SystemPromptException(), copilot.SystemPromptExceptionWithCode(), in.User, cc)
		})
	}
	// v0.9.1127 (Faz 1.5) — cevabın çıkışı tek yerden: `?stream=1` ise
	// SSE (delta→answer→done), değilse bugünkü gövde bayt bayt.
	s.deliverExplain(w, r, xid, map[string]any{
		"evidenceTraceIds": in.EvTraces,
		"evidenceSpanIds":  in.EvSpans,
		"code":             codePayload(cc, opts.IncludeCode),
	}, run, g.Service, cacheKey)
}
