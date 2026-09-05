package api

// v0.9.1150 — VictoriaMetrics read backend, Faz 1.
//
// Three gates live here, and all three protect properties that would fail
// SILENTLY:
//
//  1. SOURCE PIN — the metric handlers must go through s.metricSource().
//     A new handler that calls s.store directly would read ClickHouse
//     while every other metric surface reads VM, and the page would look
//     fine.
//  2. CACHE-KEY BACKEND MARKER — every metric cache key carries the
//     backend. Without it, flipping the Settings toggle serves the old
//     store's bodies for a full TTL and two pods refreshing at different
//     moments disagree (v0.5.187 class, with an INPUT that is a setting).
//  3. SELECTOR — Configured() alone decides, and a half-filled form must
//     stay on ClickHouse.

import (
	"context"
	"errors"
	"fmt"
	"net/http/httptest"
	"os"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/cilcenk/coremetry/internal/chstore"
	"github.com/cilcenk/coremetry/internal/vmetrics"
)

// Metric store methods that MUST NOT be called directly from api.go.
//
// Scoped to api.go on purpose. Widening the scan to the package would have
// to exempt the files that read the store for non-metric reasons, and an
// exemption list is the thing that quietly grows. api.go is where every
// operator-facing metric handler lives, which makes it the precise boundary.
//
// ONE FILE WAS ON THAT EXEMPTION LIST AND IS NOT ANY MORE, and the reason it
// left is worth keeping. service_metric_throughput.go was listed here as
// "stays on ClickHouse by DESIGN — the fixed-name throughput probe". That was
// written in v0.9.1150, when VictoriaMetrics was a second opinion rather than
// the only place a metric lives. Once VM became a primary backend the same
// sentence stopped describing a SCOPE and started describing a BUG: the
// service Overview's throughput panel searched ClickHouse on a VM install,
// found nothing, and said so honestly (operator-reported, v0.9.1268).
//
// It now routes through the seam and has its OWN gate —
// TestThroughputMapperReadsThroughTheSourceSeam in
// service_metric_throughput_test.go — because its method set is wider than the
// six below. dql.go remains genuinely exempt: its evaluator is CH-side query
// machinery, not a metric read that a backend could answer differently.
// v0.9.1157 — QueryMetricHistogram joined the list when the heatmap joined
// the seam. Adding the METHOD to this slice is the half that keeps the gate
// honest: without it, a future handler could reintroduce the direct call and
// the only signal would be a heatmap reading ClickHouse on a VM install.
var metricStoreMethods = []string{
	"QueryMetric",
	"QueryMetricHistogram",
	"ListMetricNames",
	"GetMetricNames",
	"MetricLabelValues",
	"MetricAttrKeys",
}

func TestMetricHandlersGoThroughTheSourceSeam(t *testing.T) {
	raw, err := os.ReadFile("api.go")
	if err != nil {
		t.Fatal(err)
	}
	src := stripGoComments(string(raw))

	// Guard against a vacuous pass: if the comment stripper over-ate (a
	// `//` line containing `/*` can make the naive block regex swallow
	// real code — the reason this assertion exists), the scan below would
	// find nothing and report success.
	for _, marker := range []string{
		"func (s *Server) getMetricNames",
		"func (s *Server) queryMetric",
		"func (s *Server) getMetricLabelValues",
		"func (s *Server) getMetricAttrKeys",
		"func (s *Server) getMetricHistogram", // v0.9.1157
	} {
		if !strings.Contains(src, marker) {
			t.Fatalf("comment stripping ate real code — %q is missing, so this scan proves nothing", marker)
		}
	}

	for _, m := range metricStoreMethods {
		re := regexp.MustCompile(`s\.store\.` + m + `\(`)
		if loc := re.FindStringIndex(src); loc != nil {
			t.Errorf("api.go calls s.store.%s directly at offset %d — metric reads must go "+
				"through s.metricSource() (metricsource.go), or the VictoriaMetrics backend "+
				"silently does not apply to this surface", m, loc[0])
		}
	}
}

// Every metric cache key must name the backend. The five keys are matched
// by their literal prefixes so a renamed key fails loudly rather than
// dropping out of the scan.
func TestMetricCacheKeysCarryTheBackendMarker(t *testing.T) {
	raw, err := os.ReadFile("api.go")
	if err != nil {
		t.Fatal(err)
	}
	src := stripGoComments(string(raw))

	// v0.9.1159 — four of these gained a `%s` between the prefix and `:src=`:
	// metricNameRuleTag, the VM-only candidate-rule stamp. The prefixes are
	// UPDATED rather than shortened to `metric-query:` — dropping the `src=%s`
	// tail would leave the gate matching a key with no backend marker at all,
	// which is the whole failure it exists to catch. The lesson is the
	// recurring one: when an atom gains a second spelling, widen the gate's
	// scope, never relax its assertion.
	keyPrefixes := []string{
		"metric-names:v3:src=",      // legacy bare-array shape
		"metric-names:v3:src=%s",    // {names,total,hasMore} envelope
		"metric-query:v4%s:src=%s",  // Explore / dashboards / MQE
		"metric-labels%s:src=%s",    // filter value suggestions
		"metric-attr-keys%s:src=%s", // filter key suggestions
		"metric-hist%s:src=%s",      // v0.9.1157 — the heatmap
	}
	for _, p := range keyPrefixes {
		if !strings.Contains(src, p) {
			t.Errorf("no metric cache key with prefix %q found in api.go — a metric key without "+
				"the backend marker cross-poisons across a Settings toggle (v0.5.187)", p)
		}
	}

	// And the inverse: no metric key may exist WITHOUT src=. Catches a new
	// key added by copy-pasting a pre-v0.9.1150 line.
	badKey := regexp.MustCompile(`"metric-(names|query|labels|attr-keys|hist)[^"]*"`)
	for _, m := range badKey.FindAllString(src, -1) {
		if !strings.Contains(m, "src=") {
			t.Errorf("metric cache key %s has no backend marker", m)
		}
	}

	// v0.9.1159 — the four keys whose ANSWER depends on the VM metric-name
	// candidate rules must carry the stamp, and it must be applied through the
	// shared helper. A key that spells its own `:n1` inline would drift the
	// day the rules change again; a key that forgets it entirely serves the
	// pre-fix EMPTY body for a full TTL right after the deploy that fixed it.
	for _, tagged := range []string{
		"metric-query:v4%s", "metric-hist%s", "metric-labels%s", "metric-attr-keys%s",
	} {
		if !strings.Contains(src, tagged) {
			t.Errorf("cache key %q lost its metricNameRuleTag slot — a warm pre-v0.9.1159 "+
				"entry would keep serving the empty body the candidate rules exist to fix", tagged)
		}
	}
	if n := strings.Count(src, "metricNameRuleTag(src)"); n != 4 {
		t.Errorf("metricNameRuleTag applied at %d call sites in api.go, want 4 "+
			"(query, heatmap, label values, attr keys)", n)
	}

	// The seventh key lives in promql.go. Same rule, separate file — see
	// TestMetricHandlersResolveSourcePerRequest for why the scans stay
	// file-scoped rather than package-wide.
	praw, err := os.ReadFile("promql.go")
	if err != nil {
		t.Fatal(err)
	}
	psrc := stripGoComments(string(praw))
	const promKeyPrefix = `"promql:src=%s:q=`
	if !strings.Contains(psrc, promKeyPrefix) {
		t.Errorf("no %s cache key in promql.go — /api/metrics/promql answers the "+
			"SAME body shape from two engines, so without the marker a ?metricsrc=vm trial is "+
			"served ClickHouse numbers for a full TTL (v0.5.187 with a query param as the input)",
			promKeyPrefix)
	}
}

// v0.9.1157 — the /api/metrics/query envelope grew a `note` field, so the key
// moved to v4. The version digit is pinned SEPARATELY from the src= marker
// above because the two protect different failures and only one of them is
// visible: src= stops a trial reading the default backend's body, while the
// version digit stops a ROLLING DEPLOY serving an old pod's note-less body —
// which renders as the silent empty chart the note exists to prevent
// (v0.9.443/458 lesson).
func TestMetricQueryKeyVersionMatchesTheEnvelope(t *testing.T) {
	raw, err := os.ReadFile("api.go")
	if err != nil {
		t.Fatal(err)
	}
	src := stripGoComments(string(raw))
	if strings.Contains(src, "metric-query:v3:") {
		t.Error("metric-query key is still v3 while the envelope carries `note` — a rolling " +
			"deploy would serve note-less bodies for a full TTL")
	}
	if !strings.Contains(src, `out["note"] = note`) {
		t.Fatal("queryMetric no longer attaches the note — if the field was removed on purpose, " +
			"drop this test WITH it; leaving the key at v4 and the field gone is the drift this catches")
	}
}

// The selector is the whole routing decision. Note the "enabled but no
// URL" row: without it a half-filled form would route every metric
// surface at a backend that cannot answer, and there is no fallback.
func TestMetricSourceSelector(t *testing.T) {
	tests := []struct {
		name string
		cfg  *vmetrics.Settings // nil = no service wired at all
		want string
	}{
		{name: "no vmetrics service wired", cfg: nil, want: metricSourceCH},
		{name: "service wired but unconfigured", cfg: &vmetrics.Settings{}, want: metricSourceCH},
		{name: "disabled with a url", cfg: &vmetrics.Settings{BaseURL: "http://vm:8428"}, want: metricSourceCH},
		{name: "enabled without a url", cfg: &vmetrics.Settings{Enabled: true}, want: metricSourceCH},
		{name: "enabled with a url", cfg: &vmetrics.Settings{Enabled: true, BaseURL: "http://vm:8428"}, want: metricSourceVM},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			s := &Server{}
			if tc.cfg != nil {
				s.vmetrics = vmetrics.New()
				s.vmetrics.Configure(*tc.cfg)
			}
			if got := s.metricSource().Name(); got != tc.want {
				t.Fatalf("metricSource() = %q, want %q", got, tc.want)
			}
		})
	}
}

// A VM failure is 502 with VM's own text, not 500. Counting it as 500
// would both blame Coremetry in the UI and inflate coremetry-api's
// self-observed error rate into a false anomaly (v0.7.13).
func TestUpstreamErrorIs502(t *testing.T) {
	rec := httptest.NewRecorder()
	writeErr(rec, upstream(errors.New("dial tcp 10.0.0.5:8428: connect: connection refused")))
	if rec.Code != 502 {
		t.Fatalf("status = %d, want 502", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "connection refused") {
		t.Fatalf("VM's own error text was swallowed: %s", rec.Body.String())
	}

	// A plain error stays 500 — the classification must not leak.
	rec = httptest.NewRecorder()
	writeErr(rec, errors.New("some clickhouse problem"))
	if rec.Code != 500 {
		t.Fatalf("status = %d, want 500", rec.Code)
	}
}

// A query we cannot TRANSLATE is 400, not 502. 502 would tell the operator
// their VictoriaMetrics is broken and send them to check a healthy
// cluster — a wrong diagnosis is worse than a blunt one.
func TestUntranslatableQueryIs400Not502(t *testing.T) {
	v := vmMetricSource{svc: vmetrics.New()}
	v.svc.Configure(vmetrics.Settings{Enabled: true, BaseURL: "http://vm:8428"})

	// agg=p90 is refused by the translator BEFORE any HTTP happens, so this
	// needs no live VM.
	//
	// THIS TEST'S SUBJECT HAS NOW GONE STALE TWICE, and the pattern is worth
	// naming because the next release will do it again:
	//
	//	v0.9.1154 — the case was agg=last, which Faz 1.5 taught the
	//	  translator. v0.9.1157 — it was agg=p99, which Faz 2 taught it.
	//
	// Neither staleness showed up as a failed ASSERTION about aggregations.
	// The translated query simply reached HTTP, failed to dial vm:8428 and
	// came back tagged errUpstream — so the errUpstream guard below is the
	// thing that caught both, and it is the assertion to keep no matter which
	// aggregation this test names.
	//
	// p90 is the durable choice rather than another real aggregation:
	// Coremetry's builder cannot produce it and the ClickHouse sibling does
	// not implement it, so there is no roadmap on which it starts
	// translating. A refusal test should name something that is refused on
	// PURPOSE, not something merely unimplemented yet.
	_, err := v.QueryMetric(context.Background(), chstore.MetricQueryFilter{
		Name: "http.server.request.duration", Aggregation: "p90",
	})
	if err == nil {
		t.Fatal("want a refusal for an unsupported aggregation")
	}
	if errors.Is(err, errUpstream) {
		t.Fatal("a translation refusal must NOT be tagged errUpstream — it would 502 and " +
			"blame a healthy VictoriaMetrics")
	}
	if !errors.Is(err, errBadRequest) {
		t.Fatalf("refusal not tagged errBadRequest: %v", err)
	}

	rec := httptest.NewRecorder()
	writeErr(rec, err)
	if rec.Code != 400 {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
	// The message must name the supported set so the operator can fix it
	// without reading the source. The percentiles are IN that set as of
	// v0.9.1157, which makes this list the operator-visible proof that Faz 2
	// landed — a translation that worked but kept advertising percentiles as
	// unsupported would send them back to ClickHouse for no reason.
	body := rec.Body.String()
	for _, want := range []string{
		"avg", "sum", "min", "max", "count", "last", "rate", "increase",
		"p50", "p95", "p99",
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("error body does not mention %q: %s", want, body)
		}
	}
	// And the inverse: the old "Faz 2" pointer must be GONE. Left in place it
	// would tell an operator whose percentile now works that the feature is
	// still coming.
	if strings.Contains(body, "Faz 2") {
		t.Fatalf("refusal still defers percentiles to Faz 2, which shipped in v0.9.1157: %s", body)
	}

	// A filter operator with no MetricsQL matcher takes the same path.
	_, err = v.QueryMetric(context.Background(), chstore.MetricQueryFilter{
		Name:    "m",
		Filters: []chstore.FilterExpr{{Key: "n", Op: ">", Values: []string{"5"}}},
	})
	if !errors.Is(err, errBadRequest) {
		t.Fatalf("filter refusal not tagged errBadRequest: %v", err)
	}
}

func TestUpstreamPreservesNilAndWraps(t *testing.T) {
	if upstream(nil) != nil {
		t.Fatal("upstream(nil) must stay nil — a nil error wrapped into a non-nil one 502s a successful read")
	}
	inner := errors.New("boom")
	got := upstream(inner)
	if !errors.Is(got, errUpstream) {
		t.Fatal("wrapped error must match errUpstream")
	}
	if !errors.Is(got, inner) {
		t.Fatal("wrapped error must still match the inner cause")
	}
}

// vmMetricSource must tag EVERY method's error. An untagged one 500s and
// reads as a Coremetry bug.
func TestVMSourceTagsEveryError(t *testing.T) {
	// Unconfigured service → every read returns its own "not configured"
	// error, which is the cheapest way to exercise all four paths without
	// a live VM.
	v := vmMetricSource{svc: vmetrics.New()}
	ctx := context.Background()

	_, _, errNames := v.ListMetricNames(ctx, "", "", 10, 0)
	_, errQuery := v.QueryMetric(ctx, chstore.MetricQueryFilter{Name: "m"})
	_, errLabels := v.MetricLabelValues(ctx, "m", "pod", time.Hour)
	_, errKeys := v.MetricAttrKeys(ctx, "m", "", time.Hour)

	for name, err := range map[string]error{
		"ListMetricNames":   errNames,
		"QueryMetric":       errQuery,
		"MetricLabelValues": errLabels,
		"MetricAttrKeys":    errKeys,
	} {
		if err == nil {
			t.Fatalf("%s: want an error from an unconfigured backend, got nil", name)
		}
		if !errors.Is(err, errUpstream) {
			t.Errorf("%s: error is not tagged errUpstream (%v) — it would surface as a 500", name, err)
		}
	}
}

// Compile-time proof that both adapters satisfy the seam. If chstore or
// vmetrics drifts on a signature this line fails to build, which is the
// entire reason the two method sets were made identical.
var _ = []metricSource{chMetricSource{}, vmMetricSource{}}

// v0.9.1159 — the candidate-rule stamp is VM-ONLY, and both halves matter.
//
// Present on VM: the metric-name candidates changed WHICH series a byte-
// identical request resolves to, so a warm entry from before the deploy keeps
// answering empty for its whole TTL — 30s on the query/heatmap keys, 60s on
// the two discovery keys — which is precisely the symptom the release fixes.
//
// Absent on CH: this release cannot change a ClickHouse answer, and the two
// discovery keys exist to keep a DISTINCT metric_points scan off the hot path
// (v0.8.456). Stamping them would buy nothing and charge a scan wave to a prod
// ClickHouse.
func TestMetricNameRuleTagIsVMOnly(t *testing.T) {
	// v0.9.1160 — n1 → n2: rate/increase/avg gained their `or` histogram arms.
	// v0.9.1165 — tag artık ÇÖZÜLMÜŞ rate tabanını da taşır (rwf=N): 1164'ün
	// canlı probu taban 60↔600 arasında bayt-aynı gövde gösterdi — kopuk olan
	// kablo değil anahtardı; taban emitted pencereyi değiştirir ama istek
	// bayt-aynıdır, PUT sonrası TTL boyunca eski tabanın gövdesi servis
	// ediliyordu. vmetrics servisi bağlı değilken çözülmüş taban = 300.
	srv := &Server{}
	if got := srv.metricNameRuleTag(vmMetricSource{}); got != ":n2:rwf=300" {
		t.Fatalf("VM source tag = %q, want %q", got, ":n2:rwf=300")
	}
	if got := srv.metricNameRuleTag(chMetricSource{}); got != "" {
		t.Fatalf("CH source tag = %q, want empty — a ClickHouse key must stay byte-identical", got)
	}
	// nil receiver: a partially wired Server must not panic inside a cache-key
	// format string, and "no tag" is the CH-shaped, zero-cost direction.
	if got := srv.metricNameRuleTag(nil); got != "" {
		t.Fatalf("nil source tag = %q, want empty", got)
	}
	// The tag must be a KEY SEGMENT, not free text: it lands between a prefix
	// and `:src=`, so a value without the leading separator would fuse into the
	// prefix and make `metric-hist` collide with `metric-histn1`-shaped keys
	// from a future rule bump.
	if !strings.HasPrefix(srv.metricNameRuleTag(vmMetricSource{}), ":") {
		t.Fatal("the tag must open with ':' or it fuses into the key prefix")
	}
	// AYIRT EDİCİ vaka — 1164 probunun yakaladığı senaryo: taban değişince
	// anahtar da değişmeli ki PUT sonrası sorgu eski gövdeye düşmesin.
	vm := vmetrics.New()
	vm.Configure(vmetrics.Settings{BaseURL: "http://vm:8428", RateWindowFloorS: 600})
	srv2 := &Server{vmetrics: vm}
	if got := srv2.metricNameRuleTag(vmMetricSource{}); got != ":n2:rwf=600" {
		t.Fatalf("taban 600 iken tag = %q, want %q", got, ":n2:rwf=600")
	}
}

func TestSourceNamesAreStable(t *testing.T) {
	// These strings ride in cache keys AND in the /api/metrics/names
	// response the frontend badge reads. Changing one silently invalidates
	// every cached key and un-badges the catalogue.
	if metricSourceCH != "ch" || metricSourceVM != "vm" {
		t.Fatalf("source names changed: %q / %q", metricSourceCH, metricSourceVM)
	}
	if got := fmt.Sprintf("%s|%s", chMetricSource{}.Name(), vmMetricSource{}.Name()); got != "ch|vm" {
		t.Fatalf("adapter names = %q", got)
	}
}

// v0.9.1268 — chMetricSource DELEGASYON PARİTESİ.
//
// Bu sürüm ClickHouse kurulumunda HİÇBİR ŞEYİ değiştirmemeli: throughput
// eşleyicisinin s.store.X(...) çağrıları c.store.X(...) delegasyonlarına
// dönüştü, ve dönüşümün doğru olduğunun tek kanıtı argümanların AYNI SIRADA
// geçmesi.
//
// NEDEN DERLEYİCİ YETMİYOR: bu metotların çoğu iki string alıyor —
// MetricUnit(ctx, metric, service), MetricInstrument(ctx, name, service),
// ListMetricNames(ctx, service, pattern, …). İkisini yer değiştirmek SESSİZ:
// derlenir, koşar, ve "cart servisinin http.server.duration metriği" yerine
// "http.server.duration servisinin cart metriği" sorar — her zaman boş, hiçbir
// zaman hatalı. Tam olarak bu sürümün düzelttiği semptomun bir başka üreteci.
//
// Kapı gövdeyi TEK SATIRLIK delegasyon olarak çiviliyor. Bir metot mantık
// kazanırsa (dönüşüm, varsayılan, ikinci çağrı) burası kırmızı yanar — ve
// yanmalı: chMetricSource'un sözleşmesi "saf delegasyon", davranış farkı
// varsa vmMetricSource tarafında olmalı.
func TestCHSourceDelegationIsArgumentIdentical(t *testing.T) {
	raw, err := os.ReadFile("metricsource.go")
	if err != nil {
		t.Fatal(err)
	}
	src := stripGoComments(string(raw))

	// Vacuous-pass guard — yorum soyucu kodu yediyse tarama hiçbir şey bulmaz
	// ve BAŞARI raporlar.
	if !strings.Contains(src, "type chMetricSource struct") {
		t.Fatal("yorum soyucu gerçek kodu yedi — chMetricSource tanımı yok, bu tarama hiçbir şey kanıtlamıyor")
	}

	// Her satır: metodun gövdesinde AYNEN bulunması gereken delegasyon.
	// Argüman adları ve sıraları imzadan birebir kopya.
	for _, want := range []string{
		"return c.store.MetricExists(ctx, name)",
		"return c.store.MetricInstrument(ctx, name, service)",
		"return c.store.QueryMetricRate(ctx, f, mode)",
		"return c.store.QueryMetricCountRate(ctx, f, mode)",
		"return c.store.MetricUnit(ctx, metric, service)",
		"return c.store.MetricPresentKeys(ctx, metric, keys, since)",
		// Faz 1/2'den beri var olanlar da listede: bu sürüm onlara
		// dokunmadı, ve dokunulmadığının kanıtı da burada dursun.
		"return c.store.QueryMetric(ctx, f)",
		"return c.store.QueryMetricHistogram(ctx, f)",
		"return c.store.MetricLabelValues(ctx, metric, key, since)",
		"return c.store.MetricAttrKeys(ctx, metric, service, since)",
		"return c.store.ListMetricNames(ctx, service, pattern, limit, offset)",
	} {
		if !strings.Contains(src, want) {
			t.Errorf("chMetricSource delegasyonu bulunamadı ya da argümanları değişti:\n\t%s\n"+
				"Aynı tipli iki argümanı yer değiştirmek DERLENİR ve sessizce boş sonuç verir.", want)
		}
	}

	// ServiceIdentityLabels ClickHouse tarafında chstore listesinin KENDİSİ
	// olmalı — kopyalanmış bir liste, iki tarafın sessizce ayrışacağı yerdir.
	if !strings.Contains(src, "func (chMetricSource) ServiceIdentityLabels() []string { return chstore.ServiceIdentityLabels }") {
		t.Error("chMetricSource.ServiceIdentityLabels chstore listesini AYNEN döndürmüyor — " +
			"kopya bir liste, CH davranışının bu sürümde değişmediği iddiasını çürütür")
	}
}

// v0.9.1268 — VM'siz kurulumda seçici ClickHouse'a düşmeli, yani bu sürüm
// VictoriaMetrics'i olmayan bir kurulumda GÖRÜNMEZ olmalı.
//
// Yerel smoke'un kanıtlayabildiği tek şey bu ve dürüst olmak gerekirse
// kanıtlaması gereken de bu: bu makinede VictoriaMetrics yok, VM yolu
// httptest ile test ediliyor (internal/vmetrics/throughput_test.go).
func TestThroughputFallsBackToClickHouseWithoutVM(t *testing.T) {
	// Hiç vmetrics servisi bağlı değil — Available() nil alıcıda false.
	s := &Server{}
	got, err := s.metricSourceFor(httptest.NewRequest("GET",
		"/api/services/checkout/metric-throughput?from=1&to=2", nil))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got.Name() != metricSourceCH {
		t.Fatalf("VM'siz kurulumda kaynak %q, beklenen %q — bu sürüm VM'i olmayan bir "+
			"kurulumda davranış DEĞİŞTİRMEMELİ", got.Name(), metricSourceCH)
	}
	// Ve kimlik adayları ClickHouse'un listesi olmalı: VM yazımları
	// (service_name, k8s_deployment_name) CH'de sessizce hiç eşleşmez.
	labels := identityLabelCandidates("", got)
	if strings.Join(labels, ",") != strings.Join(chstore.ServiceIdentityLabels, ",") {
		t.Fatalf("CH fallback'inde aday listesi değişti: %v", labels)
	}
}
