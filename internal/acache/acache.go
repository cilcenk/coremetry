// Package acache is the Traces filter autocomplete cache.
//
// Goal: serve service names, operation names and common attribute *values*
// for the /traces (and /explore) filter pickers in microseconds, straight
// out of Redis, without round-tripping ClickHouse. Data loss is acceptable
// — Redis runs as a pure cache (save "", appendonly no, maxmemory-policy
// allkeys-lru). On a cold cache or a Redis blip every read reports a miss
// and the caller falls back to the existing CH-backed picker endpoints.
//
// Two halves:
//
//	Ingestion (write): ObserveSpan(sp) is called per span on the ingest
//	  side-effect path. It does NOT touch Redis — it folds the span into an
//	  in-memory delta aggregator under a brief lock. A background flusher
//	  drains the accumulated deltas into Redis with ONE pipeline every
//	  FlushEvery. At ~11.5k spans/s (1B/day) a per-span ZINCRBY would be
//	  ~tens of thousands of Redis ops/s; local pre-aggregation collapses an
//	  entire flush window of a (service, op, attr-value) into a single
//	  ZINCRBY <delta>, which is mathematically identical to N×(+1) but
//	  costs one network op. This is the production-correct shape of "batch
//	  all writes with a pipeline".
//
//	Read (autocomplete): GetServices / GetOperations / GetAttributeKeys /
//	  GetAttributeValues read the sorted sets directly, ordered by frequency.
//	  Each returns a `hit bool`; a miss (cold key or Redis error) lets the
//	  HTTP handler fall back to ClickHouse — DB fallback is optional and the
//	  caller decides.
//
// Redis key layout (all under the coremetry: namespace):
//
//	coremetry:services            SET   service names
//	coremetry:services:rank       ZSET  service -> frequency
//	coremetry:ops:<svc>           SET   operation names for a service
//	coremetry:ops:rank:<svc>      ZSET  operation -> frequency
//	coremetry:attr:keys           SET   known attribute keys
//	coremetry:attr:<key>          ZSET  value -> frequency   (low/med cardinality)
//	coremetry:attr:card:<key>     HLL   approximate distinct count (high cardinality)
//
// Cardinality policy decides, per attribute key, whether values are kept as
// a ranked ZSET (Track), counted approximately with a HyperLogLog (HLL), or
// ignored (Skip). The policy is swappable at runtime (SetPolicy) so adding a
// new attribute never requires a code change — wire it from an env CSV or
// from the admin-editable system_settings blob.
package acache

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/redis/go-redis/v9"

	"github.com/cilcenk/coremetry/internal/chstore"
)

// ── Key namespace ────────────────────────────────────────────────────────────

const (
	keyServices       = "coremetry:services"
	keyServicesRank   = "coremetry:services:rank"
	keyOpsSetPrefix   = "coremetry:ops:"
	keyOpsRankPrefix  = "coremetry:ops:rank:"
	keyAttrKeys       = "coremetry:attr:keys"
	keyAttrValPrefix  = "coremetry:attr:"
	keyAttrCardPrefix = "coremetry:attr:card:"

	// WindowMode bookkeeping.
	tsSuffix    = ":ts"                      // companion ZSET, member -> lastSeenUnix
	tsRegistry  = "coremetry:acache:tsreg"   // HASH rankKey -> setKey ("" if none)
)

// ── Cardinality policy ───────────────────────────────────────────────────────

// Card is how a single attribute key's values are cached.
type Card uint8

const (
	// CardTrack keeps the top-N values as a frequency-ranked ZSET. Use for
	// low/medium-cardinality keys whose values are useful in a dropdown
	// (http.route, db.system, cloud.region…).
	CardTrack Card = iota
	// CardHLL keeps only a HyperLogLog approximate distinct count — no values.
	// Use for high-cardinality keys (trace_id, k8s.pod.name, account ids):
	// the picker shows "free-text (~N distinct)" instead of a value list.
	CardHLL
	// CardSkip records the key under coremetry:attr:keys but stores nothing
	// about its values.
	CardSkip
)

// Policy classifies an attribute key into a Card. Implementations must be safe
// for concurrent use (Classify is called on the hot ingest path).
type Policy interface {
	Classify(attrKey string) Card
}

// PolicyFunc adapts a plain function to Policy.
type PolicyFunc func(string) Card

func (f PolicyFunc) Classify(k string) Card { return f(k) }

// StaticPolicy resolves keys against an allowlist (Track) and a denylist
// (HLL); everything else falls to Default. Build it from config (splitCSV
// env vars) or from a system_settings JSON blob so new attributes are a
// config change, not a code change.
type StaticPolicy struct {
	low     map[string]struct{} // allowlist -> CardTrack
	high    map[string]struct{} // denylist  -> CardHLL
	Default Card
}

// NewStaticPolicy builds a policy from a low/med-cardinality allowlist and a
// high-cardinality denylist. def is applied to keys in neither list.
func NewStaticPolicy(lowCard, highCard []string, def Card) *StaticPolicy {
	p := &StaticPolicy{low: map[string]struct{}{}, high: map[string]struct{}{}, Default: def}
	for _, k := range lowCard {
		if k = strings.TrimSpace(k); k != "" {
			p.low[k] = struct{}{}
		}
	}
	for _, k := range highCard {
		if k = strings.TrimSpace(k); k != "" {
			p.high[k] = struct{}{}
		}
	}
	return p
}

func (p *StaticPolicy) Classify(k string) Card {
	if _, ok := p.low[k]; ok {
		return CardTrack
	}
	if _, ok := p.high[k]; ok {
		return CardHLL
	}
	return p.Default
}

// DefaultPolicy is a sensible starting allowlist/denylist for an OTel-shaped
// span stream. Unknown keys default to CardHLL — we count them (so the picker
// can say "free-text") but never store their values, which bounds memory
// against a cardinality explosion from an un-classified key.
func DefaultPolicy() *StaticPolicy {
	return NewStaticPolicy(
		// low/med cardinality — keep ranked values
		[]string{
			"http.status_code", "http.route", "http.method", "http.scheme",
			"rpc.method", "rpc.system", "rpc.service",
			"db.system", "db.operation", "db.name",
			"messaging.system", "messaging.operation", "messaging.destination.name",
			"cloud.region", "cloud.provider", "cloud.availability_zone",
			"deployment.environment", "service.namespace", "service.version",
			"peer.service", "server.address", "kind", "otel.status_code",
		},
		// high cardinality — HLL count only, no values
		[]string{
			"trace_id", "span_id", "parent_id",
			"banking.account_id", "user.id", "enduser.id", "session.id",
			"k8s.pod.name", "k8s.pod.uid", "host.id", "container.id",
			"http.url", "url.full", "http.target", "url.path",
			"db.statement", "db.query.text", "exception.stacktrace",
			"thread.id", "net.peer.port", "client.address",
		},
		CardHLL,
	)
}

// ── Options ──────────────────────────────────────────────────────────────────

// Options tune the cache. The zero value is invalid — use defaults via
// NewStore, which fills any unset field.
type Options struct {
	Policy     Policy        // attribute-key classifier (default DefaultPolicy())
	FlushEvery time.Duration // pipeline flush cadence (default 2s)
	TTL        time.Duration // sliding EXPIRE on every touched key (default 24h; 0 disables)
	TopN       int           // values kept per CardTrack key (default 1000)
	MaxValLen  int           // attribute values longer than this are ignored (default 256)
	MaxDistinctPerKey int     // per-flush in-memory distinct cap per attr key (default 50000)

	// WindowMode swaps the sliding-EXPIRE staleness strategy for per-member
	// time windows: every flush stamps member->now into a companion ZSET, and
	// a background sweeper drops members not seen within WindowSize. More
	// precise (a single stale operation is evicted without dropping the whole
	// service), at the cost of a parallel ZSET per ranked key. Default off.
	WindowMode bool
	WindowSize time.Duration // default 24h
	SweepEvery time.Duration // sweeper cadence (default 5m)
}

func (o *Options) applyDefaults() {
	if o.Policy == nil {
		o.Policy = DefaultPolicy()
	}
	if o.FlushEvery <= 0 {
		o.FlushEvery = 2 * time.Second
	}
	if o.TTL == 0 {
		o.TTL = 24 * time.Hour
	}
	if o.TopN <= 0 {
		o.TopN = 1000
	}
	if o.MaxValLen <= 0 {
		o.MaxValLen = 256
	}
	if o.MaxDistinctPerKey <= 0 {
		o.MaxDistinctPerKey = 50_000
	}
	if o.WindowSize <= 0 {
		o.WindowSize = 24 * time.Hour
	}
	if o.SweepEvery <= 0 {
		o.SweepEvery = 5 * time.Minute
	}
}

// ── Store ────────────────────────────────────────────────────────────────────

// Store is the autocomplete cache. A nil *redis.Client (or a NewStoreFromURL
// that failed to connect) yields a disabled store: every write is a no-op and
// every read reports a miss, so callers degrade to ClickHouse cleanly — the
// same graceful-degradation contract as cache.NewNoop.
type Store struct {
	cli      *redis.Client
	disabled bool
	opt      Options
	policy   atomic.Value // holds Policy, swappable at runtime via SetPolicy

	mu  sync.Mutex
	cur *agg
}

// NewStore wraps an already-constructed, pooled *redis.Client. This is the
// preferred constructor inside the binary: build the client once and share it
// with internal/cache so both layers ride one connection pool. Pass cli == nil
// to get a disabled (no-op) store.
func NewStore(cli *redis.Client, opt Options) *Store {
	opt.applyDefaults()
	s := &Store{cli: cli, disabled: cli == nil, opt: opt, cur: newAgg()}
	s.policy.Store(opt.Policy)
	return s
}

// NewStoreFromURL is the standalone constructor: it parses a redis:// URL and
// dials its own pooled client, mirroring internal/cache.New (parse → NewClient
// → 3s PING). On parse/ping failure it returns a *disabled* store plus the
// error, so a caller that ignores the error still gets a working no-op store.
//
// Inside Coremetry prefer NewStore(sharedClient, …) to avoid a second pool.
func NewStoreFromURL(url string, opt Options) (*Store, error) {
	if url == "" {
		return NewStore(nil, opt), nil
	}
	opts, err := redis.ParseURL(url)
	if err != nil {
		return NewStore(nil, opt), fmt.Errorf("acache: parse redis url: %w", err)
	}
	cli := redis.NewClient(opts)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := cli.Ping(ctx).Err(); err != nil {
		_ = cli.Close()
		return NewStore(nil, opt), fmt.Errorf("acache: redis ping: %w", err)
	}
	return NewStore(cli, opt), nil
}

// Enabled reports whether the store talks to a live Redis.
func (s *Store) Enabled() bool { return s != nil && !s.disabled }

// SetPolicy swaps the cardinality policy atomically. Use it to apply an
// admin-edited allowlist/denylist at runtime without restarting.
func (s *Store) SetPolicy(p Policy) {
	if s == nil || p == nil {
		return
	}
	s.policy.Store(p)
}

func (s *Store) loadPolicy() Policy { return s.policy.Load().(Policy) }

// ── Ingestion (write path) ───────────────────────────────────────────────────

// agg is the in-memory delta accumulator drained on each flush. All maps hold
// frequency deltas for the current window; HLL keys hold the distinct set of
// values to PFADD.
type agg struct {
	services map[string]int64            // svc -> delta
	ops      map[string]map[string]int64 // svc -> op -> delta
	attrVals map[string]map[string]int64 // key -> value -> delta (CardTrack)
	attrHLL  map[string]map[string]struct{}
	keys     map[string]struct{}
}

func newAgg() *agg {
	return &agg{
		services: map[string]int64{},
		ops:      map[string]map[string]int64{},
		attrVals: map[string]map[string]int64{},
		attrHLL:  map[string]map[string]struct{}{},
		keys:     map[string]struct{}{},
	}
}

func (a *agg) empty() bool {
	return len(a.services) == 0 && len(a.ops) == 0 && len(a.attrVals) == 0 &&
		len(a.attrHLL) == 0 && len(a.keys) == 0
}

// ObserveSpan folds a span into the delta aggregator. It is non-blocking
// (a brief mutex around map writes only — no Redis I/O) and safe to call from
// the fire-and-forget ingest side-effect path with context.Background().
func (s *Store) ObserveSpan(sp *chstore.Span) {
	if s == nil || s.disabled || sp == nil {
		return
	}
	pol := s.loadPolicy()

	s.mu.Lock()
	defer s.mu.Unlock()
	a := s.cur

	if sp.ServiceName != "" {
		a.services[sp.ServiceName]++
		if sp.Name != "" {
			m := a.ops[sp.ServiceName]
			if m == nil {
				m = map[string]int64{}
				a.ops[sp.ServiceName] = m
			}
			m[sp.Name]++
		}
	}

	// Dedicated semconv columns first — these are the highest-value, lowest-
	// cardinality filter facets and live in their own typed columns.
	s.observeAttr(a, pol, "http.method", sp.HTTPMethod)
	s.observeAttr(a, pol, "http.route", sp.HTTPRoute)
	if sp.HTTPStatus != 0 {
		s.observeAttr(a, pol, "http.status_code", strconv.Itoa(int(sp.HTTPStatus)))
	}
	s.observeAttr(a, pol, "db.system", sp.DBSystem)
	s.observeAttr(a, pol, "rpc.system", sp.RPCSystem)
	s.observeAttr(a, pol, "rpc.method", sp.RPCMethod)
	s.observeAttr(a, pol, "peer.service", sp.PeerService)
	s.observeAttr(a, pol, "messaging.system", sp.MsgSystem)
	if sp.Kind != "" {
		s.observeAttr(a, pol, "kind", sp.Kind)
	}
	s.observeAttr(a, pol, "host.name", sp.HostName)
	s.observeAttr(a, pol, "deployment.environment", sp.DeployEnv)

	// Free-form span + resource attributes (order-aligned parallel arrays).
	// Skip keys already harvested from a dedicated column above: the OTLP
	// converter keeps well-known attrs in BOTH the typed column AND the attr
	// arrays, so without this guard a value (http.method, db.system…) would be
	// counted twice and its frequency rank inflated.
	for i, k := range sp.AttrKeys {
		if _, dup := dedicatedHarvested[k]; dup {
			continue
		}
		if i < len(sp.AttrValues) {
			s.observeAttr(a, pol, k, sp.AttrValues[i])
		}
	}
	for i, k := range sp.ResKeys {
		if _, dup := dedicatedHarvested[k]; dup {
			continue
		}
		if i < len(sp.ResValues) {
			s.observeAttr(a, pol, k, sp.ResValues[i])
		}
	}
}

// dedicatedHarvested are the semconv keys ObserveSpan reads from a span's
// dedicated typed columns; they're skipped in the AttrKeys/ResKeys loops to
// avoid double-counting. Kept in lockstep with the column harvest above and a
// subset of chstore.WellKnownTraceCol (db.statement / service.name are NOT
// here — they're not harvested from a column, so they flow through the arrays).
var dedicatedHarvested = map[string]struct{}{
	"http.method": {}, "http.route": {}, "http.status_code": {},
	"db.system": {}, "rpc.system": {}, "rpc.method": {},
	"peer.service": {}, "messaging.system": {}, "kind": {},
	"host.name": {}, "deployment.environment": {},
}

// observeAttr records one (key,value). Caller holds s.mu.
func (s *Store) observeAttr(a *agg, pol Policy, key, val string) {
	if key == "" || val == "" || len(val) > s.opt.MaxValLen {
		return
	}
	a.keys[key] = struct{}{} // record the key regardless of policy
	switch pol.Classify(key) {
	case CardTrack:
		m := a.attrVals[key]
		if m == nil {
			m = map[string]int64{}
			a.attrVals[key] = m
		}
		// Bound the per-window in-memory distinct set; the Redis-side ZSET is
		// further bounded to TopN by the flush trim. Re-counting an existing
		// value is always allowed (it's the common case for low-card keys).
		if _, seen := m[val]; seen || len(m) < s.opt.MaxDistinctPerKey {
			m[val]++
		}
	case CardHLL:
		m := a.attrHLL[key]
		if m == nil {
			m = map[string]struct{}{}
			a.attrHLL[key] = m
		}
		if len(m) < s.opt.MaxDistinctPerKey {
			m[val] = struct{}{}
		}
	case CardSkip:
		// key already recorded; nothing else to do
	}
}

// ── Flush + lifecycle ────────────────────────────────────────────────────────

// Start launches the background flusher (and, in WindowMode, the staleness
// sweeper). Both stop when ctx is cancelled, after a final drain. No-op on a
// disabled store.
func (s *Store) Start(ctx context.Context) {
	if s == nil || s.disabled {
		return
	}
	go s.runFlush(ctx)
	if s.opt.WindowMode {
		go s.runSweep(ctx)
	}
}

func (s *Store) runFlush(ctx context.Context) {
	t := time.NewTicker(s.opt.FlushEvery)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			// Final drain on shutdown using a short detached context so a
			// cancelled request ctx doesn't drop the last window.
			fc, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			_ = s.Flush(fc)
			cancel()
			return
		case <-t.C:
			_ = s.Flush(ctx)
		}
	}
}

// Flush drains the current delta window into Redis with a single pipeline.
// Exported so callers can force a flush (tests, graceful shutdown). Safe to
// call concurrently with ObserveSpan.
func (s *Store) Flush(ctx context.Context) error {
	if s == nil || s.disabled {
		return nil
	}
	// Swap the accumulator out under the lock; pipeline outside it so
	// ObserveSpan never waits on Redis.
	s.mu.Lock()
	a := s.cur
	s.cur = newAgg()
	s.mu.Unlock()
	if a.empty() {
		return nil
	}

	now := float64(time.Now().Unix())
	pipe := s.cli.Pipeline()
	touched := map[string]struct{}{}
	// rankKey -> setKey, registered for the WindowMode sweeper.
	tsReg := map[string]string{}

	mark := func(rankKey, setKey string) {
		touched[rankKey] = struct{}{}
		if setKey != "" {
			touched[setKey] = struct{}{}
		}
		if s.opt.WindowMode {
			tsReg[rankKey] = setKey
			touched[rankKey+tsSuffix] = struct{}{} // give the :ts companion a TTL too
		}
	}

	// Services.
	if len(a.services) > 0 {
		members := make([]interface{}, 0, len(a.services))
		for svc, d := range a.services {
			pipe.ZIncrBy(ctx, keyServicesRank, float64(d), svc)
			members = append(members, svc)
			if s.opt.WindowMode {
				pipe.ZAdd(ctx, keyServicesRank+tsSuffix, redis.Z{Score: now, Member: svc})
			}
		}
		pipe.SAdd(ctx, keyServices, members...)
		mark(keyServicesRank, keyServices)
	}

	// Operations per service.
	for svc, ops := range a.ops {
		setKey := keyOpsSetPrefix + svc
		rankKey := keyOpsRankPrefix + svc
		members := make([]interface{}, 0, len(ops))
		for op, d := range ops {
			pipe.ZIncrBy(ctx, rankKey, float64(d), op)
			members = append(members, op)
			if s.opt.WindowMode {
				pipe.ZAdd(ctx, rankKey+tsSuffix, redis.Z{Score: now, Member: op})
			}
		}
		pipe.SAdd(ctx, setKey, members...)
		mark(rankKey, setKey)
	}

	// Known attribute keys.
	if len(a.keys) > 0 {
		km := make([]interface{}, 0, len(a.keys))
		for k := range a.keys {
			km = append(km, k)
		}
		pipe.SAdd(ctx, keyAttrKeys, km...)
		touched[keyAttrKeys] = struct{}{}
	}

	// Attribute values (CardTrack) — ZINCRBY then trim to TopN.
	for key, vals := range a.attrVals {
		zk := keyAttrValPrefix + key
		for v, d := range vals {
			pipe.ZIncrBy(ctx, zk, float64(d), v)
			if s.opt.WindowMode {
				pipe.ZAdd(ctx, zk+tsSuffix, redis.Z{Score: now, Member: v})
			}
		}
		// Keep the TopN highest-scoring members: remove ranks [0 .. -(TopN+1)]
		// (ascending rank, so this drops everything below the top TopN).
		pipe.ZRemRangeByRank(ctx, zk, 0, trimStop(s.opt.TopN))
		mark(zk, "")
	}

	// Attribute cardinality (CardHLL) — approximate distinct count.
	for key, set := range a.attrHLL {
		ck := keyAttrCardPrefix + key
		members := make([]interface{}, 0, len(set))
		for v := range set {
			members = append(members, v)
		}
		pipe.PFAdd(ctx, ck, members...)
		touched[ck] = struct{}{}
	}

	// Staleness. Every touched key gets a sliding EXPIRE so nothing persists
	// forever — including HLL keys and (in WindowMode) the :ts companions, which
	// the per-member sweeper alone cannot reclaim. WindowMode additionally
	// registers the ranked keys for fine-grained per-member eviction and uses
	// WindowSize as the coarse backstop TTL.
	effTTL := s.opt.TTL
	if s.opt.WindowMode {
		effTTL = s.opt.WindowSize
		if len(tsReg) > 0 {
			fields := make([]interface{}, 0, len(tsReg)*2)
			for rankKey, setKey := range tsReg {
				fields = append(fields, rankKey, setKey)
			}
			pipe.HSet(ctx, tsRegistry, fields...)
		}
	}
	if effTTL > 0 {
		for k := range touched {
			pipe.Expire(ctx, k, effTTL)
		}
	}

	_, err := pipe.Exec(ctx)
	return err
}

// trimStop is the ZREMRANGEBYRANK stop index that keeps the top n members:
// remove ranks 0..-(n+1). For n=1000 that's -1001.
func trimStop(n int) int64 { return int64(-n - 1) }

// ── WindowMode sweeper ───────────────────────────────────────────────────────

func (s *Store) runSweep(ctx context.Context) {
	t := time.NewTicker(s.opt.SweepEvery)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			_ = s.Sweep(ctx)
		}
	}
}

// Sweep evicts members not seen within WindowSize. For each registered ranked
// key it reads the stale members from the companion :ts ZSET, ZREMs them from
// the rank ZSET, SREMs them from the companion set, then prunes the :ts ZSET.
// No-op unless WindowMode is on.
func (s *Store) Sweep(ctx context.Context) error {
	if s == nil || s.disabled || !s.opt.WindowMode {
		return nil
	}
	reg, err := s.cli.HGetAll(ctx, tsRegistry).Result()
	if err != nil {
		return err
	}
	cutoff := strconv.FormatInt(time.Now().Add(-s.opt.WindowSize).Unix(), 10)
	for rankKey, setKey := range reg {
		tsKey := rankKey + tsSuffix
		dead, err := s.cli.ZRangeArgs(ctx, redis.ZRangeArgs{
			Key: tsKey, ByScore: true, Start: "-inf", Stop: "(" + cutoff,
		}).Result()
		if err != nil || len(dead) == 0 {
			continue
		}
		members := make([]interface{}, len(dead))
		for i, m := range dead {
			members[i] = m
		}
		pipe := s.cli.Pipeline()
		pipe.ZRem(ctx, rankKey, members...)
		if setKey != "" {
			pipe.SRem(ctx, setKey, members...)
		}
		pipe.ZRemRangeByScore(ctx, tsKey, "-inf", "("+cutoff)
		_, _ = pipe.Exec(ctx)
	}
	return nil
}

// ── Read path (autocomplete) ─────────────────────────────────────────────────

// ValueCount is a single attribute value plus its observed frequency.
type ValueCount struct {
	Value string `json:"value"`
	Count int64  `json:"count"`
}

// GetServices returns service names ordered by frequency. If prefix is set it
// is matched case-insensitively (substring, or glob when it contains * / ?).
// total is the number of matches; hit is false on a cold cache or Redis error
// so the caller can fall back to ClickHouse.
func (s *Store) GetServices(ctx context.Context, prefix string, limit int) (names []string, total int, hit bool) {
	if s == nil || s.disabled {
		return nil, 0, false
	}
	return s.readRank(ctx, keyServicesRank, prefix, limit)
}

// GetOperations returns operation names for a service, ordered by frequency.
func (s *Store) GetOperations(ctx context.Context, svc, prefix string, limit int) (names []string, total int, hit bool) {
	if s == nil || s.disabled || svc == "" {
		return nil, 0, false
	}
	return s.readRank(ctx, keyOpsRankPrefix+svc, prefix, limit)
}

// readRank serves a frequency ZSET. Without a prefix it pulls the top `limit`
// directly; with a prefix it pulls the (bounded) full set and filters in rank
// order. total reflects the match count for the picker's "+N more" affordance.
func (s *Store) readRank(ctx context.Context, rankKey, prefix string, limit int) ([]string, int, bool) {
	limit = clampLimit(limit)
	card, err := s.cli.ZCard(ctx, rankKey).Result()
	if err != nil || card == 0 {
		return nil, 0, false // error or cold key → miss
	}
	if prefix == "" {
		names, err := s.cli.ZRevRange(ctx, rankKey, 0, int64(limit-1)).Result()
		if err != nil {
			return nil, 0, false
		}
		return names, int(card), true
	}
	all, err := s.cli.ZRevRange(ctx, rankKey, 0, -1).Result()
	if err != nil {
		return nil, 0, false
	}
	matches := filterByPattern(all, prefix)
	total := len(matches)
	if total > limit {
		matches = matches[:limit]
	}
	return matches, total, true
}

// GetAttributeKeys returns the known attribute keys (sorted). hit is false on a
// cold cache or error.
func (s *Store) GetAttributeKeys(ctx context.Context) (keys []string, hit bool) {
	if s == nil || s.disabled {
		return nil, false
	}
	ks, err := s.cli.SMembers(ctx, keyAttrKeys).Result()
	if err != nil || len(ks) == 0 {
		return nil, false
	}
	sort.Strings(ks)
	return ks, true
}

// GetAttributeValues returns cached values for an attribute key.
//
//   - CardTrack key: a frequency-ranked value list (prefix-filtered), freeText
//     false, approxCount 0.
//   - CardHLL key: no values; approxCount is the HyperLogLog distinct estimate
//     and freeText is true, signalling the picker to render a free-text input
//     ("~N distinct values") instead of a dropdown.
//   - CardSkip key (or cold): hit false → caller falls back to ClickHouse.
func (s *Store) GetAttributeValues(ctx context.Context, key, prefix string, limit int) (vals []ValueCount, approxCount int64, freeText bool, hit bool) {
	if s == nil || s.disabled || key == "" {
		return nil, 0, false, false
	}
	limit = clampLimit(limit)
	switch s.loadPolicy().Classify(key) {
	case CardHLL:
		n, err := s.cli.PFCount(ctx, keyAttrCardPrefix+key).Result()
		if err != nil || n == 0 {
			return nil, 0, false, false // miss → freeText false so the caller falls back unambiguously
		}
		return nil, n, true, true
	case CardSkip:
		return nil, 0, false, false
	default: // CardTrack
		zk := keyAttrValPrefix + key
		zs, err := s.cli.ZRevRangeWithScores(ctx, zk, 0, -1).Result() // bounded by TopN
		if err != nil || len(zs) == 0 {
			return nil, 0, false, false
		}
		for _, z := range zs {
			v, ok := z.Member.(string)
			if !ok {
				continue // defensive: members are always written as strings
			}
			if prefix != "" && !matchPattern(v, prefix) {
				continue
			}
			vals = append(vals, ValueCount{Value: v, Count: int64(z.Score)})
			if len(vals) >= limit {
				break
			}
		}
		return vals, 0, false, true
	}
}

// ── Pattern matching (mirrors the CH picker semantics) ───────────────────────

func clampLimit(n int) int {
	if n <= 0 {
		return 200
	}
	if n > 1000 {
		return 1000
	}
	return n
}

// matchPattern mirrors the server-side picker rule: case-insensitive; a bare
// term is a substring match; a term containing * or ? is a glob (full match),
// where * matches any run and ? matches any single rune. Unlike path.Match it
// does NOT treat '/' specially, so it works on http.route values.
func matchPattern(value, pattern string) bool {
	v := strings.ToLower(value)
	p := strings.ToLower(pattern)
	if !strings.ContainsAny(p, "*?") {
		return strings.Contains(v, p)
	}
	return glob(p, v)
}

// filterByPattern returns the matches in input order.
func filterByPattern(values []string, pattern string) []string {
	out := make([]string, 0, len(values))
	for _, v := range values {
		if matchPattern(v, pattern) {
			out = append(out, v)
		}
	}
	return out
}

// glob is a minimal * / ? matcher over runes (no '/' special-casing).
func glob(pattern, s string) bool {
	pr := []rune(pattern)
	sr := []rune(s)
	// star is the position of the most recent '*' in pattern; match is where
	// in s that star started matching — standard linear backtracking glob.
	pi, si := 0, 0
	star, mark := -1, 0
	for si < len(sr) {
		if pi < len(pr) && (pr[pi] == '?' || pr[pi] == sr[si]) {
			pi++
			si++
		} else if pi < len(pr) && pr[pi] == '*' {
			star = pi
			mark = si
			pi++
		} else if star != -1 {
			pi = star + 1
			mark++
			si = mark
		} else {
			return false
		}
	}
	for pi < len(pr) && pr[pi] == '*' {
		pi++
	}
	return pi == len(pr)
}
