package chstore

// rollout_problem_telemetry.go — v0.10.241 Problem↔Rollout korelasyonu
// (D1, telemetri yarısı). Yalnız MV + spans okur → telemetryReadConn
// (RoundRobin); state tablosu YOK (conn_strategy_test allowlist'i).
//
// Neden MV'nin tersi: RolloutServices (rollout_services.go) "bu rollout
// hangi servisleri taşıdı" sorar; burada "bu servis pencerede hangi
// (cluster, ns, workload, revision)'da span üretti" — aynı MV, ters yön.
// service_name ORDER BY öneğinde DEĞİL (5. kolon) → pencere partition'ı
// taranır; 125 dk × 1-dk kova × birkaç bin iş yükü = küçük, LIMIT + 10 s
// tavanla sınırlı.
//
// Cluster: MV'deki `cluster` SPAN değeridir (k8s.cluster.name…),
// workload_rollouts.cluster_id EffectiveID'dir. Çeviri çağıranda
// (thanos ayarı SpanClusterKeys → EffectiveID); burada ham değer döner.

import (
	"context"
	"fmt"
	"time"
)

// WorkloadRevisionRef — MV'den/spans'ten gelen ham (span cluster değeri)
// iş yükü + revizyon referansı.
type WorkloadRevisionRef struct {
	Cluster   string // span değeri, EffectiveID DEĞİL
	Namespace string
	Workload  string
	Revision  string
}

const rolloutRefsForServiceMax = 50

// rolloutRefsForServiceSQL — SAF; nClusters > 0 ise cluster IN (…) eklenir.
func rolloutRefsForServiceSQL(nClusters int) string {
	clusterWhere := ""
	if nClusters > 0 {
		clusterWhere = " AND cluster IN (" + chPlaceholders(nClusters) + ")"
	}
	return `SELECT cluster, k8s_namespace, workload, revision
		FROM workload_revision_activity_1m
		WHERE service_name = ?
		  AND bucket >= toDateTime64(?, 3, 'UTC') AND bucket <= toDateTime64(?, 3, 'UTC')
		  AND workload != '' AND revision != ''` + clusterWhere + `
		GROUP BY cluster, k8s_namespace, workload, revision
		ORDER BY cluster, k8s_namespace, workload, revision
		LIMIT ` + fmt.Sprint(rolloutRefsForServiceMax) + ` SETTINGS max_execution_time = 10`
}

// RolloutRefsForService — servisin [from, to] penceresinde span ürettiği
// (cluster, ns, workload, revision) kümesi. clusterValues boşsa cluster
// filtresi yok (tek-cluster kurulum / Clusters zenginleştirmesi boş).
func (s *Store) RolloutRefsForService(ctx context.Context, service string, clusterValues []string, from, to time.Time) ([]WorkloadRevisionRef, error) {
	if service == "" {
		return nil, nil
	}
	args := []any{service, chDateTime64Arg(from), chDateTime64Arg(to)}
	for _, c := range clusterValues {
		args = append(args, c)
	}
	rows, err := s.telemetryReadConn().Query(ctx, rolloutRefsForServiceSQL(len(clusterValues)), args...)
	if err != nil {
		return nil, fmt.Errorf("rollout refs for service: %w", err)
	}
	defer rows.Close()
	var out []WorkloadRevisionRef
	for rows.Next() {
		var r WorkloadRevisionRef
		if err := rows.Scan(&r.Cluster, &r.Namespace, &r.Workload, &r.Revision); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// rolloutRefForPodSQL — SAF. service_name PK öneği + zaman sınırı; pod
// filtresi terfi kolonu k8s_pod (promoted_attr.go). Revizyon MV ile AYNI
// ifade (RS yoksa imaj tag'i — v0.10.211 STS/DS vekili).
func rolloutRefForPodSQL() string {
	return `SELECT any(cluster), any(k8s_namespace),
		any(multiIf(k8s_deployment != '', k8s_deployment,
		            k8s_statefulset != '', k8s_statefulset,
		            k8s_daemonset != '', k8s_daemonset, '')),
		any(if(k8s_replicaset != '', k8s_replicaset, container_image_tag))
		FROM spans
		WHERE service_name = ? AND k8s_pod = ?
		  AND time >= toDateTime64(?, 3, 'UTC') AND time <= toDateTime64(?, 3, 'UTC')
		LIMIT 1 SETTINGS max_execution_time = 5`
}

// RolloutRefForPod — problemin pod'unun penceredeki iş yükü + revizyonu.
// ok=false → pod pencerede span üretmemiş ya da iş yükü/revizyon boş.
func (s *Store) RolloutRefForPod(ctx context.Context, service, pod string, from, to time.Time) (WorkloadRevisionRef, bool, error) {
	if service == "" || pod == "" {
		return WorkloadRevisionRef{}, false, nil
	}
	row := s.telemetryReadConn().QueryRow(ctx, rolloutRefForPodSQL(),
		service, pod, chDateTime64Arg(from), chDateTime64Arg(to))
	var r WorkloadRevisionRef
	if err := row.Scan(&r.Cluster, &r.Namespace, &r.Workload, &r.Revision); err != nil {
		return WorkloadRevisionRef{}, false, fmt.Errorf("rollout ref for pod: %w", err)
	}
	if r.Workload == "" || r.Revision == "" {
		return WorkloadRevisionRef{}, false, nil
	}
	return r, true, nil
}
