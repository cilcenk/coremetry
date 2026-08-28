package api

import "testing"

// v0.10.119 — ETA saf matematik: kalan dilim × ortalama / paralellik;
// koşu ETA'sı kalan günleri bu günün projeksiyonuyla ekler.
func TestBackfillEta(t *testing.T) {
	cases := []struct {
		done, total int
		avg         int64
		par, left   int
		day, run    int64
	}{
		{10, 96, 10_000, 1, 0, 860_000, 860_000},
		{10, 96, 10_000, 2, 0, 430_000, 430_000},
		{10, 96, 10_000, 1, 3, 860_000, 860_000 + 3*96*10_000},
		{96, 96, 10_000, 1, 1, 0, 960_000},
		{0, 96, 0, 1, 1, 0, 0}, // ortalama yok → bilinmiyor
		{5, 0, 10_000, 1, 0, 0, 0},
	}
	for _, c := range cases {
		d, r := backfillEta(c.done, c.total, c.avg, c.par, c.left)
		if d != c.day || r != c.run {
			t.Errorf("eta(%d/%d avg=%d par=%d left=%d) = %d/%d, istenen %d/%d", c.done, c.total, c.avg, c.par, c.left, d, r, c.day, c.run)
		}
	}
}
