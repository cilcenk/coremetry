package api

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/cilcenk/coremetry/internal/chstore"
)

// endpoints_metric_test.go — v0.10.336: /endpoints "Kaynak: metrik".
//
// Handler canlı depo ister; karar mantığı (plan, indirgeme, sıralama,
// not) saf ve sahte kaynakla çivilenir. Sahte kaynak metricSource
// arayüzünü gömer: yalnız kullanılan yöntemler doldurulur, kullanılmayan
// bir yönteme dokunulursa nil-panic testi kırar — sessiz geçiş yok.

type fakeEPSource struct {
	metricSource
	name    string
	unit    string
	keys    []string
	envOK   bool
	rateFn  func(f chstore.MetricQueryFilter, mode string) ([]chstore.SpanMetricSeries, error)
	queryFn func(f chstore.MetricQueryFilter) ([]chstore.SpanMetricSeries, error)
	calls   []string
	filters [][]chstore.FilterExpr
}

func (f *fakeEPSource) Name() string                      { return f.name }
func (f *fakeEPSource) LatencyMetricName(n string) string { return strings.TrimSuffix(n, "_count") }
func (f *fakeEPSource) MetricPresentKeys(context.Context, string, []string, time.Duration) []string {
	return f.keys
}
func (f *fakeEPSource) MetricUnit(context.Context, string, string) string { return f.unit }
func (f *fakeEPSource) EnvFilterExpr(env string) (chstore.FilterExpr, bool) {
	if f.envOK {
		return chstore.FilterExpr{Key: "deployment.environment", Op: "=", Values: []string{env}}, true
	}
	return chstore.FilterExpr{}, false
}
func (f *fakeEPSource) QueryMetricRate(_ context.Context, q chstore.MetricQueryFilter, mode string) ([]chstore.SpanMetricSeries, error) {
	f.calls = append(f.calls, "rate:"+mode)
	f.filters = append(f.filters, q.Filters)
	return f.rateFn(q, mode)
}
func (f *fakeEPSource) QueryMetricCountRate(_ context.Context, q chstore.MetricQueryFilter, mode string) ([]chstore.SpanMetricSeries, error) {
	f.calls = append(f.calls, "countrate:"+mode)
	f.filters = append(f.filters, q.Filters)
	return f.rateFn(q, mode)
}
func (f *fakeEPSource) QueryMetric(_ context.Context, q chstore.MetricQueryFilter) ([]chstore.SpanMetricSeries, error) {
	f.calls = append(f.calls, "query:"+q.Aggregation+":"+q.Name)
	return f.queryFn(q)
}

func epFixtureWindow() (time.Time, time.Time) {
	to := time.Date(2026, 9, 4, 12, 0, 0, 0, time.UTC)
	return to.Add(-time.Hour), to
}

// epConst — her adımda sabit değerli seri (VM gibi pencere SONUNA damgalı).
// epConst — n nokta, kova BAŞLANGICI damgalı (from + i·step, i = 0..n−1).
// v0.10.504 (dış denetim A6): kaynak artık kova başlangıcını döner
// (vmetrics bucketStartNs); eski `i = 1..n` şekli VM'in kova-sonu
// damgasını taklit ediyordu ve reduce'taki `off--` telafisine yaslanıyordu.
func epConst(gk []string, from time.Time, step, n int, v float64) chstore.SpanMetricSeries {
	s := chstore.SpanMetricSeries{GroupKey: gk}
	for i := 0; i < n; i++ {
		s.Points = append(s.Points, chstore.SpanMetricPoint{Time: from.Add(time.Duration(i*step) * time.Second).UnixNano(), Value: v})
	}
	return s
}

func hasStatusFilter(fs []chstore.FilterExpr) bool {
	for _, f := range fs {
		if strings.Contains(f.Key, "status_code") {
			return true
		}
	}
	return false
}

func epFixtureSource(unit string, keys []string) *fakeEPSource {
	from, _ := epFixtureWindow()
	step := 60
	src := &fakeEPSource{name: "vm", unit: unit, keys: keys}
	src.rateFn = func(f chstore.MetricQueryFilter, mode string) ([]chstore.SpanMetricSeries, error) {
		if hasStatusFilter(f.Filters) {
			return []chstore.SpanMetricSeries{
				epConst([]string{"/orders/8421"}, from, step, 60, 1), // 60 hata
			}, nil
		}
		return []chstore.SpanMetricSeries{
			epConst([]string{"/orders/8421"}, from, step, 60, 10), // 600 çağrı
			epConst([]string{"/orders/77"}, from, step, 60, 5),    // 300
			epConst([]string{"/health"}, from, step, 60, 1),       // 60
			epConst([]string{""}, from, step, 60, 99),             // rotasız: atlanır
		}, nil
	}
	src.queryFn = func(f chstore.MetricQueryFilter) ([]chstore.SpanMetricSeries, error) {
		switch f.Aggregation {
		case "avg":
			return []chstore.SpanMetricSeries{epConst([]string{"/orders/8421"}, from, step, 60, 0.2)}, nil
		case "p50":
			return []chstore.SpanMetricSeries{epConst([]string{"/orders/8421"}, from, step, 60, 0.1)}, nil
		case "p95":
			return []chstore.SpanMetricSeries{epConst([]string{"/orders/8421"}, from, step, 60, 0.5)}, nil
		case "p99":
			// ilk yarı 1.0, ikinci yarı 0.0 — eşit ağırlık → 0.5
			s := epConst([]string{"/orders/8421"}, from, step, 60, 1.0)
			for i := 30; i < 60; i++ {
				s.Points[i].Value = 0
			}
			return []chstore.SpanMetricSeries{s}, nil
		}
		return nil, nil
	}
	return src
}

func epPlan(from, to time.Time) endpointsMetricPlan {
	return endpointsMetricPlan{From: from, To: to, Service: "cart", Limit: 100, StepSec: endpointsMetricStep(from, to)}
}

func epRow(rows []chstore.EndpointRow, path string) *chstore.EndpointRow {
	for i := range rows {
		if rows[i].Path == path {
			return &rows[i]
		}
	}
	return nil
}

func TestEndpointsMetricStep(t *testing.T) {
	from, to := epFixtureWindow()
	if s := endpointsMetricStep(from, to); s != 60 {
		t.Fatalf("1h → 60 s adım bekleniyordu, %d", s)
	}
	if s := endpointsMetricStep(from, from.Add(10*time.Minute)); s != 60 {
		t.Fatalf("10 dk → taban 60 s, %d", s)
	}
	if s := endpointsMetricStep(from, from.Add(24*time.Hour)); s != 1440 {
		t.Fatalf("24h → 1440 s (60 adım), %d", s)
	}
}

func TestBuildEndpointsMetric_Reduce(t *testing.T) {
	from, to := epFixtureWindow()
	src := epFixtureSource("s", []string{"http.response.status_code"})
	id := endpointsMetricIdentity{Metric: "http_server_request_duration_seconds_count", RTMetric: "http_server_request_duration_seconds", Instrument: "histogram", Service: "cart", MatchedBy: "service_name"}
	resp, err := buildEndpointsMetric(context.Background(), src, id, epPlan(from, to))
	if err != nil {
		t.Fatal(err)
	}
	if len(resp.Rows) != 3 {
		t.Fatalf("3 satır (rotasız seri atlanır), %d: %+v", len(resp.Rows), resp.Rows)
	}
	r := epRow(resp.Rows, "/orders/8421")
	if r == nil || r.Service != "cart" {
		t.Fatalf("satır yok / servis yanlış: %+v", r)
	}
	if r.Calls != 600 || r.Errors != 60 || r.Http5xx != 60 {
		t.Fatalf("çağrı/hata toplamı: %+v", r)
	}
	if r.ErrorRate < 9.99 || r.ErrorRate > 10.01 {
		t.Fatalf("errorRate yüzde olmalı (10): %v", r.ErrorRate)
	}
	// birim s → ms: avg 0.2 s = 200 ms; p95 500; p99 ağırlıklı 0.5 s = 500
	if r.AvgMs != 200 || r.P50Ms != 100 || r.P95Ms != 500 || r.P99Ms != 500 {
		t.Fatalf("süreler ms'e çevrilmeli: avg=%v p50=%v p95=%v p99=%v", r.AvgMs, r.P50Ms, r.P95Ms, r.P99Ms)
	}
	if r.ReqPerMin != 10 {
		t.Fatalf("600 çağrı / 60 dk = 10: %v", r.ReqPerMin)
	}
	if len(r.Sparkline) != 60 || len(r.ErrorsSparkline) != 60 || len(r.P99Sparkline) != 60 {
		t.Fatalf("sparkline 60 slot: %d/%d/%d", len(r.Sparkline), len(r.ErrorsSparkline), len(r.P99Sparkline))
	}
	if r.Sparkline[0] != 10 || r.P99Sparkline[0] != 1000 || r.P99Sparkline[59] != 0 {
		t.Fatalf("slot değerleri: %v %v", r.Sparkline[0], r.P99Sparkline[0])
	}
	if r.SlowTraceID != "" || r.ErrorTraceID != "" {
		t.Fatalf("metrikte exemplar olamaz: %+v", r)
	}
	// hatasız route: 0 hata, oran 0
	if h := epRow(resp.Rows, "/health"); h == nil || h.Errors != 0 || h.ErrorRate != 0 || h.Calls != 60 {
		t.Fatalf("/health: %+v", h)
	}
	// v0.10.362 — iki aşama: sıralama (kaba adım, çağrı) + ayrıntı (çağrı,
	// hata, 4 değer) yalnız ilk N route için. histogram → count rate; increase.
	want := []string{"countrate:increase", "countrate:increase", "countrate:increase",
		"query:avg:http_server_request_duration_seconds", "query:p50:http_server_request_duration_seconds",
		"query:p95:http_server_request_duration_seconds", "query:p99:http_server_request_duration_seconds"}
	if strings.Join(src.calls, ",") != strings.Join(want, ",") {
		t.Fatalf("sorgu dizisi: %v", src.calls)
	}
	// 1. aşama daraltmasız; 2. aşama route regex'i taşır; hata sorgusu 5xx + regex
	if len(src.filters[0]) != 0 {
		t.Fatalf("sıralama sorgusu çip taşımaz: %+v", src.filters[0])
	}
	if fs := src.filters[1]; len(fs) != 1 || fs[0].Key != "http.route" || fs[0].Op != "=~" || !strings.Contains(fs[0].Values[0], "/orders/8421") || !strings.Contains(fs[0].Values[0], "/health") {
		t.Fatalf("ayrıntı route daraltması: %+v", fs)
	}
	if fs := src.filters[2]; len(fs) != 2 || fs[1].Key != "http.response.status_code" || fs[1].Op != "=~" || fs[1].Values[0] != "^5[0-9][0-9]$" {
		t.Fatalf("hata filtresi: %+v", fs)
	}
	if resp.Pool != 3 || resp.PoolCapped {
		t.Fatalf("havuz: %d capped=%v", resp.Pool, resp.PoolCapped)
	}
	if resp.ErrorsUnknown || resp.StatusKey != "http.response.status_code" || !resp.LatencyUnitKnown || resp.LatencyUnit != "s" {
		t.Fatalf("zarf: %+v", resp)
	}
	if resp.Source != "metric" || resp.Backend != "vm" || !resp.MetricExists {
		t.Fatalf("zarf kaynak: %+v", resp)
	}
}

// CH kova BAŞINA damgalar: i=0..59 → slot i, kayma yok. v0.10.504 (A6) —
// VM kaynağı da artık kova başlangıcını döner (vmetrics bucketStartNs), bu
// yüzden iki kaynak AYNI fixture'la aynı ızgarayı üretmeli: buradaki eski
// "−60 s kaydır" sarmalayıcısı (VM'in kova-sonu taklidini CH'ye çeviren)
// kalktı; test yalnız kaynak adının reduce'u DEĞİŞTİRMEDİĞİNİ pinler.
func TestBuildEndpointsMetric_StartStampedCH(t *testing.T) {
	from, to := epFixtureWindow()
	src := epFixtureSource("s", []string{"http.response.status_code"})
	src.name = "ch"
	id := endpointsMetricIdentity{Metric: "m", RTMetric: "m", Instrument: "histogram", Service: "cart"}
	resp, err := buildEndpointsMetric(context.Background(), src, id, epPlan(from, to))
	if err != nil {
		t.Fatal(err)
	}
	r := epRow(resp.Rows, "/orders/8421")
	if r.Calls != 600 || r.P99Ms != 500 || r.Sparkline[0] != 10 || r.Sparkline[59] != 10 || r.P99Sparkline[0] != 1000 || r.P99Sparkline[59] != 0 {
		t.Fatalf("CH başa damgalı ızgara: calls=%d p99=%v spark0=%v spark59=%v p99s0=%v p99s59=%v", r.Calls, r.P99Ms, r.Sparkline[0], r.Sparkline[59], r.P99Sparkline[0], r.P99Sparkline[59])
	}
}

func TestBuildEndpointsMetric_SignatureSearchSort(t *testing.T) {
	from, to := epFixtureWindow()
	src := epFixtureSource("s", []string{"http.status_code"})
	id := endpointsMetricIdentity{Metric: "m_count", RTMetric: "m", Instrument: "histogram", Service: "cart"}
	p := epPlan(from, to)
	p.BySignature = true
	resp, err := buildEndpointsMetric(context.Background(), src, id, p)
	if err != nil {
		t.Fatal(err)
	}
	r := epRow(resp.Rows, "/orders/:id")
	if r == nil || len(resp.Rows) != 2 {
		t.Fatalf("shape: /orders/8421 + /orders/77 → /orders/:id: %+v", resp.Rows)
	}
	if r.Calls != 900 || r.Errors != 60 {
		t.Fatalf("birleşik toplam: %+v", r)
	}
	if r.ErrorRate < 6.6 || r.ErrorRate > 6.7 {
		t.Fatalf("60/900: %v", r.ErrorRate)
	}
	// ağırlıklı avg: yalnız 8421 için avg var (600 ağırlık) → 200 ms
	if r.AvgMs != 200 {
		t.Fatalf("ağırlıklı avg: %v", r.AvgMs)
	}
	if resp.StatusKey != "http.status_code" {
		t.Fatalf("eski yazım da kabul: %q", resp.StatusKey)
	}

	p.BySignature = false
	p.Search = "HEALTH"
	resp, _ = buildEndpointsMetric(context.Background(), src, id, p)
	if len(resp.Rows) != 1 || resp.Rows[0].Path != "/health" {
		t.Fatalf("arama harf-duyarsız alt-dize: %+v", resp.Rows)
	}

	p.Search = ""
	resp, _ = buildEndpointsMetric(context.Background(), src, id, p)
	endpointsMetricSort(resp.Rows, "errorRate", "desc")
	if resp.Rows[0].Path != "/orders/8421" {
		t.Fatalf("errorRate desc: %+v", resp.Rows)
	}
	endpointsMetricSort(resp.Rows, "calls", "asc")
	if resp.Rows[0].Path != "/health" || resp.Rows[2].Path != "/orders/8421" {
		t.Fatalf("calls asc: %+v", resp.Rows)
	}
	endpointsMetricSort(resp.Rows, "path", "asc")
	if resp.Rows[0].Path != "/health" {
		t.Fatalf("path asc: %+v", resp.Rows)
	}
	endpointsMetricSort(resp.Rows, "bogus", "")
	if resp.Rows[0].Path != "/orders/8421" {
		t.Fatalf("bilinmeyen → calls desc: %+v", resp.Rows)
	}
}

func TestBuildEndpointsMetric_ErrorsUnknownAndUnit(t *testing.T) {
	from, to := epFixtureWindow()
	src := epFixtureSource("", nil) // etiket yok, birim yok
	id := endpointsMetricIdentity{Metric: "http.server.request.duration", RTMetric: "http.server.request.duration", Instrument: "histogram", Service: "cart"}
	resp, err := buildEndpointsMetric(context.Background(), src, id, epPlan(from, to))
	if err != nil {
		t.Fatal(err)
	}
	if !resp.ErrorsUnknown || resp.StatusKey != "" {
		t.Fatalf("durum-kodu etiketi yok → hata bilinmiyor: %+v", resp)
	}
	for _, fs := range src.filters {
		if hasStatusFilter(fs) {
			t.Fatalf("hata sorgusu HİÇ atılmamalı: %+v", src.filters)
		}
	}
	if len(src.calls) != 2+4 { // sıralama + ayrıntı çağrı + 4 değer
		t.Fatalf("sorgu sayısı: %v", src.calls)
	}
	r := epRow(resp.Rows, "/orders/8421")
	if r.Errors != 0 || r.Http5xx != 0 || r.ErrorRate != 0 {
		t.Fatalf("bilinmeyen hata 0 olarak servis edilir ama zarf söyler: %+v", r)
	}
	if resp.LatencyUnitKnown || r.AvgMs != 0.2 {
		t.Fatalf("birim bilinmiyor → ham değer + bayrak: known=%v avg=%v", resp.LatencyUnitKnown, r.AvgMs)
	}
	note := endpointsMetricNote(resp, epPlan(from, to))
	for _, want := range []string{"Hata BİLİNMİYOR", "Süre birimi BİLİNMİYOR", "Exemplar"} {
		if !strings.Contains(note, want) {
			t.Fatalf("not %q içermeli: %s", want, note)
		}
	}

	// Ad sözleşmesi: `_seconds` → s (addan)
	src = epFixtureSource("", []string{"http.response.status_code"})
	id.RTMetric = "http_server_request_duration_seconds"
	resp, _ = buildEndpointsMetric(context.Background(), src, id, epPlan(from, to))
	if !resp.LatencyUnitKnown || resp.LatencyUnitFrom != "name" || resp.LatencyUnit != "s" {
		t.Fatalf("addan birim: %+v", resp)
	}
	if r := epRow(resp.Rows, "/orders/8421"); r.AvgMs != 200 {
		t.Fatalf("addan s → ms: %v", r.AvgMs)
	}
	if note := endpointsMetricNote(resp, epPlan(from, to)); !strings.Contains(note, "metrik adından") || !strings.Contains(note, "5xx") {
		t.Fatalf("not: %s", note)
	}
}

func TestBuildEndpointsMetric_SumInstrumentAllServicesEnvCluster(t *testing.T) {
	from, to := epFixtureWindow()
	src := epFixtureSource("ms", []string{"http.response.status_code"})
	step := 60
	src.rateFn = func(f chstore.MetricQueryFilter, mode string) ([]chstore.SpanMetricSeries, error) {
		if strings.Join(f.GroupBy, ",") != "service.name,http.route" {
			t.Fatalf("servissiz istekte servis de gruplanmalı: %v", f.GroupBy)
		}
		if hasStatusFilter(f.Filters) {
			return nil, nil
		}
		return []chstore.SpanMetricSeries{
			epConst([]string{"cart", "/a"}, from, step, 60, 2),
			epConst([]string{"pay", "/a"}, from, step, 60, 3),
		}, nil
	}
	src.queryFn = func(chstore.MetricQueryFilter) ([]chstore.SpanMetricSeries, error) { return nil, nil }
	id := endpointsMetricIdentity{Metric: "http_requests_total", RTMetric: "http_requests_total", Instrument: "sum", MatchedBy: "service_name"}
	p := endpointsMetricPlan{From: from, To: to, Limit: 100, StepSec: step, Env: "uat", Cluster: "c1"}
	resp, err := buildEndpointsMetric(context.Background(), src, id, p)
	if err != nil {
		t.Fatal(err)
	}
	if len(resp.Rows) != 2 {
		t.Fatalf("iki servis aynı route → iki satır: %+v", resp.Rows)
	}
	if src.calls[0] != "rate:increase" {
		t.Fatalf("sum instrument → QueryMetricRate: %v", src.calls)
	}
	if !resp.EnvAmbiguous || !resp.ClusterIgnored {
		t.Fatalf("env ifade edilemedi + cluster yok sayıldı İLAN edilmeli: %+v", resp)
	}
	note := endpointsMetricNote(resp, p)
	if !strings.Contains(note, "TÜM ortamları") || !strings.Contains(note, "cluster filtresi") {
		t.Fatalf("not: %s", note)
	}
	// env ifade EDİLİYORSA filtre eklenir, bayrak düşer
	src.envOK = true
	src.calls = nil
	src.filters = nil
	resp, _ = buildEndpointsMetric(context.Background(), src, id, p)
	if resp.EnvAmbiguous || len(src.filters[0]) != 1 || src.filters[0][0].Key != "deployment.environment" {
		t.Fatalf("env filtresi: %+v / %+v", resp.EnvAmbiguous, src.filters[0])
	}
}

func TestMergePriorEndpointRows(t *testing.T) {
	rows := []chstore.EndpointRow{{Service: "cart", Path: "/a", P99Ms: 300}, {Service: "cart", Path: "/new", P99Ms: 50}}
	prior := []chstore.EndpointRow{{Service: "cart", Path: "/a", Calls: 10, Errors: 1, AvgMs: 20, P99Ms: 100}}
	mergePriorEndpointRows(rows, prior)
	if rows[0].PriorCalls != 10 || rows[0].PriorErrors != 1 || rows[0].PriorAvgMs != 20 || rows[0].PriorP99Ms != 100 {
		t.Fatalf("prior bindirme: %+v", rows[0])
	}
	if rows[1].PriorCalls != 0 || rows[1].PriorP99Ms != 0 {
		t.Fatalf("prior'da yok → sıfır (UI NEW): %+v", rows[1])
	}
	endpointsMetricSort(rows, "p99Delta", "desc")
	if rows[0].Path != "/a" {
		t.Fatalf("p99Delta 200 > 50: %+v", rows)
	}
}

func TestEndpointPathSignature(t *testing.T) {
	cases := map[string]string{
		"/orders/8421":                              "/orders/:id",
		"/orders/8421/items/77":                     "/orders/:id/items/:id",
		"/u/5b1c2a3d-1111-2222-3333-444455556666/x": "/u/:id/x",
		"/blob/0123456789abcdef0123456789abcdef":    "/blob/:id",
		"/list/accounts":                            "/list/accounts",
		"/v2/list":                                  "/v2/list",
	}
	for in, want := range cases {
		if got := endpointPathSignature(in); got != want {
			t.Errorf("sig(%q) = %q want %q", in, got, want)
		}
	}
}

func TestEndpointsMetricKeyCoversEveryInput(t *testing.T) {
	from, to := epFixtureWindow()
	base := endpointsMetricPlan{From: from, To: to, Service: "cart", Search: "a", Env: "uat", Cluster: "c", Limit: 100, Sort: "calls", Dir: "desc", StepSec: 60}
	k0 := endpointsMetricKey(base, "vm", "0")
	mut := []func(p *endpointsMetricPlan){
		func(p *endpointsMetricPlan) { p.To = p.To.Add(time.Hour) },
		func(p *endpointsMetricPlan) { p.Service = "pay" },
		func(p *endpointsMetricPlan) { p.Search = "b" },
		func(p *endpointsMetricPlan) { p.Env = "prod" },
		func(p *endpointsMetricPlan) { p.Cluster = "d" },
		func(p *endpointsMetricPlan) { p.Limit = 500 },
		func(p *endpointsMetricPlan) { p.Compare = true },
		func(p *endpointsMetricPlan) { p.BySignature = true },
		func(p *endpointsMetricPlan) { p.Sort = "p99Ms" },
		func(p *endpointsMetricPlan) { p.Dir = "asc" },
		func(p *endpointsMetricPlan) { p.StepSec = 120 },
	}
	for i, m := range mut {
		p := base
		m(&p)
		if endpointsMetricKey(p, "vm", "0") == k0 {
			t.Fatalf("girdi %d anahtara girmiyor (v0.5.187 sınıfı)", i)
		}
	}
	if endpointsMetricKey(base, "ch", "0") == k0 || endpointsMetricKey(base, "vm", "1") == k0 {
		t.Fatal("depo adı ve dışlama özeti anahtarda olmalı")
	}
	if !strings.Contains(k0, ":src=vm:") {
		t.Fatalf("depo segmenti: %s", k0)
	}
}

func TestEndpointsMetricRouteIsInTheRegistry(t *testing.T) {
	if _, ok := extraRouteRegistrars["endpoints-metric"]; !ok {
		t.Fatal("endpoints-metric defterde değil (api.go'ya satır eklemeden kayıt)")
	}
	if src := readRepoFile(t, "api.go"); strings.Contains(src, "/api/endpoints/metric") {
		t.Fatal("api.go bu rotayı bilmemeli — defter kaydı yeter")
	}
}

func TestEndpointsMetricNoteWhenMetricMissing(t *testing.T) {
	resp := endpointsMetricResponse{Source: "metric", Backend: "vm", Tried: []string{"a", "b"}}
	note := endpointsMetricNote(resp, endpointsMetricPlan{})
	if !strings.Contains(note, "metrik bulunamadı") || !strings.Contains(note, "denenen: a, b") {
		t.Fatalf("not: %s", note)
	}
}
