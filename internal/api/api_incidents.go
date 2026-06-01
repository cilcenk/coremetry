package api

// Incident-management handlers. Split out of api.go for code
// organisation (behaviour-preserving). The actorOf() helper stays
// in api.go because putRetention + acknowledgeProblems use it too.

import (
	"encoding/json"
	"net/http"
	"time"

	"github.com/cilcenk/coremetry/internal/chstore"
)

func (s *Server) listIncidents(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	rows, err := s.store.ListIncidents(r.Context(), chstore.IncidentFilter{
		Status:   q.Get("status"),
		Service:  q.Get("service"),
		Severity: q.Get("severity"),
		Limit:    parseInt(q.Get("limit"), 200),
	})
	if err != nil {
		writeErr(w, err)
		return
	}
	rows = s.store.EnrichIncidentsWithClusters(r.Context(), rows, time.Hour)
	writeJSON(w, rows)
}

func (s *Server) getIncident(w http.ResponseWriter, r *http.Request) {
	inc, err := s.store.GetIncident(r.Context(), r.PathValue("id"))
	if err != nil {
		writeErr(w, err)
		return
	}
	if inc == nil {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	writeJSON(w, inc)
}

func (s *Server) createIncident(w http.ResponseWriter, r *http.Request) {
	var inc chstore.Incident
	if err := json.NewDecoder(r.Body).Decode(&inc); err != nil {
		http.Error(w, "invalid JSON: "+err.Error(), http.StatusBadRequest)
		return
	}
	if inc.Title == "" {
		http.Error(w, "title required", http.StatusBadRequest)
		return
	}
	inc.ID = ""
	if err := s.store.UpsertIncident(r.Context(), &inc); err != nil {
		writeErr(w, err)
		return
	}
	actor := actorOf(r)
	_ = s.store.AppendIncidentEvent(r.Context(), chstore.IncidentEvent{
		IncidentID: inc.ID, Kind: "created", Actor: actor,
		Body: "Manually created",
	})
	writeJSON(w, inc)
}

func (s *Server) updateIncident(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	var inc chstore.Incident
	if err := json.NewDecoder(r.Body).Decode(&inc); err != nil {
		http.Error(w, "invalid JSON: "+err.Error(), http.StatusBadRequest)
		return
	}
	inc.ID = id
	if err := s.store.UpsertIncident(r.Context(), &inc); err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, inc)
}

func (s *Server) ackIncident(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	inc, err := s.store.GetIncident(r.Context(), id)
	if err != nil {
		writeErr(w, err)
		return
	}
	if inc == nil {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	now := time.Now().UnixNano()
	inc.Status = "acknowledged"
	inc.AckAt = &now
	actor := actorOf(r)
	if inc.Assignee == "" {
		inc.Assignee = actor
	}
	if err := s.store.UpsertIncident(r.Context(), inc); err != nil {
		writeErr(w, err)
		return
	}
	_ = s.store.AppendIncidentEvent(r.Context(), chstore.IncidentEvent{
		IncidentID: id, Kind: "ack", Actor: actor, Body: "Incident acknowledged",
	})
	writeJSON(w, inc)
}

func (s *Server) resolveIncident(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	inc, err := s.store.GetIncident(r.Context(), id)
	if err != nil {
		writeErr(w, err)
		return
	}
	if inc == nil {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	now := time.Now().UnixNano()
	inc.Status = "resolved"
	inc.ResolvedAt = &now
	if err := s.store.UpsertIncident(r.Context(), inc); err != nil {
		writeErr(w, err)
		return
	}
	_ = s.store.AppendIncidentEvent(r.Context(), chstore.IncidentEvent{
		IncidentID: id, Kind: "resolved", Actor: actorOf(r), Body: "Incident resolved",
	})
	writeJSON(w, inc)
}

func (s *Server) addIncidentNote(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	var body struct {
		Text string `json:"text"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "invalid JSON: "+err.Error(), http.StatusBadRequest)
		return
	}
	if body.Text == "" {
		http.Error(w, "text required", http.StatusBadRequest)
		return
	}
	if err := s.store.AppendIncidentEvent(r.Context(), chstore.IncidentEvent{
		IncidentID: id, Kind: "note", Actor: actorOf(r), Body: body.Text,
	}); err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, map[string]string{"status": "ok"})
}

func (s *Server) incidentTimeline(w http.ResponseWriter, r *http.Request) {
	rows, err := s.store.IncidentTimeline(r.Context(), r.PathValue("id"))
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, rows)
}

func (s *Server) incidentProblems(w http.ResponseWriter, r *http.Request) {
	ids, err := s.store.IncidentProblems(r.Context(), r.PathValue("id"))
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, ids)
}
