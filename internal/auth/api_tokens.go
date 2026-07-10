package auth

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"log"
	"strings"
	"sync"
	"time"
)

// api_tokens.go — v0.8.444 servis token kimlik doğrulaması. Bearer
// değeri "cmk_" ile başlıyorsa JWT parse hiç denenmez; token'ın sha256
// hash'i bellek-içi cache'te aranır. Cache 30 sn'de bir chstore'dan
// tazelenir (multi-pod senkronu copilot config-refresh deseni) ve
// create/revoke anında invalidate edilir — istek yolunda CH sorgusu
// YOKTUR (auth p99'u etkilenmez).

// TokenInfo — cache'teki tek kayıt (chstore.APIToken'ın auth dilimi;
// import döngüsü olmasın diye kendi tipi).
type TokenInfo struct {
	ID   string
	Name string
	Role string
}

// TokenSource — chstore'un ihtiyaç duyulan dilimi.
type TokenSource interface {
	ActiveHashes(ctx context.Context) (map[string]TokenInfo, error)
}

type tokenCache struct {
	mu     sync.RWMutex
	byHash map[string]TokenInfo
	src    TokenSource
}

// EnableAPITokens — boot'ta bağlar ve tazeleme döngüsünü başlatır.
func (s *Service) EnableAPITokens(ctx context.Context, src TokenSource) {
	s.mu.Lock()
	s.tokens = &tokenCache{src: src, byHash: map[string]TokenInfo{}}
	s.mu.Unlock()
	s.RefreshAPITokens(ctx)
	go func() {
		t := time.NewTicker(30 * time.Second)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				s.RefreshAPITokens(ctx)
			}
		}
	}()
}

// RefreshAPITokens — cache'i kaynaktan yeniler; create/revoke sonrası
// API handler'ı da çağırır (anında etki).
func (s *Service) RefreshAPITokens(ctx context.Context) {
	s.mu.RLock()
	tc := s.tokens
	s.mu.RUnlock()
	if tc == nil {
		return
	}
	m, err := tc.src.ActiveHashes(ctx)
	if err != nil {
		log.Printf("[auth] api-token refresh: %v", err)
		return
	}
	tc.mu.Lock()
	tc.byHash = m
	tc.mu.Unlock()
}

// apiTokenClaims — cmk_ token'ı Claims'e çözer. Bulunamazsa nil (401'e
// düşer). HashFn parametresi yok: hash chstore ile AYNI algoritma
// olmalı — LookupHash string'i chstore.HashAPIToken üretir; auth
// katmanı hash'lemeyi kaynağa bırakmamak için kendi sha256'sını
// KULLANMAZ, Lookup düz token alır ve kaynak-hash map'inde arar.
func (s *Service) apiTokenClaims(plain string) *Claims {
	s.mu.RLock()
	tc := s.tokens
	s.mu.RUnlock()
	if tc == nil {
		return nil
	}
	h := hashToken(plain)
	tc.mu.RLock()
	info, ok := tc.byHash[h]
	tc.mu.RUnlock()
	if !ok {
		return nil
	}
	return &Claims{
		UserID: "token:" + info.ID,
		Email:  "token:" + info.Name,
		Role:   info.Role,
	}
}

// IsAPIToken — Bearer değerinin servis token'ı olup olmadığı.
func IsAPIToken(v string) bool { return strings.HasPrefix(v, "cmk_") }

// hashToken — chstore.HashAPIToken ile bit-uyumlu (sha256 hex). İki
// pakette de aynı algoritmanın durması bilinçli: auth chstore'u import
// EDEMEZ (döngü); uyum apiTokenHashParityTest ile pinli.
func hashToken(plain string) string {
	h := sha256.Sum256([]byte(plain))
	return hex.EncodeToString(h[:])
}
