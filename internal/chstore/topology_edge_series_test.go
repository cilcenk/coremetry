package chstore

import (
	"strings"
	"testing"
)

// v0.10.438 (D3) — kenar serisi SQL sözleşmesi: MV, FINAL, zaman sınırlı
// WHERE, GROUP BY time_bucket, LIMIT + max_execution_time; ham spans yok.
func TestTopologyEdgeSeriesSQLContract(t *testing.T) {
	q := topologyEdgeSeriesSQL
	for _, want := range []string{"FROM topology_edges_5m FINAL", "parent_service = ?", "child_node IN (?, ?, ?, ?)",
		"time_bucket >= ? AND time_bucket < ?", "GROUP BY time_bucket", "ORDER BY time_bucket", "LIMIT 2000", "max_execution_time = 10"} {
		if !strings.Contains(q, want) {
			t.Errorf("SQL %q içermeli:\n%s", want, q)
		}
	}
	if strings.Contains(q, "FROM spans") {
		t.Fatal("kenar serisi ham spans'a inmemeli")
	}
}
