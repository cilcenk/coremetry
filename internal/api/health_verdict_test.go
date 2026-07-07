package api

import (
	"net/http"
	"testing"
)

// v0.8.339 (HA audit H3) — readiness verdict. The audited hole: a
// fast-refusing dead ClickHouse keeps the ingest queues EMPTY (flushers
// discard instantly), so the old queue-depth-only health reported "ok"
// while 100% of telemetry was being thrown away. CH reachability now
// dominates the verdict; overload stays a 503 (drain out of the LB) and
// degraded stays 200 (visible, not evicting).
func TestHealthVerdict(t *testing.T) {
	cases := []struct {
		name                       string
		overloaded, degraded, chOK bool
		wantStatus                 string
		wantCode                   int
	}{
		{"all healthy", false, false, true, "ok", http.StatusOK},
		{"degraded is visible but routable", false, true, true, "degraded", http.StatusOK},
		{"overload evicts from LB", true, false, true, "overloaded", http.StatusServiceUnavailable},
		{"CH down with EMPTY queues still 503s", false, false, false, "clickhouse-unreachable", http.StatusServiceUnavailable},
		{"CH down wins over overload in the label", true, true, false, "clickhouse-unreachable", http.StatusServiceUnavailable},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			status, code := healthVerdict(c.overloaded, c.degraded, c.chOK)
			if status != c.wantStatus || code != c.wantCode {
				t.Fatalf("healthVerdict(%v,%v,%v) = (%q,%d), want (%q,%d)",
					c.overloaded, c.degraded, c.chOK, status, code, c.wantStatus, c.wantCode)
			}
		})
	}
}
