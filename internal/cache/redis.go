package cache

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/redis/go-redis/v9"
)

// New parses a Redis URL (redis://host:port/db) and returns a working
// Cache+Lock pair. On URL parse error or initial PING failure it falls
// back to the Noop implementation and returns the error so the caller
// can log it — Qmetry should not crash just because Redis is unhealthy.
func New(url string) (Cache, Lock, error) {
	opts, err := redis.ParseURL(url)
	if err != nil {
		c, l := NewNoop()
		return c, l, fmt.Errorf("parse redis url: %w", err)
	}
	cli := redis.NewClient(opts)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := cli.Ping(ctx).Err(); err != nil {
		c, l := NewNoop()
		return c, l, fmt.Errorf("redis ping: %w", err)
	}
	return &redisCache{cli: cli}, &redisLock{cli: cli, tokens: map[string]string{}}, nil
}

// ── Cache ───────────────────────────────────────────────────────────────────

type redisCache struct {
	cli *redis.Client
}

func (r *redisCache) Get(ctx context.Context, key string) ([]byte, bool, error) {
	b, err := r.cli.Get(ctx, key).Bytes()
	if errors.Is(err, redis.Nil) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	return b, true, nil
}

func (r *redisCache) Set(ctx context.Context, key string, value []byte, ttl time.Duration) error {
	return r.cli.Set(ctx, key, value, ttl).Err()
}

func (r *redisCache) Del(ctx context.Context, key string) error {
	return r.cli.Del(ctx, key).Err()
}

// ── Lock ────────────────────────────────────────────────────────────────────

// releaseScript ensures we only delete the key if the value still matches
// the token issued at acquire time. Without this, a slow caller whose
// lease expired could wipe out a fresh holder's lock on Release.
var releaseScript = redis.NewScript(`
if redis.call("GET", KEYS[1]) == ARGV[1] then
  return redis.call("DEL", KEYS[1])
else
  return 0
end`)

type redisLock struct {
	cli *redis.Client

	// In-process token bookkeeping — Release(key) needs to know what we
	// stored, since the token isn't returned to callers (keeps the API
	// simple).
	mu     sync.Mutex
	tokens map[string]string
}

func (r *redisLock) TryAcquire(ctx context.Context, key string, ttl time.Duration) (bool, error) {
	tok, err := randomToken()
	if err != nil {
		return false, err
	}
	ok, err := r.cli.SetNX(ctx, key, tok, ttl).Result()
	if err != nil {
		return false, err
	}
	if !ok {
		return false, nil
	}
	r.mu.Lock()
	r.tokens[key] = tok
	r.mu.Unlock()
	return true, nil
}

func (r *redisLock) Release(ctx context.Context, key string) error {
	r.mu.Lock()
	tok, ok := r.tokens[key]
	delete(r.tokens, key)
	r.mu.Unlock()
	if !ok {
		// We never owned it (or already released). Don't touch Redis —
		// a blind DEL would race with whoever holds it now.
		return nil
	}
	_, err := releaseScript.Run(ctx, r.cli, []string{key}, tok).Result()
	if errors.Is(err, redis.Nil) {
		return nil // lease already expired; harmless
	}
	return err
}

func randomToken() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}
