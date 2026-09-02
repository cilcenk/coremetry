package chstore

// rollout_problem.go — v0.10.241 Problem↔Rollout korelasyonu (D1, state
// yarısı). workload_rollouts STATE tablosu → in-order ana bağlantı
// (s.conn); telemetri yarısı (MV + spans) rollout_problem_telemetry.go'da
// RoundRobin havuzunda. İkisi tek dosyada olsaydı conn_strategy_test'in
// "allowlist'li dosya state tablosu okuyamaz" kapısına takılırdı.
//
// Akış (anomaly/rootcause_worker.go appendRolloutCauses):
//   1. RolloutRefsForService  (MV)    → servisin pencerede span ürettiği
//                                       (cluster, ns, workload, revision)
//   2. RolloutRefForPod       (spans) → problemin pod'unun revizyonu
//   3. cluster span değeri → EffectiveID (çağıran çevirir; thanos ayarı)
//   4. RolloutsForWorkloads   (BURASI) → o iş yüklerinin penceredeki
//                                       rollout satırları
//   5. rollout.Rank                    → puan + kesme

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/cilcenk/coremetry/internal/rollout"
)

// rolloutsForWorkloadsMax — bir problem için okunacak en çok rollout satırı.
// Pencere ≤ 125 dk, iş yükü ≤ 50; 200 zaten cömert.
const rolloutsForWorkloadsMax = 200

// rolloutsForWorkloadsSQL — SAF: (cluster_id, namespace, workload) demet
// listesi + started_at penceresi. Test SQL şeklini çiviler (zaman sınırı,
// LIMIT, max_execution_time, demet sayısı).
func rolloutsForWorkloadsSQL(n int) string {
	tuples := make([]string, n)
	for i := range tuples {
		tuples[i] = "(?, ?, ?)"
	}
	return `SELECT ` + rolloutRowCols + ` FROM workload_rollouts FINAL
		WHERE started_at >= toDateTime64(?, 3, 'UTC') AND started_at <= toDateTime64(?, 3, 'UTC')
		  AND (cluster_id, namespace, workload) IN (` + strings.Join(tuples, ", ") + `)
		ORDER BY started_at DESC, cluster_id, namespace, workload, revision
		LIMIT ` + fmt.Sprint(rolloutsForWorkloadsMax) + ` SETTINGS max_execution_time = 10`
}

// RolloutsForWorkloads — verilen iş yüklerinin [from, to] içinde BAŞLAYAN
// rollout satırları. keys boşsa sorgu yok (boş küme kaybolur, sıfır
// olmaz: çağıran boş dönüşü "aday yok" okur).
func (s *Store) RolloutsForWorkloads(ctx context.Context, keys []rollout.Key, from, to time.Time) ([]RolloutRow, error) {
	if len(keys) == 0 {
		return []RolloutRow{}, nil
	}
	if len(keys) > 50 {
		keys = keys[:50]
	}
	args := []any{chDateTime64Arg(from), chDateTime64Arg(to)}
	for _, k := range keys {
		args = append(args, k.ClusterID, k.Namespace, k.Workload)
	}
	rows, err := s.conn.Query(ctx, rolloutsForWorkloadsSQL(len(keys)), args...)
	if err != nil {
		return nil, fmt.Errorf("rollouts for workloads: %w", err)
	}
	defer rows.Close()
	out := []RolloutRow{}
	for rows.Next() {
		r, upd, err := scanRollout(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, RolloutRow{Rollout: r, UpdatedAt: upd})
	}
	return out, rows.Err()
}
