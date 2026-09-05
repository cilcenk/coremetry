package chstore

// topology_edge_series.go — v0.10.438 (CoSRE router boşlukları D3): tek bir
// yönlü kenarın (parent → child) 5 dk kovalı çağrı serisi. Periyot sorusu
// ("A'dan B'ye her 5 dk gibi istek atılıyor mu") için MV-first kaynak;
// ham spans'ta parent→child join'i YOK (A'nın 1 saatlik span_id kümesi
// IN alt-sorgusu bellek riski). 5 dk kova 10 dk altındaki periyodu
// göremez (Nyquist) — çağıran bunu ilan eder; dakikalık taraf servis
// kapsamlı QuerySpanMetric(count, 60 sn) ile ayrı okunur.

import (
	"context"
	"time"
)

// EdgeSeriesPoint — kova başlangıcı (unix ns) + çağrı/hata.
type EdgeSeriesPoint struct {
	Time   int64  `json:"time"`
	Calls  uint64 `json:"calls"`
	Errors uint64 `json:"errors"`
}

// topologyEdgeSeriesSQL — SAF (test pinler): zaman sınırlı WHERE, FINAL,
// LIMIT, max_execution_time. child_node üç önekle de eşlenir (servis düz
// ad, ext:/db:/q: düğümler).
const topologyEdgeSeriesSQL = `
		SELECT time_bucket, sum(calls), sum(errors)
		FROM topology_edges_5m FINAL
		WHERE parent_service = ?
		  AND child_node IN (?, ?, ?, ?)
		  AND ` + TopoCallEdgeFilterSQL + `
		  AND time_bucket >= ? AND time_bucket < ?
		GROUP BY time_bucket
		ORDER BY time_bucket
		LIMIT 2000
		SETTINGS max_execution_time = 10`

// TopologyEdgeSeries — parent → child kenarının 5 dk serisi [from, to).
func (s *Store) TopologyEdgeSeries(ctx context.Context, parent, child string, from, to time.Time) ([]EdgeSeriesPoint, error) {
	if parent == "" || child == "" || !to.After(from) {
		return nil, nil
	}
	rows, err := s.conn.Query(ctx, topologyEdgeSeriesSQL, parent, child, "ext:"+child, "db:"+child, "q:"+child,
		from.Truncate(5*time.Minute), to)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []EdgeSeriesPoint
	for rows.Next() {
		var t time.Time
		var p EdgeSeriesPoint
		if err := rows.Scan(&t, &p.Calls, &p.Errors); err != nil {
			return nil, err
		}
		p.Time = t.UnixNano()
		out = append(out, p)
	}
	return out, rows.Err()
}
