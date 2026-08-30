package api

import (
	"fmt"
	"strconv"
	"time"

	"github.com/cilcenk/coremetry/internal/chstore"
)

// rollout_keys.go — v0.10.200: /api/rollouts* önbellek anahtarları — SAF
// (rollouts_test.go pinler: her girdi anahtarda, ayraç saldırısına
// kapalı — serbest metinler fnvStr ile AYRI AYRI özetlenir, v0.5.187).
// Pencere cacheBucket ile 30 s ızgaraya oturur; limit/topN çağıranda
// kelepçelenmiş gelir (anahtardan ÖNCE — crafted ?limit= ayrı girdi basmasın).

func rolloutsListKey(f chstore.RolloutFilter, limit int, from, to time.Time) string {
	return fmt.Sprintf("rollouts:list:%s:lim=%d:w=%s",
		fnvStr(f.ClusterID, f.Namespace, f.Workload, f.Status, f.Kind), limit, cacheBucket(from, to))
}

func rolloutKey(id chstore.RolloutID) string {
	// StartedAt de fnvStr içinde: ailenin tek sabit-genişlik-dışı damgası kalmasın
	return "rollouts:one:" + fnvStr(id.ClusterID, id.Namespace, id.Workload, id.Revision, strconv.FormatInt(id.StartedAt.UnixMilli(), 10))
}

func rolloutStatsKey(cluster, ns string, topN int, from, to time.Time) string {
	return fmt.Sprintf("rollouts:stats:%s:top=%d:w=%s", fnvStr(cluster, ns), topN, cacheBucket(from, to))
}
