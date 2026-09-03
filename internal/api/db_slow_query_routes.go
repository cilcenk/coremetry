package api

// db_slow_query_routes.go — v0.10.325: yavaş SQL dedektörü ayarları
// (system_settings['db_slow_query']). Defterden kayıt (api.go büyümez).

import (
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/cilcenk/coremetry/internal/auth"
	"github.com/cilcenk/coremetry/internal/chstore"
)

func init() { registerRoutesExtra("db-slow-query", (*Server).registerDBSlowQueryRoutes) }

func (s *Server) registerDBSlowQueryRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/settings/db-slow-query", auth.RequireRole(auth.RoleAdmin, s.getDBSlowQuery))
	mux.HandleFunc("PUT /api/settings/db-slow-query", auth.RequireRole(auth.RoleAdmin, s.putDBSlowQuery))
}

func (s *Server) getDBSlowQuery(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, s.store.GetDBSlowQuery(r.Context()))
}

// validateDBSlowQuery — saf; sınırlar operatör hatasını erken yakalar.
func validateDBSlowQuery(c chstore.DBSlowQueryConfig) error {
	if c.ThresholdMs < 100 || c.ThresholdMs > 600000 {
		return fmt.Errorf("thresholdMs must be between 100 and 600000")
	}
	if c.CriticalMs < c.ThresholdMs || c.CriticalMs > 3600000 {
		return fmt.Errorf("criticalMs must be >= thresholdMs and <= 3600000")
	}
	if c.MinExecutions < 1 || c.MinExecutions > 1000000 {
		return fmt.Errorf("minExecutions must be between 1 and 1000000")
	}
	if c.ForBuckets < 1 || c.ForBuckets > 12 {
		return fmt.Errorf("forBuckets must be between 1 and 12")
	}
	if c.CooldownSec < 0 || c.CooldownSec > 86400 {
		return fmt.Errorf("cooldownSec must be between 0 and 86400")
	}
	return nil
}

func (s *Server) putDBSlowQuery(w http.ResponseWriter, r *http.Request) {
	var c chstore.DBSlowQueryConfig
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 16<<10)).Decode(&c); err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
		return
	}
	if err := validateDBSlowQuery(c); err != nil {
		writeJSONError(w, http.StatusBadRequest, err.Error())
		return
	}
	saved, err := s.store.SaveDBSlowQuery(r.Context(), c)
	if err != nil {
		writeErr(w, err)
		return
	}
	s.audit(r, "settings.db_slow_query.update", "settings", "db_slow_query",
		fmt.Sprintf(`{"enabled":%v,"thresholdMs":%v,"criticalMs":%v,"minExecutions":%d,"forBuckets":%d,"cooldownSec":%d}`,
			saved.Enabled, saved.ThresholdMs, saved.CriticalMs, saved.MinExecutions, saved.ForBuckets, saved.CooldownSec))
	writeJSON(w, saved)
}
