package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/cilcenk/coremetry/internal/chstore"
)

// System-wide SRE analysis (v0.8.75). SINGLE-SHOT — no tool/function calling.
// The server assembles a compact snapshot of the WHOLE system's observability
// (every service's RED, open problems, active log/trace anomalies, and the
// service-to-service topology) and hands it to the model with a fixed
// SRE-analyst prompt. The model evaluates ALL of it together and returns a
// strict JSON verdict: system status, root cause, affected service chain,
// per-service findings, and recommended actions. One call, system-level
// "what's wrong and where did it start" — the correlation-differentiator thesis
// at the fleet level.

// systemAnalysisPrompt is the operator-authored SRE-analyst instruction. Kept
// verbatim (Turkish) — the model must answer ONLY in the JSON shape below.
const systemAnalysisPrompt = `Sen Coremetry'nin SRE analiz motorusun. Sana TÜM sistemin observability
özeti verilir (tüm servisler için metrics, anomalies, log ve trace özetleri).
Görevin: bütün veriyi birlikte değerlendirip sistem genelinde kök-neden ve
etkilenen servis zincirini bulmak.

KURALLAR:
- Sadece VERİLEN veriye dayan. Veride olmayanı uydurma.
- Servisler arası ilişkiyi dikkate al (bir servisin sorunu başka servisi
  etkileyebilir — örn. DB yavaşsa ona bağlı tüm servisler etkilenir).
- En kritik sorunu en üste koy (en yüksek error_rate / latency artışı).
- Veri yetersizse bunu açıkça belirt.
- Türkçe, kısa ve teknik yaz.
- Çıktıyı SADECE aşağıdaki JSON formatında ver, başka hiçbir şey yazma.

ÇIKTI FORMATI:
{
  "sistem_durumu": "saglikli" | "bozulma" | "kritik",
  "ozet": "<2-3 cümle genel durum>",
  "kok_neden": "<tüm veriye göre en olası ana kaynak>",
  "etkilenen_zincir": ["<servis A>", "<servis B>", "..."],
  "bulgular": [
    {
      "servis": "<servis adı>",
      "sorun": "<ne oluyor>",
      "kanit": "<hangi metrik/log/anomali>",
      "onem": "yuksek" | "orta" | "dusuk"
    }
  ],
  "oneriler": ["<aksiyon 1>", "<aksiyon 2>"],
  "guven": "yuksek" | "orta" | "dusuk"
}`

// systemAnalysis mirrors the required JSON output so the server can validate +
// hand the frontend a typed object (with the raw text as a fallback).
type systemAnalysis struct {
	SistemDurumu    string   `json:"sistem_durumu"`
	Ozet            string   `json:"ozet"`
	KokNeden        string   `json:"kok_neden"`
	EtkilenenZincir []string `json:"etkilenen_zincir"`
	Bulgular        []struct {
		Servis string `json:"servis"`
		Sorun  string `json:"sorun"`
		Kanit  string `json:"kanit"`
		Onem   string `json:"onem"`
	} `json:"bulgular"`
	Oneriler []string `json:"oneriler"`
	Guven    string   `json:"guven"`
}

// copilotAnalyze runs the system-wide single-shot analysis and returns the
// model's JSON verdict (parsed + raw). Read-only; any authenticated user.
func (s *Server) copilotAnalyze(w http.ResponseWriter, r *http.Request) {
	if s.copilot == nil || !s.copilot.Active() {
		http.Error(w, `{"error":"AI copilot not available (disabled or not configured)"}`, http.StatusServiceUnavailable)
		return
	}
	rangeS := parseInt(r.URL.Query().Get("rangeS"), 1800)
	if rangeS <= 0 || rangeS > 7*24*3600 {
		rangeS = 1800
	}
	to := time.Now()
	from := to.Add(-time.Duration(rangeS) * time.Second)

	snapshot := s.buildSystemSnapshot(r.Context(), from, to)
	if strings.TrimSpace(snapshot) == "" {
		writeJSON(w, map[string]any{"parsed": false, "raw": "", "error": "no telemetry in window"})
		return
	}

	// Single-shot through the /ai-attributed wrapper (CLAUDE.md: never call
	// s.copilot.Explain direct). No tools — the snapshot IS the context.
	raw, err := s.copilotExplain(r, systemAnalysisPrompt, snapshot)
	if err != nil {
		writeErr(w, err)
		return
	}
	parsed := parseSystemAnalysis(raw)
	writeJSON(w, map[string]any{
		"analysis": parsed,
		"raw":      raw,
		"parsed":   parsed != nil,
	})
}

// buildSystemSnapshot assembles the compact whole-system observability summary
// fed to the model: services RED, open problems, active log/trace anomalies,
// and the service dependency edges (so it can reason about cascades).
func (s *Server) buildSystemSnapshot(ctx context.Context, from, to time.Time) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Gözlem penceresi: son %d dakika.\n\n", int(to.Sub(from).Minutes()))

	// Services RED — every service's health (error rate, latency, apdex).
	if svcs, err := s.store.GetServicesAgg(ctx, from, to, 300); err == nil && len(svcs) > 0 {
		b.WriteString("SERVİSLER (RED):\n")
		for _, sv := range svcs {
			fmt.Fprintf(&b, "- %s: error_rate=%.2f%%, p99=%.0fms, avg=%.0fms, apdex=%.2f, error_count=%d\n",
				sv.Name, sv.ErrorRate, sv.P99Ms, sv.AvgMs, sv.Apdex, sv.ErrorCount)
		}
		b.WriteString("\n")
	}

	// Open problems (alert/anomaly-fired).
	if ps, err := s.store.ListProblems(ctx, chstore.ProblemFilter{Status: "open", Limit: 100}); err == nil && len(ps) > 0 {
		b.WriteString("AÇIK PROBLEMLER:\n")
		for _, p := range ps {
			fmt.Fprintf(&b, "- %s [%s] %s metric=%s value=%.2f threshold=%.2f\n",
				p.Service, p.Severity, p.RuleName, p.Metric, p.Value, p.Threshold)
		}
		b.WriteString("\n")
	}

	// Active anomalies — log patterns + trace-op error spikes.
	if evs, err := s.store.ListAnomalyEvents(ctx, chstore.ListAnomalyEventsFilter{
		SinceNs: from.UnixNano(), Limit: 100,
	}); err == nil {
		var lines []string
		for _, e := range evs {
			if e.Status != "active" {
				continue
			}
			line := fmt.Sprintf("- %s [%s] %s", e.Service, e.Kind, e.Pattern)
			if smp := strings.TrimSpace(e.Sample); smp != "" {
				if len(smp) > 100 {
					smp = smp[:100] + "…"
				}
				line += " örnek=\"" + smp + "\""
			}
			lines = append(lines, line)
		}
		if len(lines) > 0 {
			b.WriteString("ANOMALİLER (log/trace):\n")
			b.WriteString(strings.Join(lines, "\n"))
			b.WriteString("\n\n")
		}
	}

	// Service dependency topology — A→B with call + error volume, so the model
	// can trace a cascade (a slow/failing dep dragging its callers down).
	if edges, err := s.store.GetServiceAdjacencyWeighted(ctx, to.Sub(from)); err == nil && len(edges) > 0 {
		b.WriteString("SERVİS BAĞIMLILIKLARI (A→B: çağrı/hata):\n")
		for i, e := range edges {
			if i >= 100 {
				break
			}
			fmt.Fprintf(&b, "- %s→%s: calls=%d errors=%d\n", e.Caller, e.Callee, e.Calls, e.Errors)
		}
		b.WriteString("\n")
	}

	return b.String()
}

// parseSystemAnalysis tolerantly extracts the JSON verdict — strips ``` fences
// the model sometimes wraps it in, then unmarshals the first {...} block.
// Returns nil if the model didn't produce parseable JSON (the caller falls back
// to showing the raw text).
func parseSystemAnalysis(raw string) *systemAnalysis {
	t := strings.TrimSpace(raw)
	if strings.HasPrefix(t, "```") {
		t = strings.TrimPrefix(t, "```json")
		t = strings.TrimPrefix(t, "```")
		t = strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(t), "```"))
	}
	i := strings.Index(t, "{")
	j := strings.LastIndex(t, "}")
	if i < 0 || j <= i {
		return nil
	}
	var a systemAnalysis
	if err := json.Unmarshal([]byte(t[i:j+1]), &a); err != nil {
		return nil
	}
	return &a
}
