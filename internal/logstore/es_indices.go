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
//
// v0.9.283 — on a DATA-STREAM cluster the rule above was a no-op: a
// backing index is named ".ds-<stream>-<date>-<generation>", the
// `$`-anchored date suffix matched nothing, and every read fell back
// to the bare pattern. Backing indices are narrowed by generation
// coverage (dsCoverage) rather than by their own stamp, because the
// stamp is the rollover date, not the data's date.

import (
	"context"
	"encoding/json"
	"log"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/elastic/go-elasticsearch/v8/esapi"
)

// esDateSuffix matches the trailing day stamp of time-partitioned index
// names: "app-2026.06.10" (Logstash/Filebeat default) or "app-2026-06-10".
var esDateSuffix = regexp.MustCompile(`(\d{4})[.-](\d{2})[.-](\d{2})$`)

// esDataStreamName matches a data-stream backing index:
// ".ds-<stream>-<YYYY.MM.DD>-<generation>". The day stamp sits in the
// MIDDLE and the name ends with the rollover generation, which is why
// the `$`-anchored esDateSuffix never matched one — see the v0.9.283
// note on dsCoverage.
var esDataStreamName = regexp.MustCompile(`^\.ds-(.+)-(\d{4})[.-](\d{2})[.-](\d{2})-(\d+)$`)

const esIndexCacheTTL = 5 * time.Minute

// esIndexCacheNegTTL throttles retries after a FAILED listing. Without
// it a credential lacking the `monitor` privilege fires one extra
// _cat/indices per /logs request, forever — the listing is only cached
// on success (v0.9.283). Short so the privilege being granted is
// noticed within a minute.
const esIndexCacheNegTTL = 30 * time.Second

// esListingSuppressed reports whether a failed-listing window is still
// open, i.e. the _cat/indices round-trip must be skipped entirely.
// Pure so both branches are exercised without a live cluster.
func esListingSuppressed(failedAt, now time.Time) bool {
	return !failedAt.IsZero() && now.Sub(failedAt) < esIndexCacheNegTTL
}

// dsBacking is one parsed data-stream backing index.
type dsBacking struct {
	name   string
	stream string
	day    time.Time
	gen    int
}

// parseDataStreamBacking decomposes ".ds-<stream>-<date>-<generation>".
// The stream capture is greedy, so a hyphenated stream name keeps its
// full identity (".ds-app-checkout-prod-2026.07.03-000001" → stream
// "app-checkout-prod") and a stream whose own name contains a date
// still binds the LAST date to the rollover stamp.
func parseDataStreamBacking(n string) (dsBacking, bool) {
	m := esDataStreamName.FindStringSubmatch(n)
	if m == nil {
		return dsBacking{}, false
	}
	day, err := time.Parse("2006-01-02", m[2]+"-"+m[3]+"-"+m[4])
	if err != nil {
		return dsBacking{}, false
	}
	gen, err := strconv.Atoi(m[5])
	if err != nil {
		return dsBacking{}, false
	}
	return dsBacking{name: n, stream: m[1], day: day, gen: gen}, true
}

// dsCoverage decides which data-stream backing indices can hold
// documents in [fromDay, toDay].
//
// v0.9.283 — a backing index CANNOT be filtered by its own day stamp:
// that stamp is the ROLLOVER date, so index N holds documents from
// day(N) until the next rollover, and with size-triggered rollover
// that span is arbitrarily long. Filtering by the stamp would silently
// drop the one index that actually holds the window — the worst
// failure mode this file has, worse than the fan-out it fixes.
//
// Coverage comes from the generation ordering instead: within a stream,
// index N covers [day(N), day(N+1)] and the newest is open-ended. Two
// rollovers on the same day both stay (day precision cannot separate
// them). This needs no extra ES call — the generation is already in the
// name we listed.
func dsCoverage(backings []dsBacking, fromDay, toDay time.Time) map[string]bool {
	byStream := map[string][]dsBacking{}
	for _, b := range backings {
		byStream[b.stream] = append(byStream[b.stream], b)
	}
	keep := make(map[string]bool, len(backings))
	for _, list := range byStream {
		sort.Slice(list, func(i, j int) bool { return list[i].gen < list[j].gen })
		for i, b := range list {
			if b.day.After(toDay) {
				continue // rolled over after the window closed
			}
			// Closed by its successor's creation day, inclusive: on that
			// day both indices were being written to.
			if i+1 < len(list) && list[i+1].day.Before(fromDay) {
				continue
			}
			keep[b.name] = true
		}
	}
	return keep
}

// narrowIndices filters concrete index names to those that can hold
// documents in [from, to] (UTC calendar days). Three families, each by
// its own rule: data-stream backing indices by generation coverage
// (dsCoverage), classic dailies by their trailing day stamp, and
// undated names unconditionally. ok=false when NO name carries a
// parsable date in either family — the caller falls back to the raw
// pattern.
func narrowIndices(names []string, from, to time.Time) ([]string, bool) {
	fromDay := from.UTC().Truncate(24 * time.Hour)
	toDay := to.UTC().Truncate(24 * time.Hour)

	backings := make([]dsBacking, 0, len(names))
	for _, n := range names {
		if b, ok := parseDataStreamBacking(n); ok {
			backings = append(backings, b)
		}
	}
	keepDS := dsCoverage(backings, fromDay, toDay)

	out := make([]string, 0, len(names))
	dated := len(backings) > 0
	for _, n := range names {
		if _, isDS := parseDataStreamBacking(n); isDS {
			if keepDS[n] {
				out = append(out, n)
			}
			continue
		}
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
	mu       sync.RWMutex
	names    []string
	fetched  time.Time
	failedAt time.Time
}

// resolveIndexTemplate substitutes the {service} / {namespace}
// placeholders of an operator-configured index template. Pure so the
// v0.8.231 tests exercise every placeholder combination. Returns ""
// (caller falls back to the pattern path) when the template is unset
// or the query isn't pinned to one service; an empty namespace
// substitutes "*" so the resolved name still covers the family.
func resolveIndexTemplate(tpl, service, ns string) string {
	if tpl == "" || service == "" {
		return ""
	}
	out := strings.ReplaceAll(tpl, "{service}", service)
	if ns == "" {
		ns = "*"
	}
	return strings.ReplaceAll(out, "{namespace}", ns)
}

// templateIndex resolves cfg.IndexTemplate for a service-scoped query,
// consulting the NamespaceResolver for the {namespace} placeholder
// only when the template actually contains it (skips the CH-backed
// lookup otherwise). "" = template path not applicable.
func (s *ESStore) templateIndex(ctx context.Context, service string) string {
	tpl := s.cfg.IndexTemplate
	if tpl == "" || service == "" {
		return ""
	}
	ns := ""
	if strings.Contains(tpl, "{namespace}") && s.NamespaceResolver != nil {
		ns = s.NamespaceResolver(ctx, service)
	}
	return resolveIndexTemplate(tpl, service, ns)
}

// queryIndices returns the concrete, window-narrowed index list for a
// query. Falls back to the raw pattern when the window is unbounded
// (trace-id correlation lookups), the listing fails, the cluster uses
// undated rollover names, or narrowing leaves nothing (the requests
// already carry allow_no_indices/ignore_unavailable, but an empty index
// list means "all" to ES — never send that). One day of slack is applied
// before `from`: an event timestamped 00:05 can sit in yesterday's index
// when the shipper rotates on ingest date.
//
// v0.8.231 — service-pinned queries short-circuit to the operator's
// index template (e.g. app-{service}.{namespace} → app-checkout.prod)
// when one is configured: one concrete index family instead of the
// whole pattern fan-out, and no _cat listing needed. A not-yet-created
// resolved index answers 0 hits (allow_no_indices/ignore_unavailable
// ride every request), not a 404.
// rolloverRemainder matches what may follow "<stream-name>-" in a
// rollover / dated child: digits, dots, dashes only ("000079",
// "2026.07.03-000391"). Anything else means the prefix cut a LONGER
// stream name mid-way (app-identityhub vs app-identityhub-int) — that
// must NOT count as a match.
var rolloverRemainder = regexp.MustCompile(`^[0-9][0-9.\-]*$`)

// indexKnown reports whether a template-resolved CONCRETE index name is
// backed by anything the cluster actually has: an exact index match, a
// rollover/dated child ("<name>-000123", "<name>-2026.07.03"), or a
// data-stream backing index (".ds-<name>-<date>-<seq>"). Pure
// (v0.8.239) so the misconfigured-template fallback is unit-tested.
func indexKnown(names []string, resolved string) bool {
	child := func(n, prefix string) bool {
		return strings.HasPrefix(n, prefix) && rolloverRemainder.MatchString(n[len(prefix):])
	}
	for _, n := range names {
		if n == resolved ||
			child(n, resolved+"-") ||
			child(n, ".ds-"+resolved+"-") {
			return true
		}
	}
	return false
}

func (s *ESStore) queryIndices(ctx context.Context, f Filter) []string {
	if idx := s.templateIndex(ctx, f.Service); idx != "" {
		// v0.8.239 — operator-reported (service-detail Logs tab empty):
		// a template whose separator doesn't match the real index naming
		// (e.g. app-{service}.{namespace} configured, cluster uses
		// app-<service>-<env>) resolves to a name that matches NOTHING —
		// and allow_no_indices turns that into a silent 0. When the
		// resolved name is concrete (no wildcard), verify it against the
		// cached index inventory; unknown → fall back to the pattern
		// (wider but correct — the service term still filters).
		// Wildcarded resolutions (unresolved {namespace} → "*") skip the
		// check: ES expands them server-side. Empty inventory (listing
		// failed / no _cat privilege) also skips — the check must never
		// be the reason a working template stops working.
		if strings.ContainsAny(idx, "*?") {
			return []string{idx}
		}
		if names := s.cachedIndexNames(ctx); len(names) == 0 || indexKnown(names, idx) {
			return []string{idx}
		}
		log.Printf("[logstore-es] index template resolved %q but no such index/data-stream exists — falling back to pattern %q (check the template separator vs your index naming)", idx, s.cfg.Index)
	}
	fallback := []string{s.cfg.Index}
	if f.From.IsZero() || f.To.IsZero() {
		return fallback
	}
	names := s.cachedIndexNames(ctx)
	if len(names) == 0 {
		return fallback
	}
	narrowed, ok := narrowIndices(names, f.From.Add(-24*time.Hour), f.To)
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
	// v0.9.283 — a recent FAILURE is cached too. The listing is a
	// best-effort optimisation (callers fall back to the raw pattern),
	// so retrying it on every single request only adds ES load at the
	// exact moment ES is already refusing us.
	if esListingSuppressed(s.idxCache.failedAt, time.Now()) {
		s.idxCache.mu.RUnlock()
		return nil
	}
	s.idxCache.mu.RUnlock()

	req := esapi.CatIndicesRequest{
		Index:  []string{s.cfg.Index},
		Format: "json",
		H:      []string{"index"},
	}
	res, err := req.Do(ctx, s.cli)
	if err != nil {
		s.noteIndexListFailure()
		return nil
	}
	defer res.Body.Close()
	if res.IsError() {
		s.noteIndexListFailure()
		return nil
	}
	var rows []struct {
		Index string `json:"index"`
	}
	if err := json.NewDecoder(res.Body).Decode(&rows); err != nil {
		s.noteIndexListFailure()
		return nil
	}
	names := make([]string, 0, len(rows))
	for _, r := range rows {
		names = append(names, r.Index)
	}
	s.idxCache.mu.Lock()
	s.idxCache.names, s.idxCache.fetched = names, time.Now()
	s.idxCache.failedAt = time.Time{}
	s.idxCache.mu.Unlock()
	return names
}

// noteIndexListFailure opens the negative-cache window so the next
// esIndexCacheNegTTL of requests skip the listing entirely instead of
// each firing their own failing _cat/indices.
func (s *ESStore) noteIndexListFailure() {
	s.idxCache.mu.Lock()
	s.idxCache.failedAt = time.Now()
	s.idxCache.mu.Unlock()
}
