// Package api — service dependency contracts (v0.5.191).
// Admin-only read/write endpoints + the evaluator surface that
// drives /admin/contracts. See chstore/service_contracts.go for
// the data model + evaluator semantics.
package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/cilcenk/coremetry/internal/auth"
	"github.com/cilcenk/coremetry/internal/chstore"
)

func (s *Server) listServiceContracts(w http.ResponseWriter, r *http.Request) {
	rows, err := s.store.ListServiceContracts(r.Context())
	if err != nil {
		writeErr(w, err)
		return
	}
	if rows == nil {
		rows = []chstore.ServiceContract{}
	}
	writeJSON(w, rows)
}

func (s *Server) upsertServiceContract(w http.ResponseWriter, r *http.Request) {
	var c chstore.ServiceContract
	if err := json.NewDecoder(r.Body).Decode(&c); err != nil {
		http.Error(w, "invalid JSON: "+err.Error(), http.StatusBadRequest)
		return
	}
	claims := auth.FromContext(r.Context())
	if claims != nil && c.CreatedBy == "" {
		c.CreatedBy = claims.Email
	}
	if err := s.store.UpsertServiceContract(r.Context(), &c); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	s.audit(r, "service_contract.upsert", "service_contract", c.ID,
		fmt.Sprintf(`{"service":%q,"rule":%q,"target":%q}`,
			c.Service, c.RuleType, c.TargetService))
	writeJSON(w, c)
}

func (s *Server) deleteServiceContract(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if id == "" {
		http.Error(w, "id required", http.StatusBadRequest)
		return
	}
	if err := s.store.DeleteServiceContract(r.Context(), id); err != nil {
		writeErr(w, err)
		return
	}
	s.audit(r, "service_contract.delete", "service_contract", id, "")
	writeJSON(w, map[string]string{"status": "deleted"})
}

// getContractViolations runs the evaluator against the
// recent-window topology and returns violations. Cached 30s so
// the admin page refreshing the violations list doesn't hammer
// the GROUP BY on topology_edges_5m. The underlying read is
// already cheap (~10s execution-time guard, partition-pruned)
// but at 50+ contracts × N operators the cache matters.
func (s *Server) getContractViolations(w http.ResponseWriter, r *http.Request) {
	windowMin := parseInt(r.URL.Query().Get("windowMinutes"), 30)
	key := fmt.Sprintf("contract-violations:win=%d", windowMin)
	s.serveCached(w, r, key, 30*time.Second, func() (any, error) {
		violations, err := s.store.EvaluateServiceContracts(r.Context(), windowMin)
		if err != nil {
			return nil, err
		}
		if violations == nil {
			violations = []chstore.ContractViolation{}
		}
		return violations, nil
	})
}
