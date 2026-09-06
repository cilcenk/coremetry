package chstore

import (
	"os"
	"sort"
	"strings"
	"testing"
)

// v0.10.496 — regression: the Stage 2 id-split retry (v0.9.300) recursed
// with the caller's TraceFilter unchanged, so EACH half applied
// `LIMIT pageLimit OFFSET f.Offset` and mergeTracePages joined two heads
// that had both already skipped `offset` rows — a page with offset > 0
// skipped up to 2× the rows it should (found in the v0.10.494 review).
// Contract: each half fetches its first offset+limit rows with OFFSET 0
// and assembleSplitPage cuts the page from the UNION, exactly like the
// un-split `ORDER BY … LIMIT limit+1 OFFSET offset` would.
func TestAssembleSplitPageMatchesSingleQuery_v0_10_496(t *testing.T) {
	row := func(id string, dur float64) TraceRow { return TraceRow{TraceID: id, DurationMs: dur} }
	// union of 9 traces, duration desc = a(900) … i(100)
	union := []TraceRow{row("a", 900), row("b", 800), row("c", 700), row("d", 600), row("e", 500), row("f", 400), row("g", 300), row("h", 200), row("i", 100)}
	// arbitrary split into two id halves
	loAll := []TraceRow{union[0], union[3], union[4], union[7]} // a d e h
	hiAll := []TraceRow{union[1], union[2], union[5], union[6], union[8]}
	// what a half's statement returns for `LIMIT n+1 OFFSET 0` then trimmed to n with hasMore
	half := func(all []TraceRow, n int) ([]TraceRow, bool) {
		if len(all) > n {
			return all[:n], true
		}
		return all, false
	}
	single := func(offset, limit int) ([]string, bool) {
		if offset >= len(union) {
			return nil, false
		}
		end := offset + limit
		if end > len(union) {
			end = len(union)
		}
		ids := []string{}
		for _, r := range union[offset:end] {
			ids = append(ids, r.TraceID)
		}
		return ids, len(union) > offset+limit
	}
	for _, c := range []struct{ offset, limit int }{{0, 3}, {2, 3}, {3, 3}, {6, 3}, {7, 5}, {9, 3}, {12, 3}, {0, 20}} {
		lo, loMore := half(loAll, c.offset+c.limit)
		hi, hiMore := half(hiAll, c.offset+c.limit)
		page, more := assembleSplitPage(lo, hi, loMore, hiMore, "duration", "desc", c.offset, c.limit)
		got := []string{}
		for _, r := range page {
			got = append(got, r.TraceID)
		}
		want, wantMore := single(c.offset, c.limit)
		if strings.Join(got, ",") != strings.Join(want, ",") || more != wantMore {
			t.Errorf("offset=%d limit=%d: got %v more=%v, single query gives %v more=%v", c.offset, c.limit, got, more, want, wantMore)
		}
	}
	// sanity: the union really is sorted the way traceRowLess sorts
	if !sort.SliceIsSorted(union, func(i, j int) bool { return union[i].DurationMs > union[j].DurationMs }) {
		t.Fatal("fixture must be duration desc")
	}
}

// Source pin: the halves run with OFFSET 0 and an offset+limit budget, and
// the page comes from assembleSplitPage — a helper the recursion does not
// call pins nothing (feedback-tested-but-unreachable).
func TestSplitRetryUsesZeroOffsetHalves_v0_10_496(t *testing.T) {
	b, err := os.ReadFile("trace_slice.go")
	if err != nil {
		t.Fatal(err)
	}
	src := string(b)
	i := strings.Index(src, "func (s *Store) runTraceStage2(")
	if i < 0 {
		t.Fatal("runTraceStage2 yok")
	}
	body := src[i:]
	for _, want := range []string{
		"hf.Offset, hf.Limit = 0, f.Offset+f.Limit",
		"s.runTraceStage2(ctx, hf, stage2, idArgs[:half], floor, ceil, hf.Limit+1)",
		"s.runTraceStage2(ctx, hf, stage2, idArgs[half:], floor, ceil, hf.Limit+1)",
		"assembleSplitPage(lo, hi, loMore, hiMore, f.Sort, f.Order, f.Offset, f.Limit)",
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("runTraceStage2 missing %q", want)
		}
	}
	if strings.Contains(body, "s.runTraceStage2(ctx, f, stage2, idArgs[") {
		t.Fatal("a half must never recurse with the caller's own OFFSET")
	}
}
