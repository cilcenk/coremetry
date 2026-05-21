// Package sampling decides whether to keep or drop an incoming span
// based on a hybrid rule set:
//
//   1. Always-keep rules: spans matching these are always kept,
//      regardless of probabilistic decision. Default rules:
//        - status_code = "error"
//        - parent_span_id == "" (i.e. root span)
//      The rules are intentional: errors are 100x more valuable
//      than successful spans, and root spans are a cheap RPS
//      anchor that lets services-list / service-map maths still
//      work at a low ratio.
//
//   2. Probabilistic ratio: for spans that didn't match an
//      always-keep, we hash trace_id and keep iff the hash falls
//      below ratio * 2^32. Same trace_id → same decision, so the
//      spans of one trace are kept or dropped together — partial
//      traces are a pain to analyse and we avoid producing them.
//
// At billion-span/day scale a Default=0.1 + always-keep-errors +
// always-keep-roots cuts ingest volume ~90% on a healthy service
// while preserving 100% of failures and the root-span RPS index.
// That's the difference between affordable and not at scale.
//
// Decisions are pure functions of the Span struct + config — no
// state, no buffering, safe to call from many goroutines. This is
// "head sampling" in OTel terms; full tail-sampling (buffer until
// trace is "done", then decide based on aggregate properties)
// requires a per-trace buffer with TTL, deferred for a follow-up.
package sampling

import (
	"context"
	"encoding/json"
	"hash/fnv"
	"log"
	"strings"
	"sync"
	"time"

	"github.com/cilcenk/coremetry/internal/chstore"
	"github.com/cilcenk/coremetry/internal/config"
)

// settingsKey is the system_settings entry that persists the live
// sampling config. Admin UI writes here; LoadPersisted picks it
// up on boot. JSON-encoded SamplingConfig.
const settingsKey = "sampling"

// Sampler is the application-side decision engine. Construct one
// per process from cfg; safe for concurrent use.
type Sampler struct {
	mu               sync.RWMutex
	defaultRatio     float64
	services         map[string]uint32 // ratio * 2^32, precomputed
	defaultThreshold uint32
	keepErrors       bool
	keepRoots        bool

	// lastCfg is what the live sampler is configured for; AttachFlush
	// re-applies it to bring up the tail stage now that the consumer
	// callback is in place. Without this, tail-enabled configs would
	// silently drop into "configured but flush=nil → no-op" land.
	lastCfg config.SamplingConfig

	// tail is created lazily on Reload() when the cfg has Tail.Enabled
	// set; nil otherwise. Owners pass `flushFn` (consumer.Add) so the
	// tail sampler can push kept spans downstream after its decision
	// window. Lifecycle: Stop()'d on Reload when transitioning
	// enabled→disabled, replaced when transitioning disabled→enabled.
	tail       *TailSampler
	tailCancel func()
	tailFlush  func(*chstore.Span) bool
}

// New builds a Sampler from config. Default ratios outside [0,1]
// are clamped; nil maps treated as empty.
func New(cfg config.SamplingConfig) *Sampler {
	keepErr := true
	if cfg.AlwaysKeepErrors != nil {
		keepErr = *cfg.AlwaysKeepErrors
	}
	keepRoot := true
	if cfg.AlwaysKeepRoots != nil {
		keepRoot = *cfg.AlwaysKeepRoots
	}
	d := clamp01(cfg.Default)
	if cfg.Default == 0 && len(cfg.Services) == 0 && cfg.AlwaysKeepErrors == nil && cfg.AlwaysKeepRoots == nil {
		// Empty config = sampling disabled (keep everything). The
		// "Default=0 means drop probabilistic" semantics only apply
		// once the operator has actually set something.
		d = 1.0
	}
	s := &Sampler{
		defaultRatio:     d,
		defaultThreshold: ratioToThreshold(d),
		services:         map[string]uint32{},
		keepErrors:       keepErr,
		keepRoots:        keepRoot,
		lastCfg:          cfg,
	}
	for svc, r := range cfg.Services {
		s.services[svc] = ratioToThreshold(clamp01(r))
	}
	return s
}

// Decide returns true if the span should be kept. Inlinable hot
// path — no allocations, no map fallback besides the per-service
// lookup. Callers usually wrap an `if !s.Decide(span) { drop }`.
func (s *Sampler) Decide(span *chstore.Span) bool {
	if span == nil {
		return false
	}
	if s.keepErrors && isError(span.StatusCode) {
		return true
	}
	if s.keepRoots && span.ParentID == "" {
		return true
	}
	s.mu.RLock()
	threshold, ok := s.services[span.ServiceName]
	s.mu.RUnlock()
	if !ok {
		threshold = s.defaultThreshold
	}
	if threshold == 0 {
		return false
	}
	if threshold >= 0xffffffff {
		return true
	}
	return traceHash(span.TraceID) < threshold
}

// Reload swaps in a new config atomically. Lets the admin UI
// adjust ratios without a process restart. When the tail
// sub-stage transitions in/out of enabled, the old TailSampler
// (if any) is Stop()'d and a fresh one started — buffered spans
// in the going-away tail get flushed via final sweep before exit.
func (s *Sampler) Reload(cfg config.SamplingConfig) {
	next := New(cfg)
	s.mu.Lock()
	s.defaultRatio = next.defaultRatio
	s.defaultThreshold = next.defaultThreshold
	s.services = next.services
	s.keepErrors = next.keepErrors
	s.keepRoots = next.keepRoots
	s.lastCfg = cfg

	// Reconcile tail: tear down any prior tail, then start fresh
	// if the new config wants one.
	if s.tail != nil {
		s.tail.Stop()
		s.tail = nil
		if s.tailCancel != nil {
			s.tailCancel()
			s.tailCancel = nil
		}
	}
	if cfg.Tail.Enabled && s.tailFlush != nil {
		s.tail = NewTailSampler(cfg.Tail, s.defaultRatio, s.keepErrors, s.keepRoots, s.tailFlush)
		ctx, cancel := context.WithCancel(context.Background())
		s.tailCancel = cancel
		go s.tail.Run(ctx)
	}
	s.mu.Unlock()
}

// AttachFlush wires the consumer.Add callback so the tail stage
// (when enabled) has a downstream to flush kept spans into. Must
// be called exactly once after construction, before any spans
// hit the ingester. Re-applies the last-known config so a tail-
// enabled boot config gets its tail stage spun up here (the
// initial New() couldn't, since the flush callback didn't exist
// yet).
func (s *Sampler) AttachFlush(flush func(*chstore.Span) bool) {
	s.mu.Lock()
	s.tailFlush = flush
	cfg := s.lastCfg
	s.mu.Unlock()
	s.Reload(cfg)
}

// Tail returns the live TailSampler (or nil when disabled). Hot
// path: ingester checks `s.Tail() != nil` per span — fine because
// the field write under Reload is the only mutation.
func (s *Sampler) Tail() *TailSampler {
	s.mu.RLock()
	t := s.tail
	s.mu.RUnlock()
	return t
}

// LoadPersisted reads system_settings["sampling"] and applies it
// to the live Sampler. Called once at boot after the env-var /
// config.yaml-derived state is in place; if a persisted value
// exists it wins, since the admin UI is the canonical source of
// truth for runtime tuning.
func (s *Sampler) LoadPersisted(ctx context.Context, store *chstore.Store) error {
	raw, err := store.GetSetting(ctx, settingsKey)
	if err != nil {
		return err
	}
	if len(raw) == 0 {
		return nil
	}
	var cfg config.SamplingConfig
	if err := json.Unmarshal(raw, &cfg); err != nil {
		log.Printf("[sampling] decode persisted: %v (using config.yaml defaults)", err)
		return nil
	}
	s.Reload(cfg)
	log.Printf("[sampling] loaded persisted config (default=%.2f, %d service overrides)",
		cfg.Default, len(cfg.Services))
	return nil
}

// StartConfigRefresh — v0.5.324. Background poll keeps the
// sampler in sync with the shared persisted config across pods.
// interval ≤ 0 → 30s.
func (s *Sampler) StartConfigRefresh(ctx context.Context, store *chstore.Store, interval time.Duration) {
	if s == nil || store == nil {
		return
	}
	if interval <= 0 {
		interval = 30 * time.Second
	}
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			if err := s.LoadPersisted(ctx, store); err != nil {
				log.Printf("[sampling] config refresh: %v", err)
			}
		}
	}
}

// SavePersisted serialises cfg to system_settings + hot-reloads
// the in-memory Sampler. Admin UI calls this on its "Save" button.
func (s *Sampler) SavePersisted(ctx context.Context, store *chstore.Store, cfg config.SamplingConfig) error {
	raw, err := json.Marshal(cfg)
	if err != nil {
		return err
	}
	if err := store.PutSetting(ctx, settingsKey, raw); err != nil {
		return err
	}
	s.Reload(cfg)
	return nil
}

// Snapshot returns the live config for /api/sampling readback.
func (s *Sampler) Snapshot() config.SamplingConfig {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := config.SamplingConfig{
		Default:  s.defaultRatio,
		Services: map[string]float64{},
	}
	for svc, t := range s.services {
		out.Services[svc] = float64(t) / float64(0xffffffff)
	}
	keepErr, keepRoot := s.keepErrors, s.keepRoots
	out.AlwaysKeepErrors = &keepErr
	out.AlwaysKeepRoots = &keepRoot
	out.Tail = s.lastCfg.Tail
	return out
}

func clamp01(v float64) float64 {
	if v < 0 {
		return 0
	}
	if v > 1 {
		return 1
	}
	return v
}

func ratioToThreshold(ratio float64) uint32 {
	if ratio <= 0 {
		return 0
	}
	if ratio >= 1 {
		return 0xffffffff
	}
	return uint32(ratio * float64(0xffffffff))
}

// traceHash maps a trace_id (hex string, 32 chars usually) to a
// 32-bit value. FNV-1a is non-cryptographic, fast, and good enough
// for uniform partition selection — same trace_id always lands at
// the same bucket so the keep/drop decision is consistent.
func traceHash(traceID string) uint32 {
	h := fnv.New32a()
	h.Write([]byte(traceID))
	return h.Sum32()
}

func isError(statusCode string) bool {
	switch strings.ToLower(statusCode) {
	case "error", "status_code_error":
		return true
	}
	return false
}
