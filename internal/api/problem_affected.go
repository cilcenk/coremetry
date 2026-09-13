package api

// problem_affected.go — v0.10.707 (Dynatrace paritesi #5, dilim 2):
//
//	GET /api/problems/{id}/affected
//
// Davis'in "affected entities" listesi: problem öznesinin blast-radius
// ÇAĞIRANLARI (service_callers_5m, ≤25, çağrıya göre) ∪ kök-neden hipotezinin
// etkilenen POD'ları (Deep.AffectedPods; yalnız derin soruşturma koştuysa) ∪
// k8s CLUSTER'lar (okuma-anı Clusters). Liste ucunda DEĞİL (200 satır ×
// blast radius); yalnız çekmece/detay, aç-üzerine-getir. Servissiz problem
// (exception-storm, watcher, self-*) → boş liste, dürüst. {id} "P-…"
// görüntü kimliğini de kabul eder. serveCached 60 s (blast-radius ile aynı).
// Rol kapısı yok — viewer görür. api.go BÜYÜMEZ: route defteri.

import (
	"context"
	"net/http"
	"sort"
	"time"

	"github.com/cilcenk/coremetry/internal/chstore"
)

func init() { registerRoutesExtra("problem-affected", (*Server).registerProblemAffectedRoutes) }

func (s *Server) registerProblemAffectedRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/problems/{id}/affected", s.getProblemAffected)
}

// affectedEntity — tek satır. Kind: service | pod | cluster.
type affectedEntity struct {
	Kind           string  `json:"kind"`
	ID             string  `json:"id"`
	Calls          uint64  `json:"calls,omitempty"`
	Errors         uint64  `json:"errors,omitempty"`
	ErrorRate      float64 `json:"errorRate,omitempty"`
	HasOpenProblem bool    `json:"hasOpenProblem,omitempty"`
	Count          int     `json:"count,omitempty"` // pod: hipotez sayımı
}

const affectedCallersMax = 25

// buildAffectedEntities — SAF: çağıranlar (çağrı desc, ≤25) → pod'lar (sayım
// desc) → cluster'lar; tekrarsız (kind+id); özne servisin kendisi listeye
// girmez. Girdiler nil olabilir.
func buildAffectedEntities(subject string, br *chstore.BlastRadius, pods []chstore.PodHit, clusters []string) []affectedEntity {
	out := []affectedEntity{}
	seen := map[string]bool{}
	add := func(e affectedEntity) {
		k := e.Kind + "|" + e.ID
		if e.ID == "" || seen[k] || (e.Kind == "service" && e.ID == subject) {
			return
		}
		seen[k] = true
		out = append(out, e)
	}
	if br != nil {
		callers := append([]chstore.BlastRadiusCaller(nil), br.Callers...)
		sort.SliceStable(callers, func(i, j int) bool { return callers[i].Calls > callers[j].Calls })
		if len(callers) > affectedCallersMax {
			callers = callers[:affectedCallersMax]
		}
		for _, c := range callers {
			add(affectedEntity{Kind: "service", ID: c.Service, Calls: c.Calls, Errors: c.Errors, ErrorRate: c.ErrorRate, HasOpenProblem: c.HasOpenProblem})
		}
	}
	ps := append([]chstore.PodHit(nil), pods...)
	sort.SliceStable(ps, func(i, j int) bool { return ps[i].Count > ps[j].Count })
	for _, p := range ps {
		add(affectedEntity{Kind: "pod", ID: p.Pod, Count: p.Count})
	}
	for _, c := range clusters {
		add(affectedEntity{Kind: "cluster", ID: c})
	}
	return out
}

// affectedWindow — SAF: [onset−1h, çözüm|şimdi], en çok 24 s.
func affectedWindow(p chstore.Problem, now time.Time) (from, to time.Time) {
	to = now
	if p.ResolvedAt != nil && *p.ResolvedAt > 0 {
		to = time.Unix(0, *p.ResolvedAt)
	}
	from = time.Unix(0, p.StartedAt).Add(-time.Hour)
	if p.StartedAt <= 0 || from.After(to) {
		from = to.Add(-time.Hour)
	}
	if to.Sub(from) > 24*time.Hour {
		from = to.Add(-24 * time.Hour)
	}
	return from, to
}

func (s *Server) getProblemAffected(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if id == "" {
		writeJSONError(w, http.StatusBadRequest, "problem id required")
		return
	}
	s.serveCached(w, r, "problem:affected:v1:"+id, 60*time.Second, func(ctx context.Context) (any, error) {
		var p *chstore.Problem
		var err error
		if chstore.IsProblemDisplayID(id) {
			p, err = s.store.GetProblemByDisplayID(ctx, id)
		} else {
			p, err = s.store.GetProblem(ctx, id)
		}
		if err != nil {
			return nil, err
		}
		if p == nil {
			return nil, notFoundf("problem %q", id)
		}
		from, to := affectedWindow(*p, time.Now())
		var br *chstore.BlastRadius
		if p.Service != "" && (p.Kind == "" || p.Kind == chstore.ProblemKindService) {
			if b, berr := s.store.GetServiceBlastRadius(ctx, p.Service, from, to); berr == nil {
				br = &b
			}
		}
		var pods []chstore.PodHit
		if h, herr := s.store.GetHypothesis(ctx, "problem", p.ID); herr == nil && h != nil && h.Deep != nil {
			pods = h.Deep.AffectedPods
		}
		probs := s.store.EnrichProblemsWithClusters(ctx, []chstore.Problem{*p}, time.Hour)
		ents := buildAffectedEntities(p.Service, br, pods, probs[0].Clusters)
		return map[string]any{
			"problemId": p.ID, "displayId": chstore.ProblemDisplayID(p.ID), "service": p.Service,
			"windowFromNs": from.UnixNano(), "windowToNs": to.UnixNano(),
			"entities": ents, "total": len(ents),
		}, nil
	})
}
