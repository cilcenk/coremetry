package logstore

import "testing"

// v0.8.377 — Operator-reported: the log severity histogram (error/
// warn/info stacked bands on /logs and the service Logs tab, both via
// GET /api/logs/timeseries?groupBy=severity) showed wrong band counts
// on the external-ES test env. Root cause: the old shape ran a terms
// agg on ONE configured text field (SeverityTx+".keyword") while row
// extraction fell back across four candidates (level, log.level,
// severity_text, severity) and an optional numeric field — logs whose
// severity lives elsewhere vanished from bands or landed in OTHER,
// and raw values (numeric strings, exotic casing) leaked to the
// frontend which mis-banded them.
//
// Fix: buildSeverityHistogramBody replaces the terms agg with a fixed
// keyed `filters` aggregation — one filter per canonical band (ERROR/
// WARN/INFO/DEBUG/TRACE), each a bool.should over case-insensitive
// `prefix` queries on the DEDUPED candidate keyword fields plus (when
// configured) an OTel severity_number range. These table-driven
// assertions pin the band definitions AND every v0.8.3 cost guard.

// wantBandRanges — the OTel severity_number band boundaries. Keep in
// lockstep with severityBands (elasticsearch.go), chSeverityBandExpr
// (clickhouse.go) and severityBandOf (frontend lib/severityBand.ts).
var wantBandRanges = map[string][2]int{
	"ERROR": {17, 24},
	"WARN":  {13, 16},
	"INFO":  {9, 12},
	"DEBUG": {5, 8},
	"TRACE": {1, 4},
}

var wantBandPrefixes = map[string][]string{
	"ERROR": {"fatal", "err"}, // err catches err / error / error: / erro…
	"WARN":  {"warn"},         // warn / warning
	"INFO":  {"info"},         // info / information
	"DEBUG": {"debug"},
	"TRACE": {"trace"},
}

func TestSeverityCandidateKeywordFields_Dedupe(t *testing.T) {
	cases := []struct {
		name       string
		severityTx string
		want       []string
	}{
		{
			// Default config: "log.level" collides with the hard-coded
			// fallback — must appear ONCE.
			name:       "default log.level dedupes",
			severityTx: "log.level",
			want:       []string{"log.level.keyword", "level.keyword", "severity_text.keyword", "severity.keyword"},
		},
		{
			name:       "custom field prepends",
			severityTx: "app.sev",
			want:       []string{"app.sev.keyword", "level.keyword", "log.level.keyword", "severity_text.keyword", "severity.keyword"},
		},
		{
			// Collision with a non-first fallback still dedupes.
			name:       "configured equals severity_text",
			severityTx: "severity_text",
			want:       []string{"severity_text.keyword", "level.keyword", "log.level.keyword", "severity.keyword"},
		},
		{
			name:       "empty configured field skipped",
			severityTx: "",
			want:       []string{"level.keyword", "log.level.keyword", "severity_text.keyword", "severity.keyword"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := severityCandidateKeywordFields(tc.severityTx)
			if len(got) != len(tc.want) {
				t.Fatalf("got %v, want %v", got, tc.want)
			}
			for i := range got {
				if got[i] != tc.want[i] {
					t.Errorf("field[%d] = %q, want %q (full: %v)", i, got[i], tc.want[i], got)
				}
			}
		})
	}
}

// bandFiltersOf digs aggs.groups.filters.filters out of the body.
func bandFiltersOf(t *testing.T, body map[string]any) map[string]any {
	t.Helper()
	aggs, ok := body["aggs"].(map[string]any)
	if !ok {
		t.Fatalf("body has no aggs: %#v", body)
	}
	groups, ok := aggs["groups"].(map[string]any)
	if !ok {
		t.Fatalf("aggs has no groups: %#v", aggs)
	}
	fw, ok := groups["filters"].(map[string]any)
	if !ok {
		t.Fatalf("groups is not a filters agg (terms regression?): %#v", groups)
	}
	bands, ok := fw["filters"].(map[string]any)
	if !ok {
		t.Fatalf("filters agg has no keyed filters map: %#v", fw)
	}
	return bands
}

// shouldClausesOf digs the bool.should slice out of one band filter.
func shouldClausesOf(t *testing.T, band string, f any) []any {
	t.Helper()
	m, ok := f.(map[string]any)
	if !ok {
		t.Fatalf("band %s filter is not a map: %#v", band, f)
	}
	b, ok := m["bool"].(map[string]any)
	if !ok {
		t.Fatalf("band %s filter has no bool: %#v", band, m)
	}
	if msm := b["minimum_should_match"]; msm != 1 {
		t.Errorf("band %s minimum_should_match = %v, want 1", band, msm)
	}
	sh, ok := b["should"].([]any)
	if !ok {
		t.Fatalf("band %s bool has no should slice: %#v", band, b)
	}
	return sh
}

func TestBuildSeverityHistogramBody_Bands(t *testing.T) {
	cands := severityCandidateKeywordFields("log.level")

	cases := []struct {
		name       string
		severityNo string
		wantRange  bool
	}{
		{name: "severityNo configured adds range clause", severityNo: "severity_number", wantRange: true},
		{name: "severityNo absent omits range clause", severityNo: "", wantRange: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			body := buildSeverityHistogramBody(
				map[string]any{"match_all": map[string]any{}},
				"@timestamp", cands, tc.severityNo, 30, "10s")
			bands := bandFiltersOf(t, body)

			if len(bands) != len(wantBandPrefixes) {
				t.Errorf("band count = %d, want %d (%#v)", len(bands), len(wantBandPrefixes), bands)
			}
			for band, prefixes := range wantBandPrefixes {
				f, ok := bands[band]
				if !ok {
					t.Errorf("band %s missing from filters agg", band)
					continue
				}
				should := shouldClausesOf(t, band, f)

				wantN := len(cands) * len(prefixes)
				if tc.wantRange {
					wantN++
				}
				if len(should) != wantN {
					t.Errorf("band %s should-clause count = %d, want %d", band, len(should), wantN)
				}

				// Every (candidate field × prefix) pair present as a
				// case-insensitive prefix query. query_string cannot do
				// case_insensitive on ES 8.x (house pitfall) — prefix can.
				seenPrefix := map[string]map[string]bool{} // field → prefix → seen
				var gotRange map[string]any
				for _, c := range should {
					cm, _ := c.(map[string]any)
					if p, ok := cm["prefix"].(map[string]any); ok {
						for field, spec := range p {
							sm, _ := spec.(map[string]any)
							if ci, _ := sm["case_insensitive"].(bool); !ci {
								t.Errorf("band %s prefix on %s lacks case_insensitive:true", band, field)
							}
							v, _ := sm["value"].(string)
							if seenPrefix[field] == nil {
								seenPrefix[field] = map[string]bool{}
							}
							seenPrefix[field][v] = true
						}
					}
					if rng, ok := cm["range"].(map[string]any); ok {
						gotRange = rng
					}
				}
				for _, field := range cands {
					for _, p := range prefixes {
						if !seenPrefix[field][p] {
							t.Errorf("band %s missing prefix %q on field %s", band, p, field)
						}
					}
				}

				if tc.wantRange {
					if gotRange == nil {
						t.Errorf("band %s missing severity_number range clause", band)
					} else {
						spec, _ := gotRange[tc.severityNo].(map[string]any)
						want := wantBandRanges[band]
						if spec == nil || spec["gte"] != want[0] || spec["lte"] != want[1] {
							t.Errorf("band %s range = %#v, want gte=%d lte=%d on %s",
								band, gotRange, want[0], want[1], tc.severityNo)
						}
					}
				} else if gotRange != nil {
					t.Errorf("band %s has a range clause %#v but severityNo is unconfigured", band, gotRange)
				}
			}
		})
	}
}

func TestBuildSeverityHistogramBody_CostGuards(t *testing.T) {
	body := buildSeverityHistogramBody(
		map[string]any{"match_all": map[string]any{}},
		"@timestamp", severityCandidateKeywordFields("log.level"),
		"severity_number", 300, "10s")

	if got := body["timeout"]; got != "10s" {
		t.Errorf("timeout = %v, want 10s (ES soft-timeout missing → unbounded coordinator hold)", got)
	}
	if got, ok := body["track_total_hits"].(bool); !ok || got {
		t.Errorf("track_total_hits = %v, want false (agg-only request must not count all docs)", body["track_total_hits"])
	}
	if body["size"] != 0 {
		t.Errorf("size = %v, want 0 (histogram is agg-only)", body["size"])
	}

	aggs := body["aggs"].(map[string]any)
	groups := aggs["groups"].(map[string]any)
	inner, ok := groups["aggs"].(map[string]any)
	if !ok {
		t.Fatalf("groups has no inner aggs: %#v", groups)
	}
	// Both date_histograms (per-band + total for the OTHER synthesis)
	// carry the v0.8.3 guards.
	for i, agg := range []any{inner["buckets"], aggs["total_buckets"]} {
		dh := dateHistOf(t, agg)
		if mdc := dh["min_doc_count"]; mdc != 1 {
			t.Errorf("date_histogram[%d] min_doc_count = %v, want 1 (dense-grid incident guard)", i, mdc)
		}
		if iv := dh["fixed_interval"]; iv != "300s" {
			t.Errorf("date_histogram[%d] fixed_interval = %v, want 300s", i, iv)
		}
		if dh["field"] != "@timestamp" {
			t.Errorf("date_histogram[%d] field = %v, want @timestamp", i, dh["field"])
		}
	}
}
