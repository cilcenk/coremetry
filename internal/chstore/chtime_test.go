package chstore

import (
	"strings"
	"testing"
	"time"
)

// v0.8.197 — regression guard for the operator-reported prod bug where
// toDateTime64(?, 9, 'UTC') args were formatted with time.RFC3339Nano, emitting
// a trailing 'Z' that CH's DateTime64 parser rejects (code 6). The bound string
// must never contain 'T' or 'Z', and a non-UTC input must be normalised to UTC.
func TestChDateTime64Arg(t *testing.T) {
	utc := time.Date(2026, 6, 27, 18, 51, 27, 714000000, time.UTC)
	got := chDateTime64Arg(utc)
	if strings.ContainsAny(got, "TZ") {
		t.Errorf("chDateTime64Arg must not contain 'T' or 'Z' (CH toDateTime64 rejects them): %q", got)
	}
	if got != "2026-06-27 18:51:27.714" {
		t.Errorf("chDateTime64Arg = %q, want \"2026-06-27 18:51:27.714\"", got)
	}
	// A whole-second time renders with no fractional part (CH parses it fine).
	if g := chDateTime64Arg(time.Date(2026, 6, 27, 18, 51, 27, 0, time.UTC)); g != "2026-06-27 18:51:27" {
		t.Errorf("whole-second = %q", g)
	}
	// Non-UTC input MUST be normalised to UTC, else the tz-less string is wrong.
	loc := time.FixedZone("UTC+3", 3*3600)
	if g := chDateTime64Arg(time.Date(2026, 6, 27, 21, 51, 27, 0, loc)); g != "2026-06-27 18:51:27" {
		t.Errorf("non-UTC not normalised to UTC: %q", g)
	}
}
