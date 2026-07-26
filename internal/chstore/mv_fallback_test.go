package chstore

import (
	"context"
	"errors"
	"fmt"
	"testing"
)

// v0.9.276 — the unconditional fallback is what turned a slow /traces query
// into an HTTP 500 with a transport error body. A resource failure on the MV
// read cannot be rescued by re-running the same shape over raw spans; it only
// consumes the client's remaining budget and hides the real ClickHouse error.
func TestMVFallbackEligible(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want bool
	}{
		// Structural — the raw path reads different tables and genuinely helps.
		{"missing table", errors.New("code: 60, message: Table coremetry.trace_summary_5m doesn't exist"), true},
		{"unknown column", errors.New("code: 47, message: Unknown identifier: trace_id"), true},
		{"unclassified", errors.New("some driver hiccup"), true},

		// Resource — retrying on raw is strictly worse.
		{"timeout by code", errors.New("code: 159, message: Timeout exceeded: elapsed 15.0 s"), false},
		{"memory by code", errors.New("code: 241, message: Memory limit exceeded: would use 3.73 GiB"), false},
		{"cancelled", errors.New("code: 394, message: Query was cancelled"), false},
		{"too many queries", errors.New("code: 202, message: Too many simultaneous queries"), false},
		{"transport gave up", errors.New("failed to read packet from 10.0.0.1:9000: read tcp: i/o timeout"), false},
		{"context deadline", fmt.Errorf("wrapped: %w", context.DeadlineExceeded), false},

		{"nil is never eligible", nil, false},
	}
	for _, c := range cases {
		if got := mvFallbackEligible(c.err); got != c.want {
			t.Errorf("%s: mvFallbackEligible(%v) = %v, want %v", c.name, c.err, got, c.want)
		}
	}
}

// The exact two errors the operator hit in production, pinned by their real
// message text: a 2-day window that died in the transport, and a 6-hour window
// with a db.statement filter that died on ClickHouse's memory limit.
func TestOperatorReportedFailuresDoNotFallBack(t *testing.T) {
	for _, msg := range []string{
		`query processing: failed to read packet from 172.31.240.15:9000 (conn_id=39): read: read tcp 100.80.13.225:43194->172.31.240.15:9000: i/o timeout`,
		`code: 241, message: Query memory limit exceeded: would use 3.73 GiB (attempt to allocate chunk of 4.00 MiB), maximum: 3.73 GiB: While executing SourceFromNativeStream`,
	} {
		if mvFallbackEligible(errors.New(msg)) {
			t.Errorf("this failure must NOT be retried on the raw path — doing so is what\n"+
				"produced the 500 the operator saw:\n  %s", msg)
		}
	}
}
