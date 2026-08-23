package chstore

import (
	"strings"
	"testing"
	"time"
)

// service_seen_test.go — v0.9.1317, entity-model slice A2.
//
// What these pin, and why it is worth a test file:
//
// The service_seen MV populates forward only, so on the release that
// introduces it EVERY service's first_seen is really just "when we
// deployed this feature". The read side refuses to report those as births
// (FirstSeenIsKnown), and the api layer emits no date at all when the
// answer is unknown — so the UI has nothing to render and cannot invent
// one.
//
// That honest branch is one boolean deep. A later refactor that
// "simplifies" it — dropping the grace window, treating a zero floor as
// permissive, comparing with >= instead of > — would silently restore the
// fabricated dates, and nothing else in the build would go red. These
// tests are that alarm.

func TestFirstSeenIsKnown(t *testing.T) {
	// Fixed clock: the tests are about ORDERING against the floor, so a
	// wall-clock read would only add flake.
	floor := time.Date(2026, 8, 23, 12, 0, 0, 0, time.UTC)
	const grace = ServiceSeenGrace

	tests := []struct {
		name      string
		firstSeen time.Time
		floor     time.Time
		grace     time.Duration
		want      bool
		why       string
	}{
		{
			name: "service born long after the MV started — the only true case",
			// The MV watched this one appear: it was absent for days,
			// then showed up. This is the answer the feature exists for.
			firstSeen: floor.Add(72 * time.Hour),
			floor:     floor, grace: grace, want: true,
			why: "genuinely new services must still be reported as new",
		},
		{
			name:      "first_seen exactly at the floor — the upgrade-day shape",
			firstSeen: floor,
			floor:     floor, grace: grace, want: false,
			why: "this is the service that defined the floor; it was already running",
		},
		{
			name:      "first_seen inside the grace smear",
			firstSeen: floor.Add(grace / 2),
			floor:     floor, grace: grace, want: false,
			why: "async_insert batching + rolling deploy spread the already-running fleet past the floor",
		},
		{
			name:      "first_seen exactly at floor+grace — boundary is EXCLUSIVE",
			firstSeen: floor.Add(grace),
			floor:     floor, grace: grace, want: false,
			why: "the boundary belongs to the uncertain side; > not >=",
		},
		{
			name:      "first_seen one nanosecond past floor+grace",
			firstSeen: floor.Add(grace + 1),
			floor:     floor, grace: grace, want: true,
			why: "the first instant we are willing to call a birth",
		},
		{
			name:      "first_seen BEFORE the floor (cross-shard clock skew)",
			firstSeen: floor.Add(-time.Hour),
			floor:     floor, grace: grace, want: false,
			why: "older than everything we observed cannot be something we observed",
		},
		{
			name:      "zero first_seen",
			firstSeen: time.Time{},
			floor:     floor, grace: grace, want: false,
			why: "absence of a timestamp is not a date",
		},
		{
			name:      "zero floor — MV empty, nothing is provable",
			firstSeen: floor.Add(72 * time.Hour),
			floor:     time.Time{}, grace: grace, want: false,
			why: "an unknown floor must close the gate, not open it",
		},
		{
			name:      "both zero",
			firstSeen: time.Time{},
			floor:     time.Time{}, grace: grace, want: false,
			why: "no inputs, no claim",
		},
		{
			name:      "zero grace still rejects first_seen AT the floor",
			firstSeen: floor,
			floor:     floor, grace: 0, want: false,
			why: "even with the smear window removed the floor row itself is censored",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := FirstSeenIsKnown(tc.firstSeen, tc.floor, tc.grace)
			if got != tc.want {
				t.Errorf("FirstSeenIsKnown(%v, %v, %v) = %v, want %v — %s",
					tc.firstSeen, tc.floor, tc.grace, got, tc.want, tc.why)
			}
		})
	}
}

func TestServiceSeenFloor(t *testing.T) {
	base := time.Date(2026, 8, 23, 12, 0, 0, 0, time.UTC)

	tests := []struct {
		name string
		in   map[string]ServiceSeen
		want time.Time
		why  string
	}{
		{
			name: "earliest first_seen across the fleet wins",
			in: map[string]ServiceSeen{
				"api":     {FirstSeen: base.Add(2 * time.Hour), LastSeen: base.Add(9 * time.Hour)},
				"billing": {FirstSeen: base, LastSeen: base.Add(9 * time.Hour)},
				"worker":  {FirstSeen: base.Add(5 * time.Hour), LastSeen: base.Add(9 * time.Hour)},
			},
			want: base,
			why:  "the floor is the MV's own birth, i.e. the earliest thing it ever saw",
		},
		{
			name: "zero first_seen entries are skipped, not treated as the minimum",
			in: map[string]ServiceSeen{
				"ghost": {FirstSeen: time.Time{}, LastSeen: base},
				"api":   {FirstSeen: base.Add(2 * time.Hour), LastSeen: base.Add(9 * time.Hour)},
			},
			want: base.Add(2 * time.Hour),
			why:  "a zero would pin the floor to year 1 and make EVERY service look new",
		},
		{
			name: "empty map",
			in:   map[string]ServiceSeen{},
			want: time.Time{},
			why:  "no rows, no floor",
		},
		{
			name: "nil map",
			in:   nil,
			want: time.Time{},
			why:  "must not panic on the cold-cache shape",
		},
		{
			name: "every entry zero",
			in: map[string]ServiceSeen{
				"a": {FirstSeen: time.Time{}},
				"b": {FirstSeen: time.Time{}},
			},
			want: time.Time{},
			why:  "still no provable floor",
		},
		{
			name: "single service",
			in:   map[string]ServiceSeen{"only": {FirstSeen: base, LastSeen: base}},
			want: base,
			why:  "one row is a valid floor",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := ServiceSeenFloor(tc.in)
			if !got.Equal(tc.want) {
				t.Errorf("ServiceSeenFloor() = %v, want %v — %s", got, tc.want, tc.why)
			}
		})
	}
}

// TestServiceSeenFloorThenKnown — the two pure functions composed the way
// the api layer composes them, on the shape that matters most: the upgrade
// where the MV has just been created and the whole fleet appears at once.
//
// Every service must read UNKNOWN here. This is the assertion that would
// fire if someone reintroduced a backfill or relaxed the gate: the day-one
// snapshot must not produce a single birth date.
func TestServiceSeenFloorThenKnown(t *testing.T) {
	base := time.Date(2026, 8, 23, 12, 0, 0, 0, time.UTC)

	// The fleet as the MV first sees it: everyone arrives within the
	// first couple of minutes, because that is when the first async_insert
	// batches flush, not because anyone was born then.
	snapshot := map[string]ServiceSeen{
		"api":      {FirstSeen: base, LastSeen: base.Add(time.Hour)},
		"billing":  {FirstSeen: base.Add(30 * time.Second), LastSeen: base.Add(time.Hour)},
		"worker":   {FirstSeen: base.Add(2 * time.Minute), LastSeen: base.Add(time.Hour)},
		"reporter": {FirstSeen: base.Add(4 * time.Minute), LastSeen: base.Add(time.Hour)},
	}
	floor := ServiceSeenFloor(snapshot)
	if !floor.Equal(base) {
		t.Fatalf("floor = %v, want %v", floor, base)
	}
	for name, sv := range snapshot {
		if FirstSeenIsKnown(sv.FirstSeen, floor, ServiceSeenGrace) {
			t.Errorf("%s: reported a birth date on the MV's first day — "+
				"this is exactly the fabricated first_seen the design forbids", name)
		}
	}

	// A service that genuinely appears later must still be detectable, or
	// the gate is just "always unknown" and the feature is dead weight.
	snapshot["newcomer"] = ServiceSeen{FirstSeen: base.Add(48 * time.Hour), LastSeen: base.Add(49 * time.Hour)}
	if floor := ServiceSeenFloor(snapshot); !floor.Equal(base) {
		t.Fatalf("floor moved after a later arrival: %v", floor)
	}
	if !FirstSeenIsKnown(snapshot["newcomer"].FirstSeen, base, ServiceSeenGrace) {
		t.Error("a service first seen 48h after the floor must be reported as genuinely new")
	}
}

// TestServiceSeenSQLBounds pins the query's cost guards and the states it
// reads. The MV is small BY CONSTRUCTION (one row per service), and that
// is the whole justification for a whole-table read with no time filter —
// so if someone later points this SQL at a time-bucketed table, the
// missing WHERE stops being safe. Pin the shape that makes it safe.
func TestServiceSeenSQLBounds(t *testing.T) {
	for _, want := range []string{
		"FROM service_seen",
		"minMerge(first_seen_state)",
		"maxMerge(last_seen_state)",
		"GROUP BY service_name",
		"LIMIT",
		"max_execution_time",
	} {
		if !strings.Contains(serviceSeenSQL, want) {
			t.Errorf("serviceSeenSQL missing %q:\n%s", want, serviceSeenSQL)
		}
	}
	// The read must NOT filter by time. A `WHERE time >= …` here would
	// drop precisely the disappeared services the table exists to keep.
	if strings.Contains(serviceSeenSQL, "WHERE") {
		t.Errorf("serviceSeenSQL grew a WHERE clause — a time filter here "+
			"silently deletes the disappeared services this MV exists to remember:\n%s",
			serviceSeenSQL)
	}
}
