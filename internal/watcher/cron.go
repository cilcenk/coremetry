package watcher

// Quartz calendar cron — ES Watcher import Faz-2 (v0.9.x). Faz-1
// mapped only the FIXED-RATE cron subset onto an interval
// (cronIntervalSec, "cron→interval eşlendi" — that mapping is kept
// verbatim for geriye uyum); everything calendar-shaped ("0 0 8 * *
// ?", "0 30 9 ? * MON-FRI") fell back to continuous 5m evaluation.
// This file adds a minimal Quartz next-fire calculator so calendar
// watches run ON THEIR OWN SCHEDULE: the evaluator's due-check
// becomes "has a scheduled fire time passed since the last run".
//
// Supported per field (sec min hour dom mon dow [year]):
//
//	*        any value
//	?        no specific value (dom/dow only, Quartz rule)
//	N        fixed value (names: JAN..DEC for month, SUN..SAT for dow;
//	         Quartz numeric dow is 1=SUN..7=SAT)
//	a,b,c    list (elements may be values or ranges)
//	a-b      range (inclusive; reversed ranges rejected)
//	*/n a/n a-b/n  steps
//
// NOT supported (ParseCron errors → the watch stays on the Faz-1
// continuous-5m fallback, reported Unsupported): the calendar special
// characters L, W and # — their semantics (last day, nearest weekday,
// nth weekday) are not worth modelling for the watches seen in prod —
// and schedules constraining BOTH day-of-month and day-of-week
// (Quartz itself rejects those).
//
// Times are evaluated in UTC — ES Watcher parity: a watcher cron runs
// UTC unless trigger.schedule carries a timezone, which this package
// does not model.
//
// Pure file: no store, no clock — NextFire takes `after` explicitly.

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

// cronField is one parsed field: either "any" (* or ?) or an explicit
// allowed-value set.
type cronField struct {
	any bool
	set map[int]bool
}

func (f cronField) match(v int) bool { return f.any || f.set[v] }

// CronSchedule is a parsed Quartz expression in the supported subset.
type CronSchedule struct {
	sec, min, hour, dom, mon, dow, year cronField
	// domAny / dowAny remember whether the field was * or ? — needed
	// because Quartz treats an unconstrained day field as "no
	// constraint" while a constrained one gates the day.
	domAny, dowAny bool
}

var monthNames = map[string]int{
	"JAN": 1, "FEB": 2, "MAR": 3, "APR": 4, "MAY": 5, "JUN": 6,
	"JUL": 7, "AUG": 8, "SEP": 9, "OCT": 10, "NOV": 11, "DEC": 12,
}

// Quartz day-of-week numbering: 1=SUN .. 7=SAT (NOT the Unix 0-6).
var dowNames = map[string]int{
	"SUN": 1, "MON": 2, "TUE": 3, "WED": 4, "THU": 5, "FRI": 6, "SAT": 7,
}

// ParseCron validates a Quartz cron expression against the supported
// subset and returns the parsed schedule. Errors carry the reason the
// Validate report shows the operator.
func ParseCron(expr string) (*CronSchedule, error) {
	fields := strings.Fields(strings.TrimSpace(expr))
	if len(fields) != 6 && len(fields) != 7 {
		return nil, fmt.Errorf("quartz cron needs 6 or 7 fields (sec min hour dom mon dow [year]), got %d in %q", len(fields), expr)
	}
	// v0.9.202 review-fix — özel-karakter reddi ALAN-BAZLI: Quartz'ta L/W
	// yalnız dom'da, L/# yalnız dow'da anlamlıdır. Ham tüm-alan ContainsAny
	// taraması BÜYÜK HARF ad'ları yanlış reddediyordu: WED 'W' içerir (dow),
	// JUL 'L' içerir (mon) — "0 0 8 ? * WED" desteklenen alt-kümede olduğu
	// halde L/W/# gerekçesiyle düşüyordu. mon alanı ad alabilir ama özel
	// karakter alamaz; dow adlarının hiçbiri L/# içermez.
	if strings.ContainsAny(fields[3], "LW") {
		return nil, fmt.Errorf("cron special characters L/W are not supported in day-of-month (field %q)", fields[3])
	}
	if strings.ContainsAny(fields[5], "L#") {
		return nil, fmt.Errorf("cron special characters L/# are not supported in day-of-week (field %q)", fields[5])
	}
	cs := &CronSchedule{}
	specs := []struct {
		dst      *cronField
		src      string
		min, max int
		names    map[string]int
		allowQ   bool
	}{
		{&cs.sec, fields[0], 0, 59, nil, false},
		{&cs.min, fields[1], 0, 59, nil, false},
		{&cs.hour, fields[2], 0, 23, nil, false},
		{&cs.dom, fields[3], 1, 31, nil, true},
		{&cs.mon, fields[4], 1, 12, monthNames, false},
		{&cs.dow, fields[5], 1, 7, dowNames, true},
	}
	for _, sp := range specs {
		f, err := parseCronField(sp.src, sp.min, sp.max, sp.names, sp.allowQ)
		if err != nil {
			return nil, err
		}
		*sp.dst = f
	}
	cs.year = cronField{any: true}
	if len(fields) == 7 {
		f, err := parseCronField(fields[6], 1970, 2199, nil, true)
		if err != nil {
			return nil, err
		}
		cs.year = f
	}
	cs.domAny, cs.dowAny = cs.dom.any, cs.dow.any
	if !cs.domAny && !cs.dowAny {
		return nil, fmt.Errorf("day-of-month and day-of-week cannot both be constrained (Quartz rule) — use ? for one of them in %q", expr)
	}
	return cs, nil
}

// parseCronField parses one field: * ? N a-b a/n a-b/n */n and
// comma-lists of value/range elements.
func parseCronField(s string, lo, hi int, names map[string]int, allowQ bool) (cronField, error) {
	if s == "*" {
		return cronField{any: true}, nil
	}
	if s == "?" {
		if !allowQ {
			return cronField{}, fmt.Errorf("? is only valid in the day-of-month / day-of-week fields (got %q)", s)
		}
		return cronField{any: true}, nil
	}
	set := map[int]bool{}
	for _, part := range strings.Split(s, ",") {
		rangePart, stepPart, hasStep := strings.Cut(part, "/")
		step := 1
		if hasStep {
			n, err := strconv.Atoi(stepPart)
			if err != nil || n <= 0 {
				return cronField{}, fmt.Errorf("bad cron step %q", part)
			}
			step = n
		}
		from, to := lo, hi
		switch {
		case rangePart == "*":
			if !hasStep {
				return cronField{}, fmt.Errorf("bad cron element %q", part)
			}
		case strings.Contains(rangePart, "-"):
			a, b, _ := strings.Cut(rangePart, "-")
			av, err := cronValue(a, lo, hi, names)
			if err != nil {
				return cronField{}, err
			}
			bv, err := cronValue(b, lo, hi, names)
			if err != nil {
				return cronField{}, err
			}
			if av > bv {
				return cronField{}, fmt.Errorf("reversed cron range %q", part)
			}
			from, to = av, bv
		default:
			v, err := cronValue(rangePart, lo, hi, names)
			if err != nil {
				return cronField{}, err
			}
			if !hasStep {
				set[v] = true
				continue
			}
			from = v // a/n = from a to field max, every n
		}
		for v := from; v <= to; v += step {
			set[v] = true
		}
	}
	if len(set) == 0 {
		return cronField{}, fmt.Errorf("empty cron field %q", s)
	}
	return cronField{set: set}, nil
}

// cronValue resolves one literal: a number in [lo,hi] or a name from
// the field's name table (case-insensitive).
func cronValue(s string, lo, hi int, names map[string]int) (int, error) {
	if names != nil {
		if v, ok := names[strings.ToUpper(s)]; ok {
			return v, nil
		}
	}
	v, err := strconv.Atoi(s)
	if err != nil {
		return 0, fmt.Errorf("bad cron value %q", s)
	}
	if v < lo || v > hi {
		return 0, fmt.Errorf("cron value %d out of range [%d,%d]", v, lo, hi)
	}
	return v, nil
}

// dayMatch applies the Quartz day gate: whichever of dom/dow is
// constrained must match (ParseCron guarantees at most one is).
func (c *CronSchedule) dayMatch(t time.Time) bool {
	if !c.domAny && !c.dom.match(t.Day()) {
		return false
	}
	if !c.dowAny && !c.dow.match(int(t.Weekday())+1) { // Go Sunday=0 → Quartz 1
		return false
	}
	return true
}

// nextFireSearchYears bounds the field-advance loop: a valid schedule
// that cannot fire within this horizon (exhausted year constraint,
// Feb 30-style impossible dates) errors instead of spinning.
const nextFireSearchYears = 5

// Next returns the first fire time strictly after `after` (UTC), or
// the zero time when none exists within the search horizon.
func (c *CronSchedule) Next(after time.Time) time.Time {
	t := after.UTC().Truncate(time.Second).Add(time.Second)
	limit := t.AddDate(nextFireSearchYears, 0, 0)
	for t.Before(limit) {
		switch {
		case !c.year.match(t.Year()):
			t = time.Date(t.Year()+1, 1, 1, 0, 0, 0, 0, time.UTC)
		case !c.mon.match(int(t.Month())):
			t = time.Date(t.Year(), t.Month()+1, 1, 0, 0, 0, 0, time.UTC)
		case !c.dayMatch(t):
			t = time.Date(t.Year(), t.Month(), t.Day()+1, 0, 0, 0, 0, time.UTC)
		case !c.hour.match(t.Hour()):
			t = time.Date(t.Year(), t.Month(), t.Day(), t.Hour()+1, 0, 0, 0, time.UTC)
		case !c.min.match(t.Minute()):
			t = time.Date(t.Year(), t.Month(), t.Day(), t.Hour(), t.Minute()+1, 0, 0, time.UTC)
		case !c.sec.match(t.Second()):
			t = t.Add(time.Second)
		default:
			return t
		}
	}
	return time.Time{}
}

// NextFire parses expr and returns the next fire time strictly after
// `after`. Unparseable expressions and schedules with no future fire
// time (e.g. an exhausted year constraint) error.
func NextFire(expr string, after time.Time) (time.Time, error) {
	cs, err := ParseCron(expr)
	if err != nil {
		return time.Time{}, err
	}
	t := cs.Next(after)
	if t.IsZero() {
		return time.Time{}, fmt.Errorf("cron %q has no fire time within %d years after %s", expr, nextFireSearchYears, after.UTC().Format(time.RFC3339))
	}
	return t, nil
}

// parseableCrons reports whether every expression is in the ParseCron
// subset — the Validate gate for the calendar-cron Supported verdict.
func parseableCrons(exprs []string) bool {
	if len(exprs) == 0 {
		return false
	}
	for _, e := range exprs {
		if _, err := ParseCron(e); err != nil {
			return false
		}
	}
	return true
}

// CalendarCron returns the schedule's cron expressions when the watch
// paces via CALENDAR cron: cron-only schedule (an interval, parseable
// or not, keeps the Faz-1 interval path — mirrors IntervalSec
// priority), NOT the fixed-rate cron→interval subset (geriye uyum:
// those watches keep their Faz-1 pacing and report message), and
// every expression parseable by ParseCron. nil otherwise — the caller
// falls back to interval/default pacing.
func (w *Watch) CalendarCron() []string {
	if w == nil || w.Trigger == nil || w.Trigger.Schedule == nil {
		return nil
	}
	s := w.Trigger.Schedule
	if s.Interval != "" || len(s.Cron) == 0 {
		return nil
	}
	// v0.9.202 review-fix — schedule bir TIMEZONE taşıyorsa (ES 8.13+
	// trigger.schedule.timezone; UnmarshalJSON Other'a düşürür) takvim
	// yoluna GİRME: cron'u UTC'de koşturmak ES'in tz-aware ateşlemesinden
	// saatlerce kayardı. Faz-1 fallback'inde kalır; Validate doğru raporlar.
	if s.HasTimezone() {
		return nil
	}
	if cronIntervalSec(s.Cron) > 0 {
		return nil // fixed-rate subset stays on the interval path
	}
	for _, e := range s.Cron {
		if _, err := ParseCron(e); err != nil {
			return nil
		}
		// v0.9.202 review-fix — saniye alanı "0" olmayan takvim cron'u
		// ("15/20 * * * * ?") 1dk evaluator tick'iyle temsil edilemez;
		// takvim yoluna alma (Faz-1 5m default'una düşer, Validate söyler).
		if f := strings.Fields(strings.TrimSpace(e)); len(f) > 0 && f[0] != "0" && f[0] != "00" {
			return nil
		}
	}
	return s.Cron
}

// cronSecondsZero — her ifadenin saniye alanı sabit 0 mı (takvim yolunun
// ön koşulu; 1dk tick saniye-bazlı ateşlemeyi temsil edemez). CalendarCron
// ve Validate aynı kapıyı paylaşır (v0.9.202).
func cronSecondsZero(exprs []string) bool {
	for _, e := range exprs {
		f := strings.Fields(strings.TrimSpace(e))
		if len(f) == 0 || (f[0] != "0" && f[0] != "00") {
			return false
		}
	}
	return true
}
