package chstore

import (
	"strings"
	"testing"

	"github.com/cilcenk/coremetry/internal/config"
)

// History: v0.8.358 pinned a cluster() fan-out here because the
// spanmetrics_* doorway MVs were per-shard with no Distributed
// wrapper (bare-name reads returned ONE shard's slice — ~half on the
// 2-shard reference install).
//
// v0.8.408 — the doorway tiers were PROMOTED into highVolumeTables
// (boot-time promoteCombinedMVs RENAMEs bare → _local + creates the
// Distributed wrapper, data preserved), so the bare name now fans out
// by itself. These tests pin the INVERSE invariant: the helper must
// return the bare name in every mode — cluster() over a Distributed
// wrapper reads every shard N times (N× overcount).

func TestSpanmetricsSourceFor(t *testing.T) {
	cases := []struct {
		name    string
		cluster string
		table   string
		want    string
	}{
		{"single-node bare name", "", "spanmetrics_1m", "spanmetrics_1m"},
		{"whitespace cluster = unset", "  ", "spanmetrics_10s", "spanmetrics_10s"},
		{"cluster mode ALSO bare — wrapper fans out (v0.8.408)", "coremetry", "spanmetrics_1m", "spanmetrics_1m"},
		{"every tier honored", "coremetry", "spanmetrics_1s", "spanmetrics_1s"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			s := &Store{cfg: config.CHConfig{ClusterName: c.cluster}}
			got := s.spanmetricsSourceFor(c.table)
			if got != c.want {
				t.Fatalf("spanmetricsSourceFor(%q) with cluster %q = %q, want %q",
					c.table, c.cluster, got, c.want)
			}
			if strings.Contains(got, "cluster(") {
				t.Fatalf("cluster() over the promoted Distributed wrapper double-counts: %q", got)
			}
		})
	}
}

// Shape-level pin for the promotion itself: the registrations exist
// and adaptDDL emits the _local MV + Distributed wrapper pair for a
// doorway tier, same as the long-promoted *_5m summaries.
func TestDoorwayTiersArePromoted(t *testing.T) {
	for _, tier := range []string{"spanmetrics_1m", "spanmetrics_10s", "spanmetrics_1s"} {
		if !highVolumeTables[tier] {
			t.Errorf("%s missing from highVolumeTables (v0.8.408 promotion)", tier)
		}
		if defaultShardPolicy[tier] == "" {
			t.Errorf("%s missing from defaultShardPolicy", tier)
		}
		if !tablesWithoutTraceID[tier] {
			t.Errorf("%s must be in tablesWithoutTraceID (trace_id only inside AggregateFunction states)", tier)
		}
	}

	s := &Store{cfg: config.CHConfig{ClusterName: "coremetry", Database: "coremetry"}}
	frags := s.adaptDDL(`CREATE MATERIALIZED VIEW IF NOT EXISTS spanmetrics_1m
		 ENGINE = AggregatingMergeTree
		 ORDER BY (service_name, time_bucket)
		 AS SELECT service_name, countState() AS calls_state FROM spans GROUP BY service_name`)
	if len(frags) != 2 {
		t.Fatalf("adaptDDL must emit MV + wrapper, got %d fragment(s): %v", len(frags), frags)
	}
	if !strings.Contains(frags[0], "spanmetrics_1m_local") || !strings.Contains(frags[0], "FROM spans_local") {
		t.Errorf("first fragment must create the _local MV reading spans_local: %s", frags[0])
	}
	if !strings.Contains(frags[1], "ENGINE = Distributed") || !strings.Contains(frags[1], "spanmetrics_1m_local") {
		t.Errorf("second fragment must be the Distributed wrapper over _local: %s", frags[1])
	}
}

func TestMeasureAllServicesPlanClusterSource(t *testing.T) {
	src := "cluster('coremetry', currentDatabase(), 'spanmetrics_1m')"

	// mq_* plans must read through the injected source, never the bare MV.
	plan, err := measureAllServicesPlan("mq_consume_count", src)
	if err != nil {
		t.Fatalf("plan: %v", err)
	}
	if !strings.Contains(plan.sql, "FROM "+src) {
		t.Errorf("mq plan must read the injected source\n--- SQL ---\n%s", plan.sql)
	}
	if strings.Contains(plan.sql, "FROM spanmetrics_1m") {
		t.Errorf("mq plan still reads the bare per-shard MV\n--- SQL ---\n%s", plan.sql)
	}

	// Non-spanmetrics routes must be untouched by the injection.
	for metric, from := range map[string]string{
		"error_rate": "FROM service_summary_5m",
		"db_p99_ms":  "FROM spans",
	} {
		p, err := measureAllServicesPlan(metric, src)
		if err != nil {
			t.Fatalf("plan(%q): %v", metric, err)
		}
		if !strings.Contains(p.sql, from) || strings.Contains(p.sql, "cluster(") {
			t.Errorf("plan(%q) must keep %s and ignore the spanmetrics source\n--- SQL ---\n%s",
				metric, from, p.sql)
		}
	}
}
