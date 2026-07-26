package logstore

import (
	"encoding/json"
	"os"
	"regexp"
	"strings"
	"testing"
)

// v0.9.288 — every ES log query is sent with a SOFT timeout. The whole
// point of a soft timeout is that ES returns what it finished computing
// and reports `timed_out: true`; shard failures work the same way.
// None of the four decode structs read either field, so a partial
// answer was indistinguishable from a complete one. At 10B docs/day
// that is not the edge case — it is the realistic outcome of a heavy
// search.
//
// These tests decode the REAL response shape, not a hand-rolled mirror
// of it, so a rename on either side fails here.

func TestESSearchEnvelopeDecodesPartiality(t *testing.T) {
	cases := []struct {
		name        string
		body        string
		wantPartial bool
		wantFailed  int
	}{
		{
			name:        "clean response",
			body:        `{"timed_out":false,"_shards":{"total":30,"successful":30,"failed":0,"skipped":0}}`,
			wantPartial: false,
		},
		{
			// The headline case: ES answered 200 with real data that is
			// simply incomplete.
			name:        "soft timeout fired",
			body:        `{"timed_out":true,"_shards":{"total":30,"successful":30,"failed":0,"skipped":0}}`,
			wantPartial: true,
		},
		{
			name:        "shards failed",
			body:        `{"timed_out":false,"_shards":{"total":30,"successful":27,"failed":3,"skipped":0}}`,
			wantPartial: true,
			wantFailed:  3,
		},
		{
			name:        "both at once",
			body:        `{"timed_out":true,"_shards":{"total":30,"successful":27,"failed":3,"skipped":0}}`,
			wantPartial: true,
			wantFailed:  3,
		},
		{
			// SKIPPED shards are the can_match phase working: those
			// shards provably hold nothing matching. Counting them as
			// partial would flag exactly the best-pruned queries — and
			// after v0.9.283's index narrowing, that is most of them.
			name:        "skipped shards are pruning, not failure",
			body:        `{"timed_out":false,"_shards":{"total":30,"successful":4,"failed":0,"skipped":26}}`,
			wantPartial: false,
		},
		{
			// Absent fields (an older cluster, or a response shape
			// without them) must read as complete — the envelope may not
			// invent uncertainty it has no evidence for.
			name:        "absent fields read as complete",
			body:        `{"hits":{"hits":[]}}`,
			wantPartial: false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var e esSearchEnvelope
			if err := json.Unmarshal([]byte(tc.body), &e); err != nil {
				t.Fatalf("decode: %v", err)
			}
			if got := e.partial(); got != tc.wantPartial {
				t.Fatalf("partial() = %v, want %v", got, tc.wantPartial)
			}
			if e.Shards.Failed != tc.wantFailed {
				t.Fatalf("Shards.Failed = %d, want %d", e.Shards.Failed, tc.wantFailed)
			}
			if tc.wantPartial && e.describe() == "" {
				t.Fatal("a partial envelope must describe itself for the pod log")
			}
			if !tc.wantPartial && e.describe() != "" {
				t.Fatalf("a complete envelope must describe nothing, got %q", e.describe())
			}
		})
	}
}

// track_total_hits is capped at 10,000 — counting every matching doc is
// precisely what you avoid at billion-doc scale. Past the cap ES answers
// relation "gte" and stops counting. Decoding only `value` turned "at
// least 10,000" into "exactly 10,000", while the identical number from
// the ClickHouse backend really is an exact count(): one string, two
// backends, two meanings, neither stated.
func TestESTotalRelation(t *testing.T) {
	cases := []struct {
		name         string
		body         string
		wantValue    int
		wantLowerBnd bool
	}{
		{"exact count under the cap", `{"value":137,"relation":"eq"}`, 137, false},
		{"capped — at least this many", `{"value":10000,"relation":"gte"}`, 10000, true},
		{"absent relation reads as exact (pre-v0.9.288 behaviour)", `{"value":42}`, 42, false},
		{"unknown relation is not treated as a bound", `{"value":42,"relation":"lte"}`, 42, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var total esTotal
			if err := json.Unmarshal([]byte(tc.body), &total); err != nil {
				t.Fatalf("decode: %v", err)
			}
			if total.Value != tc.wantValue {
				t.Fatalf("Value = %d, want %d", total.Value, tc.wantValue)
			}
			if got := total.isLowerBound(); got != tc.wantLowerBnd {
				t.Fatalf("isLowerBound() = %v, want %v", got, tc.wantLowerBnd)
			}
		})
	}
}

// The risk named when this slice was designed: four separate decode
// structs, and if ONE is forgotten that path silently keeps telling the
// old lie. Rather than trusting a review, assert it — every response
// body decoded in elasticsearch.go must embed the shared envelope.
//
// A new search decode site added without it fails here with its line
// number.
func TestEverySearchDecodeCarriesTheEnvelope(t *testing.T) {
	src, err := os.ReadFile("elasticsearch.go")
	if err != nil {
		t.Fatalf("read source: %v", err)
	}
	lines := strings.Split(string(src), "\n")

	// Each `var raw struct {` opens a response decode. Scan forward to
	// its closing brace at the same indent and require the embed.
	open := regexp.MustCompile(`^(\s*)var raw struct \{`)
	found := 0
	for i, ln := range lines {
		m := open.FindStringSubmatch(ln)
		if m == nil {
			continue
		}
		found++
		indent := m[1]
		body := []string{}
		for j := i + 1; j < len(lines); j++ {
			if lines[j] == indent+"}" {
				break
			}
			body = append(body, lines[j])
		}
		if !strings.Contains(strings.Join(body, "\n"), "esSearchEnvelope") {
			t.Errorf("elasticsearch.go:%d — `var raw struct` does not embed esSearchEnvelope; "+
				"this decode path reports complete results even when ES timed out or lost shards", i+1)
		}
	}
	if found < 4 {
		t.Fatalf("expected at least 4 response decode sites, found %d — "+
			"the scan pattern has drifted from the source and is no longer guarding anything", found)
	}
}
