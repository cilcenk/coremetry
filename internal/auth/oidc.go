package auth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"

	"github.com/coreos/go-oidc/v3/oidc"
	"golang.org/x/oauth2"

	"github.com/cenk/qmetry/internal/config"
)

// OIDCService is the optional SSO layer. nil when disabled — every call
// site checks Enabled() before invoking other methods.
type OIDCService struct {
	cfg      config.OIDCConfig
	provider *oidc.Provider
	verifier *oidc.IDTokenVerifier
	oauth    oauth2.Config
}

// NewOIDCService runs OIDC discovery against the issuer. Returns (nil, nil)
// when disabled. Returns an error when enabled but the issuer is
// unreachable / misconfigured — the caller decides whether to abort startup
// or just log and continue with local-only auth.
func NewOIDCService(ctx context.Context, cfg config.OIDCConfig) (*OIDCService, error) {
	if !cfg.Enabled {
		return nil, nil
	}
	missing := []string{}
	if cfg.IssuerURL == "" {
		missing = append(missing, "issuer_url")
	}
	if cfg.ClientID == "" {
		missing = append(missing, "client_id")
	}
	if cfg.ClientSecret == "" {
		missing = append(missing, "client_secret")
	}
	if cfg.RedirectURL == "" {
		missing = append(missing, "redirect_url")
	}
	if len(missing) > 0 {
		return nil, fmt.Errorf("oidc enabled but missing: %s", strings.Join(missing, ", "))
	}
	prov, err := oidc.NewProvider(ctx, cfg.IssuerURL)
	if err != nil {
		return nil, fmt.Errorf("oidc discovery (%s): %w", cfg.IssuerURL, err)
	}
	return &OIDCService{
		cfg:      cfg,
		provider: prov,
		verifier: prov.Verifier(&oidc.Config{ClientID: cfg.ClientID}),
		oauth: oauth2.Config{
			ClientID:     cfg.ClientID,
			ClientSecret: cfg.ClientSecret,
			RedirectURL:  cfg.RedirectURL,
			Endpoint:     prov.Endpoint(),
			Scopes:       cfg.Scopes,
		},
	}, nil
}

// Enabled is true when the service is configured. Safe on a nil receiver.
func (o *OIDCService) Enabled() bool       { return o != nil }
func (o *OIDCService) DisplayName() string { return o.cfg.DisplayName }
func (o *OIDCService) DefaultRole() string { return o.cfg.DefaultRole }

// AuthURL builds the IdP redirect URL with PKCE + nonce + state.
func (o *OIDCService) AuthURL(state, nonce, codeChallenge string) string {
	return o.oauth.AuthCodeURL(state,
		oidc.Nonce(nonce),
		oauth2.SetAuthURLParam("code_challenge", codeChallenge),
		oauth2.SetAuthURLParam("code_challenge_method", "S256"),
	)
}

// OIDCClaims is the subset of id_token claims we care about.
type OIDCClaims struct {
	Email         string `json:"email"`
	EmailVerified bool   `json:"email_verified"`
	Subject       string `json:"-"`
	Nonce         string `json:"-"`
}

// Exchange completes the auth code flow: token exchange, id_token verify,
// nonce check, claim extraction, domain whitelist.
func (o *OIDCService) Exchange(ctx context.Context, code, codeVerifier, expectedNonce string) (*OIDCClaims, error) {
	tok, err := o.oauth.Exchange(ctx, code,
		oauth2.SetAuthURLParam("code_verifier", codeVerifier),
	)
	if err != nil {
		return nil, fmt.Errorf("token exchange: %w", err)
	}
	rawID, _ := tok.Extra("id_token").(string)
	if rawID == "" {
		return nil, errors.New("id_token missing from token response")
	}
	idTok, err := o.verifier.Verify(ctx, rawID)
	if err != nil {
		return nil, fmt.Errorf("id_token verify: %w", err)
	}
	if expectedNonce != "" && idTok.Nonce != expectedNonce {
		return nil, errors.New("nonce mismatch")
	}
	var c OIDCClaims
	if err := idTok.Claims(&c); err != nil {
		return nil, fmt.Errorf("claims decode: %w", err)
	}
	c.Subject = idTok.Subject
	c.Nonce = idTok.Nonce
	if c.Email == "" {
		return nil, errors.New("id_token has no email claim — request the 'email' scope")
	}
	if !o.AllowEmail(c.Email) {
		return nil, fmt.Errorf("email domain %q is not in the allowlist", domainOf(c.Email))
	}
	return &c, nil
}

// AllowEmail enforces the optional domain whitelist. Empty list = allow all.
func (o *OIDCService) AllowEmail(email string) bool {
	if len(o.cfg.AllowedDomains) == 0 {
		return true
	}
	dom := domainOf(email)
	if dom == "" {
		return false
	}
	for _, d := range o.cfg.AllowedDomains {
		if strings.EqualFold(strings.TrimSpace(d), dom) {
			return true
		}
	}
	return false
}

func domainOf(email string) string {
	at := strings.LastIndex(email, "@")
	if at < 0 {
		return ""
	}
	return strings.ToLower(email[at+1:])
}

// ── PKCE + state helpers ─────────────────────────────────────────────────────

// RandomURLToken returns a base64url-encoded random string of nBytes
// entropy — used for state, nonce, and PKCE code_verifier.
func RandomURLToken(nBytes int) string {
	b := make([]byte, nBytes)
	_, _ = rand.Read(b)
	return base64.RawURLEncoding.EncodeToString(b)
}

// PKCEChallenge returns base64url(SHA256(verifier)) — the S256 method
// from RFC 7636.
func PKCEChallenge(verifier string) string {
	h := sha256.Sum256([]byte(verifier))
	return base64.RawURLEncoding.EncodeToString(h[:])
}
