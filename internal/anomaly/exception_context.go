package anomaly

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/cilcenk/coremetry/internal/chstore"
	"github.com/cilcenk/coremetry/internal/logstore"
)

// exception_context.go — exception kök-sebep girdi kurucusu (v0.9.415).
// v0.9.414'te copilot_exception.go içinde doğan prefetch buraya taşındı:
// operatör-tıklı explain (internal/api) ile proaktif ExceptionExplainer
// işçisi AYNI girdiyi kurar — iki kopya sürüklenmez.
//
// Sözleşme (v0.9.414'ten aynen): tüm zenginleştirmeler best-effort;
// kanıt trace/span'leri LLM'den bağımsız, girdiye giren veriden
// deterministik. Saf montaj (assembleExceptionPrompt) ve deploy seçimi
// (PickDeploysAroundStart) tablo-testli — exception_context_test.go.

// ExceptionExplainInput — kurulan girdi + deterministik kanıt.
type ExceptionExplainInput struct {
	User     string   // narration user prompt'u
	EvTraces []string // kanıt trace id'leri (örnek tablosu kutulaması)
	EvSpans  []string // kanıt span id'leri
}

// BuildExceptionExplainInput — grup meta + occurrence trendi + temsilî
// stacktrace + en yeni örneğin TAM trace'i + o trace'in logları +
// FirstSeen-merkezli deploy penceresi. logs nil olabilir (CH-only
// kurulum ya da işçi bağlamı) — log bloğu atlanır.
func BuildExceptionExplainInput(ctx context.Context, store *chstore.Store, logs logstore.Store, g *chstore.ExceptionGroup) ExceptionExplainInput {
	samples, _, _, _ := store.GetExceptionGroupSamples(ctx, g.Fingerprint, 5)

	trend := ""
	if occ, oerr := store.GetExceptionOccurrences(ctx, g.Fingerprint); oerr == nil && len(occ) > 0 {
		var total, last24, peak uint64
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

	// En yeni trace'li örnek → tam trace + kanıt (error span'ler GARANTİLİ
	// — v0.9.414 verify bulgusu: düz head-cap derindeki hatayı düşürüyordu).
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
	var traceBlock, logsBlock string
	var evTraces, evSpans []string
	traceID := ""
	var traceMinT, traceMaxT int64
	for _, sm := range samples {
		if sm.TraceID != "" {
			traceID = sm.TraceID
			break
		}
	}
	if traceID != "" {
		tctx, cancel := context.WithTimeout(ctx, 8*time.Second)
		spans, terr := store.GetTrace(tctx, traceID)
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

	// Trace'in logları — tek pivot sorgusu; trace yüklenemediyse (1970
	// penceresi) HİÇ gitme (v0.9.414 verify bulgusu, ES maliyet disiplini).
	if logs != nil && traceID != "" && traceMaxT > 0 {
		from := time.Unix(0, traceMinT).Add(-time.Minute)
		to := time.Unix(0, traceMaxT).Add(time.Minute)
		lctx, cancel := context.WithTimeout(ctx, 6*time.Second)
		if page, lerr := logstore.LogsForTrace(lctx, logs, traceID, from, to, 30); lerr == nil && page != nil && len(page.Logs) > 0 {
			type liteLog struct {
				Sev    string `json:"sev,omitempty"`
				Svc    string `json:"svc,omitempty"`
				ExType string `json:"exType,omitempty"`
				Stack  string `json:"stack,omitempty"`
				Body   string `json:"body,omitempty"`
			}
			lgs := page.Logs
			sort.SliceStable(lgs, func(i, j int) bool { return lgs[i].Severity > lgs[j].Severity })
			ll := make([]liteLog, 0, 12)
			for _, lg := range lgs {
				if len(ll) >= 12 {
					break
				}
				e := liteLog{Sev: lg.SeverityText, Svc: lg.ServiceName, Body: truncRunes(lg.Body, 500)}
				if lg.Attributes != nil {
					e.ExType = lg.Attributes["exception.type"]
					e.Stack = truncRunes(lg.Attributes["exception.stacktrace"], 900)
				}
				ll = append(ll, e)
			}
			if lp, e := json.Marshal(ll); e == nil {
				logsBlock = fmt.Sprintf("\n\nBu trace'in ilişkili LOGLARI (yüksek severity önce):\n```json\n%s\n```", string(lp))
			}
		}
		cancel()
	}

	// Deploy penceresi — FirstSeen'e YAKINLIĞA göre seçim + önce/sonra
	// açık etiket (v0.9.414 verify bulguları).
	deployBlock := ""
	if g.Service != "" {
		dFrom := time.Unix(0, g.FirstSeen).Add(-6 * time.Hour)
		dTo := time.Unix(0, g.LastSeen)
		if deps, derr := store.GetServiceDeploys(ctx, g.Service, dFrom, dTo); derr == nil && len(deps) > 0 {
			if parts := PickDeploysAroundStart(deps, g.FirstSeen); len(parts) > 0 {
				deployBlock = "\n\nAynı servisin yakın DEPLOY'ları: " + fmt.Sprintf("%v", parts) +
					"\nGrubun başlangıcı bir deploy'un hemen SONRASINA denk geliyorsa o deploy'u kök neden adayı olarak öne al."
			}
		}
	}

	stack := ""
	for _, sm := range samples {
		if sm.Stacktrace != "" {
			stack = truncRunes(sm.Stacktrace, 1800)
			break
		}
	}

	return ExceptionExplainInput{
		User:     assembleExceptionPrompt(g, trend, stack, traceBlock, logsBlock, deployBlock),
		EvTraces: evTraces,
		EvSpans:  evSpans,
	}
}

// PickDeploysAroundStart — GetServiceDeploys ASC döner; FirstSeen
// ÖNCESİNDEN son 3 + SONRASINDAN ilk 2 seçilir ve yön açıkça yazılır.
// Saf — exception_context_test.go: düz "son 5" kesimi uzun ömürlü
// gruplarda asıl adayı (başlangıçtan hemen önceki deploy) düşürüyordu,
// negatif "önce" ise LLM'e yanlış kanıt oluyordu (v0.9.414 bulguları).
func PickDeploysAroundStart(deps []chstore.Deploy, firstSeen int64) []string {
	split := len(deps)
	for i, d := range deps {
		if d.TimeUnixNs > firstSeen {
			split = i
			break
		}
	}
	before, after := deps[:split], deps[split:]
	if len(before) > 3 {
		before = before[len(before)-3:]
	}
	if len(after) > 2 {
		after = after[:2]
	}
	parts := make([]string, 0, len(before)+len(after))
	for _, d := range before {
		rel := (firstSeen - d.TimeUnixNs) / int64(time.Minute)
		parts = append(parts, fmt.Sprintf("%s (grubun başlangıcından %d dk ÖNCE)", d.Version, rel))
	}
	for _, d := range after {
		rel := (d.TimeUnixNs - firstSeen) / int64(time.Minute)
		parts = append(parts, fmt.Sprintf("%s (grubun başlangıcından %d dk SONRA — kök neden OLAMAZ, olsa olsa etki/çözüm denemesi)", d.Version, rel))
	}
	return parts
}

// assembleExceptionPrompt — saf montaj; boş bloklar atlanır.
func assembleExceptionPrompt(g *chstore.ExceptionGroup, trend, stack, traceBlock, logsBlock, deployBlock string) string {
	meta := map[string]any{
		"type": g.Type, "message": truncRunes(g.Message, 400), "service": g.Service,
		"state": g.State, "occurrences": g.Occurrences,
		"firstSeen": time.Unix(0, g.FirstSeen).UTC().Format(time.RFC3339),
		"lastSeen":  time.Unix(0, g.LastSeen).UTC().Format(time.RFC3339),
	}
	mp, _ := json.Marshal(meta)
	var sb strings.Builder
	fmt.Fprintf(&sb, "Exception GRUBU:\n```json\n%s\n```", string(mp))
	if trend != "" {
		sb.WriteString("\n\nOccurrence trendi: " + trend)
	}
	if stack != "" {
		sb.WriteString("\n\nTemsilî STACKTRACE:\n```\n" + stack + "\n```")
	}
	sb.WriteString(traceBlock)
	sb.WriteString(logsBlock)
	sb.WriteString(deployBlock)
	sb.WriteString("\n\nStacktrace + trace + logları BİRLİKTE yorumla: kök nedeni stack'in en üst uygulama-frame'ine ve trace'te hatanın DOĞDUĞU (en derin error) span'a dayandır; yayılan (propagate) hataları kök sanma.")
	return sb.String()
}

// truncRunes — rune-güvenli kesme (api.truncate byte keserdi; Türkçe
// çok-baytlı mesajlarda U+FFFD üretiyordu — v0.9.414 verify notu).
func truncRunes(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…"
}
