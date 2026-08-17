package api

// v0.9.1164 — where the unfiltered-bucket-scan guard is allowed to exist, and
// what the HTTP layer must do with its refusal.
//
// Two properties, both of which would fail SILENTLY:
//
//  1. the refusal classifies as 400. It travels as a sentinel through
//     upstream(), whose DEFAULT branch is 502 — so a sentinel that was added
//     to vmetrics but never listed here would tell the operator their
//     VictoriaMetrics is broken and send them to inspect a healthy cluster,
//     while hiding the checkbox that would have let the query through.
//  2. the guard stays on the VICTORIAMETRICS side. The ClickHouse metric path
//     reads pre-aggregated MVs; a percentile there is a column, not a bucket
//     fan-out, so guarding it would refuse queries that cost nothing. Nothing
//     in the diff put it there — this pins that it stays that way, because the
//     "make both backends behave the same" instinct is exactly how it would
//     spread.

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/cilcenk/coremetry/internal/vmetrics"
)

// The guard sentinel must classify as a 400, exactly like ErrUnsupported, and
// never as an upstream failure.
func TestUpstreamClassifiesTheGuardAs400(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want error
	}{
		{"the guard refusal", vmetrics.ErrUnfilteredBuckets, errBadRequest},
		{"wrapped, as the builders return it",
			fmt.Errorf("%w: add a filter", vmetrics.ErrUnfilteredBuckets), errBadRequest},
		{"an untranslatable query, unchanged", vmetrics.ErrUnsupported, errBadRequest},
		{"anything else is still upstream", errors.New("dial tcp: connection refused"), errUpstream},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := upstream(tc.err)
			if !errors.Is(got, tc.want) {
				t.Fatalf("upstream(%v) did not classify as %v: %v", tc.err, tc.want, got)
			}
			// And it must not ALSO match the other class — the HTTP layer picks
			// a status by the first match, so an error tagged both ways would
			// change behaviour on a refactor of the status switch.
			other := errUpstream
			if tc.want == errUpstream {
				other = errBadRequest
			}
			if errors.Is(got, other) {
				t.Fatalf("upstream(%v) matches BOTH classes: %v", tc.err, got)
			}
			// The operator-facing text has to survive the wrap, or the 400
			// arrives with no instructions.
			if !errors.Is(got, tc.err) {
				t.Fatalf("upstream() dropped the original error: %v", got)
			}
		})
	}
}

// A nil error must stay nil — upstream() is called on every delegated method's
// return, including the successful ones.
func TestUpstreamPassesNilThrough(t *testing.T) {
	if err := upstream(nil); err != nil {
		t.Fatalf("upstream(nil) = %v", err)
	}
}

// THE CLICKHOUSE PATH IS UNTOUCHED.
//
// Scanned rather than argued: the claim "we did not add it there" is only
// true until someone does. internal/promql is included because the CH
// percentile path runs through its evaluator, and internal/chstore because
// that is where the MV reads live.
func TestGuardDoesNotReachTheClickHousePath(t *testing.T) {
	// Symbols that would only appear if the guard had been ported. The
	// sentinel is the qualified spelling a consumer outside vmetrics must use;
	// the helper name is unexported, so a hit on it means the CODE was copied.
	banned := []string{"ErrUnfilteredBuckets", "guardBucketScan", "AllowUnfilteredPercentiles"}

	for _, pkg := range []string{"chstore", "promql"} {
		dir := filepath.Join("..", pkg)
		entries, err := os.ReadDir(dir)
		if err != nil {
			t.Fatalf("cannot read internal/%s — the scope pin cannot be verified: %v", pkg, err)
		}
		for _, e := range entries {
			if e.IsDir() || !strings.HasSuffix(e.Name(), ".go") {
				continue
			}
			b, err := os.ReadFile(filepath.Join(dir, e.Name()))
			if err != nil {
				t.Fatalf("cannot read %s/%s: %v", pkg, e.Name(), err)
			}
			for _, sym := range banned {
				if strings.Contains(string(b), sym) {
					t.Errorf("internal/%s/%s references %q — the bucket-scan guard is a "+
						"VICTORIAMETRICS cost guard. ClickHouse answers percentiles from "+
						"pre-aggregated MVs, so guarding it there refuses queries that cost "+
						"nothing. If this is intentional, the decision belongs in the "+
						"guard's header first.", pkg, e.Name(), sym)
				}
			}
		}
	}
}

// The 400 mapping must be reachable from the one place every VM read is
// classified. If a future method stops routing through upstream(), the
// sentinel test above would still pass while real requests 500'd.
func TestEveryVMSourceMethodClassifiesThroughUpstream(t *testing.T) {
	b, err := os.ReadFile("metricsource.go")
	if err != nil {
		t.Fatalf("metricsource.go unreadable: %v", err)
	}
	src := string(b)
	if !strings.Contains(src, "vmetrics.ErrUnfilteredBuckets") {
		t.Fatal("upstream() no longer knows the guard sentinel — every refusal " +
			"would 502 and point the operator at a healthy VictoriaMetrics")
	}
	// Every vmMetricSource method body must hand its error to upstream().
	// Counted rather than eyeballed: the delegation is mechanical, which is
	// exactly the kind of code a new method gets added to without the tag.
	for _, m := range []string{
		"ListMetricNames", "QueryMetric", "MetricLabelValues", "MetricAttrKeys",
		"QueryMetricHistogram", "QueryPromQLRange", "QueryMetricNoted",
	} {
		i := strings.Index(src, "func (v vmMetricSource) "+m+"(")
		if i < 0 {
			t.Errorf("vmMetricSource.%s is gone — if it was renamed, update this gate", m)
			continue
		}
		// Look only inside the method: from its signature to the next top-level
		// func. A whole-file check would pass on any file that mentions
		// upstream anywhere.
		body := src[i:]
		if j := strings.Index(body[1:], "\nfunc "); j >= 0 {
			body = body[:j+1]
		}
		if !strings.Contains(body, "upstream(") {
			t.Errorf("vmMetricSource.%s does not classify through upstream() — its "+
				"errors would 500 and read as a Coremetry bug", m)
		}
	}
}
