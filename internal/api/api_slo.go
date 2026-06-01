package api

// SLO read + CRUD handlers. Split out of api.go for code
// organisation (behaviour-preserving). The auto-create flow lives
// separately in slo_autocreate.go; shared helpers (writeJSON,
// writeErr, parseInt, parseDuration, newID, s.serveCached) stay in
// api.go.

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"time"

	"github.com/cilcenk/coremetry/internal/chstore"
)

func (s *Server) listSLOs(w http.ResponseWriter, r *http.Request) {
	out, err := s.store.ListSLOs(r.Context())
	if err != nil {
		writeErr(w, err)
		return
	}
	// For the list page, pre-compute status alongside each SLO so the UI
	// can show health badges without N round-trips.
	type row struct {
		chstore.SLO
		Status *chstore.SLOStatus `json:"status,omitempty"`
	}
	rows := make([]row, 0, len(out))
	for _, o := range out {
		st, err := s.store.ComputeSLOStatus(r.Context(), o)
		if err != nil {
			log.Printf("[slo] status %s: %v", o.ID, err)
		}
		rows = append(rows, row{SLO: o, Status: st})
	}
	writeJSON(w, rows)
}

func (s *Server) getSLO(w http.ResponseWriter, r *http.Request) {
	o, err := s.store.GetSLO(r.Context(), r.PathValue("id"))
	if err != nil {
		writeErr(w, err)
		return
	}
	if o == nil {
		http.Error(w, `{"error":"slo not found"}`, http.StatusNotFound)
		return
	}
	writeJSON(w, o)
}

func (s *Server) sloStatus(w http.ResponseWriter, r *http.Request) {
	o, err := s.store.GetSLO(r.Context(), r.PathValue("id"))
	if err != nil {
		writeErr(w, err)
		return
	}
	if o == nil {
		http.Error(w, `{"error":"slo not found"}`, http.StatusNotFound)
		return
	}
	st, err := s.store.ComputeSLOStatus(r.Context(), *o)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, st)
}

// sloForecast (v0.6.30) — burn-down projection. Cached 60s on
// (id, burnWindow) so /slos polling doesn't fan out per-row
// CH reads on every tab refresh. ComputeSLOForecast itself runs
// TWO short queries (status + short-window burn rate) — the
// cache wrapper collapses them across consecutive operator
// page-loads in a session.
func (s *Server) sloForecast(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	burnWindow := parseDuration(r.URL.Query().Get("window"), time.Hour)
	// Cap the burn window so a hallucinated ?window=720h doesn't
	// blow the budget on a backfill-shaped query.
	if burnWindow > 24*time.Hour {
		burnWindow = 24 * time.Hour
	}
	key := fmt.Sprintf("slo-forecast:%s:%s", id, burnWindow)
	s.serveCached(w, r, key, 60*time.Second, func() (any, error) {
		o, err := s.store.GetSLO(r.Context(), id)
		if err != nil {
			return nil, err
		}
		if o == nil {
			return nil, fmt.Errorf("slo not found")
		}
		return s.store.ComputeSLOForecast(r.Context(), *o, burnWindow)
	})
}

// sloBurnSeries serves the per-day burn-rate timeseries that
// drives the /slos sparkline (v0.5.150). Cached 60s on (id, days)
// — sparkline doesn't need real-time accuracy and the GROUP BY
// over a 7d service-slice is cheap but not free.
func (s *Server) sloBurnSeries(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	days := parseInt(r.URL.Query().Get("days"), 7)
	key := fmt.Sprintf("slo-burn-series:%s:%d", id, days)
	s.serveCached(w, r, key, 60*time.Second, func() (any, error) {
		o, err := s.store.GetSLO(r.Context(), id)
		if err != nil {
			return nil, err
		}
		if o == nil {
			return nil, fmt.Errorf("slo not found")
		}
		series, err := s.store.ComputeSLOBurnSeries(r.Context(), *o, days)
		if err != nil {
			return nil, err
		}
		if series == nil {
			series = []chstore.BurnPoint{}
		}
		return map[string]any{
			"series": series,
			"days":   days,
		}, nil
	})
}

func (s *Server) createSLO(w http.ResponseWriter, r *http.Request) {
	var o chstore.SLO
	if err := json.NewDecoder(r.Body).Decode(&o); err != nil {
		http.Error(w, "invalid body", http.StatusBadRequest)
		return
	}
	if o.Name == "" || o.Service == "" || o.SLIType == "" {
		http.Error(w, `{"error":"name, service and sliType required"}`, http.StatusBadRequest)
		return
	}
	if o.Target <= 0 || o.Target >= 1 {
		http.Error(w, `{"error":"target must be a fraction between 0 and 1 (e.g. 0.99)"}`, http.StatusBadRequest)
		return
	}
	if o.SLIType == chstore.SLITypeLatency && o.ThresholdMs <= 0 {
		http.Error(w, `{"error":"thresholdMs required for latency SLIs"}`, http.StatusBadRequest)
		return
	}
	o.ID = newID(8)
	if err := s.store.UpsertSLO(r.Context(), o); err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, o)
}

func (s *Server) deleteSLO(w http.ResponseWriter, r *http.Request) {
	if err := s.store.DeleteSLO(r.Context(), r.PathValue("id")); err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, map[string]string{"status": "ok"})
}
