package mcptools

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/cilcenk/coremetry/internal/chstore"
	"github.com/cilcenk/coremetry/internal/rollout"
)

// v0.10.545 — list_deployments: üç kaynağın birleşimi saf ve tablo-testli;
// namespace daraltması inferred'ı düşürür (dürüstlük), servis süzgeci, sıra, limit.
func TestMergeChanges(t *testing.T) {
	t0 := time.Date(2026, 9, 8, 9, 0, 0, 0, time.UTC)
	inf := []chstore.RecentDeployEntry{
		{Service: "api", Version: "1.2.0", FirstSeenNs: t0.Add(10 * time.Minute).UnixNano(), SpanCount: 500},
		{Service: "pay", Version: "3.0.1", FirstSeenNs: t0.Add(30 * time.Minute).UnixNano(), Source: "event"},
	}
	ro := []chstore.RolloutRow{{Rollout: rollout.Rollout{ClusterID: "c1", Namespace: "shop", Workload: "api", Revision: "7", ImageTag: "1.2.0", StartedAt: t0.Add(20 * time.Minute), Status: "completed", SpanCount: 42}}}
	rows := mergeChanges(inf, ro, "", true, 10)
	if len(rows) != 3 || rows[0].Source != "event" || rows[1].Source != "rollout" || rows[2].Source != "inferred" {
		t.Fatalf("sıra/etiket: %+v", rows)
	}
	if rows[1].Version != "1.2.0" || rows[1].Namespace != "shop" || rows[1].Cluster != "c1" || rows[1].Status != "completed" {
		t.Fatalf("rollout satırı: %+v", rows[1])
	}
	if rows[2].TimeISO != "2026-09-08T09:10:00Z" || rows[2].SpanCount != 500 {
		t.Fatalf("inferred satırı: %+v", rows[2])
	}
	// servis süzgeci: inferred tam ad, rollout workload
	if got := mergeChanges(inf, ro, "api", true, 10); len(got) != 2 || got[0].Source != "rollout" || got[1].Service != "api" {
		t.Fatalf("servis süzgeci: %+v", got)
	}
	// namespace daraltması, servissiz → inferred düşer
	if got := mergeChanges(inf, ro, "", false, 10); len(got) != 1 || got[0].Source != "rollout" {
		t.Fatalf("includeInferred=false: %+v", got)
	}
	if got := mergeChanges(inf, ro, "", true, 2); len(got) != 2 {
		t.Fatalf("limit: %d", len(got))
	}
}

func TestListDeploymentsToolShape(t *testing.T) {
	tool := toolByName(t, ToolList(Deps{}), "list_deployments")
	if tool.ShortDescription == "" || tool.MinRole != "" {
		t.Fatalf("kısa açıklama + viewer tabanı: %+v", tool.MinRole)
	}
	props := tool.InputSchema["properties"].(map[string]any)
	for _, k := range []string{"cluster", "namespace", "service", "range_s", "limit"} {
		if _, ok := props[k]; !ok {
			t.Errorf("şema %s eksik", k)
		}
	}
	if _, err := tool.Handler(context.Background(), []byte(`{"namespace":"shop"}`)); err == nil || !strings.Contains(err.Error(), "store yok") {
		t.Fatalf("store'suz handler panik değil hata: %v", err)
	}
}
