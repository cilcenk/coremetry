package api

import "time"

// snapAnomalyWindow clamps the /api/anomalies/log-patterns ?window=
// param onto a fixed rung set (v0.8.270, operator: "log anomalies
// elastic backend'de çok fazla sorgu yapmasın"). The window is part
// of the endpoint's cache key, so unbounded values let every caller
// mint a fresh key — and every fresh key pays a full _msearch batch
// against the external ES cluster. Four rungs cap the worst-case
// key cardinality (and therefore the ES rate) no matter what
// dashboards or hand-edited URLs send. Snaps to the smallest rung
// that covers the request so a caller never silently gets LESS
// lookback than asked for; anything past the top rung gets the top
// rung (30m — the detector story is "what changed recently", not
// long-range analytics).
var anomalyWindowRungs = []time.Duration{
	1 * time.Minute,
	5 * time.Minute,
	15 * time.Minute,
	30 * time.Minute,
}

func snapAnomalyWindow(d time.Duration) time.Duration {
	for _, rung := range anomalyWindowRungs {
		if d <= rung {
			return rung
		}
	}
	return anomalyWindowRungs[len(anomalyWindowRungs)-1]
}
