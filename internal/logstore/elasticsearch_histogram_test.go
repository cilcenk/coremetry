package logstore

import "testing"

// v0.8.3 — operator-reported: on an external Elasticsearch logs backend
// the /api/logs/timeseries histogram drove a continuous api-pod CPU
// climb to pod restart, while the identical query on the bundled
// ClickHouse backend was fine. Root cause: the ES Histogram body diverged
// from the CH path's cost guards —
//   - min_doc_count:0 materialised EVERY empty interval as a bucket, once
//     per severity term (≤50) + the total band → a dense grid the api pod
//     parsed into a LogPoint each (CH only ever returns non-empty buckets);
//   - no ES soft "timeout" (significant_text already had one) so a slow
//     agg on a billion-doc index held the coordinator slot to the full
//     caller deadline;
//   - no track_total_hits:false so ES counted every matching doc for an
//     agg-only request.
//
// These table-driven assertions exercise buildHistogramBody for every
// groupBy shape (CLAUDE.md "exercise every branch") and fail if any guard
// regresses. They pin the exact divergence that caused the incident.

// dateHistOf pulls the date_histogram sub-map out of a {"date_histogram": …}
// agg wrapper, failing the test if the shape is unexpected.
func dateHistOf(t *testing.T, agg any) map[string]any {
	t.Helper()
	m, ok := agg.(map[string]any)
	if !ok {
		t.Fatalf("agg is not a map: %T", agg)
	}
	dh, ok := m["date_histogram"].(map[string]any)
	if !ok {
		t.Fatalf("agg has no date_histogram map: %#v", m)
	}
	return dh
}

func TestBuildHistogramBody_CostGuards(t *testing.T) {
	const ts = "@timestamp"
	const timeout = "10s"

	cases := []struct {
		name       string
		groupField string
		bucketSec  int
		wantIntvl  string
	}{
		{name: "ungrouped total", groupField: "", bucketSec: 30, wantIntvl: "30s"},
		{name: "grouped severity", groupField: "log.level.keyword", bucketSec: 300, wantIntvl: "300s"},
		{name: "grouped service", groupField: "service.name.keyword", bucketSec: 5, wantIntvl: "5s"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			body := buildHistogramBody(map[string]any{"match_all": map[string]any{}}, ts, tc.groupField, tc.bucketSec, timeout)

			// Top-level ES-cost guards (the inner bound that stops the
			// coordinator hang + the agg-only count).
			if got := body["timeout"]; got != timeout {
				t.Errorf("timeout = %v, want %q (ES soft-timeout missing → unbounded coordinator hold)", got, timeout)
			}
			if got, ok := body["track_total_hits"].(bool); !ok || got {
				t.Errorf("track_total_hits = %v, want false (agg-only request must not count all docs)", body["track_total_hits"])
			}
			if body["size"] != 0 {
				t.Errorf("size = %v, want 0 (histogram is agg-only)", body["size"])
			}

			aggs, ok := body["aggs"].(map[string]any)
			if !ok {
				t.Fatalf("body has no aggs map: %#v", body)
			}

			// Collect every date_histogram in the tree — the one shape
			// that caused the incident (min_doc_count:0) must be 1
			// everywhere it appears.
			var dateHists []map[string]any
			if tc.groupField == "" {
				dateHists = append(dateHists, dateHistOf(t, aggs["buckets"]))
			} else {
				groups, ok := aggs["groups"].(map[string]any)
				if !ok {
					t.Fatalf("grouped body has no groups agg: %#v", aggs)
				}
				// terms agg present + capped so we keep the severity coverage
				terms, ok := groups["terms"].(map[string]any)
				if !ok {
					t.Fatalf("groups has no terms agg: %#v", groups)
				}
				if terms["field"] != tc.groupField {
					t.Errorf("terms field = %v, want %q", terms["field"], tc.groupField)
				}
				inner, ok := groups["aggs"].(map[string]any)
				if !ok {
					t.Fatalf("groups has no inner aggs: %#v", groups)
				}
				dateHists = append(dateHists, dateHistOf(t, inner["buckets"]))
				// the parallel total-band histogram used to synthesise OTHER
				dateHists = append(dateHists, dateHistOf(t, aggs["total_buckets"]))
			}

			for i, dh := range dateHists {
				// THE regression guard: 0 → 1. 0 makes ES emit a bucket for
				// every empty interval, per term — the dense grid that OOM'd
				// the api pod at billion-doc scale.
				if mdc := dh["min_doc_count"]; mdc != 1 {
					t.Errorf("date_histogram[%d] min_doc_count = %v, want 1 (0 materialises every empty bucket → dense grid → api-pod CPU climb)", i, mdc)
				}
				if iv := dh["fixed_interval"]; iv != tc.wantIntvl {
					t.Errorf("date_histogram[%d] fixed_interval = %v, want %q", i, iv, tc.wantIntvl)
				}
				if dh["field"] != ts {
					t.Errorf("date_histogram[%d] field = %v, want %q", i, dh["field"], ts)
				}
			}
		})
	}
}
