# /spec — Automatic anomaly → root-cause hypothesis

## What

When an anomaly (or critical Problem) fires, Coremetry should **auto-assemble,
rank, and persist a root-cause hypothesis** — "Hypothesis #1: payment-svc deploy
4m ago (78%); #2: downstream Oracle dep degraded (15%); #3: noisy-neighbour CPU
(7%)" — and surface it **in-page on /anomalies and /problems with no operator
click**. This is the Datadog/Dynatrace differentiator
([[project-correlation-differentiator]]).

**The gap is narrow.** The deterministic machinery already exists and is
reused, not rebuilt:
- `internal/anomaly/fusion.go` — `buildEvidenceBundle()` (PURE, store-free):
  co-firing problems + matching deploy + ranked neighbours + a confidence score.
- `internal/correlator/propagation.go` — `RankRootCausesFromEdges()`: scores
  downstream causes by error-share, hop-decayed (0.5^hops), 2-hop capped.
- `internal/api/rootcause.go` — `getProblemRootCause()`: the 5-way parallel
  soft-fail fan-out (correlated changes, blast radius, exemplar, bubble-up,
  recent deploy), cached, bounded.
- `internal/anomaly/problem_explainer.go` — already narrates critical Problems
  via `copilot.Explain` at explain-time.

What's MISSING for *automatic*:
1. **Anchor on an anomaly**, not only a rule-Problem (the fan-out takes a
   Problem; an `AnomalyEvent` carries `{Service, StartedAt, LastSeen}` — enough).
2. **Fuse into a single ranked hypothesis** (deploy vs propagation-ranked
   neighbours vs co-firing) — today these are returned as separate un-ranked
   fields the operator pieces together mentally.
3. **At fire-time + persisted** — today it is built on-demand (a click) or only
   for critical Problems after a 30s tick; nothing is stored to show in-page.
4. **An in-page surface** — a ribbon under the anomaly/problem, not a click.

## Files (shipping order)

Backend:
- `internal/chstore/rootcause_hypothesis.go` (new, +~90) — `RootCauseHypothesis
  {AnchorKind, AnchorID, Service, ComputedAt, TopSuspect, TopScore, Confidence,
  Candidates []ScoredCause, RecentDeploy}` + a `root_cause_hypotheses`
  ReplacingMergeTree(version), ORDER BY (anchor_kind, anchor_id), 30d TTL.
  `UpsertHypothesis` / `GetHypothesis` (FINAL read). (Mirrors `anomaly_event.go`.)
- `internal/correlator/hypothesis.go` (new, +~80) — `Synthesize(anchor,
  inputs)` PURE fuser: takes the existing `EvidenceBundle` + the
  `RankRootCausesFromEdges` output + the recent deploy, emits ranked
  `[]ScoredCause` with confidence weights (deploy strongest, then propagation
  score, then co-firing). Table-driven unit-tested (no I/O).
- `internal/anomaly/` worker (+~40) — extend the existing leader-gated
  `problem_explainer` tick (or a sibling tick): for newly-open anomalies/
  high-sev problems, `gatherEvidenceInputs()` once → `Synthesize()` per anchor →
  `UpsertHypothesis()`. Batched + bounded exactly like the explainer.
- `internal/api/rootcause.go` (+~40) — `getAnomalyRootCause(id)` anchored on an
  `AnomalyEvent` (reuse the `getProblemRootCause` fan-out with the anomaly
  window); `serveCached` 60s; viewer auth; no audit. The `/anomalies` +
  `/problems` list handlers join the persisted top-suspect summary so the ribbon
  renders without a per-row fetch.
- (optional, last) route the hypothesis through `s.copilotExplain(...)` for a
  prose narration on top of the deterministic ranking (the /ai-attribution
  wrapper, never `copilot.Explain` direct).

Frontend:
- `frontend/src/lib/types.ts` (+~12) — `RootCauseHypothesis` + `ScoredCause`.
- `frontend/src/lib/api.ts` (+~3) — `anomalyRootCause(id)`.
- `frontend/src/components/RootCauseRibbon.tsx` (new, ~90) — "Root cause:
  <top-suspect> (NN%) ▸" chip; expands to the ranked candidates + the evidence
  (deploy / co-firing / blast-radius / exemplar ◆). Reuses `<Button>`, the
  `.badge` chips, `<Spinner/>`/`<Empty/>`.
- `frontend/src/pages/Anomalies*` + `RootCausePanel.tsx` (+~20) — mount the
  ribbon on each anomaly/problem; low/no-evidence shows an honest "no clear
  cause — N signals" state.

Gates: `go build ./...` + `go test ./internal/...` (the pure Synthesize +
hypothesis table) + `tsc` + `make audit`.

## API surface

- `GET /api/anomalies/{id}/rootcause` → `RootCauseHypothesis` (cached 60s, anchor
  on the AnomalyEvent window; viewer; no audit).
- `/api/anomalies` + `/api/problems` list rows gain a `topSuspect` + `confidence`
  summary (from the persisted hypothesis) for the in-page ribbon.

## Schema changes

- New `root_cause_hypotheses` ReplacingMergeTree(version), 30d TTL, keyed on
  (anchor_kind, anchor_id). Computed state (not user-saved) — a new MV-style
  table like `anomaly_events`, NOT `saved_views` (invariant #5 is for *user*
  state). Additive `chmigrate` CREATE IF NOT EXISTS.

## UX surface

- /anomalies + /problems: each row auto-shows a "Root cause: <suspect> (NN%)"
  ribbon, computed by the worker (no click). Expand → ranked candidates with the
  reason per candidate ("downstream Oracle dep, error-share 0.62, 1 hop") + the
  recent deploy + the exemplar ◆ click-to-trace.
- Loading/empty/error: shared `<Spinner/>`/`<Empty/>`; honest low-confidence
  state. Viewer SEES the hypothesis (read-only).

## Risk

Medium. The ranking *weights* (deploy vs propagation vs co-firing) need tuning —
start from the existing `fusion.go` confidence model + propagation scores and
iterate; ship the deterministic ranking first, Copilot prose last. Worker load
is bounded (batch + leader-gated like `problem_explainer`; reuses the already-
cached/bounded `rootcause` reads — no new unbounded queries). The hypothesis is
ADVISORY (a ranked guess) — the UI must frame it as a hypothesis, not a verdict.

## Estimate

~yarım gün total, shipped in 3–4 releases:
1. `getAnomalyRootCause` (anomaly-anchored fan-out, reuse) — ~1 saat.
2. `Synthesize` ranking + `root_cause_hypotheses` persist + worker — ~2 saat.
3. Frontend `RootCauseRibbon` + list summary — ~1 saat.
4. (optional) Copilot narration via `s.copilotExplain` — ~30 dk.

## Open questions

1. **Anchor scope:** synthesize for ALL anomalies, or only high/critical
   severity (bounded worker load, like the explainer's critical-only batch)?
   *Recommend high-severity first.*
2. **Fire-time vs background tick:** synthesize synchronously on detector fire
   (adds latency to the fire path) vs a 10–30s leader-gated tick (decoupled).
   *Recommend the background tick (reuse the problem_explainer cadence).*
3. **Ranking weights:** is "recent deploy ≫ propagation-ranked anomalous
   neighbour ≫ co-firing problem" the right priority, or fold straight into the
   existing `fusion.go` confidence model? *Recommend deploy-weighted, iterate.*
4. **Copilot prose:** deterministic ranked hypothesis ALWAYS + optional Copilot
   narration on top, or deterministic-only for v1? *Recommend deterministic v1,
   Copilot as release #4.*
5. **Storage:** new `root_cause_hypotheses` table (recommended) vs a JSON column
   on `problems`/`anomaly_events`. *Recommend the new table — clean + queryable.*
