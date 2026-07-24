// Quartz calendar-cron tests — ES Watcher import Faz-2 (v0.9.x).
// Contract: ParseCron accepts the minimal Quartz subset real prod
// watches schedule with — per field (sec min hour dom mon dow [year]):
// `*`, `?`, fixed values, lists (a,b), ranges (a-b), steps (*/n, a/n,
// a-b/n), day-of-week / month NAMES (MON..SUN, JAN..DEC), Quartz DOW
// numbering (1=SUN..7=SAT) — and REJECTS the special characters with
// calendar semantics we don't model (L, W, #), both dom+dow
// constrained, and anything non-Quartz-shaped. NextFire computes the
// next fire time strictly after `after`, in UTC (ES Watcher parity —
// watcher crons run UTC unless a timezone is set, which we don't
// model). The fixed-rate cron→interval subset (cronIntervalSec,
// Faz-1) KEEPS its interval mapping — ParseCron covering it too must
// be behaviourally equivalent (successive fires exactly N apart).
package watcher

import (
	"testing"
	"time"
)

func TestParseCronSupportedForms(t *testing.T) {
	valid := []string{
		"0 0 8 * * ?",          // daily at 08:00
		"0 30 9 ? * MON-FRI",   // weekdays 09:30, dow name range
		"0 0/10 * * * ?",       // fixed-rate subset stays parseable
		"0 */5 * * * ?",        // */n step
		"0 0 12 1 * ?",         // 1st of the month, noon
		// v0.9.202 review-fix pinleri — BÜYÜK HARF adlar özel-karakter
		// taramasına takılmaz: WED 'W' içerir, JUL 'L' içerir; eski
		// tüm-alan ContainsAny bunları yanlış reddediyordu.
		"0 0 8 ? * WED",         // uppercase WED (contains W)
		"0 0 8 ? * MON,WED,FRI", // list with WED
		"0 0 8 ? * MON-WED",     // range ending at WED
		"0 0 12 1 JUL ?",        // uppercase JUL (contains L)
		"0 0 12 ? JUL-SEP TUE",  // month range with JUL
		"0 0 8,20 * * ?",       // value list
		"0 0 8-10 * * ?",       // range
		"0 0 8 * JAN ?",        // month name
		"0 0 8 * jan-mar ?",    // month name range, case-insensitive
		"0 0 8 ? * 2",          // Quartz numeric DOW (2 = MON)
		"0 0 8 ? * SUN,SAT",    // dow name list
		"0 0 8 * * ? *",        // 7-field, wildcard year
		"0 0 8 * * ? 2027",     // constrained year
		"0 0 8 * * ? 2026-2028", // year range
		"0 15/15 * * * ?",      // a/n step (15,30,45)
		"0 0-30/10 * * * ?",    // a-b/n step
		"30 0 8 * * ?",         // fixed seconds
	}
	for _, expr := range valid {
		if _, err := ParseCron(expr); err != nil {
			t.Errorf("ParseCron(%q) = %v, want ok", expr, err)
		}
	}
}

func TestParseCronErrors(t *testing.T) {
	invalid := []struct {
		name string
		expr string
	}{
		{"empty", ""},
		{"5 fields is not Quartz", "0 8 * * ?"},
		{"8 fields", "0 0 8 * * ? 2027 junk"},
		{"L day-of-month", "0 0 8 L * ?"},
		{"W nearest weekday", "0 0 8 15W * ?"},
		{"# nth weekday", "0 0 8 ? * 6#3"},
		{"minute out of range", "0 60 * * * ?"},
		{"hour out of range", "0 0 24 * * ?"},
		{"dom zero", "0 0 8 0 * ?"},
		{"month out of range", "0 0 8 * 13 ?"},
		{"dow zero (Quartz is 1=SUN..7=SAT)", "0 0 8 ? * 0"},
		{"unknown name", "0 0 8 ? * MONDAY"},
		{"reversed range", "0 0 10-8 * * ?"},
		{"degenerate step", "0 0/0 * * * ?"},
		{"garbage token", "0 x * * * ?"},
		{"both dom and dow constrained", "0 0 8 1 * MON"},
		{"question mark outside dom/dow", "0 ? * * * ?"},
	}
	for _, tt := range invalid {
		if _, err := ParseCron(tt.expr); err == nil {
			t.Errorf("%s: ParseCron(%q) = ok, want error", tt.name, tt.expr)
		}
	}
}

func TestNextFire(t *testing.T) {
	at := func(y int, mo time.Month, d, h, mi, s int) time.Time {
		return time.Date(y, mo, d, h, mi, s, 0, time.UTC)
	}
	tests := []struct {
		name  string
		expr  string
		after time.Time
		want  time.Time
	}{
		{"daily 08:00, morning before", "0 0 8 * * ?", at(2026, 7, 24, 7, 0, 0), at(2026, 7, 24, 8, 0, 0)},
		{"daily 08:00, exactly at fire → next day (strictly after)", "0 0 8 * * ?", at(2026, 7, 24, 8, 0, 0), at(2026, 7, 25, 8, 0, 0)},
		{"daily 08:00, after → next day", "0 0 8 * * ?", at(2026, 7, 24, 9, 30, 0), at(2026, 7, 25, 8, 0, 0)},
		// 2026-07-24 is a Friday; the next MON-FRI 09:30 after Friday
		// 10:00 is Monday 2026-07-27.
		{"weekday 09:30 skips the weekend", "0 30 9 ? * MON-FRI", at(2026, 7, 24, 10, 0, 0), at(2026, 7, 27, 9, 30, 0)},
		{"weekday 09:30 same day when early", "0 30 9 ? * MON-FRI", at(2026, 7, 24, 6, 0, 0), at(2026, 7, 24, 9, 30, 0)},
		{"every 10 minutes", "0 0/10 * * * ?", at(2026, 7, 24, 12, 3, 0), at(2026, 7, 24, 12, 10, 0)},
		{"every 10 minutes on the boundary → next slot", "0 0/10 * * * ?", at(2026, 7, 24, 12, 10, 0), at(2026, 7, 24, 12, 20, 0)},
		{"1st of month noon", "0 0 12 1 * ?", at(2026, 7, 24, 0, 0, 0), at(2026, 8, 1, 12, 0, 0)},
		{"hour list picks the evening slot", "0 0 8,20 * * ?", at(2026, 7, 24, 9, 0, 0), at(2026, 7, 24, 20, 0, 0)},
		{"quartz numeric DOW 2 = Monday", "0 0 8 ? * 2", at(2026, 7, 24, 12, 0, 0), at(2026, 7, 27, 8, 0, 0)},
		{"year-constrained fires next year", "0 0 0 1 1 ? 2027", at(2026, 7, 24, 12, 0, 0), at(2027, 1, 1, 0, 0, 0)},
		{"month rollover across year end", "0 0 12 1 * ?", at(2026, 12, 15, 0, 0, 0), at(2027, 1, 1, 12, 0, 0)},
		{"seconds field honoured", "30 0 8 * * ?", at(2026, 7, 24, 8, 0, 0), at(2026, 7, 24, 8, 0, 30)},
		{"non-UTC after is evaluated in UTC", "0 0 8 * * ?",
			time.Date(2026, 7, 24, 9, 0, 0, 0, time.FixedZone("UTC+3", 3*3600)), // = 06:00 UTC
			at(2026, 7, 24, 8, 0, 0)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := NextFire(tt.expr, tt.after)
			if err != nil {
				t.Fatalf("NextFire(%q, %s): %v", tt.expr, tt.after, err)
			}
			if !got.Equal(tt.want) {
				t.Fatalf("NextFire(%q, %s) = %s, want %s", tt.expr, tt.after, got, tt.want)
			}
		})
	}
}

func TestNextFireErrors(t *testing.T) {
	if _, err := NextFire("0 0 8 L * ?", time.Now()); err == nil {
		t.Fatal("unparseable expression must error")
	}
	// Year window entirely in the past: no fire time exists.
	if _, err := NextFire("0 0 0 1 1 ? 2020", time.Date(2026, 7, 24, 0, 0, 0, 0, time.UTC)); err == nil {
		t.Fatal("exhausted year constraint must error, not loop or return zero silently")
	}
}

// The Faz-1 fixed-rate subset (cron→interval eşlendi) must stay
// behaviourally equivalent under NextFire: successive fires exactly
// cronIntervalSec apart.
func TestNextFireConsistentWithFixedRateSubset(t *testing.T) {
	for _, expr := range []string{"0 0/10 * * * ?", "0 0 * * * ?", "0 0 0/2 * * ?"} {
		wantStep := time.Duration(cronIntervalSec([]string{expr})) * time.Second
		if wantStep == 0 {
			t.Fatalf("fixture %q must be in the fixed-rate subset", expr)
		}
		t.Run(expr, func(t *testing.T) {
			// Start from a fire boundary so steps are exact.
			cur, err := NextFire(expr, time.Date(2026, 7, 24, 0, 0, 0, 0, time.UTC))
			if err != nil {
				t.Fatalf("NextFire: %v", err)
			}
			for i := 0; i < 5; i++ {
				next, err := NextFire(expr, cur)
				if err != nil {
					t.Fatalf("NextFire: %v", err)
				}
				if got := next.Sub(cur); got != wantStep {
					t.Fatalf("fire %d → %d step = %s, want %s", i, i+1, got, wantStep)
				}
				cur = next
			}
		})
	}
}

// CalendarCron gates the evaluator's cron pacing: only a schedule
// that is cron-only (no interval), NOT the fixed-rate subset (that
// keeps its Faz-1 interval mapping — geriye uyum), and fully
// parseable counts as calendar cron.
func TestCalendarCron(t *testing.T) {
	mk := func(schedule string) *Watch {
		w, err := Parse([]byte(`{"trigger": {"schedule": ` + schedule + `}, "input": {"search": {"request": {"body": {"query": {"match_all": {}}}}}}}`))
		if err != nil {
			t.Fatalf("Parse: %v", err)
		}
		return w
	}
	tests := []struct {
		name     string
		schedule string
		want     int // number of expressions returned
	}{
		{"daily calendar cron", `{"cron": "0 0 8 * * ?"}`, 1},
		{"weekday calendar cron", `{"cron": "0 30 9 ? * MON-FRI"}`, 1},
		{"multiple calendar crons", `{"cron": ["0 0 8 * * ?", "0 0 20 * * ?"]}`, 2},
		{"fixed-rate subset stays on the interval path", `{"cron": "0 0/10 * * * ?"}`, 0},
		{"interval schedule has no cron", `{"interval": "5m"}`, 0},
		{"interval wins over cron (IntervalSec priority)", `{"interval": "5m", "cron": "0 0 8 * * ?"}`, 0},
		{"unparseable cron (L) → not calendar", `{"cron": "0 0 8 L * ?"}`, 0},
		{"mixed parseable + unparseable → not calendar", `{"cron": ["0 0 8 * * ?", "0 0 8 L * ?"]}`, 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := mk(tt.schedule).CalendarCron(); len(got) != tt.want {
				t.Fatalf("CalendarCron() = %v, want %d expression(s)", got, tt.want)
			}
		})
	}
	var nilWatch *Watch
	if got := nilWatch.CalendarCron(); got != nil {
		t.Fatalf("nil watch CalendarCron() = %v, want nil", got)
	}
}
