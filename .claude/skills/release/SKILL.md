---
name: release
description: Cut a Coremetry release — compute next v0.5.X tag, commit, push, kick off docker-up in background. Use when the user says "release", "ship", or finishes a coherent change and wants it shipped. Gates on `go build ./...` (backend) and `cd frontend && npx tsc --noEmit` (frontend) per CLAUDE.md.
---

# /release — ship a Coremetry change

Use this skill to turn the current working-tree state into a shipped
Coremetry release. Follows the conventions in `CLAUDE.md` —
small frequent commits, monotonic v0.5.X tags, push, background
rebuild.

## Args

The user invokes as `/release [short title]`. The argument (if any)
becomes the **title** of the commit and tag. If omitted, ask the user
for one — never invent a title from `git diff` alone, the commit
title should reflect intent, not a description of the diff.

## Steps

### 1. Verify there's something to release

Run in parallel:
```
git status --short
git diff --stat
```

If both are empty: tell the user there's nothing to commit and stop.

If there are untracked files that look like junk (build artifacts,
.DS_Store, editor swap files), warn but don't auto-clean — the
operator may want them.

### 2. Determine the next version

```
git tag --sort=-v:refname | grep -E '^v0\.5\.[0-9]+$' | head -1
```

Increment the patch component by 1. Example: previous tag `v0.5.188`
→ next is `v0.5.189`. The series is monotonic; never re-use a tag.

### 3. Type-check + build — the gate

Per CLAUDE.md "When you ship a new feature" steps 9 + 10:
TypeScript is law, `go build` is law. Gate the commit on a clean
type-check / build. Run in parallel based on which file types
changed:
- If any `frontend/**/*.tsx` or `frontend/**/*.ts` changed:
  `cd frontend && npx tsc --noEmit`
- If any `*.go` changed: `go build ./...`

If either fails, surface the error and stop — don't try to fix it
silently. The operator decides whether to fix-forward or abort.

### 3b. `go test ./...` — regression suite gate (v0.5.447)

Run after the build gate. The regression-test discipline
(CLAUDE.md "When you ship" item 11) ships a test per
`v0.5.X — bug-fix` release, so the suite grows over time and
catches recurrence of historical bug classes.

- Exit non-zero: STOP. Surface the failing test name to the
  operator. Don't tag — a red test on a release branch is the
  signal the user asked us to install.
- Skip safely on tags that don't touch Go (`docs/` only,
  `frontend/` only) — but err on the side of running.

### 3a. `make audit` — hard-constraint linter (v0.5.446)

Run `make audit` after the type-check/build gate but before
staging. It greps for the regression patterns from CLAUDE.md's
"Hard constraints" and "Performance pitfalls" sections (cache-key
length anti-pattern, eager Combobox, setInterval without
document.hidden, direct s.copilot.Explain, non-GLOBAL IN over
Distributed, FROM spans without nearby LIMIT).

- Exit 1 (🔴 critical findings present): STOP. Surface findings,
  let the operator decide fix-forward or abort. Don't tag.
- Exit 0 with warnings (🟡 only): print the warning list, ask
  the operator if any are unexpected. If all are known false
  positives (e.g. Profiling.tsx pprof code-sample template),
  proceed.

### 4. Stage the changes

Use `git add` with **specific paths** (never `-A` / `.`) to avoid
accidentally staging secrets, .env files, large binaries, or
unrelated working-tree noise. List the staged files back to the
user via `git diff --cached --stat`.

### 5. Compose the commit message

Format per CLAUDE.md exactly, using a HEREDOC so newlines preserve:

```
v0.5.X — <short title, max 70 chars>

<body — what changed and why; wrap at 72 chars. If this is a
bug fix, start the body with "Operator-reported: <one-line
description of the original report>". Include the root cause
when it's non-obvious. Keep the body tight — 3-12 lines is
typical for v0.5.X commits.>

Co-Authored-By: Claude Opus 4.7 <noreply@anthropic.com>
```

The body should be derived from the actual diff content + the
conversation context (what motivated the change). Don't pad it.

### 6. Commit, tag, push

Run these sequentially (each depends on the last):

```
git commit -m "$(cat <<'EOF'
... message from step 5 ...
EOF
)"
git tag -a v0.5.X -m "v0.5.X — <short title>"
git push origin main
git push origin v0.5.X
```

Tag annotation message can be just the title — the full body lives
on the commit.

### 7. Kick off the rebuild

Start `make docker-up` in the **background** so the user can keep
working while the image rebuilds:

```
make docker-up
```

Use `run_in_background: true` on the Bash call. The runtime will
notify when it completes (~30-60s). Do NOT wait inline — that
blocks the conversation.

If a previous `make docker-up` is still running (the user can see
this in `docker ps` if needed), tell them rather than starting a
second one — the rebuild lock will fight itself and produce a
broken image. CLAUDE.md is explicit: one rebuild at a time.

### 8. Confirm

One concise line: which version was tagged, which commit hash, and
that rebuild is running. No multi-paragraph summary — the diff is
visible in the commit, the operator knows what they shipped.

Example confirmation:
> v0.5.189 commit/push tamam, rebuild arkada. <release title>

## Common pitfalls

- **Don't amend.** If a pre-commit hook fails, fix the underlying
  issue and create a NEW commit (the failed one didn't exist).
  Amending past tags risks losing the previous push.
- **Don't push --force.** Ever. Even on a tag.
- **Don't skip the type-check / build.** A red build that ships
  breaks the rebuild cycle and the operator has to revert.
- **Don't re-use a tag.** If `v0.5.X` exists, the next is X+1, even
  if X was a no-op or got reverted.
- **Don't combine unrelated changes.** If the working tree has two
  logical units of work, do two separate releases — the small
  commit cadence is the workflow.
- **Bug fix commits go IMMEDIATELY.** Don't batch a bug fix into a
  feature commit — ship the bug fix as its own v0.5.X+1 right after
  the prior release.
- **Don't `make docker-up` while another rebuild is in flight.**
  BuildKit serialises layer cache; concurrent rebuilds fight and
  one of the resulting images will be broken.
