package api

// v0.9.1157 — VictoriaMetrics Faz 2 at the HTTP layer: the histogram heatmap
// and the raw-query proxy joining the backend seam.
//
// Four properties are pinned here, and the first two are the ones that would
// break a working ClickHouse install rather than merely fail on VM:
//
//  1. THE CH PATH IS UNCHANGED. Both handlers were rewritten to go through
//     the seam, and on a ClickHouse-only install (every install today) the
//     rewrite must be invisible. Pinned as the exact CACHE KEY a paramless
//     request builds, because the key is the one artefact that carries the
//     whole request shape and is cheap to assert without a live store.
//  2. THE VALIDATION ASYMMETRY IS DELIBERATE. CH parses, VM does not. Get
//     this backwards and an operator's working MetricsQL query 400s inside
//     Coremetry while running fine in vmui — which reads as a broken
//     endpoint, not as a guardrail.
//  3. THE NOTE CAPABILITY IS VM-ONLY, and reaches the response body.
//  4. EVERY new VM method tags its errors, or a wedged VM 500s and reads as
//     a Coremetry bug (inflating its own self-observed error rate — v0.7.13).

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/cilcenk/coremetry/internal/chstore"
	"github.com/cilcenk/coremetry/internal/vmetrics"
)

// ── 1. The ClickHouse path is byte-identical for a paramless request ────────

// A fixed window so the minute-bucketed key is deterministic.
var faz2From = time.Unix(1_700_000_000, 0).UTC()
var faz2To = faz2From.Add(time.Hour)

func faz2Server() (*Server, *keyRecorderCache) {
	rc := &keyRecorderCache{}
	// VM has a URL but is DISABLED — the Settings default is ClickHouse, so
	// any src=vm in a key can only have come from the param.
	vm := vmetrics.New()
	vm.Configure(vmetrics.Settings{BaseURL: "http://vm:8428"})
	return &Server{
		store:    &chstore.Store{},
		vmetrics: vm,
		cache:    rc,
		l1:       newL1Cache(8),
		stats:    newCacheStats(),
	}, rc
}

// The heatmap's key, spelled out. Every segment is a result-affecting input,
// and the ORDER is part of the contract: a reordered key is a new key, which
// costs one cold read per pod — acceptable — but a key that DROPS a segment
// cross-poisons silently, which is not (v0.5.187).
func TestHistogramCHKeyIsPinned(t *testing.T) {
	s, rc := faz2Server()
	url := fmt.Sprintf(
		"/api/metrics/histogram?name=http.server.request.duration&service=cart&step=60&from=%d&to=%d",
		faz2From.UnixNano(), faz2To.UnixNano())
	s.getMetricHistogram(httptest.NewRecorder(), httptest.NewRequest("GET", url, nil))

	want := fmt.Sprintf(
		"metric-hist:src=ch:name=http.server.request.duration:svc=cart:step=60:f=:from=%d:to=%d:mx=%s",
		faz2From.Unix()/60, faz2To.Unix()/60, (&chstore.Store{}).MetricExclusions().Digest())
	if got := rc.last(); got != want {
		t.Fatalf("histogram key drifted:\n got  %s\n want %s", got, want)
	}
}

func TestPromQLCHKeyIsPinned(t *testing.T) {
	s, rc := faz2Server()
	url := fmt.Sprintf("/api/metrics/promql?query=up&maxDataPoints=1500&from=%d&to=%d",
		faz2From.UnixNano(), faz2To.UnixNano())
	s.queryPromQL(httptest.NewRecorder(), httptest.NewRequest("GET", url, nil))

	want := fmt.Sprintf("promql:src=ch:q=up:step=0:mdp=1500:from=%d:to=%d",
		faz2From.Unix()/60, faz2To.Unix()/60)
	if got := rc.last(); got != want {
		t.Fatalf("promql key drifted:\n got  %s\n want %s", got, want)
	}
}

// The heatmap handler must still thread EVERY field the query string
// carries into the filter it hands the seam.
//
// Scanned from source rather than exercised through a recorder, because a
// recorder test here is a tautology — it asserts the arguments the test itself
// passed. The failure this guards is a field that stopped being threaded
// during the seam rewrite, and the precedent is exact: the dashboards bundle
// dropped `Filters` for months (v0.9.566) and the result was not an empty
// panel, it was heap+non-heap summed and LABELLED heap. Nobody questions a
// plausible number.
func TestHistogramHandlerThreadsEveryFilterField(t *testing.T) {
	raw, err := os.ReadFile("api.go")
	if err != nil {
		t.Fatal(err)
	}
	src := stripGoComments(string(raw))

	i := strings.Index(src, "func (s *Server) getMetricHistogram")
	if i < 0 {
		t.Fatal("comment stripping ate getMetricHistogram — this scan proves nothing")
	}
	body := src[i:]
	if j := strings.Index(body, "\nfunc "); j > 0 {
		body = body[:j]
	}

	// The five fields the histogram read is defined by. Each is a
	// result-affecting input the operator can set from the UI.
	for _, field := range []string{
		"Name:", "Service:", "Filters:", "From:", "To:", "StepSeconds:",
	} {
		if !strings.Contains(body, field) {
			t.Errorf("getMetricHistogram no longer threads %s into the filter — a dropped input "+
				"is a WRONG heatmap, not an empty one (v0.9.566)", field)
		}
	}
	// And the read goes through the seam, not the store.
	if !strings.Contains(body, "src.QueryMetricHistogram(") {
		t.Error("getMetricHistogram does not call src.QueryMetricHistogram — the ?metricsrc= " +
			"override and the VictoriaMetrics backend would not apply to the heatmap")
	}
	// Filters must be PARSED, not passed as the raw string (which would
	// compile fine against a []FilterExpr field only if someone changed the
	// type, and would silently apply nothing).
	if !strings.Contains(body, "parseFilters(filtersRaw)") {
		t.Error("getMetricHistogram no longer parses the filters param")
	}
}

// ── 2. The validation asymmetry ─────────────────────────────────────────────

// CH parses; VM does not. Both halves asserted, because either one alone
// passes for the wrong reason: "CH rejects garbage" is satisfied by a gate
// that rejects everything, and "VM accepts a MetricsQL extension" is
// satisfied by a gate that validates nothing anywhere.
func TestValidatePromQLAsymmetry(t *testing.T) {
	ch := chMetricSource{&chstore.Store{}}
	vm := vmMetricSource{vmetrics.New()}

	// Real syntax errors: 400 on the CH path, with the parser's own text.
	for _, bad := range []string{"sum(", "up{", ")))", "1 +"} {
		err := ch.ValidatePromQL(bad)
		if err == nil {
			t.Errorf("CH accepted the malformed query %q — the operator would get a 500 from the "+
				"evaluator instead of a 400 naming the syntax error", bad)
			continue
		}
		if !errors.Is(err, errBadRequest) {
			t.Errorf("CH refusal for %q is not errBadRequest (%v) — it would 500", bad, err)
		}
		if !isBadRequest(err) {
			t.Errorf("CH refusal for %q does not map to 400: %v", bad, err)
		}
	}

	// Valid PromQL passes the CH gate — otherwise the gate is just "no".
	for _, good := range []string{"up", `sum(rate(m[5m]))`, `sum by (le) (rate(m[5m]))`} {
		if err := ch.ValidatePromQL(good); err != nil {
			t.Errorf("CH rejected valid PromQL %q: %v", good, err)
		}
	}

	// THE VM HALF. Every string below is valid MetricsQL that Coremetry's own
	// parser rejects. VM must accept all of them, or an operator's working
	// query fails only inside Coremetry.
	metricsQLOnly := []string{
		`WITH (cpu = rate(node_cpu_seconds_total[5m])) sum(cpu)`,
		`sum(rate(m[5m:1m]))`,
		`rollup_rate(m[5m])`,
		`sum(m) keep_metric_names`,
		`quantile_over_time(0.9, m[1h])`,
	}
	for _, q := range metricsQLOnly {
		if err := vm.ValidatePromQL(q); err != nil {
			t.Errorf("VM pre-validated %q and refused it (%v) — MetricsQL is a PromQL SUPERSET; "+
				"being stricter than the engine that runs the query reads as a broken endpoint", q, err)
		}
		// The premise: our parser really does reject these. A test that
		// asserted "VM accepts them" while the CH parser had grown to accept
		// them too would prove nothing about the asymmetry.
		if err := ch.ValidatePromQL(q); err == nil {
			t.Logf("note: the CH parser now accepts %q — this row no longer demonstrates the "+
				"asymmetry, pick another MetricsQL-only construct", q)
		}
	}
	// And the degenerate case: VM accepts even outright garbage, because VM is
	// the authority on its own dialect and its error message is the better
	// diagnosis.
	if err := vm.ValidatePromQL("sum("); err != nil {
		t.Errorf("VM refused a malformed query locally instead of letting VM answer: %v", err)
	}
}

// A CH-path syntax error must be answered BEFORE the cache is consulted: a
// 400 body is not a cacheable answer, and a key built for a query that never
// ran would be a lie about what it holds.
func TestPromQLSyntaxErrorNeverTouchesTheCache(t *testing.T) {
	s, rc := faz2Server()
	w := httptest.NewRecorder()
	s.queryPromQL(w, httptest.NewRequest("GET", "/api/metrics/promql?query=sum%28", nil))
	if w.Code != http.StatusBadRequest {
		t.Fatalf("code = %d, want 400; body: %s", w.Code, w.Body.String())
	}
	if got := rc.last(); got != "" {
		t.Fatalf("a rejected query reached the cache with key %q", got)
	}
	// The parser's own message survives — "unexpected" / the offending token
	// is the whole diagnosis, and a generic "bad query" costs the operator a
	// guessing round.
	if !strings.Contains(w.Body.String(), "promql") {
		t.Fatalf("the parser's message was swallowed: %s", w.Body.String())
	}
}

// The same query on the VM path must NOT be refused at the gate. It will fail
// on transport (vm:8428 is not there) — a 502 — and that is the point: the
// refusal moved from Coremetry to the engine that owns the dialect.
func TestPromQLVMPathIsNotPreValidated(t *testing.T) {
	// A MISS cache, deliberately: the point is that the request reaches the
	// VM transport. keyRecorderCache answers every key with a HIT, which
	// would return 200 from cache and prove nothing about the gate.
	s, _ := faz2Server()
	s.cache = &fakeCache{}
	for _, q := range []string{
		`WITH (x = rate(m[5m])) sum(x)`,
		`sum(rate(m[5m:1m]))`,
	} {
		w := httptest.NewRecorder()
		s.queryPromQL(w, httptest.NewRequest("GET",
			"/api/metrics/promql?metricsrc=vm&query="+url.QueryEscape(q), nil))
		if w.Code == http.StatusBadRequest {
			t.Fatalf("MetricsQL %q was 400'd on the VM path — Coremetry pre-validated a query it "+
				"does not own the dialect of; body: %s", q, w.Body.String())
		}
		if w.Code != http.StatusBadGateway {
			t.Fatalf("query %q: code = %d, want 502 (unreachable VM), body: %s", q, w.Code, w.Body.String())
		}
	}
}

// A missing metric name is a CLIENT error on the heatmap too — the same
// v0.9.1152 fix its sibling handler got. Without it the store's "metric name
// required" is wrapped as errUpstream on the VM path and surfaces as 502,
// sending the operator to check a perfectly healthy VictoriaMetrics.
func TestHistogramMissingNameIs400(t *testing.T) {
	for _, url := range []string{
		"/api/metrics/histogram?service=cart",
		"/api/metrics/histogram?name=%20%20&service=cart",
		"/api/metrics/histogram?metricsrc=vm&service=cart",
	} {
		s, rc := faz2Server()
		w := httptest.NewRecorder()
		s.getMetricHistogram(w, httptest.NewRequest("GET", url, nil))
		if w.Code != http.StatusBadRequest {
			t.Errorf("%s: code = %d, want 400; body: %s", url, w.Code, w.Body.String())
		}
		if got := rc.last(); got != "" {
			t.Errorf("%s: a nameless request reached the cache with key %q", url, got)
		}
	}
}

// ── 3. The note capability ──────────────────────────────────────────────────

// VM implements metricNoteSource; ClickHouse deliberately does not. Asserted
// structurally (a type assertion) rather than by string-matching, so it is
// really a guard against someone adding the method to the CH adapter "for
// symmetry" — which would mean answering "why is this empty?" from a backend
// that guessed no names and therefore has nothing to say.
func TestNoteCapabilityIsVMOnly(t *testing.T) {
	if _, ok := any(vmMetricSource{vmetrics.New()}).(metricNoteSource); !ok {
		t.Error("vmMetricSource does not implement metricNoteSource — the percentile note would " +
			"be built and then dropped, and the operator would see the silent blank chart again")
	}
	if _, ok := any(chMetricSource{&chstore.Store{}}).(metricNoteSource); ok {
		t.Error("chMetricSource implements metricNoteSource — ClickHouse reads the bucket layout " +
			"out of the row and guesses no names, so a note there would imply a symmetry that " +
			"does not exist")
	}
}

// queryMetricNoted must fall back cleanly for a source without the
// capability: same series, empty note, same error. The fallback is what keeps
// the handler free of a backend branch.
func TestQueryMetricNotedFallsBackForPlainSources(t *testing.T) {
	plain := &noteFreeSource{series: []chstore.SpanMetricSeries{{GroupKey: []string{"a"}}}}
	series, note, err := queryMetricNoted(context.Background(), plain, chstore.MetricQueryFilter{Name: "m"})
	if err != nil {
		t.Fatal(err)
	}
	if note != "" {
		t.Fatalf("a note-free source produced a note: %q", note)
	}
	if len(series) != 1 || series[0].GroupKey[0] != "a" {
		t.Fatalf("series did not pass through: %+v", series)
	}
	// An error passes through untouched too — swallowing it here would turn a
	// failed read into an empty chart.
	plain.err = errors.New("boom")
	if _, _, err := queryMetricNoted(context.Background(), plain, chstore.MetricQueryFilter{Name: "m"}); err == nil {
		t.Fatal("queryMetricNoted swallowed the source's error")
	}
}

type noteFreeSource struct {
	metricSource
	series []chstore.SpanMetricSeries
	err    error
}

func (noteFreeSource) Name() string { return metricSourceCH }
func (n *noteFreeSource) QueryMetric(context.Context, chstore.MetricQueryFilter) ([]chstore.SpanMetricSeries, error) {
	return n.series, n.err
}

// END TO END: an empty percentile against a live (fake) VM lands a `note` in
// the /api/metrics/query response body.
//
// Worth a real handler + a real VM stub rather than a unit test of the
// plumbing, because the failure mode is a link going missing between the
// three layers (vmetrics builds it → the seam forwards it → the handler
// attaches it) and each layer's own test would still pass.
func TestQueryMetricBodyCarriesThePercentileNote(t *testing.T) {
	vmSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.Contains(r.URL.Query().Get("query"), "histogram_quantile") {
			t.Errorf("p99 did not translate to histogram_quantile: %s", r.URL.Query().Get("query"))
		}
		_, _ = w.Write([]byte(`{"status":"success","data":{"resultType":"matrix","result":[]}}`))
	}))
	defer vmSrv.Close()

	vm := vmetrics.New()
	vm.Configure(vmetrics.Settings{BaseURL: vmSrv.URL})
	s := &Server{
		store: &chstore.Store{}, vmetrics: vm,
		cache: &fakeCache{}, l1: newL1Cache(8), stats: newCacheStats(),
	}

	w := httptest.NewRecorder()
	s.queryMetric(w, httptest.NewRequest("GET",
		"/api/metrics/query?metricsrc=vm&name=jvm.memory.used&agg=p99", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("code = %d, body: %s", w.Code, w.Body.String())
	}
	var body struct {
		Series []chstore.SpanMetricSeries `json:"series"`
		Note   string                     `json:"note"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("body is not the expected envelope: %v (%s)", err, w.Body.String())
	}
	if len(body.Series) != 0 {
		t.Fatalf("expected an empty result, got %d series", len(body.Series))
	}
	if body.Note == "" {
		t.Fatal("the envelope carries no note — the operator sees a blank chart and cannot tell " +
			"'no data' from 'this metric is not a histogram'")
	}
	if !strings.Contains(body.Note, "jvm.memory.used_bucket") {
		t.Fatalf("note does not name the series that was queried: %s", body.Note)
	}
}

// And the inverse: an ordinary empty result carries NO note field at all, so
// the frontend's `note ? … : default` check stays meaningful. A `"note":""`
// on every quiet metric would make the field worthless.
func TestQueryMetricBodyOmitsTheNoteForOrdinaryEmptyResults(t *testing.T) {
	vmSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"status":"success","data":{"resultType":"matrix","result":[]}}`))
	}))
	defer vmSrv.Close()

	vm := vmetrics.New()
	vm.Configure(vmetrics.Settings{BaseURL: vmSrv.URL})
	s := &Server{
		store: &chstore.Store{}, vmetrics: vm,
		cache: &fakeCache{}, l1: newL1Cache(8), stats: newCacheStats(),
	}
	w := httptest.NewRecorder()
	s.queryMetric(w, httptest.NewRequest("GET",
		"/api/metrics/query?metricsrc=vm&name=jvm.memory.used&agg=avg", nil))
	if strings.Contains(w.Body.String(), `"note"`) {
		t.Fatalf("an ordinary empty result attached a note: %s", w.Body.String())
	}
}

// ── 4. Error tagging on the new methods ─────────────────────────────────────

// An untagged error 500s, which both blames Coremetry in the UI and inflates
// coremetry-api's self-observed error rate into a false anomaly (v0.7.13).
func TestFaz2VMSourceTagsEveryError(t *testing.T) {
	// Unconfigured service → every read returns its own "not configured"
	// error, the cheapest way to exercise both paths without a live VM.
	v := vmMetricSource{vmetrics.New()}
	ctx := context.Background()

	_, errHist := v.QueryMetricHistogram(ctx, chstore.MetricQueryFilter{Name: "m"})
	_, errProm := v.QueryPromQLRange(ctx, "up", faz2From, faz2To, 60, 0)
	_, _, errNoted := v.QueryMetricNoted(ctx, chstore.MetricQueryFilter{Name: "m"})

	for name, err := range map[string]error{
		"QueryMetricHistogram": errHist,
		"QueryPromQLRange":     errProm,
		"QueryMetricNoted":     errNoted,
	} {
		if err == nil {
			t.Fatalf("%s: want an error from an unconfigured backend, got nil", name)
		}
		if !errors.Is(err, errUpstream) {
			t.Errorf("%s: error is not tagged errUpstream (%v) — it would surface as a 500", name, err)
		}
		if !isUpstream(err) {
			t.Errorf("%s: error does not map to 502: %v", name, err)
		}
	}

	// A TRANSLATION refusal on the new paths is still a 400, not a 502 — VM
	// is healthy, the request is what does not translate.
	vmOK := vmMetricSource{vmetrics.New()}
	vmOK.svc.Configure(vmetrics.Settings{BaseURL: "http://vm:8428"})
	_, err := vmOK.QueryMetricHistogram(ctx, chstore.MetricQueryFilter{
		Name: "m", Instance: "pg-1", Engine: "postgres", From: faz2From, To: faz2To,
	})
	if err == nil {
		t.Fatal("instance scoping must be refused on the VM heatmap path")
	}
	if !errors.Is(err, errBadRequest) || errors.Is(err, errUpstream) {
		t.Fatalf("heatmap translation refusal is misclassified (%v) — a 502 would blame a "+
			"healthy VictoriaMetrics", err)
	}
}

// Compile-time proof that the Faz 2 methods are on the seam for BOTH
// adapters. The `var _ = []metricSource{…}` line in metricsource_test.go
// already covers this, but naming the new methods here makes a signature
// change fail with a message about Faz 2 rather than about an anonymous slice.
var _ = func() bool {
	var _ interface {
		QueryMetricHistogram(context.Context, chstore.MetricQueryFilter) (*chstore.HistogramSeries, error)
		ValidatePromQL(string) error
		QueryPromQLRange(context.Context, string, time.Time, time.Time, int, int) ([]chstore.SpanMetricSeries, error)
	} = chMetricSource{}
	var _ interface {
		QueryMetricHistogram(context.Context, chstore.MetricQueryFilter) (*chstore.HistogramSeries, error)
		ValidatePromQL(string) error
		QueryPromQLRange(context.Context, string, time.Time, time.Time, int, int) ([]chstore.SpanMetricSeries, error)
	} = vmMetricSource{}
	return true
}()

// v0.9.1158 — CH değerlendiricisinin sorgu-şekli hataları 400 sınıfına
// sarılır (1157 canlı doğrulama bulgusu: range_last CH'de 500 dönüyordu;
// 1152/1155 sınıfının promql yolu). Önek sözleşmesini iki yönden pinler:
// "promql:" önekli hata errBadRequest'e sarılır, öneksiz (store) hata
// olduğu gibi kalır.
func TestPromQLEvalQueryShapeErrorsAreBadRequest(t *testing.T) {
	shape := errors.New("promql: function \"range_last\" is not supported yet")
	if wrapped := classifyPromQLEvalErr(shape); !errors.Is(wrapped, errBadRequest) {
		t.Fatalf("sorgu-şekli hatası errBadRequest'e sarılmadı: %v", wrapped)
	}
	store := errors.New("clickhouse: read timeout")
	if wrapped := classifyPromQLEvalErr(store); errors.Is(wrapped, errBadRequest) {
		t.Fatalf("store hatası yanlışlıkla 400 sınıfına girdi: %v", wrapped)
	}
}
