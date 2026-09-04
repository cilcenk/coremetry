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
- **Service filter + long window: stage 2 merged the whole window**
  (v0.10.265, operator-reported on prod 7d + service + hasError, time
  sort: every "Next" 40+ s and a "shortened to …" badge). The MV list
  path had two service shapes and both were linear in the window:
  post-agg filters (hasError/rootOnly/min/max) ran stage 2 with
  `trace_id GLOBAL IN (index GROUP BY trace_id)` — the set prunes
  nothing when the service touches most traces (gateway: 409k of 7d
  traces locally), so trace_summary_5m was merged end to end (1.7-2.0M
  rows, 12 s cap, two halvings); the plain shape ran `GROUP BY
  trace_id ORDER BY maxMerge(last_seen)` over the whole service index
  (800k rows, 10-15 s) and ignored `order=asc`. Fix: a recency slice
  on `trace_service_index_5m` in primary-key order (service_name,
  time_bucket, trace_id) with `optimize_read_in_order` and no GROUP
  BY — the same contract as the no-service `traceRecencySlice` —
  feeding the already-bounded stage 2 (`trace_id IN (ids)` + floor at
  the cut for desc; asc keeps the window floor because its ids sit at
  the window start). Lesson: a `trace_id IN (subquery)` bound is only a
  bound if the subquery is small; when the entity is a hub (gateway,
  root service) it is the whole window in disguise — bound by TIME
  (PK-ordered slice), never by set membership. Regression pin:
  `trace_service_slice_test.go`.
- **ClickHouse integer promotion bites the scanner** (v0.10.269):
  `toUInt64(x) - ?` with a UInt64 bind is promoted by ClickHouse to
  Int64 (unsigned minus unsigned is signed), and `intDiv(Int64,
  UInt64)` stays Int64 — scanning it into a Go `uint64` fails at
  runtime with "converting Int64 to *uint64 is unsupported" while
  every unit test stays green (no CH in tests). Scan arithmetic
  results into `int64` and clamp, or cast explicitly in SQL
  (`toUInt64(intDiv(...))`). Same class as the tz-less DateTime64
  bind: the type is decided by the server, not by the Go side.
- **Errors + attribute filter + service: whole-service GROUP BY, halved
  window, "No traces found"** (v0.10.307, operator-reported on prod: 6h
  with Errors + `name = POST` returned rows, 3h with the same filters
  returned nothing although every row sat inside the 3h window; "it has
  happened before"). An attribute filter forces the raw path; with
  another span predicate present, `hasError` is not span-local (the
  trace must have an error span AND a span matching the filter, possibly
  different spans), so Errors moved into HAVING and stage 1 grouped
  every trace of the service in the window — `idx_status` pruned
  nothing. On prod that blew the 25 s stage-1 budget,
  `narrowOnExhaustion` halved the window and the page came back empty
  with a `narrowedFromNs` the UI never showed. Fix: error-first
  candidate narrowing (`trace_error_first.go`) — a cheap
  `status_code = 'error'` scan in primary-key order (service_name,
  time) returns the service's error trace ids (cap 5000, `rankedWithin`
  set when hit); every later stage carries `trace_id IN (ids)` through
  `TraceFilter.CandidateIDs`, so GROUP BY sees at most the candidates
  and an empty answer is exact ("no error traces") rather than "budget
  ran out". `TracesEmpty` now says "No traces in the shortened window"
  when `narrowedFromNs` is set. Local measurement (api-gateway 3h,
  fixture where every granule carries errors and the window is two
  index granules): stage 1/2 unchanged at 65k rows, the extra pass
  costs ~0.7 s — the fixture cannot show the prune; the prod shape (a
  handful of error traces in a hub service) is where the `idx_trace`
  bloom and the bounded GROUP BY pay, and a candidate set in the
  thousands saturates the bloom (only the GROUP BY bound remains).
  Lesson: when a filter cannot be expressed on the primary key, find
  the cheapest predicate that IS on it and let that drive the candidate
  set; and an empty page under a narrowed window is a budget statement,
  never a fact — say so in the UI. Regression pins:
  `trace_error_first_test.go` (eligibility, SQL shape, order-of-
  operations source pin). v0.10.308 fixed the pin itself: a substring
  assertion on `name = ?` matched `service_name = ?`, and the 307 gate
  chain read a missing test-output file as "no FAIL" — negative greps
  are not gates without positive evidence (`[ -s f ] && grep -q '^ok'`).
- **Traces strip empty under a non-entry-span filter** (v0.10.323,
  operator-reported on prod: `service.name = X AND db.statement ~ …`
  listed rows while the volume strip said "No traces in view to
  bucket"). Since v0.10.268 the strip counts entry spans (`kind IN
  (server, consumer)`) and ANDs the operator's filters onto the SAME
  span; the table applies them trace-wide (any span of the trace).
  `db.statement` never lives on an entry span, so the strip's predicate
  was unsatisfiable. Fix: `stripScope` — when a filter targets a key
  that does not live on entry spans (db., messaging., custom
  attributes) or a free-text search is present, the strip counts
  MATCHING SPANS instead (kind restriction dropped, unit "spans",
  header labels and hint say so; the median is then the span's own
  latency — exactly the question a db.statement filter asks). Entry-span
  keys (service/http/url/status/cluster/channel_code…) keep the trace
  count. Lesson: two surfaces sharing one filter must share its
  SEMANTICS (span-local vs trace-wide), or the cheaper one must say what
  it counts. Pin: `volumeSeries.test.ts` (stripScope table).
- **Waterfall rows vanish past the first screen on 400+ span traces**
  (v0.10.324, operator-reported on prod with a 1065-span trace: ~40 rows
  then blank). v0.10.278 virtualised the waterfall with
  `useWindowVirtualizer`, i.e. it listened to WINDOW scroll — but the
  app never scrolls the window: `#content` (flex:1 + overflow:auto) and,
  on the trace page, `.tc-wf` are the scroll containers. No scroll event
  ever reached the virtualiser, `scrollOffset` stayed 0, only the first
  viewport + 20 overscan rows were ever rendered while the spacer kept
  the full height. jsdom cannot see this (no layout), which is why the
  1000-row virtual test stayed green. Fix: `lib/scrollParent.ts`
  (nearest overflow auto/scroll ancestor + offset within it as
  `scrollMargin`) and a dual-mode `useVirtualizer` — element mode when a
  scroll parent exists, react-virtual's own window composition
  (`observeWindow*` + `windowScroll`) otherwise. Lesson: a virtualiser is
  a contract with ONE scroll container; pin the container choice in
  source (`traceWaterfallVirtual.test.tsx`), because the DOM test cannot.

### v0.10.338 — Çekmecede tam kimlik "…" ile kırpıldı (genel `tbody td` kuralı)

Operator-reported: rollout çekmecesinde revizyon ve imaj (repo:tag) satırları
sonundan kırpılıyordu; aynı workload birden çok cluster'a çıktığı hâlde küme
adı görünmüyordu. Kırpmanın sebebi bileşen değil, `globals.css`'teki genel
`tbody td { white-space: nowrap; max-width: 320px; text-overflow: ellipsis }`
kuralı: hücreye verilen `wordBreak: break-all` nowrap'i kaldırmadığı için
hiçbir etkisi yoktu. Ders: "kırpma" pitfall'ının üçüncü şekli — sınıfsız bir
`<td>` her çekmecede 320 px'e kilitli. Okunması gereken tam metin taşıyan
hücre `.td-full` alır (sarar, kırpmaz) ve yanına kopyalama düğmesi konur;
`RolloutDrawer.pins.test.ts` hücreleri ve CSS kuralını pinler.
