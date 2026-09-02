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
	"errors"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/cilcenk/coremetry/internal/chstore"
	"github.com/cilcenk/coremetry/internal/promapi"
	"github.com/cilcenk/coremetry/internal/secretref"
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
	// TokenRef — v0.10.273: `env:NAME` | `file:/path` (internal/secretref);
	// doluysa saklı Token'a TERCİH edilir. Çözüm Configure'da (boot / PUT /
	// yenileme), istekte IO yok; Test probe'u gönderilen ref'i anında çözer.
	TokenRef string `json:"tokenRef,omitempty"`
	// InsecureSkipVerify disables TLS chain verification. Named to match
	// the four sibling settings blobs (tempo / thanos / devops /
	// logstore) so the frontend form and the audit details read the same
	// across all of them.
	InsecureSkipVerify bool `json:"insecureSkipVerify,omitempty"`
	// RateWindowFloorS overrides the 300s rate/last lookbehind floor
	// (v0.9.1164). 0 = unset, use promLookbehindFloorSec; otherwise
	// [10, 3600] — validated on PUT, re-checked on read
	// (resolveRateWindowFloor) so a hand-edited blob cannot emit a window
	// nobody chose. The floor never reaches increase() or the heatmap; see
	// promRollupWindow.
	RateWindowFloorS int `json:"rateWindowFloorS,omitempty"`
	// WriteURL / WriteEnabled — v0.10.292 (VM tek metrik deposu, Dilim 1a):
	// OTLP metrik ingest'inin HAM gövdesi VM'e de yazılır
	// (POST <WriteURL>/opentelemetry/v1/metrics, protobuf+gzip). WriteURL boş
	// → BaseURL (vmsingle'da okuma ve yazma aynı host; vmagent/vminsert
	// ayrıysa ayrı URL). WriteEnabled kapalı = bugünkü davranış; çift yazım
	// (Aşama 1) yalnız bayrakla açılır. Okuma tarafındaki Enabled'dan
	// BAĞIMSIZ: yazımı açıp okumayı CH'de bırakmak kıyas dönemidir.
	WriteURL     string `json:"writeUrl,omitempty"`
	WriteEnabled bool   `json:"writeEnabled,omitempty"`
	// AllowUnfilteredPercentiles lifts the bucket-scan guard
	// (v0.9.1164). The DEFAULT — false, the zero value — is the PROTECTED
	// state, which is the direction that matters: a fresh install, a
	// missing blob and a partially-written blob all land on "guarded", so
	// the protection can only be removed by an explicit admin decision
	// that lands in audit_log. See guardBucketScan for the decision table.
	AllowUnfilteredPercentiles bool `json:"allowUnfilteredPercentiles,omitempty"`
}

// Snapshot is what GET /api/settings/victoria-metrics returns: Settings
// with the token replaced by a presence bit.
type Snapshot struct {
	Enabled            bool   `json:"enabled"`
	BaseURL            string `json:"baseUrl"`
	AuthType           string `json:"authType,omitempty"`
	HasToken           bool   `json:"hasToken"`
	InsecureSkipVerify bool   `json:"insecureSkipVerify,omitempty"`
	// v0.10.273 — referans görünür (secret değil); çözüm durumu rozet.
	TokenRef      string `json:"tokenRef,omitempty"`
	TokenResolved bool   `json:"tokenResolved"`
	TokenError    string `json:"tokenError,omitempty"`
	// Both v0.9.1164 knobs round-trip in full: neither is secret-adjacent,
	// and the form has to be able to show the operator the floor they set
	// (an input that cannot read its own stored value re-submits a blank on
	// every unrelated save — the aiTuning failure class).
	RateWindowFloorS           int  `json:"rateWindowFloorS,omitempty"`
	AllowUnfilteredPercentiles bool `json:"allowUnfilteredPercentiles,omitempty"`
	// v0.10.292 — çift yazım alanları (secret değil, tam tur).
	WriteURL     string `json:"writeUrl,omitempty"`
	WriteEnabled bool   `json:"writeEnabled"`
}

// Service is the per-process VM client. Config is swapped under an
// RWMutex so an admin PUT takes effect without a restart, and every
// method is nil-receiver-safe.
type Service struct {
	mu  sync.RWMutex
	cfg Settings
	// v0.10.273 — tokenRef çözümü (Configure'da); testler getenv/readFile
	// enjekte eder (influx/tempo/thanos deseni).
	resolvedToken string
	resolveErr    string
	getenv        func(string) string
	readFile      func(string) ([]byte, error)
	// writeHTTP — v0.10.292 — WriteOTLP'nin istemcisi; Configure'da ayara
	// göre (TLS) kurulur, istek başına transport açılmaz.
	writeHTTP *http.Client
}

func New() *Service { return &Service{getenv: os.Getenv, readFile: os.ReadFile} }

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
	// v0.10.273 — düz token blob'a referans kılığında GİRMEZ.
	if cfg.TokenRef != "" && !secretref.Valid(cfg.TokenRef) {
		return errors.New(secretref.InvalidMessage)
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
	s.writeHTTP = newWriteClient(cfg) // v0.10.292
	// v0.10.273 — referans burada çözülür (boot / PUT / yenileme).
	s.resolvedToken, s.resolveErr = "", ""
	if cfg.TokenRef != "" {
		if v, err := secretref.ResolveWith(cfg.TokenRef, s.env(), s.files()); err != nil {
			s.resolveErr = err.Error()
		} else {
			s.resolvedToken = v
		}
	}
}

func (s *Service) env() func(string) string {
	if s.getenv == nil {
		return os.Getenv
	}
	return s.getenv
}

func (s *Service) files() func(string) ([]byte, error) {
	if s.readFile == nil {
		return os.ReadFile
	}
	return s.readFile
}

// effectiveToken — v0.10.273: ref doluysa çözülmüş değer (çözülemediyse
// BOŞ — saklı token'a sessizce düşülmez), yoksa saklı düz token. SAF.
func effectiveToken(cfg Settings, resolved string) string {
	if cfg.TokenRef != "" {
		return resolved
	}
	return cfg.Token
}

// tokenFor — istek yolundaki token. cfg canlı yapılandırmaysa Configure'da
// çözülmüş değer (IO yok); gönderilmiş bir form ise (Test probe'u, henüz
// kaydedilmemiş ref) referans o an çözülür — probe bir IO'dur zaten.
func (s *Service) tokenFor(cfg Settings) string {
	if cfg.TokenRef == "" {
		return cfg.Token
	}
	s.mu.RLock()
	live := s.cfg.TokenRef == cfg.TokenRef
	resolved := s.resolvedToken
	s.mu.RUnlock()
	if live {
		return resolved
	}
	v, err := secretref.ResolveWith(cfg.TokenRef, s.env(), s.files())
	if err != nil {
		return ""
	}
	return v
}

// Snapshot returns the public config view (no token).
func (s *Service) Snapshot() Snapshot {
	if s == nil {
		return Snapshot{}
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	return Snapshot{
		Enabled:                    s.cfg.Enabled,
		BaseURL:                    s.cfg.BaseURL,
		AuthType:                   s.cfg.AuthType,
		HasToken:                   s.cfg.Token != "",
		InsecureSkipVerify:         s.cfg.InsecureSkipVerify,
		TokenRef:                   s.cfg.TokenRef,
		TokenResolved:              s.cfg.TokenRef != "" && s.resolvedToken != "",
		TokenError:                 s.resolveErr,
		RateWindowFloorS:           s.cfg.RateWindowFloorS,
		AllowUnfilteredPercentiles: s.cfg.AllowUnfilteredPercentiles,
		WriteURL:                   s.cfg.WriteURL,
		WriteEnabled:               s.cfg.WriteEnabled,
	}
}

// promOptions lifts the query-shaping knobs out of a config SNAPSHOT
// (v0.9.1164).
//
// It takes the cfg the caller already got from ready() rather than reading
// s.cfg again, and that is the whole reason it is a free function on Settings'
// shape instead of a method that re-locks. A second read could observe a
// different config than the one the rest of the query was built from — an
// admin PUT landing between the two would produce an expression whose window
// and whose guard decision came from different configurations. One snapshot in,
// one set of options out.
func promOptions(cfg Settings) promOpts {
	return promOpts{
		RateWindowFloorS:           cfg.RateWindowFloorS,
		AllowUnfilteredPercentiles: cfg.AllowUnfilteredPercentiles,
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

// ResolvedRateWindowFloor — cache anahtarı için ÇÖZÜLMÜŞ taban
// (v0.9.1165). Persisted değer değil: 0 ve 300 aynı davranıştır, aynı
// tag'i üretmeli ki gereksiz cache kaçırması olmasın. Neden anahtarda:
// taban sorgunun EMİTTED penceresini değiştirir ama istek bayt-aynıdır —
// ayar PUT'undan sonraki TTL boyunca eski tabanın gövdesi servis edilir
// (v0.5.187 sınıfının ayar-girdili hâli; 1164 canlı probu tam bunu
// "kablo kopuk" olarak gösterdi — kopuk olan kablo değil ANAHTARDI).
func (s *Service) ResolvedRateWindowFloor() int {
	if s == nil {
		return resolveRateWindowFloor(0)
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	return resolveRateWindowFloor(s.cfg.RateWindowFloorS)
}

// request builds a promapi.Request from the live config.
func (s *Service) request(path string, params url.Values, cfg Settings) promapi.Request {
	return promapi.Request{
		Label:    "victoriametrics",
		BaseURL:  cfg.BaseURL,
		Path:     path,
		Params:   params,
		AuthType: cfg.AuthType,
		Token:    s.tokenFor(cfg), // v0.10.273 — ref > saklı token
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
		// v0.9.1180 — birim ve tip ADIN İÇİNDEN. Prometheus dünyasında
		// sözleşme budur ve VM'in kataloğu başka bir yerde taşımıyor; boş
		// bırakmak paneli birimsiz ("0.25" — saniye mi ms mi?) ve
		// şablonsuz bırakıyordu.
		unit, typ := describeMetricName(n)
		out = append(out, chstore.MetricInfo{Name: n, Unit: unit, Type: typ})
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
	f = normalizeQueryWindow(f)
	q, err := buildPromQL(f, promOptions(cfg))
	if err != nil {
		return nil, "", err
	}
	out, err := s.runRangeQuery(ctx, cfg, q, f)
	if err != nil {
		return nil, "", err
	}
	// The note is attached only on the empty PERCENTILE outcome — see the
	// header. Everything else returns "" and the envelope carries no note
	// field at all.
	if len(out) == 0 {
		// Recomputed from the same pure function buildPromQL used, so a note
		// cannot name a spelling the query did not try (the promStep precedent:
		// pure + same inputs, therefore incapable of drifting).
		cands := nameCandidates(f.Name, f.Aggregation)
		if _, isPercentile := promPercentile(f.Aggregation); isPercentile {
			return out, emptyBucketNote(cands), nil
		}
		// v0.9.1160 — every OTHER aggregation earns a note too, on ONE
		// condition: the translation guessed more than one spelling. The live
		// check of v0.9.1159 called the silent empty out by name, and the
		// aggregations it hits hardest are the ones with no histogram arm
		// (min/max/sum/count/last), where an OTLP histogram is empty BY DESIGN
		// and only the note can say so.
		//
		// `len(cands) > 1` is the whole gate, and it is the honest line: with a
		// single candidate nothing was guessed, the operator asked for exactly
		// one series by name, and an empty answer means the series was empty.
		// A note there would be noise on every quiet gauge — which is why
		// v0.9.1159 scoped notes to percentiles in the first place.
		if len(cands) > 1 {
			return out, emptyNameNote(cands), nil
		}
	}
	return out, "", nil
}

// MetricLabelValues answers the filter-value suggestion list.
// Capped at the same 200 the CH sibling uses so the picker behaves
// identically on both backends.
//
// v0.9.1159 — the `match[]` scope is a candidate ALTERNATION
// (discoveryNameCandidates). Label discovery had the same spelling bug as the
// query path, plus one that bites harder: a histogram metric has no base
// series in VM, so the suggestion list came back empty for exactly the metric
// the operator was trying to filter, and an empty picker asserts "no such
// values" rather than "wrong name".
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
		"match[]": {"{" + nameMatcher(discoveryNameCandidates(metric)) + "}"},
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
//
// v0.9.1159 — same candidate alternation as MetricLabelValues, and the reason
// discoveryNameCandidates stands the histogram family up on `_count` rather
// than `_bucket` lands HERE: `le` is a label on the bucket series, so a
// `_bucket` scope would hand the operator the histogram's own internal
// dimension as if it were one of their attributes.
func (s *Service) MetricAttrKeys(ctx context.Context, metric, service string, since time.Duration) ([]string, error) {
	if metric == "" {
		return nil, nil
	}
	// v0.9.1268 — the fetch moved to labelNames so MetricPresentKeys can
	// intersect against the UNCAPPED set. The 100 cap stays HERE because it is
	// a picker bound, not a fact about the metric.
	out, err := s.labelNames(ctx, metric, service, since)
	if err != nil {
		return nil, err
	}
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
	// v0.10.292 — yazım açıksa yazma yolu da yoklanır: BOŞ bir
	// ExportMetricsServiceRequest (0 bayt protobuf) — veri yazmaz, uç +
	// kimlik + TLS'i ispatlar. Okuma geçip yazma düşerse operatör "Test
	// yeşil ama VM boş" tuzağına düşmez.
	if cfg.WriteEnabled {
		if err := writeOTLPWith(ctx, newWriteClient(cfg), cfg, s.tokenFor(cfg), nil, false); err != nil {
			return fmt.Errorf("write probe: %w", err)
		}
	}
	return nil
}
