// Package cluster gives Coremetry pods a way to see each other in
// an HA / replicated deployment (v0.5.253). Coremetry's background
// workers (evaluator, anomaly detector, monitor runner, log
// templater, topology aggregator) have always been HA-safe — they
// take a per-tick Redis lock so only one replica runs each tick.
// The missing piece was operator visibility: "I scaled to 10 pods,
// are they all alive? Which one ran the last tick?"
//
// Each pod writes a heartbeat to a Redis key
// `coremetry:pod:<id>` every 10s with a 30s TTL. The admin
// /api/admin/cluster endpoint scans those keys via the existing
// cache.Cache abstraction to render a live member list. Pods that
// disappear from Redis (crashed / rolled out) silently fall off
// the list within 30s.
//
// Pure visibility — no leader election here. The existing
// per-tick TryAcquire pattern is the leader-election story; this
// package is purely "who's alive".
package cluster

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"log"
	"os"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/cilcenk/coremetry/internal/cache"
)

// keyPrefix — every pod's heartbeat key starts here, so a
// ScanPrefix on `coremetry:pod:` returns the full member list.
const keyPrefix = "coremetry:pod:"

// Member is the public shape returned by the cluster admin
// endpoint. Mirrors the JSON the heartbeat key holds plus the
// derived `isThisPod` field (populated server-side per request).
type Member struct {
	ID          string   `json:"id"`
	Hostname    string   `json:"hostname"`
	Version     string   `json:"version"`
	StartedAt   int64    `json:"startedAt"` // unix ns
	LastSeen    int64    `json:"lastSeen"`  // unix ns
	IsThisPod   bool     `json:"isThisPod"`
	LeaderLocks []string `json:"leaderLocks,omitempty"`
}

// payload is the JSON shape written to Redis on each heartbeat.
// Kept narrow so a future field doesn't break old pods reading
// new keys mid-rollout.
type payload struct {
	ID        string `json:"id"`
	Hostname  string `json:"hostname"`
	Version   string `json:"version"`
	StartedAt int64  `json:"startedAt"`
	Updated   int64  `json:"updated"`
}

// Service is the per-pod heartbeat loop + membership lookup. One
// instance lives in main(); the API handler reads from it.
type Service struct {
	cache     cache.Cache
	interval  time.Duration
	ttl       time.Duration
	id        string
	hostname  string
	version   string
	startedAt time.Time

	mu              sync.RWMutex
	lastLeaderLocks []string
}

// New constructs the service but does not start the heartbeat
// loop — call Start(ctx) once the process is ready to identify
// itself. Generates a stable pod ID: hostname + 4-byte random
// suffix so duplicate hostnames (rare but possible on bare-metal)
// don't collide.
//
// The Noop cache short-circuits both heartbeat + Members(); the
// admin page renders a single "this pod" entry in dev mode.
func New(c cache.Cache, version string) *Service {
	host, _ := os.Hostname()
	if host == "" {
		host = "coremetry"
	}
	suffix := make([]byte, 4)
	_, _ = rand.Read(suffix)
	id := host + "-" + hex.EncodeToString(suffix)
	return &Service{
		cache:     c,
		interval:  10 * time.Second,
		ttl:       30 * time.Second,
		id:        id,
		hostname:  host,
		version:   version,
		startedAt: time.Now(),
	}
}

// MyID returns this pod's stable identifier. Embedded in
// log lines + audit details when the operator wants to know
// "which replica did this".
func (s *Service) MyID() string {
	if s == nil {
		return "single"
	}
	return s.id
}

// Start launches the heartbeat loop. Writes an immediate
// heartbeat so a freshly-rolled-out pod appears in the member
// list before the first interval elapses. Returns when ctx
// is cancelled; the last-written key expires naturally via
// its TTL (10-30s grace period before it falls off the list).
func (s *Service) Start(ctx context.Context) {
	if s == nil {
		return
	}
	if err := s.heartbeat(ctx); err != nil {
		log.Printf("[cluster] initial heartbeat: %v", err)
	}
	t := time.NewTicker(s.interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			if err := s.heartbeat(ctx); err != nil {
				log.Printf("[cluster] heartbeat: %v", err)
			}
		}
	}
}

// heartbeat writes (or refreshes) this pod's key with the
// configured TTL. Idempotent — a missed beat just lengthens
// the window before disappearance.
func (s *Service) heartbeat(ctx context.Context) error {
	p := payload{
		ID:        s.id,
		Hostname:  s.hostname,
		Version:   s.version,
		StartedAt: s.startedAt.UnixNano(),
		Updated:   time.Now().UnixNano(),
	}
	raw, err := json.Marshal(p)
	if err != nil {
		return err
	}
	return s.cache.Set(ctx, keyPrefix+s.id, raw, s.ttl)
}

// SetLocalLocks lets callers record which lock keys this pod
// currently holds (e.g. the evaluator's "evaluator:lock"). The
// admin page surfaces them as "this pod owns: …" so the operator
// can see which replica is the active worker for each tick.
// Last-write-wins; callers typically call this after a successful
// TryAcquire.
func (s *Service) SetLocalLocks(keys []string) {
	if s == nil {
		return
	}
	sorted := make([]string, len(keys))
	copy(sorted, keys)
	sort.Strings(sorted)
	s.mu.Lock()
	s.lastLeaderLocks = sorted
	s.mu.Unlock()
}

// Members returns the full live member list — every pod that's
// written a heartbeat in the last `ttl` window. Sorted by
// StartedAt ascending so the oldest replica leads the list.
//
// When the cache is Noop (single-instance dev mode), returns a
// single-member list representing this process so the admin page
// renders sensibly in compose-up too.
func (s *Service) Members(ctx context.Context) ([]Member, error) {
	if s == nil {
		return nil, nil
	}
	raws, err := s.cache.ScanPrefix(ctx, keyPrefix)
	if err != nil {
		return nil, err
	}
	// Noop / empty → fabricate a single-member view from local
	// state so the admin page reads the same in dev as in K8s.
	if len(raws) == 0 {
		s.mu.RLock()
		locks := append([]string{}, s.lastLeaderLocks...)
		s.mu.RUnlock()
		return []Member{{
			ID:          s.id,
			Hostname:    s.hostname,
			Version:     s.version,
			StartedAt:   s.startedAt.UnixNano(),
			LastSeen:    time.Now().UnixNano(),
			IsThisPod:   true,
			LeaderLocks: locks,
		}}, nil
	}
	out := make([]Member, 0, len(raws))
	for _, raw := range raws {
		var p payload
		if err := json.Unmarshal(raw, &p); err != nil {
			continue
		}
		m := Member{
			ID:        p.ID,
			Hostname:  p.Hostname,
			Version:   p.Version,
			StartedAt: p.StartedAt,
			LastSeen:  p.Updated,
			IsThisPod: p.ID == s.id,
		}
		if m.IsThisPod {
			s.mu.RLock()
			m.LeaderLocks = append([]string{}, s.lastLeaderLocks...)
			s.mu.RUnlock()
		}
		out = append(out, m)
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].StartedAt < out[j].StartedAt
	})
	return out, nil
}

// PodIDFromHostname extracts the short label most operators expect
// from a pod hostname — strips the random suffix so two replicas
// of the same Deployment cluster together visually. Used by the
// frontend's group-by-hostname rendering.
func PodIDFromHostname(id string) string {
	if i := strings.LastIndex(id, "-"); i > 0 {
		return id[:i]
	}
	return id
}
