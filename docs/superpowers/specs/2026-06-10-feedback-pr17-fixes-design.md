# PR #17 Feedback Page — Fix & Land Design

Date: 2026-06-10 · Target release: v0.8.106 · Status: design approved

## Goal

Land community PR #17 (user feedback page, @atilsensalduz) with all
review findings fixed, preserving the contributor's authorship.

## Decisions (made with operator 2026-06-10)

- **Delivery:** contributor's fork forbids maintainer edits
  (`maintainerCanModify: false`), so: branch from main → merge PR head
  `f2e73f8` → one fix commit on top → merge to main → close #17 with a
  thank-you comment linking the merged commits.
- **Scope:** everything from the review — 3 bugs, engine change,
  cache-key clamp, pagination dedupe, validation test.
- **Schema:** dedicated `feedbacks` table KEPT — documented exception
  to CLAUDE.md invariant #5. Feedback is append-only event data with
  its own list/paginate access pattern, not a saved view/preset.
- **Frontend:** minimal fix on the contributor's structure, not a
  React Query rewrite.
- **Viewer-can-write:** conscious exception to "viewer = read-only
  everywhere" — product feedback is meta-state, not operator state.

## Changes

### Backend

1. **`internal/chstore/store.go` migration** — `feedbacks` DDL becomes
   `ENGINE = MergeTree ORDER BY (created_at, id)
   PARTITION BY toYYYYMM(created_at)`; drop the `version` column.
   Migration comment documents the invariant-#5 exception and the
   deliberate absence of a TTL. Safe to change in place: the PR is
   unmerged, so no install has created the old table.
2. **`internal/chstore/feedback.go`** — remove `FINAL` from
   `ListFeedbacks` (plain MergeTree now); keep the fetch-N+1 `hasMore`
   pattern and the limit/offset clamps as defense-in-depth.
3. **`internal/api/api_feedback.go`** — extract pure
   `validateFeedbackMessage(string) (string, error)`: TrimSpace,
   non-empty, `utf8.RuneCountInString(msg) <= 2000` (fixes the
   byte-vs-rune mismatch with the frontend's `maxLength={2000}`).
   Clamp `limit` in the handler BEFORE building the `serveCached`
   key — ≤0/unparseable → 20, >100 → 100 (true cap, replacing the
   store's reset-to-20) — and `offset` < 0 → 0, so raw query values
   can't create distinct cache entries for identical clamped results.
   Store clamps updated to match.
4. **New `internal/api/feedback_validate_test.go`** — table-driven:
   empty; whitespace-only; exactly 2000 ASCII (pass); 2001 ASCII
   (fail); 1500 two-byte Turkish chars = 3000 bytes (pass — the
   regression case); emoji within limit (pass). Header cites the
   PR #17 review per the regression-test convention.

### Frontend (`frontend/src/pages/Feedback.tsx` only)

5. Initial load via `useEffect(() => { load(0, false); }, [load])` —
   replaces the `useState`-initializer side effect (double-fetches
   under StrictMode, runs during render).
6. Real design tokens: feed items use the `.card` class; muted text
   `var(--text2)`; error text `var(--err)`; textarea drops its
   hand-rolled style block for the global textarea style (keeps
   `resize: vertical`, `width: 100%`). The previously referenced
   `--fg`, `--fg-muted`, `--color-error`, `--bg-card`, `--bg-input`
   variables do not exist in `globals.css`.
7. Dedupe by `id` when appending a fetched page, so the optimistic
   prepend after submit can't produce duplicate rows/keys against
   offset pagination.

## Unchanged by decision

30s GET cache (submitter sees own entry via optimistic prepend); no
rate limit on POST (single-tenant internal tool); `/api/feedbacks`
auth via the global JWT middleware (verified — not in `SkipPath`).

## Error handling

Unchanged paths: `writeErr` on store failure, 400 on validation
failure, existing loading/error/empty states in the UI.

## Testing & gates

`go test ./...` (new validation test), `go build ./...`,
`cd frontend && npx tsc --noEmit`, `make audit`. No Playwright pass
required (token swap to existing classes; per playwright-cadence
preference).

## Release

Merge to main, tag `v0.8.106`, `git push && git push --tags`, close
#17 with credit comment, redeploy minikube to v0.8.106.
