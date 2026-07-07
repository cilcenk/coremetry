package chstore

import (
	"strings"
	"testing"

	"github.com/cilcenk/coremetry/internal/config"
)

// v0.8.358 — the spanmetrics_* doorway MVs are per-shard on a
// chstore-owned cluster (no Distributed wrapper), so a bare-name read
// returns ONE shard's slice (~half on the 2-shard reference install).
// v0.8.356 fixed the /endpoints read via a cluster() source; these tests
// pin the generalized helper and its injection into the evaluator's
// batched mq_* plans (the /explore doorway, coverage probe and exemplar
// rollup use the same helper inline).

func TestSpanmetricsSourceFor(t *testing.T) {
	cases := []struct {
		name    string
		cluster string
		table   string
		want    string
	}{
		{"single-node bare name", "", "spanmetrics_1m", "spanmetrics_1m"},
		{"whitespace cluster = unset", "  ", "spanmetrics_10s", "spanmetrics_10s"},
		{"cluster fans out", "coremetry", "spanmetrics_1m",
			"cluster('coremetry', currentDatabase(), 'spanmetrics_1m')"},
		{"every tier honored", "coremetry", "spanmetrics_1s",
			"cluster('coremetry', currentDatabase(), 'spanmetrics_1s')"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			s := &Store{cfg: config.CHConfig{ClusterName: c.cluster}}
			if got := s.spanmetricsSourceFor(c.table); got != c.want {
				t.Fatalf("spanmetricsSourceFor(%q) with cluster %q = %q, want %q",
					c.table, c.cluster, got, c.want)
			}
		})
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
