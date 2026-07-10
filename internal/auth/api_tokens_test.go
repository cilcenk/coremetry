package auth

import (
	"context"
	"testing"

	"github.com/cilcenk/coremetry/internal/chstore"
)

// api_tokens_test.go — v0.8.444. cmk_ yolunun kabul/ret tablosu +
// revoke'un cache tazelemesiyle anında etkisi.
type fakeTokenSource struct{ m map[string]TokenInfo }

func (f *fakeTokenSource) ActiveHashes(context.Context) (map[string]TokenInfo, error) {
	return f.m, nil
}

func TestAPITokenClaims(t *testing.T) {
	s := &Service{}
	plain := "cmk_deadbeef"
	src := &fakeTokenSource{m: map[string]TokenInfo{
		hashToken(plain): {ID: "t1", Name: "studio-agent", Role: "viewer"},
	}}
	s.EnableAPITokens(context.Background(), src)

	c := s.apiTokenClaims(plain)
	if c == nil || c.Role != "viewer" || c.Email != "token:studio-agent" {
		t.Fatalf("claims: %+v", c)
	}
	if s.apiTokenClaims("cmk_wrong") != nil {
		t.Fatal("unknown token must be rejected")
	}
	if !IsAPIToken(plain) || IsAPIToken("eyJhbGciOi...") {
		t.Fatal("IsAPIToken prefix ayrımı bozuk")
	}

	// revoke: kaynak boşalır → refresh → anında ret
	src.m = map[string]TokenInfo{}
	s.RefreshAPITokens(context.Background())
	if s.apiTokenClaims(plain) != nil {
		t.Fatal("revoked token must be rejected after refresh")
	}
}

// Hash paritesi — auth.hashToken chstore.HashAPIToken ile bit-uyumlu
// KALMALI (iki paket import döngüsü yüzünden ayrı kopya taşıyor).
func TestAPITokenHashParity(t *testing.T) {
	const sample = "cmk_0123456789abcdef"
	if hashToken(sample) != chstore.HashAPIToken(sample) {
		t.Fatal("auth.hashToken ile chstore.HashAPIToken ayrıştı — token doğrulaması kırılır")
	}
}
