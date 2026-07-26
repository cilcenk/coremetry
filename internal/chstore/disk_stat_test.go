package chstore

import "testing"

// v0.9.289 (operator ask: "can I see the disk usage of the server
// ClickHouse runs on"). /admin/stats already reported how much room
// Coremetry's TABLES occupy (system.parts); it never reported how much
// room the volume HAS. Those are different questions, and only the
// second one tells you whether retention is set correctly.
//
// The arithmetic is small but it is on an operator-facing gauge, so the
// degenerate inputs are pinned: a disk ClickHouse lists but cannot stat
// must not render as a full one.
func TestDiskStatUsage(t *testing.T) {
	const gb = uint64(1) << 30

	cases := []struct {
		name     string
		disk     DiskStat
		wantUsed uint64
		wantPct  float64
	}{
		{
			name:     "half full",
			disk:     DiskStat{TotalBytes: 100 * gb, FreeBytes: 50 * gb},
			wantUsed: 50 * gb,
			wantPct:  50,
		},
		{
			name:     "brand new disk",
			disk:     DiskStat{TotalBytes: 100 * gb, FreeBytes: 100 * gb},
			wantUsed: 0,
			wantPct:  0,
		},
		{
			name:     "genuinely full — the case that must page someone",
			disk:     DiskStat{TotalBytes: 100 * gb, FreeBytes: 0},
			wantUsed: 100 * gb,
			wantPct:  100,
		},
		{
			// A disk CH lists but cannot stat reports zeroes. Dividing
			// there would panic; treating it as full would raise a false
			// alarm on a healthy cluster. Unknown reads as 0%.
			name:     "unstattable disk is unknown, not full",
			disk:     DiskStat{TotalBytes: 0, FreeBytes: 0},
			wantUsed: 0,
			wantPct:  0,
		},
		{
			// Free > total is nonsense the filesystem can still report
			// mid-resize. Clamp rather than underflow — these are
			// unsigned, so the naive subtraction yields ~16 exabytes.
			name:     "free exceeding total clamps instead of underflowing",
			disk:     DiskStat{TotalBytes: 10 * gb, FreeBytes: 20 * gb},
			wantUsed: 0,
			wantPct:  0,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.disk.UsedBytes(); got != tc.wantUsed {
				t.Fatalf("UsedBytes() = %d, want %d", got, tc.wantUsed)
			}
			if got := tc.disk.UsedPct(); got != tc.wantPct {
				t.Fatalf("UsedPct() = %v, want %v", got, tc.wantPct)
			}
			if p := tc.disk.UsedPct(); p < 0 || p > 100 {
				t.Fatalf("UsedPct() = %v is outside 0..100 — the gauge would overflow", p)
			}
		})
	}
}
