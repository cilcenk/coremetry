package chstore

// rollout_problem_test.go — v0.10.241 Problem↔Rollout korelasyonu D1.
// SQL şekli sözleşmesi: her sorgu zaman sınırlı + LIMIT + max_execution_time
// (CLAUDE.md CH sınırı); demet/IN sayısı girdiyle birebir; pod sorgusu
// service_name PK öneğini kullanır; MV revizyon ifadesi spans ifadesiyle
// aynı (RS yoksa imaj tag'i).

import (
	"strings"
	"testing"
)

func TestRolloutsForWorkloadsSQLShape(t *testing.T) {
	q := rolloutsForWorkloadsSQL(3)
	for _, want := range []string{
		"FROM workload_rollouts FINAL",
		"started_at >= toDateTime64(?, 3, 'UTC') AND started_at <= toDateTime64(?, 3, 'UTC')",
		"(cluster_id, namespace, workload) IN ((?, ?, ?), (?, ?, ?), (?, ?, ?))",
		"LIMIT 200",
		"max_execution_time = 10",
	} {
		if !strings.Contains(q, want) {
			t.Errorf("sorguda %q yok:\n%s", want, q)
		}
	}
	if strings.Count(q, "?") != 2+3*3 {
		t.Errorf("placeholder sayısı %d, istenen 11", strings.Count(q, "?"))
	}
}

func TestRolloutRefsForServiceSQLShape(t *testing.T) {
	q := rolloutRefsForServiceSQL(2)
	for _, want := range []string{
		"FROM workload_revision_activity_1m",
		"service_name = ?",
		"bucket >= toDateTime64(?, 3, 'UTC') AND bucket <= toDateTime64(?, 3, 'UTC')",
		"cluster IN (?,?)",
		"GROUP BY cluster, k8s_namespace, workload, revision",
		"LIMIT 50",
		"max_execution_time = 10",
	} {
		if !strings.Contains(q, want) {
			t.Errorf("sorguda %q yok:\n%s", want, q)
		}
	}
	if strings.Contains(rolloutRefsForServiceSQL(0), "cluster IN") {
		t.Error("cluster listesi boşken IN filtresi olmamalı")
	}
}

func TestRolloutRefForPodSQLShape(t *testing.T) {
	q := rolloutRefForPodSQL()
	for _, want := range []string{
		"FROM spans",
		"service_name = ? AND k8s_pod = ?",
		"time >= toDateTime64(?, 3, 'UTC') AND time <= toDateTime64(?, 3, 'UTC')",
		"if(k8s_replicaset != '', k8s_replicaset, container_image_tag)",
		"LIMIT 1",
		"max_execution_time = 5",
	} {
		if !strings.Contains(q, want) {
			t.Errorf("sorguda %q yok:\n%s", want, q)
		}
	}
}
