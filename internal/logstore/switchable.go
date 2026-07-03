package logstore

import (
	"context"
	"sync"
	"time"
)

// Switchable wraps the live Store behind a RWMutex so the admin
// Settings → Elasticsearch tab can swap the logs read backend at
// runtime without a pod restart (v0.8.232). Every consumer (API
// handlers, alert evaluator, anomaly recorder, Drain templater) holds
// the same *Switchable; ESManager.apply calls Swap and the new backend
// is live for all of them on their next call.
//
// Optional per-backend capabilities (ListFields, ExecSQL, Diagnoser)
// are NOT forwarded here on purpose — forwarding would make the type
// assertions at the call sites succeed even when the inner backend
// lacks the capability, turning clean "not available on this backend"
// 400s into runtime errors. Call sites assert via Unwrap instead.
type Switchable struct {
	mu    sync.RWMutex
	inner Store
}

func NewSwitchable(inner Store) *Switchable {
	return &Switchable{inner: inner}
}

// Current returns the live inner store. Callers must not cache it
// across requests — that would defeat the swap.
func (s *Switchable) Current() Store {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.inner
}

// Swap atomically replaces the inner store. In-flight queries finish
// on the store they started with; nil is ignored (a failed rebuild
// must never leave consumers with a nil backend).
func (s *Switchable) Swap(n Store) {
	if n == nil {
		return
	}
	s.mu.Lock()
	s.inner = n
	s.mu.Unlock()
}

// Unwrap returns the innermost live Store — the Switchable's current
// target when s is a Switchable, else s itself. Every call site that
// type-asserts an optional backend capability (fielder / esSQLRunner /
// Diagnoser) must go through this so a UI-driven backend swap is
// respected.
func Unwrap(s Store) Store {
	if sw, ok := s.(*Switchable); ok {
		return sw.Current()
	}
	return s
}

// ── Store interface forwarding ──────────────────────────────────────

func (s *Switchable) Search(ctx context.Context, f Filter) (*Page, error) {
	return s.Current().Search(ctx, f)
}

func (s *Switchable) CountPatterns(ctx context.Context, pats []PatternSpec, curStart, baseStart, now time.Time) ([]PatternStats, error) {
	return s.Current().CountPatterns(ctx, pats, curStart, baseStart, now)
}

func (s *Switchable) Histogram(ctx context.Context, f Filter, bucketSec int, groupBy string) ([]LogSeries, error) {
	return s.Current().Histogram(ctx, f, bucketSec, groupBy)
}

func (s *Switchable) EQLSearch(ctx context.Context, q EQLQuery) ([]EQLSequence, error) {
	return s.Current().EQLSearch(ctx, q)
}

func (s *Switchable) Indices(ctx context.Context) ([]IndexInfo, error) {
	return s.Current().Indices(ctx)
}

func (s *Switchable) FieldValues(ctx context.Context, field, prefix string, limit int) ([]string, error) {
	return s.Current().FieldValues(ctx, field, prefix, limit)
}

func (s *Switchable) Backend() string { return s.Current().Backend() }

func (s *Switchable) Ping(ctx context.Context) error { return s.Current().Ping(ctx) }
