package chstore

import (
	"errors"
	"fmt"
	"testing"
	"time"
)

// Regression guards for v0.9.795 (operator-reported): a group that had
// STOPPED firing showed an empty "Sample traces" list and an empty "Stack
// trace" box, both of which used to be full. Two defects compounded:
//
//	(a) the candidate window's upper bound was last_seen+1h. A group member
//	    cannot exist after last_seen, so that hour only donated the budget to
//	    SIBLING fingerprints sharing (service, exception.type) — one shared
//	    wrapper type is enough — whose spans are newer and sort first.
//	(b) the scan was a single LIMIT 500 shot: once siblings filled the page
//	    it gave up, never reaching the group's own rows.
//
// The tests below pin both halves: the window arithmetic, and the paged
// scan that must keep walking past a sibling-filled first page.

func TestExceptionScanWindow(t *testing.T) {
	base := time.Date(2026, 8, 8, 12, 0, 0, 0, time.UTC)

	cases := []struct {
		name           string
		first, last    time.Time
		wantFrom, want time.Time
	}{
		{
			// The bug: +1h let ~an hour of sibling traffic outrank the
			// group's own newest rows. 2 minutes is late-arrival slack only.
			name:     "upper bound is last_seen+2m, not +1h",
			first:    base,
			last:     base.Add(30 * time.Minute),
			wantFrom: base.Add(-time.Hour),
			want:     base.Add(32 * time.Minute),
		},
		{
			name:     "lower bound keeps the -1h first_seen slack",
			first:    base,
			last:     base,
			wantFrom: base.Add(-time.Hour),
			want:     base.Add(2 * time.Minute),
		},
		{
			name:     "long-lived group spans its whole life",
			first:    base,
			last:     base.Add(72 * time.Hour),
			wantFrom: base.Add(-time.Hour),
			want:     base.Add(72*time.Hour + 2*time.Minute),
		},
		{
			// Degenerate row (clock skew / partial merge): last_seen far
			// behind first_seen would invert the window and match nothing.
			name:     "inverted group row still yields a non-empty window",
			first:    base,
			last:     base.Add(-5 * time.Hour),
			wantFrom: base.Add(-time.Hour),
			want:     base.Add(-time.Hour).Add(time.Minute),
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			from, to := exceptionScanWindow(c.first.UnixNano(), c.last.UnixNano())
			if !from.Equal(c.wantFrom) {
				t.Errorf("from = %s, want %s", from.UTC(), c.wantFrom.UTC())
			}
			if !to.Equal(c.want) {
				t.Errorf("to = %s, want %s", to.UTC(), c.want.UTC())
			}
			if !to.After(from) {
				t.Errorf("window is empty: [%s, %s]", from.UTC(), to.UTC())
			}
			// The +1h regression, pinned directly: any upper bound more than
			// a few minutes past last_seen re-opens the sibling-starvation
			// hole, because candidates sort newest-first.
			if slack := to.Sub(c.last); slack > 2*time.Minute && c.last.After(c.first) {
				t.Errorf("upper bound is last_seen+%s; members cannot exist after "+
					"last_seen and the slack feeds sibling fingerprints", slack)
			}
		})
	}
}

// exSampleFixture builds a fake candidate stream, newest-first, one row per
// nanosecond going backwards from `newest`. fp(i) decides which fingerprint
// row i belongs to, mimicking the Go-side recompute.
type exSampleFixture struct {
	rows  []ExceptionSample
	pages int // how many fetch calls were made
}

func newExSampleFixture(newest time.Time, n int, mine func(i int) bool) *exSampleFixture {
	f := &exSampleFixture{}
	for i := 0; i < n; i++ {
		f.rows = append(f.rows, ExceptionSample{
			TraceID:    fmt.Sprintf("t%05d", i),
			SpanID:     fmt.Sprintf("s%05d", i),
			Time:       newest.Add(-time.Duration(i) * time.Nanosecond).UnixNano(),
			Message:    map[bool]string{true: "mine", false: "sibling"}[mine(i)],
			Stacktrace: map[bool]string{true: "at com.acme.Boom(Boom.java:1)", false: ""}[mine(i)],
			SpanName:   "GET /x",
		})
	}
	return f
}

// fetch mimics the CH page: newest `size` rows inside [from, to].
func (f *exSampleFixture) fetch(from, to time.Time, size int) ([]ExceptionSample, error) {
	f.pages++
	out := make([]ExceptionSample, 0, size)
	for _, r := range f.rows {
		ts := time.Unix(0, r.Time)
		if ts.After(to) || ts.Before(from) {
			continue
		}
		out = append(out, r)
		if len(out) >= size {
			break
		}
	}
	return out, nil
}

func mineOnly(sm ExceptionSample) bool { return sm.Message == "mine" }

func TestScanExceptionSamples_SiblingFilledFirstPage(t *testing.T) {
	newest := time.Date(2026, 8, 8, 12, 0, 0, 0, time.UTC)
	// The reported shape: the whole first page (exSampleBatch rows) belongs
	// to sibling fingerprints on the same (service, type); the group's own
	// occurrences start right after it.
	fx := newExSampleFixture(newest, exSampleBatch+50, func(i int) bool { return i >= exSampleBatch })

	res, err := scanExceptionSamples(newest.Add(-time.Hour), newest.Add(time.Minute), 10, fx.fetch, mineOnly)
	if err != nil {
		t.Fatalf("scan: %v", err)
	}
	if len(res.Samples) != 10 {
		t.Fatalf("got %d samples, want 10 — the pre-v0.9.795 single-shot scan "+
			"stopped at the sibling-filled first page and reported none",
			len(res.Samples))
	}
	if fx.pages < 2 {
		t.Errorf("fetched %d page(s); reaching the group needs a second page", fx.pages)
	}
	if res.Scanned <= exSampleBatch {
		t.Errorf("scanned = %d, want cumulative across pages (> %d)", res.Scanned, exSampleBatch)
	}
	if res.ScanCapped {
		t.Error("ScanCapped set although the scan succeeded well under the ceiling")
	}
	// Stack trace box is fed from these same samples — the reported "No
	// stack trace on the sampled occurrences." went away with the samples.
	if res.Samples[0].Stacktrace == "" {
		t.Error("first sample carries no stacktrace; the stack box stays empty")
	}
}

func TestScanExceptionSamples_HardCeiling(t *testing.T) {
	newest := time.Date(2026, 8, 8, 12, 0, 0, 0, time.UTC)
	// Nothing in the window belongs to the group and the window is far
	// bigger than the ceiling: the scan must stop at exSampleMaxScan and
	// SAY it stopped, not walk forever.
	fx := newExSampleFixture(newest, exSampleMaxScan*3, func(int) bool { return false })

	res, err := scanExceptionSamples(newest.Add(-time.Hour), newest.Add(time.Minute), 10, fx.fetch, mineOnly)
	if err != nil {
		t.Fatalf("scan: %v", err)
	}
	if len(res.Samples) != 0 {
		t.Fatalf("got %d samples, want 0", len(res.Samples))
	}
	if res.Scanned != exSampleMaxScan {
		t.Errorf("scanned = %d, want exactly the %d ceiling", res.Scanned, exSampleMaxScan)
	}
	if !res.ScanCapped {
		t.Error("ScanCapped false at the ceiling — the UI would claim the window was fully read")
	}
	if res.WindowExhausted {
		t.Error("WindowExhausted true although candidates remained — that is the false-empty this fix removes")
	}
	if fx.pages != exSampleMaxScan/exSampleBatch {
		t.Errorf("fetched %d pages, want %d", fx.pages, exSampleMaxScan/exSampleBatch)
	}
}

func TestScanExceptionSamples_WindowExhausted(t *testing.T) {
	newest := time.Date(2026, 8, 8, 12, 0, 0, 0, time.UTC)
	// Sibling-only traffic, but LESS than the ceiling: the honest answer is
	// "we read the group's whole window", which the UI phrases as a
	// retention hint rather than a scan-budget excuse.
	fx := newExSampleFixture(newest, exSampleBatch+7, func(int) bool { return false })

	res, err := scanExceptionSamples(newest.Add(-time.Hour), newest.Add(time.Minute), 10, fx.fetch, mineOnly)
	if err != nil {
		t.Fatalf("scan: %v", err)
	}
	if len(res.Samples) != 0 {
		t.Fatalf("got %d samples, want 0", len(res.Samples))
	}
	if !res.WindowExhausted {
		t.Error("WindowExhausted false although the window ran dry")
	}
	if res.ScanCapped {
		t.Error("ScanCapped true below the ceiling — the two endings must stay distinguishable (v0.9.296 lesson)")
	}
	if res.Scanned != exSampleBatch+7 {
		t.Errorf("scanned = %d, want %d", res.Scanned, exSampleBatch+7)
	}
}

func TestScanExceptionSamples_StopsAtLimitWithoutExtraPages(t *testing.T) {
	newest := time.Date(2026, 8, 8, 12, 0, 0, 0, time.UTC)
	fx := newExSampleFixture(newest, exSampleBatch*4, func(int) bool { return true })

	res, err := scanExceptionSamples(newest.Add(-time.Hour), newest.Add(time.Minute), 10, fx.fetch, mineOnly)
	if err != nil {
		t.Fatalf("scan: %v", err)
	}
	if len(res.Samples) != 10 {
		t.Fatalf("got %d samples, want 10", len(res.Samples))
	}
	if fx.pages != 1 {
		t.Errorf("fetched %d pages; a group with dense own traffic must be served by the first page", fx.pages)
	}
	if res.ScanCapped || res.WindowExhausted {
		t.Errorf("a satisfied scan must report neither ending: capped=%v exhausted=%v",
			res.ScanCapped, res.WindowExhausted)
	}
}

func TestScanExceptionSamples_FetchErrorSurfaces(t *testing.T) {
	newest := time.Date(2026, 8, 8, 12, 0, 0, 0, time.UTC)
	boom := errors.New("max_execution_time exceeded")
	calls := 0
	fetch := func(from, to time.Time, size int) ([]ExceptionSample, error) {
		calls++
		if calls == 1 {
			fx := newExSampleFixture(newest, exSampleBatch, func(int) bool { return false })
			return fx.fetch(from, to, size)
		}
		return nil, boom
	}
	res, err := scanExceptionSamples(newest.Add(-time.Hour), newest.Add(time.Minute), 10, fetch, mineOnly)
	if !errors.Is(err, boom) {
		t.Fatalf("err = %v, want the page error surfaced (a timed-out page must not read as an empty group)", err)
	}
	if res.WindowExhausted {
		t.Error("WindowExhausted set on an errored scan")
	}
}
