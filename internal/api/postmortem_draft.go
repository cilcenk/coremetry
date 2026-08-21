package api

// postmortem_draft.go — POSTMORTEM TASLAĞI (v0.9.1197, AI Faz 5.4,
// onaylı plan).
//
// Incident sayfasındaki "✨ AI taslağı": incident satırı + zaman
// çizelgesi + ilişkili problemler (ihlal değerleri, kök-neden
// hipotezleri, AI özetleri) tek kanıt paketine iner, model blameless
// bir markdown taslak üretir ve taslak MEVCUT postmortem editörüne
// (textarea) düşer. Model hiçbir şey KAYDETMEZ — operatör düzenler,
// kaydeden yine v0.9.206'nın PUT /api/incidents/{id} yolu.
//
// Kanıt toplarken yalnız incident zaten sahip olduğu okumalar
// kullanılır (GetIncident + IncidentTimeline + IncidentProblems +
// GetProblem + GetHypotheses batch) — yeni sorgu şekli yok. Timeline
// ve problem listesi ile serbest metinler klamplanır; kesilen her şey
// pakete dürüstçe yazılır ("N olay, ilk 40 listede") ki model "hepsini
// gördüm" sanmasın.

import (
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/cilcenk/coremetry/internal/chstore"
	"github.com/cilcenk/coremetry/internal/copilot"
)

const (
	pmMaxTimelineEvents = 40
	pmMaxProblems       = 20
	pmMaxSummaryRunes   = 600 // problem AI özeti — prompt'u tek özet domine etmesin
	pmMaxEventRunes     = 240 // timeline olay gövdesi (not'lar serbest metin)
)

// pmClamp — rune-güvenli kırpma + satır sonlarını tek boşluğa indirme.
// Bayt kesmesi Türkçe karakteri ortadan bölerdi (curatedDocTitle ile
// aynı gerekçe).
func pmClamp(s string, maxRunes int) string {
	s = strings.Join(strings.Fields(s), " ")
	r := []rune(s)
	if len(r) > maxRunes {
		return string(r[:maxRunes]) + "…"
	}
	return s
}

// pmTime — kanıt paketindeki tek zaman biçimi (UTC). Model saatleri
// taslağa aynen kopyalar; iki farklı biçim iki farklı "gerçek" üretir.
func pmTime(ns int64) string {
	return time.Unix(0, ns).UTC().Format("2006-01-02 15:04") + " UTC"
}

type pmEvidenceInput struct {
	Inc           chstore.Incident
	Events        []chstore.IncidentEvent
	TotalEvents   int // klamp öncesi gerçek sayı
	Problems      []chstore.Problem
	TotalProblems int
	Hyps          map[string]chstore.RootCauseHypothesis
	NowNs         int64 // açık incident süresi için; testte deterministik
}

// renderPostmortemEvidence — SAF paket kurucu, tablo-testli.
func renderPostmortemEvidence(in pmEvidenceInput) string {
	var b strings.Builder
	inc := in.Inc

	fmt.Fprintf(&b, "Incident: %s\n", pmClamp(inc.Title, 200))
	svc := inc.Service
	if svc == "" {
		svc = "-"
	}
	fmt.Fprintf(&b, "Servis: %s — Önem: %s — Durum: %s\n", svc, inc.Severity, inc.Status)
	if inc.ResolvedAt != nil {
		dur := time.Duration(*inc.ResolvedAt - inc.StartedAt).Round(time.Minute)
		fmt.Fprintf(&b, "Pencere: %s → %s (süre %s)\n", pmTime(inc.StartedAt), pmTime(*inc.ResolvedAt), dur)
	} else {
		dur := time.Duration(in.NowNs - inc.StartedAt).Round(time.Minute)
		fmt.Fprintf(&b, "Pencere: %s → HENÜZ ÇÖZÜLMEDİ (şu ana dek %s)\n", pmTime(inc.StartedAt), dur)
	}
	if s := strings.TrimSpace(inc.Summary); s != "" {
		fmt.Fprintf(&b, "Operatör özeti: %s\n", pmClamp(s, 400))
	}

	b.WriteString("\nZaman çizelgesi")
	if in.TotalEvents > len(in.Events) {
		fmt.Fprintf(&b, " (%d olay, ilk %d listede)", in.TotalEvents, len(in.Events))
	}
	b.WriteString(":\n")
	if len(in.Events) == 0 {
		b.WriteString("- (olay kaydı yok ya da OKUNAMADI)\n")
	}
	for _, ev := range in.Events {
		line := fmt.Sprintf("- %s [%s]", pmTime(ev.Time), ev.Kind)
		if ev.Actor != "" {
			line += " " + ev.Actor
		}
		if body := pmClamp(ev.Body, pmMaxEventRunes); body != "" {
			line += ": " + body
		}
		b.WriteString(line + "\n")
	}

	b.WriteString("\nİlişkili problemler")
	if in.TotalProblems > len(in.Problems) {
		fmt.Fprintf(&b, " (%d problem, ilk %d listede)", in.TotalProblems, len(in.Problems))
	}
	b.WriteString(":\n")
	if len(in.Problems) == 0 {
		b.WriteString("- (ilişkili problem kaydı yok)\n")
	}
	for _, p := range in.Problems {
		fmt.Fprintf(&b, "- [%s] %s @ %s — %s: değer %.4g (eşik %.4g), durum %s, başlangıç %s",
			p.Severity, pmClamp(p.RuleName, 120), p.Service, p.Metric, p.Value, p.Threshold, p.Status, pmTime(p.StartedAt))
		if p.ResolvedAt != nil {
			fmt.Fprintf(&b, ", çözülme %s", pmTime(*p.ResolvedAt))
		}
		b.WriteString("\n")
		if h, ok := in.Hyps[p.ID]; ok && h.TopSuspect != "" {
			fmt.Fprintf(&b, "  Kök-neden hipotezi: şüpheli %s (güven %.2f)", h.TopSuspect, h.Confidence)
			if h.RecentDeploy != nil && h.RecentDeploy.Version != "" {
				fmt.Fprintf(&b, "; deploy %s @ %s", h.RecentDeploy.Version, pmTime(h.RecentDeploy.TimeUnixNs))
			}
			b.WriteString("\n")
		}
		if s := strings.TrimSpace(p.AISummary); s != "" {
			fmt.Fprintf(&b, "  AI özeti: %s\n", pmClamp(s, pmMaxSummaryRunes))
		}
	}
	return b.String()
}

// draftPostmortem — POST /api/copilot/draft-postmortem/{id}.
// requireCopilot + editör kapısı route'ta (ai_routes.go); handler'da
// inline kapı YASAK (TestNoInlineCopilotGates).
func (s *Server) draftPostmortem(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if id == "" {
		writeJSONError(w, http.StatusBadRequest, "incident id required")
		return
	}
	ctx := r.Context()
	inc, err := s.store.GetIncident(ctx, id)
	if err != nil {
		writeErr(w, err)
		return
	}
	if inc == nil {
		writeJSONError(w, http.StatusNotFound, "incident not found")
		return
	}

	// Kanıt okumaları SOFT-FAIL: timeline okunamadıysa paket "OKUNAMADI"
	// der ve taslak yine üretilir — kısmi kanıtla dürüst taslak, hiç
	// taslak olmamasından iyi (explain-log-patterns'ın iki-kaynak emsali).
	events, _ := s.store.IncidentTimeline(ctx, id)
	totalEvents := len(events)
	if len(events) > pmMaxTimelineEvents {
		events = events[:pmMaxTimelineEvents]
	}
	pids, _ := s.store.IncidentProblems(ctx, id)
	totalProblems := len(pids)
	if len(pids) > pmMaxProblems {
		pids = pids[:pmMaxProblems]
	}
	problems := make([]chstore.Problem, 0, len(pids))
	for _, pid := range pids {
		if p, err := s.store.GetProblem(ctx, pid); err == nil && p != nil {
			problems = append(problems, *p)
		}
	}
	hyps, _ := s.store.GetHypotheses(ctx, "problem", pids)

	evidence := renderPostmortemEvidence(pmEvidenceInput{
		Inc: *inc, Events: events, TotalEvents: totalEvents,
		Problems: problems, TotalProblems: totalProblems,
		Hyps: hyps, NowNs: time.Now().UnixNano(),
	})

	r, xid := withExchange(r)
	out, err := s.copilotExplain(r, copilot.SystemPromptPostmortem(), evidence)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, map[string]any{"draft": out, "exchangeId": xid})
}
