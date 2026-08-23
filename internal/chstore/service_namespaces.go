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
//
// v0.9.1318 — zincir artık identity.go'nun PAYLAŞILAN sözlüğünden
// geliyor (namespaceExpr/namespaceHasGuard). Öncesinde bu dosya kendi
// dört-basamaklı res-only sözlüğünü taşıyordu ve ilk iki basamağı
// service_metadata.go'nun türeticisine göre TERS sıradaydı —
// `k8s.namespace.name` önce. Her iki anahtarı da yayınlayan bir servis
// bu yüzden topoloji kutusunda bir ada, /services facet'inde başka bir
// ada sahipti. Eski yorum zinciri "deriveNamespaceSQL ile aynı sözlük"
// diye tarif ediyordu; öyle bir sembol repoda hiç YOKTU.
//
// attr_keys basamakları da eklendi (öncesinde yalnız res_keys). Ölçüldü
// (lokal, 1s pencere): read_bytes 6,85 MiB → 9,79 MiB (+%43), süre
// 614ms → 388ms. max_execution_time=10 bütçesi içinde; doğru namespace
// için ödenen bedel.
// serviceNamespacesSQL — paylaşılan namespace sözlüğünden ÜRETİLİR.
// Elle yazılmış bir ikizi olamaz: identity_test.go hem ifadenin hem
// guard'ın buradan geldiğini çiviler.
var serviceNamespacesSQL = `
		SELECT service_name,
		       anyHeavy(` + namespaceExpr() + `) AS ns
		FROM spans
		WHERE time >= ?
		  AND (` + namespaceHasGuard() + `)
		GROUP BY service_name
		LIMIT 5000
		SETTINGS max_execution_time = 10`

func (s *Store) GetServiceNamespaces(ctx context.Context, since time.Duration) (map[string]string, error) {
	if since <= 0 {
		since = time.Hour
	}
	cutoff := time.Now().Add(-since)
	rows, err := s.conn.Query(ctx, serviceNamespacesSQL, cutoff)
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
