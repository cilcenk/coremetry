package chstore

import (
	"strings"
	"testing"
	"time"
)

// v0.8.241 — notification dispatch history. ListNotificationLog reads
// the append-only notification_log; per the CLAUDE.md hard constraint
// every such read MUST carry a LIMIT, a SETTINGS max_execution_time,
// and a time-bounded WHERE on the sent_at ORDER BY prefix — otherwise
// a 90-day table full-scans at operator-open time. This pins the SQL
// SHAPE (no live CH needed): a regression that dropped any bound would
// break here.
func TestBuildNotificationLogQuery_Bounds(t *testing.T) {
	from := time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC)
	to := time.Date(2026, 7, 4, 0, 0, 0, 0, time.UTC)

	sql, args := buildNotificationLogQuery(from, to, "", 100, 0)

	mustContain := []string{
		"sent_at >= ?",          // lower time bound on the ORDER BY prefix
		"sent_at < ?",           // upper time bound
		"ORDER BY sent_at DESC", // newest-first, prefix-aligned
		"LIMIT ? OFFSET ?",      // bounded row count
		"max_execution_time",    // wall-clock cap
		"FROM notification_log", // never FROM ... FINAL (plain MergeTree)
	}
	for _, frag := range mustContain {
		if !strings.Contains(sql, frag) {
			t.Errorf("query missing required fragment %q\nSQL:\n%s", frag, sql)
		}
	}
	// MergeTree, not ReplacingMergeTree — reads must NOT use FINAL.
	if strings.Contains(sql, "FINAL") {
		t.Errorf("notification_log is a plain MergeTree; read must not use FINAL\nSQL:\n%s", sql)
	}
	// from, to are the first two bind args in order.
	if len(args) < 2 || args[0] != from || args[1] != to {
		t.Fatalf("expected args[0]=from args[1]=to, got %v", args)
	}
	// No kind filter → exactly from, to, limit, offset (4 args).
	if len(args) != 4 {
		t.Fatalf("no-kind query: expected 4 args (from,to,limit,offset), got %d: %v", len(args), args)
	}
}

// A channel_kind filter appends exactly one predicate + one bind arg,
// positioned before the trailing LIMIT/OFFSET args.
func TestBuildNotificationLogQuery_KindFilter(t *testing.T) {
	from := time.Unix(1000, 0).UTC()
	to := time.Unix(2000, 0).UTC()

	sql, args := buildNotificationLogQuery(from, to, "slack", 50, 10)

	if !strings.Contains(sql, "channel_kind = ?") {
		t.Errorf("kind filter missing channel_kind predicate\nSQL:\n%s", sql)
	}
	// from, to, kind, limit, offset.
	if len(args) != 5 {
		t.Fatalf("kind query: expected 5 args, got %d: %v", len(args), args)
	}
	if args[2] != "slack" {
		t.Errorf("expected args[2]=\"slack\", got %v", args[2])
	}
	if args[3] != 50 || args[4] != 10 {
		t.Errorf("expected trailing limit=50 offset=10, got limit=%v offset=%v", args[3], args[4])
	}
}

// limit/offset are clamped: non-positive or oversized limit falls back
// to 100; negative offset to 0. Guards the pagination bound.
func TestBuildNotificationLogQuery_LimitClamp(t *testing.T) {
	from := time.Unix(1000, 0).UTC()
	to := time.Unix(2000, 0).UTC()

	cases := []struct {
		name              string
		limit, offset     int
		wantLimit, wantOf int
	}{
		{"zero limit -> 100", 0, 0, 100, 0},
		{"negative limit -> 100", -5, 0, 100, 0},
		{"oversized limit -> 100", 100000, 0, 100, 0},
		{"in-range kept", 250, 40, 250, 40},
		{"negative offset -> 0", 100, -1, 100, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, args := buildNotificationLogQuery(from, to, "", tc.limit, tc.offset)
			gotLimit, gotOffset := args[len(args)-2], args[len(args)-1]
			if gotLimit != tc.wantLimit {
				t.Errorf("limit = %v, want %d", gotLimit, tc.wantLimit)
			}
			if gotOffset != tc.wantOf {
				t.Errorf("offset = %v, want %d", gotOffset, tc.wantOf)
			}
		})
	}
}
