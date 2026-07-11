package anomaly

import (
	"math"
	"strings"
	"testing"
	"time"

	"github.com/cilcenk/coremetry/internal/chstore"
)

// v0.8.46 (anomaly intelligence Phase 4) — time-of-day seasonal baseline. A
// flat 24h baseline mixes low off-peak/night traffic with the daytime peak,
// so the daily morning ramp looks anomalous every day (false positive) and a
// real off-peak dip hides. The seasonal baseline compares the current slot to
// the SAME time-of-day on prior days. These tests pin the baseline choice +
// prove the diurnal false positive disappears (the fetch itself is CH-bound +
// exercised live; the math is what's unit-tested).
//
// v0.8.250 — operator-reported diurnal false positives on off-peak/night slots
// traced to SAMPLE SCARCITY (a Saturday-night slot had ~2 candidate samples,
// below seasonalMinSamples, so it fell back to the flat window and fired).
// Fix widens the baseline three ways: ±neighbour-bucket window (dayClass +
// circular-midnight distance in seasonalBaselineSQL), 14-day history, and a
// three-way weekday/saturday/sunday class. These tests pin dayClass, the SQL
// shape (wrap + bounds + MV), and the operator-tunable param resolution.

func TestChooseBaseline(t *testing.T) {
	consec := []float64{1, 2, 3}
	// Enough seasonal samples → seasonal wins (same slice header returned).
	enough := []float64{10, 11, 12, 13} // len == default seasonalMinSamples (4)
	if got := chooseBaseline(enough, consec, seasonalMinSamples); &got[0] != &enough[0] {
		t.Error("with >= minSamples seasonal samples, seasonal must be used")
	}
	// Too few → fall back to the consecutive baseline.
	if got := chooseBaseline([]float64{10, 11}, consec, seasonalMinSamples); &got[0] != &consec[0] {
		t.Error("with too few seasonal samples, must fall back to consecutive")
	}
	// Nil seasonal → consecutive.
	if got := chooseBaseline(nil, consec, seasonalMinSamples); &got[0] != &consec[0] {
		t.Error("nil seasonal must fall back to consecutive")
	}
	// minSamples is a parameter now (operator-tunable): the SAME 3-sample
	// seasonal set is trusted at minSamples=3 but rejected at minSamples=4.
	three := []float64{10, 11, 12}
	if got := chooseBaseline(three, consec, 3); &got[0] != &three[0] {
		t.Error("3 seasonal samples must be trusted when minSamples=3")
	}
	if got := chooseBaseline(three, consec, 4); &got[0] != &consec[0] {
		t.Error("3 seasonal samples must fall back when minSamples=4")
	}
}

// TestDayClass pins the three-way bank day class across a full week. Splitting
// the old weekday/weekend binary into weekday / saturday / sunday keeps a
// bank's distinct Saturday and Sunday profiles from poisoning each other's
// baseline (cmt ≠ paz). Mirrored in SQL by the multiIf(toDayOfWeek …).
func TestDayClass(t *testing.T) {
	// A known Monday: 2026-06-01 is a Monday (UTC).
	monday := time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC)
	want := []string{
		"weekday",  // Mon
		"weekday",  // Tue
		"weekday",  // Wed
		"weekday",  // Thu
		"weekday",  // Fri
		"saturday", // Sat
		"sunday",   // Sun
	}
	for i, w := range want {
		day := monday.AddDate(0, 0, i)
		if got := dayClass(day); got != w {
			t.Errorf("%s (%s): dayClass = %q, want %q", day.Format("2006-01-02"), day.Weekday(), got, w)
		}
	}
}

// TestSeasonalBaselineSQLShape asserts the batched seasonal query keeps every
// scale guardrail + the v0.8.250 semantics: circular midnight-wrap neighbour
// distance, a LIMIT + max_execution_time bound, a time-bounded WHERE for
// partition pruning, the three-way day class, and that it reads the MV — NOT
// raw spans (the MV-bypass invariant).
//
// v0.8.507 — batched: buildAllSeasonalQuery replaced the per-service
// seasonalBaselineSQL. The service filter is GONE (one GROUP BY service_name
// pass covers all services); the slot/class/radius binds are sweep constants
// so the bind count drops from six to five.
func TestSeasonalBaselineSQLShape(t *testing.T) {
	sql := buildAllSeasonalQuery("countMerge(span_count_state) / 300.0")

	mustContain := map[string]string{
		"MV read (not raw spans)": "service_summary_5m",
		"time-bounded WHERE":      "time_bucket >= ?",
		// v0.8.507 — batched over all services in one pass.
		"batch grouping": "GROUP BY service_name, t",
		// v0.8.323 — hour/weekday pinned to UTC on BOTH sides: the Go
		// slot/class comes from at.UTC(), so the SQL must resolve on the
		// same clock regardless of the CH server's default timezone. A
		// TZ delta (app Europe/Istanbul vs CH UTC) used to match the
		// wrong time-of-day slot — day-peak history against night "now".
		"three-way day class (UTC)": "multiIf(toDayOfWeek(time_bucket, 0, 'UTC') = 6, 'saturday', toDayOfWeek(time_bucket, 0, 'UTC') = 7, 'sunday', 'weekday')",
		"UTC hour grid":             "toHour(time_bucket, 'UTC')",
		"circular distance (near)":  "least(abs(",
		"circular wrap (far side)":  "86400 - abs(",
		"per-service row cap":       "LIMIT 700 BY service_name",
		"overall row cap":           "LIMIT 14000000",
		"execution-time bound":      "max_execution_time = 30",
	}
	for label, sub := range mustContain {
		if !strings.Contains(sql, sub) {
			t.Errorf("seasonal SQL missing %s: %q not found in\n%s", label, sub, sql)
		}
	}
	// MV-bypass invariant: must never scan raw spans.
	if strings.Contains(sql, "FROM spans") {
		t.Errorf("seasonal SQL must read the MV, not raw spans:\n%s", sql)
	}
	// v0.8.507 — the per-service filter is GONE; the batch reads all services.
	if strings.Contains(sql, "service_name = ?") {
		t.Errorf("batched seasonal SQL must NOT filter by a single service:\n%s", sql)
	}
	// Exactly five bind placeholders (cutoff, class, targetSod×2, radius).
	if n := strings.Count(sql, "?"); n != 5 {
		t.Errorf("batched seasonal SQL must have 5 bind placeholders, got %d", n)
	}
}

// TestSeasonalCircularDistance documents the midnight-wrap semantics the SQL's
// least(|d|, 86400-|d|) implements: a slot near midnight has its across-the-
// boundary neighbours (e.g. 00:00) counted as ONE bucket away, not a full day.
// Without the wrap, 23:55's neighbour window would clip at 23:59 and drop the
// 00:00–00:10 slots, re-introducing sample scarcity exactly at the boundary.
func TestSeasonalCircularDistance(t *testing.T) {
	// Mirror the SQL formula in Go for a correctness check (CH executes the
	// real one; this pins the intended math).
	circ := func(sod, target int) int {
		d := sod - target
		if d < 0 {
			d = -d
		}
		if w := 86400 - d; w < d {
			return w
		}
		return d
	}
	radius := seasonalNeighborBuckets * bucketSeconds // ±3 buckets = 900s
	// Centre 23:55 (86100s). 00:00 (next day, 0s) must be one bucket (300s) away.
	center := 23*3600 + 55*60
	if got := circ(0, center); got != 300 {
		t.Errorf("00:00 vs 23:55 circular distance = %ds, want 300s (1 bucket)", got)
	}
	// 00:10 (600s) is within the ±3-bucket window from 23:55; 00:20 (1200s) is not.
	if got := circ(600, center); got > radius {
		t.Errorf("00:10 should be inside the ±%ds window from 23:55, dist=%ds", radius, got)
	}
	if got := circ(1200, center); got <= radius {
		t.Errorf("00:20 should be OUTSIDE the ±%ds window from 23:55, dist=%ds", radius, got)
	}
	// Non-wrapping sanity: same-hour neighbours behave normally.
	noon := 12 * 3600
	if got := circ(noon+300, noon); got != 300 {
		t.Errorf("12:05 vs 12:00 distance = %ds, want 300s", got)
	}
}

// TestSlotSecondsOfDay pins the 5-min-aligned centre: seconds ARE ignored and
// the minute floors to the bucket grid, so the neighbour distance is a whole
// number of buckets.
func TestSlotSecondsOfDay(t *testing.T) {
	// 10:07:43 → aligned to 10:05 → 10*3600 + 5*60 = 36300s.
	at := time.Date(2026, 6, 1, 10, 7, 43, 0, time.UTC)
	if got := slotSecondsOfDay(at); got != 36300 {
		t.Errorf("slotSecondsOfDay(10:07:43) = %d, want 36300 (aligned to 10:05)", got)
	}
	// Exact midnight → 0.
	if got := slotSecondsOfDay(time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)); got != 0 {
		t.Errorf("slotSecondsOfDay(00:00) = %d, want 0", got)
	}
}

// TestSeasonalParams pins the operator-tunable knob resolution off the shared
// anomaly_promotion blob: valid values pass through, zero/absent and
// out-of-range fall back to the compile-time defaults so a bad (or partial,
// pre-v0.8.250) saved config can never push the CH read out of bounds.
func TestSeasonalParams(t *testing.T) {
	cases := []struct {
		name                     string
		in                       chstore.AnomalyPromotionConfig
		wantDays, wantMin, wantN int
	}{
		{"all-zero → defaults", chstore.AnomalyPromotionConfig{}, seasonalDays, seasonalMinSamples, seasonalNeighborBuckets},
		{"valid custom", chstore.AnomalyPromotionConfig{SeasonalDays: 21, SeasonalMinSamples: 6, SeasonalNeighborBuckets: 2}, 21, 6, 2},
		{"days too large → default", chstore.AnomalyPromotionConfig{SeasonalDays: 999, SeasonalMinSamples: 6, SeasonalNeighborBuckets: 2}, seasonalDays, 6, 2},
		{"negative → defaults", chstore.AnomalyPromotionConfig{SeasonalDays: -1, SeasonalMinSamples: -5, SeasonalNeighborBuckets: -3}, seasonalDays, seasonalMinSamples, seasonalNeighborBuckets},
		{"neighbor too large → default", chstore.AnomalyPromotionConfig{SeasonalDays: 14, SeasonalMinSamples: 4, SeasonalNeighborBuckets: 99}, 14, 4, seasonalNeighborBuckets},
		{"minSamples too large → default", chstore.AnomalyPromotionConfig{SeasonalDays: 14, SeasonalMinSamples: 9999, SeasonalNeighborBuckets: 3}, 14, seasonalMinSamples, 3},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			days, min, n := seasonalParams(c.in)
			if days != c.wantDays || min != c.wantMin || n != c.wantN {
				t.Errorf("seasonalParams(%+v) = (%d,%d,%d), want (%d,%d,%d)",
					c.in, days, min, n, c.wantDays, c.wantMin, c.wantN)
			}
		})
	}
}

func TestSeasonalBaseline_NoFalsePositiveOnDailyRamp(t *testing.T) {
	const current = 50.0 // a value that recurs at this slot every day

	// 24h-CONSECUTIVE baseline: mostly low off-peak/night traffic → low median,
	// tight spread → the recurring morning value looks like a huge spike.
	consecutive := make([]float64, 0, 280)
	for i := 0; i < 280; i++ {
		consecutive = append(consecutive, 8+float64(i%3)) // 8,9,10 …
	}
	cm, cmad := medianMAD(consecutive)
	if cmad < 1e-9 {
		t.Fatal("degenerate consecutive baseline")
	}
	consecZ := math.Abs(madScale * (current - cm) / cmad)
	if consecZ < openZ {
		t.Fatalf("precondition: flat-24h baseline should make the daily ramp look anomalous; z=%.2f", consecZ)
	}

	// SEASONAL baseline: the SAME morning slot over the last 14 days, all ~50.
	seasonal := []float64{48, 50, 52, 49, 51, 50, 48}
	if got := chooseBaseline(seasonal, consecutive, seasonalMinSamples); &got[0] != &seasonal[0] {
		t.Fatal("chooseBaseline should select the seasonal samples here")
	}
	sm, smad := medianMAD(seasonal)
	seasZ := math.Abs(madScale * (current - sm) / smad)
	if seasZ >= openZ {
		t.Errorf("seasonal baseline must NOT fire on the daily ramp; z=%.2f (median=%.1f mad=%.1f)", seasZ, sm, smad)
	}
}
