package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"time"

	"github.com/cilcenk/coremetry/internal/copilot"
	"github.com/cilcenk/coremetry/internal/logstore"
)

// copilot_exception.go — exception grubu kök-sebep açıklaması
// (v0.9.414, operatör istegi: "anonslu exception'ları otomatik log +
// trace ile sorgulayıp kök sebep çıkarsın — aynı Explain trace gibi").
//
// Desen copilotExplainTrace'in birebir devamı: sunucu prefetch'i
// (grup meta + örnekler + occurrence trendi + örnek TRACE + trace'in
// LOGLARI + deploy penceresi) → tek narration çağrısı
// (SystemPromptException, zaten mevcuttu — tek tüketicisi MCP
// prompt'uydu) → v0.9.408 kanıt sözleşmesi: kanıt trace/span'leri LLM
// çıktısından DEĞİL, modele beslenen aynı compact veriden
// deterministik hesaplanır; UI örnek-trace satırlarını kutular.
//
// Tüm zenginleştirmeler best-effort — log store yok/yavaş, trace
// bulunamadı, deploy sorgusu düştü → o blok atlanır, explain asla
// düşmez (explain-trace sözleşmesi). Cache yok: interaktif explain'ler
// her tıkta taze LLM çağrısıdır (rootcause'un aksine), kayıt ai_calls
// üzerinden s.copilotExplain wrapper'ında.

func (s *Server) copilotExplainException(w http.ResponseWriter, r *http.Request) {
	if !s.copilot.Active() {
		http.Error(w, "AI copilot not available (disabled or not configured)", http.StatusServiceUnavailable)
		return
	}
	fp := r.PathValue("fp")
	g, err := s.store.GetExceptionGroup(r.Context(), fp)
	if err != nil {
		writeErr(w, err)
		return
	}
	if g == nil {
		http.Error(w, "exception group not found", http.StatusNotFound)
		return
	}

	// ── Örnekler (trace_id + stacktrace kaynağı) ─────────────────────────
	samples, _ := s.store.GetExceptionGroupSamples(r.Context(), fp, 5)

	// ── Occurrence trendi — compact özet (tam histogram prompt'u şişirir) ─
	trend := ""
	if occ, oerr := s.store.GetExceptionOccurrences(r.Context(), fp); oerr == nil && len(occ) > 0 {
		var total, last24 uint64
		var peak uint64
		var peakAt int64
		cut := time.Now().Add(-24 * time.Hour).UnixNano()
		for _, p := range occ {
			total += p.Count
			if p.Time >= cut {
				last24 += p.Count
			}
			if p.Count > peak {
				peak, peakAt = p.Count, p.Time
			}
		}
		trend = fmt.Sprintf("toplam=%d son24h=%d tepe=%d@%s bucket=%d",
			total, last24, peak, time.Unix(0, peakAt).UTC().Format("2006-01-02 15:04"), len(occ))
	}

	// ── Örnek TRACE (en yeni trace'li örnek) + kanıt span'leri ───────────
	type liteSpan struct {
		Name       string  `json:"name"`
		Service    string  `json:"service"`
		Kind       string  `json:"kind"`
		ParentSpan string  `json:"parent,omitempty"`
		SpanID     string  `json:"id"`
		DurationMs float64 `json:"durMs"`
		Status     string  `json:"status,omitempty"`
		StatusMsg  string  `json:"statusMsg,omitempty"`
	}
	var traceBlock string
	var evSpans []string
	var evTraces []string
	var traceID string
	var traceMinT, traceMaxT int64
	for _, sm := range samples {
		if sm.TraceID != "" {
			traceID = sm.TraceID
			break
		}
	}
	if traceID != "" {
		tctx, cancel := context.WithTimeout(r.Context(), 8*time.Second)
		spans, terr := s.store.GetTrace(tctx, traceID)
		cancel()
		if terr == nil && len(spans) > 0 {
			evTraces = append(evTraces, traceID)
			traceMinT, traceMaxT = spans[0].StartTime, spans[0].EndTime
			for _, sp := range spans {
				if sp.StartTime < traceMinT {
					traceMinT = sp.StartTime
				}
				if sp.EndTime > traceMaxT {
					traceMaxT = sp.EndTime
				}
			}
			// 60'lık prompt bütçesi — ama error span'ler GARANTİLİ:
			// GetTrace time-ASC döner ve hata tipik olarak trace'in
			// derinindedir; düz spans[:60] kesimi asıl exception
			// span'ini hem prompt'tan hem kanıttan düşürüyordu
			// (verify bulgusu). Önce error'lar (≤20) işaretlenir,
			// kalan bütçe time-ASC dolar; orijinal sıra korunur.
			include := make([]bool, len(spans))
			kept, errKept := 0, 0
			for i, sp := range spans {
				if sp.StatusCode == "error" && errKept < 20 {
					include[i] = true
					kept++
					errKept++
				}
			}
			for i := range spans {
				if kept >= 60 {
					break
				}
				if !include[i] {
					include[i] = true
					kept++
				}
			}
			compact := make([]liteSpan, 0, kept)
			for i, sp := range spans {
				if !include[i] {
					continue
				}
				l := liteSpan{Name: sp.Name, Service: sp.ServiceName, Kind: sp.Kind,
					ParentSpan: sp.ParentSpanID, SpanID: sp.SpanID,
					DurationMs: float64(sp.EndTime-sp.StartTime) / 1e6}
				if sp.StatusCode == "error" {
					l.Status = "error"
					l.StatusMsg = sp.StatusMessage
					if len(evSpans) < 5 {
						evSpans = append(evSpans, sp.SpanID)
					}
				}
				compact = append(compact, l)
			}
			if tp, e := json.Marshal(compact); e == nil {
				traceBlock = fmt.Sprintf("\n\nÖrnek hata TRACE'i (%s, %d span):\n```json\n%s\n```",
					traceID, len(compact), string(tp))
			}
		}
	}

	// ── Trace'in logları (tek pivot sorgusu; explain-trace v0.9.166 deseni) ─
	// traceMaxT > 0 şartı: trace retention dışına düşmüş/yüklenememişse
	// pencere 1970 epoch olurdu — garanti-boş ES turu atma (verify bulgusu).
	var logsBlock string
	if s.logs != nil && traceID != "" && traceMaxT > 0 {
		from := time.Unix(0, traceMinT).Add(-time.Minute)
		to := time.Unix(0, traceMaxT).Add(time.Minute)
		lctx, cancel := context.WithTimeout(r.Context(), 6*time.Second)
		if page, lerr := logstore.LogsForTrace(lctx, s.logs, traceID, from, to, 30); lerr == nil && page != nil && len(page.Logs) > 0 {
			type liteLog struct {
				Sev    string `json:"sev,omitempty"`
				Svc    string `json:"svc,omitempty"`
				ExType string `json:"exType,omitempty"`
				Stack  string `json:"stack,omitempty"`
				Body   string `json:"body,omitempty"`
			}
			logs := page.Logs
			sort.SliceStable(logs, func(i, j int) bool { return logs[i].Severity > logs[j].Severity })
			ll := make([]liteLog, 0, 12)
			for _, lg := range logs {
				if len(ll) >= 12 {
					break
				}
				e := liteLog{Sev: lg.SeverityText, Svc: lg.ServiceName, Body: truncate(lg.Body, 500)}
				if lg.Attributes != nil {
					e.ExType = lg.Attributes["exception.type"]
					e.Stack = truncate(lg.Attributes["exception.stacktrace"], 900)
				}
				ll = append(ll, e)
			}
			if lp, e := json.Marshal(ll); e == nil {
				logsBlock = fmt.Sprintf("\n\nBu trace'in ilişkili LOGLARI (yüksek severity önce):\n```json\n%s\n```", string(lp))
			}
		}
		cancel()
	}

	// ── Deploy penceresi — "deploy sonrası regresyon" en güçlü ipucu ─────
	// Seçim FirstSeen'e YAKINLIĞA göre (verify bulguları): asıl aday,
	// grubun başlangıcından hemen ÖNCEKİ deploy'dur — düz "son 5" kesimi
	// uzun ömürlü gruplarda onu düşürüyordu. GetServiceDeploys ASC döner:
	// FirstSeen öncesinden son 3 + sonrasından ilk 2 alınır ve önce/sonra
	// açıkça yazılır (negatif "önce" LLM'e yanlış kanıt olur).
	deployBlock := ""
	if g.Service != "" {
		dFrom := time.Unix(0, g.FirstSeen).Add(-6 * time.Hour)
		dTo := time.Unix(0, g.LastSeen)
		if deps, derr := s.store.GetServiceDeploys(r.Context(), g.Service, dFrom, dTo); derr == nil && len(deps) > 0 {
			split := len(deps) // ilk FirstSeen-SONRASI deploy'un indexi
			for i, d := range deps {
				if d.TimeUnixNs > g.FirstSeen {
					split = i
					break
				}
			}
			before := deps[:split]
			after := deps[split:]
			if len(before) > 3 {
				before = before[len(before)-3:]
			}
			if len(after) > 2 {
				after = after[:2]
			}
			parts := make([]string, 0, len(before)+len(after))
			for _, d := range before {
				rel := (g.FirstSeen - d.TimeUnixNs) / int64(time.Minute)
				parts = append(parts, fmt.Sprintf("%s (grubun başlangıcından %d dk ÖNCE)", d.Version, rel))
			}
			for _, d := range after {
				rel := (d.TimeUnixNs - g.FirstSeen) / int64(time.Minute)
				parts = append(parts, fmt.Sprintf("%s (grubun başlangıcından %d dk SONRA — kök neden OLAMAZ, olsa olsa etki/çözüm denemesi)", d.Version, rel))
			}
			deployBlock = "\n\nAynı servisin yakın DEPLOY'ları: " + fmt.Sprintf("%v", parts) +
				"\nGrubun başlangıcı bir deploy'un hemen SONRASINA denk geliyorsa o deploy'u kök neden adayı olarak öne al."
		}
	}

	// ── Stacktrace (temsilî — trace'siz gruplarda tek sinyal bu) ─────────
	stack := ""
	for _, sm := range samples {
		if sm.Stacktrace != "" {
			stack = truncate(sm.Stacktrace, 1800)
			break
		}
	}

	meta := map[string]any{
		"type": g.Type, "message": truncate(g.Message, 400), "service": g.Service,
		"state": g.State, "occurrences": g.Occurrences,
		"firstSeen": time.Unix(0, g.FirstSeen).UTC().Format(time.RFC3339),
		"lastSeen":  time.Unix(0, g.LastSeen).UTC().Format(time.RFC3339),
	}
	mp, _ := json.Marshal(meta)
	user := fmt.Sprintf("Exception GRUBU:\n```json\n%s\n```", string(mp))
	if trend != "" {
		user += "\n\nOccurrence trendi: " + trend
	}
	if stack != "" {
		user += "\n\nTemsilî STACKTRACE:\n```\n" + stack + "\n```"
	}
	user += traceBlock + logsBlock + deployBlock +
		"\n\nStacktrace + trace + logları BİRLİKTE yorumla: kök nedeni stack'in en üst uygulama-frame'ine ve trace'te hatanın DOĞDUĞU (en derin error) span'a dayandır; yayılan (propagate) hataları kök sanma."

	out, err := s.copilotExplain(r, copilot.SystemPromptException(), user)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, map[string]any{
		"explanation":      out,
		"evidenceTraceIds": evTraces,
		"evidenceSpanIds":  evSpans,
	})
}
