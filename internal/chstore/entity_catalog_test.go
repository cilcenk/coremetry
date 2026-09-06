package chstore

import (
	"strings"
	"testing"
	"time"
)

// v0.10.468 (Faz 2 F2-1) — katalog okumaları: JOIN yok, cluster_id + geçerlilik
// + LIMIT + max_execution_time her sorguda.

func TestEntityCatalogSQLShapes(t *testing.T) {
	at := time.Date(2026, 9, 6, 12, 0, 0, 0, time.UTC)
	sql, args := entityCountsByNamespaceSQL("c-1", "pod", time.Time{})
	for _, want := range []string{"FROM entities FINAL", "cluster_id = ?", "entity_type = ?", "valid_to = toDateTime(0)", "GROUP BY namespace", "LIMIT 5000", "max_execution_time"} {
		if !strings.Contains(sql, want) {
			t.Errorf("counts: %q yok", want)
		}
	}
	if strings.Contains(sql, "JOIN") || len(args) != 2 {
		t.Errorf("counts: JOIN'siz + 2 arg: %v", args)
	}
	sql, args = entityChildrenCountsByParentsSQL("c-1", "pod", []string{"wl:c-1/pay/Deployment/api"}, at)
	if !strings.Contains(sql, "parent_id IN (?)") || !strings.Contains(sql, "valid_from <= ?") || len(args) != 5 {
		t.Errorf("children: %s %v", sql, args)
	}
	from, to := at.Add(-30*time.Minute), at
	sql, args = entitySeenServicesByNamespaceSQL([]string{"prod-eu-west"}, "pay", from, to)
	for _, want := range []string{"FROM entity_seen_5m", "cluster IN (?)", "k8s_namespace = ?", "toStartOfFiveMinute(?)", "GROUP BY service_name", "LIMIT 200", "max_execution_time"} {
		if !strings.Contains(sql, want) {
			t.Errorf("seen services: %q yok", want)
		}
	}
	if len(args) != 4 {
		t.Errorf("seen services arg sayısı: %v", args)
	}
}
