# Coremetry load + large-data-volume test harness

A runnable, ramp-able, stoppable harness that stresses BOTH halves of
Coremetry at once:

- **Write path** — crank the Go demo's OTLP firehose to a target spans/sec to
  stress CH RSS, `async_insert` batching and the materialized-view fan-out.
- **Read path** — hammer the hot endpoints (`/api/services`, `/api/problems`,
  `/api/spans/metric`, `/api/traces`, `/api/logs`) concurrently and compare
  p99 against the CLAUDE.md budgets.

Prior context: the [scale-test result](../../../.claude/...) (7.4×, 114M
spans) found MV reads stayed flat and raw-scan stayed within budget, but a
local **2 GiB ClickHouse OOM'd on MV bulk inserts**. This harness is built to
walk you toward that cliff *deliberately and reversibly*, watching CH RSS so
you can stop one step short.

> The operator runs this against the cluster. Nothing here auto-runs.

---

## 0. The single ingest knob

`cmd/demo` (the Go synthetic generator) is rate-controlled by one flag:

```
-rps <float>   scenarios per second   (default 2.0)
```

Each *scenario* is a multi-span trace (~10 spans avg across the ~45-service
banking mesh — `cmd/demo/main.go` + `bank_extra.go` have ~182 `t.Add` call
sites spread over ~30 weighted scenarios). The live diurnal/incident model in
`cmd/demo/realism.go` multiplies the driver's emission rate by
`L.rateFactor()` (0.28 overnight → ~1.0 at the 10:00 peak, +15–40 % during
incidents/spikes). So:

```
effective spans/sec  ≈  rps × ~10 × rateFactor
```

That means **nominal `-rps` is a floor, not the truth** — always read the real
accepted rate off `watch.sh` (the `spans_acc` delta), not the flag.

Metrics volume is *independent* of `-rps`: the demo flushes per-service gauges
+ histograms on a fixed 10 s ticker, so metric_points/sec scales with the
service count, not the trace rate. Logs ride traces (~1 log per 3 traces +
denser error logs during incidents).

New in this harness: a `-duration <dur>` flag (`0` = forever, the original
behaviour) so a stress step self-terminates even if the controlling script's
trap is bypassed. It cancels the same context `SIGTERM` does, so the final
metrics flush still runs.

### Where `-rps` is wired

| Surface | Knob |
|---|---|
| Host / compose | `go run ./cmd/demo -endpoint <otlp> -rps <N> -duration <D>` |
| docker-compose | the `go-demo` service `command: [-endpoint, …, -rps, "3"]` |
| Helm / k8s | `charts/coremetry/values.yaml` → `goDemo.rps` (default 3) |

### Endpoints

- **OTLP/HTTP ingest** — `http://localhost:14318` (compose host port → the
  collector's 4318) or `http://otel-collector:4318` in-cluster. To bypass the
  collector and hit Coremetry's direct OTLP/HTTP, use `http://localhost:8088`
  (the same port as the API; `/v1/traces|logs|metrics|profiles` are public per
  `internal/auth/auth.go` SkipPath).
- **API / read load** — `http://localhost:8088`.

---

## 1. The scripts (`scripts/loadtest/`)

| Script | Role |
|---|---|
| `ingest-ramp.sh` | Walks `-rps` up in steps (binary or k8s mode), holds each level, soaks at target. Self-limiting via `-duration`. |
| `read-load.sh`   | Logs in once, drives concurrent read load at the hot endpoints with `hey`/`vegeta`/bash-curl, prints latency percentiles. |
| `watch.sh`       | Polls `/api/health` + `/api/admin/stats` + `/api/admin/cache-stats` + `docker stats <CH>` every 10 s into a CSV. |
| `stop.sh`        | Kill switch — SIGTERM every demo process (binary) or reset `goDemo.rps` / scale to 0 (k8s). |

All four are `bash -n`-clean and `chmod +x`. `--help` on each prints usage.

Optional but recommended on the runner host: `hey`
(`go install github.com/rakyll/hey@latest`) for true p99, and `jq` for the
watch CSV's admin columns. Both degrade gracefully if absent.

---

## 2. Run it — local docker-compose

Four terminals (or `tmux` panes). **Start the watcher first**, ramp last.

```bash
# T1 — telemetry recorder (leave running the whole test)
scripts/loadtest/watch.sh -u http://localhost:8088 -i 10 \
    --out loadtest-$(date +%H%M).csv

# T2 — read load (re-run as needed; each run is bounded by -d)
scripts/loadtest/read-load.sh -u http://localhost:8088 -c 20 -d 120

# T3 — RAMP ingest: 20 → 300 rps in +20 steps, 2-min holds, 10-min soak
scripts/loadtest/ingest-ramp.sh -e http://localhost:14318 \
    -s 20 -t 300 --step 20 --hold 120 --soak 600

# T4 — your hand:  STOP anything immediately
scripts/loadtest/stop.sh
```

The default compose `go-demo` already emits ~3 rps; either stop it first
(`docker compose stop go-demo`) so your ramp is the only producer, or just let
it add a small constant floor.

---

## 3. Run it — minikube / k8s

```bash
# Ramp the go-demo pod's rps (patches the Deployment arg in place)
scripts/loadtest/ingest-ramp.sh -m k8s --release coremetry -n default \
    -s 20 -t 150 --step 20 --hold 120 --soak 600

# Read load through a port-forward / Route to the API
scripts/loadtest/read-load.sh -u http://localhost:8088 -c 20 -d 120

# Watch (CH RSS column needs the CH container locally; in k8s use
# `kubectl top pod` separately — watch.sh still records health + drops)
scripts/loadtest/watch.sh -u http://localhost:8088 -i 10

# Reset when done
scripts/loadtest/stop.sh -m k8s            # back to rps 3
scripts/loadtest/stop.sh -m k8s --zero     # or scale go-demo to 0
```

> Minikube runs monolithic; the single CH pod is the constraint. Size its
> memory limit deliberately and watch `kubectl top pod` for the CH pod
> alongside `watch.sh`.

---

## 4. How to RAMP and how to STOP (safety)

**Ramp.** `ingest-ramp.sh` never jumps to the top rate. It launches one demo
process per `--step`, holds `--hold` seconds, then moves up — so CH RSS / queue
depth climb *gradually* and you can abort before an OOM. The default is
20→target in +20 steps.

**Self-limit.** Every demo child gets `-duration (hold+5)s`, a dead-man switch:
a lost SSH / `kill -9` of the script can't leave a firehose running.

**Stop, three ways:**
1. `Ctrl-C` in the ramp terminal — kills the current child, exits.
2. `scripts/loadtest/stop.sh` from any shell — SIGTERMs all demo procs.
3. k8s: `scripts/loadtest/stop.sh -m k8s [--zero]`.

Ingest drains to baseline within ~10 s of the producers stopping (the CH
flusher empties the queue; the queue is in-memory, bounded — nothing replays).

**Read load** is read-only and bounded by `-d`; `Ctrl-C` stops it.

---

## 5. MEASUREMENT PLAN — what to watch + pass/fail

`watch.sh` records a CSV row every 10 s. Columns and how to read them:

### A. Ingest keeping up (write path)

| Signal (source: `/api/health`) | CSV column | PASS | FAIL |
|---|---|---|---|
| Span queue depth vs capacity | `spans_qd / spans_cap` | < 70 % steady | sustained ≥ 90 % → `status:overloaded`, health returns **503** |
| **Spans dropped** (queue full) | `spans_drop` | **stays 0** | any non-zero = backpressure data loss |
| **Spans write-failed** (CH insert errored) | `spans_wfail` | **stays 0** | any non-zero = silent CH write loss |
| Logs / metrics dropped | `logs_drop`, `metrics_drop` | 0 | non-zero |
| Accepted span rate | Δ`spans_acc` / interval | tracks your effective target | flattens below target while queue fills = consumer-bound |

The hard line: **`spans_drop` and `spans_wfail` must remain 0 for the whole
run.** The moment either climbs, you've found the ingest ceiling for this CH
sizing — record the rps and back off one step. (`status` flips
`ok`→`degraded`(70 %)→`overloaded`(90 %, 503) before drops start, giving you a
warning band.)

### B. ClickHouse RSS — the OOM cliff

| Signal | CSV column | PASS | FAIL |
|---|---|---|---|
| CH container RSS | `ch_rss_mb` (`docker stats`) | plateaus below the CH memory limit | marches toward the limit during MV bulk inserts → OOMKill |

This is the [scale-test](.) finding: a 2 GiB CH OOM'd on MV bulk inserts even
though reads were fine. If `ch_rss_mb` is climbing toward your limit while
`spans_drop` is still 0, you are heading for that cliff — **stop one step
early.** Cross-check with `/api/admin/stats` → `tables[]` (per-table rows +
`bytesOnDisk`, `parts`) to see which table / MV is growing fastest, and
`ingest.spansPerSec/logsPerSec/metricsPerSec` for CH's own view of the rate.

### C. Read-path p99 vs the CLAUDE.md budgets

`read-load.sh` prints percentiles per endpoint. Compare:

| Endpoint | Budget (CLAUDE.md) | PASS |
|---|---|---|
| `/api/services` | hot — **p99 < 50 ms warm** | p99 ≤ 50 ms |
| `/api/problems` | hot — **p99 < 50 ms warm** | p99 ≤ 50 ms |
| `/api/spans/metric` | **p99 < 200 ms warm**, < 1 s cold | p99 ≤ 200 ms warm |
| `/api/traces` | p99 < 200 ms warm, < 1 s cold | p99 ≤ 200 ms warm |
| `/api/logs` | p99 < 200 ms warm, < 1 s cold | p99 ≤ 200 ms warm |
| `/api/spans/heatmap` (`--heatmap`) | **< 3 s for ≤ 6 h window** | p99 < 3 s |

"Warm" = the fixed-window run (serveCached has warmed: 30 s TTL on span-metric,
15 s on logs). Run with `--fresh-window` to measure **cold** reads against the
< 1 s budget. The interesting result is read p99 **under concurrent ingest
load** — if `/api/services` / `/api/problems` blow their 50 ms budget while the
ramp is running, the MV read path is contending with the write path (expected
failure mode to characterize).

### D. Cache effectiveness (Redis / L1)

`/api/admin/cache-stats` → `counts` is a tier→hit-count map (`l1`, `redis`,
`singleflight`, `miss`). CSV columns `cache_l1 / cache_redis / cache_miss` are
cumulative; the Δ between ticks is the per-interval mix.

| Signal | PASS | INVESTIGATE |
|---|---|---|
| `l1` + `redis` hits dominate after warm-up | hit ratio rising under load | `miss` Δ growing → cache key churn or sub-TTL re-queries (e.g. ticking `now()` in a key — the v0.5.184 class) |

A healthy run shows the miss delta shrinking as the fixed-window reads warm.
A miss delta that *stays high* under steady load points at a cache-key bug.

### Overall PASS/FAIL for a target rps

A run **passes** at rate R if, sustained through the soak:

1. `spans_drop == 0` AND `spans_wfail == 0` (and same for logs/metrics), and
   health never goes `overloaded`/503;
2. `ch_rss_mb` plateaus below the CH memory limit (no OOMKill);
3. hot-endpoint p99 within budget **warm** (50 ms for services/problems,
   200 ms for metric/traces/logs), heatmap < 3 s;
4. cache miss-delta trending down, not up.

The deliverable of a session is the **highest R that passes all four** — that's
the ingest+read ceiling for the cluster's current CH sizing. Re-run after a CH
memory bump to confirm the ceiling moved.

---

## 6. Notes / gotchas

- **Auth.** Read endpoints need a JWT; `read-load.sh` and `watch.sh` log in
  with the bootstrap admin (`admin@coremetry.local` / `admin`, overridable via
  `COREMETRY_INITIAL_ADMIN` / `COREMETRY_INITIAL_PASSWORD` at boot →
  `COREMETRY_EMAIL` / `COREMETRY_PASSWORD` for the scripts). `/api/health` is
  public, so `watch.sh --no-admin` works with zero creds (health-only).
- **`from`/`to` are unix-nanosecond ints**, not relative tokens — `parseTime`
  in `internal/api/api.go` parses only int64 ns. The scripts compute the ns
  window for you.
- **OTLP exporter restart caveat** — if you bounce the Coremetry pod
  mid-test, also restart the collector (`coremetry-otelcol`): its gRPC
  exporter wedges on zero-addresses and memory_limiter 503s the demo. (Known
  local-deploy gotcha.)
- **Don't tune `async_insert`** to chase a number — it's a hard constraint;
  the harness measures the path, it doesn't reconfigure it.
