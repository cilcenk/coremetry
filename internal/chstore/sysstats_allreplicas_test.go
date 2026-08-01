package chstore

import (
	"os"
	"strings"
	"testing"
)

// v0.9.454 (operator-reported, prod: "4 node'lu CH cluster'da yalnız 2
// node'un diskini görüyorum") — cluster() her shard'dan TEK replika
// okur; 2 shard × 2 replika kümede node-düzeyi paneller (disk,
// utilizasyon) yarım küme gösteriyordu. Node metadata'sı
// clusterAllReplicas ile okunur; system.parts BİLEREK cluster()'da
// kalır (veri baytları replika başına kopyadır — tüm replikaları
// toplamak çift sayım olurdu).
func TestNodeMetadataUsesAllReplicas(t *testing.T) {
	sys, err := os.ReadFile("sysstats.go")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(sys), `"clusterAllReplicas('"+cn+"', system.disks)"`) {
		t.Error("system.disks must fan out to EVERY node (clusterAllReplicas) — cluster() shows one replica per shard, half the fleet on a 2×2 cluster")
	}
	if !strings.Contains(string(sys), `"cluster('" + cn + "', system.parts)"`) {
		t.Error("system.parts must STAY on cluster() — data bytes are per-replica copies; summing all replicas double-counts storage")
	}
	srv, err := os.ReadFile("server_stats.go")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(srv), `"clusterAllReplicas('" + cn + "', " + tbl + ")"`) {
		t.Error("server utilisation must fan out to EVERY node (clusterAllReplicas)")
	}
}

// v0.9.481'in TestMainConnRoundRobin pin'i v0.9.486'da KALDIRILDI:
// RoundRobin ana bağlantıda state okumalarını node'dan node'a gezdirdi
// (prod /users: 2 vs 205 kullanıcı). Güncel sözleşme conn_strategy_test.go'da.
