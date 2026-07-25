package api

import (
	"testing"
	"time"
)

// v0.9.244 regression pin — operator-reported: /api/services/<svc>/bundle
// took 17.41s in prod because the deploys leg (a 48h raw-spans scan) gated
// the whole fan-out. The fix serves deploys from cache and fills in the
// background, which only pays off if the cache key is COARSE enough to be
// reused across polls.
//
// The failure mode this pins is the same class as the TTL x staleFactor
// trap: a rolling `to=now` window snapped to a fine grid mints a brand-new
// key on every request, so the expensive fill is recomputed forever and the
// cache never hits. cacheBucket's 30s grid is exactly that mistake here,
// which is why deployWindowBucket exists as its own coarser helper.
func TestDeployWindowBucket(t *testing.T) {
	base := time.Date(2026, 7, 25, 14, 0, 0, 0, time.UTC)
	from := base.Add(-24 * time.Hour)

	t.Run("same 5-minute slot shares one key", func(t *testing.T) {
		// A page polling every 10s walks `to` forward second by second.
		// Every one of those requests must land on the same key.
		want := deployWindowBucket(from, base)
		for _, off := range []time.Duration{
			0, time.Second, 10 * time.Second, 90 * time.Second,
			4 * time.Minute, 4*time.Minute + 59*time.Second,
		} {
			if got := deployWindowBucket(from, base.Add(off)); got != want {
				t.Errorf("to+%s: got %q, want %q (same 5m slot must share a key)", off, got, want)
			}
		}
	})

	t.Run("next 5-minute slot gets a new key", func(t *testing.T) {
		a := deployWindowBucket(from, base)
		b := deployWindowBucket(from, base.Add(5*time.Minute))
		if a == b {
			t.Errorf("crossing a 5m boundary must change the key, both %q", a)
		}
	})

	t.Run("window start is bucketed too", func(t *testing.T) {
		// Both ends snap: a shifting `from` inside one slot must not
		// invalidate the fill either.
		if a, b := deployWindowBucket(from, base), deployWindowBucket(from.Add(42*time.Second), base); a != b {
			t.Errorf("from+42s: got %q, want %q", b, a)
		}
		if a, b := deployWindowBucket(from, base), deployWindowBucket(from.Add(5*time.Minute), base); a == b {
			t.Errorf("from crossing a 5m boundary must change the key, both %q", a)
		}
	})

	t.Run("grid is coarser than cacheBucket", func(t *testing.T) {
		// The whole point of a separate helper. If someone swaps this
		// back to cacheBucket, a 60s shift stops sharing a key.
		shifted := base.Add(60 * time.Second)
		if cacheBucket(from, base) == cacheBucket(from, shifted) {
			t.Fatal("precondition failed: cacheBucket is expected to differ at +60s")
		}
		if deployWindowBucket(from, base) != deployWindowBucket(from, shifted) {
			t.Error("deployWindowBucket must still share a key at +60s")
		}
	})

	t.Run("distinct windows never collide", func(t *testing.T) {
		seen := map[string]string{}
		for _, w := range []struct {
			name     string
			from, to time.Time
		}{
			{"1h", base.Add(-time.Hour), base},
			{"6h", base.Add(-6 * time.Hour), base},
			{"24h", base.Add(-24 * time.Hour), base},
			{"24h shifted a day", base.Add(-48 * time.Hour), base.Add(-24 * time.Hour)},
		} {
			k := deployWindowBucket(w.from, w.to)
			if prev, dup := seen[k]; dup {
				t.Errorf("%s collides with %s on key %q", w.name, prev, k)
			}
			seen[k] = w.name
		}
	})
}
