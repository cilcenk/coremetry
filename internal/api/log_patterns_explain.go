package api

import (
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/cilcenk/coremetry/internal/anomaly"
	"github.com/cilcenk/coremetry/internal/chstore"
	"github.com/cilcenk/coremetry/internal/copilot"
)

// log_patterns_explain.go — F3.5 (v0.9.1100): Logs sayfasının ✨ desen
// anlatıcısı. İki HAZIR deterministik kaynak, sıfır yeni sorgu şekli
// (ES-maliyet disiplini, v0.8.270):
//
//   - anomaly.DetectLogPatterns — "ne değişti": yeni/patlayan desenler,
//     rung'lu pencere (snapAnomalyWindow, tavan 30m), batched _msearch.
//   - chstore.ListLogTemplates — "sürekli gürültü": Drain'in kalıcı
//     şablonları, CH state okuması (ucuz; ES'e hiç dokunmaz).
//
// Fetch YALNIZ tıkla (Shift/Noisy emsali); model paketi anlatır,
// yeniden inceleme yapmaz.

const (
	logPatternsExplainTopAnoms = 10
	logPatternsExplainTopTpls  = 8
)

func (s *Server) explainLogPatterns(w http.ResponseWriter, r *http.Request) {
	// 503 ön-kapısı ROUTE'ta: requireCopilot (ai_routes.go, v0.9.1118).
	window := snapAnomalyWindow(parseDuration(r.URL.Query().Get("window"), 30*time.Minute))

	hits, hitsErr := anomaly.DetectLogPatterns(r.Context(), s.logs, window)
	tpls, tplsErr := s.store.ListLogTemplates(r.Context(), chstore.ListLogTemplatesFilter{
		SinceNs: time.Now().Add(-24 * time.Hour).UnixNano(),
		SortBy:  "count",
		Limit:   logPatternsExplainTopTpls,
	})
	if hitsErr != nil && tplsErr != nil {
		writeErr(w, fmt.Errorf("log patterns: %v · templates: %v", hitsErr, tplsErr))
		return
	}
	evidence := renderLogPatternsEvidence(window, hits, tpls, hitsErr == nil, tplsErr == nil)
	out, err := s.copilotExplain(r, copilot.SystemPromptLogPatterns(), evidence)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, map[string]any{"explanation": out, "windowSec": int(window.Seconds())})
}

// renderLogPatternsEvidence — kanıt paketi (saf, tablo testli). Model
// yalnız buradaki rakamları anlatır; okunamayan kaynak İTİRAF edilir,
// kesmeler ifşa edilir (no-silent-caps).
func renderLogPatternsEvidence(window time.Duration, hits []anomaly.LogPatternAnomaly, tpls []chstore.LogTemplate, hitsOK, tplsOK bool) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Pencere: son %d dakika (desen anomali taraması).\n", int(window.Minutes()))

	switch {
	case !hitsOK:
		b.WriteString("Desen anomali taraması OKUNAMADI — yeni/patlayan desen bilgisi yok (sıfır DEĞİL).\n")
	case len(hits) == 0:
		b.WriteString("Pencerede yeni ya da patlayan log deseni yok.\n")
	default:
		shown := hits
		if len(shown) > logPatternsExplainTopAnoms {
			shown = shown[:logPatternsExplainTopAnoms]
			fmt.Fprintf(&b, "Desen anomalileri (%d bulundu, ilk %d listede):\n", len(hits), logPatternsExplainTopAnoms)
		} else {
			fmt.Fprintf(&b, "Desen anomalileri (%d):\n", len(hits))
		}
		for i, h := range shown {
			kind := "PATLAMA"
			if h.Kind == "new" {
				kind = "YENİ"
			}
			fmt.Fprintf(&b, "%d. [%s] %q — şimdi %d, taban %d (%.1fx); en çok %s",
				i+1, kind, h.Pattern, h.CurrentCount, h.BaselineCount, h.Ratio, h.Service)
			if h.Sample != "" {
				fmt.Fprintf(&b, "; örnek: %s", oneLineSample(h.Sample, 120))
			}
			b.WriteString("\n")
		}
	}

	switch {
	case !tplsOK:
		b.WriteString("Şablon kataloğu OKUNAMADI — sürekli-gürültü bilgisi yok.\n")
	case len(tpls) == 0:
		b.WriteString("Son 24 saatte kayıtlı log şablonu yok.\n")
	default:
		fmt.Fprintf(&b, "Sürekli gürültü — son 24 saatin en yüksek hacimli %d şablonu:\n", len(tpls))
		for i, t := range tpls {
			fmt.Fprintf(&b, "%d. %q — %d kayıt, %d servis",
				i+1, oneLineSample(t.Template, 100), t.TotalCount, len(t.Services))
			if t.ExceptionType != "" {
				fmt.Fprintf(&b, ", exception=%s", t.ExceptionType)
			}
			b.WriteString("\n")
		}
	}
	return b.String()
}

// oneLineSample — çok satırlı örneği tek satıra indirip RUNE sınırında
// keser (UTF-8 güvenli; bayt kesmesi geçersiz karakter üretir).
func oneLineSample(s string, max int) string {
	s = strings.Join(strings.Fields(s), " ")
	if r := []rune(s); len(r) > max {
		return string(r[:max]) + "…"
	}
	return s
}
