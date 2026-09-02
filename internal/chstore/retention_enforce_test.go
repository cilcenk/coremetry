package chstore

import "testing"

// v0.5.326 — locks the retention horizon parser + humanBytes
// formatter introduced by v0.5.320. Bad horizon parsing here
// would silently drop the wrong partitions; bad byte formatting
// would garble the disk-reclaim log lines operators rely on for
// audit.

func TestParseRetentionDays_Days(t *testing.T) {
	cases := []struct {
		in   string
		want int
	}{
		{"1d", 1},
		{"7d", 7},
		{"30d", 30},
		{"365d", 365},
	}
	for _, c := range cases {
		got, err := parseRetentionDays(c.in)
		if err != nil || got != c.want {
			t.Errorf("%q → want %d, got %d (err=%v)", c.in, c.want, got, err)
		}
	}
}

func TestParseRetentionDays_HoursRoundUp(t *testing.T) {
	// Hours below 1d round UP to whole days so we don't drop
	// data that's between 23h and 47h old.
	cases := []struct {
		in   string
		want int
	}{
		{"1h", 1},
		{"23h", 1},
		{"24h", 1},
		{"25h", 2},
		{"48h", 2},
		{"49h", 3},
	}
	for _, c := range cases {
		got, err := parseRetentionDays(c.in)
		if err != nil || got != c.want {
			t.Errorf("%q → want %d, got %d (err=%v)", c.in, c.want, got, err)
		}
	}
}

func TestParseRetentionDays_Invalid(t *testing.T) {
	bad := []string{
		"", "30", "30s", "30w", "0d", "-1d", "abc", "1.5d",
	}
	for _, b := range bad {
		if _, err := parseRetentionDays(b); err == nil {
			t.Errorf("expected error for %q, got nil", b)
		}
	}
}

func TestHumanBytes(t *testing.T) {
	cases := []struct {
		in   uint64
		want string
	}{
		{0, "0 B"},
		{1, "1 B"},
		{1023, "1023 B"},
		{1024, "1.0 KiB"},
		{1536, "1.5 KiB"},
		{1024 * 1024, "1.0 MiB"},
		{1024 * 1024 * 1024, "1.0 GiB"},
		{1024 * 1024 * 1024 * 1024, "1.0 TiB"},
	}
	for _, c := range cases {
		got := humanBytes(c.in)
		if got != c.want {
			t.Errorf("humanBytes(%d) → %q, want %q", c.in, got, c.want)
		}
	}
}

// v0.10.263 (perf §7 madde 7, S2/S5) — MV depolarında partition düşürme hedefleri:
// trace MV'leri spans+1 gün (spans kapalıysa yok), spanmetrics_1s 1 / _10s 2 gün;
// DROP PARTITION SQL'i inner adı + ON CLUSTER eki ile.
func TestMVRetentionTargets(t *testing.T) {
	got := mvRetentionTargets(30)
	want := map[string]int{"spanmetrics_1s": 1, "spanmetrics_10s": 2, "trace_summary_5m": 31, "trace_service_index_5m": 31}
	if len(got) != len(want) {
		t.Fatalf("hedef sayısı %d, istenen %d: %+v", len(got), len(want), got)
	}
	for _, g := range got {
		if want[g.mv] != g.days {
			t.Errorf("%s: %d gün, istenen %d", g.mv, g.days, want[g.mv])
		}
	}
	off := mvRetentionTargets(0)
	if len(off) != 2 {
		t.Errorf("spans retention kapalıyken yalnız spanmetrics: %+v", off)
	}
	if got := mvDropPartitionSQL("`.inner_id.abc`", "20260901", " ON CLUSTER `c`"); got != "ALTER TABLE `.inner_id.abc` ON CLUSTER `c` DROP PARTITION 20260901" {
		t.Errorf("sql: %s", got)
	}
	if stripBackticks("`.inner_id.abc`") != ".inner_id.abc" {
		t.Error("backtick temizliği")
	}
}
