# internal/acache — Traces filter autocomplete cache

Microsecond service / operation / attribute-value autocomplete for the
`/traces` (and `/explore`) filter pickers, served straight out of Redis
instead of round-tripping ClickHouse. Redis runs as a **pure cache**
(`save ""`, `appendonly no`, `maxmemory-policy allkeys-lru`) — data loss is
acceptable because every key is rebuildable from the span stream and reads
fall back to CH on a miss.

Two halves: an **ingestion** side that folds spans into Redis sorted sets, and
a **read API** that serves the pickers.

---

## Why local pre-aggregation (not a ZINCRBY per span)

At 1B spans/day (~11.5k/s) a `ZINCRBY +1` per span per facet would be tens of
thousands of Redis round-trips per second. Instead `ObserveSpan` only updates
an **in-memory delta map** under a brief lock (no I/O), and a background
flusher drains the whole window into Redis with **one pipeline** every
`FlushEvery` (default 2s). Collapsing a window of a `(service, op, value)` into
a single `ZINCRBY <delta>` is mathematically identical to N×`+1` but costs one
network op. This is the production-correct shape of "batch all writes with a
pipeline."

---

## Redis key layout

| Key | Type | Holds |
|---|---|---|
| `coremetry:services` | SET | service names |
| `coremetry:services:rank` | ZSET | service → frequency |
| `coremetry:ops:<svc>` | SET | operation names for a service |
| `coremetry:ops:rank:<svc>` | ZSET | operation → frequency |
| `coremetry:attr:keys` | SET | known attribute keys |
| `coremetry:attr:<key>` | ZSET | value → frequency (low/med cardinality) |
| `coremetry:attr:card:<key>` | HLL | approximate distinct count (high cardinality) |

**Cardinality policy** decides, per attribute key, whether values are kept as a
ranked ZSET (`CardTrack`), counted with a HyperLogLog (`CardHLL`), or ignored
(`CardSkip`). It is swappable at runtime (`SetPolicy`) so a new attribute is a
config change, never a code change.

**Staleness** — two strategies:
- default: sliding `EXPIRE <key> 86400` re-applied on every flush that touches
  the key (a service unseen for 24h drops out wholesale);
- `WindowMode`: per-member time windows via a companion `<key>:ts` ZSET
  (`member → lastSeenUnix`) + a background sweeper (`ZREMRANGEBYSCORE`) that
  evicts individual stale members.

---

## A) Ingestion — wiring the write path

`ObserveSpan` is fire-and-forget and never blocks on Redis, so hook it into the
existing async ingest side-effect path (the consumer flusher runs on
`context.Background()`).

```go
// main.go — construct once, share the pooled client with internal/cache.
opts, _ := redis.ParseURL(cfg.Redis.URL)   // one parse
rdb := redis.NewClient(opts)               // one pool, shared below

acStore := acache.NewStore(rdb, acache.Options{
    Policy:     buildPolicy(cfg),  // from env CSV or system_settings (see §C)
    FlushEvery: 2 * time.Second,
    TTL:        24 * time.Hour,
    TopN:       1000,
})
acStore.Start(ctx)                          // launches the flusher goroutine

// internal/otlp ingester — call per span on the side-effect path.
func (ing *Ingester) addSpan(sp *chstore.Span) bool {
    ing.acache.ObserveSpan(sp)   // non-blocking: in-memory delta only
    return ing.Spans.Add(sp)
}
```

Disabled-store contract: `acache.NewStore(nil, …)` (or a `NewStoreFromURL`
that failed to connect) returns a **no-op** store — every write is dropped,
every read misses — so single-instance / Redis-down installs degrade exactly
like `cache.NewNoop`.

```go
// Standalone (own pool) if you don't want to thread the shared client:
acStore, err := acache.NewStoreFromURL(cfg.Redis.URL, acache.Options{})
if err != nil { log.Printf("[acache] %v — running disabled", err) } // still usable
```

---

## B) Read API — autocomplete endpoints

Each getter returns a `hit bool`; on a miss the handler falls back to the
existing CH-backed picker (DB fallback is **optional** and the caller decides).
Response shapes stay drop-in compatible with the current pickers
(`{names,total,hasMore}`, `{scope,key,count}[]`, `{value,count}[]`).

```go
// GET /api/service-names — try the cache, fall back to CH on a miss.
func (s *Server) getServiceNames(w http.ResponseWriter, r *http.Request) {
    q := r.URL.Query().Get("q")
    limit := atoiDefault(r.URL.Query().Get("limit"), 200)

    if names, total, hit := s.acache.GetServices(r.Context(), q, limit); hit {
        writeJSON(w, namesResp{Names: names, Total: total, HasMore: total > len(names)})
        return
    }
    // miss → existing serveCached + ClickHouse path
    s.serveServiceNamesFromCH(w, r, q, limit)
}

// GET /api/operation-names?service=…
names, total, hit := s.acache.GetOperations(r.Context(), svc, q, limit)

// GET /api/attribute-keys
keys, hit := s.acache.GetAttributeKeys(r.Context())   // []string

// GET /api/attribute-values?key=…
vals, approx, freeText, hit := s.acache.GetAttributeValues(r.Context(), key, q, limit)
if hit && freeText {
    // high-cardinality key: render a free-text input, not a dropdown.
    writeJSON(w, attrValuesResp{FreeText: true, ApproxDistinct: approx})
} else if hit {
    writeJSON(w, attrValuesResp{Values: vals}) // []{value,count}, ranked
}
```

---

## C) Config — allowlist/denylist without code changes

The cardinality policy is an interface; build a `StaticPolicy` from whatever
config source fits. Two options (the codebase supports both patterns):

**Env CSV** (boot-time, via the existing `splitCSV` helper):
```go
func buildPolicy(cfg config.Config) acache.Policy {
    low  := splitCSV(os.Getenv("COREMETRY_ACACHE_LOWCARD_KEYS"))
    high := splitCSV(os.Getenv("COREMETRY_ACACHE_HIGHCARD_KEYS"))
    if len(low) == 0 && len(high) == 0 {
        return acache.DefaultPolicy()
    }
    return acache.NewStaticPolicy(low, high, acache.CardHLL)
}
```

**Admin-editable `system_settings`** (runtime, no restart — recommended since
new attributes appear at runtime). Store an `{low:[…], high:[…], default}`
JSON blob under a `acache_policy` key (mirror `branding`/`pipeline_rules`),
load at boot, and on `PUT /api/acache-policy` (admin-gated + `s.audit(...)`)
call `acStore.SetPolicy(newPolicy)` to swap it atomically:
```go
acStore.SetPolicy(acache.NewStaticPolicy(blob.Low, blob.High, acache.CardHLL))
```

`DefaultPolicy()` ships a reasonable OTel-shaped allowlist (http/db/rpc/cloud
low-card facets) and denylist (ids, urls, statements); unknown keys default to
`CardHLL` so an unclassified key is counted but never stores values — bounding
memory against a cardinality explosion.

---

## Operational notes

- **Connection pool**: never opens a connection per call — `NewStore` takes an
  already-pooled `*redis.Client`; `NewStoreFromURL` dials one pool up front.
  Prefer sharing the `internal/cache` client so the binary keeps a single pool.
- **Bounded memory**: per-flush in-memory distinct values per key are capped
  (`MaxDistinctPerKey`, default 50k); the persistent value ZSET is trimmed to
  `TopN` (default 1000) on every flush; oversized values (`MaxValLen`, default
  256B) are dropped before they reach the aggregator.
- **Lock contention**: `ObserveSpan` holds a single mutex only for map writes
  (microseconds). If a profile shows contention above ~50k spans/s, shard the
  aggregator by `hash(serviceName)` and merge at flush.
- See `redis-acache.conf.example` for the pure-cache Redis config.
