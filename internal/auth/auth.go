// Package auth implements local username/password auth + JWT issuing for
// the Qmetry HTTP API. It deliberately avoids external session storage —
// JWTs are stateless and self-contained.
package auth

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"golang.org/x/crypto/bcrypt"
)

const (
	RoleAdmin  = "admin"
	RoleViewer = "viewer"

	// CookieName is set on login and cleared on logout. It must be the
	// same on the frontend (login fetch uses credentials: 'include').
	CookieName = "qmetry_session"
)

type ctxKey string

const userCtxKey ctxKey = "qmetry.user"

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
}

// NewService is the constructor. If secret is empty a random one is
// generated — fine for first-run dev, but logged so operators can pin it.
func NewService(secret string, ttl time.Duration) *Service {
	if secret == "" {
		b := make([]byte, 32)
		_, _ = rand.Read(b)
		secret = hex.EncodeToString(b)
		log.Printf("[auth] no jwt_secret configured — generated ephemeral key (sessions will not survive restarts; set QMETRY_JWT_SECRET in production)")
	}
	if ttl == 0 {
		ttl = 24 * time.Hour
	}
	return &Service{secret: []byte(secret), ttl: ttl}
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
			Issuer:    "qmetry",
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
		"/api/health":
		return true
	}
	if strings.HasPrefix(path, "/v1/traces") ||
		strings.HasPrefix(path, "/v1/logs") ||
		strings.HasPrefix(path, "/v1/metrics") ||
		strings.HasPrefix(path, "/v1/profiles") {
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
func (s *Service) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if SkipPath(r.Method, r.URL.Path) {
			next.ServeHTTP(w, r)
			return
		}

		token := tokenFromRequest(r)
		if token == "" {
			writeUnauth(w, "missing credentials")
			return
		}
		claims, err := s.Parse(token)
		if err != nil {
			writeUnauth(w, "invalid or expired token")
			return
		}
		ctx := context.WithValue(r.Context(), userCtxKey, claims)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
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
