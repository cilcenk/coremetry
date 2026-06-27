package chstore

import (
	"strings"
	"testing"
)

// v0.8.191 — regression guard for the PRODUCTION OOM on the external
// Distributed cluster (1000s services / 10000s ops / billions of traces):
// summary-MV reads merging the 8192-sample reservoir quantilesState
// (~64 KiB/row, measured) blew the 3.73 GiB per-query memory limit
// (code 241) and the 10s timeout (code 159), so /services and /service-ops
// rendered only the tiny self-obs service.
//
// The heavy quantilesMerge reads MUST carry the memory-bounding settings.
// If a future edit drops them, this test fails. (The proper fix —
// quantilesTDigest state — is tracked separately; until it lands these
// settings are the safety floor.)
func TestMVQuantileMemSettings(t *testing.T) {
	for _, want := range []string{
		"max_threads",
		"distributed_aggregation_memory_efficient",
	} {
		if !strings.Contains(mvQuantileMemSettings, want) {
			t.Errorf("mvQuantileMemSettings missing %q: %q", want, mvQuantileMemSettings)
		}
	}
	// Must be a valid trailing SETTINGS fragment (appended after an
	// existing `SETTINGS ... ,`) — no leading/trailing comma that would
	// produce `SETTINGS , x` or `x ,` and fail to parse.
	if strings.HasPrefix(strings.TrimSpace(mvQuantileMemSettings), ",") ||
		strings.HasSuffix(strings.TrimSpace(mvQuantileMemSettings), ",") {
		t.Errorf("mvQuantileMemSettings must not lead/trail with a comma: %q", mvQuantileMemSettings)
	}
}
