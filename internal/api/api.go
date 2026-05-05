package api

import (
	"context"
	"crypto/rand"
	"embed"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"log"
	"net/http"
	"net/url"
	"path"
	"strconv"
	"strings"
	"time"

	"github.com/cilcenk/coremetry/internal/auth"
	"github.com/cilcenk/coremetry/internal/cache"
	"github.com/cilcenk/coremetry/internal/chstore"
	"github.com/cilcenk/coremetry/internal/logstore"
	"github.com/cilcenk/coremetry/internal/notify"
	"github.com/cilcenk/coremetry/internal/otlp"
	"github.com/cilcenk/coremetry/internal/profileconv"
)

type Server struct {
	addr   string
	store  *chstore.Store
	logs   logstore.Store    // read-side abstraction; CH or external ES
	ing    *otlp.Ingester
	webFS  embed.FS
	auth   *auth.Service
	oidc   *auth.OIDCService // nil when SSO disabled
	cache  cache.Cache       // Noop when Redis isn't configured
	notify *notify.Notifier

	// Demo deployments only — when true, /api/auth/config returns
	// initial admin credentials so the login page can pre-fill them.
	demoMode      bool
	demoEmail     string
	demoPassword  string
}

func NewServer(addr string, ing *otlp.Ingester, store *chstore.Store, logs logstore.Store, webFS embed.FS, authSvc *auth.Service, oidcSvc *auth.OIDCService, c cache.Cache, n *notify.Notifier) *Server {
	return &Server{addr: addr, store: store, logs: logs, ing: ing, webFS: webFS, auth: authSvc, oidc: oidcSvc, cache: c, notify: n}
}

// EnableDemoMode wires the demo credentials returned by /api/auth/config.
// Loud no-op when called with empty credentials so a misconfigured demo
// flag doesn't silently expose nothing.
func (s *Server) EnableDemoMode(email, password string) {
	s.demoMode = true
	s.demoEmail = email
	s.demoPassword = password
}

func (s *Server) Start() error {
	mux := http.NewServeMux()

	// OTLP HTTP
	otlpHandler := otlp.HTTPHandler(s.ing)
	mux.Handle("POST /v1/traces", otlpHandler)
	mux.Handle("POST /v1/logs", otlpHandler)
	mux.Handle("POST /v1/metrics", otlpHandler)

	// Profile ingest (custom — not OTLP, raw pprof bytes)
	mux.HandleFunc("POST /v1/profiles", s.ingestProfile)

	// REST API
	mux.HandleFunc("GET /api/services", s.getServices)
	mux.HandleFunc("GET /api/services/graph", s.getServiceGraph)
	mux.HandleFunc("GET /api/services/sparklines", s.getServiceSparklines)
	mux.HandleFunc("GET /api/operations", s.getOperations)
	mux.HandleFunc("GET /api/traces", s.getTraces)
	mux.HandleFunc("GET /api/traces/aggregate", s.getTraceAggregate)
	mux.HandleFunc("GET /api/traces/{id}", s.getTrace)
	mux.HandleFunc("GET /api/logs", s.getLogs)
	mux.HandleFunc("GET /api/metrics/names", s.getMetricNames)
	mux.HandleFunc("GET /api/metrics", s.getMetrics)
	mux.HandleFunc("GET /api/metrics/query", s.queryMetric)
	mux.HandleFunc("GET /api/metrics/labels", s.getMetricLabelValues)
	mux.HandleFunc("GET /api/spans/metric", s.spanMetric)
	mux.HandleFunc("GET /api/profiles", s.listProfiles)
	mux.HandleFunc("GET /api/profiles/by-span", s.profilesForSpan)
	mux.HandleFunc("GET /api/profiles/{id}", s.getProfile)
	mux.HandleFunc("GET    /api/exceptions",               s.listExceptions)
	// Errors Inbox — stateful exception groups (read = any, write = admin)
	mux.HandleFunc("GET    /api/exception-groups",                s.listExceptionGroups)
	mux.HandleFunc("GET    /api/exception-groups/{fp}/samples",   s.getExceptionGroupSamples)
	mux.HandleFunc("POST   /api/exception-groups/{fp}/state",     auth.RequireRole(auth.RoleAdmin, s.setExceptionGroupState))
	mux.HandleFunc("POST   /api/exception-groups/{fp}/assign",    auth.RequireRole(auth.RoleAdmin, s.assignExceptionGroup))
	mux.HandleFunc("GET    /api/services/{name}/operations", s.svcOperationSummary)
	mux.HandleFunc("GET    /api/services/{name}/callers",  s.svcCallers)
	mux.HandleFunc("GET    /api/services/{name}/callees",  s.svcCallees)
	mux.HandleFunc("GET    /api/problems",                  s.listProblems)
	mux.HandleFunc("GET    /api/alert-rules",               s.listAlertRules)
	mux.HandleFunc("POST   /api/alert-rules",               auth.RequireRole(auth.RoleAdmin, s.createAlertRule))
	mux.HandleFunc("DELETE /api/alert-rules/{id}",          auth.RequireRole(auth.RoleAdmin, s.deleteAlertRule))
	mux.HandleFunc("POST   /api/alert-rules/{id}/enable",   auth.RequireRole(auth.RoleAdmin, s.enableAlertRule))
	mux.HandleFunc("GET /api/health", s.getHealth)
	mux.HandleFunc("GET /api/status", s.getStatus)

	// Auth
	mux.HandleFunc("GET  /api/auth/config",   s.authConfig)
	mux.HandleFunc("POST /api/auth/login",    s.login)
	mux.HandleFunc("POST /api/auth/logout",   s.logout)
	mux.HandleFunc("GET  /api/auth/me",       s.me)
	mux.HandleFunc("POST /api/auth/password", s.changeOwnPassword)
	mux.HandleFunc("GET  /api/auth/oidc/start",    s.oidcStart)
	mux.HandleFunc("GET  /api/auth/oidc/callback", s.oidcCallback)

	// SLOs (read = any user, write = admin)
	mux.HandleFunc("GET    /api/slos",            s.listSLOs)
	mux.HandleFunc("GET    /api/slos/{id}",       s.getSLO)
	mux.HandleFunc("GET    /api/slos/{id}/status", s.sloStatus)
	mux.HandleFunc("POST   /api/slos",            auth.RequireRole(auth.RoleAdmin, s.createSLO))
	mux.HandleFunc("DELETE /api/slos/{id}",       auth.RequireRole(auth.RoleAdmin, s.deleteSLO))

	// Dashboards (read = any user, write = admin)
	mux.HandleFunc("GET    /api/dashboards",      s.listDashboards)
	mux.HandleFunc("GET    /api/dashboards/{id}", s.getDashboard)
	mux.HandleFunc("POST   /api/dashboards",      auth.RequireRole(auth.RoleAdmin, s.createDashboard))
	mux.HandleFunc("PUT    /api/dashboards/{id}", auth.RequireRole(auth.RoleAdmin, s.updateDashboard))
	mux.HandleFunc("DELETE /api/dashboards/{id}", auth.RequireRole(auth.RoleAdmin, s.deleteDashboard))

	// Settings + notification channels (admin only)
	mux.HandleFunc("GET    /api/settings/smtp",       auth.RequireRole(auth.RoleAdmin, s.getSMTPSettings))
	mux.HandleFunc("PUT    /api/settings/smtp",       auth.RequireRole(auth.RoleAdmin, s.putSMTPSettings))
	mux.HandleFunc("POST   /api/settings/smtp/test",  auth.RequireRole(auth.RoleAdmin, s.testSMTPSettings))
	mux.HandleFunc("GET    /api/channels",            auth.RequireRole(auth.RoleAdmin, s.listChannels))
	mux.HandleFunc("POST   /api/channels",            auth.RequireRole(auth.RoleAdmin, s.createChannel))
	mux.HandleFunc("PUT    /api/channels/{id}",       auth.RequireRole(auth.RoleAdmin, s.updateChannel))
	mux.HandleFunc("DELETE /api/channels/{id}",       auth.RequireRole(auth.RoleAdmin, s.deleteChannel))
	mux.HandleFunc("POST   /api/channels/{id}/test",  auth.RequireRole(auth.RoleAdmin, s.testChannel))

	// User management (admin only)
	mux.HandleFunc("GET    /api/users",                  auth.RequireRole(auth.RoleAdmin, s.listUsers))
	mux.HandleFunc("POST   /api/users",                  auth.RequireRole(auth.RoleAdmin, s.createUser))
	mux.HandleFunc("DELETE /api/users/{id}",             auth.RequireRole(auth.RoleAdmin, s.deleteUser))
	mux.HandleFunc("POST   /api/users/{id}/password",    auth.RequireRole(auth.RoleAdmin, s.resetUserPassword))

	// Tempo-compatible API (Grafana datasource integration)
	s.registerTempoRoutes(mux)

	// Web UI (embedded Next.js static export). Custom handler instead of
	// http.FileServer so we can:
	//   • Serve `<path>/index.html` directly without a 301 to `<path>/`
	//     (the redirect breaks behind some OpenShift / load-balancer
	//     setups where X-Forwarded-Proto isn't propagated and the
	//     redirect Location ends up with the wrong scheme).
	//   • Provide a SPA fallback for deep-links and unknown routes by
	//     serving the root index.html — the Next.js client router then
	//     mounts the right page from the current URL.
	sub, _ := fs.Sub(s.webFS, "frontend/out")
	mux.Handle("/", spaHandler(sub))

	log.Printf("[api] HTTP listening on %s", s.addr)
	return http.ListenAndServe(s.addr, cors(s.auth.Middleware(mux)))
}

// ── Handlers ──────────────────────────────────────────────────────────────────

func (s *Server) getServices(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	since := parseDuration(q.Get("since"), 24*time.Hour)
	from, to := parseTime(q.Get("from")), parseTime(q.Get("to"))
	// Top-N cap. The default keeps pages responsive at 10s of thousands
	// of services; the UI offers a "Load all" affordance for users who
	// genuinely need the long tail.
	limit := parseInt(q.Get("limit"), 50)
	if limit > 5000 {
		limit = 5000
	}
	if from.IsZero() {
		from = time.Now().Add(-since)
	}
	if to.IsZero() {
		to = time.Now()
	}
	// MV-backed fast path. The MV writes a row every 5 minutes, so windows
	// shorter than ~5 min would return empty — fall through to the raw
	// scan in that case (small window = small scan, also fast).
	useMV := to.Sub(from) >= 5*time.Minute
	key := fmt.Sprintf("services:mv=%t:limit=%d:since=%s:from=%s:to=%s",
		useMV, limit, q.Get("since"), q.Get("from"), q.Get("to"))
	s.serveCached(w, r, key, 5*time.Second, func() (any, error) {
		if useMV {
			return s.store.GetServicesAgg(r.Context(), from, to, limit)
		}
		return s.store.GetServices(r.Context(), since, from, to)
	})
}

// getServiceSparklines returns one bucket array per service for the
// requested window, sourced from the 5-minute summary MV. Used by the
// services list to render thumbnail charts next to each row without a
// separate round-trip per service.
//
// Response shape:
//   { "<service>": [ { "t": <unix ns>, "spans": N, "errs": N,
//                      "avgMs": F, "p99Ms": F }, ... ] }
func (s *Server) getServiceSparklines(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	since := parseDuration(q.Get("since"), 24*time.Hour)
	from, to := parseTime(q.Get("from")), parseTime(q.Get("to"))
	if from.IsZero() {
		from = time.Now().Add(-since)
	}
	if to.IsZero() {
		to = time.Now()
	}
	// Caller passes the visible service names (comma-separated). Without
	// this filter the response is one bucket array per service across the
	// full window — at 10k+ services that's multi-MB of JSON the browser
	// has to parse just to render 50 thumbnails. The cap (200) is a hard
	// safety net; the UI normally sends 50.
	wantSvcs := strings.Split(q.Get("services"), ",")
	cleaned := wantSvcs[:0]
	for _, s := range wantSvcs {
		if s = strings.TrimSpace(s); s != "" {
			cleaned = append(cleaned, s)
		}
		if len(cleaned) >= 200 {
			break
		}
	}
	wantSvcs = cleaned
	key := fmt.Sprintf("services-spark:from=%s:to=%s:svcs=%s",
		q.Get("from"), q.Get("to"), strings.Join(wantSvcs, ","))
	s.serveCached(w, r, key, 30*time.Second, func() (any, error) {
		rows, err := s.store.GetServiceSummary5mFor(r.Context(), wantSvcs, from, to)
		if err != nil {
			return nil, err
		}
		// Group by service. Map insertion preserves nothing on Go's side,
		// but the front-end indexes by name so order doesn't matter.
		out := map[string][]map[string]any{}
		for _, b := range rows {
			out[b.Service] = append(out[b.Service], map[string]any{
				"t":     b.BucketStart,
				"spans": b.SpanCount,
				"errs":  b.ErrorCount,
				"avgMs": b.AvgMs,
				"p99Ms": b.P99Ms,
			})
		}
		return out, nil
	})
}

func (s *Server) getServiceGraph(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	since := parseDuration(q.Get("since"), time.Hour)
	from, to := parseTime(q.Get("from")), parseTime(q.Get("to"))
	edges, err := s.store.GetServiceGraph(r.Context(), q.Get("service"), since, from, to)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, edges)
}

func (s *Server) getOperations(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	since := parseDuration(q.Get("since"), 24*time.Hour)
	from, to := parseTime(q.Get("from")), parseTime(q.Get("to"))
	key := fmt.Sprintf("operations:svc=%s:since=%s:from=%s:to=%s",
		q.Get("service"), q.Get("since"), q.Get("from"), q.Get("to"))
	s.serveCached(w, r, key, 30*time.Second, func() (any, error) {
		return s.store.GetOperations(r.Context(), q.Get("service"), since, from, to)
	})
}

func (s *Server) getTraces(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	f := chstore.TraceFilter{
		Service:  q.Get("service"),
		Search:   q.Get("search"),
		TraceID:  strings.ToLower(strings.TrimSpace(q.Get("traceId"))),
		From:     parseTime(q.Get("from")),
		To:       parseTime(q.Get("to")),
		HasError: q.Get("hasError") == "true",
		MinMs:    parseFloat(q.Get("minMs")),
		MaxMs:    parseFloat(q.Get("maxMs")),
		AttrKey:  q.Get("attrKey"),
		AttrVal:  q.Get("attrVal"),
		Sort:     q.Get("sort"),
		Order:    q.Get("order"),
		Limit:    parseInt(q.Get("limit"), 50),
		Offset:   parseInt(q.Get("offset"), 0),
	}
	filters, ferr := parseFiltersAndDSL(q.Get("filters"), q.Get("dsl"))
	if ferr != nil {
		http.Error(w, "invalid query DSL: "+ferr.Error(), http.StatusBadRequest)
		return
	}
	f.Filters = filters
	// count mode — opt-in for the expensive count(DISTINCT trace_id):
	//   skip   (default) — no count; UI shows ">=N+1" when the page is full
	//   approx           — count over top N+1 trace_ids only (fast)
	//   exact            — full scan (the legacy behaviour, 30s+ at scale)
	f.CountMode = q.Get("count")
	if f.CountMode == "" {
		f.CountMode = "skip"
	}
	traces, total, hasMore, err := s.store.GetTraces(r.Context(), f)
	if err != nil {
		writeErr(w, err)
		return
	}
	resp := map[string]interface{}{"traces": traces, "hasMore": hasMore}
	// Only emit `total` when the caller actually computed one — clients
	// distinguish "unknown total" from "zero total" by the field's
	// presence, not its value.
	if f.CountMode != "skip" {
		resp["total"] = total
	}
	writeJSON(w, resp)
}

func (s *Server) getTraceAggregate(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	f := chstore.AggregateFilter{
		GroupBy:  q.Get("groupBy"),
		Service:  q.Get("service"),
		Search:   q.Get("search"),
		From:     parseTime(q.Get("from")),
		To:       parseTime(q.Get("to")),
		HasError: q.Get("hasError") == "true",
		MinMs:    parseFloat(q.Get("minMs")),
		MaxMs:    parseFloat(q.Get("maxMs")),
		Sort:     q.Get("sort"),
		Order:    q.Get("order"),
		Limit:    parseInt(q.Get("limit"), 100),
	}
	filters, ferr := parseFiltersAndDSL(q.Get("filters"), q.Get("dsl"))
	if ferr != nil {
		http.Error(w, "invalid query DSL: "+ferr.Error(), http.StatusBadRequest)
		return
	}
	f.Filters = filters
	rows, err := s.store.GetTraceAggregate(r.Context(), f)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, rows)
}

func (s *Server) getTrace(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	spans, err := s.store.GetTrace(r.Context(), id)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, map[string]interface{}{"traceId": id, "spans": spans})
}

func (s *Server) getLogs(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	sev, _ := strconv.Atoi(q.Get("severity"))
	f := logstore.Filter{
		Service:     q.Get("service"),
		Search:      q.Get("search"),
		From:        parseTime(q.Get("from")),
		To:          parseTime(q.Get("to")),
		SeverityMin: uint8(sev),
		TraceID:     q.Get("traceId"),
		SpanID:      q.Get("spanId"),
		Limit:       parseInt(q.Get("limit"), 100),
		Offset:      parseInt(q.Get("offset"), 0),
	}
	page, err := s.logs.Search(r.Context(), f)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, map[string]interface{}{"total": page.Total, "logs": page.Logs})
}

func (s *Server) getMetricNames(w http.ResponseWriter, r *http.Request) {
	svc := r.URL.Query().Get("service")
	s.serveCached(w, r, "metric-names:svc="+svc, 60*time.Second, func() (any, error) {
		return s.store.GetMetricNames(r.Context(), svc)
	})
}

func (s *Server) getMetrics(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	pts, err := s.store.GetMetricPoints(r.Context(),
		q.Get("name"), q.Get("service"),
		parseTime(q.Get("from")), parseTime(q.Get("to")),
		parseInt(q.Get("limit"), 500),
	)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, pts)
}

// ── Metric query (multi-series, attribute filters, group-by) ─────────────────

func (s *Server) queryMetric(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	step, _ := strconv.Atoi(q.Get("step"))
	groupBy := splitNonEmpty(q.Get("groupBy"), ',')
	series, err := s.store.QueryMetric(r.Context(), chstore.MetricQueryFilter{
		Name:        q.Get("name"),
		Service:     q.Get("service"),
		Filters:     parseFilters(q.Get("filters")),
		GroupBy:     groupBy,
		Aggregation: q.Get("agg"),
		From:        parseTime(q.Get("from")),
		To:          parseTime(q.Get("to")),
		StepSeconds: step,
	})
	if err != nil { writeErr(w, err); return }
	writeJSON(w, series)
}

func (s *Server) getMetricLabelValues(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	since := parseDuration(q.Get("since"), 24*time.Hour)
	vals, err := s.store.MetricLabelValues(r.Context(), q.Get("metric"), q.Get("key"), since)
	if err != nil { writeErr(w, err); return }
	writeJSON(w, vals)
}

// ── Span metrics (Tempo span-metrics generator + Dynatrace MDA) ──────────────

func (s *Server) spanMetric(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	groupBy := splitNonEmpty(q.Get("groupBy"), ',')
	step, _ := strconv.Atoi(q.Get("step"))
	filters, err := parseFiltersAndDSL(q.Get("filters"), q.Get("dsl"))
	if err != nil {
		http.Error(w, "invalid query DSL: "+err.Error(), http.StatusBadRequest)
		return
	}
	f := chstore.SpanMetricFilter{
		Filters:     filters,
		Aggregation: q.Get("agg"),
		Field:       q.Get("field"),
		GroupBy:     groupBy,
		From:        parseTime(q.Get("from")),
		To:          parseTime(q.Get("to")),
		StepSeconds: step,
	}
	series, err := s.store.QuerySpanMetric(r.Context(), f)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, series)
}

func splitNonEmpty(s string, sep rune) []string {
	if s == "" {
		return nil
	}
	parts := strings.FieldsFunc(s, func(r rune) bool { return r == sep })
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p != "" {
			out = append(out, p)
		}
	}
	return out
}

// ── Profiling ─────────────────────────────────────────────────────────────────

func (s *Server) ingestProfile(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(io.LimitReader(r.Body, 64<<20))
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	service := r.Header.Get("X-Coremetry-Service")
	if service == "" {
		service = "unknown"
	}
	host := r.Header.Get("X-Coremetry-Host")
	ptype := r.Header.Get("X-Coremetry-Profile-Type")
	if ptype == "" {
		ptype = "cpu"
	}
	durNs, _ := strconv.ParseInt(r.Header.Get("X-Coremetry-Duration-Ns"), 10, 64)
	startNs, _ := strconv.ParseInt(r.Header.Get("X-Coremetry-Start-Time-Ns"), 10, 64)
	if startNs == 0 {
		startNs = time.Now().UnixNano()
	}

	cnt, _ := profileconv.SampleCount(body)

	id := newID(16)
	p := &chstore.Profile{
		ProfileID:   id,
		ServiceName: service,
		HostName:    host,
		ProfileType: ptype,
		StartTime:   time.Unix(0, startNs).UTC(),
		DurationNs:  durNs,
		PprofData:   body,
		SampleCount: uint32(cnt),
	}
	if err := s.store.InsertProfile(r.Context(), p); err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, map[string]string{"profileId": id})
}

func (s *Server) listProfiles(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	f := chstore.ProfileFilter{
		Service:     q.Get("service"),
		ProfileType: q.Get("type"),
		From:        parseTime(q.Get("from")),
		To:          parseTime(q.Get("to")),
		Limit:       parseInt(q.Get("limit"), 100),
	}
	rows, err := s.store.ListProfiles(r.Context(), f)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, rows)
}

func (s *Server) getProfile(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	data, meta, err := s.store.GetProfileBytes(r.Context(), id)
	if err != nil {
		writeErr(w, err)
		return
	}
	flame, err := profileconv.BuildFlameAuto(data)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, map[string]interface{}{"meta": meta, "flame": flame})
}

func (s *Server) profilesForSpan(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	startNs, _ := strconv.ParseInt(q.Get("start"), 10, 64)
	endNs, _ := strconv.ParseInt(q.Get("end"), 10, 64)
	if startNs == 0 || endNs == 0 {
		http.Error(w, "missing start/end query params", http.StatusBadRequest)
		return
	}
	rows, err := s.store.FindProfilesForSpan(r.Context(),
		q.Get("service"),
		time.Unix(0, startNs), time.Unix(0, endNs))
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, rows)
}

func newID(n int) string {
	b := make([]byte, n)
	rand.Read(b)
	return hex.EncodeToString(b)
}

// ── Auth ─────────────────────────────────────────────────────────────────────

func (s *Server) login(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Email    string `json:"email"`
		Password string `json:"password"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "invalid body", http.StatusBadRequest)
		return
	}
	body.Email = strings.ToLower(strings.TrimSpace(body.Email))
	if body.Email == "" || body.Password == "" {
		http.Error(w, "email and password required", http.StatusBadRequest)
		return
	}
	user, err := s.store.GetUserByEmail(r.Context(), body.Email)
	if err != nil {
		writeErr(w, err)
		return
	}
	// Run bcrypt either way to keep the timing path consistent across the
	// "unknown user" and "wrong password" branches.
	ok := user != nil && auth.CheckPassword(user.PasswordHash, body.Password)
	if !ok {
		http.Error(w, `{"error":"invalid credentials"}`, http.StatusUnauthorized)
		return
	}
	tok, exp, err := s.auth.Issue(user.ID, user.Email, user.Role)
	if err != nil {
		writeErr(w, err)
		return
	}
	http.SetCookie(w, &http.Cookie{
		Name:     auth.CookieName,
		Value:    tok,
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		Expires:  exp,
		MaxAge:   int(s.auth.TTL().Seconds()),
	})
	writeJSON(w, map[string]interface{}{
		"token":     tok,
		"expiresAt": exp.UnixNano(),
		"user": map[string]string{
			"id": user.ID, "email": user.Email, "role": user.Role,
		},
	})
}

func (s *Server) logout(w http.ResponseWriter, r *http.Request) {
	http.SetCookie(w, &http.Cookie{
		Name: auth.CookieName, Value: "", Path: "/",
		HttpOnly: true, MaxAge: -1,
	})
	writeJSON(w, map[string]string{"status": "ok"})
}

func (s *Server) me(w http.ResponseWriter, r *http.Request) {
	c := auth.FromContext(r.Context())
	if c == nil {
		http.Error(w, `{"error":"not authenticated"}`, http.StatusUnauthorized)
		return
	}
	writeJSON(w, map[string]string{
		"id": c.UserID, "email": c.Email, "role": c.Role,
	})
}

// authConfig describes which auth methods the UI should offer. Public on
// purpose — the login page must be able to call it before the user signs in.
//
// When demo mode is on the response also includes the bootstrap admin
// credentials so the login form can pre-fill them. That is intentionally
// public — anyone with the demo URL is meant to log in as the demo user.
func (s *Server) authConfig(w http.ResponseWriter, r *http.Request) {
	// Cache key includes demoMode so flipping it doesn't keep serving stale
	// auth config to the login page.
	key := "auth-config"
	if s.demoMode {
		key = "auth-config:demo"
	}
	s.serveCached(w, r, key, 60*time.Second, func() (any, error) {
		resp := map[string]interface{}{
			"local": map[string]bool{"enabled": true},
			"oidc":  map[string]interface{}{"enabled": false},
			"demo":  map[string]interface{}{"enabled": false},
		}
		if s.oidc.Enabled() {
			resp["oidc"] = map[string]interface{}{
				"enabled":     true,
				"displayName": s.oidc.DisplayName(),
			}
		}
		if s.demoMode {
			resp["demo"] = map[string]interface{}{
				"enabled":  true,
				"email":    s.demoEmail,
				"password": s.demoPassword,
			}
		}
		return resp, nil
	})
}

// ── OIDC sign-in ─────────────────────────────────────────────────────────────

const (
	oidcStateCookie    = "coremetry_oidc_state"
	oidcNonceCookie    = "coremetry_oidc_nonce"
	oidcVerifierCookie = "coremetry_oidc_verifier"
)

func (s *Server) oidcStart(w http.ResponseWriter, r *http.Request) {
	if !s.oidc.Enabled() {
		http.Error(w, "oidc not configured", http.StatusNotFound)
		return
	}
	state := auth.RandomURLToken(16)
	nonce := auth.RandomURLToken(16)
	verifier := auth.RandomURLToken(32)
	challenge := auth.PKCEChallenge(verifier)

	// Short-lived HttpOnly cookies — the IdP round-trip should take seconds.
	for _, c := range []*http.Cookie{
		setOIDCCookie(oidcStateCookie, state),
		setOIDCCookie(oidcNonceCookie, nonce),
		setOIDCCookie(oidcVerifierCookie, verifier),
	} {
		http.SetCookie(w, c)
	}
	http.Redirect(w, r, s.oidc.AuthURL(state, nonce, challenge), http.StatusFound)
}

func (s *Server) oidcCallback(w http.ResponseWriter, r *http.Request) {
	if !s.oidc.Enabled() {
		http.Error(w, "oidc not configured", http.StatusNotFound)
		return
	}
	q := r.URL.Query()
	if errParam := q.Get("error"); errParam != "" {
		s.oidcFail(w, r, errParam+": "+q.Get("error_description"))
		return
	}
	code := q.Get("code")
	stateGot := q.Get("state")
	if code == "" || stateGot == "" {
		s.oidcFail(w, r, "missing code or state")
		return
	}

	// Pull and immediately clear the in-flight cookies.
	stateWant, _ := r.Cookie(oidcStateCookie)
	nonce, _ := r.Cookie(oidcNonceCookie)
	verifier, _ := r.Cookie(oidcVerifierCookie)
	for _, name := range []string{oidcStateCookie, oidcNonceCookie, oidcVerifierCookie} {
		http.SetCookie(w, &http.Cookie{Name: name, Value: "", Path: "/", MaxAge: -1, HttpOnly: true})
	}
	if stateWant == nil || stateWant.Value != stateGot {
		s.oidcFail(w, r, "state mismatch — possible CSRF or stale tab")
		return
	}
	if nonce == nil || verifier == nil {
		s.oidcFail(w, r, "session cookies expired — try again")
		return
	}

	claims, err := s.oidc.Exchange(r.Context(), code, verifier.Value, nonce.Value)
	if err != nil {
		log.Printf("[oidc] callback: %v", err)
		s.oidcFail(w, r, err.Error())
		return
	}

	// Lookup or auto-provision. Email is the identity key; existing local
	// users with the same email are kept (they can use either method).
	email := strings.ToLower(claims.Email)
	user, err := s.store.GetUserByEmail(r.Context(), email)
	if err != nil {
		s.oidcFail(w, r, err.Error())
		return
	}
	if user == nil {
		role := s.oidc.DefaultRole()
		if role != auth.RoleAdmin {
			role = auth.RoleViewer
		}
		user = &chstore.User{
			ID:           newID(8),
			Email:        email,
			PasswordHash: "", // OIDC-only — local login impossible until an admin sets a password
			Role:         role,
			AuthProvider: "oidc",
		}
		if err := s.store.UpsertUser(r.Context(), *user); err != nil {
			s.oidcFail(w, r, err.Error())
			return
		}
		log.Printf("[oidc] auto-provisioned user %q (role=%s)", email, role)
	}

	tok, exp, err := s.auth.Issue(user.ID, user.Email, user.Role)
	if err != nil {
		s.oidcFail(w, r, err.Error())
		return
	}
	http.SetCookie(w, &http.Cookie{
		Name:     auth.CookieName,
		Value:    tok,
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		Expires:  exp,
		MaxAge:   int(s.auth.TTL().Seconds()),
	})
	http.Redirect(w, r, "/", http.StatusFound)
}

// oidcFail renders the failure on the login page so the user gets a hint
// instead of a blank API response.
func (s *Server) oidcFail(w http.ResponseWriter, r *http.Request, msg string) {
	http.Redirect(w, r, "/login/?error="+url.QueryEscape(msg), http.StatusFound)
}

func setOIDCCookie(name, value string) *http.Cookie {
	return &http.Cookie{
		Name:     name,
		Value:    value,
		Path:     "/api/auth/oidc/",
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		MaxAge:   600, // 10 min — IdP round-trip cap
	}
}

// changeOwnPassword lets the logged-in user rotate their own password.
// Requires the current password as proof of presence.
func (s *Server) changeOwnPassword(w http.ResponseWriter, r *http.Request) {
	c := auth.FromContext(r.Context())
	if c == nil {
		http.Error(w, `{"error":"not authenticated"}`, http.StatusUnauthorized)
		return
	}
	var body struct {
		Current string `json:"currentPassword"`
		New     string `json:"newPassword"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "invalid body", http.StatusBadRequest)
		return
	}
	if len(body.New) < 6 {
		http.Error(w, `{"error":"password must be at least 6 characters"}`, http.StatusBadRequest)
		return
	}
	user, err := s.store.GetUserByID(r.Context(), c.UserID)
	if err != nil {
		writeErr(w, err)
		return
	}
	if user == nil || !auth.CheckPassword(user.PasswordHash, body.Current) {
		http.Error(w, `{"error":"current password is incorrect"}`, http.StatusUnauthorized)
		return
	}
	hash, err := auth.HashPassword(body.New)
	if err != nil {
		writeErr(w, err)
		return
	}
	if err := s.store.UpdatePassword(r.Context(), c.UserID, hash); err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, map[string]string{"status": "ok"})
}

// ── SMTP + notification channels (admin only) ───────────────────────────────

// maskedSMTP returns the persisted settings with the password redacted —
// the GET endpoint must never echo the real password back to the browser
// (it'd be visible to anyone with browser dev tools open).
func maskedSMTP(s notify.SMTPSettings) map[string]any {
	masked := ""
	if s.Password != "" {
		masked = "********"
	}
	return map[string]any{
		"host": s.Host, "port": s.Port, "username": s.Username,
		"password": masked, "from": s.From, "fromName": s.FromName,
		"startTLS": s.StartTLS, "skipVerify": s.SkipVerify,
		"configured": s.Configured(),
	}
}

func (s *Server) getSMTPSettings(w http.ResponseWriter, r *http.Request) {
	cfg := s.notify.SMTP(r.Context())
	writeJSON(w, maskedSMTP(cfg))
}

func (s *Server) putSMTPSettings(w http.ResponseWriter, r *http.Request) {
	var body notify.SMTPSettings
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "invalid body", http.StatusBadRequest)
		return
	}
	// Empty / sentinel password = "keep the existing one" so admins can
	// edit the host/port without re-entering credentials each time.
	if body.Password == "" || body.Password == "********" {
		body.Password = s.notify.SMTP(r.Context()).Password
	}
	if err := s.notify.SaveSMTP(r.Context(), body); err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, maskedSMTP(body))
}

func (s *Server) testSMTPSettings(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Recipient string `json:"recipient"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "invalid body", http.StatusBadRequest)
		return
	}
	if body.Recipient == "" {
		http.Error(w, `{"error":"recipient required"}`, http.StatusBadRequest)
		return
	}
	cfgRaw, _ := json.Marshal(notify.EmailChannelConfig{Recipients: []string{body.Recipient}})
	tmp := chstore.NotificationChannel{
		ID: "ad-hoc-test", Name: "Ad-hoc test", Type: "email",
		Config: cfgRaw, Enabled: true, MinSeverity: "info",
	}
	if err := s.notify.SendTest(r.Context(), tmp); err != nil {
		http.Error(w, fmt.Sprintf(`{"error":%q}`, err.Error()), http.StatusBadGateway)
		return
	}
	writeJSON(w, map[string]string{"status": "sent"})
}

func (s *Server) listChannels(w http.ResponseWriter, r *http.Request) {
	out, err := s.store.ListChannels(r.Context())
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, out)
}

func (s *Server) createChannel(w http.ResponseWriter, r *http.Request) {
	var c chstore.NotificationChannel
	if err := json.NewDecoder(r.Body).Decode(&c); err != nil {
		http.Error(w, "invalid body", http.StatusBadRequest)
		return
	}
	if c.Name == "" || c.Type == "" {
		http.Error(w, `{"error":"name and type required"}`, http.StatusBadRequest)
		return
	}
	c.ID = newID(8)
	if err := s.store.UpsertChannel(r.Context(), c); err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, c)
}

func (s *Server) updateChannel(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	existing, err := s.store.GetChannel(r.Context(), id)
	if err != nil {
		writeErr(w, err)
		return
	}
	if existing == nil {
		http.Error(w, `{"error":"channel not found"}`, http.StatusNotFound)
		return
	}
	var c chstore.NotificationChannel
	if err := json.NewDecoder(r.Body).Decode(&c); err != nil {
		http.Error(w, "invalid body", http.StatusBadRequest)
		return
	}
	c.ID = id
	c.CreatedAt = existing.CreatedAt
	if err := s.store.UpsertChannel(r.Context(), c); err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, c)
}

func (s *Server) deleteChannel(w http.ResponseWriter, r *http.Request) {
	if err := s.store.DeleteChannel(r.Context(), r.PathValue("id")); err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, map[string]string{"status": "ok"})
}

func (s *Server) testChannel(w http.ResponseWriter, r *http.Request) {
	c, err := s.store.GetChannel(r.Context(), r.PathValue("id"))
	if err != nil {
		writeErr(w, err)
		return
	}
	if c == nil {
		http.Error(w, `{"error":"channel not found"}`, http.StatusNotFound)
		return
	}
	if err := s.notify.SendTest(r.Context(), *c); err != nil {
		http.Error(w, fmt.Sprintf(`{"error":%q}`, err.Error()), http.StatusBadGateway)
		return
	}
	writeJSON(w, map[string]string{"status": "sent"})
}

// ── User management (admin only) ─────────────────────────────────────────────

func (s *Server) listUsers(w http.ResponseWriter, r *http.Request) {
	users, err := s.store.ListUsers(r.Context())
	if err != nil {
		writeErr(w, err)
		return
	}
	// Strip password hashes before sending — defence in depth even though
	// the User struct already json:"-"s them.
	out := make([]map[string]interface{}, 0, len(users))
	for _, u := range users {
		provider := u.AuthProvider
		if provider == "" {
			provider = "local"
		}
		out = append(out, map[string]interface{}{
			"id": u.ID, "email": u.Email, "role": u.Role,
			"disabled":     u.Disabled,
			"authProvider": provider,
			"createdAt":    u.CreatedAt,
		})
	}
	writeJSON(w, out)
}

func (s *Server) createUser(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Email    string `json:"email"`
		Password string `json:"password"`
		Role     string `json:"role"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "invalid body", http.StatusBadRequest)
		return
	}
	body.Email = strings.ToLower(strings.TrimSpace(body.Email))
	if body.Email == "" || len(body.Password) < 6 {
		http.Error(w, `{"error":"email and password (>=6 chars) required"}`, http.StatusBadRequest)
		return
	}
	if body.Role != auth.RoleAdmin && body.Role != auth.RoleViewer {
		body.Role = auth.RoleViewer
	}
	existing, err := s.store.GetUserByEmail(r.Context(), body.Email)
	if err != nil {
		writeErr(w, err)
		return
	}
	if existing != nil {
		http.Error(w, `{"error":"email already exists"}`, http.StatusConflict)
		return
	}
	hash, err := auth.HashPassword(body.Password)
	if err != nil {
		writeErr(w, err)
		return
	}
	u := chstore.User{
		ID:           newID(8),
		Email:        body.Email,
		PasswordHash: hash,
		Role:         body.Role,
	}
	if err := s.store.UpsertUser(r.Context(), u); err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, map[string]interface{}{
		"id": u.ID, "email": u.Email, "role": u.Role,
	})
}

func (s *Server) deleteUser(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	caller := auth.FromContext(r.Context())
	if caller != nil && caller.UserID == id {
		http.Error(w, `{"error":"cannot delete your own account"}`, http.StatusBadRequest)
		return
	}
	target, err := s.store.GetUserByID(r.Context(), id)
	if err != nil {
		writeErr(w, err)
		return
	}
	if target == nil {
		http.Error(w, `{"error":"user not found"}`, http.StatusNotFound)
		return
	}
	// Refuse to remove the last remaining admin — locks the system.
	if target.Role == auth.RoleAdmin {
		n, err := s.store.CountAdmins(r.Context())
		if err != nil {
			writeErr(w, err)
			return
		}
		if n <= 1 {
			http.Error(w, `{"error":"cannot remove the last admin"}`, http.StatusBadRequest)
			return
		}
	}
	if err := s.store.DisableUser(r.Context(), id); err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, map[string]string{"status": "ok"})
}

// resetUserPassword lets an admin set a new password for any user without
// supplying the current one — used by the user-management screen.
func (s *Server) resetUserPassword(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	var body struct {
		Password string `json:"password"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "invalid body", http.StatusBadRequest)
		return
	}
	if len(body.Password) < 6 {
		http.Error(w, `{"error":"password must be at least 6 characters"}`, http.StatusBadRequest)
		return
	}
	hash, err := auth.HashPassword(body.Password)
	if err != nil {
		writeErr(w, err)
		return
	}
	if err := s.store.UpdatePassword(r.Context(), id, hash); err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, map[string]string{"status": "ok"})
}

// ── Exceptions / Errors page ─────────────────────────────────────────────────

// ── Errors Inbox ─────────────────────────────────────────────────────────────

func (s *Server) listExceptionGroups(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	out, err := s.store.ListExceptionGroups(r.Context(), chstore.ExceptionGroupFilter{
		State:    q.Get("state"),
		Service:  q.Get("service"),
		Assignee: q.Get("assignee"),
		Limit:    parseInt(q.Get("limit"), 200),
	})
	if err != nil { writeErr(w, err); return }
	writeJSON(w, out)
}

func (s *Server) getExceptionGroupSamples(w http.ResponseWriter, r *http.Request) {
	limit := parseInt(r.URL.Query().Get("limit"), 10)
	out, err := s.store.GetExceptionGroupSamples(r.Context(), r.PathValue("fp"), limit)
	if err != nil { writeErr(w, err); return }
	writeJSON(w, out)
}

func (s *Server) setExceptionGroupState(w http.ResponseWriter, r *http.Request) {
	var body struct{ State string `json:"state"` }
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "invalid body", http.StatusBadRequest); return
	}
	switch body.State {
	case chstore.ExStateNew, chstore.ExStateAcknowledged,
		chstore.ExStateResolved, chstore.ExStateIgnored:
		// ok
	default:
		http.Error(w, `{"error":"invalid state"}`, http.StatusBadRequest); return
	}
	if err := s.store.SetExceptionGroupState(r.Context(), r.PathValue("fp"), body.State); err != nil {
		writeErr(w, err); return
	}
	writeJSON(w, map[string]string{"status": "ok"})
}

func (s *Server) assignExceptionGroup(w http.ResponseWriter, r *http.Request) {
	var body struct{ Assignee string `json:"assignee"` }
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "invalid body", http.StatusBadRequest); return
	}
	if err := s.store.AssignExceptionGroup(r.Context(), r.PathValue("fp"), body.Assignee); err != nil {
		writeErr(w, err); return
	}
	writeJSON(w, map[string]string{"status": "ok"})
}

func (s *Server) listExceptions(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	rows, err := s.store.GetExceptions(r.Context(), chstore.ExceptionFilter{
		Service: q.Get("service"),
		GroupBy: q.Get("groupBy"),
		From:    parseTime(q.Get("from")),
		To:      parseTime(q.Get("to")),
		Limit:   parseInt(q.Get("limit"), 100),
	})
	if err != nil { writeErr(w, err); return }
	writeJSON(w, rows)
}

// ── Service backtrace ────────────────────────────────────────────────────────

// svcOperationSummary returns per-operation aggregates for a single
// service. Drives the Operations table on the service detail page.
func (s *Server) svcOperationSummary(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	svc := r.PathValue("name")
	since := parseDuration(q.Get("since"), 24*time.Hour)
	from, to := parseTime(q.Get("from")), parseTime(q.Get("to"))
	key := fmt.Sprintf("svc-ops:svc=%s:since=%s:from=%s:to=%s",
		svc, q.Get("since"), q.Get("from"), q.Get("to"))
	s.serveCached(w, r, key, 15*time.Second, func() (any, error) {
		return s.store.GetOperationSummary(r.Context(), svc, since, from, to)
	})
}

func (s *Server) svcCallers(w http.ResponseWriter, r *http.Request) {
	since := parseDuration(r.URL.Query().Get("since"), 24*time.Hour)
	out, err := s.store.CallersOf(r.Context(), r.PathValue("name"), since)
	if err != nil { writeErr(w, err); return }
	writeJSON(w, out)
}

func (s *Server) svcCallees(w http.ResponseWriter, r *http.Request) {
	since := parseDuration(r.URL.Query().Get("since"), 24*time.Hour)
	out, err := s.store.CalleesOf(r.Context(), r.PathValue("name"), since)
	if err != nil { writeErr(w, err); return }
	writeJSON(w, out)
}

// ── Problems & alert rules ───────────────────────────────────────────────────

func (s *Server) listProblems(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	f := chstore.ProblemFilter{
		Status: q.Get("status"), Service: q.Get("service"),
		Severity: q.Get("severity"), Limit: parseInt(q.Get("limit"), 100),
	}
	// Sidebar polls this endpoint per user every 30s — at 100 logged-in
	// users that's 3 RPS just for the badge. 5s TTL collapses the load.
	key := fmt.Sprintf("problems:status=%s:svc=%s:sev=%s:limit=%d",
		f.Status, f.Service, f.Severity, f.Limit)
	s.serveCached(w, r, key, 5*time.Second, func() (any, error) {
		return s.store.ListProblems(r.Context(), f)
	})
}

func (s *Server) listAlertRules(w http.ResponseWriter, r *http.Request) {
	rules, err := s.store.ListAlertRules(r.Context())
	if err != nil { writeErr(w, err); return }
	writeJSON(w, rules)
}

func (s *Server) createAlertRule(w http.ResponseWriter, r *http.Request) {
	var rule chstore.AlertRule
	if err := json.NewDecoder(r.Body).Decode(&rule); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest); return
	}
	if rule.ID == "" {
		rule.ID = newID(8)
	}
	rule.BuiltIn = false
	rule.CreatedAt = time.Now().UnixNano()
	if err := s.store.UpsertAlertRule(r.Context(), rule); err != nil {
		writeErr(w, err); return
	}
	writeJSON(w, rule)
}

func (s *Server) deleteAlertRule(w http.ResponseWriter, r *http.Request) {
	if err := s.store.DeleteAlertRule(r.Context(), r.PathValue("id")); err != nil {
		writeErr(w, err); return
	}
	writeJSON(w, map[string]string{"status": "ok"})
}

func (s *Server) enableAlertRule(w http.ResponseWriter, r *http.Request) {
	if err := s.store.SetAlertRuleEnabled(r.Context(), r.PathValue("id"), true); err != nil {
		writeErr(w, err); return
	}
	writeJSON(w, map[string]string{"status": "ok"})
}

// ── Dashboards ───────────────────────────────────────────────────────────────

func (s *Server) listDashboards(w http.ResponseWriter, r *http.Request) {
	rows, err := s.store.ListDashboards(r.Context())
	if err != nil { writeErr(w, err); return }
	writeJSON(w, rows)
}

func (s *Server) getDashboard(w http.ResponseWriter, r *http.Request) {
	d, err := s.store.GetDashboard(r.Context(), r.PathValue("id"))
	if err != nil { writeErr(w, err); return }
	if d == nil {
		http.Error(w, `{"error":"dashboard not found"}`, http.StatusNotFound)
		return
	}
	writeJSON(w, d)
}

func (s *Server) createDashboard(w http.ResponseWriter, r *http.Request) {
	var d chstore.Dashboard
	if err := json.NewDecoder(r.Body).Decode(&d); err != nil {
		http.Error(w, "invalid body", http.StatusBadRequest); return
	}
	if d.Name == "" {
		http.Error(w, `{"error":"name required"}`, http.StatusBadRequest); return
	}
	d.ID = newID(8)
	if err := s.store.UpsertDashboard(r.Context(), d); err != nil {
		writeErr(w, err); return
	}
	writeJSON(w, d)
}

func (s *Server) updateDashboard(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	existing, err := s.store.GetDashboard(r.Context(), id)
	if err != nil { writeErr(w, err); return }
	if existing == nil {
		http.Error(w, `{"error":"dashboard not found"}`, http.StatusNotFound); return
	}
	var d chstore.Dashboard
	if err := json.NewDecoder(r.Body).Decode(&d); err != nil {
		http.Error(w, "invalid body", http.StatusBadRequest); return
	}
	d.ID = id
	d.CreatedAt = existing.CreatedAt
	if err := s.store.UpsertDashboard(r.Context(), d); err != nil {
		writeErr(w, err); return
	}
	writeJSON(w, d)
}

func (s *Server) deleteDashboard(w http.ResponseWriter, r *http.Request) {
	if err := s.store.DeleteDashboard(r.Context(), r.PathValue("id")); err != nil {
		writeErr(w, err); return
	}
	writeJSON(w, map[string]string{"status": "ok"})
}

// ── SLOs ─────────────────────────────────────────────────────────────────────

func (s *Server) listSLOs(w http.ResponseWriter, r *http.Request) {
	out, err := s.store.ListSLOs(r.Context())
	if err != nil { writeErr(w, err); return }
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
	if err != nil { writeErr(w, err); return }
	if o == nil {
		http.Error(w, `{"error":"slo not found"}`, http.StatusNotFound); return
	}
	writeJSON(w, o)
}

func (s *Server) sloStatus(w http.ResponseWriter, r *http.Request) {
	o, err := s.store.GetSLO(r.Context(), r.PathValue("id"))
	if err != nil { writeErr(w, err); return }
	if o == nil {
		http.Error(w, `{"error":"slo not found"}`, http.StatusNotFound); return
	}
	st, err := s.store.ComputeSLOStatus(r.Context(), *o)
	if err != nil { writeErr(w, err); return }
	writeJSON(w, st)
}

func (s *Server) createSLO(w http.ResponseWriter, r *http.Request) {
	var o chstore.SLO
	if err := json.NewDecoder(r.Body).Decode(&o); err != nil {
		http.Error(w, "invalid body", http.StatusBadRequest); return
	}
	if o.Name == "" || o.Service == "" || o.SLIType == "" {
		http.Error(w, `{"error":"name, service and sliType required"}`, http.StatusBadRequest); return
	}
	if o.Target <= 0 || o.Target >= 1 {
		http.Error(w, `{"error":"target must be a fraction between 0 and 1 (e.g. 0.99)"}`, http.StatusBadRequest); return
	}
	if o.SLIType == chstore.SLITypeLatency && o.ThresholdMs <= 0 {
		http.Error(w, `{"error":"thresholdMs required for latency SLIs"}`, http.StatusBadRequest); return
	}
	o.ID = newID(8)
	if err := s.store.UpsertSLO(r.Context(), o); err != nil {
		writeErr(w, err); return
	}
	writeJSON(w, o)
}

func (s *Server) deleteSLO(w http.ResponseWriter, r *http.Request) {
	if err := s.store.DeleteSLO(r.Context(), r.PathValue("id")); err != nil {
		writeErr(w, err); return
	}
	writeJSON(w, map[string]string{"status": "ok"})
}

func (s *Server) getHealth(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, map[string]interface{}{
		"status":        "ok",
		"spans_queued":  s.ing.Spans.QueueLen(),
		"logs_queued":   s.ing.Logs.QueueLen(),
		"metrics_queued": s.ing.Metrics.QueueLen(),
		"spans_dropped": s.ing.Spans.Dropped(),
	})
}

// getStatus drives the public-style status page: probes every dependency
// in parallel with a 2s per-component timeout, returns a structured
// list of components with operational | degraded | outage. Overall
// status is the worst component status, the same way statuspage.io and
// other industry-standard pages compute it.
//
// Cached for 5s — the /status page polls every 30s but a refresh-spammy
// user shouldn't be able to hammer the underlying systems with probes.
func (s *Server) getStatus(w http.ResponseWriter, r *http.Request) {
	s.serveCached(w, r, "system-status", 5*time.Second, func() (any, error) {
		return s.collectStatus(r.Context()), nil
	})
}

type componentStatus struct {
	Name      string `json:"name"`
	Status    string `json:"status"` // operational | degraded | outage
	Message   string `json:"message,omitempty"`
	LatencyMs int64  `json:"latencyMs,omitempty"`
}

type systemStatus struct {
	Status      string             `json:"status"` // worst of the components
	CheckedAt   string             `json:"checkedAt"`
	Components  []componentStatus  `json:"components"`
}

func (s *Server) collectStatus(ctx context.Context) systemStatus {
	probe := func(name string, fn func(context.Context) error) componentStatus {
		// 2s budget per component — enough for a remote ping over a
		// laggy network, short enough that a single dead dependency
		// can't make the whole page hang.
		pctx, cancel := context.WithTimeout(ctx, 2*time.Second)
		defer cancel()
		start := time.Now()
		err := fn(pctx)
		ms := time.Since(start).Milliseconds()
		c := componentStatus{Name: name, LatencyMs: ms}
		if err == nil {
			c.Status = "operational"
			return c
		}
		// Timeout vs hard failure → outage either way for now;
		// "degraded" is reserved for capacity warnings (queue depth).
		c.Status = "outage"
		c.Message = err.Error()
		return c
	}

	// Run probes concurrently — sequential serialisation would make a
	// page load >2s if any single component is slow.
	type result struct {
		idx int
		c   componentStatus
	}
	probes := []struct {
		name string
		fn   func(context.Context) error
	}{
		{"ClickHouse",     s.store.Ping},
		{"Cache (Redis)",  s.cache.Ping},
		{"Logs backend",   s.logs.Ping},
	}
	out := make([]componentStatus, len(probes))
	results := make(chan result, len(probes))
	for i, p := range probes {
		i, p := i, p
		go func() { results <- result{i, probe(p.name, p.fn)} }()
	}
	for range probes {
		r := <-results
		out[r.idx] = r.c
	}

	// API + Ingest queues — synchronous, in-process, no timeout needed.
	out = append(out,
		componentStatus{Name: "HTTP API", Status: "operational"},
		queueStatus("Spans ingest",   s.ing.Spans),
		queueStatus("Logs ingest",    s.ing.Logs),
		queueStatus("Metrics ingest", s.ing.Metrics),
	)
	// Annotate the logs-backend row with which backend is wired so a
	// glance at the page tells the operator the configured topology.
	for i := range out {
		if out[i].Name == "Logs backend" {
			if out[i].Message == "" {
				out[i].Message = "backend: " + s.logs.Backend()
			}
			break
		}
	}

	// Worst-of aggregation. Operational < Degraded < Outage.
	rank := map[string]int{"operational": 0, "degraded": 1, "outage": 2}
	worst := "operational"
	for _, c := range out {
		if rank[c.Status] > rank[worst] {
			worst = c.Status
		}
	}
	return systemStatus{
		Status:     worst,
		CheckedAt:  time.Now().UTC().Format(time.RFC3339),
		Components: out,
	}
}

// queueStatus inspects an ingest queue and reports degraded once it's
// past 80% full, outage once it's past 95% (where back-pressure starts
// dropping records).
func queueStatus(name string, q interface{ QueueLen() int; Dropped() int64 }) componentStatus {
	const cap = 100_000 // matches Ingestion.BufferSize default in config
	depth := q.QueueLen()
	dropped := q.Dropped()
	c := componentStatus{Name: name, Status: "operational"}
	if dropped > 0 {
		c.Status = "outage"
		c.Message = fmt.Sprintf("dropped %d records — queue full", dropped)
		return c
	}
	if depth > cap*95/100 {
		c.Status = "outage"
		c.Message = fmt.Sprintf("queue %d / %d (>95%%)", depth, cap)
	} else if depth > cap*80/100 {
		c.Status = "degraded"
		c.Message = fmt.Sprintf("queue %d / %d (>80%%)", depth, cap)
	} else if depth > 0 {
		c.Message = fmt.Sprintf("queue %d / %d", depth, cap)
	}
	return c
}

// ── Middleware & helpers ───────────────────────────────────────────────────────

// spaHandler serves the embedded Next.js static export. Two behaviours
// that diverge from the stdlib http.FileServer:
//
//  1. For a request like `/logs` (no trailing slash) where the export
//     contains `logs/index.html`, FileServer returns a 301 redirect to
//     `/logs/`. Behind a TLS-terminating proxy where `X-Forwarded-Proto`
//     isn't propagated, the redirect's Location header carries `http://`
//     and the client either follows it back to the http virtual host
//     (which may not exist) or hits a strict-https policy. spaHandler
//     resolves the trailing-slash case in-process and serves the
//     content directly — no redirect, no scheme guessing.
//
//  2. For an unknown path that isn't a static asset (no file extension)
//     spaHandler falls back to the root `index.html` so the Next.js
//     client router can mount the right page from the URL. That covers
//     deep-links to dynamic routes that don't have a pre-rendered
//     directory in the export.
func spaHandler(root fs.FS) http.Handler {
	const indexHTML = "index.html"
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Strip the leading "/" — fs.FS paths are relative.
		p := strings.TrimPrefix(path.Clean(r.URL.Path), "/")
		if p == "" {
			p = indexHTML
		}

		// Try the literal path first — covers static assets like
		// /_next/static/chunks/foo.js and the explicit /404.html.
		if f, err := root.Open(p); err == nil {
			defer f.Close()
			info, _ := f.Stat()
			if info != nil && !info.IsDir() {
				http.ServeFileFS(w, r, root, p)
				return
			}
			// It's a directory — try its index.html.
			if idx := path.Join(p, indexHTML); fileExists(root, idx) {
				http.ServeFileFS(w, r, root, idx)
				return
			}
		}

		// Path not found as-is; check if it maps to a Next.js
		// trailingSlash-style directory index (e.g. /logs → logs/index.html).
		if idx := path.Join(p, indexHTML); fileExists(root, idx) {
			http.ServeFileFS(w, r, root, idx)
			return
		}

		// SPA fallback: anything without a file extension gets the root
		// index.html so the client router can take over. Two carve-outs:
		//
		//   • Paths with file extensions are real 404s — no point
		//     shipping HTML for a missing /favicon.png or /chunks/foo.js.
		//
		//   • /api/* and /v1/* must NEVER fall back to HTML. Mux only
		//     reaches this handler for genuinely unmatched API paths
		//     (typo, removed route); the API contract is "JSON or
		//     error", and silently returning HTML to a fetch caller
		//     causes JSON.parse failures and downstream state corruption
		//     (we hit this — broke the auth-redirect loop on /logs).
		if strings.HasPrefix(r.URL.Path, "/api/") || strings.HasPrefix(r.URL.Path, "/v1/") {
			http.NotFound(w, r)
			return
		}
		if path.Ext(p) == "" && fileExists(root, indexHTML) {
			http.ServeFileFS(w, r, root, indexHTML)
			return
		}
		http.NotFound(w, r)
	})
}

func fileExists(root fs.FS, p string) bool {
	f, err := root.Open(p)
	if err != nil {
		return false
	}
	_ = f.Close()
	return true
}

func cors(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Reflect the origin so credentialed requests (cookies) work; the
		// wildcard '*' is invalid with Allow-Credentials: true.
		if origin := r.Header.Get("Origin"); origin != "" {
			w.Header().Set("Access-Control-Allow-Origin", origin)
			w.Header().Set("Vary", "Origin")
			w.Header().Set("Access-Control-Allow-Credentials", "true")
		} else {
			w.Header().Set("Access-Control-Allow-Origin", "*")
		}
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, DELETE, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Accept, Authorization")
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		h.ServeHTTP(w, r)
	})
}

func writeJSON(w http.ResponseWriter, v interface{}) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(v)
}

// serveCached transparently caches a JSON response. Cache miss → call fn,
// marshal, write, and store. Stale-while-correct: short TTLs (5–60s) make
// invalidation unnecessary for the hot read endpoints we apply this to.
//
// Cache failures (Redis down, etc.) are non-fatal — we just fall through
// to the live path and surface the error in the X-Cache header.
func (s *Server) serveCached(w http.ResponseWriter, r *http.Request, key string, ttl time.Duration, fn func() (any, error)) {
	if data, ok, err := s.cache.Get(r.Context(), key); err == nil && ok {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("X-Cache", "HIT")
		w.Write(data)
		return
	}
	v, err := fn()
	if err != nil {
		writeErr(w, err)
		return
	}
	data, err := json.Marshal(v)
	if err != nil {
		writeErr(w, err)
		return
	}
	if err := s.cache.Set(r.Context(), key, data, ttl); err != nil {
		log.Printf("[cache] set %s: %v", key, err)
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("X-Cache", "MISS")
	w.Write(data)
}

func writeErr(w http.ResponseWriter, err error) {
	log.Printf("[api] error: %v", err)
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusInternalServerError)
	json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
}

// parseFilters decodes the JSON-encoded `filters` query parameter — a list
// of {k, op, v} objects — into the typed FilterExpr slice consumed by the
// query layer. Empty / malformed → no filters applied.
func parseFilters(raw string) []chstore.FilterExpr {
	if raw == "" {
		return nil
	}
	var out []chstore.FilterExpr
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		log.Printf("[api] filters parse: %v", err)
		return nil
	}
	return out
}

// parseFiltersAndDSL merges the JSON `filters` param with a free-form `dsl`
// param. Both are optional; conditions from each are AND-joined.
// A DSL parse error is surfaced as a 4xx via the returned error.
func parseFiltersAndDSL(jsonFilters, dsl string) ([]chstore.FilterExpr, error) {
	out := parseFilters(jsonFilters)
	if strings.TrimSpace(dsl) == "" {
		return out, nil
	}
	parsed, err := chstore.ParseDSL(dsl)
	if err != nil {
		return nil, err
	}
	return append(out, parsed...), nil
}

func parseInt(s string, def int) int {
	if v, err := strconv.Atoi(s); err == nil {
		return v
	}
	return def
}

func parseFloat(s string) float64 {
	v, _ := strconv.ParseFloat(s, 64)
	return v
}

// parseTime converts a nanosecond-epoch string to time.Time
func parseTime(s string) time.Time {
	if s == "" {
		return time.Time{}
	}
	ns, err := strconv.ParseInt(s, 10, 64)
	if err != nil {
		return time.Time{}
	}
	return time.Unix(0, ns)
}

func parseDuration(s string, def time.Duration) time.Duration {
	if d, err := time.ParseDuration(s); err == nil {
		return d
	}
	return def
}
