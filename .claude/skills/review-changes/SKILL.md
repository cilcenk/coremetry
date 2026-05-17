---
name: review-changes
description: Pre-commit review of the working-tree diff. Catches dead code, debug log lines, commented-out blocks, CLAUDE.md convention violations, naming drift, missing comments on non-obvious code. Run before invoking /release on a non-trivial change.
---

# /review-changes — second pair of eyes before /release

Coremetry ships fast (small commits, often 5+ per session).
The cost of that cadence is that minor code-quality regressions
slip through — debug `fmt.Println`, commented-out blocks left
"just in case", `<Combobox options={...}>` violations against
the picker convention, inline styles where a CSS variable
would do. This skill catches them BEFORE the commit lands.

## When to use

- After finishing a non-trivial feature commit (3+ files
  touched, or any new schema / API surface).
- Before `/release` on anything that's not a 1-line bug fix.
- When the operator asks "her şey iyi mi" / "is this ready
  to ship" — same intent.

**Don't use** for:
- One-line CSS tweaks (waste of skill cycles)
- Test-only changes (no production risk)
- Pure dependency bumps

## Steps

### 1. Pull the diff

```
git status --short
git diff --stat
git diff
```

If diff is empty: tell the operator there's nothing to review
and stop.

### 2. Scan for the standard issue classes

Run each check on the diff. For each finding, output
`file.ext:line — issue — one-line suggestion`. Stay terse.

**Class A — Debug residue (block ship if found)**
- `fmt.Println` / `fmt.Printf` (Go) — debug print left in
- `console.log` / `console.error` / `console.debug` (TS) — same
- `panic("debug")` / `panic("here")` style temporary panics
- Bare `time.Sleep(...)` in production paths (test-only is OK)
- Hard-coded localhost URLs / API keys / test credentials

**Class B — Commented-out blocks (block if >5 lines)**
- Multi-line comment blocks that look like commented-out code
  rather than docs. Single-line `//` explaining WHY is fine;
  10 lines of `// const x = ...` is not.

**Class C — Coremetry convention drift (per CLAUDE.md)**
- `<Combobox options={services|operations|metricNames}>` —
  must use the *Picker components.
- New table over ~100 rows without virtualization /
  pagination / `content-visibility: auto`.
- `s.copilot.Explain(r.Context(), …)` — must go through
  `s.copilotExplain(r, …)` so /ai surface attribution works.
- New `fmt.Sprintf("...:exN=%d", len(set))` style cache key
  using length-of-set as a discriminator (v0.5.187 incident
  — use a sorted+FNV digest instead).
- New admin mutation route without an `s.audit(r, ...)` call.
- New endpoint without `auth.RequireRole` / `RequireAnyRole`
  when it should have one (mutating? admin-only? gated).

**Class D — Naming / structure drift**
- Functions/types named ad-hoc rather than following the
  neighbouring file's convention (Get/List/Compute/Upsert
  in chstore, copilotXxx for AI handlers, *Picker for
  pickers).
- Unused exports, unused imports (the compiler catches some,
  but unused fields and types slip).
- New top-level state in a component where useState would do.

**Class E — Comment hygiene**
- Multi-line block comments that explain WHAT the code does
  rather than WHY (CLAUDE.md's defaults: don't comment WHAT,
  only WHY and only when non-obvious).
- New comments referencing "current bug" / "current task" /
  "issue #X" — those belong in the commit message, not the
  code (they rot fast).
- Missing comment on a counter-intuitive line (e.g. why a
  cache key includes a length, why a fetch is debounced at
  exactly 200ms — non-obvious choices should have a one-
  liner).

**Class F — Risk on dangerous paths**
- Goroutines launched without timeout / cancellation —
  fire-and-forget leaks. Should use bounded context.
- `for { ... }` without a clear exit condition.
- DB queries without `SETTINGS max_execution_time = N` on
  any heavy GROUP BY against `spans` / `metric_points`.
- Cache TTL of 0 / unbounded cache without LRU.
- New panic on a user-driven path. Errors should be
  returned, not paniced.

### 3. Produce the report

Use this format:

```
## Diff review — N files, +X / -Y lines

### 🚫 Blockers (fix before /release)
- file.go:42 — fmt.Println debug residue — remove
- file.tsx:88 — Combobox options=services — swap to ServicePicker

### ⚠️ Should fix (recommend)
- file.go:123 — comment explains WHAT (lines 124-130 already say it). Drop.
- file.tsx:200 — new inline color #ff5252 — use var(--err) per CLAUDE.md.

### 💡 Optional polish
- ...

### ✅ Clean
- conventions / naming / typing all in line
```

If there are no findings: report "clean" with one line on the
biggest pattern noticed (e.g. "follows the existing SLOs +
Slos.tsx pattern; OK to ship").

### 4. Don't fix anything yourself in this call

This skill REPORTS. The operator either fixes the issues then
re-runs the review, or invokes `/release` to ship as-is for
the non-blocker items. Mixing review + fix in the same call
loses the audit trail of "what was flagged".

## Anti-patterns

- **Don't lecture.** "You should always X" comments are noise.
  Cite the file:line and suggest the fix; the WHY lives in
  CLAUDE.md.
- **Don't flag style choices that match the file's existing
  style.** If the file uses inline styles throughout, don't
  flag one more inline style — that's a refactor, not a
  diff issue.
- **Don't fabricate issues.** If the diff is genuinely clean,
  say so. False positives erode trust in future runs.
- **Don't expand to architecture review.** "You should
  refactor X to Y" is `/scale-audit` territory, not
  per-commit review.
- **Don't surface CLAUDE.md violations that were ALREADY
  in the file before this diff.** Only flag what THIS diff
  introduces / touches.
