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
//   - fixed-name internal readers (hosts, infra, JVM panels, db capacity,
//     the throughput probe in service_metric_throughput.go, the DQL
//     evaluator in dql.go) each hard-code metric names and CH-side column
//     behaviour. Routing them piecemeal would put two backends behind one
//     page;
//   - histograms + the PromQL proxy are Faz 2.
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

// vmMetricSource wraps the external VictoriaMetrics reader. The method
// set is delegation-only because vmetrics.Service already carries the
// chstore-identical signatures — that is the point of matching them.
type vmMetricSource struct{ svc *vmetrics.Service }

func (vmMetricSource) Name() string { return metricSourceVM }

// upstream classifies a VM error for the HTTP layer. One helper rather
// than four inline wraps: a new method added without the tag would 500
// and read as a Coremetry bug.
//
// Two outcomes, and picking the right one is the operator's diagnosis:
//   - vmetrics.ErrUnsupported → 400. VM is fine; the QUERY does not
//     translate (aggregation / filter operator / instance scoping).
//   - anything else → 502. Transport, auth, or VM's own query error.
func upstream(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, vmetrics.ErrUnsupported) {
		return fmt.Errorf("%w: %w", errBadRequest, err)
	}
	return fmt.Errorf("%w: %w", errUpstream, err)
}

func (v vmMetricSource) ListMetricNames(ctx context.Context, service, pattern string, limit, offset int) ([]chstore.MetricInfo, int, error) {
	out, total, err := v.svc.ListMetricNames(ctx, service, pattern, limit, offset)
	return out, total, upstream(err)
}

func (v vmMetricSource) QueryMetric(ctx context.Context, f chstore.MetricQueryFilter) ([]chstore.SpanMetricSeries, error) {
	out, err := v.svc.QueryMetric(ctx, f)
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
		return vmMetricSource{s.vmetrics}
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
		return vmMetricSource{s.vmetrics}, nil
	case metricSourceCH:
		return chMetricSource{s.store}, nil
	default:
		return s.metricSource(), nil
	}
}
