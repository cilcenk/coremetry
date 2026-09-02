package api

// rollout_problems_test.go — v0.10.244 D4 sözleşmesi: sayım = başlangıçtan
// beri açık ∩ rollout'un servisleri; küme değeri birden çok olabilir
// (birleşim); registry'de olmayan küme (boş değer listesi) → 0; rollout'tan
// ÖNCE açılan problem sayılmaz.

import (
	"testing"
	"time"

	"github.com/cilcenk/coremetry/internal/chstore"
	"github.com/cilcenk/coremetry/internal/rollout"
)

func TestCountProblemsCaused(t *testing.T) {
	t0 := time.Date(2026, 9, 2, 12, 0, 0, 0, time.UTC)
	rows := []chstore.RolloutRow{
		{Rollout: rollout.Rollout{ClusterID: "prod-eu", Namespace: "pay", Workload: "api", Revision: "r2", StartedAt: t0}},
		{Rollout: rollout.Rollout{ClusterID: "ghost", Namespace: "pay", Workload: "api", Revision: "r9", StartedAt: t0}},
	}
	clusters := [][]string{{"eu1", "ocp-eu"}, nil}
	svcs := map[chstore.RolloutServiceKey][]string{
		{Cluster: "eu1", Namespace: "pay", Workload: "api", Revision: "r2"}:    {"api"},
		{Cluster: "ocp-eu", Namespace: "pay", Workload: "api", Revision: "r2"}: {"api", "api-worker"},
	}
	open := []*chstore.Problem{
		{Service: "api", StartedAt: t0.Add(5 * time.Minute).UnixNano()},        // sayılır
		{Service: "api-worker", StartedAt: t0.Add(1 * time.Minute).UnixNano()}, // ikinci küme değeri → sayılır
		{Service: "api", StartedAt: t0.Add(-1 * time.Minute).UnixNano()},       // rollout'tan önce → sayılmaz
		{Service: "billing", StartedAt: t0.Add(10 * time.Minute).UnixNano()},   // servis dışı
	}
	got := countProblemsCaused(rows, clusters, svcs, open)
	if got[0] != 2 || got[1] != 0 {
		t.Fatalf("sayım %v, istenen [2 0]", got)
	}
}
