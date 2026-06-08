package chstore

import (
	"strings"
	"testing"
)

// v0.8.70 — operator-reported: /api/traces?range=12h&search=…&sort=duration
// failed with CH code 241 ("Memory limit exceeded … AggregatingTransform"). A
// search filter disqualifies the trace_summary MV, so GetTraces falls back to a
// raw GROUP BY trace_id over spans; at scale that aggregation state crosses the
// node's memory limit. The fix appends tracesSpillSettings (external GROUP BY +
// external sort) to both the main and count queries so the aggregation spills
// to disk instead of OOM-ing. This guards the spill settings against accidental
// removal (a string-level guard; the real proof is running the query against a
// large spans table — see the release notes).
func TestTracesSpillSettingsPresent(t *testing.T) {
	for _, want := range []string{
		"max_bytes_before_external_group_by",
		"max_bytes_before_external_sort",
	} {
		if !strings.Contains(tracesSpillSettings, want) {
			t.Errorf("tracesSpillSettings must include %q (spill-to-disk guard for the raw trace GROUP BY)", want)
		}
	}
	// A zero threshold disables external GROUP BY — that would silently
	// re-introduce the OOM.
	if strings.Contains(tracesSpillSettings, "= 0,") || strings.HasSuffix(tracesSpillSettings, "= 0") {
		t.Error("spill thresholds must be > 0 (0 disables external GROUP BY/sort)")
	}
}
