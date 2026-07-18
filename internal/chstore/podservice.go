package chstore

import (
	"context"
	"time"
)

// PodServiceMap — host_name → aday servis adları (v0.9.11, audit:
// docs/audit/clusters-pod-service-correlation-audit.md §2.1).
// /clusters pod satırlarını Coremetry servisleriyle eşleştirir:
// containerized kurulumlarda resource.host.name = k8s pod adı
// (store.go:2386 sözleşmesi; canlıda %98 birebir doğrulandı).
//
// Anahtar (cluster, host_name) İKİLİSİ — namespace metric_points'te
// YOK (audit zemin bulgusu: k8s.namespace.name spans'ta %98,
// metric'te 0); belirsizlik çözümü servis-seviyesi metadata
// namespace'iyle api katmanında (pickPodService).
//
// Sorgu şekli GetHosts emsali: servis-prefix'siz tam-pencere
// metric_points taraması — bu yüzden çağıran pencereyi ≤15dk tutar
// (canlı pod eşleşmesi için yeter) + LIMIT 5000. groupUniqArray(3):
// tek pod'a birden çok servis yazan nadir durum (sidecar/agent) Go
// tarafında çözülür, 3 aday yeter.
// v0.9.52 (openshift-cluster-attr audit B1) — cluster filtresi yalnız
// k8s.cluster.name okuyordu; salt openshift.cluster.name basan bir
// OpenShift cluster'ında eşleşme sessizce boşalıyordu.
// v0.9.55 (operatör: "birini bulamazsa diğerine baksın") — coalesce
// ÖNCELİĞİ yerine herhangi-biri-eşleşirse: iki anahtar FARKLI
// değerlerle basılırsa (k8s.cluster.name="prod-1",
// openshift.cluster.name="ocp-prod-1") öncelik ikinci adı maskeler ve
// o adla filtre boş dönerdi. Sorgu zaten satır başına array taraması
// yapıyor — IN seti aynı maliyette, maskeleme yok. Anahtar seti
// clusterDeriveExpr'inkiyle birebir (podservice_test pinler).
var podServiceMapSQL = `
	SELECT host_name, groupUniqArray(3)(service_name) AS services
	FROM metric_points
	WHERE time >= ? AND time <= ? AND host_name != ''
	  AND (? = '' OR ? IN (
	    res_values[indexOf(res_keys, 'k8s.cluster.name')],
	    res_values[indexOf(res_keys, 'openshift.cluster.name')],
	    res_values[indexOf(res_keys, 'cluster')],
	    attr_values[indexOf(attr_keys, 'k8s.cluster.name')],
	    attr_values[indexOf(attr_keys, 'openshift.cluster.name')],
	    attr_values[indexOf(attr_keys, 'cluster')]
	  ))
	GROUP BY host_name
	LIMIT 5000
	SETTINGS max_execution_time = 10`

func (s *Store) PodServiceMap(ctx context.Context, cluster string, from, to time.Time) (map[string][]string, error) {
	rows, err := s.conn.Query(ctx, podServiceMapSQL, from, to, cluster, cluster)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string][]string{}
	for rows.Next() {
		var host string
		var services []string
		if err := rows.Scan(&host, &services); err != nil {
			return nil, err
		}
		out[host] = services
	}
	return out, rows.Err()
}
