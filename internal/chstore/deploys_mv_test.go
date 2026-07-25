package chstore

import (
	"strings"
	"testing"
	"time"
)

// v0.9.249 regression pin — service_version_5m replaces the 48h raw-spans
// scan in GetServiceDeploys (operator-reported: /bundle 17.41s in prod,
// the deploys leg burning its whole 15s budget on every service).
//
// The subtle part is NOT the rollup, it's the cold start. A materialized
// view only populates from inserts made after it exists, so for the first
// deployLookback after an upgrade a version that was already running has
// no rows before the window start — and would read as if it had just
// deployed. That is exactly the v0.9.205 phantom-deploy-marker bug, so
// reading the MV before its history covers the lookback would re-open it
// for two days.
//
// These are shape/contract assertions on the SQL and the constants; the
// live behaviour is verified against a running ClickHouse at release time.
func TestServiceVersionMVQueryShape(t *testing.T) {
	t.Run("keeps the v0.9.205 window-start gate", func(t *testing.T) {
		// The HAVING is what distinguishes "started inside the window"
		// (a real rollout) from "was already running" (a fixed version
		// that must NOT get a marker). Losing it silently reintroduces
		// a deploy marker on the left edge of every window.
		if !strings.Contains(serviceVersionMVSQL, "first_seen_ns >= ?") {
			t.Error("MV query dropped the HAVING first_seen_ns >= window-start gate (v0.9.205)")
		}
		if !strings.Contains(serviceDeploysSQL, "first_seen_ns >= ?") {
			t.Error("raw-spans query dropped the same gate — the two paths must agree")
		}
	})

	t.Run("both paths share the same contract", func(t *testing.T) {
		for _, want := range []string{"LIMIT 50", "ORDER BY first_seen_ns ASC", "version != ''"} {
			if !strings.Contains(serviceVersionMVSQL, want) {
				t.Errorf("MV query missing %q — diverges from the raw path", want)
			}
			if !strings.Contains(serviceDeploysSQL, want) {
				t.Errorf("raw query missing %q", want)
			}
		}
	})

	t.Run("MV query is bounded", func(t *testing.T) {
		// CLAUDE.md hard constraint: every CH read carries a wall-clock
		// cap. The MV is small, but "small" is not a guarantee.
		if !strings.Contains(serviceVersionMVSQL, "max_execution_time") {
			t.Error("MV query has no max_execution_time")
		}
		// It must read the rollup, not spans — the entire point.
		if !strings.Contains(serviceVersionMVSQL, "FROM service_version_5m") {
			t.Error("MV query does not read service_version_5m")
		}
		if strings.Contains(serviceVersionMVSQL, "FROM spans") {
			t.Error("MV query still touches raw spans")
		}
	})

	t.Run("merges aggregate states, never bare columns", func(t *testing.T) {
		// AggregatingMergeTree columns are AggregateFunction states;
		// selecting them without *Merge returns binary state blobs.
		for _, want := range []string{"minMerge(first_seen_state)", "countMerge(span_count_state)"} {
			if !strings.Contains(serviceVersionMVSQL, want) {
				t.Errorf("MV query missing %q", want)
			}
		}
	})

	t.Run("lookback constant is unchanged", func(t *testing.T) {
		// The coverage gate and the query both key off this; a silent
		// change here would alter what "already running" means.
		if deployLookback != 48*time.Hour {
			t.Errorf("deployLookback = %s, want 48h", deployLookback)
		}
	})
}

// The MV must be registered in ALL THREE cluster registries on day one.
// v0.5.426 is the incident where a table reached one registry but not the
// others: adaptDDL never renamed it to `_local`, no Distributed wrapper
// was emitted, and every bare-name read silently returned ONE shard's
// slice. The same shape hit spanmetrics_* again in v0.8.356/358.
func TestServiceVersionMVClusterRegistration(t *testing.T) {
	const tbl = "service_version_5m"
	if !highVolumeTables[tbl] {
		t.Error("missing from highVolumeTables — no _local rename, no Distributed wrapper, reads return one shard")
	}
	if defaultShardPolicy[tbl] == "" {
		t.Error("missing from defaultShardPolicy — no sharding key for the Distributed wrapper")
	}
	if !tablesWithoutTraceID[tbl] {
		t.Error("missing from tablesWithoutTraceID — the MV does not project trace_id, so the env-override shard key must fall back to rand()")
	}
}
