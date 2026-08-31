package chstore

import (
	"strings"
	"testing"
)

// rollout_schema_test.go — v0.10.193 sözleşmesi (rollout_schema.go başlığı;
// docs/audits/rollouts-audit.md §1, §5).

func TestWorkloadRolloutsDDLShape(t *testing.T) {
	ddl := workloadRolloutsDDL
	for _, want := range []string{
		"ENGINE = ReplacingMergeTree(version)",
		"version              UInt64        DEFAULT toUnixTimestamp64Nano(now64(9))",
		"ORDER BY (cluster_id, namespace, workload, revision, started_at)",
		"TTL toDate(started_at) + INTERVAL 180 DAY",
		"updated_at           DateTime64(3) DEFAULT now64(3)",
		"detected_by          LowCardinality(String)",
	} {
		if !strings.Contains(ddl, want) {
			t.Fatalf("workload_rollouts DDL'de eksik: %s", want)
		}
	}
	// Kural P1: state tablosunda PARTITION YOK; Nullable yok (sentinel '').
	if strings.Contains(ddl, "PARTITION BY") || strings.Contains(ddl, "Nullable") {
		t.Fatalf("workload_rollouts PARTITION/Nullable taşımamalı:\n%s", ddl)
	}
	runs := rolloutReconcileRunsDDL
	if !strings.Contains(runs, "ORDER BY (started_at, host)") || !strings.Contains(runs, "INTERVAL 30 DAY") || strings.Contains(runs, "PARTITION BY") {
		t.Fatalf("rollout_reconcile_runs şekli: %s", runs)
	}
}

func TestWorkloadRevisionActivityMVReadsPromotedColumnsOnly(t *testing.T) {
	ddl := workloadRevisionActivityMVDDL()
	for _, want := range []string{
		"CREATE MATERIALIZED VIEW IF NOT EXISTS workload_revision_activity_1m",
		"ENGINE = AggregatingMergeTree",
		"ORDER BY (cluster, k8s_namespace, workload, revision, service_name, bucket)",
		"SETTINGS index_granularity = 8192, ttl_only_drop_parts = 1",
		"INTERVAL 7 DAY",
		"if(k8s_replicaset != '', k8s_replicaset, container_image_tag)   AS revision",
		"anyLastSimpleState(container_image)",
		"anyLastSimpleState(container_image_tag)",
		"countState()",
		"minState(time)",
		"maxState(time)",
		"WHERE workload != '' AND revision != ''",
		"FROM spans",
	} {
		if !strings.Contains(ddl, want) {
			t.Fatalf("MV DDL'de eksik: %s\n%s", want, ddl)
		}
	}
	// INSERT başına dizi araması YOK (pod_inventory'nin ertelediği maliyet
	// sınıfı): yalnız terfi kolonları; tDigest yok (rollup ailesi DAR).
	for _, bad := range []string{"indexOf(", "res_values", "quantiles"} {
		if strings.Contains(ddl, bad) {
			t.Fatalf("MV %s içermemeli", bad)
		}
	}
	// Üç iş yükü kolonu multiIf ile tek workload; kind anyLast (GROUP BY dışı)
	if !strings.Contains(ddl, "anyLastSimpleState(multiIf(k8s_deployment != '', 'Deployment'") {
		t.Fatalf("workload_kind anyLastSimpleState(multiIf(...)) olmalı")
	}
	if strings.Contains(ddl, "GROUP BY bucket, cluster, k8s_namespace, workload, workload_kind") {
		t.Fatalf("workload_kind GROUP BY'da olmamalı (merge'de rastgele çöker)")
	}
	// LC tipi varsayılan; typ alanı ifade edilebilir (C3 kapısı)
	if got := (promotedAttr{col: "x"}).colType(); got != "LowCardinality(String)" {
		t.Fatalf("varsayılan tip LC olmalı: %s", got)
	}
	if got := (promotedAttr{col: "x", typ: "String"}).colType(); got != "String" {
		t.Fatalf("typ alanı DDL tipini seçmeli: %s", got)
	}
}

// Yeni terfi kolonları (v0.10.193): MV'nin okuduğu her kolon promotedAttrs'ta
// kayıtlı olmalı — aksi hâlde kolon yaratılmaz, MV kod 47 ile ingest'i düşürür.
func TestRolloutPromotedColumnsRegistered(t *testing.T) {
	have := map[string]promotedAttr{}
	for _, a := range promotedAttrs {
		have[a.col] = a
	}
	want := map[string][]string{
		"k8s_deployment":      {"k8s.deployment.name"},
		"k8s_statefulset":     {"k8s.statefulset.name"},
		"k8s_daemonset":       {"k8s.daemonset.name"},
		"k8s_replicaset":      {"k8s.replicaset.name"},
		"container_image":     {"container.image.name", "k8s.container.image.name"},
		"container_image_tag": {"container.image.tag", "k8s.container.image.tag"},
	}
	for col, keys := range want {
		a, ok := have[col]
		if !ok {
			t.Fatalf("promotedAttrs'ta %s yok", col)
		}
		if !a.res {
			t.Fatalf("%s resource kapsamında olmalı (k8s.* res_keys'te)", col)
		}
		if strings.Join(a.keys, ",") != strings.Join(keys, ",") {
			t.Fatalf("%s anahtarları %v, beklenen %v", col, a.keys, keys)
		}
	}
	// Tekil deployment kolonu eskisiyle çakışmasın: k8s_pod/k8s_namespace/k8s_node dokunulmadı.
	for _, col := range []string{"k8s_namespace", "k8s_pod", "k8s_node"} {
		if _, ok := have[col]; !ok {
			t.Fatalf("mevcut terfi kolonu kayboldu: %s", col)
		}
	}
}

// v0.10.193 — gün-bir üç kayıt (v0.5.426 dersi; inceleme blocker): biri eksikse
// küme kipinde _local + Distributed çıkmaz, her okuma TEK shard'ın dilimini döner.
func TestWorkloadRevisionActivityRegisteredDayOne(t *testing.T) {
	const mv = "workload_revision_activity_1m"
	if !highVolumeTables[mv] {
		t.Errorf("%s highVolumeTables'da yok", mv)
	}
	if got := defaultShardPolicy[mv]; got != "cityHash64(cluster, k8s_namespace, workload)" {
		t.Errorf("%s shard anahtarı beklenen değil: %q", mv, got)
	}
	if !tablesWithoutTraceID[mv] {
		t.Errorf("%s tablesWithoutTraceID'de yok", mv)
	}
	// state tabloları KAYITSIZ kalmalı (shard edilmez → stateTableDDL birleşik grup)
	for _, st := range []string{"workload_rollouts", "rollout_reconcile_runs"} {
		if highVolumeTables[st] || defaultShardPolicy[st] != "" || tablesWithoutTraceID[st] {
			t.Errorf("%s state tablosu shard kayıtlarına GİRMEMELİ", st)
		}
	}
}
