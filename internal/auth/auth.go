// Package auth implements local username/password auth + JWT issuing for
// the Coremetry HTTP API. It deliberately avoids external session storage —
// JWTs are stateless and self-contained.
package auth

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"golang.org/x/crypto/bcrypt"
)

const (
	// RoleAdmin can do everything (user mgmt, settings, all CRUD).
	// RoleEditor can create/edit content (dashboards, monitors,
	//            alert rules, incidents) but not user mgmt or system
	//            settings.
	// RoleViewer is read-only.
	RoleAdmin  = "admin"
	RoleEditor = "editor"
	RoleViewer = "viewer"

	// CookieName is set on login and cleared on logout. It must be the
	// same on the frontend (login fetch uses credentials: 'include').
	CookieName = "coremetry_session"
)

// IsValidRole reports whether s is one of the canonical role strings.
// Used by handlers that accept a role from outside (admin upserting
// users, LDAP group→role mapping).
func IsValidRole(s string) bool {
	return s == RoleAdmin || s == RoleEditor || s == RoleViewer
}

type ctxKey string

const userCtxKey ctxKey = "coremetry.user"

// Claims is the payload embedded in every JWT.
type Claims struct {
	UserID string `json:"uid"`
	Email  string `json:"email"`
	Role   string `json:"role"`
	jwt.RegisteredClaims
}

// Service issues, validates and exposes JWTs.
type Service struct {
	secret []byte
	ttl    time.Duration

	// Trusted-header auth — optional. When non-nil and Enabled,
	// the Middleware accepts identity from upstream-proxy
	// headers (oauth2-proxy / IAP / Cloudflare Access) on the
	// fallback path. Guarded by trustedCIDRs so a request from
	// outside the proxy mesh can't spoof the header.
	mu             sync.RWMutex
	trustedHeader  *TrustedHeaderOptions
	trustedCIDRs   []*net.IPNet
	userStore      UserLookup
	// customRoles — operator-defined subsets of viewer's page
	// access, loaded from system_settings at boot. Mutations go
	// through Upsert/Delete which re-persist atomically. Empty
	// map = no custom roles configured (default).
	customRoles    map[string]CustomRole
}

// TrustedHeaderOptions is the auth.Service-side mirror of the
// config.TrustedHeaderConfig. Kept separate so the auth package
// stays free of the internal/config import cycle.
type TrustedHeaderOptions struct {
	Enabled       bool
	EmailHeader   string
	UserHeader    string
	GroupsHeader  string
	AutoProvision bool
	DefaultRole   string
}

// UserLookup is the small store interface the trusted-header
// path needs — find an existing user by email, or upsert a new
// row when AutoProvision is on. Implemented by *chstore.Store
// (the API layer wires it in main.go).
type UserLookup interface {
	GetUserByEmail(ctx context.Context, email string) (*LookupUser, error)
	UpsertUser(ctx context.Context, u LookupUser) error
}

// LookupUser is the minimal shape exchanged through UserLookup.
// chstore.User has more fields (password hash, created_at,
// etc.) that the trusted-header path doesn't touch, so the
// interface only requires this slice.
type LookupUser struct {
	ID    string
	Email string
	Role  string
}

// NewService is the constructor. If secret is empty a random one is
// generated — fine for first-run dev, but logged so operators can pin it.
func NewService(secret string, ttl time.Duration) *Service {
	if secret == "" {
		b := make([]byte, 32)
		_, _ = rand.Read(b)
		secret = hex.EncodeToString(b)
		log.Printf("[auth] no jwt_secret configured — generated ephemeral key (sessions will not survive restarts; set COREMETRY_JWT_SECRET in production)")
	}
	if ttl == 0 {
		ttl = 24 * time.Hour
	}
	return &Service{secret: []byte(secret), ttl: ttl}
}

// EnableTrustedHeader turns on oauth2-proxy / IAP header trust.
// trustedProxies is a list of CIDR strings (e.g. "10.0.0.0/8",
// "172.16.0.0/12") — the headers are honoured only when the
// request originates from one of these blocks. An empty list
// is a config error: it'd let any caller spoof the email
// header. Returns an error in that case so main.go can fail
// fast at boot instead of silently leaving the door open.
func (s *Service) EnableTrustedHeader(opts TrustedHeaderOptions, trustedProxies []string, store UserLookup) error {
	if !opts.Enabled {
		return nil
	}
	if store == nil {
		return errors.New("trusted-header auth: UserLookup is required")
	}
	if len(trustedProxies) == 0 {
		return errors.New("trusted-header auth: trusted_proxies must list at least one CIDR (refusing to honour headers from any source)")
	}
	cidrs := make([]*net.IPNet, 0, len(trustedProxies))
	for _, p := range trustedProxies {
		_, n, err := net.ParseCIDR(strings.TrimSpace(p))
		if err != nil {
			return fmt.Errorf("trusted-header auth: invalid CIDR %q: %w", p, err)
		}
		cidrs = append(cidrs, n)
	}
	// Defaults — oauth2-proxy's outgoing header set.
	if opts.EmailHeader == "" {
		opts.EmailHeader = "X-Auth-Request-Email"
	}
	if opts.UserHeader == "" {
		opts.UserHeader = "X-Auth-Request-User"
	}
	if opts.GroupsHeader == "" {
		opts.GroupsHeader = "X-Auth-Request-Groups"
	}
	if opts.DefaultRole == "" {
		opts.DefaultRole = RoleViewer
	}
	if !IsValidRole(opts.DefaultRole) {
		return fmt.Errorf("trusted-header auth: invalid default_role %q", opts.DefaultRole)
	}
	s.mu.Lock()
	s.trustedHeader = &opts
	s.trustedCIDRs = cidrs
	s.userStore = store
	s.mu.Unlock()
	log.Printf("[auth] trusted-header mode enabled (header=%s, trusted_proxies=%v, auto_provision=%v)",
		opts.EmailHeader, trustedProxies, opts.AutoProvision)
	return nil
}

// Issue signs a JWT for the given identity.
func (s *Service) Issue(userID, email, role string) (string, time.Time, error) {
	exp := time.Now().Add(s.ttl)
	c := Claims{
		UserID: userID,
		Email:  email,
		Role:   role,
		RegisteredClaims: jwt.RegisteredClaims{
			ExpiresAt: jwt.NewNumericDate(exp),
			IssuedAt:  jwt.NewNumericDate(time.Now()),
			Issuer:    "coremetry",
			Subject:   userID,
		},
	}
	tok := jwt.NewWithClaims(jwt.SigningMethodHS256, c)
	str, err := tok.SignedString(s.secret)
	return str, exp, err
}

// Parse validates a JWT and returns its claims.
func (s *Service) Parse(token string) (*Claims, error) {
	t, err := jwt.ParseWithClaims(token, &Claims{}, func(t *jwt.Token) (interface{}, error) {
		if t.Method.Alg() != jwt.SigningMethodHS256.Alg() {
			return nil, fmt.Errorf("unexpected signing method: %v", t.Header["alg"])
		}
		return s.secret, nil
	})
	if err != nil {
		return nil, err
	}
	c, ok := t.Claims.(*Claims)
	if !ok || !t.Valid {
		return nil, errors.New("invalid token")
	}
	return c, nil
}

// TTL returns the configured token lifetime — needed for cookie MaxAge.
func (s *Service) TTL() time.Duration { return s.ttl }

// HashPassword wraps bcrypt with the default cost.
func HashPassword(plain string) (string, error) {
	b, err := bcrypt.GenerateFromPassword([]byte(plain), bcrypt.DefaultCost)
	return string(b), err
}

// CheckPassword is the constant-time bcrypt compare.
func CheckPassword(hash, plain string) bool {
	return bcrypt.CompareHashAndPassword([]byte(hash), []byte(plain)) == nil
}

// FromContext returns the authenticated claims set by Middleware.
// Handlers behind the middleware can rely on the value being non-nil.
func FromContext(ctx context.Context) *Claims {
	c, _ := ctx.Value(userCtxKey).(*Claims)
	return c
}

// SkipPath reports whether a path should bypass authentication.
// OTLP ingest endpoints stay open so SDKs without auth headers continue
// to work; health is open for liveness probes; auth/login + OIDC routes
// are the entry points so they cannot themselves require auth.
func SkipPath(method, path string) bool {
	if method == http.MethodOptions {
		return true
	}
	switch path {
	case "/api/auth/login",
		"/api/auth/config",
		"/api/auth/oidc/start",
		"/api/auth/oidc/callback",
		"/api/health",
		"/api/version":
		return true
	}
	// /api/branding is public on GET so the login page (which
	// renders before the operator has a session) can pull the
	// custom logo + strings. PUT goes through auth + admin gate
	// — match method explicitly so we don't accidentally let
	// unauthenticated writes through.
	if path == "/api/branding" && method == http.MethodGet {
		return true
	}
	if strings.HasPrefix(path, "/v1/traces") ||
		strings.HasPrefix(path, "/v1/logs") ||
		strings.HasPrefix(path, "/v1/metrics") ||
		strings.HasPrefix(path, "/v1/profiles") {
		return true
	}
	// Trace snapshots — Grafana-style "share publicly" links. The
	// random token in the path is the security boundary; same
	// threat model as the heartbeat ingest below.
	if strings.HasPrefix(path, "/api/public/trace/") {
		return true
	}
	// Heartbeat ingest is unauth'd by design — cron jobs / batch
	// scripts hit this with `curl ${URL}` and the random token in
	// the path is the security boundary. Same threat model as a
	// signed S3 URL.
	if strings.HasPrefix(path, "/api/heartbeats/") {
		return true
	}
	// Public status page — read endpoint + subscriber sign-up are
	// the customer-facing entry points; the whole point is that
	// they're reachable without a Coremetry account.
	if path == "/api/public-status" || path == "/api/public-status/subscribe" {
		return true
	}
	// Anything outside /api/* is the static UI — let the browser fetch it
	// so the login page itself can load.
	if !strings.HasPrefix(path, "/api/") {
		return true
	}
	return false
}

// Middleware enforces a valid JWT (cookie or Bearer) for every protected
// endpoint. Failures return 401 with a JSON error so the SPA can redirect.
//
// Trusted-header fallback: when EnableTrustedHeader was called at boot
// AND the JWT path fails AND the request originates from a configured
// trusted-proxy CIDR, the email header is honoured. The middleware
// looks up (or auto-provisions, if enabled) a user and mints a JWT
// cookie inline so the rest of the SPA's stateful flow keeps working.
func (s *Service) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if SkipPath(r.Method, r.URL.Path) {
			next.ServeHTTP(w, r)
			return
		}

		token := tokenFromRequest(r)
		if token != "" {
			if claims, err := s.Parse(token); err == nil {
				ctx := context.WithValue(r.Context(), userCtxKey, claims)
				next.ServeHTTP(w, r.WithContext(ctx))
				return
			}
		}

		// Trusted-header fallback — only consulted when JWT was
		// missing or invalid. Avoids the lookup cost on every
		// authenticated request.
		if claims, ok := s.trustedHeaderClaims(r.Context(), r); ok {
			s.setSessionCookie(w, claims)
			ctx := context.WithValue(r.Context(), userCtxKey, claims)
			next.ServeHTTP(w, r.WithContext(ctx))
			return
		}

		if token == "" {
			writeUnauth(w, "missing credentials")
		} else {
			writeUnauth(w, "invalid or expired token")
		}
	})
}

// trustedHeaderClaims is the trusted-header read path. Returns
// (claims, true) when the upstream proxy supplied a recognised
// email + the request comes from a trusted CIDR. Returns
// (nil, false) otherwise — caller falls through to the 401.
func (s *Service) trustedHeaderClaims(ctx context.Context, r *http.Request) (*Claims, bool) {
	s.mu.RLock()
	opts := s.trustedHeader
	cidrs := s.trustedCIDRs
	store := s.userStore
	s.mu.RUnlock()
	if opts == nil || !opts.Enabled || store == nil {
		return nil, false
	}
	if !s.requestFromTrustedProxy(r, cidrs) {
		return nil, false
	}
	email := strings.TrimSpace(r.Header.Get(opts.EmailHeader))
	if email == "" {
		return nil, false
	}
	email = strings.ToLower(email)
	u, err := store.GetUserByEmail(ctx, email)
	if err != nil {
		log.Printf("[auth] trusted-header lookup for %q: %v", email, err)
		return nil, false
	}
	if u == nil {
		if !opts.AutoProvision {
			return nil, false
		}
		// Newly-seen email — provision with DefaultRole.
		u = &LookupUser{
			ID:    newRandID(8),
			Email: email,
			Role:  opts.DefaultRole,
		}
		if err := store.UpsertUser(ctx, *u); err != nil {
			log.Printf("[auth] trusted-header provision %q: %v", email, err)
			return nil, false
		}
		log.Printf("[auth] trusted-header auto-provisioned user %q with role %q", email, opts.DefaultRole)
	}
	exp := time.Now().Add(s.ttl)
	c := &Claims{
		UserID: u.ID, Email: u.Email, Role: u.Role,
		RegisteredClaims: jwt.RegisteredClaims{
			ExpiresAt: jwt.NewNumericDate(exp),
			IssuedAt:  jwt.NewNumericDate(time.Now()),
			Issuer:    "coremetry",
			Subject:   u.ID,
		},
	}
	return c, true
}

// requestFromTrustedProxy checks the client's source IP (after
// honouring X-Forwarded-For ONLY when the immediate hop is
// already trusted) against the trusted-proxy CIDR list.
//
// Why the two-step XFF handling: a malicious caller can set any
// X-Forwarded-For value, so blindly trusting it would defeat
// the whole gate. We trust the XFF chain only when the direct
// TCP peer (RemoteAddr) is itself in the trusted set — at
// that point we know oauth2-proxy is the immediate hop and the
// XFF chain reflects its truthful "original client" record.
func (s *Service) requestFromTrustedProxy(r *http.Request, cidrs []*net.IPNet) bool {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr // RemoteAddr may already be bare in some test contexts
	}
	ip := net.ParseIP(strings.TrimSpace(host))
	if ip == nil {
		return false
	}
	for _, c := range cidrs {
		if c.Contains(ip) {
			return true
		}
	}
	return false
}

// setSessionCookie mints + writes a JWT cookie for a freshly-
// authenticated trusted-header user so subsequent requests
// don't re-do the lookup. Cookie attributes mirror the local
// login flow.
func (s *Service) setSessionCookie(w http.ResponseWriter, c *Claims) {
	tok := jwt.NewWithClaims(jwt.SigningMethodHS256, c)
	signed, err := tok.SignedString(s.secret)
	if err != nil {
		return
	}
	http.SetCookie(w, &http.Cookie{
		Name:     CookieName,
		Value:    signed,
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		Expires:  c.ExpiresAt.Time,
	})
}

// newRandID is a tiny hex-id generator for auto-provisioned
// users. Same shape as api.newID(8); duplicated here so the
// auth package doesn't import internal/api (cycle).
func newRandID(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// RequireRole gates a handler on a specific role (typically "admin").
func RequireRole(role string, h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		c := FromContext(r.Context())
		if c == nil || c.Role != role {
			writeUnauth(w, "insufficient role")
			return
		}
		h(w, r)
	}
}

// RequireAnyRole accepts any of the listed roles. Used for routes
// that should be open to admin and editor (dashboard/monitor CRUD
// etc.) — admin-only routes still use RequireRole for clarity.
func RequireAnyRole(roles []string, h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		c := FromContext(r.Context())
		if c == nil {
			writeUnauth(w, "insufficient role")
			return
		}
		for _, role := range roles {
			if c.Role == role {
				h(w, r)
				return
			}
		}
		writeUnauth(w, "insufficient role")
	}
}

func tokenFromRequest(r *http.Request) string {
	if c, err := r.Cookie(CookieName); err == nil && c.Value != "" {
		return c.Value
	}
	if h := r.Header.Get("Authorization"); strings.HasPrefix(h, "Bearer ") {
		return strings.TrimPrefix(h, "Bearer ")
	}
	return ""
}

func writeUnauth(w http.ResponseWriter, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusUnauthorized)
	_, _ = w.Write([]byte(`{"error":"` + msg + `"}`))
}
