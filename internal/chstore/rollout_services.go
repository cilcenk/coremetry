package chstore

import (
	"context"
	"fmt"
	"strings"
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

// RolloutServiceKey — v0.10.244 (Problem↔Rollout D4): toplu servis
// çözümünün anahtarı; Cluster SPAN değeridir (bir EffectiveID birden çok
// span değeri taşıyabilir — çağıran birleştirir).
type RolloutServiceKey struct {
	Cluster, Namespace, Workload, Revision string
}

// rolloutServicesBatchSQL — SAF: n demet için tek MV sorgusu. MV ORDER BY
// (cluster, k8s_namespace, workload, revision, service_name, bucket) →
// demet IN = PK öneği. LIMIT 200 servis × n rollout.
func rolloutServicesBatchSQL(n int) string {
	tuples := make([]string, n)
	for i := range tuples {
		tuples[i] = "(?, ?, ?, ?)"
	}
	return `
		SELECT cluster, k8s_namespace, workload, revision, service_name
		FROM workload_revision_activity_1m
		WHERE (cluster, k8s_namespace, workload, revision) IN (` + strings.Join(tuples, ", ") + `)
		  AND bucket >= toDateTime(?, 'UTC') AND bucket <= now()
		GROUP BY cluster, k8s_namespace, workload, revision, service_name
		ORDER BY cluster, k8s_namespace, workload, revision, service_name
		LIMIT ` + fmt.Sprint(200*n) + `
		SETTINGS max_execution_time = 10`
}

// RolloutServicesBatch — listelenen rollout'ların servis kümeleri tek
// sorguda (feed rozeti: rollout başına ayrı MV sorgusu 200 satırda 200
// sorgu olurdu). since = en eski rollout başlangıcı − 1 saat (RolloutServices
// ile aynı pay). keys boşsa sorgu yok.
func (s *Store) RolloutServicesBatch(ctx context.Context, keys []RolloutServiceKey, since time.Time) (map[RolloutServiceKey][]string, error) {
	out := map[RolloutServiceKey][]string{}
	if len(keys) == 0 {
		return out, nil
	}
	if len(keys) > 1000 {
		keys = keys[:1000]
	}
	args := make([]any, 0, len(keys)*4+1)
	for _, k := range keys {
		args = append(args, k.Cluster, k.Namespace, k.Workload, k.Revision)
	}
	args = append(args, chDateTimeArg(since))
	rows, err := s.conn.Query(ctx, rolloutServicesBatchSQL(len(keys)), args...)
	if err != nil {
		return nil, fmt.Errorf("rollout services batch: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var k RolloutServiceKey
		var svc string
		if err := rows.Scan(&k.Cluster, &k.Namespace, &k.Workload, &k.Revision, &svc); err != nil {
			return nil, err
		}
		out[k] = append(out[k], svc)
	}
	return out, rows.Err()
}
