---
name: where-is
description: Find where a concept, feature, or symbol is implemented in the Coremetry codebase — without burning the main session's context window on grep+read cycles. Returns file:line pointers + a one-line summary per hit. Use when the user asks "where is X" / "X kodu nerede" / "X nasıl çalışıyor" and the location isn't obvious.
---

# /where-is — codebase concept lookup

Most of Coremetry's surfaces follow naming conventions
(chstore.GetX, copilotExplainX, FooPanel, /api/foo/{id}) but
"the SLO burn-rate evaluator" or "the trace sampling decision"
isn't a function name — it's a *concept* that takes 3-5 grep
iterations to track down. Each iteration reads files into the
main session's context window. By call 5 the agent is bloated
and slower.

This skill delegates the search to an `Explore` subagent that
returns ONLY file:line pointers + a one-line summary per hit.
Main session's context stays clean.

## When to use

- The user asks a "where is X" question.
- Investigating a bug + the entry point isn't obvious from
  the error message.
- Designing a `/spec` and the spec needs "like the existing
  Y" reference — find Y fast.

**Don't use** for:
- Symbols the user already named (just grep — one call).
- File paths the user already provided (just Read).
- "How does X work" deep-dives — that's a research session,
  not a lookup.

## Args

`/where-is <concept>` — natural language, 2-10 words.

Examples:
- `/where-is the SLO burn rate evaluator`
- `/where-is trace sampling decision logic`
- `/where-is the alert noisy-rule evaluator`
- `/where-is the topology aggregator goroutine`
- `/where-is the Drain templater`
- `/where-is system_settings hydration on boot`

## Steps

### 1. Dispatch the search to an Explore subagent

Use the Agent tool with `subagent_type: "Explore"`. The
prompt should be:

```
You are looking for: "<concept verbatim>"

Coremetry codebase. The "What goes WHERE" map in CLAUDE.md
covers the canonical paths; common search surfaces:

  Backend (Go):
    - internal/otlp/         OTLP ingest
    - internal/chstore/      CH writes + queries
    - internal/logstore/     ES/CH-agnostic log reads
    - internal/tempo/        external trace fallback
    - internal/api/          HTTP handlers + cache wrapper
    - internal/evaluator/    alert evaluator
    - internal/anomaly/      curated + recorder
    - internal/templater/    Drain
    - internal/notify/       notification fan-out
    - internal/auth/, internal/ldap/   auth
    - internal/copilot/      AI + system prompts
    - internal/sampling/     sampling
    - internal/topology/     topology batch correlator
    - internal/chmigrate/    schema migrations

  Frontend:
    - frontend/src/pages/        page roots
    - frontend/src/components/   panels + shared widgets
    - frontend/src/lib/types.ts  shared types
    - frontend/src/lib/api.ts    client methods
    - frontend/src/App.tsx       route registry
    - frontend/src/components/Sidebar.tsx  nav registry

  Skills:
    - .claude/skills/**.md   slash command definitions

For each hit:
  - file:line (markdown link form: [filename](path#L42))
  - one short sentence on what's there

Cap output at 7 hits. If there are clearly more, mention
"+N more" at the end. Stay terse — no preamble, no
explanation of methodology. The caller wants pointers, not
narrative.

Search breadth: very thorough. The caller will burn time
on missing matches.
```

Specifically pass `"very thorough"` as the breadth hint in
the Agent's `description` parameter.

### 2. Format the response

The subagent returns a list of file:line + sentence pairs.
Pass them through unchanged — don't re-summarise (the
subagent already did). Add at the top:

```
Found N hit(s) for "<concept>":
```

If the subagent returns zero hits: try once more with a
broader phrasing (e.g. drop "the" / "logic" / "evaluator")
before reporting "no obvious match — concept may be
described differently in the code".

### 3. Don't load the files yourself

The skill is a lookup, not a deep-read. The operator (or
the next agent step) decides which hit to open. If the
operator asks "open #2" or "explain that one", THEN read
the file — but that's a different call.

## Anti-patterns

- **Don't write code in this call.** Find-and-fix is two
  steps. The /bugfix skill or a normal edit handles step 2.
- **Don't expand the search to "everything that touches
  X".** Stay focused on the concept the operator named.
  Broad "every file mentioning auth" surveys are
  /scale-audit territory.
- **Don't return file paths without line numbers.** A bare
  `internal/api/api.go` is useless at 8000+ lines — the
  point of the skill is the file:line pinpoint.
- **Don't bypass the subagent.** Inline grep + read works
  but burns main-session context. The whole reason the
  skill exists is that delegation. If you find yourself
  doing the lookup manually, you're using the wrong tool.
