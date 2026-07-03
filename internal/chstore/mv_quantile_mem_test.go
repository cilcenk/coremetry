package chstore

import (
	"strings"
	"testing"
)

// v0.8.191 introduced mvQuantileMemSettings as the safety floor for the
// PRODUCTION OOM (code 241) on summary-MV quantile reads while the MVs
// still stored ~64 KiB/row reservoir quantilesState. v0.8.194 migrated
// the state to quantilesTDigestState (~4.3 KiB/row, parallel-safe);
// v0.8.233 then removed the `max_threads = 2` band-aid so the hottest
// reads (/services agg + /service ops) regain CH's default parallelism.
//
// This pins BOTH directions of that contract:
//   - the streaming cross-shard merge must stay (it bounds the
//     initiator's peak on wide Distributed reads regardless of state
//     size, and single-node ignores it);
//   - the max_threads throttle must NOT quietly come back — with
//     TDigest state it is pure read-latency loss.
func TestMVQuantileMemSettings(t *testing.T) {
	if !strings.Contains(mvQuantileMemSettings, "distributed_aggregation_memory_efficient") {
		t.Errorf("mvQuantileMemSettings must keep the streaming cross-shard merge: %q",
			mvQuantileMemSettings)
	}
	if strings.Contains(mvQuantileMemSettings, "max_threads") {
		t.Errorf("max_threads throttle re-introduced — it was removed in v0.8.233 "+
			"because TDigest state (v0.8.194) ended the memory justification: %q",
			mvQuantileMemSettings)
	}
	// Must be a valid trailing SETTINGS fragment (appended after an
	// existing `SETTINGS ... ,`) — no leading/trailing comma that would
	// produce `SETTINGS , x` or `x ,` and fail to parse.
	if strings.HasPrefix(strings.TrimSpace(mvQuantileMemSettings), ",") ||
		strings.HasSuffix(strings.TrimSpace(mvQuantileMemSettings), ",") {
		t.Errorf("mvQuantileMemSettings must not lead/trail with a comma: %q", mvQuantileMemSettings)
	}
}
