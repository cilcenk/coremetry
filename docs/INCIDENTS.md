# Performance pitfalls — historical incidents

Full narratives behind the one-line pitfall rules in CLAUDE.md.
Each entry references the incident-shaped fix. Avoid re-living them.

- **`timeRangeToNs(range)` in JSX / IIFE on every render** —
  re-evaluates `now()`, breaks `useEffect` dep equality, infinite
  refetch (v0.5.184). Always `useMemo([range])` or call inside
  `useEffect` body where deps are explicit.
- **Cache key = `len(set)`** — cross-set poisoning where two
  different sets sharing the same cardinality return each
  other's data (v0.5.187). Stable digest required.
- **`table-layout: fixed` + `white-space: nowrap` + small fixed
  width** — silently clips text. Use `min-width` + `max-width`
  + `ellipsis` + `title` attribute for tooltip.
- **ES `query_string` with `case_insensitive: true`** — rejected
  by ES 8.x as an unknown field (v0.5.231). Don't add it back.
  Standard analyzer already case-folds.
- **Per-pattern `_search` over N curated patterns** — N
  round-trips. Use `_msearch` for a single coordinator fan-out
  (v0.5.241).
- **`significant_text` without `background_filter`** — ES
  defaults the background to the whole index = catastrophic on
  billion-doc indices. Always cap baseline window AND wrap in
  `sampler` to bound per-shard scoring (v0.5.243).
- **Drain-style templating against raw logs at billion-row
  scale** — sample-based puller (1000/5min) > full scan. Sample
  bias on rare templates is fine because the curated detector
  + significant_text panel pick those up (v0.5.244 architecture
  note).
- **Polling without `document.hidden`** — burns mobile/laptop
  battery + idle API traffic. See PublicStatus.tsx pattern.
  Always:
  ```js
  setInterval(() => { if (!document.hidden) fetchOnce(); }, 30_000);
  ```
- **Unit-mixing in SQL/time templates (`toDate(time) + INTERVAL %s`
  with `%s` ∈ {"30 DAY", "1 HOUR"})** — `toDate(time) + INTERVAL 1
  HOUR` = midnight + 1h = 01:00 of the SAME day, not "1 hour from
  the row's time". v0.6.36: retention.spans = "1h" via admin UI
  silently TTL'd every span after 01:00 — operator saw inconsistent
  /traces counts because merges ran intermittently. Rule: ANY
  template that accepts a value+unit (Nh/Nd, ms/s, MB/GB) MUST
  have a table-driven test exercising **every** unit at ship time.
  For sub-day TTLs use `<col> + INTERVAL N HOUR` (row-level); for
  day TTLs use `toDate(<col>) + INTERVAL N DAY` (partition-aligned).
  Never let `toDate()` wrap a sub-day calculation. See
  [internal/chstore/retention_test.go](../internal/chstore/retention_test.go)
  for the canonical example.
- **Combined-MV DROP at billion-row scale** — `DROP TABLE <mv>`
  trips CH's `max_table_size_to_drop` guard (default 50 GB) once
  the hidden `.inner_id.<uuid>` storage is large, and a per-query
  `SETTINGS max_table_size_to_drop=0` on the MV does NOT reach the
  inner drop (verified on 24.8). Drop the inner DIRECTLY with the
  override, then the empty MV (`dropCombinedMV`, v0.8.190). Every
  boot migration that recreates an MV routes through it.
- **Reservoir `quantilesState` for `duration_q_state` at scale** —
  the 8192-sample reservoir is ~64 KiB/row; merging a wide window
  blows the per-query memory limit (code 241) + the timeout (code
  159). The summary MVs use `quantilesTDigestState` (~4.3 KiB/row,
  parallel-safe, ≤2% error, v0.8.194) — the concrete form of the
  "quantile() past ~1M rows → TDigest" rule. Never put reservoir
  `quantiles` in an MV state column.
- **MV aggregate-type change = rolling-deploy read-error window** —
  an atomic MV state-type swap at boot means OLD pods read the NEW
  MV with the old finalizer (`quantilesMerge` on a TDigest column →
  code 43) until they roll. `maxUnavailable:0` keeps the rollout
  graceful but lengthens the window; finish the rollout fast (no
  `dev`-tagged stragglers), or use a dual-column transition to
  avoid it (v0.8.194).
- **Hardcoded database name in SQL** — `FROM coremetry.spans`
  breaks on any install whose CH database isn't literally
  `coremetry` (e.g. `coremetry_prod`) → "Database coremetry does
  not exist" (code 6, v0.8.195). The chstore conn defaults to
  `cfg.Database`, so reference telemetry tables UNQUALIFIED.
- **`toDateTime64('<RFC3339>', 9, 'UTC')` rejects the trailing
  `Z`** — `time.RFC3339Nano` emits `…Z`; CH's DateTime64 string
  parser errors at the Z (code 6, "Cannot parse string … as
  DateTime64", parsed-just-`…714`). Format UTC with a space and NO
  tz designator (`chDateTime64Arg`, v0.8.197). Any `toDateTime64(?)`
  bind arg MUST be tz-less.
- **Collector wedge after a coremetry rollout** — the OTel
  collector's gRPC exporter resolves to "zero addresses" when the
  headless ingest Service's ready endpoints drain during a roll
  (default `maxUnavailable: 25%` + slow boot) and stays wedged
  (503s every app's telemetry — only coremetry's own self-obs keeps
  landing) until the collector is restarted. Fix: explicit
  `maxUnavailable: 0` on the Deployment so endpoints never hit zero
  (v0.8.193). Pre-fix installs: restart the collector once.
