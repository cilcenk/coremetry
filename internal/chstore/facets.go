package chstore

import (
	"context"
	"strings"
	"time"
)

// Facet — one tag dimension (service.name, http.route, db.system, …)
// with its top-N values for the window. Drives the /explore facets
// panel: operator scans which tags exist, sees the frequency of
// each value, clicks one to add it as a filter chip. Datadog's
// "trace tag explorer" view, Honeycomb's facet sidebar.
type Facet struct {
	Key            string       `json:"key"`           // human-facing facet name (e.g. "service.name")
	DistinctValues int64        `json:"distinctValues"` // total uniques across the window
	Values         []FacetValue `json:"values"`         // top-N by count, descending
}

type FacetValue struct {
	Value string `json:"value"`
	Count int64  `json:"count"`
}

// facetCol is one entry of the well-known-column facet table. The
// SQL projection is the column expression CH applies before COUNT;
// using `nullIf` for "unset" / "" lets us skip blank values without
// a separate WHERE clause that would bypass the LowCardinality
// dict optimisation.
//
// The wellKnownFacets list deliberately mirrors what an operator
// scans during incident triage; adding more (esp. RPC method,
// peer.service) is one-line cheap, but each one is a separate CH
// pass so we keep the list to high-signal columns.
type facetCol struct {
	key  string // user-visible name
	expr string // CH column or expression — referenced in SELECT col, count() FROM …
}

var wellKnownFacets = []facetCol{
	{"service.name", "service_name"},
	{"deployment.environment", "deploy_env"},
	{"host.name", "host_name"},
	{"span.kind", "kind"},
	{"status_code", "status_code"},
	{"http.method", "http_method"},
	{"http.route", "http_route"},
	{"http.status_code", "toString(http_status_code)"},
	{"db.system", "db_system"},
	{"messaging.system", "msg_system"},
	{"rpc.system", "rpc_system"},
}

// GetSpanFacets returns top-N values for each well-known column the
// operator typically pivots on. Caller passes the surrounding
// filter (e.g. "service.name = api" — already chosen in the
// /explore page) and the time window; we run one COUNT GROUP BY
// per facet column.
//
// topValues controls how many values per facet (default 8); we hard-
// cap the total row count via LIMIT so a wide range doesn't return
// 10k cardinality on host.name. The query plan benefits from the
// LowCardinality dict on every well-known column so the actual scan
// is cheap even at billion-span scale — the only honest cost is the
// network round-trip per facet.
// root is the optional grouped AND/OR builder (v0.8.x gap-2). When non-nil
// it SUPERSEDES filters (ApplyFilterGroup vs ApplyFilters); a flat-AND root
// is byte-identical to the legacy filters path. Existing callers pass nil.
func (s *Store) GetSpanFacets(
	ctx context.Context,
	filters []FilterExpr,
	root *FilterGroup,
	from, to time.Time,
	topValues int,
) ([]Facet, error) {
	if topValues <= 0 || topValues > 50 {
		topValues = 8
	}
	var wc whereClause
	if !from.IsZero() {
		wc.add("time >= ?", from)
	}
	if !to.IsZero() {
		wc.add("time <= ?", to)
	}
	if root != nil {
		ApplyFilterGroup(&wc, *root)
	} else {
		ApplyFilters(&wc, filters)
	}

	out := make([]Facet, 0, len(wellKnownFacets))
	for _, fc := range wellKnownFacets {
		f, err := s.facetOne(ctx, fc, wc, topValues)
		if err != nil {
			// One bad facet shouldn't poison the panel — log the
			// failure via the dropped row and keep going. The most
			// common failure mode is "column rename mid-migration"
			// which is recoverable on the next deploy.
			continue
		}
		if len(f.Values) == 0 {
			continue
		}
		out = append(out, f)
	}
	return out, nil
}

func (s *Store) facetOne(ctx context.Context, fc facetCol, wc whereClause, topValues int) (Facet, error) {
	// Two queries per facet:
	//   1. SELECT col, count() ... GROUP BY col ORDER BY 2 DESC LIMIT N
	//   2. SELECT uniq(col) — for the "DistinctValues" total so the UI
	//      can show "12 values · top 8 below". Cheap (uniq is HLL).
	//
	// We can fuse these with a UNION ALL but the readability loss isn't
	// worth the one round-trip saved at this scale; the well-known cols
	// have LowCardinality dicts so each call is sub-50ms.
	q := `
		SELECT ` + fc.expr + ` AS v, count() AS c
		FROM spans ` + wc.sql() + `
		` + havingExpr(fc.expr) + `
		GROUP BY v
		ORDER BY c DESC
		LIMIT ?
		SETTINGS max_execution_time = 5`
	args := append([]any{}, wc.args...)
	args = append(args, topValues)
	rows, err := s.conn.Query(ctx, q, args...)
	if err != nil {
		return Facet{}, err
	}
	f := Facet{Key: fc.key}
	for rows.Next() {
		var v string
		var c int64
		if err := rows.Scan(&v, &c); err != nil {
			rows.Close()
			return f, err
		}
		f.Values = append(f.Values, FacetValue{Value: v, Count: c})
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return f, err
	}

	// uniq for total distinct count.
	uq := `
		SELECT uniq(` + fc.expr + `) FROM spans ` + wc.sql() + `
		` + havingExpr(fc.expr) + `
		SETTINGS max_execution_time = 5`
	if row := s.conn.QueryRow(ctx, uq, wc.args...); row != nil {
		var n int64
		if err := row.Scan(&n); err == nil {
			f.DistinctValues = n
		}
	}
	return f, nil
}

// havingExpr appends a WHERE-fragment that excludes the standard
// "no data" sentinels for the given column expression. status_code's
// "unset" is OTel's default for spans that never had a status set —
// filtering it out keeps the panel from being dominated by
// "everything is unset" which is the typical case for db / messaging
// spans. Likewise blank string is the default for unset
// LowCardinality columns; we skip those.
//
// Returns either "AND ..." (extends the wc.sql() WHERE) or "". The
// helper composes cleanly with wc.sql() because wc.sql() always
// produces a WHERE clause whenever it's called with predicates, and
// callers ensure at least the `time` predicate is set.
func havingExpr(expr string) string {
	// For numeric columns (toString-wrapped) we filter the cast output;
	// for plain string columns, the same fragment works.
	parts := []string{
		"AND " + expr + " != ''",
	}
	// Some defaults are special and worth excluding — they pad the
	// list with noise the operator already knows isn't actionable.
	switch {
	case strings.Contains(expr, "status_code"):
		parts = append(parts, "AND "+expr+" != 'unset'")
	}
	return strings.Join(parts, " ")
}
