package api

import (
	"net/http"

	"github.com/cilcenk/coremetry/internal/anomaly"
	"github.com/cilcenk/coremetry/internal/copilot"
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
	if !s.copilot.Active() {
		http.Error(w, "AI copilot not available (disabled or not configured)", http.StatusServiceUnavailable)
		return
	}
	g, err := s.store.GetExceptionGroup(r.Context(), r.PathValue("fp"))
	if err != nil {
		writeErr(w, err)
		return
	}
	if g == nil {
		http.Error(w, "exception group not found", http.StatusNotFound)
		return
	}
	in := anomaly.BuildExceptionExplainInput(r.Context(), s.store, s.logs, g)
	out, err := s.copilotExplain(r, copilot.SystemPromptException(), in.User)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, map[string]any{
		"explanation":      out,
		"evidenceTraceIds": in.EvTraces,
		"evidenceSpanIds":  in.EvSpans,
	})
}
