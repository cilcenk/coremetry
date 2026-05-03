package api

import (
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
	"strconv"
	"strings"
	"time"

	"github.com/cenk/qmetry/internal/auth"
	"github.com/cenk/qmetry/internal/cache"
	"github.com/cenk/qmetry/internal/chstore"
	"github.com/cenk/qmetry/internal/otlp"
	"github.com/cenk/qmetry/internal/profileconv"
)

type Server struct {
	addr  string
	store *chstore.Store
	ing   *otlp.Ingester
	webFS embed.FS
	auth  *auth.Service
	oidc  *auth.OIDCService // nil when SSO disabled
	cache cache.Cache       // Noop when Redis isn't configured
}

func NewServer(addr string, ing *otlp.Ingester, store *chstore.Store, webFS embed.FS, authSvc *auth.Service, oidcSvc *auth.OIDCService, c cache.Cache) *Server {
	return &Server{addr: addr, store: store, ing: ing, webFS: webFS, auth: authSvc, oidc: oidcSvc, cache: c}
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
	mux.HandleFunc("GET    /api/services/{name}/callers",  s.svcCallers)
	mux.HandleFunc("GET    /api/services/{name}/callees",  s.svcCallees)
	mux.HandleFunc("GET    /api/problems",                  s.listProblems)
	mux.HandleFunc("GET    /api/alert-rules",               s.listAlertRules)
	mux.HandleFunc("POST   /api/alert-rules",               auth.RequireRole(auth.RoleAdmin, s.createAlertRule))
	mux.HandleFunc("DELETE /api/alert-rules/{id}",          auth.RequireRole(auth.RoleAdmin, s.deleteAlertRule))
	mux.HandleFunc("POST   /api/alert-rules/{id}/enable",   auth.RequireRole(auth.RoleAdmin, s.enableAlertRule))
	mux.HandleFunc("GET /api/health", s.getHealth)

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

	// User management (admin only)
	mux.HandleFunc("GET    /api/users",                  auth.RequireRole(auth.RoleAdmin, s.listUsers))
	mux.HandleFunc("POST   /api/users",                  auth.RequireRole(auth.RoleAdmin, s.createUser))
	mux.HandleFunc("DELETE /api/users/{id}",             auth.RequireRole(auth.RoleAdmin, s.deleteUser))
	mux.HandleFunc("POST   /api/users/{id}/password",    auth.RequireRole(auth.RoleAdmin, s.resetUserPassword))

	// Tempo-compatible API (Grafana datasource integration)
	s.registerTempoRoutes(mux)

	// Web UI (embedded Next.js static export)
	sub, _ := fs.Sub(s.webFS, "frontend/out")
	mux.Handle("/", http.FileServer(http.FS(sub)))

	log.Printf("[api] HTTP listening on %s", s.addr)
	return http.ListenAndServe(s.addr, cors(s.auth.Middleware(mux)))
}

// ── Handlers ──────────────────────────────────────────────────────────────────

func (s *Server) getServices(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	since := parseDuration(q.Get("since"), 24*time.Hour)
	from, to := parseTime(q.Get("from")), parseTime(q.Get("to"))
	key := fmt.Sprintf("services:since=%s:from=%s:to=%s", q.Get("since"), q.Get("from"), q.Get("to"))
	s.serveCached(w, r, key, 5*time.Second, func() (any, error) {
		return s.store.GetServices(r.Context(), since, from, to)
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
	traces, total, err := s.store.GetTraces(r.Context(), f)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, map[string]interface{}{"total": total, "traces": traces})
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
	f := chstore.LogFilter{
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
	logs, total, err := s.store.GetLogs(r.Context(), f)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, map[string]interface{}{"total": total, "logs": logs})
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
	service := r.Header.Get("X-Qmetry-Service")
	if service == "" {
		service = "unknown"
	}
	host := r.Header.Get("X-Qmetry-Host")
	ptype := r.Header.Get("X-Qmetry-Profile-Type")
	if ptype == "" {
		ptype = "cpu"
	}
	durNs, _ := strconv.ParseInt(r.Header.Get("X-Qmetry-Duration-Ns"), 10, 64)
	startNs, _ := strconv.ParseInt(r.Header.Get("X-Qmetry-Start-Time-Ns"), 10, 64)
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
func (s *Server) authConfig(w http.ResponseWriter, r *http.Request) {
	s.serveCached(w, r, "auth-config", 60*time.Second, func() (any, error) {
		resp := map[string]interface{}{
			"local": map[string]bool{"enabled": true},
			"oidc":  map[string]interface{}{"enabled": false},
		}
		if s.oidc.Enabled() {
			resp["oidc"] = map[string]interface{}{
				"enabled":     true,
				"displayName": s.oidc.DisplayName(),
			}
		}
		return resp, nil
	})
}

// ── OIDC sign-in ─────────────────────────────────────────────────────────────

const (
	oidcStateCookie    = "qmetry_oidc_state"
	oidcNonceCookie    = "qmetry_oidc_nonce"
	oidcVerifierCookie = "qmetry_oidc_verifier"
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

// ── Middleware & helpers ───────────────────────────────────────────────────────

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
