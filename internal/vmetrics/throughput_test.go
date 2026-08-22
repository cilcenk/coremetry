package vmetrics

// v0.9.1268 — the VictoriaMetrics arm of the service-Overview throughput
// mapper.
//
// OPERATOR-REPORTED SYMPTOM: "Throughput · metrik
// (http.server.request.duration)" said "bu servise eşleşen seri yok" on a prod
// install whose metric backend is VictoriaMetrics, while the avg-by-route panel
// beside it drew the SAME metric fine. The mapper called *chstore.Store
// directly, so it searched ClickHouse and answered honestly about the wrong
// store.
//
// Everything here is either PURE (the MetricsQL cores) or httptest-backed. That
// is not a limitation to apologise for, it is the only honest option: this
// machine has no VictoriaMetrics, and the CH-fallback path is what a local
// smoke can actually exercise. The wire tests pin the four things a live VM
// would otherwise be the first to notice — the endpoint, the expression, the
// window and the scope.

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/cilcenk/coremetry/internal/chstore"
)

// tputFilter — the shape the API mapper actually sends: an anchored
// two-spelling identity regex on one label, plus the panel's route breakdown.
func tputFilter() chstore.MetricQueryFilter {
	to := time.Unix(1_700_000_000, 0)
	return chstore.MetricQueryFilter{
		Name:        "http.server.request.duration",
		From:        to.Add(-time.Hour),
		To:          to,
		Aggregation: "sum",
		Filters: []chstore.FilterExpr{{
			Key: "job", Op: "=~",
			Values: []string{chstore.JobServiceRegex("bsa-deposit-uat")},
		}},
	}
}

// ── The count-only rate expression ─────────────────────────────────────────

func TestBuildCountRatePromQLShape(t *testing.T) {
	q, err := buildCountRatePromQL(tputFilter(), "rate", promOpts{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// A histogram's throughput is the rate of its observation COUNT — the
	// expression every Grafana dashboard writes.
	if !strings.HasPrefix(q, "sum(rate({") {
		t.Errorf("count rate must be sum(rate(…)): %s", q)
	}
	// It must read the `_count` family and NOT the base name: the base series
	// of an OTLP histogram does not exist in VM, which is the whole reason
	// this arm exists.
	if !strings.Contains(q, "_count") {
		t.Errorf("expression does not read the _count family: %s", q)
	}
	// The identity filter rides along. An arm that lost it would answer the
	// operator's question about one service with every service's traffic —
	// plausible, wrong, unquestioned.
	if !strings.Contains(q, "job=~") || !strings.Contains(q, "bsa-deposit") {
		t.Errorf("identity matcher missing from the selector: %s", q)
	}
	// And a rollup window is present. `[0s]` would be accepted by VM and
	// return nothing.
	if !strings.Contains(q, "s])") || strings.Contains(q, "[0s]") {
		t.Errorf("missing or zero rollup window: %s", q)
	}
}

// The regex identity must reach VM ESCAPED AT THE RIGHT LAYER. chstore's
// JobServiceRegex quotes `.` and `-` for RE2; quotePromString then escapes the
// backslashes for the MetricsQL string literal. Getting this wrong is silent:
// an unescaped `.` matches any character and quietly widens the match.
func TestCountRateKeepsTheTwoSpellingRegexIntact(t *testing.T) {
	q, err := buildCountRatePromQL(tputFilter(), "rate", promOpts{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// v0.9.673's rule: BOTH the suffixed and the stripped spelling are
	// candidates, because the same install carries each on a different label.
	for _, want := range []string{"bsa-deposit-uat", "bsa-deposit"} {
		if !strings.Contains(q, want) {
			t.Errorf("identity alternation lost %q — v0.9.673: aynı kurulumda her iki biçim "+
				"birden var, yalnız birini aramak ötekini kaçırır: %s", want, q)
		}
	}
	// The namespace-prefix arm survives too (`^(.*/)?…$`).
	if !strings.Contains(q, `(.*/)?`) {
		t.Errorf("namespace-prefix arm lost from the pattern: %s", q)
	}
}

func TestCountRateGroupByBecomesByClause(t *testing.T) {
	f := tputFilter()
	f.GroupBy = []string{"http.route"}
	q, err := buildCountRatePromQL(f, "rate", promOpts{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// Dotted OTLP key → underscored MetricsQL label. Passing `http.route`
	// through verbatim would not parse.
	if !strings.Contains(q, "sum by (http_route) (") {
		t.Errorf("group-by did not become a by-clause over the sanitized label: %s", q)
	}
}

// The rate WINDOW obeys the same knobs as every other expression in the
// package. v0.9.1165's lesson: a floor that changes the emitted window while
// the request stays byte-identical is invisible from the outside.
func TestCountRateWindowHonoursCallerAndFloor(t *testing.T) {
	f := tputFilter()
	f.RateWindowSec = 600
	q, err := buildCountRatePromQL(f, "rate", promOpts{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(q, "[600s]") {
		t.Errorf("explicit rate window ignored: %s", q)
	}

	// With no explicit window the configured floor applies.
	q2, err := buildCountRatePromQL(tputFilter(), "rate", promOpts{RateWindowFloorS: 900})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(q2, "[900s]") {
		t.Errorf("rate window floor ignored: %s", q2)
	}
}

// increase is the other mode chstore accepts; anything else is refused BEFORE
// a round trip, and tagged so the API answers 400 rather than blaming a
// healthy VM with a 502.
func TestRateModeContractMatchesClickHouse(t *testing.T) {
	for _, mode := range []string{"rate", "increase", "RATE", " increase "} {
		if _, err := buildCountRatePromQL(tputFilter(), mode, promOpts{}); err != nil {
			t.Errorf("mode %q must be accepted (chstore does): %v", mode, err)
		}
	}
	for _, mode := range []string{"", "avg", "sum", "p95", "last"} {
		_, err := buildCountRatePromQL(tputFilter(), mode, promOpts{})
		if err == nil {
			t.Errorf("mode %q must be refused — chstore's queryRateFrom refuses it, and a "+
				"method named CountRate that answers an avg is a near-miss that survives review", mode)
			continue
		}
		if !strings.Contains(err.Error(), ErrUnsupported.Error()) {
			t.Errorf("mode %q refusal not tagged ErrUnsupported (%v) — it would 502 and blame "+
				"a healthy VictoriaMetrics", mode, err)
		}
	}
}

// A filter operator with no MetricsQL matcher is REFUSED, never dropped. A
// dropped conjunct is the silent-wrong-answer class this package keeps citing.
func TestCountRateRefusesUntranslatableFilters(t *testing.T) {
	f := tputFilter()
	f.Filters = append(f.Filters, chstore.FilterExpr{Key: "n", Op: ">", Values: []string{"5"}})
	if _, err := buildCountRatePromQL(f, "rate", promOpts{}); err == nil {
		t.Fatal("an untranslatable filter must refuse, not silently widen the query")
	}
}

// ── Identity labels ────────────────────────────────────────────────────────

// THE OPERATOR'S BUG, at the exact line it lives on.
func TestServiceIdentityLabelsCarryServiceName(t *testing.T) {
	got := New().ServiceIdentityLabels()
	if len(got) == 0 {
		t.Fatal("empty identity label list")
	}
	index := map[string]int{}
	for i, l := range got {
		index[l] = i
	}

	// `service_name` is the candidate the ClickHouse list cannot contain — a
	// COLUMN there, an ordinary label here, and on an OTLP-fed VM install the
	// likeliest place the identity lives.
	if _, ok := index["service_name"]; !ok {
		t.Errorf("service_name missing — this is the candidate whose absence produced the "+
			"v0.9.1268 operator report: %v", got)
	}
	// The CH list still gets translated rather than replaced, so the two
	// backends cannot drift on WHICH identities are attempted.
	for _, want := range []string{"k8s_deployment_name", "k8s_container_name", "job", "name"} {
		if _, ok := index[want]; !ok {
			t.Errorf("translated candidate %q missing: %v", want, got)
		}
	}
	// k8s_deployment_name stays ahead of service_name: it carries the
	// environment suffix, so it is the more precise identity when present.
	if index["k8s_deployment_name"] > index["service_name"] {
		t.Errorf("k8s_deployment_name must be tried before service_name (it carries the env "+
			"suffix, so it cannot merge two environments): %v", got)
	}
	// Every candidate must be a legal MetricsQL label name. A dotted one is
	// not a matcher, it is a parse error waiting to be an empty panel.
	for _, l := range got {
		if l != promLabel(l) {
			t.Errorf("candidate %q is not a sanitized label name (promLabel gives %q)", l, promLabel(l))
		}
	}
	// No duplicates: a repeated candidate is a wasted query round per request.
	if len(index) != len(got) {
		t.Errorf("duplicate candidates in %v", got)
	}
}

// ── Instrument classification ──────────────────────────────────────────────

func TestClassifyInstrument(t *testing.T) {
	tests := []struct {
		name    string
		present []string
		want    string
	}{
		{"nothing present", nil, ""},
		{"empty strings only", []string{"", ""}, ""},
		{"plain counter", []string{"http_requests_total"}, instrumentSum},
		{"gauge-ish base", []string{"jvm_memory_used"}, instrumentSum},
		{"histogram parts", []string{
			"http_server_request_duration_seconds_bucket",
			"http_server_request_duration_seconds_count",
			"http_server_request_duration_seconds_sum",
		}, instrumentHistogram},
		// The bucket series is the only positive evidence of a histogram, so
		// it wins wherever it appears in the list — order must not decide.
		{"bucket last", []string{"x_count", "x_bucket"}, instrumentHistogram},
		{"bucket first", []string{"x_bucket", "x_count"}, instrumentHistogram},
		// `_count` alone is NOT called a histogram: with no bucket series the
		// honest reading is "there is a counter to rate", and the rate arm's
		// `or` composition lands on the same series either way.
		{"count without buckets", []string{"x_count"}, instrumentSum},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := classifyInstrument(tc.present); got != tc.want {
				t.Fatalf("classifyInstrument(%v) = %q, want %q", tc.present, got, tc.want)
			}
		})
	}
}

// The unit comes from a name VM ACTUALLY CARRIES, and the answer must not
// depend on the label index's ordering — two polls a second apart labelling the
// same axis "s" and then "" is worse than never labelling it.
func TestUnitFromPresentNamesIsOrderIndependent(t *testing.T) {
	a := []string{"http_server_request_duration_seconds_count", "http_server_request_duration_seconds_bucket"}
	b := []string{"http_server_request_duration_seconds_bucket", "http_server_request_duration_seconds_count"}
	if unitFromPresentNames(a) != unitFromPresentNames(b) {
		t.Fatalf("unit depends on input order: %q vs %q", unitFromPresentNames(a), unitFromPresentNames(b))
	}
	if got := unitFromPresentNames(a); got != "s" {
		t.Errorf("the `_seconds` spelling IS the unit in Prometheus's contract, got %q", got)
	}
	if got := unitFromPresentNames(nil); got != "" {
		t.Errorf("no present name must mean no unit, got %q — a guessed unit labels an axis "+
			"with a confidence nothing earned", got)
	}
}

func TestFamilyNameCandidatesIncludeBuckets(t *testing.T) {
	got := familyNameCandidates("http.server.request.duration")
	var hasBucket, hasCount bool
	for _, c := range got {
		if strings.HasSuffix(c, bucketSuffix) {
			hasBucket = true
		}
		if strings.HasSuffix(c, histogramCountSuffix) {
			hasCount = true
		}
	}
	if !hasBucket {
		t.Errorf("no `_bucket` spelling — it is the ONLY evidence that distinguishes a "+
			"histogram from a counter, so without it every metric classifies as a counter: %v", got)
	}
	if !hasCount {
		t.Errorf("no `_count` spelling: %v", got)
	}
	// An already-suffixed name is left alone: the operator picked that part
	// off VM's catalogue deliberately.
	for _, c := range familyNameCandidates("http_requests_total") {
		if strings.HasSuffix(c, bucketSuffix) {
			t.Errorf("a `_total` counter must not grow bucket candidates: %v", c)
		}
	}
}

// ── Wire behaviour ─────────────────────────────────────────────────────────

// matrixOK is the smallest successful query_range body.
const matrixOK = `{"status":"success","data":{"resultType":"matrix","result":[` +
	`{"metric":{"http_route":"/api/v1/deposit"},"values":[[1700000000,"12.5"]]}]}}`

func TestQueryMetricCountRateHitsQueryRangeWithTheCountArm(t *testing.T) {
	var gotPath, gotQuery, gotStep string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotQuery = r.URL.Query().Get("query")
		gotStep = r.URL.Query().Get("step")
		_, _ = w.Write([]byte(matrixOK))
	}))
	defer srv.Close()

	s := New()
	s.Configure(Settings{BaseURL: srv.URL})
	f := tputFilter()
	f.GroupBy = []string{"http.route"}
	out, err := s.QueryMetricCountRate(context.Background(), f, "rate")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if gotPath != "/api/v1/query_range" {
		t.Fatalf("path = %q", gotPath)
	}
	if !strings.Contains(gotQuery, "_count") || !strings.Contains(gotQuery, "rate(") {
		t.Errorf("wire query is not a count rate: %s", gotQuery)
	}
	if gotStep == "" || gotStep == "0s" {
		t.Errorf("step = %q — a zero step returns nothing", gotStep)
	}
	// The decode must produce Coremetry's shape: the group key comes from the
	// REQUESTED group-by, positionally, so the frontend's series label matches
	// what the operator asked to split by.
	if len(out) != 1 || len(out[0].GroupKey) != 1 || out[0].GroupKey[0] != "/api/v1/deposit" {
		t.Fatalf("group key not decoded from the requested group-by: %+v", out)
	}
	if len(out[0].Points) != 1 || out[0].Points[0].Value != 12.5 {
		t.Fatalf("points not decoded: %+v", out[0].Points)
	}
	// Time is unix NANOS. A seconds value here renders every point at the
	// epoch and the chart looks empty.
	if out[0].Points[0].Time != 1_700_000_000*int64(time.Second) {
		t.Errorf("point time is not unix nanos: %d", out[0].Points[0].Time)
	}
}

// The counter arm goes through buildPromQL, which composes `base or base_count`
// — one expression that is right for a counter AND for an OTLP histogram.
func TestQueryMetricRateEmitsTheSelfSelectingOrComposition(t *testing.T) {
	var gotQuery string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotQuery = r.URL.Query().Get("query")
		_, _ = w.Write([]byte(matrixOK))
	}))
	defer srv.Close()

	s := New()
	s.Configure(Settings{BaseURL: srv.URL})
	if _, err := s.QueryMetricRate(context.Background(), tputFilter(), "rate"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(gotQuery, " or ") {
		t.Errorf("rate arm lost the `or` composition — an OTLP histogram has NO base series "+
			"in VM, so without the `_count` arm this is the empty panel we are fixing: %s", gotQuery)
	}
	if !strings.Contains(gotQuery, "rate(") {
		t.Errorf("no rate() in the expression: %s", gotQuery)
	}
	// The incoming Aggregation was "sum" (the CH path's intra-bucket op). It
	// must NOT survive as a bare sum(): that would draw a cumulative counter's
	// lifetime total under a throughput legend.
	if strings.HasPrefix(gotQuery, "sum({") {
		t.Errorf("the CH intra-bucket aggregation leaked through as a bare sum: %s", gotQuery)
	}
}

// MetricExists and MetricInstrument both read the label index, never a series
// scan — and both scope by the candidate alternation rather than one spelling.
func TestExistsAndInstrumentProbeTheLabelIndex(t *testing.T) {
	var gotPath, gotMatch string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotMatch = r.URL.Query().Get("match[]")
		_, _ = w.Write([]byte(`{"status":"success","data":[` +
			`"http_server_request_duration_seconds_bucket",` +
			`"http_server_request_duration_seconds_count"]}`))
	}))
	defer srv.Close()

	s := New()
	s.Configure(Settings{BaseURL: srv.URL})

	ok, err := s.MetricExists(context.Background(), "http.server.request.duration")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !ok {
		t.Fatal("a metric whose bucket+count series exist must report as existing")
	}
	if gotPath != "/api/v1/label/__name__/values" {
		t.Fatalf("existence probe hit %q — it must be a label-index lookup, not a series scan", gotPath)
	}
	if !strings.Contains(gotMatch, "__name__") {
		t.Errorf("probe is unscoped: %s", gotMatch)
	}

	// And the same evidence classifies the instrument.
	if got := s.MetricInstrument(context.Background(), "http.server.request.duration", "cart"); got != instrumentHistogram {
		t.Errorf("instrument = %q, want %q", got, instrumentHistogram)
	}
	// A service scope rides along, so the answer is about THIS service.
	if !strings.Contains(gotMatch, `service_name="cart"`) {
		t.Errorf("service scope lost from the instrument probe: %s", gotMatch)
	}
}

// An empty name must not become an unscoped probe: `__name__=""` matches
// nothing, but asking at all is a round trip for a question with no subject.
func TestMetricExistsShortCircuitsEmptyName(t *testing.T) {
	s := New()
	s.Configure(Settings{BaseURL: "http://127.0.0.1:1"}) // would fail to dial
	ok, err := s.MetricExists(context.Background(), "  ")
	if err != nil || ok {
		t.Fatalf("empty name = (%v, %v), want (false, nil) with no round trip", ok, err)
	}
}

// MetricPresentKeys answers in COREMETRY's spelling while matching in VM's.
// Echoing the sanitized spelling back would print two names for one key in the
// diagnostic note and read as two different keys.
func TestMetricPresentKeysMatchesSanitizedEchoesVerbatim(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"status":"success","data":["k8s_deployment_name","job","__name__"]}`))
	}))
	defer srv.Close()

	s := New()
	s.Configure(Settings{BaseURL: srv.URL})
	got := s.MetricPresentKeys(context.Background(), "http.server.request.duration",
		[]string{"resource.k8s.deployment.name", "job", "resource.k8s.container.name"}, time.Hour)

	if len(got) != 2 {
		t.Fatalf("want the two present keys, got %v", got)
	}
	if got[0] != "resource.k8s.deployment.name" {
		t.Errorf("key echoed in VM's spelling rather than the caller's: %q", got[0])
	}
	if got[1] != "job" {
		t.Errorf("unexpected second key: %q", got[1])
	}
	// __name__ is not an attribute key and must never be offered as one.
	for _, k := range got {
		if k == "__name__" {
			t.Error("__name__ reported as a present identity key")
		}
	}
}

// Every method must fail LOUDLY when VM is unconfigured — the two query paths
// with an error, the three diagnostics with their zero value (their chstore
// twins swallow the same way, and the mapper reads the zero as "unknown").
func TestThroughputMethodsFailClosedWhenUnconfigured(t *testing.T) {
	s := New()
	ctx := context.Background()

	if _, err := s.QueryMetricRate(ctx, tputFilter(), "rate"); err == nil {
		t.Error("QueryMetricRate must error with no base URL — a nil error with no series is " +
			"the silent empty panel this release exists to remove")
	}
	if _, err := s.QueryMetricCountRate(ctx, tputFilter(), "rate"); err == nil {
		t.Error("QueryMetricCountRate must error with no base URL")
	}
	if _, err := s.MetricExists(ctx, "m"); err == nil {
		t.Error("MetricExists must error with no base URL")
	}
	if got := s.MetricInstrument(ctx, "m", ""); got != "" {
		t.Errorf("MetricInstrument = %q, want empty", got)
	}
	if got := s.MetricUnit(ctx, "m", ""); got != "" {
		t.Errorf("MetricUnit = %q, want empty", got)
	}
	if got := s.MetricPresentKeys(ctx, "m", []string{"job"}, time.Hour); got != nil {
		t.Errorf("MetricPresentKeys = %v, want nil", got)
	}
}
