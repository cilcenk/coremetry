# Land PR #17 (Feedback Page) with Review Fixes — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Merge community PR #17 (user feedback page) onto main with all review findings fixed, released as v0.8.106.

**Architecture:** Cherry-pick the contributor's commit `f2e73f8` onto a branch from main (preserves their authorship, keeps linear history), apply one fix commit on top (MergeTree engine, rune-count validation + test, cache-key clamp, useEffect initial load, real CSS tokens, pagination dedupe), fast-forward merge to main, tag, push, close the PR with credit.

**Tech Stack:** Go 1.22 (net/http, clickhouse-go), ClickHouse DDL, React + Vite + TypeScript, design tokens from `frontend/src/styles/globals.css`.

**Spec:** `docs/superpowers/specs/2026-06-10-feedback-pr17-fixes-design.md`

---

### Task 1: Branch and cherry-pick the contributor's commit

**Files:** none edited by hand (cherry-pick may need conflict resolution).

- [ ] **Step 1: Create the branch and fetch the PR head**

```bash
cd /Users/cenk/Documents/gotrace
git checkout -b feedback-pr17 main
git fetch origin pull/17/head
git cherry-pick f2e73f8
```

Expected: clean cherry-pick, OR conflicts in some of: `frontend/src/App.tsx`, `frontend/src/components/Sidebar.tsx`, `frontend/src/lib/{api,types,i18n}.ts`, `internal/api/api.go`, `internal/chstore/store.go` (main moved past the PR's base — v0.8.104/105 touched the frontend).

- [ ] **Step 2: Resolve conflicts if any**

Resolution rule for every conflicted file: **keep main's current content AND add the PR's feedback lines** — the PR only ever appends (a lazy import + route in App.tsx, a nav group in Sidebar.tsx, two api methods, one interface at end of types.ts, i18n keys, two route registrations in api.go, one DDL string in store.go). Nothing in the PR modifies existing lines except the lucide-react import list in Sidebar.tsx (adds `MessageCircle`).

```bash
git add -A && git cherry-pick --continue
```

- [ ] **Step 3: Verify the contributor's commit landed with their authorship**

```bash
git log -1 --format='%an %ae | %cn'
```

Expected: author is `Atıl Sensalduz`, committer is you.

- [ ] **Step 4: Sanity build**

```bash
go build ./... && cd frontend && npx tsc --noEmit && cd ..
```

Expected: both clean (they were clean on the PR head; conflicts are the only risk).

---

### Task 2: Rune-count validation — test first (TDD)

**Files:**
- Create: `internal/api/feedback_validate_test.go`
- Modify: `internal/api/api_feedback.go`

- [ ] **Step 1: Write the failing test**

Create `internal/api/feedback_validate_test.go`:

```go
package api

// Regression test for the PR #17 review (feedback page, v0.8.106).
// Original symptom: submitFeedback validated len(message) — BYTES —
// while the frontend's maxLength={2000} counts characters. Turkish
// text (2-byte runes) or emoji passed the form but got a 400 from
// the backend. Validation must count runes, not bytes.

import (
	"strings"
	"testing"
)

func TestValidateFeedbackMessage(t *testing.T) {
	cases := []struct {
		name    string
		in      string
		want    string
		wantErr bool
	}{
		{"empty", "", "", true},
		{"whitespace only", "  \n\t ", "", true},
		{"trims surrounding space", "  hello  ", "hello", false},
		{"2000 ascii ok", strings.Repeat("a", 2000), strings.Repeat("a", 2000), false},
		{"2001 ascii too long", strings.Repeat("a", 2001), "", true},
		// 1500 chars × 2 bytes = 3000 bytes — the regression case.
		{"1500 two-byte turkish ok", strings.Repeat("ğ", 1500), strings.Repeat("ğ", 1500), false},
		// 2000 chars × 4 bytes = 8000 bytes, still 2000 runes.
		{"2000 emoji ok", strings.Repeat("🚀", 2000), strings.Repeat("🚀", 2000), false},
		{"2001 emoji too long", strings.Repeat("🚀", 2001), "", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := validateFeedbackMessage(tc.in)
			if (err != nil) != tc.wantErr {
				t.Fatalf("err = %v, wantErr = %v", err, tc.wantErr)
			}
			if !tc.wantErr && got != tc.want {
				t.Fatalf("got %d chars, want %d", len(got), len(tc.want))
			}
		})
	}
}
```

- [ ] **Step 2: Run the test to verify it fails**

```bash
go test ./internal/api/ -run TestValidateFeedbackMessage
```

Expected: FAIL to compile — `undefined: validateFeedbackMessage`.

- [ ] **Step 3: Implement validation + handler clamps**

Replace the ENTIRE content of `internal/api/api_feedback.go` with:

```go
package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/cilcenk/coremetry/internal/auth"
	"github.com/cilcenk/coremetry/internal/chstore"
)

func (s *Server) listFeedbacks(w http.ResponseWriter, r *http.Request) {
	// Clamp BEFORE building the cache key so raw query values can't
	// mint distinct cache entries for identical clamped results.
	limit := parseInt(r.URL.Query().Get("limit"), 20)
	if limit <= 0 {
		limit = 20
	} else if limit > 100 {
		limit = 100
	}
	offset := parseInt(r.URL.Query().Get("offset"), 0)
	if offset < 0 {
		offset = 0
	}
	key := fmt.Sprintf("feedbacks:limit=%d:offset=%d", limit, offset)
	s.serveCached(w, r, key, 30*time.Second, func() (any, error) {
		items, hasMore, err := s.store.ListFeedbacks(r.Context(), limit, offset)
		if err != nil {
			return nil, err
		}
		if items == nil {
			items = []chstore.Feedback{}
		}
		return map[string]any{"feedbacks": items, "hasMore": hasMore}, nil
	})
}

// validateFeedbackMessage trims and bounds a feedback submission.
// Counts RUNES, not bytes — the frontend's maxLength={2000} counts
// characters, so multibyte text (Turkish, emoji) must not be
// rejected for its UTF-8 byte length.
func validateFeedbackMessage(msg string) (string, error) {
	msg = strings.TrimSpace(msg)
	if msg == "" {
		return "", errors.New("message required")
	}
	if utf8.RuneCountInString(msg) > 2000 {
		return "", errors.New("message too long (max 2000 chars)")
	}
	return msg, nil
}

func (s *Server) submitFeedback(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Message string `json:"message"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "invalid body", http.StatusBadRequest)
		return
	}
	defer r.Body.Close()
	msg, err := validateFeedbackMessage(body.Message)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	claims := auth.FromContext(r.Context())
	userID, userEmail := "", ""
	if claims != nil {
		userID = claims.UserID
		userEmail = claims.Email
	}

	saved, err := s.store.InsertFeedback(r.Context(), chstore.Feedback{
		UserID:    userID,
		UserEmail: userEmail,
		Message:   msg,
	})
	if err != nil {
		writeErr(w, err)
		return
	}
	s.audit(r, "feedback.submit", "feedback", saved.ID,
		fmt.Sprintf(`{"email":%q}`, userEmail))
	writeJSON(w, saved)
}
```

- [ ] **Step 4: Run the test to verify it passes**

```bash
go test ./internal/api/ -run TestValidateFeedbackMessage -v
```

Expected: PASS, 8 subtests.

---

### Task 3: ClickHouse — MergeTree engine + drop FINAL

**Files:**
- Modify: `internal/chstore/store.go` (the `feedbacks` DDL added by the cherry-pick, near the end of the `tables` slice in `migrate()`)
- Modify: `internal/chstore/feedback.go`

- [ ] **Step 1: Replace the DDL**

In `internal/chstore/store.go`, find the cherry-picked block:

```go
		`CREATE TABLE IF NOT EXISTS feedbacks (
			id         String,
			user_id    String,
			user_email String,
			message    String,
			created_at DateTime64(9) DEFAULT now64(9),
			version    UInt64 DEFAULT toUnixTimestamp64Nano(now64(9))
		) ENGINE = ReplacingMergeTree(version)
		ORDER BY id`,
```

Replace with:

```go
		// feedbacks (PR #17 / v0.8.106) — dedicated table by design:
		// append-only event data with its own list/paginate access
		// pattern, NOT per-user saved state, so the saved_views
		// catch-all (invariant #5) doesn't fit. Plain MergeTree —
		// rows are immutable, a Replacing dedup key would never
		// repeat (random id) and FINAL would be pure read cost.
		// No TTL on purpose: feedback volume is tiny and it should
		// outlive telemetry retention.
		`CREATE TABLE IF NOT EXISTS feedbacks (
			id         String,
			user_id    String,
			user_email String,
			message    String,
			created_at DateTime64(9) DEFAULT now64(9)
		) ENGINE = MergeTree
		PARTITION BY toYYYYMM(created_at)
		ORDER BY (created_at, id)`,
```

(Safe in place: the PR was never merged, so no install has created the old table.)

- [ ] **Step 2: Update ListFeedbacks — no FINAL, clamp matches handler**

In `internal/chstore/feedback.go`, replace the `ListFeedbacks` function with:

```go
func (s *Store) ListFeedbacks(ctx context.Context, limit, offset int) ([]Feedback, bool, error) {
	// Defense-in-depth — the API handler clamps before its cache
	// key; mirror the same bounds here for any other caller.
	if limit <= 0 {
		limit = 20
	} else if limit > 100 {
		limit = 100
	}
	if offset < 0 {
		offset = 0
	}
	// Fetch one extra row to determine whether more pages exist.
	rows, err := s.conn.Query(ctx, `
		SELECT id, user_id, user_email, message, created_at
		FROM feedbacks
		ORDER BY created_at DESC
		LIMIT ? OFFSET ?
		SETTINGS max_execution_time = 10`,
		limit+1, offset,
	)
	if err != nil {
		return nil, false, err
	}
	defer rows.Close()

	var out []Feedback
	for rows.Next() {
		var f Feedback
		var createdAt time.Time
		if err := rows.Scan(&f.ID, &f.UserID, &f.UserEmail, &f.Message, &createdAt); err != nil {
			return nil, false, err
		}
		f.CreatedAt = createdAt.UnixNano()
		out = append(out, f)
	}
	hasMore := len(out) > limit
	if hasMore {
		out = out[:limit]
	}
	return out, hasMore, nil
}
```

(Diff vs the PR: `FROM feedbacks FINAL` → `FROM feedbacks`; the limit clamp caps at 100 instead of resetting to 20. `InsertFeedback` is unchanged.)

- [ ] **Step 3: Build**

```bash
go build ./...
```

Expected: clean.

---

### Task 4: Frontend — useEffect, real tokens, dedupe

**Files:**
- Modify: `frontend/src/pages/Feedback.tsx`

- [ ] **Step 1: Replace the file**

Replace the ENTIRE content of `frontend/src/pages/Feedback.tsx` with:

```tsx
import { useState, useEffect, useCallback } from 'react';
import { Spinner, Empty } from '@/components/Spinner';
import { Button } from '@/components/ui/Button';
import { api } from '@/lib/api';
import type { Feedback } from '@/lib/types';

const PAGE_SIZE = 20;

function formatDate(unixNs: number): string {
  return new Date(unixNs / 1_000_000).toLocaleString();
}

export default function FeedbackPage() {
  const [feedbacks, setFeedbacks] = useState<Feedback[] | null>(null);
  const [hasMore, setHasMore] = useState(false);
  const [offset, setOffset] = useState(0);
  const [loading, setLoading] = useState(true);
  const [loadingMore, setLoadingMore] = useState(false);
  const [error, setError] = useState<string | null>(null);

  const [message, setMessage] = useState('');
  const [submitting, setSubmitting] = useState(false);
  const [submitError, setSubmitError] = useState<string | null>(null);

  const load = useCallback(async (nextOffset: number, append: boolean) => {
    if (nextOffset === 0) setLoading(true);
    else setLoadingMore(true);
    setError(null);
    try {
      const res = await api.listFeedbacks(PAGE_SIZE, nextOffset);
      if (!res) {
        setError('Failed to load feedback.');
        return;
      }
      // Dedupe by id: the optimistic prepend after submit shifts
      // server-side offsets, so a later page can repeat a row.
      setFeedbacks(prev => {
        if (!append || !prev) return res.feedbacks;
        const seen = new Set(prev.map(p => p.id));
        return [...prev, ...res.feedbacks.filter(f => !seen.has(f.id))];
      });
      setHasMore(res.hasMore);
      setOffset(nextOffset + res.feedbacks.length);
    } catch {
      setError('Failed to load feedback.');
    } finally {
      setLoading(false);
      setLoadingMore(false);
    }
  }, []);

  useEffect(() => { load(0, false); }, [load]);

  async function handleSubmit(e: React.FormEvent) {
    e.preventDefault();
    const trimmed = message.trim();
    if (!trimmed) return;
    setSubmitting(true);
    setSubmitError(null);
    try {
      const saved = await api.submitFeedback(trimmed);
      if (!saved) {
        setSubmitError('Submission failed. Please try again.');
        return;
      }
      setMessage('');
      setFeedbacks(prev => [saved, ...(prev ?? [])]);
    } catch {
      setSubmitError('Submission failed. Please try again.');
    } finally {
      setSubmitting(false);
    }
  }

  return (
    <div style={{ maxWidth: 720, margin: '0 auto', padding: '24px 16px' }}>
      <h1 style={{ fontSize: 20, fontWeight: 600, marginBottom: 8 }}>Feedback</h1>
      <p style={{ color: 'var(--text2)', marginBottom: 24, fontSize: 14 }}>
        Share your thoughts about Coremetry. All feedback is visible to the team.
      </p>

      {/* Submission form */}
      <form onSubmit={handleSubmit} style={{ marginBottom: 32 }}>
        <textarea
          value={message}
          onChange={e => setMessage(e.target.value)}
          placeholder="Write your feedback here…"
          maxLength={2000}
          rows={4}
          disabled={submitting}
          style={{
            // Mirrors the global `input, select` tokens — the global
            // rule doesn't cover textarea, and extending it would
            // restyle 20 existing textareas.
            width: '100%',
            boxSizing: 'border-box',
            padding: '8px 12px',
            borderRadius: 6,
            border: '1px solid var(--border)',
            background: 'var(--bg2)',
            color: 'var(--text)',
            fontSize: 13,
            resize: 'vertical',
            fontFamily: 'inherit',
          }}
        />
        <div style={{ display: 'flex', alignItems: 'center', gap: 12, marginTop: 8 }}>
          <Button
            variant="primary"
            type="submit"
            disabled={submitting || !message.trim()}
          >
            {submitting ? 'Submitting…' : 'Submit feedback'}
          </Button>
          <span style={{ fontSize: 12, color: 'var(--text2)' }}>
            {message.length}/2000
          </span>
          {submitError && (
            <span style={{ fontSize: 13, color: 'var(--err)' }}>
              {submitError}
            </span>
          )}
        </div>
      </form>

      {/* List */}
      {loading && <Spinner />}
      {error && !loading && (
        <Empty icon="⚠" title={error} />
      )}
      {!loading && !error && feedbacks !== null && feedbacks.length === 0 && (
        <Empty icon="◇" title="No feedback yet. Be the first." />
      )}
      {feedbacks !== null && feedbacks.length > 0 && (
        <div style={{ display: 'flex', flexDirection: 'column', gap: 12 }}>
          {feedbacks.map(f => (
            <div key={f.id} className="card">
              <div style={{ display: 'flex', justifyContent: 'space-between', marginBottom: 6 }}>
                <span style={{ fontSize: 13, fontWeight: 500 }}>{f.userEmail || 'Anonymous'}</span>
                <span style={{ fontSize: 12, color: 'var(--text2)' }}>{formatDate(f.createdAt)}</span>
              </div>
              <p style={{ margin: 0, fontSize: 14, whiteSpace: 'pre-wrap', wordBreak: 'break-word' }}>
                {f.message}
              </p>
            </div>
          ))}

          {hasMore && (
            <div style={{ textAlign: 'center', marginTop: 8 }}>
              <Button
                variant="secondary"
                onClick={() => load(offset, true)}
                disabled={loadingMore}
              >
                {loadingMore ? 'Loading…' : 'Load more'}
              </Button>
            </div>
          )}
        </div>
      )}
    </div>
  );
}
```

Diff vs the PR version: `useEffect` import + initial-load line (replaces the
`useState(() => { load(...) })` render-time side effect); dedupe-by-id inside
`load`; `var(--fg-muted)` → `var(--text2)` (3×); `var(--color-error, #e55)` →
`var(--err)`; textarea tokens `--bg2`/`--border`/`--text`, 13px; feed item
`<div className="card">` instead of the hand-rolled border/background block.
Everything else byte-identical to the contributor's version.

- [ ] **Step 2: Typecheck**

```bash
cd frontend && npx tsc --noEmit && cd ..
```

Expected: clean.

---

### Task 5: Gates + the fix commit

- [ ] **Step 1: Full gate run**

```bash
go build ./... && go test ./... && cd frontend && npx tsc --noEmit && cd .. && make audit
```

Expected: all clean; `make audit` ends "audit clean (critical)" (the
Profiling.tsx 🟡 warning is pre-existing and known).

- [ ] **Step 2: Commit (exact files, no `git add -A`)**

```bash
git add internal/api/api_feedback.go internal/api/feedback_validate_test.go \
        internal/chstore/feedback.go internal/chstore/store.go \
        frontend/src/pages/Feedback.tsx \
        docs/superpowers/specs/2026-06-10-feedback-pr17-fixes-design.md \
        docs/superpowers/plans/2026-06-10-feedback-pr17-fixes.md
git commit -m "$(cat <<'EOF'
v0.8.106 — feat(feedback): user feedback page (PR #17 + review fixes)

Lands community PR #17 by @atilsensalduz (previous commit) with the
review fixes:

- feedbacks table: ReplacingMergeTree(version) ORDER BY id →
  MergeTree ORDER BY (created_at, id) PARTITION BY toYYYYMM.
  Append-only event data — a random dedup key never repeats, so
  Replacing+FINAL was pure read cost and the list query re-sorted
  the whole table every page.
- Validation counts runes, not bytes (utf8.RuneCountInString): the
  frontend's maxLength={2000} counts characters, so two-byte
  Turkish text was rejected at ~1000 visible chars. Table-driven
  regression test in feedback_validate_test.go.
- Initial fetch moved to useEffect — the useState-initializer ran a
  side effect during render and double-fetched under StrictMode.
- Real design tokens (.card, --text2, --err, --bg2): the page
  referenced --fg/--fg-muted/--color-error, which don't exist in
  globals.css, so muted text silently inherited colors.
- Cache key built from clamped limit/offset; page-append dedupes by
  id against the optimistic prepend.

Dedicated table is a documented invariant-#5 exception (append-only
events, not saved state). Viewer-can-write is a conscious exception:
product feedback is meta-state, not operator state.

Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>
EOF
)"
```

---

### Task 6: Merge to main, tag, push, close PR

- [ ] **Step 1: Fast-forward merge + tag**

```bash
git checkout main && git merge --ff-only feedback-pr17
git tag v0.8.106
```

If `--ff-only` fails (main moved while working): `git rebase main feedback-pr17`, re-run the Task 5 gate, then retry.

- [ ] **Step 2: Push (no pipes — a `| tail` can mask a failed push)**

```bash
git push && git push --tags
```

Expected: both succeed; the tag push triggers the Release workflow.

- [ ] **Step 3: Close PR #17 with credit**

```bash
gh pr close 17 --comment "Merged manually with the review fixes on top — your commit landed as-is (cherry-picked, authorship preserved) followed by a fix commit, released as v0.8.106. Changes on top of your version: feedbacks table engine → plain MergeTree ORDER BY (created_at, id) (append-only data, the Replacing dedup key never repeated), validation counts runes instead of bytes, initial fetch via useEffect, CSS variables mapped to the design tokens that actually exist in globals.css (.card, --text2, --err), and a pagination dedupe. Thanks for following the ship checklist this closely — serveCached, audit row, Spinner/Empty states and the N+1 hasMore were all spot on. 🙏"
git branch -d feedback-pr17
```

---

### Task 7: Redeploy local minikube to v0.8.106

Per `feedback-local-always-latest` + `feedback-otelcol-restart-after-rollout` memories.

- [ ] **Step 1: Rebuild + deploy (background, ONE at a time)**

```bash
make minikube-up
```

(Builds the image tagged from `git describe` → v0.8.106, side-loads into minikube, `helm upgrade --reuse-values`.)

- [ ] **Step 2: Restart the collector + port-forward after rollout**

```bash
kubectl rollout status deploy/coremetry --timeout=300s
kubectl rollout restart deploy/coremetry-otelcol
```

Then restart the local port-forward. (Bouncing the coremetry pod wedges the collector's gRPC exporter — zero-addresses — and the demos get 503s until otelcol restarts.)

- [ ] **Step 3: Verify the page live**

Open `/feedback`, submit a message with Turkish characters (e.g. "ğüşıöç …" ×300), confirm it renders in a `.card` with proper muted timestamp color, and reload to confirm persistence.
