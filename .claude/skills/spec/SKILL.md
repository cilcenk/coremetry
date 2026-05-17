---
name: spec
description: Turn a vague feature idea into a one-page implementation spec before writing any code. Use when the user describes WHAT they want but the HOW is still unclear — files to touch, API surface, schema changes, UX surface, effort estimate. The user approves the spec, THEN coding starts.
---

# /spec — idea → implementation plan, then ship

The default Coremetry workflow is small commits + frequent
releases. The biggest cost in that workflow is starting on the
wrong implementation — picking the wrong file structure, the
wrong API shape, the wrong UX surface, and burning 30 min
before pivoting. This skill removes that cost by surfacing a
1-minute spec for explicit approval BEFORE the edit phase.

## When to use

- The user describes a feature in 1-2 sentences and the
  obvious implementation has 3+ files involved.
- The user says "let's add X" and X looks like a new admin
  surface, a new endpoint, a new schema column, or a new
  AI surface.
- The work is large enough that a wrong start costs >15 min
  of rework.

**Don't use** for:
- One-line CSS tweaks
- Bug fixes where the root cause is already named
- Single-file edits to existing code

For those, just do them.

## Args

`/spec <one-line description>` — the user's intent verbatim.

## Output shape

Produce a markdown spec with these sections, in this order:

```
## What
1-3 sentences restating the user's intent in concrete terms.
Catches misunderstanding early.

## Files
- path/to/file.go (+N lines, what gets added)
- frontend/src/foo.tsx (new file, ~X lines)
- ...
Order: backend schema → backend handler → backend route →
frontend type → frontend client → frontend component → frontend
wiring. Same order /copilot-surface uses.

## API surface
- `GET/POST/PUT /api/<path>` — body shape (1 line)
- ...
Mention auth gate: admin / editor / viewer / public.

## Schema changes
- CH table additions / ALTERs
- system_settings keys
- Or "none" if read-only.

## UX surface
- Where it shows up (which page, which panel, what affordance)
- One-line interaction sketch ("operator clicks foo → modal opens → enters bar → submit")

## Risk
Low / Medium / High + one sentence on the dominant risk.

## Estimate
~10 dk | ~30 dk | ~1 saat | ~2 saat | ~yarım gün
Use one of these brackets; finer estimates are noise.

## Open questions
Bullet list of decisions the operator should make BEFORE coding.
Empty if everything's obvious.
```

## Steps

1. **Re-state the intent in your own words.** If you can't,
   you don't have enough to spec — ask the operator for
   clarification first.

2. **Sniff the codebase.** Read the most-similar existing
   feature to anchor the spec on real conventions. The
   "Files" section should reference patterns that already
   work in this repo.
   - "Like the Sampling tab in Settings.tsx"
   - "Like the OperationPicker pattern"
   - "Like the saved_views table with page='X'"
   When in doubt, dispatch an `Explore` subagent for the
   pattern search rather than burning main-session context.

3. **Identify open questions.** Common ones:
   - Auth gate: admin vs editor vs viewer
   - Storage: existing table vs new
   - Visibility: per-user vs team-shared
   - Empty / error UX: spinner vs blank vs CTA
   List them. The operator decides up front rather than at
   minute 25 when the question becomes a pivot.

4. **Present the spec + WAIT FOR APPROVAL.**
   - End with a one-line "Spec'i onaylıyor musun? Açık soru
     varsa o ilkönce."
   - Do NOT start editing.

5. **After approval, switch to implementation mode.**
   - Use `/release` to ship at the end.
   - If during implementation the spec needs to change (a
     file count was off, an unforeseen dep surfaced), tell
     the operator — don't silently expand scope.

## Anti-patterns

- **Don't over-spec.** A spec should fit in a chat message.
  Two pages of design doc is wrong format — that's a /sketch
  not a /spec.
- **Don't include the IMPLEMENTATION of the code in the
  spec.** The spec is the WHAT and WHERE; code samples
  belong in the actual edit.
- **Don't invent files that don't exist.** "Add to
  ServiceDeployPanel.tsx" only works if that file exists.
  Either reference real files OR mark them "(new file)".
- **Don't skip the open-questions section.** Even one open
  question saved is worth the line. The empty list is
  rare; if it's empty, mention that.
- **Don't drift into the next feature.** Stay focused on
  the user's single ask. If they want "X and also Y",
  spec X and mention "Y as follow-up".
