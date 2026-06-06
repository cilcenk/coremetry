package anomaly

import "testing"

// v0.8.44 (anomaly intelligence Phase 3) — dwell / M-of-N anti-flap. checkOne
// now judges the last dwellBuckets via evalWindow instead of the single most
// recent bucket: a transient one-bucket spike no longer flaps a problem open,
// and a flapping spike→drop (opposite directions) doesn't open either. The
// logic is stateless (derived from the fetched series) so it survives a
// leader handoff. These pin the open / resolve / flap behaviour.

func TestEvalWindow_Dwell(t *testing.T) {
	const median, mad = 100.0, 5.0 // z = madScale*(v-100)/5
	const (
		spike  = 140.0 // z ≈ 5.40 → open (up)
		normal = 101.0 // z ≈ 0.13 → not open, resolved
		bigUp  = 126.0 // z ≈ 3.51 → open up
		bigDn  = 74.0  // z ≈ -3.51 → open down
	)

	// Single transient spike (only the most recent bucket) must NOT open.
	if open, _, _ := evalWindow("p99_ms", median, mad, []float64{normal, spike}); open {
		t.Error("single-bucket spike must NOT open (dwell not satisfied)")
	}
	// Two consecutive spikes → open, direction from the latest bucket.
	if open, _, cur := evalWindow("p99_ms", median, mad, []float64{spike, spike}); !open || cur.direction != "spiked" {
		t.Errorf("two consecutive spikes must open spiked; open=%v dir=%q", open, cur.direction)
	}
	// Spike then back to normal → not sustained → must NOT open.
	if open, _, _ := evalWindow("p99_ms", median, mad, []float64{spike, normal}); open {
		t.Error("spike→normal must NOT open (not sustained)")
	}
	// Two normals → not open, fully resolved.
	if open, res, _ := evalWindow("p99_ms", median, mad, []float64{normal, normal}); open || !res {
		t.Errorf("two normals: want open=false resolved=true; got open=%v resolved=%v", open, res)
	}
	// request_rate "both": spike then drop (opposite directions) must NOT
	// open — that's flapping, not a sustained anomaly.
	if open, _, _ := evalWindow("request_rate", median, mad, []float64{bigUp, bigDn}); open {
		t.Error("rps spike→drop must NOT open (direction flip)")
	}
	// request_rate sustained drop → open dropped.
	if open, _, cur := evalWindow("request_rate", median, mad, []float64{bigDn, bigDn}); !open || cur.direction != "dropped" {
		t.Errorf("sustained rps drop must open dropped; open=%v dir=%q", open, cur.direction)
	}
	// Empty window → not open, not resolved (defensive).
	if open, res, _ := evalWindow("p99_ms", median, mad, nil); open || res {
		t.Errorf("empty window must be open=false resolved=false; got %v %v", open, res)
	}
}
