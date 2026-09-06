package api

import (
	"context"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/cilcenk/coremetry/internal/anomaly"
	"github.com/cilcenk/coremetry/internal/chstore"
	"github.com/cilcenk/coremetry/internal/copilot"
	"github.com/cilcenk/coremetry/internal/logstore"
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

// logPatternsScope — v0.10.507 (log arama denetimi C7): operatörün EKRANDAKİ
// süzgeci ve penceresi. Eskiden "✨ Desenleri anlat" bunları yok sayıyordu:
// filo geneli 30 dk (tavan) anomali taraması + 24 saatlik şablon kataloğu
// anlatılıyor, seçili servis/cluster/env/arama ve seçili pencere kanıta
// hiç girmiyordu. Şimdi paketin İLK bölümü bu kapsamın kendi desen
// örneklemesi (GroupBySignatureN, tek ES sayfası — 500 doküman, tıkla
// çekilir); anomali/şablon bölümleri filo geneli olarak etiketlenir.
type logPatternsScope struct {
	Service, Cluster, Env, Search string
	From, To                      time.Time
	Severity                      int
}

func (sc logPatternsScope) empty() bool {
	return sc.Service == "" && sc.Cluster == "" && sc.Env == "" && sc.Search == "" && sc.Severity == 0
}

const logPatternsExplainScopeTop = 8

// logPatternsScopeFromQuery — getLogsPatterns ile aynı parametre adları.
func logPatternsScopeFromQuery(get func(string) string) logPatternsScope {
	sev, _ := strconv.Atoi(get("severity"))
	return logPatternsScope{
		Service: get("service"), Cluster: get("cluster"), Env: strings.TrimSpace(get("env")),
		Search: get("search"), From: parseTime(get("from")), To: parseTime(get("to")), Severity: sev,
	}
}

func (s *Server) explainLogPatterns(w http.ResponseWriter, r *http.Request) {
	window := snapAnomalyWindow(parseDuration(r.URL.Query().Get("window"), 30*time.Minute))
	scope := logPatternsScopeFromQuery(r.URL.Query().Get)

	// v0.10.507 (C7) — kapsamın kendi desen örneklemesi: sayfa süzgeci +
	// seçili pencere (rung'lanmaz; pencere yoksa son `window`). Tek ES sayfası.
	sf := logstore.Filter{Service: scope.Service, Cluster: scope.Cluster, Env: scope.Env, Search: scope.Search,
		From: scope.From, To: scope.To, SeverityMin: uint8(scope.Severity)}
	if sf.To.IsZero() {
		sf.To = time.Now()
	}
	if sf.From.IsZero() {
		sf.From = sf.To.Add(-window)
	}
	pctx, pcancel := context.WithTimeout(r.Context(), 15*time.Second)
	pats, patsErr := logstore.GroupBySignatureN(pctx, s.logs, sf, logPatternsExplainScopeTop, logsPatternsSample(500))
	pcancel()

	hits, hitsErr := anomaly.DetectLogPatterns(r.Context(), s.logs, window)
	tpls, tplsErr := s.store.ListLogTemplates(r.Context(), chstore.ListLogTemplatesFilter{
		SinceNs: time.Now().Add(-24 * time.Hour).UnixNano(),
		SortBy:  "count",
		Limit:   logPatternsExplainTopTpls,
		Service: scope.Service, // v0.10.507 — şablon kataloğu da seçili servise
	})
	if hitsErr != nil && tplsErr != nil && patsErr != nil {
		writeErr(w, fmt.Errorf("log patterns: %v · templates: %v · scope: %v", hitsErr, tplsErr, patsErr))
		return
	}
	scope.From, scope.To = sf.From, sf.To
	evidence := renderLogPatternsScopeEvidence(scope, pats, patsErr == nil) +
		renderLogPatternsEvidence(window, hits, tpls, hitsErr == nil, tplsErr == nil)
	r, xid := withExchange(r)
	out, err := s.copilotExplain(r, copilot.SystemPromptLogPatterns(), evidence)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, map[string]any{"explanation": out, "exchangeId": xid,
		"windowSec": int(scope.To.Sub(scope.From).Seconds()), // v0.10.507 — GERÇEK pencere, rung değil
		"scope":     scope.label()})
}

// label — UI başlığı için kısa kapsam metni ("payments · prod · 6 sa").
func (sc logPatternsScope) label() string {
	parts := []string{}
	if sc.Service != "" {
		parts = append(parts, sc.Service)
	}
	if sc.Cluster != "" {
		parts = append(parts, sc.Cluster)
	}
	if sc.Env != "" {
		parts = append(parts, sc.Env)
	}
	if sc.Search != "" {
		parts = append(parts, "arama: "+oneLineSample(sc.Search, 40))
	}
	if !sc.From.IsZero() && !sc.To.IsZero() {
		parts = append(parts, fmtRangeTR(int64(sc.To.Sub(sc.From).Seconds())))
	}
	return strings.Join(parts, " · ")
}

// renderLogPatternsScopeEvidence — v0.10.507 (C7) SAF: paketin ilk bölümü,
// operatörün baktığı süzgeç + pencerenin desen örneklemesi. Süzgeç boşsa
// bunu söyler; örnekleme okunamadıysa "OKUNAMADI" (yokluk sıfır değil);
// tavan dolduysa kapsanan alt pencereyi yazar (v0.10.441 dürüstlük zarfı).
func renderLogPatternsScopeEvidence(sc logPatternsScope, pats *logstore.PatternsResult, ok bool) string {
	var b strings.Builder
	b.WriteString("BAKILAN KAPSAM — operatörün ekrandaki süzgeci ve penceresi:\n")
	if sc.empty() {
		b.WriteString("Süzgeç: yok (tüm servisler).")
	} else {
		fields := []string{}
		if sc.Service != "" {
			fields = append(fields, "servis="+sc.Service)
		}
		if sc.Cluster != "" {
			fields = append(fields, "cluster="+sc.Cluster)
		}
		if sc.Env != "" {
			fields = append(fields, "env="+sc.Env)
		}
		if sc.Severity > 0 {
			fields = append(fields, fmt.Sprintf("severity≥%d", sc.Severity))
		}
		if sc.Search != "" {
			fields = append(fields, "arama="+oneLineSample(sc.Search, 80))
		}
		b.WriteString("Süzgeç: " + strings.Join(fields, ", ") + ".")
	}
	if !sc.From.IsZero() && !sc.To.IsZero() {
		fmt.Fprintf(&b, " Pencere: %s (%s → %s UTC).\n",
			fmtRangeTR(int64(sc.To.Sub(sc.From).Seconds())),
			sc.From.UTC().Format("2006-01-02 15:04"), sc.To.UTC().Format("2006-01-02 15:04"))
	} else {
		b.WriteString("\n")
	}
	switch {
	case !ok || pats == nil:
		b.WriteString("Bu kapsamın desen örneklemesi OKUNAMADI — kapsam hakkında sonuç çıkarma (sıfır DEĞİL).\n")
	case len(pats.Groups) == 0:
		b.WriteString("Bu kapsamda örneklenen satır yok (pencere ve süzgeçte log bulunmadı).\n")
	default:
		fmt.Fprintf(&b, "Kapsamın desenleri (%d örnek satırdan %d farklı desen, ilk %d):\n",
			pats.Sampled, pats.Distinct, len(pats.Groups))
		if pats.Truncated && pats.CoveredFromNs > 0 && pats.CoveredToNs > 0 {
			fmt.Fprintf(&b, "(Örnekleme tavanı doldu: sayımlar yalnız %s → %s UTC alt penceresini anlatır.)\n",
				time.Unix(0, pats.CoveredFromNs).UTC().Format("15:04"), time.Unix(0, pats.CoveredToNs).UTC().Format("15:04"))
		}
		for i, g := range pats.Groups {
			fmt.Fprintf(&b, "%d. %q — %d satır, %s", i+1, oneLineSample(g.Template, 110), g.Count, sevWord(g.Severity, g.SeverityText))
			if len(g.Services) > 0 {
				fmt.Fprintf(&b, "; servisler: %s", strings.Join(g.Services, ", "))
				if g.ServiceCount > len(g.Services) {
					fmt.Fprintf(&b, " +%d", g.ServiceCount-len(g.Services))
				}
			}
			b.WriteString("\n")
		}
	}
	b.WriteString("\nFİLO GENELİ (süzgeçten bağımsız) — anomali taraması ve şablon kataloğu:\n")
	return b.String()
}

// sevWord — severity metni varsa o, yoksa sayısal seviye.
func sevWord(sev uint8, text string) string {
	if text != "" {
		return text
	}
	return fmt.Sprintf("seviye %d", sev)
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
