# Coremetry v0.7.0 — Runbooks + `coremetry-agent`

Status: **approved 2026-05-29** (Cenk). Replaces the workspace **Notebook**
(frontend-only, localStorage, 7 files, zero backend) with operational
**Runbooks** modelled on OneUptime: documented + executable step-by-step
procedures, with every run tracked for audit. Automated steps execute on a
dedicated **`coremetry-agent`** pod so arbitrary code never runs in the
api/worker.

## Data model

```
Runbook (template)        ReplacingMergeTree(version), read FINAL
  id, title, description(md), enabled, labels[], createdBy, steps[], version
Step (embedded JSON)
  id, order, kind, title, instructions(md), expected?, + kind payload
Execution (one run)       ReplacingMergeTree(version), read FINAL
  id, runbookId, titleSnapshot, status, startedBy, startedAt, completedAt,
  problemId?, stepStates[] (snapshot-on-start), version
StepState (per step, snapshotted at start)
  stepId, order, kind, title, instructions, status, by, startedAt, endedAt,
  output(stdout/stderr/returnValue/httpStatus/body), error
```

`status` (execution): `scheduled|running|waiting_for_user|completed|failed|cancelled`
`status` (step): `pending|running|waiting_for_user|completed|skipped|failed`

**snapshot-on-start**: when an execution begins, the runbook's steps are
copied onto the execution. Editing/deleting the template never mutates an
in-flight or historical run — this is the audit-integrity guarantee and
removes the need for a runbook version table.

## Step kinds

| kind | where it runs | payload |
|---|---|---|
| **Manual** | nobody — pauses (`waiting_for_user`) until a responder ticks it | — |
| **Query** | server (Coremetry-native) — runs a CH/Explore query inline | dsl/agg/query |
| **HTTP** | agent | url, method, headers, body, timeoutMs |
| **JavaScript** | agent — goja sandbox (no FS/net) | script |
| **Bash** | agent — os/exec, non-root, timeout | command |

Query is Coremetry's differentiator over OneUptime — a diagnostic step that
pulls the actual telemetry inline ("check error-rate for service X").

## `coremetry-agent` — 5th `COREMETRY_MODE` role

Single-binary preserved: same image, `COREMETRY_MODE=agent` (joins
all|ingest|api|worker from v0.6.0). Separate `coremetry-agent` Deployment;
takes NO operator traffic. Loop: **poll** api → atomically **claim** a queued
step-exec → **execute** (HTTP/JS/Bash) → **post result** → heartbeat. Manual +
Query steps never reach the agent (resolved server-side).

Why an agent and not the worker: operator-authored Bash/JS must run with an
isolated blast radius, not in the api/ingest/worker that hold data + leader
jobs. Mirrors OneUptime's self-hosted Runbook Agent. JS via `goja`
(pure-Go, FS/net denied); Bash via `os/exec` as the non-root agent user with a
hard timeout; HTTP with timeout + retry.

## Queue + agent protocol

Server enqueues an automated step-exec (Redis stream — already present for
leader-lock + SSE). Agent `claim` is atomic (consumer-group / Redis lock),
sends `heartbeat`, posts `result`; server records + advances the execution.
Agent auth = a dedicated **enrollment token** (Secret), not a user JWT;
single-tenant ⇒ one shared token. Endpoints: `/api/agent/{claim,result,heartbeat}`.
Requires Redis (the agent is inherently a distributed-mode concept). Step
state also persists in CH (`runbook_executions.step_states_json`) for audit.

## Storage

Two new `ReplacingMergeTree(version)` tables (read FINAL), NOT `saved_views`
(executions are append-heavy with their own lifecycle):
- `runbooks(id, title, description, steps_json, enabled, labels, created_by, version)` ORDER BY id
- `runbook_executions(id, runbook_id, title_snapshot, status, started_by, started_at, completed_at, problem_id, step_states_json, version)` ORDER BY id

Steps + step-states are JSON blobs (mirrors OneUptime — no per-step rows).
`/clickhouse-schema` gate before the migration; idempotent CREATE/ALTER.

## Audit (3 layers) — "who ran what when, which steps executed"

1. **`audit_events`** (existing admin trail): `s.audit(r, …)` on every mutation —
   `runbook.create|update|delete|enable|disable|execute|cancel`,
   `runbook.step.complete|skip|fail`, `runbook.agent.claim|result`.
2. **Execution record = durable rich audit**: startedBy/at, status, completedAt,
   problemId; per-step kind/title(snapshot)/status/by/timestamps/output/error.
3. **UX**: per-runbook **Executions** tab → frozen read-only runner (step-by-step
   audit); per-runbook **Audit Logs** tab (audit_events filtered to this runbook).
   Read-only to everyone incl. viewer.

## API

- `GET/POST /api/runbooks`, `GET/PUT/DELETE /api/runbooks/{id}` (editor+ writes, serveCached list, audit)
- `POST /api/runbooks/{id}/execute` → starts an execution (optional `problemId`)
- `GET /api/runbooks/executions`, `GET /api/runbooks/executions/{id}`
- `POST /api/runbooks/executions/{id}/steps/{stepId}` (manual tick: complete/skip/fail + note)
- `POST /api/runbooks/executions/{id}/cancel`
- Agent: `POST /api/agent/claim`, `POST /api/agent/result`, `POST /api/agent/heartbeat` (agent-token auth)

## Frontend

- **Runbooks list** `/runbooks` — takes the workspace nav slot (Notebook deleted).
- **Runbook detail** — left-nav Overview / **Steps** / Executions / Audit Logs
  (+ Settings/Delete). Steps editor: Manual/Query/HTTP/JavaScript/Bash kind cards +
  "Start your runbook" empty state + drag-reorder (matches OneUptime screenshot).
- **Execution runner** — step-by-step rendered markdown + live status; automated
  steps show `running`→output inline (SSE); manual steps wait for tick.
- **Problem → Runbooks panel** — `/problems/:id`: `RunbookPicker` (server-side) +
  "Run runbook" + attached executions.
- types in `lib/types.ts`, client in `lib/api.ts`, shared `<Spinner/>`/`<Empty/>`.

## Helm

`agent.enabled` (default off) → `coremetry-agent` Deployment (distributed mode);
agent token from a Secret; restricted-v2 SCC preserved (Bash runs non-root).

## Release plan → v0.7.0 (incremental)

1. **v0.6.72** — CH schema + Go model + store CRUD + `/api/runbooks` CRUD + types/client.
2. **v0.6.73** — execution lifecycle server-side (Manual + Query + HTTP), runner API, audit.
3. **v0.6.74** — `COREMETRY_MODE=agent` role + Redis claim queue + JS/Bash execution + agent protocol.
4. **v0.6.75** — frontend: Runbooks list + detail (Steps/Executions/Audit) + runner.
5. **v0.6.76** — Helm agent Deployment + Problem→Runbooks panel + remove Notebook.
6. **v0.7.0** — milestone tag.

## Risks

- Agent code-exec isolation (goja sandbox + os/exec in a restricted, non-root,
  operator-owned pod; single-tenant; editor+ authored). api never executes code.
- Redis claim atomicity (consumer-group / lock) — proven pattern (already used
  for leader-lock + SSE bridge).
