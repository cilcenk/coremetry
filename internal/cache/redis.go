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
// can log it — Coremetry should not crash just because Redis is unhealthy.
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

// ScanPrefix returns the value of every key matching `prefix*`.
// Uses SCAN (cursor-paginated, non-blocking) so a large keyspace
// doesn't stall the server, then a single MGET for the values.
// Missing / expired keys collapse out of the result quietly.
// Used by the cluster membership service (v0.5.253).
func (r *redisCache) ScanPrefix(ctx context.Context, prefix string) ([][]byte, error) {
	var (
		keys   []string
		cursor uint64
	)
	for {
		batch, next, err := r.cli.Scan(ctx, cursor, prefix+"*", 200).Result()
		if err != nil {
			return nil, fmt.Errorf("redis scan: %w", err)
		}
		keys = append(keys, batch...)
		if next == 0 {
			break
		}
		cursor = next
	}
	if len(keys) == 0 {
		return nil, nil
	}
	vals, err := r.cli.MGet(ctx, keys...).Result()
	if err != nil {
		return nil, fmt.Errorf("redis mget: %w", err)
	}
	out := make([][]byte, 0, len(vals))
	for _, v := range vals {
		if v == nil {
			continue
		}
		s, _ := v.(string)
		if s == "" {
			continue
		}
		out = append(out, []byte(s))
	}
	return out, nil
}

func (r *redisCache) Ping(ctx context.Context) error {
	return r.cli.Ping(ctx).Err()
}

// RedisStats is the slice of INFO + DBSIZE the System page renders.
// Kept small so one trip per Stats() call covers the panel without
// streaming the full INFO blob (which is hundreds of fields). All
// fields are zeroed on parse failure — UI shows "—" for those rows
// rather than crashing the panel.
type RedisStats struct {
	Version             string  `json:"version"`
	Mode                string  `json:"mode"`               // standalone|sentinel|cluster
	Uptime              int64   `json:"uptimeSec"`
	ConnectedClients    int     `json:"connectedClients"`
	Keys                int64   `json:"keys"`
	UsedMemoryBytes     int64   `json:"usedMemoryBytes"`
	UsedMemoryPeakBytes int64   `json:"usedMemoryPeakBytes"`
	MaxMemoryBytes      int64   `json:"maxMemoryBytes"`
	HitRate             float64 `json:"hitRate"`            // keyspace_hits / (hits+misses), 0..1
	OpsPerSec           float64 `json:"opsPerSec"`          // instantaneous_ops_per_sec
	NetInputKBps        float64 `json:"netInputKbps"`
	NetOutputKBps       float64 `json:"netOutputKbps"`
	EvictedKeys         int64   `json:"evictedKeys"`
	ExpiredKeys         int64   `json:"expiredKeys"`
}

// Stats parses a representative subset of `INFO` plus DBSIZE so the
// admin/stats page can render Redis health alongside ClickHouse +
// queue depth. Cheap (~ms) — no aggregation, just a single round trip.
func (r *redisCache) Stats(ctx context.Context) (RedisStats, error) {
	var out RedisStats
	info, err := r.cli.Info(ctx).Result()
	if err != nil {
		return out, fmt.Errorf("redis INFO: %w", err)
	}
	// Map keys → values from INFO's `key:value` line shape.
	parseInt := func(s string) int64 {
		var n int64
		_, _ = fmt.Sscanf(s, "%d", &n)
		return n
	}
	parseFloat := func(s string) float64 {
		var f float64
		_, _ = fmt.Sscanf(s, "%f", &f)
		return f
	}
	hits, misses := int64(0), int64(0)
	for _, raw := range splitLines(info) {
		k, v, ok := splitKV(raw, ':')
		if !ok {
			continue
		}
		switch k {
		case "redis_version":
			out.Version = v
		case "redis_mode":
			out.Mode = v
		case "uptime_in_seconds":
			out.Uptime = parseInt(v)
		case "connected_clients":
			out.ConnectedClients = int(parseInt(v))
		case "used_memory":
			out.UsedMemoryBytes = parseInt(v)
		case "used_memory_peak":
			out.UsedMemoryPeakBytes = parseInt(v)
		case "maxmemory":
			out.MaxMemoryBytes = parseInt(v)
		case "instantaneous_ops_per_sec":
			out.OpsPerSec = parseFloat(v)
		case "instantaneous_input_kbps":
			out.NetInputKBps = parseFloat(v)
		case "instantaneous_output_kbps":
			out.NetOutputKBps = parseFloat(v)
		case "keyspace_hits":
			hits = parseInt(v)
		case "keyspace_misses":
			misses = parseInt(v)
		case "evicted_keys":
			out.EvictedKeys = parseInt(v)
		case "expired_keys":
			out.ExpiredKeys = parseInt(v)
		}
	}
	if hits+misses > 0 {
		out.HitRate = float64(hits) / float64(hits+misses)
	}
	if n, err := r.cli.DBSize(ctx).Result(); err == nil {
		out.Keys = n
	}
	return out, nil
}

// splitLines splits INFO output by both \r\n and \n — Redis uses
// CRLF on the wire but some pipes normalise to LF.
func splitLines(s string) []string {
	out := []string{}
	start := 0
	for i := 0; i < len(s); i++ {
		if s[i] == '\n' {
			line := s[start:i]
			if len(line) > 0 && line[len(line)-1] == '\r' {
				line = line[:len(line)-1]
			}
			out = append(out, line)
			start = i + 1
		}
	}
	if start < len(s) {
		out = append(out, s[start:])
	}
	return out
}

func splitKV(s string, sep byte) (string, string, bool) {
	for i := 0; i < len(s); i++ {
		if s[i] == sep {
			return s[:i], s[i+1:], true
		}
	}
	return "", "", false
}

// Stats on the noop cache returns an empty struct + nil — the System
// page checks for "version == ''" and renders "Redis not configured".
func (noopCache) Stats(_ context.Context) (RedisStats, error) {
	return RedisStats{}, nil
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
