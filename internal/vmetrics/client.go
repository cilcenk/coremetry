// Package vmetrics implements a read backend for an external
// VictoriaMetrics deployment over its Prometheus-compatible HTTP API
// (v0.9.1150, Faz 1).
//
// Why: operators who already run VictoriaMetrics as their metrics store
// do not want a second copy of the same series inside Coremetry's
// ClickHouse. When this backend is enabled, the metric DISCOVERY and
// QUERY surfaces (catalogue + picker, Explore, dashboard metric panels,
// MCP query_metric, label values, attribute keys) read from VM instead.
//
// The query dialect is MetricsQL (a PromQL superset) — see promql.go for
// the two VictoriaMetrics-specific behaviours the translation relies on.
//
// Scope discipline — Faz 1 deliberately leaves in ClickHouse:
//
//   - everything SPAN-derived (services, operations, topology, traces,
//     exceptions, DB/messaging surfaces). Those are not metrics.
//   - fixed-name INTERNAL readers (hosts, infra, JVM panels, db
//     capacity). They are wired to specific metric names + CH columns
//     and each needs its own translation; a partial rewrite would make
//     some panels read VM and others CH on the same page.
//
// Faz 2 (v0.9.1157) closed the last two operator-facing gaps, so the list
// above is now the WHOLE exclusion set:
//
//   - p50/p95/p99 translate to histogram_quantile over the `_bucket`
//     series (promql.go),
//   - GET /api/metrics/histogram builds its heatmap from
//     `sum by (le) (increase(…))` (histogram.go),
//   - GET /api/metrics/promql forwards the operator's query VERBATIM —
//     MetricsQL extensions included, since pre-validating with Coremetry's
//     PromQL-subset parser would reject queries VM runs happily.
//
// There is NO silent fallback to ClickHouse. If the operator enabled VM
// and VM is unreachable, the endpoint fails with VM's error. A fallback
// would answer the operator's question with data from a store they did
// not ask about, and they would have no way to tell — the same honesty
// rule the Tempo fallback banner exists for, applied to a case where a
// banner is not enough because the NUMBERS would differ.
package vmetrics

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/cilcenk/coremetry/internal/chstore"
	"github.com/cilcenk/coremetry/internal/promapi"
)

// Settings is the persisted VictoriaMetrics read-backend config. One
// endpoint per install: vmselect already fans out across a cluster, so
// federation is VM's job, not ours.
type Settings struct {
	Enabled bool   `json:"enabled"`
	BaseURL string `json:"baseUrl"`
	// AuthType — none | bearer. VM itself has no auth; a bearer token
	// covers the vmauth / ingress-with-JWT deployments operators actually
	// run. Basic auth is absent on purpose (no operator asked, and an
	// unused credential path is a liability).
	AuthType string `json:"authType,omitempty"`
	// Token holds the bearer token. Never echoed in Snapshot() — the UI
	// sees HasToken so the operator can tell one is configured.
	Token string `json:"token,omitempty"`
	// InsecureSkipVerify disables TLS chain verification. Named to match
	// the four sibling settings blobs (tempo / thanos / devops /
	// logstore) so the frontend form and the audit details read the same
	// across all of them.
	InsecureSkipVerify bool `json:"insecureSkipVerify,omitempty"`
}

// Snapshot is what GET /api/settings/victoria-metrics returns: Settings
// with the token replaced by a presence bit.
type Snapshot struct {
	Enabled            bool   `json:"enabled"`
	BaseURL            string `json:"baseUrl"`
	AuthType           string `json:"authType,omitempty"`
	HasToken           bool   `json:"hasToken"`
	InsecureSkipVerify bool   `json:"insecureSkipVerify,omitempty"`
}

// Service is the per-process VM client. Config is swapped under an
// RWMutex so an admin PUT takes effect without a restart, and every
// method is nil-receiver-safe.
type Service struct {
	mu  sync.RWMutex
	cfg Settings
}

func New() *Service { return &Service{} }

// settingsStore is the narrow chstore interface this package persists
// through — keeps vmetrics off the concrete *chstore.Store for config
// (it still needs the package for the shared read-model types) and
// mirrors tempo.settingsStore.
type settingsStore interface {
	GetVMetricsSettingsRaw(ctx context.Context) ([]byte, error)
	PutVMetricsSettingsRaw(ctx context.Context, raw []byte) error
}

// LoadPersisted hydrates the in-memory config from system_settings.
// Missing blob = empty config (Configured() false → every read stays on
// ClickHouse). Called once at boot from main().
func (s *Service) LoadPersisted(ctx context.Context, store settingsStore) error {
	if s == nil || store == nil {
		return nil
	}
	raw, err := store.GetVMetricsSettingsRaw(ctx)
	if err != nil {
		return err
	}
	if len(raw) == 0 {
		return nil
	}
	var cfg Settings
	if err := json.Unmarshal(raw, &cfg); err != nil {
		return fmt.Errorf("vmetrics decode: %w", err)
	}
	s.Configure(cfg)
	return nil
}

// StartConfigRefresh keeps the in-memory config in sync with the shared
// persisted blob across pods (tempo/thanos precedent). interval ≤ 0 →
// 30s. The Redis config:victoria-metrics publish makes the common case
// sub-50ms; this poll is the backstop when Redis is absent.
func (s *Service) StartConfigRefresh(ctx context.Context, store settingsStore, interval time.Duration) {
	if s == nil || store == nil {
		return
	}
	if interval <= 0 {
		interval = 30 * time.Second
	}
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			if err := s.LoadPersisted(ctx, store); err != nil {
				log.Printf("[vmetrics] config refresh: %v", err)
			}
		}
	}
}

// SavePersisted writes the typed config to system_settings and swaps the
// live one. The handler merges the stored token in first so a partial
// update cannot blank it.
func (s *Service) SavePersisted(ctx context.Context, store settingsStore, cfg Settings) error {
	if s == nil || store == nil {
		return nil
	}
	raw, err := json.Marshal(cfg)
	if err != nil {
		return err
	}
	if err := store.PutVMetricsSettingsRaw(ctx, raw); err != nil {
		return err
	}
	s.Configure(cfg)
	return nil
}

// Configure swaps the live config.
func (s *Service) Configure(cfg Settings) {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.cfg = cfg
}

// Snapshot returns the public config view (no token).
func (s *Service) Snapshot() Snapshot {
	if s == nil {
		return Snapshot{}
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	return Snapshot{
		Enabled:            s.cfg.Enabled,
		BaseURL:            s.cfg.BaseURL,
		AuthType:           s.cfg.AuthType,
		HasToken:           s.cfg.Token != "",
		InsecureSkipVerify: s.cfg.InsecureSkipVerify,
	}
}

// CurrentSettings returns the full config INCLUDING the token — only for
// the handler's stored-token round-trip. Never write its return value to
// a response.
func (s *Service) CurrentSettings() Settings {
	if s == nil {
		return Settings{}
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.cfg
}

// Configured reports whether VM should serve the metric read surfaces BY
// DEFAULT. This is the predicate the API's source selector reads for a
// request that expresses no preference — enabled with an empty URL is not
// configured, so a half-filled form cannot route reads at a backend that
// cannot answer.
func (s *Service) Configured() bool {
	if s == nil {
		return false
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.cfg.Enabled && strings.TrimSpace(s.cfg.BaseURL) != ""
}

// Available reports whether VM CAN be read at all — a base URL exists,
// regardless of the Enabled toggle (v0.9.1151, deneme modu).
//
// The two predicates answer DIFFERENT questions and the split is the
// whole feature:
//
//	Configured() → "VM is the default for every metric read" (the
//	               Settings toggle the operator flips for the install)
//	Available()  → "a single ?metricsrc=vm request can reach VM" (the
//	               per-request trial gate)
//
// Trial mode exists because metric NAMES differ between the two backends
// (VM sanitises dots to underscores). An operator has to see one real
// chart from VM before committing the whole install to it, and flipping
// the global toggle to find out would move every panel, picker and
// dashboard of every user at once.
func (s *Service) Available() bool {
	if s == nil {
		return false
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	return strings.TrimSpace(s.cfg.BaseURL) != ""
}

// request builds a promapi.Request from the live config.
func (s *Service) request(path string, params url.Values, cfg Settings) promapi.Request {
	return promapi.Request{
		Label:    "victoriametrics",
		BaseURL:  cfg.BaseURL,
		Path:     path,
		Params:   params,
		AuthType: cfg.AuthType,
		Token:    cfg.Token,
		SkipTLS:  cfg.InsecureSkipVerify,
	}
}

// ready returns the config for a read, or an error when VM cannot be
// reached at all. Callers get here only after the API's source selector
// already chose VM, so a failure means the config changed under the
// request — an error, not a fallback.
//
// v0.9.1151 — the Enabled check moved OUT of this predicate. Enabled now
// means exactly one thing, "VM is the DEFAULT backend", and that decision
// belongs to the API's selector (metricsource.go), which is also the only
// place that can see the per-request ?metricsrc= override. Keeping the
// check here too would have made trial mode fail with "not configured"
// while the operator was staring at a filled-in base URL — the routing
// authority and the reachability predicate would have disagreed. What
// this function guards is the thing it can actually answer: is there a
// URL to call.
func (s *Service) ready() (Settings, error) {
	if s == nil {
		return Settings{}, fmt.Errorf("victoriametrics backend not available")
	}
	cfg := s.CurrentSettings()
	if strings.TrimSpace(cfg.BaseURL) == "" {
		return Settings{}, fmt.Errorf("victoriametrics backend is not configured")
	}
	return cfg, nil
}

// promTime formats a time for VM's start/end params (unix seconds, with
// fractional precision as the API allows).
func promTime(t time.Time) string {
	return strconv.FormatFloat(float64(t.UnixNano())/1e9, 'f', 3, 64)
}

// nameLookback bounds metric discovery. Mirrors chstore.metricNameLookback
// (7d) so switching backends does not silently change WHICH metrics are
// considered current — the catalogue would appear to gain or lose rows on
// a toggle that was supposed to change only the source.
const nameLookback = 7 * 24 * time.Hour

// ── Read surfaces (signature-identical to the chstore methods) ─────────
//
// The method NAMES and SIGNATURES deliberately match *chstore.Store's.
// The API seam (internal/api/metricsource.go) is an interface both must
// satisfy, so any future signature drift on either side is a COMPILE
// error rather than a runtime divergence between the two backends.

// ListMetricNames answers the catalogue + MetricNamePicker.
//
// VM reports metric NAMES and nothing else: /api/v1/label/__name__/values
// has no description, unit, instrument type, or last-seen. Those fields
// come back ZERO, which the frontend renders as "—". That is the honest
// shape — VM genuinely does not know (its /api/v1/metadata is a
// Prometheus-only surface VM does not populate) — and the source badge on
// the catalogue tells the operator which backend answered, so an empty
// Unit column reads as "VM doesn't report it" rather than "the metric is
// broken".
//
// Pattern matching and paging are client-side (pageNames): VM's label-
// values endpoint offers neither. The response is bounded by the metric
// NAME cardinality, which is small even on huge installs — thousands of
// names, not millions of series.
func (s *Service) ListMetricNames(ctx context.Context, service, pattern string, limit, offset int) ([]chstore.MetricInfo, int, error) {
	cfg, err := s.ready()
	if err != nil {
		return nil, 0, err
	}
	unlimited := limit == 0 && offset == 0 && pattern == ""
	now := time.Now()
	params := url.Values{
		"start": {promTime(now.Add(-nameLookback))},
		"end":   {promTime(now)},
	}
	if svc := strings.TrimSpace(service); svc != "" {
		params.Add("match[]", "{"+serviceLabel()+"="+quotePromString(svc)+"}")
	}
	all, err := promapi.QueryStrings(ctx, s.request("/api/v1/label/__name__/values", params, cfg))
	if err != nil {
		return nil, 0, err
	}
	sort.Strings(all)
	names, total := pageNames(all, pattern, limit, offset, unlimited)
	out := make([]chstore.MetricInfo, 0, len(names))
	for _, n := range names {
		out = append(out, chstore.MetricInfo{Name: n})
	}
	return out, total, nil
}

// QueryMetric runs the multi-series time-bucketed query behind Explore,
// the dashboard "metric" panels and MCP query_metric.
//
// Signature-identical to chstore.Store's — the seam invariant. The NOTE
// variant below carries the extra half; this wrapper drops it so callers
// that cannot render a note (MCP, the evaluator) are unaffected.
func (s *Service) QueryMetric(ctx context.Context, f chstore.MetricQueryFilter) ([]chstore.SpanMetricSeries, error) {
	out, _, err := s.QueryMetricNoted(ctx, f)
	return out, err
}

// QueryMetricNoted is QueryMetric plus an OPERATOR-FACING NOTE explaining an
// empty result (v0.9.1157, Faz 2).
//
// It is a separate method rather than a wider QueryMetric because
// QueryMetric's signature is load-bearing: the API seam is an interface both
// this and *chstore.Store satisfy, and matching them is what turns future
// drift into a compile error. So the note rides an OPTIONAL capability the
// HTTP layer type-asserts for (internal/api's metricNoteSource) and the
// ClickHouse source simply does not implement — it has nothing to explain,
// because its bucket layout is in the row rather than in a guessed name.
//
// The note fires for exactly one case: a PERCENTILE that returned zero
// series. Scoped that tightly on purpose. An empty gauge query is honestly
// empty and we know nothing the operator does not; a percentile queried
// `<name>_bucket`, a series they never typed and cannot see, so "no data",
// "not a histogram" and "your write path names buckets differently" all
// render as one blank chart with three different fixes.
//
// The window is normalized HERE the same way the CH path normalizes it
// (zero To → now, zero From → 24h back) so an unbounded call cannot
// become an unbounded VM query.
func (s *Service) QueryMetricNoted(ctx context.Context, f chstore.MetricQueryFilter) ([]chstore.SpanMetricSeries, string, error) {
	cfg, err := s.ready()
	if err != nil {
		return nil, "", err
	}
	now := time.Now()
	if f.To.IsZero() {
		f.To = now
	}
	if f.From.IsZero() {
		f.From = f.To.Add(-24 * time.Hour)
	}
	q, err := buildPromQL(f)
	if err != nil {
		return nil, "", err
	}
	step := promStep(f.From, f.To, f.StepSeconds, f.MaxDataPoints)
	params := url.Values{
		"query": {q},
		"start": {promTime(f.From)},
		"end":   {promTime(f.To)},
		"step":  {strconv.Itoa(step) + "s"},
	}
	series, err := promapi.QuerySeries(ctx, s.request("/api/v1/query_range", params, cfg))
	if err != nil {
		return nil, "", err
	}
	out := make([]chstore.SpanMetricSeries, 0, len(series))
	for _, sr := range series {
		row := chstore.SpanMetricSeries{GroupKey: seriesGroupKey(f.GroupBy, sr.Metric)}
		for _, raw := range sr.Values {
			ts, v, ok := promapi.Sample(raw)
			if !ok {
				// Non-finite or malformed: skip the POINT, keep the series.
				// A gap renders as a gap; a 0 renders as a measurement.
				continue
			}
			row.Points = append(row.Points, chstore.SpanMetricPoint{
				// SpanMetricPoint.Time is unix NANOS (bucket start).
				Time:  int64(ts * 1e9),
				Value: v,
			})
		}
		out = append(out, row)
	}
	// The note is attached only on the empty PERCENTILE outcome — see the
	// header. Everything else returns "" and the envelope carries no note
	// field at all.
	if len(out) == 0 {
		if _, isPercentile := promPercentile(f.Aggregation); isPercentile {
			return out, emptyBucketNote(f.Name), nil
		}
	}
	return out, "", nil
}

// MetricLabelValues answers the filter-value suggestion list.
// Capped at the same 200 the CH sibling uses so the picker behaves
// identically on both backends.
func (s *Service) MetricLabelValues(ctx context.Context, metric, key string, since time.Duration) ([]string, error) {
	if metric == "" || key == "" {
		return nil, nil
	}
	cfg, err := s.ready()
	if err != nil {
		return nil, err
	}
	label := promLabel(key)
	if label == "" {
		return nil, nil
	}
	now := time.Now()
	params := url.Values{
		"start":   {promTime(now.Add(-since))},
		"end":     {promTime(now)},
		"match[]": {"{__name__=" + quotePromString(metric) + "}"},
	}
	vals, err := promapi.QueryStrings(ctx, s.request("/api/v1/label/"+url.PathEscape(label)+"/values", params, cfg))
	if err != nil {
		return nil, err
	}
	sort.Strings(vals)
	if len(vals) > 200 {
		vals = vals[:200]
	}
	return vals, nil
}

// MetricAttrKeys answers "what can I write inside {}?".
//
// __name__ is dropped: it is not an attribute key, and the CH sibling
// (which reads the attr_keys array) never returns it. Leaving it in would
// offer the operator a filter key that duplicates the metric selector.
func (s *Service) MetricAttrKeys(ctx context.Context, metric, service string, since time.Duration) ([]string, error) {
	if metric == "" {
		return nil, nil
	}
	cfg, err := s.ready()
	if err != nil {
		return nil, err
	}
	matchers := []string{"__name__=" + quotePromString(metric)}
	if svc := strings.TrimSpace(service); svc != "" {
		matchers = append(matchers, serviceLabel()+"="+quotePromString(svc))
	}
	now := time.Now()
	params := url.Values{
		"start":   {promTime(now.Add(-since))},
		"end":     {promTime(now)},
		"match[]": {"{" + strings.Join(matchers, ", ") + "}"},
	}
	keys, err := promapi.QueryStrings(ctx, s.request("/api/v1/labels", params, cfg))
	if err != nil {
		return nil, err
	}
	out := make([]string, 0, len(keys))
	for _, k := range keys {
		if k == "" || k == "__name__" {
			continue
		}
		out = append(out, k)
	}
	sort.Strings(out)
	if len(out) > 100 {
		out = out[:100]
	}
	return out, nil
}

// Test probes a CANDIDATE config without saving or swapping it — the
// Settings tab's "Test" button. `up` is the query because it is the one
// series name every Prometheus-shaped store has an opinion about; an
// empty result is still a successful ANSWER (VM is reachable and
// speaking the API), so only transport / auth / envelope failures fail
// the probe.
func (s *Service) Test(ctx context.Context, cfg Settings) error {
	if strings.TrimSpace(cfg.BaseURL) == "" {
		return fmt.Errorf("base URL required")
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	req := s.request("/api/v1/query", url.Values{"query": {"up"}}, cfg)
	if _, err := promapi.QuerySeries(ctx, req); err != nil {
		return err
	}
	return nil
}
