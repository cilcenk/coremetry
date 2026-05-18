---
name: kuyruk
description: Show the prioritised work queue. Use whenever the user types "kuyruk" (Turkish for "queue") — they want a scannable list of pending / in-progress items and a "Hangisi?" prompt to pick from.
---

# /kuyruk — prioritised work queue

`kuyruk` is Cenk's standing pattern for "what's next" — see
`CLAUDE.md` under "Workflow / Daily". Goal of this skill: present
the current state of work in a form that takes <5 seconds to
scan, followed by "Hangisi?" so the operator picks rather than
reading more analysis.

## Steps

### 1. Gather signal in parallel

Pull from three places:

- **Active todo list** — `in_progress` and `pending` items (TodoWrite
  state). These reflect the agent's current session focus.
- **Memory** — any project memories tagged "deferred", "parked",
  or "next sprint". Look for `[[name]]` references suggesting
  outstanding decisions.
- **Recent git log** — `git log --oneline -8` to anchor "what just
  shipped" so the queue feels continuous with the prior release.

If running in a session where TodoWrite is empty AND no parked
items in memory, ask the user to seed the queue rather than
making one up.

### 2. Categorise

Group items into 2-4 buckets, in this priority order:

1. **Bugs / regressions** (`bug-fix`, "Operator-reported", "broken
   in prod"). Per CLAUDE.md, these interrupt feature work and
   ship as `v0.5.X+1` immediately — never batched.
2. **Scale / perf** — anything violating the CLAUDE.md "Hard
   constraints" (eager pickers, unbounded tables, missing
   `document.hidden` guards, MV-bypass on hot reads, cache-key
   digests) or breaching a "Performance budget" p99. Findings
   from `/scale-audit` queue here.
3. **Features** — new functionality the operator explicitly asked
   for.
4. **Polish** — consistency sweeps, refactors, low-impact UX nits.

Drop "ideas I had once" — only items the user explicitly
mentioned or that came out of agent investigation. The queue is
operator-owned, not agent-imagined.

### 3. Estimate effort

Annotate each with a rough time estimate from this scale:
- `~10 dk` — single CSS tweak, single file edit
- `~30 dk` — single endpoint, single new component
- `~1 saat` — feature with backend + frontend + types
- `~2 saat` — refactor, multi-file change, new schema column
- `~yarım gün` — bigger refactor; flag this — usually wants
  splitting up via `/spec` first

Without a real estimate, omit rather than guess. Operator can ask
if they care.

### 4. Render

Numbered list, max 7 items. Format:

```
| # | İş | Tahmin | Kategori |
|---|----|--------|----------|
| 1 | OperationPicker scale fix | ~30 dk | scale |
| 2 | Status page email resend | ~30 dk | feature |
| ... |
```

OR for very short queues (≤3), inline numbered:

```
1. OperationPicker scale fix — ~30 dk
2. Status page email resend — ~30 dk
3. AI rate admin Settings tab — ~45 dk
```

End with: `Hangisi?` (literally — operator's expected affordance).

### 5. Don't over-explain

No introductory paragraph. No "here's the queue you've been
building". No closing recommendation. The queue itself + the
"Hangisi?" prompt is the whole response.

This is feedback-load behaviour from this user — long explanations
get cut. See [[feedback-terse-responses]] in memory.

## Anti-patterns

- **Don't add items the user didn't ask for.** Surfacing "you
  should add tests" or "consider refactoring X" when the user
  didn't bring it up is noise.
- **Don't re-list shipped items.** If v0.5.X just landed, that's
  not "queue" — it's history. Skip it.
- **Don't sort by alphabetical / file path.** Sort by impact:
  bugs > scale > features > polish.
- **Don't include items already in_progress as also pending.**
  An in-progress item gets its own line at the top labelled
  "(in progress)" — never duplicate.
- **Don't conflate work-queue priority with incident triage.**
  The P1/P2/P3 hierarchy in CLAUDE.md is for `Problem` rows on
  the Inbox/Topology — a different surface. Use the
  bugs/scale/features/polish buckets here.
