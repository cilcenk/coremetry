package chstore

import (
	"context"
	"time"
)

// ServiceAttrRow surfaces the per-key + sample-value
// distribution of attrs the operator's spans actually emit
// for a given service. Powers the new /services/X/attrs view
// (v0.5.380) — answers "what attrs is my SDK actually
// putting on these spans" without opening a single trace and
// squinting.
//
// Scope distinguishes span attrs (per-span data: http.route,
// db.statement, rpc.method) from resource attrs (per-process
// data: service.namespace, host.name, k8s.pod.name). The
// distinction matters when picking attrs to use in queries —
// resource attrs are stable, span attrs vary per request.
type ServiceAttrRow struct {
	Key          string   `json:"key"`
	Scope        string   `json:"scope"` // "span" | "resource"
	Occurrences  uint64   `json:"occurrences"`
	SampleValues []string `json:"sampleValues"`
}

// GetServiceAttrs samples spans for the given service in the
// window and surfaces the top attr keys + a few sample values
// each. Bounded by an inner LIMIT on the sample size so the
// scan stays fast regardless of fleet volume (the operator
// cares about "what shapes of attrs does this service emit",
// not "every attr value across 30 days" — 5k sampled spans
// produce a thoroughly representative picture).
//
// Returns up to topPerScope keys per scope (span + resource)
// ranked by occurrence count. sampleLimit controls how many
// distinct values per key the result carries — typical
// operator use surfaces 3-5 examples to confirm the format.
func (s *Store) GetServiceAttrs(
	ctx context.Context, service string, from, to time.Time,
	topPerScope, sampleLimit int,
) ([]ServiceAttrRow, error) {
	if topPerScope <= 0 || topPerScope > 200 {
		topPerScope = 50
	}
	if sampleLimit <= 0 || sampleLimit > 50 {
		sampleLimit = 5
	}
	if from.IsZero() {
		from = time.Now().Add(-1 * time.Hour)
	}
	if to.IsZero() {
		to = time.Now()
	}
	const innerSpanLimit = 5000

	// Two parallel queries — one per scope. Different array
	// columns, same shape. We could UNION ALL in SQL but two
	// round-trips keep the per-query memory bound modest and
	// the code simpler.
	queries := []struct {
		scope string
		keys  string
		vals  string
	}{
		{"span", "attr_keys", "attr_values"},
		{"resource", "res_keys", "res_values"},
	}
	out := make([]ServiceAttrRow, 0, topPerScope*2)
	for _, q := range queries {
		rows, err := s.conn.Query(ctx, `
			SELECT k, count() AS occurrences,
			       arrayDistinct(groupArray(?)(v)) AS sample_values
			FROM (
			  SELECT `+q.keys+`[idx] AS k,
			         `+q.vals+`[idx] AS v
			  FROM (
			    SELECT `+q.keys+`, `+q.vals+`,
			           arrayJoin(range(1, length(`+q.keys+`) + 1)) AS idx
			    FROM spans
			    WHERE service_name = ?
			      AND time >= ? AND time <= ?
			    LIMIT ?
			  )
			  WHERE k != '' AND v != ''
			)
			GROUP BY k
			ORDER BY occurrences DESC
			LIMIT ?
			SETTINGS max_execution_time = 10`,
			sampleLimit,
			service, from, to, innerSpanLimit,
			topPerScope)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var r ServiceAttrRow
			r.Scope = q.scope
			if err := rows.Scan(&r.Key, &r.Occurrences, &r.SampleValues); err != nil {
				rows.Close()
				return nil, err
			}
			out = append(out, r)
		}
		rows.Close()
	}
	return out, nil
}
