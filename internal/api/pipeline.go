package api

import (
	"encoding/json"
	"net/http"
	"strings"

	"github.com/cilcenk/coremetry/internal/auth"
	"github.com/cilcenk/coremetry/internal/pipeline"
)

// Pipeline admin handlers (v0.5.263). Drop / enrich rules
// applied at OTLP ingest before the sampler. All endpoints
// admin-gated; mutations write an audit row.

func (s *Server) listPipelineRules(w http.ResponseWriter, r *http.Request) {
	if s.pipeline == nil {
		writeJSON(w, map[string]any{"rules": []any{}})
		return
	}
	writeJSON(w, map[string]any{"rules": s.pipeline.Rules()})
}

func (s *Server) upsertPipelineRule(w http.ResponseWriter, r *http.Request) {
	if s.pipeline == nil {
		http.Error(w, `{"error":"pipeline engine not configured"}`, http.StatusServiceUnavailable)
		return
	}
	var body pipeline.Rule
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, `{"error":"invalid JSON: `+err.Error()+`"}`, http.StatusBadRequest)
		return
	}
	saved, err := s.pipeline.Upsert(r.Context(), s.store, body)
	if err != nil {
		http.Error(w, `{"error":"`+err.Error()+`"}`, http.StatusBadRequest)
		return
	}
	s.publishConfigReload(r.Context(), "pipeline")
	details, _ := json.Marshal(saved)
	s.audit(r, "pipeline.upsert", "rule", saved.ID, string(details))
	writeJSON(w, saved)
}

func (s *Server) deletePipelineRule(w http.ResponseWriter, r *http.Request) {
	if s.pipeline == nil {
		http.Error(w, `{"error":"pipeline engine not configured"}`, http.StatusServiceUnavailable)
		return
	}
	id := strings.TrimSpace(r.PathValue("id"))
	if id == "" {
		http.Error(w, `{"error":"id required"}`, http.StatusBadRequest)
		return
	}
	if err := s.pipeline.Delete(r.Context(), s.store, id); err != nil {
		writeErr(w, err)
		return
	}
	s.publishConfigReload(r.Context(), "pipeline")
	s.audit(r, "pipeline.delete", "rule", id, "")
	writeJSON(w, map[string]string{"status": "ok"})
}

// SetPipeline wires the engine. Always called from main(); the
// admin handlers nil-check so a misconfigured boot doesn't 500
// every request — they return 503 with a clear reason.
func (s *Server) SetPipeline(p *pipeline.Engine) {
	s.pipeline = p
}

// editorRolesOnly — re-using the helper alias is fine since
// these routes are admin-gated. Leaving a single auth.RoleAdmin
// wrap on each route keeps the registration site readable.
var _ = auth.RoleAdmin
