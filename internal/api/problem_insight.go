package api

// problem_insight.go — v0.10.562 (CoSRE Faz 5b; operatör mockup onayı 2026-09-08).
// Problem detayının ÜSTÜNE deterministik tek satırlık insight şeridi: sormadan
// dolar, DDL yok, LLM yok. Dört hücre — etkilenen servis (hipotez baş şüphelisi
// + güven), ilk anomali (anomali olayları: problem açılışından 30 dk önce → 5 dk
// sonra penceresinde en erken), en yakın rollout (hipotezin DeepEvidence
// rollouts'undan, açılışa |Δt| en küçük), benzer geçmiş (aynı servis+kural
// çözülmüş; sayı, son süre, kim aldı). Hücre bilinmiyorsa null: şerit "—"
// basar, sebep uydurmaz.
//
//   GET /api/problems/{id}/insight   (viewer görür; serveCached 30 s)
//
// api.go BÜYÜMEZ: route_registry defteri.

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/cilcenk/coremetry/internal/chstore"
)

func init() { registerRoutesExtra("problem-insight", (*Server).registerProblemInsightRoutes) }

func (s *Server) registerProblemInsightRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/problems/{id}/insight", s.getProblemInsight)
}

const (
	insightAnomalyLookback  = 30 * time.Minute
	insightAnomalyLookahead = 5 * time.Minute
	insightSimilarMax       = 20
	insightTTL              = 30 * time.Second
)

type insightAnomaly struct {
	At      int64  `json:"at"` // unix ns
	Kind    string `json:"kind"`
	Service string `json:"service"`
}

type insightRollout struct {
	Workload   string `json:"workload"`
	Version    string `json:"version"`
	TimeUnixNs int64  `json:"timeUnixNs"`
	Cluster    string `json:"cluster,omitempty"`
	Namespace  string `json:"namespace,omitempty"`
	MatchedBy  string `json:"matchedBy,omitempty"`
	Band       string `json:"band,omitempty"`
	DeltaS     int64  `json:"deltaS"` // rollout − problem açılışı (s); negatif = önce
}

type insightSimilar struct {
	Count          int    `json:"count"`
	LastID         string `json:"lastId"`
	LastResolvedAt int64  `json:"lastResolvedAt"`
	LastDurationS  int64  `json:"lastDurationS"`
	LastAssignee   string `json:"lastAssignee,omitempty"`
}

type problemInsight struct {
	ProblemID          string          `json:"problemId"`
	Service            string          `json:"service"`
	Kind               string          `json:"kind"`
	Status             string          `json:"status"`
	StartedAt          int64           `json:"startedAt"`
	HypothesisComputed bool            `json:"hypothesisComputed"`
	TopSuspect         string          `json:"topSuspect,omitempty"`
	Confidence         float64         `json:"confidence,omitempty"`
	FirstAnomaly       *insightAnomaly `json:"firstAnomaly"`
	Rollout            *insightRollout `json:"rollout"`
	Similar            *insightSimilar `json:"similar"`
	Note               string          `json:"note"`
}

func problemInsightKey(id, status string) string {
	return fmt.Sprintf("problem-insight:v1:%s:%s", id, status)
}

// buildProblemInsight — SAF (test pinli). events: servise ait anomali olayları
// (herhangi sırada); similar: çözülmüş geçmiş (yeni→eski, anchor dahil olabilir).
func buildProblemInsight(p chstore.Problem, h *chstore.RootCauseHypothesis, events []chstore.AnomalyEvent, similar []chstore.Problem) problemInsight {
	out := problemInsight{ProblemID: p.ID, Service: p.Service, Kind: p.Kind, Status: p.Status, StartedAt: p.StartedAt}
	var notes []string
	if h != nil {
		out.HypothesisComputed = true
		out.TopSuspect, out.Confidence = h.TopSuspect, h.Confidence
		if h.Deep != nil && len(h.Deep.Rollouts) > 0 {
			best, bestD := h.Deep.Rollouts[0], int64(-1)
			for _, r := range h.Deep.Rollouts {
				d := r.StartedAtNs - p.StartedAt
				if d < 0 {
					d = -d
				}
				if bestD < 0 || d < bestD {
					best, bestD = r, d
				}
			}
			out.Rollout = &insightRollout{Workload: best.Workload, Version: firstNonEmpty(best.ImageTag, best.Revision), TimeUnixNs: best.StartedAtNs,
				Cluster: best.ClusterID, Namespace: best.Namespace, MatchedBy: best.MatchedBy, Band: best.Band, DeltaS: (best.StartedAtNs - p.StartedAt) / 1e9}
		}
	} else {
		notes = append(notes, "hipotez henüz hesaplanmadı")
	}
	lo, hi := p.StartedAt-int64(insightAnomalyLookback), p.StartedAt+int64(insightAnomalyLookahead)
	for _, e := range events {
		if e.StartedAt < lo || e.StartedAt > hi {
			continue
		}
		if out.FirstAnomaly == nil || e.StartedAt < out.FirstAnomaly.At {
			out.FirstAnomaly = &insightAnomaly{At: e.StartedAt, Kind: e.Kind, Service: e.Service}
		}
	}
	sim := insightSimilar{}
	for _, q := range similar {
		if q.ID == p.ID || q.Status != "resolved" {
			continue
		}
		sim.Count++
		if sim.LastID == "" {
			sim.LastID, sim.LastAssignee = q.ID, q.Assignee
			if q.ResolvedAt != nil {
				sim.LastResolvedAt = *q.ResolvedAt
				if *q.ResolvedAt > q.StartedAt {
					sim.LastDurationS = (*q.ResolvedAt - q.StartedAt) / 1e9
				}
			}
		}
	}
	if sim.Count > 0 {
		out.Similar = &sim
	}
	if out.Rollout == nil {
		notes = append(notes, "açılışa yakın rollout yok")
	}
	if out.FirstAnomaly == nil {
		notes = append(notes, "açılış penceresinde anomali olayı yok")
	}
	if out.Similar == nil {
		notes = append(notes, "aynı servis+kuralda çözülmüş geçmiş yok")
	}
	out.Note = strings.Join(notes, "; ")
	return out
}

func firstNonEmpty(a, b string) string {
	if strings.TrimSpace(a) != "" {
		return a
	}
	return b
}

// getProblemInsight — GET /api/problems/{id}/insight
func (s *Server) getProblemInsight(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimSpace(r.PathValue("id"))
	if id == "" {
		writeJSONError(w, http.StatusBadRequest, "problem id required")
		return
	}
	p, err := s.store.GetProblem(r.Context(), id)
	if err != nil {
		writeErr(w, err)
		return
	}
	if p == nil || p.ID == "" {
		writeJSONError(w, http.StatusNotFound, "problem not found")
		return
	}
	prob := *p
	s.serveCached(w, r, problemInsightKey(prob.ID, prob.Status), insightTTL, func(ctx context.Context) (any, error) {
		h, _ := s.store.GetHypothesis(ctx, "problem", prob.ID) // yoksa nil → hücre "—"
		var events []chstore.AnomalyEvent
		if prob.Service != "" {
			events, _ = s.store.ListAnomalyEvents(ctx, chstore.ListAnomalyEventsFilter{
				SinceNs: prob.StartedAt - int64(insightAnomalyLookback), Services: []string{prob.Service}, Limit: 100,
			})
		}
		var similar []chstore.Problem
		if prob.RuleID != "" && prob.Service != "" {
			similar, _ = s.store.FindSimilarResolvedProblems(ctx, prob.Service, prob.RuleID, insightSimilarMax)
		}
		return buildProblemInsight(prob, h, events, similar), nil
	})
}
