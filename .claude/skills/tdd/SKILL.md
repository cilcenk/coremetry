---
name: tdd
description: Test-first feature loop — write the failing test BEFORE the implementation, iterate to green, then release. Use when building a new backend feature/endpoint/pure helper, or when the operator says "tdd ile" / "test-first". Extends the bug-fix regression-test gate (v0.5.447) to FEATURES.
---

# /tdd — test-first feature loop

The bug-fix flow already enforces "every fix ships with the test
that would catch the regression" (v0.5.447). This skill runs the
same discipline FORWARD: for a new feature, the test exists and
FAILS before the implementation does. Horizon seed #3
(2026-05-28); drafted v0.8.277.

## When to use

- New backend endpoint, chstore method, evaluator rule, or any
  pure helper (SQL builders, formatters, URL codecs, filters).
- Frontend pure logic (lib/*.ts — compilers, codecs, segmenters).
- NOT for: UI layout/JSX work (no meaningful unit seam — gates are
  tsc + lint + manual check), one-line config changes, docs.

## The loop

### 1. Extract the seam FIRST

Before writing anything, name the pure function the feature pivots
on. If the design doesn't have one, reshape it until it does —
this is the same move the bug-fix flow uses ("if the fix lives
behind ClickHouse / network I/O, extract the pure SQL-building
helper"). The seam is what makes the test cheap and the feature
reviewable.

Coremetry seam examples: `snapshotLogsJSON` (share capture),
`buildFieldStatsBody` (ES cost guards), `compileSearch` /
`extractHighlightTerms` (log filters), `snapAnomalyWindow` (key
cardinality), `filterHiddenTopologyEdges`, `serviceGraphToMap`,
`withProblemParam`.

### 2. Write the failing test

- Go: table-driven, `<package>/<feature>_test.go`, comment header
  cites the upcoming vX.Y.Z + WHAT CONTRACT the feature promises.
- TS: vitest file next to the module (`lib/foo.test.ts`).
- Include the hostile cases up front: empty input, unit variants
  (the v0.6.36 lesson — exercise EVERY unit a template accepts),
  boundary/overflow, garbage tolerance for parsers.
- RUN IT. It must FAIL (compile error counts). A test that passes
  before the implementation exists is testing nothing —
  fix the test, not your morale.

### 3. Implement to green — smallest honest step

Write the implementation the test demands, no more. Re-run:
`go test ./internal/<pkg>/ -run <TestName>` or
`npm run test -- --run <file>`. Iterate until green. Don't touch
the test to make it pass unless the CONTRACT was wrong — and say
so out loud when that happens.

### 4. Wire + full gates

Only after green: wire the seam into the handler/page (route
registration in api.go per the backend rules, serveCached +
hash-all-inputs key, auth/audit if it writes). Then the standard
release gates: `npx tsc --noEmit` / `go build ./...` /
`go test ./...` / `make audit`.

### 5. Release

Normal /release flow — one logical unit per vX.Y.Z. The commit
body names the seam + its test file, same as bug-fix commits do.

## Anti-patterns

- **Test-after theatre.** Writing the impl, then a test that
  mirrors it line-for-line. The test must encode the CONTRACT
  (inputs → promised outputs), not the implementation's shape.
- **Skipping the red run.** If you never saw it fail, you don't
  know it can.
- **Mock forests.** If the test needs three mocks, the seam is in
  the wrong place — extract a purer function instead.
- **UI TDD.** Don't force this loop onto JSX; the operator's
  manual check + tsc is the honest gate there.
