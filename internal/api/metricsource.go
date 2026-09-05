package api

// metricSource — the ONE seam every operator-facing metric read passes
// through (v0.9.1150, VictoriaMetrics read backend Faz 1).
//
// Before this file the five metric handlers called *chstore.Store
// directly. Adding a second backend by editing each call site would have
// produced the drift class that keeps costing us releases: one surface
// migrated, another not, and no compiler or test noticing (v0.9.566 —
// the dashboards "metric" branch had silently diverged from
// /api/metrics/query on filters for months).
//
// So the seam is an INTERFACE both backends satisfy with IDENTICAL method
// signatures, deliberately copied from *chstore.Store. Signature drift on
// either side is a compile error. A source-pin test
// (metricsource_test.go) additionally asserts api.go contains no direct
// s.store.<metric method> call, so a new handler cannot rejoin the old
// path by accident.
//
// WHAT IS IN SCOPE is as important as what the seam does. Faz 1 routes
// the metric DISCOVERY + QUERY surfaces an operator drives by hand:
// catalogue/picker, Explore, dashboard metric panels, MCP query_metric,
// label values, attribute keys. Everything else stays on ClickHouse ON
// PURPOSE:
//
//   - span-derived reads (services, operations, topology, traces, …) are
//     not metrics at all;
//   - fixed-name internal readers (hosts, infra, JVM panels, db capacity, the
//     DQL evaluator in dql.go) each hard-code metric names and CH-side column
//     behaviour. Routing them piecemeal would put two backends behind one
//     page.
//
// v0.9.1268 REMOVED ONE NAME FROM THAT SECOND LIST, and the removal is the
// more instructive half of the rule. service_metric_throughput.go — the
// service-Overview throughput mapper — was listed as deliberately ClickHouse.
// That reasoning was sound in v0.9.1150 and went stale the moment VM became a
// PRIMARY backend rather than an alternative view: on a VM install the panel
// searched a store the metric does not live in, and reported the honest empty
// that operator-reported bug arrived as. "Deliberately scoped to CH" and
// "pinned to the wrong store" are the same code; only the deployment around it
// decides which one it is.
//
// So the second list means "reads that a backend could not answer
// differently", not "reads we have not gotten to". dql.go still qualifies —
// its evaluator IS ClickHouse query machinery. A fixed metric NAME never
// qualified on its own.
//
// v0.9.1157 (Faz 2) brought the last two operator-driven metric surfaces
// through the seam: GET /api/metrics/histogram and GET /api/metrics/promql.
// They joined LATE and separately because neither is a signature copy of a
// chstore method the way the Faz 1 four are:
//
//   - the histogram IS chstore-shaped (QueryMetricHistogram), so it is a
//     plain seam method and the compile-time identity argument applies
//     unchanged;
//   - the PromQL proxy is NOT. Its ClickHouse half is internal/promql's
//     evaluator over the store, not a store method, so the seam declares
//     its own signature and each adapter owns its dialect. That is also
//     where the deliberate asymmetry lives: CH pre-parses (ValidatePromQL),
//     VM does not, because MetricsQL is a superset our parser would reject.
//
// There is NO fallback from VM to CH. If the operator turned VM on and VM
// is down, the endpoint fails with VM's error (502 at the handler). A
// fallback would answer with numbers from a store the operator did not
// select, and unlike the Tempo trace fallback — where a banner is enough
// because the trace is either there or not — the NUMBERS would differ and
// nothing on screen could say so.

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/cilcenk/coremetry/internal/chstore"
	"github.com/cilcenk/coremetry/internal/promql"
	"github.com/cilcenk/coremetry/internal/vmetrics"
)

// errUpstream marks a failure that came from an EXTERNAL read backend
// rather than from Coremetry. writeErr maps it to 502 so the frontend's
// existing error states can say "the metrics backend answered like this"
// instead of implying Coremetry broke — and so a wedged VM does not
// inflate coremetry-api's self-observed 500 rate and trip its own
// anomaly detectors (the v0.7.13 lesson about miscounted status codes).
//
// The wrapped error's text is preserved verbatim: "connection refused",
// "401 Unauthorized" and "unknown label" are the three things that
// actually tell the operator what to fix.
var errUpstream = errors.New("upstream metrics backend")

// errBadRequest marks a request Coremetry cannot EXPRESS against the
// selected backend (a histogram percentile, a filter operator with no
// MetricsQL equivalent). 400, not 502.
//
// The distinction is a diagnosis, not pedantry: 502 says "your
// VictoriaMetrics is broken" and sends the operator to check a cluster
// that is perfectly healthy. The thing that needs changing is the query.
var errBadRequest = errors.New("unsupported request")

// Source names for the wire + cache keys. Short because they ride in
// every metric cache key.
const (
	metricSourceCH = "ch"
	metricSourceVM = "vm"
)

// metricNameRuleTag stamps the VM metric-NAME candidate rule into a cache key,
// and does it for the VM source ONLY (v0.9.1159).
//
// Why a stamp at all: v0.9.1159 changed which SERIES a VM query resolves to
// (OTLP `http.server.request.duration` now also matches VM's
// `http_server_request_duration_seconds_bucket`). The request is byte-identical
// and so is the key, so a warm Redis entry written before the deploy keeps
// serving the EMPTY body for its full TTL — 30s on the query and heatmap keys,
// 60s on the two discovery keys. Thirty seconds of the exact symptom this
// release fixes is worse than it sounds: the operator's first look after the
// deploy is the one that decides whether they believe it shipped (the v0.9.1157
// precedent bumped `metric-query:v4` for the same reason, and v0.9.443/458
// taught it).
//
// Why NOT a plain version bump on the shared key: these keys serve BOTH
// backends. A bare bump would also cold-read every ClickHouse entry, whose body
// this release does not touch — and the CH cold read behind the two discovery
// keys is the DISTINCT metric_points scan they were cached to avoid (v0.8.456).
// Paying a scan wave on a prod ClickHouse for a change that cannot affect its
// answer is the wrong trade; the tag is empty for CH, so its keys are
// byte-identical to yesterday's.
//
// Bump the digit whenever the candidate rules in internal/vmetrics/names.go
// change what a name resolves to.
//
//	n1 — v0.9.1159, the original candidate alternation.
//	n2 — v0.9.1160, rate/increase gained the `…_count` derivatives. A warm n1
//	     entry holds the 0-series body that finding reported, and it is the
//	     throughput half of a chart whose latency half already works — the most
//	     confusing possible state to leave a panel in for a full TTL.
//	n2:rwf=N — v0.9.1165, rate pencere tabanı anahtara girdi. 1164'ün
//	     canlı probu taban 60↔600 arasında BAYT-AYNI gövde gösterdi ve
//	     "kablo kopuk" dedi; kopuk olan kablo değil anahtardı — taban
//	     emitted pencereyi değiştirir ama istek bayt-aynıdır, ayar
//	     PUT'undan sonraki TTL boyunca eski tabanın gövdesi servis
//	     ediliyordu. ÇÖZÜLMÜŞ taban yazılır (0 ve 300 aynı davranış =
//	     aynı anahtar, gereksiz soğuk okuma yok).
func (s *Server) metricNameRuleTag(src metricSource) string {
	if src != nil && src.Name() == metricSourceVM {
		return fmt.Sprintf(":n2:rwf=%d", s.vmetrics.ResolvedRateWindowFloor())
	}
	return ""
}

type metricSource interface {
	// Name is the backend marker. It goes in the CACHE KEY of every
	// metric endpoint and in the /api/metrics/names response so the
	// catalogue can badge its source. Without it in the key, flipping the
	// toggle would serve the old backend's bodies for a full TTL and two
	// pods refreshing at different moments would disagree — the
	// cross-poisoning class of v0.5.187.
	Name() string

	ListMetricNames(ctx context.Context, service, pattern string, limit, offset int) ([]chstore.MetricInfo, int, error)
	QueryMetric(ctx context.Context, f chstore.MetricQueryFilter) ([]chstore.SpanMetricSeries, error)
	MetricLabelValues(ctx context.Context, metric, key string, since time.Duration) ([]string, error)
	MetricAttrKeys(ctx context.Context, metric, service string, since time.Duration) ([]string, error)

	// QueryMetricHistogram — the time × bucket heatmap behind
	// /api/metrics/histogram (v0.9.1157, Faz 2). Signature copied from
	// *chstore.Store like the four above.
	QueryMetricHistogram(ctx context.Context, f chstore.MetricQueryFilter) (*chstore.HistogramSeries, error)

	// ValidatePromQL is the PRE-CACHE syntax gate for /api/metrics/promql,
	// and the two implementations deliberately disagree.
	//
	// ClickHouse parses: internal/promql implements a PromQL SUBSET and can
	// only answer what it can push into CH, so rejecting early gives the
	// operator a clean 400 with the parser's own message before a cache slot
	// or a round trip is spent. VictoriaMetrics returns nil — MetricsQL is a
	// PromQL SUPERSET, so our parser would 400 `WITH(…)`, subqueries and
	// `keep_metric_names`: queries that run fine in vmui. An operator whose
	// working query fails only inside Coremetry reads that as a broken
	// endpoint.
	//
	// A seam method rather than a `src.Name() == "ch"` branch in the handler
	// so both behaviours are compile-required and the VM side's "nil is the
	// answer" reasoning lives next to the code that returns it.
	ValidatePromQL(query string) error

	// ── The throughput mapper's needs (v0.9.1268) ──────────────────────────
	//
	// service_metric_throughput.go used to call *chstore.Store for all of
	// these and was therefore pinned to ClickHouse. On a VictoriaMetrics
	// install that produced the operator's report: the panel searched a store
	// the metric does not live in and said "bu servise eşleşen seri yok" —
	// honest about ClickHouse, wrong about the question.
	//
	// The names and signatures are copied from *chstore.Store like every
	// method above, so drift on either side is a compile error. They are on
	// the seam ENTIRELY rather than as the two obvious query methods, because
	// a handler that routes its query but not its diagnostics tells the
	// operator about one store's labels while the chart searched another's —
	// the same wrong-store blindness in a place nobody would think to look.

	// MetricExists picks WHICH of the mapper's candidate metric names is
	// used, so it has to be answered by the store that will be queried.
	MetricExists(ctx context.Context, name string) (bool, error)

	// MetricInstrument returns "sum", "histogram", "" (unknown) or a gauge
	// class the mapper refuses. It decides which of the two rate methods
	// below is called.
	MetricInstrument(ctx context.Context, name, service string) string

	QueryMetricRate(ctx context.Context, f chstore.MetricQueryFilter, mode string) ([]chstore.SpanMetricSeries, error)
	QueryMetricCountRate(ctx context.Context, f chstore.MetricQueryFilter, mode string) ([]chstore.SpanMetricSeries, error)

	// MetricUnit feeds the panel's axis. A unit read from the other store is
	// worse than no unit: it labels an axis with a confidence nothing earned.
	MetricUnit(ctx context.Context, metric, service string) string

	// LatencyMetricName translates the name the THROUGHPUT mapper resolved into
	// the name a VALUE read (avg / latency) must use (v0.9.1274,
	// operator-reported).
	//
	// The two questions have different right answers on the SAME metric, which
	// is what the bug was: a histogram's throughput is `rate(<f>_count)`, so the
	// mapper legitimately resolves `…_seconds_count` on a VM install — and the
	// service Overview then carried that resolved name into its two `agg=avg`
	// panels. There, an explicit `_count` name is read as a deliberate operator
	// pick (mayHaveHistogramParts' documented refusal), so the query became a
	// raw `avg()` over a CUMULATIVE COUNTER, formatted as seconds because the
	// name says `_seconds`. The operator's axis read "14.2 weeks".
	//
	// PURE and synchronous — no ctx, no error. It is a naming rule, not a probe:
	// asking the store "which family does this part belong to" would add a round
	// trip to answer something the suffix already states, and the existence-is-
	// not-liveness trap (names.go) says a probe here could lock onto a stale
	// family anyway. Composition stays the DATA's job: the trimmed family name
	// is what re-opens buildPromQL's `or` arms, which self-select.
	//
	// ClickHouse returns the name UNCHANGED, and that is a decision rather than
	// a stub: the CH avg path reads a histogram row's own sum/count COLUMNS, so
	// there is no part-suffix to strip and no behaviour to change. This release
	// must be invisible on a ClickHouse install.
	LatencyMetricName(name string) string

	// MetricPresentKeys answers "does this metric carry this identity key at
	// all" — the diagnostic that separates a mis-configured collector from a
	// mismatched value (v0.9.682).
	MetricPresentKeys(ctx context.Context, metric string, keys []string, since time.Duration) []string

	// ServiceIdentityLabels is the ONE method here with no chstore twin, and
	// it exists because the two backends genuinely disagree about the
	// question rather than about the answer.
	//
	// In ClickHouse a service's identity lives in the `service_name` COLUMN,
	// and chstore.ServiceIdentityLabels lists only the ATTRIBUTES that might
	// carry it instead. In VictoriaMetrics there are no columns: the resource
	// attribute lands as the ordinary label `service_name`, which on an
	// OTLP-fed install is the likeliest identity of all — and which the CH
	// list therefore does not contain. Deriving the VM list by translating
	// the CH one would reproduce that gap exactly.
	ServiceIdentityLabels() []string

	// EnvFilterExpr renders the global env picker as a query conjunct, or
	// reports that this backend CANNOT express it.
	//
	// ClickHouse can: its filter compiler coalesces every semconv spelling of
	// deployment.environment behind one key (metricPointsWellKnown), so a
	// single `=` conjunct matches whichever the install writes.
	//
	// VictoriaMetrics cannot. A MetricsQL matcher names ONE label, and the
	// spellings are different LABELS there (`deployment_environment` vs
	// `deployment_environment_name`), so any single matcher we pick is right
	// for some installs and silently empties the panel on the rest. Refusing
	// is the honest half; the mapper marks the result env-ambiguous so the
	// operator is told the series may span environments rather than being
	// shown a narrowed chart that narrowed by nothing. Env still reaches the
	// VM path through the identity itself — k8s_deployment_name carries the
	// environment suffix, and serviceNameAttempts constrains the suffix-less
	// spelling with its own env conjunct.
	EnvFilterExpr(env string) (chstore.FilterExpr, bool)

	// QueryPromQLRange runs an operator-written range query.
	//
	// Explicit parameters instead of an options struct because there is no
	// shared type to reach for: the CH side's is internal/promql.EvalOptions
	// (its evaluator's own knobs) and importing that into vmetrics would make
	// the VictoriaMetrics reader depend on the ClickHouse evaluator for a
	// bag of ints.
	QueryPromQLRange(ctx context.Context, query string, from, to time.Time, stepSeconds, maxDataPoints int) ([]chstore.SpanMetricSeries, error)
}

// metricNoteSource is an OPTIONAL seam capability: a backend that can EXPLAIN
// an empty result implements it, and the handler type-asserts (v0.9.1157).
//
// Optional rather than part of metricSource above because only one backend
// has anything to say. VictoriaMetrics answers a percentile by querying
// `<name>_bucket` — a series name the operator never typed and cannot see —
// so an empty chart there conflates "no data in this window", "this metric is
// not a histogram" and "your write path spells buckets differently", three
// situations with three different fixes. ClickHouse reads the bucket layout
// out of the row itself, guesses no names, and therefore has no note to add;
// forcing it to implement a method that always returns "" would be ceremony
// that implies a symmetry the two backends do not have.
//
// Stateless by construction — the note is a RETURN VALUE, not a field on the
// source. A `LastNote()` accessor would have been the shorter diff and would
// have raced between two concurrent requests through the same *Service,
// attaching one panel's note to another panel's body.
type metricNoteSource interface {
	QueryMetricNoted(ctx context.Context, f chstore.MetricQueryFilter) ([]chstore.SpanMetricSeries, string, error)
}

// queryMetricNoted is the one call site of that capability: the note when the
// backend can produce one, "" otherwise. Handlers use this instead of
// src.QueryMetric so a source that GAINS the capability starts being heard
// without touching the handler.
func queryMetricNoted(ctx context.Context, src metricSource, f chstore.MetricQueryFilter) ([]chstore.SpanMetricSeries, string, error) {
	if n, ok := src.(metricNoteSource); ok {
		return n.QueryMetricNoted(ctx, f)
	}
	series, err := src.QueryMetric(ctx, f)
	return series, "", err
}

// chMetricSource is the default: the ClickHouse warm store. Pure
// delegation — the receiver is NOT named `s` so the source-pin test's
// `s.store.<method>` pattern stays a precise signal.
type chMetricSource struct{ store *chstore.Store }

func (chMetricSource) Name() string { return metricSourceCH }

func (c chMetricSource) ListMetricNames(ctx context.Context, service, pattern string, limit, offset int) ([]chstore.MetricInfo, int, error) {
	return c.store.ListMetricNames(ctx, service, pattern, limit, offset)
}

func (c chMetricSource) QueryMetric(ctx context.Context, f chstore.MetricQueryFilter) ([]chstore.SpanMetricSeries, error) {
	return c.store.QueryMetric(ctx, f)
}

func (c chMetricSource) MetricLabelValues(ctx context.Context, metric, key string, since time.Duration) ([]string, error) {
	return c.store.MetricLabelValues(ctx, metric, key, since)
}

func (c chMetricSource) MetricAttrKeys(ctx context.Context, metric, service string, since time.Duration) ([]string, error) {
	return c.store.MetricAttrKeys(ctx, metric, service, since)
}

func (c chMetricSource) QueryMetricHistogram(ctx context.Context, f chstore.MetricQueryFilter) (*chstore.HistogramSeries, error) {
	return c.store.QueryMetricHistogram(ctx, f)
}

// ── The throughput mapper's methods, ClickHouse half (v0.9.1268) ───────────
//
// Pure delegation, like every method above. The behaviour is BYTE-IDENTICAL to
// the direct s.store calls these replaced, which is the property the delegation
// parity test pins: this release must change what the VM install sees and
// nothing at all about what the ClickHouse install sees.

func (c chMetricSource) MetricExists(ctx context.Context, name string) (bool, error) {
	return c.store.MetricExists(ctx, name)
}

func (c chMetricSource) MetricInstrument(ctx context.Context, name, service string) string {
	return c.store.MetricInstrument(ctx, name, service)
}

func (c chMetricSource) QueryMetricRate(ctx context.Context, f chstore.MetricQueryFilter, mode string) ([]chstore.SpanMetricSeries, error) {
	return c.store.QueryMetricRate(ctx, f, mode)
}

func (c chMetricSource) QueryMetricCountRate(ctx context.Context, f chstore.MetricQueryFilter, mode string) ([]chstore.SpanMetricSeries, error) {
	return c.store.QueryMetricCountRate(ctx, f, mode)
}

func (c chMetricSource) MetricUnit(ctx context.Context, metric, service string) string {
	return c.store.MetricUnit(ctx, metric, service)
}

// LatencyMetricName — IDENTITY on ClickHouse, on purpose (v0.9.1274).
//
// No chstore twin to delegate to, because ClickHouse has no question to answer
// here: a histogram lands as ONE metric_points row carrying `sum` and `count`
// COLUMNS, so `QueryMetric(agg=avg)` computes sum/count off the row the caller
// already named. There is no `…_count` SERIES to be resolved onto and therefore
// nothing to trim.
//
// Spelled as a method with a named return rather than left to an embedded
// default so the CH half of this release is a written decision — and so the
// delegation-parity test can pin that this release changed nothing a
// ClickHouse install sees.
func (chMetricSource) LatencyMetricName(name string) string { return name }

func (c chMetricSource) MetricPresentKeys(ctx context.Context, metric string, keys []string, since time.Duration) []string {
	return c.store.MetricPresentKeys(ctx, metric, keys, since)
}

func (chMetricSource) ServiceIdentityLabels() []string { return chstore.ServiceIdentityLabels }

// EnvFilterExpr — the key the ClickHouse filter compiler coalesces. See the
// interface comment: `deployment.environment` there is not one spelling among
// several, it is the alias metricPointsWellKnown expands into all of them.
func (chMetricSource) EnvFilterExpr(env string) (chstore.FilterExpr, bool) {
	if strings.TrimSpace(env) == "" {
		return chstore.FilterExpr{}, false
	}
	return chstore.FilterExpr{Key: "deployment.environment", Op: "=", Values: []string{env}}, true
}

// ValidatePromQL — the ClickHouse half parses. See the interface's comment for
// why the VM half does not.
//
// The error is tagged errBadRequest so writeErr answers 400 with the parser's
// own text. This is where the pre-v0.9.1157 handler's up-front
// promql.Parse + writePromQLError(400) moved to; both produce a 400 with the
// same {"error": …} envelope, and keeping it BEFORE the cache lookup means a
// syntax error still never occupies a cache slot.
func (chMetricSource) ValidatePromQL(query string) error {
	if _, err := promql.Parse(query); err != nil {
		return fmt.Errorf("%w: %w", errBadRequest, err)
	}
	return nil
}

// QueryPromQLRange — parse + evaluate against the bounded chstore machinery.
//
// EvalString rather than Parse+Eval so the two arguments the handler cares
// about (query, window) are the only things threaded through; the AST is an
// internal of the evaluator. It reparses what ValidatePromQL already parsed,
// which costs microseconds on a query the handler caps at 8KB and buys the
// seam a string-only signature — the shape the VM side needs.
func (c chMetricSource) QueryPromQLRange(ctx context.Context, query string, from, to time.Time, stepSeconds, maxDataPoints int) ([]chstore.SpanMetricSeries, error) {
	return promql.EvalString(ctx, c.store, query, promql.EvalOptions{
		FromNs:        from.UnixNano(),
		ToNs:          to.UnixNano(),
		Step:          stepSeconds,
		MaxDataPoints: maxDataPoints,
	})
}

// vmMetricSource wraps the external VictoriaMetrics reader. The method
// set is delegation-only because vmetrics.Service already carries the
// chstore-identical signatures — that is the point of matching them.
type vmMetricSource struct {
	svc *vmetrics.Service
	// ex — the operator's metric exclusion rules (Settings → Pipeline →
	// Dışlama, v0.9.797). The ClickHouse reader applies them inside
	// every metric_points WHERE (applyMetricExclusionWhere); until
	// v0.10.367 the VictoriaMetrics path applied NOTHING while its cache
	// keys still carried the rules' digest — a rule for the health
	// probes hid them on CH and left them on VM. Read at query time so a
	// PUT takes effect without a rebuild of the source.
	ex func() *chstore.CompiledMetricExclusions
}

func newVMMetricSource(svc *vmetrics.Service, store *chstore.Store) vmMetricSource {
	return vmMetricSource{svc: svc, ex: store.MetricExclusions}
}

func (vmMetricSource) Name() string { return metricSourceVM }

// routeExclusionFilters — the rules that apply to `metric`, as `!~`
// matchers on http.route. A rule pattern is UNANCHORED RE2 ("/health"
// hits anywhere in the path, metric_exclusions.go); PromQL anchors a
// matcher fully, so the pattern is wrapped as `.*(?:pat).*` to keep the
// same set the ClickHouse `NOT match()` selects. Nil when no rule
// applies — the zero-impact pin.
func routeExclusionFilters(ex *chstore.CompiledMetricExclusions, metric string) []chstore.FilterExpr {
	pats := ex.RoutePatterns(metric)
	if len(pats) == 0 {
		return nil
	}
	out := make([]chstore.FilterExpr, 0, len(pats))
	for _, p := range pats {
		out = append(out, chstore.FilterExpr{Key: "http.route", Op: "!~", Values: []string{".*(?:" + p + ").*"}})
	}
	return out
}

// excluded — the filter with the operator's route exclusions appended.
// Copy-on-write: callers reuse their base filter across several calls.
func (v vmMetricSource) excluded(f chstore.MetricQueryFilter) chstore.MetricQueryFilter {
	if v.ex == nil {
		return f
	}
	add := routeExclusionFilters(v.ex(), f.Name)
	if len(add) == 0 {
		return f
	}
	f.Filters = append(append([]chstore.FilterExpr(nil), f.Filters...), add...)
	return f
}

// upstream classifies a VM error for the HTTP layer. One helper rather
// than four inline wraps: a new method added without the tag would 500
// and read as a Coremetry bug.
//
// Three outcomes, and picking the right one is the operator's diagnosis:
//   - vmetrics.ErrUnsupported → 400. VM is fine; the QUERY does not
//     translate (aggregation / filter operator / instance scoping).
//   - vmetrics.ErrUnfilteredBuckets → 400 (v0.9.1164). VM is fine and the
//     query translates; this install refuses to SEND it unfiltered. The
//     message carries the fix and the Settings off-switch, so the one thing
//     that must not happen is a 502 — that would point the operator at a
//     healthy cluster and hide a checkbox they own.
//   - anything else → 502. Transport, auth, or VM's own query error.
//
// Both 400 sentinels are listed EXPLICITLY rather than collapsed into a
// "not a transport error" catch-all: the default has to stay 502, so that a
// future error type added without a decision here is loud (a Coremetry bug
// reported as one) instead of being quietly reclassified as the operator's
// fault.
func upstream(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, vmetrics.ErrUnsupported) || errors.Is(err, vmetrics.ErrUnfilteredBuckets) {
		return fmt.Errorf("%w: %w", errBadRequest, err)
	}
	return fmt.Errorf("%w: %w", errUpstream, err)
}

func (v vmMetricSource) ListMetricNames(ctx context.Context, service, pattern string, limit, offset int) ([]chstore.MetricInfo, int, error) {
	out, total, err := v.svc.ListMetricNames(ctx, service, pattern, limit, offset)
	return out, total, upstream(err)
}

func (v vmMetricSource) QueryMetric(ctx context.Context, f chstore.MetricQueryFilter) ([]chstore.SpanMetricSeries, error) {
	out, err := v.svc.QueryMetric(ctx, v.excluded(f))
	return out, upstream(err)
}

func (v vmMetricSource) MetricLabelValues(ctx context.Context, metric, key string, since time.Duration) ([]string, error) {
	out, err := v.svc.MetricLabelValues(ctx, metric, key, since)
	return out, upstream(err)
}

func (v vmMetricSource) MetricAttrKeys(ctx context.Context, metric, service string, since time.Duration) ([]string, error) {
	out, err := v.svc.MetricAttrKeys(ctx, metric, service, since)
	return out, upstream(err)
}

func (v vmMetricSource) QueryMetricHistogram(ctx context.Context, f chstore.MetricQueryFilter) (*chstore.HistogramSeries, error) {
	out, err := v.svc.QueryMetricHistogram(ctx, v.excluded(f))
	return out, upstream(err)
}

// ValidatePromQL returns nil ON PURPOSE — no pre-validation on the VM path.
//
// VictoriaMetrics speaks MetricsQL, a PromQL SUPERSET. Coremetry's parser
// implements a subset of the SUBSET dialect, so running it here would 400
// `WITH(…)` templates, subqueries, `keep_metric_names`, `rollup_rate` — every
// one a query that works in vmui. Being stricter than the engine that will run
// the query reads to the operator as a broken endpoint, not as a guardrail.
//
// A malformed query is not unchecked, it is checked by the RIGHT authority: VM
// answers status != success and promapi surfaces its message verbatim, which
// is a better diagnosis than ours because it comes from the actual evaluator.
// The bound that stays ours is LENGTH, enforced in the handler for both
// backends.
func (vmMetricSource) ValidatePromQL(string) error { return nil }

func (v vmMetricSource) QueryPromQLRange(ctx context.Context, query string, from, to time.Time, stepSeconds, maxDataPoints int) ([]chstore.SpanMetricSeries, error) {
	out, err := v.svc.QueryPromQLRange(ctx, query, from, to, stepSeconds, maxDataPoints)
	return out, upstream(err)
}

// ── The throughput mapper's methods, VictoriaMetrics half (v0.9.1268) ──────
//
// Same upstream() tagging as every method above: an untagged error 500s and
// reads as a Coremetry bug rather than as "your VM is unreachable".
//
// The three that return a BARE value (no error) mirror their chstore twins,
// which swallow their own query errors the same way. That is not a dropped
// error: each is a diagnostic refinement on a path whose next call surfaces the
// real failure, and the mapper reads their zero value as "unknown" rather than
// as "absent".

func (v vmMetricSource) MetricExists(ctx context.Context, name string) (bool, error) {
	ok, err := v.svc.MetricExists(ctx, name)
	return ok, upstream(err)
}

func (v vmMetricSource) MetricInstrument(ctx context.Context, name, service string) string {
	return v.svc.MetricInstrument(ctx, name, service)
}

func (v vmMetricSource) QueryMetricRate(ctx context.Context, f chstore.MetricQueryFilter, mode string) ([]chstore.SpanMetricSeries, error) {
	out, err := v.svc.QueryMetricRate(ctx, v.excluded(f), mode)
	return out, upstream(err)
}

func (v vmMetricSource) QueryMetricCountRate(ctx context.Context, f chstore.MetricQueryFilter, mode string) ([]chstore.SpanMetricSeries, error) {
	out, err := v.svc.QueryMetricCountRate(ctx, v.excluded(f), mode)
	return out, upstream(err)
}

func (v vmMetricSource) MetricUnit(ctx context.Context, metric, service string) string {
	return v.svc.MetricUnit(ctx, metric, service)
}

// LatencyMetricName — the half that DOES something (v0.9.1274). Delegation,
// like every method here; the rule and its argument live in
// vmetrics.latencyFamilyName, next to the mayHaveHistogramParts gate it exists
// to re-open.
func (v vmMetricSource) LatencyMetricName(name string) string {
	return v.svc.LatencyMetricName(name)
}

func (v vmMetricSource) MetricPresentKeys(ctx context.Context, metric string, keys []string, since time.Duration) []string {
	return v.svc.MetricPresentKeys(ctx, metric, keys, since)
}

func (v vmMetricSource) ServiceIdentityLabels() []string { return v.svc.ServiceIdentityLabels() }

// EnvFilterExpr refuses ON PURPOSE — MetricsQL cannot express "either of these
// two LABELS equals x" in one matcher, and picking one spelling would empty the
// panel on every install that writes the other. See the interface comment for
// the full argument and for the paths through which env still narrows here.
func (vmMetricSource) EnvFilterExpr(string) (chstore.FilterExpr, bool) {
	return chstore.FilterExpr{}, false
}

// QueryMetricNoted satisfies the OPTIONAL metricNoteSource capability — the
// VM source is its only implementer. Same upstream() tagging as every other
// method: an untagged error 500s and reads as a Coremetry bug.
func (v vmMetricSource) QueryMetricNoted(ctx context.Context, f chstore.MetricQueryFilter) ([]chstore.SpanMetricSeries, string, error) {
	out, note, err := v.svc.QueryMetricNoted(ctx, v.excluded(f))
	return out, note, upstream(err)
}

// metricSource picks the backend from SETTINGS alone — the default when a
// request expresses no preference. Cheap (one RWMutex read behind
// Configured) so handlers call it inline; the returned value is used for
// BOTH the cache key and the read, which is what makes them impossible to
// disagree.
//
// HTTP handlers must call metricSourceFor(r) instead, so the per-request
// ?metricsrc= trial override applies. This function stays exported-in-
// package for the settings-driven callers that have no request:
// mcpDeps().
func (s *Server) metricSource() metricSource {
	if s.vmetrics != nil && s.vmetrics.Configured() {
		return newVMMetricSource(s.vmetrics, s.store)
	}
	return chMetricSource{s.store}
}

// ── Per-request source override (v0.9.1151, deneme modu) ────────────────────
//
// `?metricsrc=vm|ch` picks the backend for ONE request. The operator asked
// for it because metric NAMES differ between the two stores (VM sanitises
// `jvm.memory.used` to `jvm_memory_used`), so "does VM actually answer my
// dashboards" cannot be settled by reading Settings — it needs one real
// chart. The global toggle is the wrong instrument for that question: it
// moves every panel, picker and dashboard of every logged-in user at once,
// and moves them back just as loudly.
//
// Precedent: the old-engine escape hatch that carried the chart-engine
// migration (retired v0.9.844 — a frontend gate now bans that flag's NAME
// from the tree, so it is described here rather than named). Same posture,
// deliberately — a URL param, no UI that writes it, no persistence. An
// operator types it; nothing in the product hands it out. That is what
// keeps it a probe rather than a second, invisible configuration surface
// that some page could get stuck in.
//
// Two asymmetries worth naming, because both were choices:
//
//   - `metricsrc=vm` does NOT require Enabled. Requiring it would make
//     the param useless — trying VM before committing to it is the entire
//     point. It DOES require a base URL, because without one there is
//     nothing to call.
//   - `metricsrc=ch` is always valid, even with VM as the default. The
//     escape hatch has to work in both directions or an operator who
//     turned VM on and hit a gap has no way to check the same chart
//     against ClickHouse.
//
// There is still NO silent fallback. `metricsrc=vm` against an
// unconfigured VM is a 400 that names the Settings page, never a quiet
// ClickHouse read — the reason is the same one metricsource.go's header
// gives for refusing VM→CH fallback: the numbers would differ and nothing
// on screen could say so.
const metricSourceParam = "metricsrc"

// metricSourceParamValues is the accepted set, in the order the error
// message lists them.
var metricSourceParamValues = []string{metricSourceVM, metricSourceCH}

// resolveMetricSourceParam is the pure half of the override, kept
// separate so every branch is table-testable without a Server, a request
// or a live VM.
//
// raw is the verbatim query value; vmAvailable is
// vmetrics.Service.Available() (a base URL exists — Enabled NOT required).
// Returns "" for "the request has no opinion, use the Settings default".
func resolveMetricSourceParam(raw string, vmAvailable bool) (string, error) {
	switch v := strings.TrimSpace(raw); v {
	case "":
		return "", nil
	case metricSourceCH:
		// Always honoured. See the asymmetry note above.
		return metricSourceCH, nil
	case metricSourceVM:
		if !vmAvailable {
			// 400, not 502: nothing upstream was contacted, and there is
			// no host to blame. The message carries the fix, because the
			// operator typing this param by hand has no other feedback
			// channel.
			return "", fmt.Errorf("%w: %s=vm — VictoriaMetrics yapılandırılmamış (Settings → Metrik backend'i: bir base URL girin)",
				errBadRequest, metricSourceParam)
		}
		return metricSourceVM, nil
	default:
		// Echo the value back CLAMPED. A typo is the common case and
		// seeing it is most of the diagnosis; an unbounded echo of
		// attacker-controlled input into a shared error path is not
		// something to hand out even when the JSON encoder escapes it.
		return "", fmt.Errorf("%w: %s=%q geçersiz — beklenen %s",
			errBadRequest, metricSourceParam, clampParamValue(raw, 32),
			strings.Join(quoteEach(metricSourceParamValues), " veya "))
	}
}

// clampParamValue truncates by RUNE, not byte: cutting a byte slice mid
// rune yields a replacement char in the JSON body and, worse, makes the
// echoed value differ from what the operator typed for reasons they
// cannot see.
func clampParamValue(v string, max int) string {
	r := []rune(v)
	if len(r) <= max {
		return v
	}
	return string(r[:max]) + "…"
}

func quoteEach(vals []string) []string {
	out := make([]string, len(vals))
	for i, v := range vals {
		out[i] = `"` + v + `"`
	}
	return out
}

// metricSourceFor resolves the backend for one HTTP request: the
// ?metricsrc= override when present, the Settings default otherwise.
//
// Handlers MUST use this rather than metricSource() — and must use its
// return value for the CACHE KEY as well as the read. The key already
// carries src=<name>; because the name comes from the same value the read
// goes to, a trial request can never land on the default backend's cached
// body (v0.5.187 class).
func (s *Server) metricSourceFor(r *http.Request) (metricSource, error) {
	want, err := resolveMetricSourceParam(r.URL.Query().Get(metricSourceParam), s.vmetrics.Available())
	if err != nil {
		return nil, err
	}
	switch want {
	case metricSourceVM:
		return newVMMetricSource(s.vmetrics, s.store), nil
	case metricSourceCH:
		return chMetricSource{s.store}, nil
	default:
		return s.metricSource(), nil
	}
}
