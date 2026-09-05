package api

import "net/http"

// logs_routes.go — v0.10.281 (log-search audit §6.3): /api/logs rotaları
// api.go'dan TAŞINDI (yeni rota YOK; davranış birebir). Kayıt defter
// üzerinden (route_registry.go) — api.go bir daha büyümez.
//
// Taşıma ayrı commit: taşıma sırasında bir rota düşerse HTTP 404 değil,
// çoğu istemcide "boş sayfayla 200" görünür ve TestMuxRoutePatterns
// çakışmayı görür, EKSİKLİĞİ görmez. Bu yüzden logs_routes_test.go dokuz
// kalıbı ADLA pinler ve deploy sonrası curl listesi (scratchpad
// verify_logs_routes.sh) her ucu yoklar.
func init() { registerRoutesExtra("logs", (*Server).registerLogsRoutes) }

func (s *Server) registerLogsRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/logs", s.getLogs)
	// v0.9.1094 — Load more transport fix: ES PIT cursor'ı URL sınırını
	// aşabilir; liste POST gövdesiyle de sorulabilir (GET aynen durur).
	mux.HandleFunc("POST /api/logs/search", s.postLogsSearch)
	mux.HandleFunc("GET /api/logs/stream", s.streamLogs) // v0.8.x — live-tail SSE
	mux.HandleFunc("GET /api/logs/timeseries", s.getLogsTimeseries)
	mux.HandleFunc("GET /api/logs/fields", s.getLogsFields)
	// v0.8.255 — fields-panel accordion: top-5 values of one field
	// in the current window. Expand-triggered + 60s cached; single
	// bounded terms agg (ES) / capped GROUP BY (CH).
	mux.HandleFunc("GET /api/logs/fieldstats", s.getLogsFieldStats)
	// v0.5.464 — field-aware autocomplete on the /logs search
	// box. ES _terms_enum on keyword subfields for sub-ms prefix
	// lookups; CH backend returns [] (handler degrades silently).
	mux.HandleFunc("GET /api/logs/field-values", s.getLogsFieldValues)
	// v0.5.402 — surrounding context (±N logs around a pivot ts).
	// Datadog Context tab equivalent. Two parallel logstore.Search
	// calls (before / after — gerçekten paralel + sayaçsız v0.10.414) so
	// the operator sees what was emitted either side of the log.
	mux.HandleFunc("GET /api/logs/context", s.getLogsContext)
	// v0.5.244 — Drain-extracted log template ledger. Persistent
	// templates with sticky first_seen so the operator can ask
	// "what shape just started appearing?".
	mux.HandleFunc("GET /api/logs/templates", s.getLogsTemplates)
	// v0.10.296 — Dilim 2: pencere içi desenler (NormalizeSignature, örneklemeli).
	mux.HandleFunc("GET /api/logs/patterns", s.getLogsPatterns)
}
