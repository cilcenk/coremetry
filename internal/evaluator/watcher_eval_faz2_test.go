// Watcher evaluation Faz-2 pure helpers (v0.9.x): agg-payload path
// walking (extractPayloadValue), the array_compare bucket machine
// (extractArrayCompareMax + arrayCompareSettleValue — ANY matching
// bucket fires, the Problem value is the MAX of the matches, and the
// settle value must round-trip through the shared count-alert
// machine's compare to the SAME verdict), calendar-cron due pacing
// (watcherCronDue — a daily 08:00 watch on the 1m tick runs on the
// first tick after 08:00, once per day), and the notification sample
// block (watcherSampleBlock).
package evaluator

import (
	"encoding/json"
	"math"
	"strings"
	"testing"
	"time"
)

const aggFixture = `{
  "hits": {"total": {"value": 42}},
  "aggregations": {
    "err_count": {"value": 123.5},
    "err.rate":  {"value": 5},
    "big":       {"value": 1753000000123456789},
    "by_svc": {"buckets": [
      {"key": "a", "doc_count": 120, "lat": {"value": 30}},
      {"key": "b", "doc_count": 80,  "lat": {"value": 90}},
      {"key": "c", "doc_count": 150}
    ]},
    "plain": {"values": [1, 9, 4]}
  }
}`

func TestExtractPayloadValue(t *testing.T) {
	tests := []struct {
		name    string
		path    string
		want    float64
		wantErr bool
	}{
		{"agg single value", "aggregations.err_count.value", 123.5, false},
		{"hits total", "hits.total.value", 42, false},
		{"numeric index into buckets", "aggregations.by_svc.buckets.0.doc_count", 120, false},
		{"dotted agg name resolves via longest literal key", "aggregations.err.rate.value", 5, false},
		{"missing agg", "aggregations.nope.value", 0, true},
		{"path into a scalar", "aggregations.err_count.value.deeper", 0, true},
		{"non-numeric terminal", "aggregations.by_svc.buckets.0.key", 0, true},
		{"index out of range", "aggregations.by_svc.buckets.9.doc_count", 0, true},
		{"terminal is an object", "aggregations.by_svc.buckets.0", 0, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := extractPayloadValue(json.RawMessage(aggFixture), tt.path)
			if (err != nil) != tt.wantErr {
				t.Fatalf("err = %v, wantErr %v", err, tt.wantErr)
			}
			if !tt.wantErr && got != tt.want {
				t.Fatalf("extractPayloadValue(%q) = %v, want %v", tt.path, got, tt.want)
			}
		})
	}
	if _, err := extractPayloadValue(json.RawMessage(`not json`), "aggregations.x"); err == nil {
		t.Fatal("malformed payload must error")
	}
}

func TestExtractArrayCompareMax(t *testing.T) {
	tests := []struct {
		name       string
		arrayPath  string
		itemPath   string
		comparator string
		threshold  float64
		want       arrayCompareResult
		wantErr    bool
	}{
		{"gte matches two buckets, value is their max",
			"aggregations.by_svc.buckets", "doc_count", ">=", 100,
			arrayCompareResult{Matched: true, MaxMatched: 150, MaxAll: 150, HasItems: true}, false},
		{"gte no bucket matches",
			"aggregations.by_svc.buckets", "doc_count", ">=", 200,
			arrayCompareResult{Matched: false, MaxAll: 150, HasItems: true}, false},
		{"lte matches the small bucket only",
			"aggregations.by_svc.buckets", "doc_count", "<=", 90,
			arrayCompareResult{Matched: true, MaxMatched: 80, MaxAll: 150, HasItems: true}, false},
		{"nested item path; buckets missing it are skipped",
			"aggregations.by_svc.buckets", "lat.value", ">", 50,
			arrayCompareResult{Matched: true, MaxMatched: 90, MaxAll: 90, HasItems: true}, false},
		{"empty item path compares the element itself",
			"aggregations.plain.values", "", ">", 5,
			arrayCompareResult{Matched: true, MaxMatched: 9, MaxAll: 9, HasItems: true}, false},
		{"array path not an array", "aggregations.err_count", "doc_count", ">=", 1, arrayCompareResult{}, true},
		{"array path missing", "aggregations.nope.buckets", "doc_count", ">=", 1, arrayCompareResult{}, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := extractArrayCompareMax(json.RawMessage(aggFixture), tt.arrayPath, tt.itemPath, tt.comparator, tt.threshold)
			if (err != nil) != tt.wantErr {
				t.Fatalf("err = %v, wantErr %v", err, tt.wantErr)
			}
			if !tt.wantErr && got != tt.want {
				t.Fatalf("extractArrayCompareMax = %+v, want %+v", got, tt.want)
			}
		})
	}
	t.Run("empty buckets", func(t *testing.T) {
		got, err := extractArrayCompareMax(json.RawMessage(`{"aggregations":{"x":{"buckets":[]}}}`), "aggregations.x.buckets", "doc_count", ">=", 1)
		if err != nil {
			t.Fatalf("empty buckets must not error: %v", err)
		}
		if got.Matched || got.HasItems {
			t.Fatalf("empty buckets = %+v, want no match, no items", got)
		}
	})
}

// The settle value hands the array_compare verdict to the SHARED
// count-alert machine, which re-derives breached via compare() — so
// the invariant is: compare(settleValue, comparator, threshold) MUST
// equal res.Matched for every shape, including the empty-bucket lt/lte
// edge where a naive 0 would false-fire.
func TestArrayCompareSettleValue(t *testing.T) {
	tests := []struct {
		name       string
		res        arrayCompareResult
		comparator string
		threshold  float64
		want       float64
	}{
		{"matched → max of matches", arrayCompareResult{Matched: true, MaxMatched: 150, MaxAll: 150, HasItems: true}, ">=", 100, 150},
		{"unmatched with items → observed max (fails compare)", arrayCompareResult{MaxAll: 150, HasItems: true}, ">=", 200, 150},
		{"unmatched lte with items → observed max", arrayCompareResult{MaxAll: 150, HasItems: true}, "<=", 50, 150},
		// v0.9.202 — sentetik değer artık Nextafter (threshold±1 büyük
		// |threshold|'da threshold'a çöküyordu); yön aynı, bir ULP komşu.
		{"empty buckets gte → synthetic non-breaching", arrayCompareResult{}, ">=", 0, math.Nextafter(0, math.Inf(-1))},
		{"empty buckets lte → synthetic non-breaching (0 would false-fire)", arrayCompareResult{}, "<=", 100, math.Nextafter(100, math.Inf(1))},
		{"empty buckets lt", arrayCompareResult{}, "<", 100, math.Nextafter(100, math.Inf(1))},
		{"empty buckets gte LARGE threshold stays non-breaching", arrayCompareResult{}, ">=", 1e18, math.Nextafter(1e18, math.Inf(-1))},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := arrayCompareSettleValue(tt.res, tt.comparator, tt.threshold)
			if got != tt.want {
				t.Fatalf("arrayCompareSettleValue = %v, want %v", got, tt.want)
			}
			if compare(got, tt.comparator, tt.threshold) != tt.res.Matched {
				t.Fatalf("verdict drift: compare(%v, %s, %v) = %v but Matched = %v",
					got, tt.comparator, tt.threshold, !tt.res.Matched, tt.res.Matched)
			}
		})
	}
}

func TestWatcherCronDue(t *testing.T) {
	daily := []string{"0 0 8 * * ?"}
	at := func(h, m, s int) time.Time { return time.Date(2026, 7, 24, h, m, s, 0, time.UTC) }
	tests := []struct {
		name  string
		last  time.Time
		exprs []string
		now   time.Time
		want  bool
	}{
		{"never ran → NOT due (v0.9.202: boot fırtınası yok — çağıran damgayı basar, sonraki ateşleme beklenir)", time.Time{}, daily, at(7, 30, 0), false},
		{"before the fire time → not due", at(7, 30, 0), daily, at(7, 59, 0), false},
		{"first tick after the fire time → due", at(7, 30, 0), daily, at(8, 0, 5), true},
		{"already ran after the fire → not due for the rest of the day", at(8, 0, 5), daily, at(18, 0, 0), false},
		{"multiple expressions: any passed fire counts", at(7, 30, 0), []string{"0 0 6 * * ?", "0 45 7 * * ?"}, at(7, 46, 0), true},
		{"unparseable-only expressions → never due", at(7, 30, 0), []string{"0 0 8 L * ?"}, at(9, 0, 0), false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := watcherCronDue(tt.last, tt.exprs, tt.now); got != tt.want {
				t.Fatalf("watcherCronDue = %v, want %v", got, tt.want)
			}
		})
	}
}

// Simulation on the real 1m evaluator tick: a daily-08:00 cron watch
// booted at 07:30 runs exactly once per day, on the first tick at or
// after 08:00 — never drifting, never doubling. Keep-alive planning
// (watcherTickPlanDue) is exercised alongside: while a problem is
// open, every not-due tick refreshes it, so the 3×tick stale sweep
// can never resolve a calendar watch "source silent" between fires
// (the Faz-1 F0/F9 contract carried over to cron pacing).
func TestWatcherCronDailySimulation(t *testing.T) {
	exprs := []string{"0 0 8 * * ?"}
	start := time.Date(2026, 7, 24, 7, 30, 0, 0, time.UTC)
	var lastRun, updatedAt time.Time
	problemOpen := false
	runs := 0
	var runTimes []time.Time
	for i := 0; i <= 2*24*60; i++ { // two days of 1m ticks
		now := start.Add(time.Duration(i) * time.Minute)
		// v0.9.202 — evaluateWatcher cron dalının boot davranışı: zero
		// lastRun'da damga basılır, KOŞULMAZ (sonraki planlı ateşleme).
		if lastRun.IsZero() {
			lastRun = now
		}
		if problemOpen && updatedAt.Before(now.Add(-3*time.Minute)) {
			t.Fatalf("tick %d: updated_at stale past the 3×tick sweep cutoff", i)
		}
		switch watcherTickPlanDue(watcherCronDue(lastRun, exprs, now), problemOpen) {
		case watcherTickRun:
			lastRun = now
			runs++
			runTimes = append(runTimes, now)
			problemOpen = true // continuously-breaching watch
			updatedAt = now
		case watcherTickKeepAlive:
			updatedAt = now
		case watcherTickIdle:
			if problemOpen {
				t.Fatalf("tick %d: idle with an open problem", i)
			}
		}
	}
	// v0.9.202: boot'ta koşulmaz — yalnız günde bir, 08:00 tick'inde (2 gün = 2).
	if runs != 2 {
		t.Fatalf("runs = %d (%v), want 2 (no boot run; one per 08:00)", runs, runTimes)
	}
	for _, rt := range runTimes[1:] {
		if rt.Hour() != 8 || rt.Minute() != 0 {
			t.Fatalf("daily fire landed at %s, want the first tick at 08:00", rt)
		}
	}
}

func TestWatcherSampleBlock(t *testing.T) {
	if got := watcherSampleBlock(nil); got != "" {
		t.Fatalf("no samples must add nothing, got %q", got)
	}
	if got := watcherSampleBlock([]string{}); got != "" {
		t.Fatalf("empty samples must add nothing, got %q", got)
	}
	got := watcherSampleBlock([]string{"a [svc] one", "b [svc] two"})
	want := "\nÖrnekler:\n- a [svc] one\n- b [svc] two"
	if got != want {
		t.Fatalf("watcherSampleBlock = %q, want %q", got, want)
	}
	if strings.Count(got, "\n- ") != 2 {
		t.Fatalf("one bullet per sample line: %q", got)
	}
}
