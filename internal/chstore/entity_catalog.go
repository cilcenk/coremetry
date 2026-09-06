package chstore

// entity_catalog.go — v0.10.468 (CoSRE Telemetry Agent Faz 2, F2-1): varlık
// KATALOĞU bileşik okumaları — cluster → namespace → workload → pod →
// service.name yürüyüşü için tool'ların (mcptools/entity_catalog.go) ihtiyacı
// olan sayımlar ve telemetri eşlemesi.
//
// JOIN YOK — bilerek: prod'da entities/entity_seen_5m dağıtık; iki dağıtık alt
// sorgunun JOIN'i GLOBAL ister ve shard başına kopya üretir (query_log paket
// dersi). Her okuma tek tablo, cluster_id + geçerlilik + LIMIT + max_execution_time;
// birleştirme Go'da (küçük kümeler: namespace ≤500, workload ≤500).

import (
	"context"
	"fmt"
	"time"
)

// entityCountsByNamespaceSQL — verilen tipin namespace başına sayısı (o an
// geçerli). Saf; tablo-testli.
// v0.10.490 (Astra #6): namespaces doluysa yalnız o namespace'ler sayılır (tek
// eşleşme için tüm cluster'ı FINAL taramaz).
func entityCountsByNamespaceSQL(cid, typ string, namespaces []string, at time.Time) (string, []any) {
	v, vargs := entityValidAtSQL(at)
	args := []any{cid, typ}
	nsWhere := ""
	if len(namespaces) > 0 {
		nsWhere = " AND namespace IN (?)"
		args = append(args, namespaces)
	}
	args = append(args, vargs...)
	return `SELECT namespace, count() FROM entities FINAL
		WHERE cluster_id = ? AND entity_type = ?` + nsWhere + ` AND ` + v + `
		GROUP BY namespace
		LIMIT 5000
		SETTINGS max_execution_time = 10`, args
}

// EntityCountsByNamespace — namespace → adet (workload ya da pod); namespaces
// doluysa yalnız onlar.
func (s *Store) EntityCountsByNamespace(ctx context.Context, cid, typ string, namespaces []string, at time.Time) (map[string]int, error) {
	if len(namespaces) > 500 {
		namespaces = namespaces[:500]
	}
	sql, args := entityCountsByNamespaceSQL(cid, typ, namespaces, at)
	rows, err := s.conn.Query(ctx, sql, args...)
	if err != nil {
		return nil, fmt.Errorf("entity counts by namespace: %w", err)
	}
	defer rows.Close()
	out := map[string]int{}
	for rows.Next() {
		var ns string
		var n uint64
		if err := rows.Scan(&ns, &n); err != nil {
			return nil, err
		}
		out[ns] = int(n)
	}
	return out, rows.Err()
}

// entityChildrenCountsByParentsSQL — parent_id başına geçerli çocuk (tip)
// sayısı; workload'ların pod sayısı. Saf.
func entityChildrenCountsByParentsSQL(cid, typ string, parents []string, at time.Time) (string, []any) {
	v, vargs := entityValidAtSQL(at)
	args := append([]any{cid, typ, parents}, vargs...)
	return `SELECT parent_id, count() FROM entities FINAL
		WHERE cluster_id = ? AND entity_type = ? AND parent_id IN (?) AND ` + v + `
		GROUP BY parent_id
		LIMIT 5000
		SETTINGS max_execution_time = 10`, args
}

// EntityChildrenCountsByParents — {parent_id: adet}; parents boşsa boş.
func (s *Store) EntityChildrenCountsByParents(ctx context.Context, cid, typ string, parents []string, at time.Time) (map[string]int, error) {
	if len(parents) == 0 {
		return map[string]int{}, nil
	}
	if len(parents) > 500 {
		parents = parents[:500]
	}
	sql, args := entityChildrenCountsByParentsSQL(cid, typ, parents, at)
	rows, err := s.conn.Query(ctx, sql, args...)
	if err != nil {
		return nil, fmt.Errorf("entity children by parents: %w", err)
	}
	defer rows.Close()
	out := map[string]int{}
	for rows.Next() {
		var p string
		var n uint64
		if err := rows.Scan(&p, &n); err != nil {
			return nil, err
		}
		out[p] = int(n)
	}
	return out, rows.Err()
}

// EntitySeenService — namespace içindeki bir service.name'in telemetri özeti.
type EntitySeenService struct {
	Service  string    `json:"service"`
	Pods     int       `json:"pods"`
	Spans    int64     `json:"spans"`
	Errors   int64     `json:"errors"`
	LastSeen time.Time `json:"lastSeen"`
}

// entitySeenServicesByNamespaceSQL — namespace → service.name'ler (telemetri
// tarafı). ORDER BY öneki service_name olduğu için bu okuma MV'nin gün
// partition'ını tarar; pencere 5 dk kovalı, LIMIT 200, 10 s tavan. Saf.
func entitySeenServicesByNamespaceSQL(clusterValues []string, namespace string, from, to time.Time) (string, []any) {
	return `SELECT service_name,
	       toInt32(uniqExact(k8s_pod)) AS pods,
	       toInt64(countMerge(span_count_state)) AS spans,
	       toInt64(countIfMerge(error_count_state)) AS errors,
	       maxMerge(last_seen_state) AS last_seen
		FROM entity_seen_5m
		WHERE cluster IN (?) AND k8s_namespace = ?
		  AND time_bucket >= toStartOfFiveMinute(?) AND time_bucket <= ?
		GROUP BY service_name
		ORDER BY spans DESC
		LIMIT 200
		SETTINGS max_execution_time = 10`, []any{clusterValues, namespace, from, to}
}

// EntitySeenServicesByNamespace — telemetri gönderen service.name'ler; span
// cluster DEĞERLERİ (SpanClusterKeys) ile; boş değer listesi → boş sonuç.
func (s *Store) EntitySeenServicesByNamespace(ctx context.Context, clusterValues []string, namespace string, from, to time.Time) ([]EntitySeenService, error) {
	if len(clusterValues) == 0 || namespace == "" {
		return []EntitySeenService{}, nil
	}
	sql, args := entitySeenServicesByNamespaceSQL(clusterValues, namespace, from, to)
	rows, err := s.conn.Query(ctx, sql, args...)
	if err != nil {
		return nil, fmt.Errorf("entity seen services: %w", err)
	}
	defer rows.Close()
	out := []EntitySeenService{}
	for rows.Next() {
		var r EntitySeenService
		var pods int32
		if err := rows.Scan(&r.Service, &pods, &r.Spans, &r.Errors, &r.LastSeen); err != nil {
			return nil, err
		}
		r.Pods = int(pods)
		out = append(out, r)
	}
	return out, rows.Err()
}
