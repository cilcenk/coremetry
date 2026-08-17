package vmetrics

// v0.9.1159 — metric-NAME candidates. Operator-reported: every VM panel was
// empty in prod because Coremetry asked for `http.server.request.duration`
// while VM held `http_server_request_duration_seconds_bucket`.
//
// Every expectation in this file is HAND-WRITTEN from the OTel→Prometheus
// naming convention, never derived from promUnitSuffixes or nameSpellings. A
// test that asked the implementation which spellings it produces would pass
// while both were wrong — the same rule the window pins in promql_test.go
// follow, and it matters more here: a wrong candidate list is invisible (an
// empty panel), never an error.
//
// Four properties are pinned harder than the rest because each one fails
// SILENTLY:
//
//	ORDER + DEDUPE  — the alternation text rides in VM's query log and rule 3
//	                  in promql.go requires two polls of one panel to render
//	                  the same string. A map-derived list would relabel the
//	                  same query on every scrape.
//	REGEX ESCAPING  — an unescaped `.` makes `http.server` match
//	                  `httpxserver`; the alternation would silently WIDEN
//	                  instead of failing.
//	`=` FOR ONE     — a name that needs no guessing must render byte-for-byte
//	                  the query that shipped before v0.9.1159.
//	IDEMPOTENCE     — a VM-catalogue row is already exact, and `…_seconds_seconds`
//	                  or `…_bucket_ratio` are names no write path can produce.

import (
	"strings"
	"testing"
)

// ── Shared selector pins ────────────────────────────────────────────────────
//
// The `__name__` matcher text for the fixture names promql_test.go and
// histogram_test.go build their expression pins from. Factored out because a
// ten-member alternation repeated inline forty times is unreadable, NOT
// because it is derived: every member below is typed out by hand from the
// convention, in the order nameSpellings is specified to emit, and
// TestNameCandidates pins the same lists item by item. If these constants and
// that table ever disagree, one of them is wrong and the suite says so.
//
// Note the DOUBLED backslash. Two escapes are stacked here and each has to
// happen exactly once: regexp.QuoteMeta turns `.` into `\.` so the regex
// matches a literal dot, then quotePromString turns that `\` into `\\` so the
// PromQL string literal survives parsing. VM unescapes back to `\.`.
const (
	// http.server.request.duration — the operator's metric. Plain
	// aggregations read the base spellings; the third and fourth members are
	// the OTel-Prometheus unit forms, and `…_seconds_bucket` (below) is what
	// their install actually holds.
	mcHTTPDur = `__name__=~"^(http\\.server\\.request\\.duration|` +
		`http_server_request_duration|http_server_request_duration_seconds|` +
		`http_server_request_duration_milliseconds|http_server_request_duration_bytes|` +
		`http_server_request_duration_ratio|http_server_request_duration_total|` +
		`http_server_request_duration_seconds_total|` +
		`http_server_request_duration_milliseconds_total|` +
		`http_server_request_duration_bytes_total)$"`

	// Same metric, percentile/heatmap path: `_bucket` composes last and no
	// `_total` appears (a histogram is not a monotonic sum).
	mcHTTPDurBucket = `__name__=~"^(http\\.server\\.request\\.duration_bucket|` +
		`http_server_request_duration_bucket|` +
		`http_server_request_duration_seconds_bucket|` +
		`http_server_request_duration_milliseconds_bucket|` +
		`http_server_request_duration_bytes_bucket|` +
		`http_server_request_duration_ratio_bucket)$"`

	// The already-sanitised spelling: verbatim and sanitised collide and
	// dedupe, so this list is one member shorter than mcHTTPDur.
	mcHTTPDurUnderscore = `__name__=~"^(http_server_request_duration|` +
		`http_server_request_duration_seconds|` +
		`http_server_request_duration_milliseconds|` +
		`http_server_request_duration_bytes|http_server_request_duration_ratio|` +
		`http_server_request_duration_total|` +
		`http_server_request_duration_seconds_total|` +
		`http_server_request_duration_milliseconds_total|` +
		`http_server_request_duration_bytes_total)$"`

	mcJVM = `__name__=~"^(jvm\\.memory\\.used|jvm_memory_used|` +
		`jvm_memory_used_seconds|jvm_memory_used_milliseconds|` +
		`jvm_memory_used_bytes|jvm_memory_used_ratio|jvm_memory_used_total|` +
		`jvm_memory_used_seconds_total|jvm_memory_used_milliseconds_total|` +
		`jvm_memory_used_bytes_total)$"`

	mcHTTPReq = `__name__=~"^(http\\.server\\.requests|http_server_requests|` +
		`http_server_requests_seconds|http_server_requests_milliseconds|` +
		`http_server_requests_bytes|http_server_requests_ratio|` +
		`http_server_requests_total|http_server_requests_seconds_total|` +
		`http_server_requests_milliseconds_total|http_server_requests_bytes_total)$"`

	// The `_count` arm of the same name (v0.9.1160).
	mcHTTPReqCount = `__name__=~"^(http\\.server\\.requests_count|` +
		`http_server_requests_count|http_server_requests_seconds_count|` +
		`http_server_requests_milliseconds_count|http_server_requests_bytes_count|` +
		`http_server_requests_ratio_count)$"`

	// The two arms for the operator's metric — the `avg by (route)` panel that
	// was empty in prod resolves through these.
	mcHTTPDurSum = `__name__=~"^(http\\.server\\.request\\.duration_sum|` +
		`http_server_request_duration_sum|http_server_request_duration_seconds_sum|` +
		`http_server_request_duration_milliseconds_sum|` +
		`http_server_request_duration_bytes_sum|` +
		`http_server_request_duration_ratio_sum)$"`
	mcHTTPDurCount = `__name__=~"^(http\\.server\\.request\\.duration_count|` +
		`http_server_request_duration_count|` +
		`http_server_request_duration_seconds_count|` +
		`http_server_request_duration_milliseconds_count|` +
		`http_server_request_duration_bytes_count|` +
		`http_server_request_duration_ratio_count)$"`

	mcJVMSum = `__name__=~"^(jvm\\.memory\\.used_sum|jvm_memory_used_sum|` +
		`jvm_memory_used_seconds_sum|jvm_memory_used_milliseconds_sum|` +
		`jvm_memory_used_bytes_sum|jvm_memory_used_ratio_sum)$"`
	mcJVMCount = `__name__=~"^(jvm\\.memory\\.used_count|jvm_memory_used_count|` +
		`jvm_memory_used_seconds_count|jvm_memory_used_milliseconds_count|` +
		`jvm_memory_used_bytes_count|jvm_memory_used_ratio_count)$"`

	mcHTTPDurUnderscoreSum = `__name__=~"^(http_server_request_duration_sum|` +
		`http_server_request_duration_seconds_sum|` +
		`http_server_request_duration_milliseconds_sum|` +
		`http_server_request_duration_bytes_sum|` +
		`http_server_request_duration_ratio_sum)$"`
	mcHTTPDurUnderscoreCount = `__name__=~"^(http_server_request_duration_count|` +
		`http_server_request_duration_seconds_count|` +
		`http_server_request_duration_milliseconds_count|` +
		`http_server_request_duration_bytes_count|` +
		`http_server_request_duration_ratio_count)$"`

	mcQuotedSum = `__name__=~"^(m\"x_sum|m_x_sum|m_x_seconds_sum|` +
		`m_x_milliseconds_sum|m_x_bytes_sum|m_x_ratio_sum)$"`
	mcQuotedCount = `__name__=~"^(m\"x_count|m_x_count|m_x_seconds_count|` +
		`m_x_milliseconds_count|m_x_bytes_count|m_x_ratio_count)$"`

	mcM = `__name__=~"^(m|m_seconds|m_milliseconds|m_bytes|m_ratio|m_total|` +
		`m_seconds_total|m_milliseconds_total|m_bytes_total)$"`

	// v0.9.1160 — the HISTOGRAM-PART matchers. Separate `or` arms, not extra
	// members of the list above: an alternation sums its members, `or` picks the
	// left arm per group. Unit-first composition, and no `_total` members — a
	// histogram is never a monotonic sum.
	mcMCount = `__name__=~"^(m_count|m_seconds_count|m_milliseconds_count|` +
		`m_bytes_count|m_ratio_count)$"`
	mcMSum = `__name__=~"^(m_sum|m_seconds_sum|m_milliseconds_sum|` +
		`m_bytes_sum|m_ratio_sum)$"`

	mcMBucket = `__name__=~"^(m_bucket|m_seconds_bucket|m_milliseconds_bucket|` +
		`m_bytes_bucket|m_ratio_bucket)$"`

	// `m"x` — verbatim keeps the quote (escaped for the PromQL literal, not by
	// QuoteMeta: `"` is not a regex metacharacter), sanitised turns it into
	// `_`. Both ride.
	mcQuoted = `__name__=~"^(m\"x|m_x|m_x_seconds|m_x_milliseconds|m_x_bytes|` +
		`m_x_ratio|m_x_total|m_x_seconds_total|m_x_milliseconds_total|` +
		`m_x_bytes_total)$"`
)

// The constants above must be exactly what nameMatcher renders for those
// names. Without this the forty pins that use them would be pinned to a
// hand-written string that no longer describes the code — the failure mode is
// a suite that passes green while every VM query in prod asks for the wrong
// series.
func TestSelectorConstantsMatchTheImplementation(t *testing.T) {
	cases := []struct {
		name, agg, want string
	}{
		{"http.server.request.duration", "max", mcHTTPDur},
		{"http.server.request.duration", "p99", mcHTTPDurBucket},
		{"http_server_request_duration", "max", mcHTTPDurUnderscore},
		{"jvm.memory.used", "last", mcJVM},
		{"m", "max", mcM},
		{"m", "p50", mcMBucket},
		{`m"x`, "max", mcQuoted},
		{"http.server.requests", "max", mcHTTPReq},
		// last is NOT a counter rollup and gets no histogram arm:
		// last_over_time over a `_count` series returns the cumulative total
		// since process start, which is not what anyone means by "the last
		// value".
		{"m", "last", mcM},
	}
	for _, tc := range cases {
		if got := nameMatcher(nameCandidates(tc.name, tc.agg)); got != tc.want {
			t.Fatalf("nameMatcher(%q, %q) =\n  %s\nconstant says\n  %s", tc.name, tc.agg, got, tc.want)
		}
	}

	// v0.9.1160 — and the per-ARM matchers, which is what buildPromQL composes
	// with `or`. Pinned separately from the union above because the union is
	// only ever used for the empty-result NOTE; getting it right while an arm
	// is wrong would be a note that names spellings the query never sent.
	armCases := []struct {
		name, want string
		build      func(string) []string
	}{
		{"m", mcMCount, countNameCandidates},
		{"m", mcMSum, sumNameCandidates},
		{"http.server.requests", mcHTTPReqCount, countNameCandidates},
		{"http.server.request.duration", mcHTTPDurCount, countNameCandidates},
		{"http.server.request.duration", mcHTTPDurSum, sumNameCandidates},
		{"http_server_request_duration", mcHTTPDurUnderscoreCount, countNameCandidates},
		{"http_server_request_duration", mcHTTPDurUnderscoreSum, sumNameCandidates},
		{"jvm.memory.used", mcJVMCount, countNameCandidates},
		{"jvm.memory.used", mcJVMSum, sumNameCandidates},
		{`m"x`, mcQuotedCount, countNameCandidates},
		{`m"x`, mcQuotedSum, sumNameCandidates},
	}
	for _, tc := range armCases {
		if got := nameMatcher(tc.build(tc.name)); got != tc.want {
			t.Fatalf("arm matcher for %q =\n  %s\nconstant says\n  %s", tc.name, got, tc.want)
		}
	}
}

func TestPromMetricName(t *testing.T) {
	cases := map[string]string{
		// The sanitisation every OTLP→Prometheus write path applies.
		"http.server.request.duration": "http_server_request_duration",
		"jvm.memory.used":              "jvm_memory_used",
		// Already sanitised → unchanged (so it dedupes against the verbatim
		// candidate rather than widening the alternation).
		"http_server_request_duration": "http_server_request_duration",
		// `:` survives: Prometheus reserves it for recording rules and the
		// convention's charset keeps it.
		"job:latency:p99": "job:latency:p99",
		// Everything else outside [a-zA-Z0-9_:] becomes `_`.
		"weird-name!": "weird_name_",
		`m"x`:         "m_x",
		"a b":         "a_b",
		// A metric name cannot START with a digit.
		"5xx.count": "_5xx_count",
		// Trim, and empty stays empty so the caller's own "name required"
		// check is the one that fires.
		"  m  ": "m",
		"":      "",
		"   ":   "",
	}
	for in, want := range cases {
		if got := promMetricName(in); got != want {
			t.Errorf("promMetricName(%q) = %q, want %q", in, got, want)
		}
	}
}

// The candidate table. Written out member by member — this is the pin the
// selector constants in promql_test.go / histogram_test.go lean on, so it
// carries the whole rule on its own.
func TestNameCandidates(t *testing.T) {
	tests := []struct {
		name string
		in   string
		agg  string
		want []string
	}{
		{
			// THE OPERATOR'S CASE. Verbatim first (it is the only spelling
			// somebody actually asked for, and the pre-cutover family in
			// their install still answers to it), then the sanitised form,
			// then the unit suffixes. The third member is the live series.
			name: "base spellings (max — no histogram arm)",
			in:   "http.server.request.duration",
			agg:  "max",
			want: []string{
				"http.server.request.duration",
				"http_server_request_duration",
				"http_server_request_duration_seconds",
				"http_server_request_duration_milliseconds",
				"http_server_request_duration_bytes",
				"http_server_request_duration_ratio",
				"http_server_request_duration_total",
				"http_server_request_duration_seconds_total",
				"http_server_request_duration_milliseconds_total",
				"http_server_request_duration_bytes_total",
			},
		},
		{
			// THE OPERATOR'S CASE, percentile half. `_bucket` composes LAST:
			// the unit belongs to the metric, the `_bucket` to the histogram
			// explosion that happens after naming. `…_bucket_seconds` is not a
			// spelling anything produces.
			//
			// And no `_total` variants: the convention appends `_total` to
			// monotonic sums only, so a histogram never carries one. Every
			// candidate dropped here is a name that cannot exist.
			name: "dotted OTLP name, percentile → bucket spellings",
			in:   "http.server.request.duration",
			agg:  "p99",
			want: []string{
				"http.server.request.duration_bucket",
				"http_server_request_duration_bucket",
				"http_server_request_duration_seconds_bucket",
				"http_server_request_duration_milliseconds_bucket",
				"http_server_request_duration_bytes_bucket",
				"http_server_request_duration_ratio_bucket",
			},
		},
		{
			// No dots: verbatim and sanitised COLLIDE and are deduped, so the
			// list is one shorter than the dotted case rather than carrying
			// the same string twice.
			name: "underscored name dedupes verbatim against sanitised",
			in:   "m",
			agg:  "max",
			want: []string{
				"m", "m_seconds", "m_milliseconds", "m_bytes", "m_ratio",
				"m_total", "m_seconds_total", "m_milliseconds_total", "m_bytes_total",
			},
		},
		{
			// Already Prometheus-named → taken as EXACT. This is the VM
			// catalogue row, and it is a SINGLE candidate, which is what keeps
			// the `=` selector form alive for it.
			name: "a name that already carries a unit suffix is exact",
			in:   "jvm_memory_used_bytes",
			agg:  "last",
			want: []string{"jvm_memory_used_bytes"},
		},
		{
			// Not just useless — a RISK. `x_seconds` (gauge) and
			// `x_seconds_total` (counter) can be genuinely different metrics,
			// and alternating them sums two unrelated series into one line.
			name: "no _total is guessed on top of a unit suffix",
			in:   "process_cpu_seconds",
			agg:  "max",
			want: []string{"process_cpu_seconds"},
		},
		{
			name: "a _total name is exact too",
			in:   "http_requests_total",
			agg:  "avg",
			want: []string{"http_requests_total"},
		},
		{
			// The full VM-catalogue bucket row, asked for as a percentile:
			// bucketMetricName's idempotence keeps it out of
			// `…_bucket_bucket`, and the suffix rule adds nothing.
			name: "VM catalogue bucket row, percentile",
			in:   "http_server_request_duration_seconds_bucket",
			agg:  "p95",
			want: []string{"http_server_request_duration_seconds_bucket"},
		},
		{
			// `_sum` / `_count` are histogram parts as far as the rule can
			// tell, so nothing is guessed on top. The `_bucket` suffix is
			// still applied for a percentile — that part is
			// bucketMetricName's documented refusal to GUESS a sibling: the
			// operator gets an honest miss with a note naming what was tried,
			// not a silent answer to a different question.
			name: "_sum is not stripped, and gets no unit guesses",
			in:   "m_sum",
			agg:  "p99",
			want: []string{"m_sum_bucket"},
		},
		{
			// Sanitisation and the verbatim spelling DIVERGE here, so both
			// ride — the quote is not a regex metacharacter but it is a
			// PromQL string-literal one, which is what the selector pins
			// downstream.
			name: "unsanitary characters keep both spellings",
			in:   `m"x`,
			agg:  "max",
			want: []string{
				`m"x`, "m_x", "m_x_seconds", "m_x_milliseconds", "m_x_bytes",
				"m_x_ratio", "m_x_total", "m_x_seconds_total",
				"m_x_milliseconds_total", "m_x_bytes_total",
			},
		},
		{
			name: "trimmed",
			in:   "  m_bucket  ",
			agg:  "p50",
			want: []string{"m_bucket"},
		},
		{
			// Empty is nil, not [""]. The callers each raise their own
			// "metric name required" and nameMatcher never sees a 0-length
			// list in practice.
			name: "empty name yields no candidates",
			in:   "   ",
			agg:  "avg",
			want: nil,
		},
		{
			name: "min takes the plain spellings and NO histogram parts",
			in:   "http.server.requests",
			agg:  "min",
			want: []string{
				"http.server.requests",
				"http_server_requests",
				"http_server_requests_seconds",
				"http_server_requests_milliseconds",
				"http_server_requests_bytes",
				"http_server_requests_ratio",
				"http_server_requests_total",
				"http_server_requests_seconds_total",
				"http_server_requests_milliseconds_total",
				"http_server_requests_bytes_total",
			},
		},

		// ── v0.9.1160: the histogram-aware aggregations ─────────────────────
		//
		// Operator-verified gap, twice. `rate` on the dotted OTLP name returned
		// 200 with ZERO series, and so did the service page's "Response time ·
		// avg (by route)" panel — in VM an OTLP histogram has no base series, so
		// its throughput is `<name>_count` and its mean is `_sum`/`_count`.
		//
		// nameCandidates returns the UNION of the arms buildPromQL composes with
		// `or`, because that union is what the empty-result note lists. The arm
		// MATCHERS are pinned separately in
		// TestSelectorConstantsMatchTheImplementation, and the `or` SHAPE in
		// TestOrCompositionShapes.
		{
			// THE FIRST REPORTED CASE. Base list, then the `_count` derivatives
			// with unit-first composition. Member 13 is the one every Grafana
			// dashboard rates.
			name: "rate on the dotted OTLP name reaches the _count series",
			in:   "http.server.request.duration",
			agg:  "rate",
			want: []string{
				"http.server.request.duration",
				"http_server_request_duration",
				"http_server_request_duration_seconds",
				"http_server_request_duration_milliseconds",
				"http_server_request_duration_bytes",
				"http_server_request_duration_ratio",
				"http_server_request_duration_total",
				"http_server_request_duration_seconds_total",
				"http_server_request_duration_milliseconds_total",
				"http_server_request_duration_bytes_total",
				"http.server.request.duration_count",
				"http_server_request_duration_count",
				"http_server_request_duration_seconds_count", // ← the live one
				"http_server_request_duration_milliseconds_count",
				"http_server_request_duration_bytes_count",
				"http_server_request_duration_ratio_count",
			},
		},
		{
			// THE SECOND REPORTED SPELLING: the clean unit-suffixed base, which
			// the skip set otherwise takes as exact. It still earns a `_count`
			// arm, because THIS is the name whose base does not exist while
			// `…_seconds_count` does. mayHaveHistogramParts lets a `_seconds`
			// suffix through for exactly this reason.
			name: "rate on a unit-suffixed base still reaches its _count arm",
			in:   "http_server_request_duration_seconds",
			agg:  "rate",
			want: []string{
				"http_server_request_duration_seconds",
				"http_server_request_duration_seconds_count",
			},
		},
		{
			name: "increase behaves exactly like rate",
			in:   "http_server_request_duration_seconds",
			agg:  "increase",
			want: []string{
				"http_server_request_duration_seconds",
				"http_server_request_duration_seconds_count",
			},
		},
		{
			// THE SECOND REPORTED CASE — the avg panel. `_sum` comes before
			// `_count` because that is the order the ratio arm divides them, and
			// the note reads in the same order the expression does.
			name: "avg reaches BOTH histogram parts",
			in:   "http_server_request_duration_seconds",
			agg:  "avg",
			want: []string{
				"http_server_request_duration_seconds",
				"http_server_request_duration_seconds_sum",
				"http_server_request_duration_seconds_count",
			},
		},
		{
			// An omitted aggregation IS avg (promAggregator's default), and the
			// service page's route panel sends exactly that. If the gate keyed
			// on the literal "avg" only, the reported panel would still be
			// empty while every test using the explicit label passed.
			name: "an EMPTY aggregation is avg and reaches both parts",
			in:   "http_server_request_duration_seconds",
			agg:  "",
			want: []string{
				"http_server_request_duration_seconds",
				"http_server_request_duration_seconds_sum",
				"http_server_request_duration_seconds_count",
			},
		},
		{
			// IDEMPOTENCE: a `_count` row picked straight off VM's catalogue is
			// not doubled into `…_count_count`, and stays a SINGLE candidate —
			// so it keeps the `=` selector form.
			name: "rate on an already-_count name is idempotent and single-arm",
			in:   "http_server_request_duration_seconds_count",
			agg:  "rate",
			want: []string{"http_server_request_duration_seconds_count"},
		},
		{
			// A real `_total` counter is UNCHANGED from v0.9.1159: monotonic
			// sums have no histogram siblings, so no `_count` is guessed and the
			// single-candidate path is preserved.
			name: "rate on a _total counter stays the single exact path (no arm)",
			in:   "http_requests_total",
			agg:  "increase",
			want: []string{"http_requests_total"},
		},
		{
			// `last` is NOT a counter rollup. last_over_time over a `_count`
			// series is the cumulative total since process start.
			name: "last gets no histogram arm at all",
			in:   "http_server_request_duration_seconds",
			agg:  "last",
			want: []string{"http_server_request_duration_seconds"},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := nameCandidates(tc.in, tc.agg)
			if len(got) != len(tc.want) {
				t.Fatalf("nameCandidates(%q, %q) =\n  %v\nwant\n  %v", tc.in, tc.agg, got, tc.want)
			}
			for i := range got {
				if got[i] != tc.want[i] {
					t.Fatalf("candidate[%d] = %q, want %q\nfull: %v", i, got[i], tc.want[i], got)
				}
			}
		})
	}
}

// All three percentile labels take the bucket path — a translation that
// covered p99 but let p50 read the base series would chart a number that is
// not a percentile of anything, under a legend that says p50.
func TestNameCandidatesPercentilesAllUseBuckets(t *testing.T) {
	for _, agg := range []string{"p50", "p95", "p99", "P99", " p95 "} {
		got := nameCandidates("m", agg)
		for _, c := range got {
			if !strings.HasSuffix(c, "_bucket") {
				t.Fatalf("agg=%q candidate %q is not a bucket series: %v", agg, c, got)
			}
		}
		if len(got) != 5 {
			t.Fatalf("agg=%q produced %d candidates, want 5 (no _total on a histogram): %v",
				agg, len(got), got)
		}
	}
	// And a non-percentile must NOT read buckets.
	for _, agg := range []string{"", "avg", "sum", "min", "max", "count", "last", "rate", "increase"} {
		for _, c := range nameCandidates("m", agg) {
			if strings.HasSuffix(c, "_bucket") {
				t.Fatalf("agg=%q reached the bucket series via %q", agg, c)
			}
		}
	}
}

// THE AGGREGATION GATE (v0.9.1160). The histogram parts must reach avg, rate
// and increase — and NOTHING ELSE.
//
// Both directions fail silently and differently, which is why both are asserted:
//
//	leaked into min/max/sum/count/last → the query resolves to a SAMPLE COUNT
//	  (or, for last, a cumulative total since process start) where the operator
//	  asked for the measurement. Large, plausible, wrong, under a latency
//	  legend: the v0.9.566 class.
//	missing from avg/rate/increase → the reported bugs return: a permanently
//	  blank throughput line and an empty "Response time · avg (by route)" panel
//	  beside a working latency chart.
func TestHistogramPartsReachOnlyTheHistogramAwareAggs(t *testing.T) {
	const histogramMetric = "http.server.request.duration"
	const countSpelling = "http_server_request_duration_seconds_count"
	const sumSpelling = "http_server_request_duration_seconds_sum"

	// rate / increase: the `_count` arm, and NOT the `_sum` one — throughput is
	// a count of observations, not a total of their values.
	for _, agg := range []string{"rate", "increase", "RATE", " increase "} {
		cands := nameCandidates(histogramMetric, agg)
		if !containsString(cands, countSpelling) {
			t.Fatalf("agg=%q cannot reach %s — an OTLP histogram has no base series in VM, "+
				"so its throughput panel stays permanently empty", agg, countSpelling)
		}
		if containsString(cands, sumSpelling) {
			t.Fatalf("agg=%q reached %s — rate over the value TOTAL is not throughput", agg, sumSpelling)
		}
		if !isCounterRollup(agg) || isMeanAgg(agg) {
			t.Fatalf("predicates disagree for %q", agg)
		}
	}

	// avg: BOTH parts, because its histogram form is the ratio.
	for _, agg := range []string{"", "avg", "AVG", " avg "} {
		cands := nameCandidates(histogramMetric, agg)
		for _, want := range []string{sumSpelling, countSpelling} {
			if !containsString(cands, want) {
				t.Fatalf("agg=%q cannot reach %s — the service page's avg-by-route panel "+
					"stays empty on a histogram metric", agg, want)
			}
		}
		if !isMeanAgg(agg) || isCounterRollup(agg) {
			t.Fatalf("predicates disagree for %q", agg)
		}
	}

	// Everything else: no histogram part at all.
	for _, agg := range []string{"min", "max", "sum", "count", "last", "MAX", " last "} {
		for _, c := range nameCandidates(histogramMetric, agg) {
			if strings.HasSuffix(c, "_count") || strings.HasSuffix(c, "_sum") {
				t.Fatalf("agg=%q leaked the histogram part %q — it would chart a sample count "+
					"as though it were the measurement", agg, c)
			}
		}
		if isCounterRollup(agg) || isMeanAgg(agg) {
			t.Fatalf("predicates disagree for %q", agg)
		}
	}

	// A percentile keeps the BUCKET branch: the guessing rules must not merge,
	// or a p99 would read its distribution off a count series.
	for _, agg := range []string{"p50", "p95", "p99"} {
		if isCounterRollup(agg) || isMeanAgg(agg) {
			t.Fatalf("agg=%q read as throughput/mean — percentiles carry Rollup==rate but "+
				"are neither", agg)
		}
		for _, c := range nameCandidates(histogramMetric, agg) {
			if !strings.HasSuffix(c, "_bucket") {
				t.Fatalf("agg=%q candidate %q left the bucket branch", agg, c)
			}
		}
	}

	// An unknown aggregation guesses nothing: buildPromQL is about to refuse it.
	if isCounterRollup("nonsense") || isMeanAgg("nonsense") {
		t.Fatal("an unsupported aggregation must not be read as a counter rollup or a mean")
	}
}

func containsString(hay []string, needle string) bool {
	for _, h := range hay {
		if h == needle {
			return true
		}
	}
	return false
}

// Candidates must never repeat. A duplicate is not cosmetic: VM accepts it,
// but the expression text is what makes two polls of one panel recognisable as
// one query in the query log, and a duplicated member also inflates the
// alternation the name index has to walk.
func TestNameCandidatesAreDeduped(t *testing.T) {
	inputs := []string{
		"m", "m_bucket", "m_sum", "http.server.request.duration",
		"http_server_request_duration", "jvm_memory_used_bytes", `m"x`,
		// san == verbatim AND a bucket suffix — two collision routes at once.
		"http_server_request_duration_seconds_bucket",
	}
	for _, in := range inputs {
		for _, agg := range []string{"avg", "rate", "p99"} {
			seen := map[string]bool{}
			for _, c := range nameCandidates(in, agg) {
				if seen[c] {
					t.Fatalf("nameCandidates(%q, %q) repeats %q: %v",
						in, agg, c, nameCandidates(in, agg))
				}
				seen[c] = true
			}
		}
	}
}

// Discovery scopes label lookups. Two things are load-bearing and both are
// about what an EMPTY picker claims: a filter list with nothing in it reads as
// "this metric has no attributes", which is a stronger and more wrong
// statement than a blank chart.
func TestDiscoveryNameCandidates(t *testing.T) {
	got := discoveryNameCandidates("http.server.request.duration")
	want := []string{
		// The plain spellings first, in the same order as everywhere else.
		"http.server.request.duration",
		"http_server_request_duration",
		"http_server_request_duration_seconds",
		"http_server_request_duration_milliseconds",
		"http_server_request_duration_bytes",
		"http_server_request_duration_ratio",
		"http_server_request_duration_total",
		"http_server_request_duration_seconds_total",
		"http_server_request_duration_milliseconds_total",
		"http_server_request_duration_bytes_total",
		// Then the histogram family, represented by `_count` alone — the
		// attribute set is identical on _bucket/_sum/_count, and _count is
		// the one that does NOT carry `le`.
		"http.server.request.duration_count",
		"http_server_request_duration_count",
		"http_server_request_duration_seconds_count",
		"http_server_request_duration_milliseconds_count",
		"http_server_request_duration_bytes_count",
		"http_server_request_duration_ratio_count",
	}
	if len(got) != len(want) {
		t.Fatalf("discoveryNameCandidates =\n  %v\nwant\n  %v", got, want)
	}
	for i := range got {
		if got[i] != want[i] {
			t.Fatalf("candidate[%d] = %q, want %q\nfull: %v", i, got[i], want[i], got)
		}
	}
}

// `le` must not be reachable through discovery, or MetricAttrKeys offers the
// histogram's own internal dimension as one of the operator's attributes.
func TestDiscoveryNeverScopesTheBucketSeries(t *testing.T) {
	for _, in := range []string{"http.server.request.duration", "m", "m_sum", "jvm_memory_used_bytes"} {
		for _, c := range discoveryNameCandidates(in) {
			if strings.HasSuffix(c, "_bucket") && !strings.HasSuffix(in, "_bucket") {
				t.Fatalf("discovery for %q reached the bucket series via %q (le would be "+
					"offered as a filter key)", in, c)
			}
		}
	}
	// A monotonic counter has no histogram siblings, so no `_count` is
	// guessed on top of a `_total`.
	for _, c := range discoveryNameCandidates("http_requests_total") {
		if strings.HasSuffix(c, "_total_count") {
			t.Fatalf("discovery guessed %q — a _total has no _count sibling", c)
		}
	}
	// An already-histogram-part name is scoped as itself, not doubled.
	got := discoveryNameCandidates("m_count")
	if len(got) != 1 || got[0] != "m_count" {
		t.Fatalf("discoveryNameCandidates(m_count) = %v, want [m_count]", got)
	}
}

// The selector FORM. One candidate keeps `=`; many become an anchored literal
// alternation.
func TestNameMatcherForm(t *testing.T) {
	// Single → the pre-v0.9.1159 spelling, byte for byte. This is what keeps
	// a query that needed no guessing from changing at all.
	if got, want := nameMatcher([]string{"m_bucket"}), `__name__="m_bucket"`; got != want {
		t.Fatalf("single candidate = %s, want %s", got, want)
	}
	// Multi → `=~`, anchored, pipe-joined.
	got := nameMatcher([]string{"a", "b_seconds"})
	if want := `__name__=~"^(a|b_seconds)$"`; got != want {
		t.Fatalf("multi candidate = %s, want %s", got, want)
	}
	// Defensive: an empty list matches NOTHING rather than everything. An
	// empty regex would have alternated into `^()$`… or worse, `=~""`, which
	// in Prometheus matches every series without that label.
	if got, want := nameMatcher(nil), `__name__=""`; got != want {
		t.Fatalf("empty candidate list = %s, want %s", got, want)
	}
}

// Regex escaping, asserted at the REGEX layer — one below the PromQL string
// literal, where quotePromString doubles the backslash.
//
// This is the widening bug, not a crash: unescaped, `http.server` also matches
// `httpxserver`, so the alternation would silently pull in series nobody asked
// for and SUM them into the panel.
func TestNameAlternationEscapesRegexMetacharacters(t *testing.T) {
	got := nameAlternation([]string{"http.server.request.duration_bucket", "http_server_request_duration_bucket"})
	want := `^(http\.server\.request\.duration_bucket|http_server_request_duration_bucket)$`
	if got != want {
		t.Fatalf("alternation =\n  %s\nwant\n  %s", got, want)
	}
	// And through the PromQL literal, where the backslash is doubled. Both
	// layers are pinned because escaping exactly once at each is the only
	// combination VM reads back as a literal dot.
	if m := nameMatcher([]string{"a.b", "a_b"}); m != `__name__=~"^(a\\.b|a_b)$"` {
		t.Fatalf("matcher = %s", m)
	}
}
