package chstore

import (
	"strings"
	"testing"
)

// v0.7.36 — Operator-reported: Logs "Save view" → "expected 8 arguments got 7"
// (repro: trace-detail → Logs → Save view). PrepareBatch with no column list
// bound all 8 saved_views columns while Append passed 7, so every save failed
// on a fresh schema. The fix uses an explicit 7-column list (omitting the
// auto-defaulting `version`). This pins the list to the Append arg count so the
// two can't silently drift apart again. CLAUDE.md #11.
func TestSavedViewsInsertColumns(t *testing.T) {
	got := strings.Count(savedViewsInsertCols, ",") + 1
	if got != savedViewsInsertColCount {
		t.Fatalf("savedViewsInsertCols has %d columns, want %d (must equal UpsertSavedView's Append args)\n  list: %s",
			got, savedViewsInsertColCount, savedViewsInsertCols)
	}
	// `version` must be OMITTED — it defaults to now64(9) and drives the
	// ReplacingMergeTree(version) dedup; binding it would reintroduce the
	// 8-vs-7 mismatch.
	if strings.Contains(savedViewsInsertCols, "version") {
		t.Errorf("savedViewsInsertCols must omit `version` (it defaults): %s", savedViewsInsertCols)
	}
	for _, col := range []string{"id", "owner_id", "name", "page", "query_string", "pinned", "created_at"} {
		if !strings.Contains(savedViewsInsertCols, col) {
			t.Errorf("savedViewsInsertCols missing %q: %s", col, savedViewsInsertCols)
		}
	}
}
