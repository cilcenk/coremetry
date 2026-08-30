package chstore

import (
	"context"
	"fmt"
	"time"
)

// rollout_services.go — v0.10.203 (ROLLOUTS Faz 4b; audit §4.2).
//
// Bir rollout satırının SERVİSLERİ: workload_revision_activity_1m'in
// service_name boyutu tam bu an için gün-bir eklendi (§1.3) — çekmecenin
// health verdict'i ve problem/anomali/exception daraltması workload →
// servis eşlemesini buradan alır (service_metadata.Deployment tersi
// çoktan-çoğa ve elle bakım ister; MV gerçeği yazar).
//
// cluster parametresi SPAN DEĞERLERİ (registry EffectiveID değil):
// çağıran (api) Remote Cluster kaydını değerlere çözer — MV'de kimlik
// değil ham değer var (reconciler'daki MapClusters'ın ters yönü).
// capped — tavan ısırdı (sessiz kesme yok — aile kuralı: RolloutActivity `cut`,
// RolloutFirstSeen hata; burada liste yine işe yarar → bool + çekmece notu).
func (s *Store) RolloutServices(ctx context.Context, clusterValues []string, namespace, workload, revision string, since time.Time) ([]string, bool, error) {
	if len(clusterValues) == 0 {
		return []string{}, false, nil
	}
	rows, err := s.conn.Query(ctx, `
		SELECT DISTINCT service_name
		FROM workload_revision_activity_1m
		WHERE cluster IN (?) AND k8s_namespace = ? AND workload = ? AND revision = ?
		  AND bucket >= toDateTime(?, 'UTC') AND bucket <= now()
		ORDER BY service_name
		LIMIT 201
		SETTINGS max_execution_time = 10`,
		clusterValues, namespace, workload, revision, chDateTimeArg(since))
	if err != nil {
		return nil, false, fmt.Errorf("rollout services: %w", err)
	}
	defer rows.Close()
	out := []string{}
	for rows.Next() {
		var svc string
		if err := rows.Scan(&svc); err != nil {
			return nil, false, err
		}
		out = append(out, svc)
	}
	capped := false
	if len(out) > 200 {
		out, capped = out[:200], true
	}
	return out, capped, rows.Err()
}
