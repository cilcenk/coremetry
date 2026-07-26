package chstore

import (
	"errors"
	"testing"
)

// v0.9.285 — /metrics' first paint went straight to the raw
// metric_points scan.
//
// The catalog-first read (v0.8.396) guards the raw scan behind a probe
// that asks "is metric_catalog actually empty, or did this search just
// match nothing?". That probe sat behind `pattern != "" || service !=
// ""` — and /metrics' first paint sends BOTH empty
// (api.metricNamesSearch('', ...)). So on the one call every operator
// makes, on every page load, the probe never ran and the escalation
// fired: `count(DISTINCT metric) FROM metric_points WHERE time >=
// now()-7d` plus a `GROUP BY metric`. That is the query behind two prod
// incidents (v0.8.311, v0.8.396); on a toy install it already reads
// 8.87M rows / 76 MiB.
//
// The decision is a named function over three booleans precisely
// because the bug was a MISSING ROW in this table — the
// empty-result/populated-catalog/no-search-terms case fell through. An
// inline condition is where that kind of gap hides.
func TestDecideMetricNamesSource(t *testing.T) {
	boom := errors.New("metric_catalog: no such table")

	cases := []struct {
		name           string
		catalogErr     error
		rows           int
		catalogHasRows bool
		want           metricNamesSource
	}{
		{
			// THE regression. /metrics first paint: no pattern, no
			// service, catalog populated but this window matched
			// nothing. Pre-fix this reached the raw scan.
			name:           "empty result over a populated catalog is authoritative",
			rows:           0,
			catalogHasRows: true,
			want:           metricNamesUseCatalog,
		},
		{
			name:           "genuinely empty catalog escalates (fresh-upgrade window)",
			rows:           0,
			catalogHasRows: false,
			want:           metricNamesRawScan,
		},
		{
			name:           "catalog answered — never escalate",
			rows:           7,
			catalogHasRows: true,
			want:           metricNamesUseCatalog,
		},
		{
			name:           "catalog unreadable — escalate, nothing else to try",
			catalogErr:     boom,
			rows:           0,
			catalogHasRows: false,
			want:           metricNamesRawScan,
		},
		{
			// A failed probe reports false, but the read error already
			// decided this; the error must win regardless of the probe.
			name:           "read error beats a probe that somehow saw rows",
			catalogErr:     boom,
			rows:           0,
			catalogHasRows: true,
			want:           metricNamesRawScan,
		},
		{
			// Defensive: rows returned alongside an error is a shape we
			// never expect, but the error still means "don't trust it".
			name:           "rows alongside an error are not trusted",
			catalogErr:     boom,
			rows:           3,
			catalogHasRows: true,
			want:           metricNamesRawScan,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := decideMetricNamesSource(tc.catalogErr, tc.rows, tc.catalogHasRows)
			if got != tc.want {
				t.Fatalf("decideMetricNamesSource(err=%v, rows=%d, hasRows=%v) = %v, want %v",
					tc.catalogErr, tc.rows, tc.catalogHasRows, got, tc.want)
			}
		})
	}
}

// The raw scan is the most expensive query /metrics can issue. Pin the
// only two ways to reach it, so a future condition cannot quietly widen
// the door: the catalog errored, or the catalog is empty.
func TestRawMetricScanHasExactlyTwoEntrances(t *testing.T) {
	reached := 0
	for _, catErr := range []error{nil, errors.New("x")} {
		for _, rows := range []int{0, 5} {
			for _, hasRows := range []bool{false, true} {
				if decideMetricNamesSource(catErr, rows, hasRows) == metricNamesRawScan {
					reached++
					if catErr == nil && hasRows {
						t.Fatalf("raw scan reached with a populated, readable catalog (rows=%d)", rows)
					}
				}
			}
		}
	}
	// err×{rows 0,5}×{hasRows false,true} = 4, plus nil-err/empty-catalog
	// at rows=0 = 1. rows>0 with a readable catalog never escalates.
	if reached != 5 {
		t.Fatalf("raw scan reachable from %d input combinations, want 5", reached)
	}
}
