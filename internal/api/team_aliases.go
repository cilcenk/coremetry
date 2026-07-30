package api

import (
	"context"
	"encoding/json"
	"net/http"

	"github.com/cilcenk/coremetry/internal/chstore"
)

// team_aliases.go — LDAP↔telemetri takım adı eşleme ayarları
// (v0.9.427, operatör istegi: "SY-Dijital Bankacılık" LDAP'ta,
// telemetride "dijitalsy"/"avengersy" — eşleme operatör tablosu).
// team_contacts ile aynı desen: settings blob + admin GET/PUT + audit.
// Tüketiciler: guided my_services/my_problems (servicesForUserTeam),
// inbox owner/SRE takım filtreleri (servicesForTeam + satır filtresi).

func (s *Server) getTeamAliases(w http.ResponseWriter, r *http.Request) {
	ta, err := s.store.GetTeamAliases(r.Context())
	if err != nil {
		writeErr(w, err)
		return
	}
	if ta.Aliases == nil {
		ta.Aliases = map[string]string{}
	}
	writeJSON(w, ta)
}

func (s *Server) putTeamAliases(w http.ResponseWriter, r *http.Request) {
	var body chstore.TeamAliases
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "invalid body", http.StatusBadRequest)
		return
	}
	if len(body.Aliases) > 500 {
		http.Error(w, "en fazla 500 alias", http.StatusBadRequest)
		return
	}
	if err := s.store.PutTeamAliases(r.Context(), body); err != nil {
		writeErr(w, err)
		return
	}
	details, _ := json.Marshal(map[string]any{"aliases": len(body.Aliases)})
	s.audit(r, "settings.teamaliases.update", "settings", chstore.TeamAliasesKey, string(details))
	writeJSON(w, body)
}

// teamAliasesCtx — istek-anlık okuma (tek küçük FINAL point-read; inbox
// sonucu zaten serveCached arkasında). Hata → boş tablo (eşleme yokmuş
// gibi davran — eski davranış).
func (s *Server) teamAliasesCtx(ctx context.Context) chstore.TeamAliases {
	ta, err := s.store.GetTeamAliases(ctx)
	if err != nil {
		return chstore.TeamAliases{}
	}
	return ta
}
