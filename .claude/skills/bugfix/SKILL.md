---
name: bugfix
description: Investigate a production bug report → root cause → fix → ship as v0.5.X+1. Use when the user reports a defect observed in prod ("X is broken", "operator-reported: Y", "neden Z oluyor"). Treats the bug as the new priority — parks any in-flight feature work first.
---

# /bugfix — production bug → fix → release

Bug reports interrupt feature work. The workflow per `CLAUDE.md`:

> Bug reports: investigate root cause, write the fix as v0.5.X+1
> *immediately* after the prior release rather than batching.

This skill enforces that discipline: investigate first, fix once
the root cause is named, ship a tight commit, then resume what
was going.

## Args

`/bugfix [short description]` — the operator's one-line bug
description. If omitted, summarise the user's most recent message
that triggered the skill.

## Steps

### 0. Re-read past-mistake ledger (MANDATORY — every invocation)

**Operator directive (v0.6.37): I keep repeating the same class of
mistake unless I re-read the lesson list at the start of each
bugfix.** This step is non-skippable.

Before any investigation, open and read in full:

1. `/Users/cenk/.claude/projects/-Users-cenk-Documents-gotrace/memory/MEMORY.md`
   — the index of feedback memories.
2. Every `feedback-*.md` file referenced in that index. The
   feedback memories encode patterns of past failure I am known to
   repeat: bug-repro discipline, unit-mixing in templates,
   audit/verify context, terse-response preference, etc.
3. The "Performance pitfalls — historical incidents" section of
   the repo `CLAUDE.md` — these are the codebase-specific
   re-occurrence patterns (cache key digests, render-time
   recomputation, document.hidden, ES query_string flags,
   significant_text background, TTL unit-mixing, etc.).

Then, for THIS specific bug report, identify the 1-3 lessons that
most plausibly apply. Write them down in one or two lines BEFORE
opening any source file. Example:

> Re-read ledger. Most-relevant lessons for this report:
> - [[feedback-bug-repro-discipline]] — reproduce against CH
>   ground truth first; data window shifts.
> - [[feedback-unit-mixing-needs-both-branches]] — if the bug
>   touches a value+unit template, exercise EVERY unit at ship
>   time, not just the obvious one.

This step costs ~30 seconds and prevents the v0.6.36→v0.6.37
class of regression where the second iteration ships the same
sub-genus of mistake the first iteration just made.

### 1. Park current work (if any)

If a feature commit is in progress (uncommitted working tree, or
a multi-step task active in TodoWrite):

- Note what's parked in 1 line: "Parking <task> mid-stream"
- Don't commit half-baked work; the working tree is preserved as
  is, we'll just write a different file for the bug fix or stash
  if the bug fix touches the same area.

### 2. Reproduce / investigate

This is the slow step. Don't skip it.

- Read the operator's report verbatim. Don't paraphrase or
  reinterpret what they said.
- If the report names a page / surface, open that file first.
- If the report names a behaviour ("spinner never stops",
  "label cut off"), grep for the relevant component.
- For UI bugs: check CSS (`globals.css`) AND the React component.
  Some "UI bugs" are CSS-only, some are state-management.
- For backend bugs: read the relevant handler + the chstore
  method it calls. Cache key bugs hide in `serveCached(...)`
  signatures — see v0.5.187 for the precedent.

If after 5 minutes of investigation there's no clear lead, ASK
the operator for repro steps rather than guessing — they have
context you don't.

### 2a. Coremetry's bug-pattern catalogue

Common shapes of past bugs — try these first when the symptom
matches:

- **"Spinner never stops" / "constantly loading"** — almost
  always a `useEffect` dep that's not reference-stable. Often
  `timeRangeToNs(range)` called inline (v0.5.184) producing
  fresh `from`/`to` every render. Fix: memoise on `range`
  identity OR call inside `useEffect` body.
- **"Wrong tenant's data" / "I see service X under service Y"**
  — cache key audit. `len(set)` digests cross-poison
  (v0.5.187). Fix: sorted + FNV digest.
- **"Page is slow" / "this endpoint takes 10s"** — read endpoint
  is aggregating raw `spans` when an MV exists. Check `FROM
  spans GROUP BY ...` patterns; swap to `service_summary_5m`
  etc. per CLAUDE.md invariant #3.
- **"Setting reverts on restart" / "I configured X but lost it"**
  — config service missing `LoadPersisted(ctx, store)` at boot.
  See `internal/tempo/client.go` for the template.
- **"AI usage isn't appearing on /ai"** — Copilot handler is
  calling `s.copilot.Explain` direct instead of routing through
  `s.copilotExplain(r, ...)` wrapper. The wrapper writes the
  `ai_calls` row.
- **"Logs page is empty even though ES has data"** — detector or
  query is hitting `chstore` direct instead of `logstore.Store`.
  When `COREMETRY_LOGS_BACKEND=elasticsearch` the CH-coupled
  path returns 0.
- **"Polling on a backgrounded tab burns my battery"** —
  `setInterval` lacks the `document.hidden` guard (v0.5.248
  pattern). Wrap the callback.
- **"Label clipped" / "text cut off mid-word"** — `table-layout:
  fixed` + `white-space: nowrap` + small fixed width. Fix:
  `min-width` + `max-width` + `ellipsis` + `title` for tooltip.
- **"ES query rejects with unknown field"** — historical
  `query_string` with `case_insensitive: true` (v0.5.231). ES
  8.x rejects. Standard analyzer already case-folds.

### 3. Name the root cause

Before writing any fix, articulate the root cause in one sentence:

> "Root cause: timeRangeToNs(range) was called on every render
> inside an IIFE, producing fresh `from`/`to` timestamps that
> the FacetsPanel useEffect treated as new deps."

If you can't write that sentence, you're not ready to fix.
Investigate more.

### 4. Apply the minimal fix

- Touch only the file(s) directly responsible. A regression fix
  is not a refactor — don't drag in cleanups, don't rename
  variables, don't reorganise imports.
- Add a brief code comment if the fix is non-obvious. Reference
  the version that introduced or surfaced the issue if you can
  trace it: `(v0.5.X — operator-reported: …)`.
- Type-check + build before considering the fix done.

### 4a. Regression test — bug-fix releases ship with one (v0.5.447)

CLAUDE.md "When you ship a new feature" item 11: every
`v0.5.X — bug-fix` release ships with a Go test that fails on
re-regression. This catches future copy-paste-induced
re-occurrences of the SAME class of bug.

- Pattern: extract the minimal pure function the fix touches
  (often already there). Table-driven test in
  `<package>/<feature>_test.go`. Comment header cites this
  release + the original symptom.
- Canonical example: [internal/api/cache_key_test.go](internal/api/cache_key_test.go)
  guards the v0.5.187 cache-key collision.
- If the fix lives behind ClickHouse / network I/O, extract the
  pure SQL-building helper into a testable function rather than
  skipping the test entirely.
- `go test ./...` must pass before tagging. The /release skill
  runs this as step 3b.

### 4b. Runtime verification gate — MANDATORY (v0.6.37)

**A unit test that asserts the rendered SQL / config / template
string is NOT enough.** v0.6.36 shipped a TTL fix with a passing
string-match test; the produced SQL was rejected by CH at boot
because of a type mismatch the string-match couldn't see. Same-day
v0.6.37 had to fix it again.

Before tagging a bug-fix release, **run the artifact the user
will actually run**:

- SQL / DDL changes → execute the produced statement against the
  live CH (`docker exec coremetry-clickhouse clickhouse-client -q
  "<your statement>"`) and confirm exit code 0 + the table now
  shows the expected shape.
- API changes → curl the endpoint with the cookie jar
  (`/tmp/cm.cookies`, login via
  `admin@coremetry.local`/`admin`) and assert the response shape.
- Frontend changes → not optional; load the page in the running
  app. (For pure-CSS edits, take a screenshot.)
- Config / boot-order changes → `make docker-up` + tail
  `docker logs coremetry 2>&1` for the first 10s after boot,
  watching for error lines that mention the changed code path.

If running the artifact is impossible (no live env, no test
fixture), say so explicitly in the release message — don't sneak
through with "tests pass, shipping". The class of bug the unit
test couldn't see is exactly the class that catches us twice.

### 5. Ship via `/release`

Invoke the `release` skill with the bug-fix description. The
commit message body MUST start with `Operator-reported:` and
include the root cause sentence from step 3.

Example body:
```
Operator-reported: spinner under the Explore facets panel never
stopped, gave a false "still loading" impression.

Root cause: timeRangeToNs(range) evaluated inside an IIFE on
every render produced fresh from/to numbers; FacetsPanel
useEffect re-fired every render, the 300ms debounce
cancelled itself before settling, data never landed.

Fix moves range resolution INSIDE FacetsPanel and memoises
on the range object identity (stable across renders).
```

### 6. Resume parked work

After the bug-fix release lands + rebuild kicks off, surface a
one-line "Bug fix shipped — resuming <parked task>" so the
operator knows you're back on the feature path. Don't re-explain
the bug; the release confirmation already covered that.

## Anti-patterns

- **Don't ship a guess.** If you don't have a clear root cause,
  you're hiding the bug, not fixing it.
- **Don't bundle the fix into a feature commit.** The release log
  needs the bug fix as its own row for forensics.
- **Don't add tests without asking.** This repo doesn't have a
  test suite for most of the codebase; adding one for a single
  bug is scope creep. Ship the fix; if the operator wants a test,
  they'll ask.
- **Don't blame past code.** "v0.5.X introduced this" in the body
  is fine; "the previous developer should have…" isn't.
- **Don't over-explain the conversation.** The commit message is
  for future operators reading `git log`. Keep it terse.
