package chstore

import "testing"

// v0.6.36 — regression guard for the silent-data-drain bug.
//
// Operator-reported: setting retention.spans = "1h" via the admin UI
// caused every span ingested after 01:00 of the day to be TTL'd on
// the next merge cycle, because the previous TTL template
// `toDate(time) + INTERVAL %s` expanded to `toDate(time) + INTERVAL 1
// HOUR` = midnight + 1h = 01:00 of the SAME day, not "1 hour after
// the row was written". Symptoms were inconsistent trace counts
// across /traces time windows (counts changed merge-to-merge as parts
// rolled through cleanup).
//
// These tests pin the two semantically distinct shapes:
//
//   - "Nd"   → toDate(<col>) + INTERVAL N DAY   (partition-aligned)
//   - "Nh"   → <col> + INTERVAL N HOUR          (row-level)
//
// The 1h case is the smoking gun. Any future refactor that
// reintroduces `toDate(...) + INTERVAL N HOUR` will fail here.
func TestBuildRetentionTTL(t *testing.T) {
	cases := []struct {
		name string
		val  string
		col  string
		want string
	}{
		{"spans 30 days", "30d", "time", "toDate(time) + INTERVAL 30 DAY"},
		{"logs 90 days", "90d", "time", "toDate(time) + INTERVAL 90 DAY"},
		{"profiles uses start_time", "7d", "start_time", "toDate(start_time) + INTERVAL 7 DAY"},

		// The original bug: hour-granularity must NOT be wrapped in
		// toDate() — that pins the TTL to a clock time on the same day
		// instead of a rolling N-hour window from the row's timestamp.
		//
		// v0.6.37 — also must wrap in toDateTime() because `time` is
		// DateTime64(9) and CH error 450 rejects nanosecond-precision
		// TTL expressions ("result column should have DateTime or
		// Date type").
		{"spans 1 hour", "1h", "time", "toDateTime(time) + INTERVAL 1 HOUR"},
		{"spans 48 hours", "48h", "time", "toDateTime(time) + INTERVAL 48 HOUR"},
		{"profiles 6 hours", "6h", "start_time", "toDateTime(start_time) + INTERVAL 6 HOUR"},

		// Case-insensitivity + whitespace trim — these were supported
		// by the old parseRetention and the contract still holds.
		{"uppercase D", "30D", "time", "toDate(time) + INTERVAL 30 DAY"},
		{"leading space", "  14d", "time", "toDate(time) + INTERVAL 14 DAY"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := buildRetentionTTL(c.val, c.col)
			if err != nil {
				t.Fatalf("buildRetentionTTL(%q, %q) returned error: %v", c.val, c.col, err)
			}
			if got != c.want {
				t.Errorf("buildRetentionTTL(%q, %q) = %q; want %q", c.val, c.col, got, c.want)
			}
		})
	}
}

func TestBuildRetentionTTL_Reject(t *testing.T) {
	bad := []string{"", "1", "1m", "0h", "-1d", "1week", "abc", "1.5h", "1 h"}
	for _, s := range bad {
		if _, err := buildRetentionTTL(s, "time"); err == nil {
			t.Errorf("buildRetentionTTL(%q) accepted; want error", s)
		}
	}
}
