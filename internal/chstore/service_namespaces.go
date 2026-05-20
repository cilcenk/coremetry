package chstore

import (
	"context"
	"time"
)

// GetServiceNamespaces returns the most-frequent k8s.namespace.name
// (or service.namespace fallback) for every service that has emitted
// a span in the last `since` window. Used by the Service Topology
// redux to soft-cluster nodes visually by namespace.
//
// v0.5.312 — operator runs 3000+ services in a multi-tenant
// OpenShift estate; the topology was unscannable without
// grouping. This is a read-time enrichment (not stored on
// service_metadata) so the namespace stays fresh as workloads
// move between namespaces during migrations.
//
// Performance posture: one CH query, partition-pruned to the
// 1h default window, GROUP BY service_name. service_name is
// LowCardinality; the indexOf() expressions over res_keys are
// per-row but cheap. Caps at 5000 services.
func (s *Store) GetServiceNamespaces(ctx context.Context, since time.Duration) (map[string]string, error) {
	if since <= 0 {
		since = time.Hour
	}
	cutoff := time.Now().Add(-since)
	rows, err := s.conn.Query(ctx, `
		SELECT service_name,
		       anyHeavy(coalesce(
		         nullIf(res_values[indexOf(res_keys, 'k8s.namespace.name')], ''),
		         nullIf(res_values[indexOf(res_keys, 'service.namespace')], ''),
		         ''
		       )) AS ns
		FROM spans
		WHERE time >= ?
		  AND (has(res_keys, 'k8s.namespace.name') OR has(res_keys, 'service.namespace'))
		GROUP BY service_name
		LIMIT 5000
		SETTINGS max_execution_time = 10`, cutoff)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make(map[string]string, 256)
	for rows.Next() {
		var svc, ns string
		if err := rows.Scan(&svc, &ns); err != nil {
			return nil, err
		}
		if ns != "" {
			out[svc] = ns
		}
	}
	return out, rows.Err()
}
