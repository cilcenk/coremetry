package logstore

// Operator rule (v0.8.109): ES queries never run against the bare index
// pattern. At 10B+ docs/day behind app-*, a search over the raw pattern
// fans out to every daily index's shards even when the question covers
// ten minutes. The store resolves the pattern to concrete index names
// (cached _cat/indices, 5 min TTL) and narrows them to the queried
// window, so a 10-minute question hits 1-2 dailies. Names without a
// date suffix are always kept (rollover/ILM naming may hold any window);
// when NO name carries a date suffix the narrowing falls back to the
// raw pattern. Listing errors also fall back — index resolution must
// never be the reason a query fails.

import (
	"context"
	"encoding/json"
	"regexp"
	"sync"
	"time"

	"github.com/elastic/go-elasticsearch/v8/esapi"
)

// esDateSuffix matches the trailing day stamp of time-partitioned index
// names: "app-2026.06.10" (Logstash/Filebeat default) or "app-2026-06-10".
var esDateSuffix = regexp.MustCompile(`(\d{4})[.-](\d{2})[.-](\d{2})$`)

const esIndexCacheTTL = 5 * time.Minute

// narrowIndices filters concrete index names to those that can hold
// documents in [from, to] (UTC calendar days). Undated names are always
// kept. ok=false when no name carries a parsable date suffix — the
// caller falls back to the raw pattern.
func narrowIndices(names []string, from, to time.Time) ([]string, bool) {
	fromDay := from.UTC().Truncate(24 * time.Hour)
	toDay := to.UTC().Truncate(24 * time.Hour)
	out := make([]string, 0, len(names))
	dated := false
	for _, n := range names {
		m := esDateSuffix.FindStringSubmatch(n)
		if m == nil {
			out = append(out, n)
			continue
		}
		dated = true
		day, err := time.Parse("2006-01-02", m[1]+"-"+m[2]+"-"+m[3])
		if err != nil {
			out = append(out, n)
			continue
		}
		if !day.Before(fromDay) && !day.After(toDay) {
			out = append(out, n)
		}
	}
	if !dated {
		return nil, false
	}
	return out, true
}

// clampWindow guarantees a bounded query window: zero To = now, zero
// From = To - 10m. The 10-minute default is the operator rule — an ES
// query without an explicit range asks about "right now", not about
// the whole retention.
func clampWindow(from, to time.Time) (time.Time, time.Time) {
	if to.IsZero() {
		to = time.Now()
	}
	if from.IsZero() {
		from = to.Add(-10 * time.Minute)
	}
	return from, to
}

type esIndexCache struct {
	mu      sync.RWMutex
	names   []string
	fetched time.Time
}

// queryIndices returns the concrete, window-narrowed index list for a
// query. Falls back to the raw pattern when the window is unbounded
// (trace-id correlation lookups), the listing fails, the cluster uses
// undated rollover names, or narrowing leaves nothing (the requests
// already carry allow_no_indices/ignore_unavailable, but an empty index
// list means "all" to ES — never send that). One day of slack is applied
// before `from`: an event timestamped 00:05 can sit in yesterday's index
// when the shipper rotates on ingest date.
func (s *ESStore) queryIndices(ctx context.Context, from, to time.Time) []string {
	fallback := []string{s.cfg.Index}
	if from.IsZero() || to.IsZero() {
		return fallback
	}
	names := s.cachedIndexNames(ctx)
	if len(names) == 0 {
		return fallback
	}
	narrowed, ok := narrowIndices(names, from.Add(-24*time.Hour), to)
	if !ok || len(narrowed) == 0 {
		return fallback
	}
	return narrowed
}

func (s *ESStore) cachedIndexNames(ctx context.Context) []string {
	s.idxCache.mu.RLock()
	if !s.idxCache.fetched.IsZero() && time.Since(s.idxCache.fetched) < esIndexCacheTTL {
		names := s.idxCache.names
		s.idxCache.mu.RUnlock()
		return names
	}
	s.idxCache.mu.RUnlock()

	req := esapi.CatIndicesRequest{
		Index:  []string{s.cfg.Index},
		Format: "json",
		H:      []string{"index"},
	}
	res, err := req.Do(ctx, s.cli)
	if err != nil {
		return nil
	}
	defer res.Body.Close()
	if res.IsError() {
		return nil
	}
	var rows []struct {
		Index string `json:"index"`
	}
	if err := json.NewDecoder(res.Body).Decode(&rows); err != nil {
		return nil
	}
	names := make([]string, 0, len(rows))
	for _, r := range rows {
		names = append(names, r.Index)
	}
	s.idxCache.mu.Lock()
	s.idxCache.names, s.idxCache.fetched = names, time.Now()
	s.idxCache.mu.Unlock()
	return names
}
