package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/cilcenk/coremetry/internal/auth"
	"github.com/cilcenk/coremetry/internal/chstore"
)

// Operator events HTTP surface (v0.5.476). Three endpoints —
// list (any signed-in role can see), create (editor+), delete
// (admin or creator). Each mutation writes an audit row so the
// /admin/audit page shows who marked what when.

func (s *Server) listEvents(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	f := chstore.EventFilter{
		From:    parseTime(q.Get("from")),
		To:      parseTime(q.Get("to")),
		Service: q.Get("service"),
		Kind:    q.Get("kind"),
		Limit:   parseInt(q.Get("limit"), 200),
	}
	// Short cache — events change on operator click + are read
	// by every time-series chart on the page; 15s smooths the
	// burst without serving stale-to-the-second markers.
	key := fmt.Sprintf("events:%d:%d:%s:%s:%d",
		f.From.Unix()/60, f.To.Unix()/60, f.Service, f.Kind, f.Limit)
	s.serveCached(w, r, key, 15*time.Second, func() (any, error) {
		evs, err := s.store.ListEvents(r.Context(), f)
		if err != nil {
			return nil, err
		}
		if evs == nil {
			evs = []chstore.Event{}
		}
		return evs, nil
	})
}

func (s *Server) createEvent(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Kind    string `json:"kind"`
		Label   string `json:"label"`
		Time    int64  `json:"time"` // unix ns; 0 = now
		Service string `json:"service"`
		Link    string `json:"link"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "invalid body: "+err.Error(), http.StatusBadRequest)
		return
	}
	defer r.Body.Close()
	if body.Label == "" {
		http.Error(w, "label required", http.StatusBadRequest)
		return
	}
	claims := auth.FromContext(r.Context())
	owner := ""
	if claims != nil {
		owner = claims.Email
	}
	ev := chstore.Event{
		Kind:    body.Kind,
		Label:   body.Label,
		Time:    body.Time,
		Service: body.Service,
		Link:    body.Link,
		Owner:   owner,
	}
	saved, err := s.store.UpsertEvent(r.Context(), ev)
	if err != nil {
		writeErr(w, err)
		return
	}
	s.audit(r, "event.create", "event", saved.ID,
		fmt.Sprintf(`{"kind":%q,"label":%q,"service":%q}`,
			saved.Kind, saved.Label, saved.Service))
	writeJSON(w, saved)
}

func (s *Server) deleteEvent(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if id == "" {
		http.Error(w, "id required", http.StatusBadRequest)
		return
	}
	if err := s.store.DeleteEvent(r.Context(), id); err != nil {
		writeErr(w, err)
		return
	}
	s.audit(r, "event.delete", "event", id, "")
	w.WriteHeader(http.StatusNoContent)
}
