package api

// db_statement_search_routes.go — v0.10.331: /alerts SQL arama seçici.
// GET /api/db/statements/search?q=…&limit=20[&service=] — son 24 saat, örnek SQL'de
// alt-dize (büyük/küçük harfsiz), yürütmeye göre sıralı. Kimlikli her rol
// (kural yazmak editör ister; aramak okuma).

import (
	"net/http"

	"github.com/cilcenk/coremetry/internal/auth"
)

func init() { registerRoutesExtra("db-statement-search", (*Server).registerDBStatementSearchRoutes) }

func (s *Server) registerDBStatementSearchRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/db/statements/search", auth.RequireAnyRole([]string{auth.RoleAdmin, auth.RoleEditor, auth.RoleViewer}, s.searchDBStatements))
}

func (s *Server) searchDBStatements(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	// v0.10.515 — ?service= kuralın servisi (isteğe bağlı kapsam).
	rows, err := s.store.SearchStatements(r.Context(), q.Get("q"), q.Get("service"), parseInt(q.Get("limit"), 20))
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, map[string]any{"rows": rows})
}
