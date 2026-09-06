package api

// v0.10.365 — /hosts + service infra sparklines through the metricSource
// seam (VM dilim 3a). Pure builders driven by a fake source: candidate
// fallback order, per-host folding (mem% only over limited services),
// Up from bucket freshness, VM end-stamped minute shift, and the
// handler pins that keep the backend name in every cache key.

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/cilcenk/coremetry/internal/chstore"
)

type fakeHostSource struct {
	metricSource
	name    string
	exists  map[string]bool
	queryFn func(f chstore.MetricQueryFilter) ([]chstore.SpanMetricSeries, error)
	calls   []string
	filters []chstore.MetricQueryFilter
}

func (f *fakeHostSource) Name() string { return f.name }
func (f *fakeHostSource) MetricExists(_ context.Context, name string) (bool, error) {
	f.calls = append(f.calls, "exists:"+name)
	return f.exists[name], nil
}
func (f *fakeHostSource) QueryMetric(_ context.Context, q chstore.MetricQueryFilter) ([]chstore.SpanMetricSeries, error) {
	f.calls = append(f.calls, "query:"+q.Aggregation+":"+q.Name)
	f.filters = append(f.filters, q)
	return f.queryFn(q)
}

func pt(t time.Time, v float64) chstore.SpanMetricPoint {
	return chstore.SpanMetricPoint{Time: t.UnixNano(), Value: v}
}

func TestHostsMetricStep(t *testing.T) {
	to := time.Date(2026, 9, 5, 12, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		window time.Duration
		want   int
	}{{15 * time.Minute, 60}, {time.Hour, 60}, {6 * time.Hour, 360}} {
		if got := hostsMetricStep(to.Add(-tc.window), to); got != tc.want {
			t.Fatalf("%s → step %d, want %d", tc.window, got, tc.want)
		}
	}
}

func TestBuildHostsMetricFoldsCandidatesPerHost(t *testing.T) {
	to := time.Date(2026, 9, 5, 12, 0, 0, 0, time.UTC)
	from := to.Add(-time.Hour)
	src := &fakeHostSource{
		name: "vm",
		exists: map[string]bool{
			"process.runtime.cpu.utilization": true, // 2nd cpu candidate
			"process.runtime.memory.rss":      true, // 2nd mem candidate
			"k8s.pod.memory.limit":            true, // 2nd lim candidate
		},
	}
	src.queryFn = func(q chstore.MetricQueryFilter) ([]chstore.SpanMetricSeries, error) {
		switch q.Name {
		case "process.runtime.cpu.utilization":
			return []chstore.SpanMetricSeries{
				{GroupKey: []string{"host-a", "svc-x", "zone-1"}, Points: []chstore.SpanMetricPoint{pt(to.Add(-2*time.Minute), 0.5), pt(to, 0.3)}},
				{GroupKey: []string{"host-a", "svc-y", ""}, Points: []chstore.SpanMetricPoint{pt(to, 0.2)}},
				{GroupKey: []string{"host-b", "svc-x", "zone-2"}, Points: []chstore.SpanMetricPoint{pt(to.Add(-10*time.Minute), 0.1)}},
				{GroupKey: []string{"", "svc-orphan", ""}, Points: []chstore.SpanMetricPoint{pt(to, 9)}},
			}, nil
		case "process.runtime.memory.rss":
			return []chstore.SpanMetricSeries{
				{GroupKey: []string{"host-a", "svc-x"}, Points: []chstore.SpanMetricPoint{pt(to, 100)}},
				{GroupKey: []string{"host-a", "svc-y"}, Points: []chstore.SpanMetricPoint{pt(to, 50)}},
				{GroupKey: []string{"host-b", "svc-x"}, Points: []chstore.SpanMetricPoint{pt(to.Add(-10*time.Minute), 30)}},
			}, nil
		case "k8s.pod.memory.limit":
			return []chstore.SpanMetricSeries{
				{GroupKey: []string{"host-a", "svc-x"}, Points: []chstore.SpanMetricPoint{pt(to, 400)}},
			}, nil
		}
		t.Fatalf("unexpected query %s", q.Name)
		return nil, nil
	}
	rows, err := buildHostsMetric(context.Background(), src, from, to)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 {
		t.Fatalf("rows = %d, want 2 (orphan host dropped): %+v", len(rows), rows)
	}
	a, b := rows[0], rows[1]
	if a.Host != "host-a" || b.Host != "host-b" {
		t.Fatalf("order by cpu desc: %s, %s", a.Host, b.Host)
	}
	if a.CPUPct != 50 || a.MemBytes != 150 || a.MemPct != 25 || a.Zone != "zone-1" {
		t.Fatalf("host-a cpu=%v mem=%v mem%%=%v zone=%q", a.CPUPct, a.MemBytes, a.MemPct, a.Zone)
	}
	if strings.Join(a.Services, ",") != "svc-x,svc-y" {
		t.Fatalf("host-a services %v", a.Services)
	}
	if !a.Up || b.Up {
		t.Fatalf("Up: a=%v (want true) b=%v (want false, 10 min stale vs 2×60 s)", a.Up, b.Up)
	}
	if b.MemPct != 0 {
		t.Fatalf("host-b has no limit → mem%% 0, got %v", b.MemPct)
	}
	// Missing candidates are never queried; present ones are queried
	// with last-aggregation and the host/service grouping.
	joined := strings.Join(src.calls, " ")
	if strings.Contains(joined, "query:last:jvm.cpu.recent_utilization") {
		t.Fatalf("absent candidate was queried: %s", joined)
	}
	if !strings.Contains(joined, "query:last:process.runtime.cpu.utilization") {
		t.Fatalf("present candidate not queried: %s", joined)
	}
	for _, f := range src.filters {
		if f.GroupBy[0] != "host.name" || f.GroupBy[1] != "service.name" || f.StepSeconds != 60 {
			t.Fatalf("group/step: %+v", f)
		}
	}
}

func TestBuildHostDetailMetricFiltersHostAndShiftsVMMinutes(t *testing.T) {
	to := time.Date(2026, 9, 5, 12, 0, 0, 0, time.UTC)
	from := to.Add(-10 * time.Minute)
	src := &fakeHostSource{
		name:   "vm",
		exists: map[string]bool{"jvm.cpu.recent_utilization": true, "jvm.memory.used": true},
	}
	src.queryFn = func(q chstore.MetricQueryFilter) ([]chstore.SpanMetricSeries, error) {
		if q.Aggregation == "last" && len(q.GroupBy) >= 2 { // latest rows
			if q.Name == "jvm.cpu.recent_utilization" {
				return []chstore.SpanMetricSeries{
					{GroupKey: []string{"host-a", "svc-x", "zone-1"}, Points: []chstore.SpanMetricPoint{pt(to, 0.4)}},
					{GroupKey: []string{"host-a", "svc-y", ""}, Points: []chstore.SpanMetricPoint{pt(to, 0.6)}},
				}, nil
			}
			return []chstore.SpanMetricSeries{
				{GroupKey: []string{"host-a", "svc-x"}, Points: []chstore.SpanMetricPoint{pt(to, 100)}},
			}, nil
		}
		// trend: service-only grouping. v0.10.504 (A6) — kaynak artık kova
		// BAŞLANGICINI damgalar (vmetrics bucketStartNs); burada kaydırma yok.
		m1 := to.Add(-10 * time.Minute)
		m2 := to.Add(-9 * time.Minute)
		if q.Name == "jvm.cpu.recent_utilization" {
			return []chstore.SpanMetricSeries{{GroupKey: []string{"svc-x"}, Points: []chstore.SpanMetricPoint{pt(m1, 0.4), pt(m2, 0.2)}}}, nil
		}
		return []chstore.SpanMetricSeries{{GroupKey: []string{"svc-x"}, Points: []chstore.SpanMetricPoint{pt(m1, 100), pt(m2, 120)}}}, nil
	}
	d, err := buildHostDetailMetric(context.Background(), src, "host-a", from, to)
	if err != nil {
		t.Fatal(err)
	}
	if d.Zone != "zone-1" || len(d.Services) != 2 || d.Services[0].Service != "svc-y" || d.Services[0].CPUPct != 60 || d.Services[1].MemBytes != 100 {
		t.Fatalf("services: zone=%q %+v", d.Zone, d.Services)
	}
	for _, f := range src.filters {
		if len(f.Filters) != 1 || f.Filters[0].Key != "host.name" || f.Filters[0].Values[0] != "host-a" {
			t.Fatalf("every seam query must be pinned to the host: %+v", f.Filters)
		}
	}
	if len(d.Trend) != 2 {
		t.Fatalf("trend points = %d, want 2: %+v", len(d.Trend), d.Trend)
	}
	wantFirst := to.Add(-10 * time.Minute).Unix() // kova başlangıcı olduğu gibi
	if d.Trend[0].Bucket != wantFirst || d.Trend[0].CPUPct != 40 || d.Trend[0].MemBytes != 100 {
		t.Fatalf("trend[0] = %+v, want bucket %d cpu 40 mem 100", d.Trend[0], wantFirst)
	}
	if d.Trend[1].CPUPct != 20 || d.Trend[1].MemBytes != 120 {
		t.Fatalf("trend[1] = %+v", d.Trend[1])
	}
}

func TestBuildInfraMetricWalksSlotFallback(t *testing.T) {
	now := time.Date(2026, 9, 5, 12, 0, 0, 0, time.UTC)
	src := &fakeHostSource{
		name: "vm",
		exists: map[string]bool{
			"container.cpu.usage":        true, // exists but empty for this service
			"jvm.cpu.recent_utilization": true,
			"http.server.requests":       true,
		},
	}
	src.queryFn = func(q chstore.MetricQueryFilter) ([]chstore.SpanMetricSeries, error) {
		if q.Service != "svc-x" {
			t.Fatalf("service filter lost: %+v", q)
		}
		switch q.Name {
		case "container.cpu.usage":
			return nil, nil
		case "jvm.cpu.recent_utilization":
			return []chstore.SpanMetricSeries{{Points: []chstore.SpanMetricPoint{pt(now.Add(-time.Minute), 0.3), pt(now, 0.5)}}}, nil
		case "http.server.requests":
			return []chstore.SpanMetricSeries{
				{GroupKey: []string{"a"}, Points: []chstore.SpanMetricPoint{pt(now, 10)}},
				{GroupKey: []string{"b"}, Points: []chstore.SpanMetricPoint{pt(now, 30)}},
			}, nil
		}
		t.Fatalf("unexpected query %s", q.Name)
		return nil, nil
	}
	out, err := buildInfraMetric(context.Background(), src, "svc-x", 15*time.Minute, now)
	if err != nil {
		t.Fatal(err)
	}
	if len(out) != 2 {
		t.Fatalf("slots = %d, want cpu + rps: %+v", len(out), out)
	}
	cpu, rps := out[0], out[1]
	if cpu.Metric != "cpu" || cpu.Source != "jvm.cpu.recent_utilization" || cpu.Unit != "%" || len(cpu.Points) != 2 {
		t.Fatalf("cpu slot: %+v", cpu)
	}
	if rps.Metric != "rps" || len(rps.Points) != 1 || rps.Points[0].Value != 20 {
		t.Fatalf("rps slot must average split series: %+v", rps)
	}
	if src.filters[0].StepSeconds != 10 { // 15 min / 120 buckets = 7.5 s → floor 10 s
		t.Fatalf("bucket = %d s, want 10", src.filters[0].StepSeconds)
	}
	if strings.Contains(strings.Join(src.calls, " "), "query:avg:k8s.pod.cpu.usage") {
		t.Fatal("absent first candidate must not be queried")
	}
}

// Handler pins: backend name in every cache key and an explicit CH
// branch — otherwise flipping the source serves the other backend's
// body for a full TTL (v0.5.187 class).
func TestHostAndInfraHandlersKeyOnSource(t *testing.T) {
	hosts := readRepoFile(t, "hosts.go")
	for _, want := range []string{`"hosts:" + src.Name()`, `"hosts-detail:%s:%s:%s", src.Name()`, "buildHostsMetric(ctx, src", "buildHostDetailMetric(ctx, src"} {
		if !strings.Contains(hosts, want) {
			t.Fatalf("hosts.go must contain %q", want)
		}
	}
	if strings.Count(hosts, "src.Name() == metricSourceCH") != 2 {
		t.Fatal("both host handlers keep the ClickHouse SQL path behind an explicit branch")
	}
	apiSrc := readRepoFile(t, "api.go")
	i := strings.Index(apiSrc, "func (s *Server) getServiceInfraMetrics(")
	if i < 0 {
		t.Fatal("getServiceInfraMetrics not found")
	}
	body := apiSrc[i:]
	if j := strings.Index(body, "\nfunc "); j > 0 {
		body = body[:j]
	}
	for _, want := range []string{`"infra-metrics:%s:svc=%s:since=%s", src.Name()`, "buildInfraMetric(ctx, src, name, since"} {
		if !strings.Contains(body, want) {
			t.Fatalf("infra handler must contain %q", want)
		}
	}
}
