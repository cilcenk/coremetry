package api

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/cilcenk/coremetry/internal/chstore"
)

// service_metric_red_test.go — v0.10.337: Overview RED'i metrikten.
// Sahte kaynak endpoints_metric_test.go'daki fakeEPSource (aynı paket).

func redFixtureSource(unit string, keys []string) *fakeEPSource {
	from, _ := epFixtureWindow()
	step := 60
	src := &fakeEPSource{name: "vm", unit: unit, keys: keys}
	src.rateFn = func(f chstore.MetricQueryFilter, mode string) ([]chstore.SpanMetricSeries, error) {
		if mode != "rate" {
			return nil, nil
		}
		if len(f.GroupBy) != 0 {
			return nil, nil // RED grupsuz: tek seri
		}
		if hasStatusFilter(f.Filters) {
			s := epConst(nil, from, step, 60, 0.5) // 0.5 hata/sn
			s.Points = s.Points[:30]               // ikinci yarıda hata yok
			return []chstore.SpanMetricSeries{s}, nil
		}
		return []chstore.SpanMetricSeries{epConst(nil, from, step, 60, 10)}, nil // 10 req/s
	}
	src.queryFn = func(f chstore.MetricQueryFilter) ([]chstore.SpanMetricSeries, error) {
		v := map[string]float64{"p50": 0.1, "p95": 0.5, "p99": 1.0, "avg": 0.2}[f.Aggregation]
		return []chstore.SpanMetricSeries{epConst(nil, from, step, 60, v)}, nil
	}
	return src
}

func TestBuildServiceMetricRED(t *testing.T) {
	from, to := epFixtureWindow()
	src := redFixtureSource("s", []string{"http.response.status_code"})
	id := endpointsMetricIdentity{Metric: "http_server_request_duration_seconds_count", RTMetric: "http_server_request_duration_seconds", Instrument: "histogram", Service: "cart", MatchedBy: "service_name"}
	p := serviceMetricREDPlan{Service: "cart", From: from, To: to, Mdp: 300, RateWin: 180}
	resp, err := buildServiceMetricRED(context.Background(), src, id, p)
	if err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{"rate", "error_rate", "p50", "p95", "p99", "avg"} {
		if len(resp.Series[k]) != 1 || len(resp.Series[k][0].Points) == 0 {
			t.Fatalf("seri %q eksik: %+v", k, resp.Series[k])
		}
	}
	rate := resp.Series["rate"][0].Points
	if rate[0].Value != 10 || resp.StepSeconds != 60 {
		t.Fatalf("rate/adım: %v %d", rate[0].Value, resp.StepSeconds)
	}
	er := resp.Series["error_rate"][0].Points
	if len(er) != 60 || er[0].Value != 5 || er[59].Value != 0 {
		t.Fatalf("error_rate YÜZDE ve hizalı: n=%d ilk=%v son=%v", len(er), er[0].Value, er[59].Value)
	}
	if resp.Series["p99"][0].Points[0].Value != 1000 || resp.Series["p50"][0].Points[0].Value != 100 || resp.Series["avg"][0].Points[0].Value != 200 {
		t.Fatalf("s → ms: p99=%v p50=%v avg=%v", resp.Series["p99"][0].Points[0].Value, resp.Series["p50"][0].Points[0].Value, resp.Series["avg"][0].Points[0].Value)
	}
	if resp.ErrorsUnknown || !resp.LatencyUnitKnown || resp.StatusKey != "http.response.status_code" || resp.Source != "vm" {
		t.Fatalf("zarf: %+v", resp)
	}
	// histogram → count rate, rate modu; değer sorguları rtMetric adıyla
	if src.calls[0] != "countrate:rate" || src.calls[1] != "countrate:rate" || !strings.HasSuffix(src.calls[2], ":http_server_request_duration_seconds") {
		t.Fatalf("sorgu dizisi: %v", src.calls)
	}
	if f := src.filters[1]; len(f) != 1 || f[0].Values[0] != "^5[0-9][0-9]$" {
		t.Fatalf("5xx filtresi: %+v", f)
	}
	// rate penceresi + nokta bütçesi geçer
	if !strings.Contains(resp.Note, "5xx") || !strings.Contains(resp.Note, "birim s") {
		t.Fatalf("not: %s", resp.Note)
	}
}

func TestBuildServiceMetricRED_UnknownsAreAbsentNotZero(t *testing.T) {
	from, to := epFixtureWindow()
	src := redFixtureSource("", nil)
	id := endpointsMetricIdentity{Metric: "http.server.request.duration", RTMetric: "http.server.request.duration", Instrument: "histogram", Service: "cart"}
	p := serviceMetricREDPlan{Service: "cart", From: from, To: to, Env: "uat"}
	resp, err := buildServiceMetricRED(context.Background(), src, id, p)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := resp.Series["error_rate"]; ok || !resp.ErrorsUnknown {
		t.Fatalf("durum etiketi yok → error_rate serisi HİÇ olmamalı: %+v", resp.Series)
	}
	for _, k := range []string{"p50", "p95", "p99", "avg"} {
		if _, ok := resp.Series[k]; ok {
			t.Fatalf("birim bilinmiyor → %s serisi olmamalı", k)
		}
	}
	if resp.LatencyUnitKnown || !resp.EnvAmbiguous {
		t.Fatalf("bayraklar: %+v", resp)
	}
	if len(src.calls) != 1 {
		t.Fatalf("yalnız rate sorgusu atılmalı: %v", src.calls)
	}
	for _, w := range []string{"Failure rate BİLİNMİYOR", "Süre birimi BİLİNMİYOR", "TÜM ortamları"} {
		if !strings.Contains(resp.Note, w) {
			t.Fatalf("not %q içermeli: %s", w, resp.Note)
		}
	}
}

func TestErrorRatePercent(t *testing.T) {
	from, _ := epFixtureWindow()
	rate := []chstore.SpanMetricSeries{epConst(nil, from, 60, 3, 4)}
	rate[0].Points[2].Value = 0 // rate 0 → nokta atlanır
	errs := []chstore.SpanMetricSeries{epConst(nil, from, 60, 1, 1)}
	out := errorRatePercent(rate, errs)
	if len(out) != 1 || len(out[0].Points) != 2 || out[0].Points[0].Value != 25 || out[0].Points[1].Value != 0 {
		t.Fatalf("yüzde: %+v", out)
	}
	if got := errorRatePercent(nil, errs); len(got) != 0 {
		t.Fatalf("rate yok → boş: %+v", got)
	}
	if s := seriesStep(rate); s != 60 {
		t.Fatalf("adım: %d", s)
	}
}

func TestServiceMetricREDKeyCoversEveryInput(t *testing.T) {
	from, to := epFixtureWindow()
	base := serviceMetricREDPlan{Service: "cart", From: from, To: to, Mdp: 300, RateWin: 180, Env: "uat"}
	k0 := serviceMetricREDKey(base, "vm", "0")
	muts := []func(p *serviceMetricREDPlan){
		func(p *serviceMetricREDPlan) { p.Service = "pay" },
		func(p *serviceMetricREDPlan) { p.To = p.To.Add(time.Hour) },
		func(p *serviceMetricREDPlan) { p.Mdp = 600 },
		func(p *serviceMetricREDPlan) { p.RateWin = 60 },
		func(p *serviceMetricREDPlan) { p.Env = "prod" },
	}
	for i, m := range muts {
		p := base
		m(&p)
		if serviceMetricREDKey(p, "vm", "0") == k0 {
			t.Fatalf("girdi %d anahtara girmiyor", i)
		}
	}
	if serviceMetricREDKey(base, "ch", "0") == k0 || serviceMetricREDKey(base, "vm", "1") == k0 {
		t.Fatal("depo + dışlama özeti anahtarda olmalı")
	}
}

func TestServiceMetricREDRouteIsInTheRegistry(t *testing.T) {
	if _, ok := extraRouteRegistrars["service-metric-red"]; !ok {
		t.Fatal("service-metric-red defterde değil")
	}
	if src := readRepoFile(t, "api.go"); strings.Contains(src, "metric-red") {
		t.Fatal("api.go bu rotayı bilmemeli")
	}
}
