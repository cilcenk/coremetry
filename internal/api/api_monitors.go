package api

// Synthetic-monitor handlers (HTTP checks + heartbeats). Split out
// of api.go for code organisation (behaviour-preserving).

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"time"

	"github.com/cilcenk/coremetry/internal/chstore"
	"github.com/cilcenk/coremetry/internal/monitor"
)

// validateMonitor enforces the type whitelist and per-type required fields
// on create AND update. Returns a user-facing message (→ HTTP 400) or "".
// Kept in one place so the two handlers can't drift.
func validateMonitor(m *chstore.Monitor) string {
	if m.Name == "" {
		return "name required"
	}
	switch m.Type {
	case "http":
		if m.URL == "" {
			return "url required for http monitor"
		}
	case "heartbeat":
		// passive — no probe config to validate.
	case "tcp":
		if m.Target == "" {
			return "target (host:port) required for tcp monitor"
		}
		// No default port for tcp — the operator must name it. Also
		// range-checks the port so a typo fails here, not every probe.
		if _, err := monitor.NormalizeAddr(m.Target, ""); err != nil {
			return err.Error()
		}
	case "ssl-cert":
		if m.Target == "" {
			return "target (host[:443]) required for ssl-cert monitor"
		}
		if _, err := monitor.NormalizeAddr(m.Target, "443"); err != nil {
			return err.Error()
		}
		// CertWarnDays==0 is defaulted to 14 by UpsertMonitor; nothing to
		// reject (uint16 can't go negative). Threshold is thus always >0.
	case "keyword":
		if m.URL == "" {
			return "url required for keyword monitor"
		}
		if m.Keyword == "" {
			return "keyword required for keyword monitor"
		}
	default:
		return "type must be one of: http, tcp, ssl-cert, keyword, heartbeat"
	}
	return ""
}

func (s *Server) listMonitors(w http.ResponseWriter, r *http.Request) {
	monitors, err := s.store.ListMonitors(r.Context())
	if err != nil {
		writeErr(w, err)
		return
	}
	last, err := s.store.LastMonitorStatus(r.Context())
	if err != nil {
		writeErr(w, err)
		return
	}
	// Single rollup query for uptime % + avg latency over 1h/24h —
	// cheaper than the alternative (browser fetches 500-row
	// timelines per monitor and computes client-side). Empty map on
	// error so the list keeps rendering.
	stats, err := s.store.MonitorStatsAll(r.Context())
	if err != nil {
		log.Printf("[api] monitor stats: %v", err)
		stats = map[string]chstore.MonitorStats{}
	}
	// Combine definition + last status + 1h/24h rollups in the
	// response so the list page renders without a per-row roundtrip.
	type row struct {
		chstore.Monitor
		LastResult *chstore.MonitorResult `json:"lastResult,omitempty"`
		Stats      *chstore.MonitorStats  `json:"stats,omitempty"`
	}
	out := make([]row, 0, len(monitors))
	for _, m := range monitors {
		r := row{Monitor: m}
		if lr, ok := last[m.ID]; ok {
			r.LastResult = &lr
		}
		if st, ok := stats[m.ID]; ok {
			r.Stats = &st
		}
		out = append(out, r)
	}
	writeJSON(w, out)
}

func (s *Server) getMonitor(w http.ResponseWriter, r *http.Request) {
	m, err := s.store.GetMonitor(r.Context(), r.PathValue("id"))
	if err != nil {
		writeErr(w, err)
		return
	}
	if m == nil {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	writeJSON(w, m)
}

func (s *Server) createMonitor(w http.ResponseWriter, r *http.Request) {
	var m chstore.Monitor
	if err := json.NewDecoder(r.Body).Decode(&m); err != nil {
		http.Error(w, "invalid JSON: "+err.Error(), http.StatusBadRequest)
		return
	}
	if msg := validateMonitor(&m); msg != "" {
		http.Error(w, msg, http.StatusBadRequest)
		return
	}
	m.ID = "" // force new ID
	if err := s.store.UpsertMonitor(r.Context(), &m); err != nil {
		writeErr(w, err)
		return
	}
	s.audit(r, "monitor.create", "monitor", m.ID, fmt.Sprintf(`{"name":%q,"type":%q}`, m.Name, m.Type))
	// UpsertMonitor stamped the new id + heartbeat token onto m;
	// echo it back directly. Re-reading via FINAL would race the
	// MergeTree merge cycle and sometimes return null.
	writeJSON(w, m)
}

func (s *Server) updateMonitor(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	var m chstore.Monitor
	if err := json.NewDecoder(r.Body).Decode(&m); err != nil {
		http.Error(w, "invalid JSON: "+err.Error(), http.StatusBadRequest)
		return
	}
	if msg := validateMonitor(&m); msg != "" {
		http.Error(w, msg, http.StatusBadRequest)
		return
	}
	m.ID = id
	if err := s.store.UpsertMonitor(r.Context(), &m); err != nil {
		writeErr(w, err)
		return
	}
	s.audit(r, "monitor.update", "monitor", id, "")
	writeJSON(w, m)
}

func (s *Server) deleteMonitor(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if err := s.store.DeleteMonitor(r.Context(), id); err != nil {
		writeErr(w, err)
		return
	}
	s.audit(r, "monitor.delete", "monitor", id, "")
	writeJSON(w, map[string]string{"status": "deleted"})
}

func (s *Server) monitorTimeline(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	limit := parseInt(r.URL.Query().Get("limit"), 500)
	rows, err := s.store.MonitorTimeline(r.Context(), id, limit)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, rows)
}

// acceptHeartbeat is the unauth'd ingest endpoint. The token in the
// URL is matched against the heartbeat_token column on a monitor; if
// it matches, an "up" result is recorded AND any open Problem for that
// monitor is resolved synchronously (the runner only watches for
// absence so it never sees the up→down transition on its own).
func (s *Server) acceptHeartbeat(w http.ResponseWriter, r *http.Request) {
	token := r.PathValue("token")
	m, err := s.store.GetMonitorByToken(r.Context(), token)
	if err != nil {
		writeErr(w, err)
		return
	}
	if m == nil {
		// Don't leak whether the token is valid — same response shape
		// as a successful beat. Cheap defense against token enumeration.
		writeJSON(w, map[string]string{"status": "ok"})
		return
	}
	_ = s.store.InsertMonitorResult(r.Context(), chstore.MonitorResult{
		MonitorID: m.ID, Status: "up", Message: "heartbeat received",
	})
	// Auto-resolve any open Problem the runner opened for this monitor.
	// Runner ticks every 5s; without this synchronous resolution, the
	// alert would clear on the next tick (a 0-5s lag) AND no
	// notification would fire because runner rate-limits to state
	// changes only.
	if open, err := s.store.FindOpenProblem(r.Context(), "monitor:"+m.ID, m.Name); err == nil && open != nil {
		open.Status = "resolved"
		now := time.Now().UnixNano()
		open.ResolvedAt = &now
		_ = s.store.UpsertProblem(r.Context(), *open)
	}
	writeJSON(w, map[string]string{"status": "ok"})
}
