package api

import (
	"context"
	"fmt"
	"net/http"
	"sort"
	"time"

	"github.com/cilcenk/coremetry/internal/chstore"
	"github.com/cilcenk/coremetry/internal/copilot"
)

// shift_page.go — /shift vardiya özeti yüzeyinin backend'i (v0.9.1071,
// Faz 3.2; mockup operatör-onaylı: üç tablo + tek ✨, liste-üstü KPI
// yok). Vardiya devri bugün 4 sayfa gezilerek yapılıyordu; buradaki tek
// GET üç bloğu birden verir. Tüm okumalar bounded state/MV okumaları —
// spans taraması yok. Pencere RUNG'LU (8h/12h/24h): değer cache
// anahtarına girer, serbest pencere kardinaliteyi patlatırdı (v0.8.270).

// shiftWindows — izinli pencereler (sunucu tek otorite; UI seg'i buna
// birebir).
var shiftWindows = map[string]time.Duration{
	"8h":  8 * time.Hour,
	"12h": 12 * time.Hour,
	"24h": 24 * time.Hour,
}

// shiftWindow — rung çözümü (saf). Bilinmeyen/boş → 12h varsayılan.
func shiftWindow(raw string) (string, time.Duration) {
	if d, ok := shiftWindows[raw]; ok {
		return raw, d
	}
	return "12h", shiftWindows["12h"]
}

// ShiftSummary — GET /api/shift cevabı.
type ShiftSummary struct {
	WindowSec int64 `json:"windowSec"`
	FromNs    int64 `json:"fromNs"`
	ToNs      int64 `json:"toNs"`
	// Problems — pencerede AÇILAN + pencerede ÇÖZÜLEN problemler
	// (öncelik + deploy + kök-neden özeti enriched). Kapananlar da
	// listede: "gece ne kendi kendine düzeldi" sorusu vardiyanın yarısı.
	Problems []chstore.Problem `json:"problems"`
	// Worsened — pencere vs önceki eş-boy pencere RED kıyası
	// (CorrelatedChangesMV; en fazla 10).
	Worsened []chstore.ChangedService `json:"worsened"`
	// NewExceptions — first_seen pencerede olan gruplar (en fazla 20;
	// kesme truncated ile İFŞA edilir).
	NewExceptions       []chstore.ExceptionGroup `json:"newExceptions"`
	NewExceptionsTotal  int                      `json:"newExceptionsTotal"`
}

// getShiftSummary — GET /api/shift?w=8h|12h|24h. Viewer-görünür salt
// okuma; 60s cache (rung'lu anahtar — operatörler pencereyi paylaşır).
func (s *Server) getShiftSummary(w http.ResponseWriter, r *http.Request) {
	rung, dur := shiftWindow(r.URL.Query().Get("w"))
	key := "shift:w=" + rung
	s.serveCached(w, r, key, 60*time.Second, func(ctx context.Context) (any, error) {
		to := time.Now()
		from := to.Add(-dur)
		out := ShiftSummary{
			WindowSec:     int64(dur.Seconds()),
			FromNs:        from.UnixNano(),
			ToNs:          to.UnixNano(),
			Problems:      []chstore.Problem{},
			Worsened:      []chstore.ChangedService{},
			NewExceptions: []chstore.ExceptionGroup{},
		}

		// 1. Pencere problemleri — guided bundle'ın aynı okuması.
		if probs, err := s.store.ListProblemWindowEvents(ctx, "", from, to); err == nil {
			probs = s.enrichProblemsForRead(ctx, probs)
			sort.SliceStable(probs, func(i, j int) bool { return probs[i].StartedAt > probs[j].StartedAt })
			if len(probs) > 100 {
				probs = probs[:100]
			}
			out.Problems = probs
		}

		// 2. Kötüleşenler — baseline = önceki eş-boy pencere (at=from).
		if cs, err := s.store.GetCorrelatedChangesMV(ctx, from, int(dur.Seconds()), int(dur.Seconds())); err == nil && cs != nil {
			if len(cs) > 10 {
				cs = cs[:10]
			}
			out.Worsened = cs
		}

		// 3. Pencerede doğan exception grupları. first_seen filtresi
		// SQL'de yok (bilinen sınır) — aktif-pencere ön-süzgeci + Go'da
		// first_seen; kesme dürüstçe ifşa.
		if groups, err := s.store.ListExceptionGroups(ctx, chstore.ExceptionGroupFilter{
			ActiveFromNs: from.UnixNano(), ActiveToNs: to.UnixNano(),
			Sort: "first_seen", Dir: "desc", Limit: 200,
		}); err == nil {
			var born []chstore.ExceptionGroup
			for _, g := range groups {
				if g.FirstSeen >= from.UnixNano() {
					born = append(born, g)
				}
			}
			sort.SliceStable(born, func(i, j int) bool { return born[i].Occurrences > born[j].Occurrences })
			out.NewExceptionsTotal = len(born)
			if len(born) > 20 {
				born = born[:20]
			}
			out.NewExceptions = born
		}
		return out, nil
	})
}

// explainShift — POST /api/copilot/explain-shift?w=… Tek-atış vardiya
// anlatımı: guided'ın hazır kanıt paketi (v0.9.416) + odaklı sistem
// prompt'u. FAB'a doğru cümleyi yazma şartı kalkıyor — sayfadaki ✨
// düğmesi buraya gelir. copilotExplain sarmalayıcısı /ai atıfını yazar.
func (s *Server) explainShift(w http.ResponseWriter, r *http.Request) {
	rung, dur := shiftWindow(r.URL.Query().Get("w"))
	to := time.Now()
	from := to.Add(-dur)
	evidence, _, err := s.guidedShiftSummaryBundle(r.Context(), func(string, any) {}, "", from, to, int64(dur.Seconds()))
	if err != nil {
		writeErr(w, fmt.Errorf("shift bundle: %w", err))
		return
	}
	out, err := s.copilotExplain(r, copilot.SystemPromptShiftSummary(), evidence)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, map[string]any{"explanation": out, "window": rung})
}
