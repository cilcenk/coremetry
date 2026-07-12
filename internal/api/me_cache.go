package api

import (
	"sync"
	"time"

	"github.com/cilcenk/coremetry/internal/chstore"
)

// meCache — v0.8.519 (perf raporu #7): /api/auth/me her çağrıda
// GetUserByID (FINAL'lı ReplacingMergeTree okuması) yapıyordu ve bu
// çağrı HER cold tab açılışının seri kapısı: SPA me() bitmeden route
// verisi istemiyor. 30s TTL'li küçük in-memory cache; JWT zaten base
// role taşıdığından customRole değişiminin ≤30s gecikmesi kabul
// edilebilir. Her kullanıcı-yazma yolu clear() çağırır (site
// kaçırmamak için anahtar bazlı değil TOPTAN temizlik — yazmalar
// nadir admin işlemleri).
//
// Saf ve test edilebilir: now dışarıdan enjekte edilir.
type meCache struct {
	mu  sync.Mutex
	ttl time.Duration
	m   map[string]meCacheEntry
}

type meCacheEntry struct {
	u  *chstore.User
	at time.Time
}

func newMeCache(ttl time.Duration) *meCache {
	return &meCache{ttl: ttl, m: map[string]meCacheEntry{}}
}

func (c *meCache) get(id string, now time.Time) (*chstore.User, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.m[id]
	if !ok || now.Sub(e.at) > c.ttl {
		return nil, false
	}
	return e.u, true
}

func (c *meCache) put(id string, u *chstore.User, now time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	// Sınır: patolojik durumda (ör. token-fabrikası) sınırsız büyüme
	// olmasın; 10k üstünde sıfırla — normal kurulumda erişilmez.
	if len(c.m) > 10_000 {
		c.m = map[string]meCacheEntry{}
	}
	c.m[id] = meCacheEntry{u: u, at: now}
}

func (c *meCache) clear() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.m = map[string]meCacheEntry{}
}
