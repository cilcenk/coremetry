package thanos

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// v0.8.575 — client contracts against a fake Querier (audit §4):
// four-query merge, best-effort limits, bearer header, masked
// snapshot, minute-bucket trend.

func fakeQuerier(t *testing.T, wantBearer string, byQuerySub map[string]string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if wantBearer != "" && r.Header.Get("Authorization") != "Bearer "+wantBearer {
			w.WriteHeader(http.StatusUnauthorized)
			fmt.Fprint(w, `{"status":"error","errorType":"unauthorized","error":"bad token"}`)
			return
		}
		q := r.URL.Query().Get("query")
		for sub, resp := range byQuerySub {
			if strings.Contains(q, sub) {
				fmt.Fprint(w, resp)
				return
			}
		}
		// Unknown query → empty success (Prometheus shape for "no series").
		fmt.Fprint(w, `{"status":"success","data":{"resultType":"vector","result":[]}}`)
	}))
}

func vec(samples ...string) string {
	return `{"status":"success","data":{"resultType":"vector","result":[` +
		strings.Join(samples, ",") + `]}}`
}

func sample(ns, pod string, v string) string {
	return fmt.Sprintf(`{"metric":{"namespace":"%s","pod":"%s"},"value":[1784271068,"%s"]}`, ns, pod, v)
}

func TestPodMetricsMergesFourQueries(t *testing.T) {
	srv := fakeQuerier(t, "tok-1", map[string]string{
		"container_cpu_usage_seconds_total": vec(
			sample("payments", "api-1", "0.5"),
			sample("payments", "api-2", "0.1")),
		"container_memory_working_set_bytes": vec(
			sample("payments", "api-1", "1073741824")),
		`resource="cpu"`: vec(sample("payments", "api-1", "1")),
		`resource="memory"`: vec(
			sample("payments", "api-1", "2147483648"),
			// limit-only pod: no cpu/mem sample → must be skipped
			sample("payments", "idle-1", "1")),
	})
	defer srv.Close()

	s := New()
	c := ClusterConfig{Name: "prod-ist", URL: srv.URL, AuthType: "bearer", Token: "tok-1", Enabled: true}
	rows, err := s.PodMetrics(context.Background(), c)
	if err != nil {
		t.Fatalf("PodMetrics: %v", err)
	}
	if len(rows) != 2 {
		t.Fatalf("want 2 rows (idle limit-only pod skipped), got %d: %+v", len(rows), rows)
	}
	var api1 *PodRow
	for i := range rows {
		if rows[i].Pod == "api-1" {
			api1 = &rows[i]
		}
	}
	if api1 == nil {
		t.Fatal("api-1 row missing")
	}
	if api1.Cluster != "prod-ist" || api1.Namespace != "payments" {
		t.Fatalf("identity wrong: %+v", *api1)
	}
	if api1.CPUCores != 0.5 || api1.MemBytes != 1073741824 {
		t.Fatalf("usage wrong: %+v", *api1)
	}
	if api1.CPUPct != 50 || api1.MemPct != 50 {
		t.Fatalf("pct wrong (want 50/50): %+v", *api1)
	}
}

// v0.8.580 — request ekseni: PctOfReq değerleri bilerek CLAMP'SİZ
// (aşım = sinyal); limit yüzdeleri clamp'li kalır; requests serisi
// yoksa alanlar 0 (best-effort sözleşmesi, mevcut test zaten
// limit'siz durumu pin'liyor).
func TestPodMetricsRequestAxisUnclamped(t *testing.T) {
	srv := fakeQuerier(t, "", map[string]string{
		"container_cpu_usage_seconds_total":   vec(sample("ns", "p", "0.5")),
		"container_memory_working_set_bytes":  vec(sample("ns", "p", "100")),
		`resource_requests{resource="cpu"`:    vec(sample("ns", "p", "0.25")),
		`resource_requests{resource="memory"`: vec(sample("ns", "p", "200")),
	})
	defer srv.Close()

	s := New()
	rows, err := s.PodMetrics(context.Background(), ClusterConfig{Name: "c", URL: srv.URL, Enabled: true})
	if err != nil {
		t.Fatalf("PodMetrics: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("want 1 row, got %+v", rows)
	}
	// 0.5 core kullanım / 0.25 request = %200 — clamp'lenmemeli.
	if rows[0].CPUPctOfReq != 200 {
		t.Fatalf("CPUPctOfReq = %v, want 200 (unclamped)", rows[0].CPUPctOfReq)
	}
	if rows[0].MemPctOfReq != 50 {
		t.Fatalf("MemPctOfReq = %v, want 50", rows[0].MemPctOfReq)
	}
	// Limits fikstürü yok → limit yüzdeleri 0 kalır.
	if rows[0].CPUPct != 0 || rows[0].MemPct != 0 {
		t.Fatalf("limit pcts must stay 0 without limits: %+v", rows[0])
	}
}

func TestPodMetricsLimitsAreBestEffort(t *testing.T) {
	srv := fakeQuerier(t, "", map[string]string{
		"container_cpu_usage_seconds_total":  vec(sample("ns", "p", "0.2")),
		"container_memory_working_set_bytes": vec(sample("ns", "p", "100")),
		"kube_pod_container_resource_limits": `{"status":"error","errorType":"execution","error":"unknown metric"}`,
	})
	defer srv.Close()

	s := New()
	rows, err := s.PodMetrics(context.Background(), ClusterConfig{Name: "c", URL: srv.URL, Enabled: true})
	if err != nil {
		t.Fatalf("limits failure must not fail the read: %v", err)
	}
	if len(rows) != 1 || rows[0].CPUPct != 0 || rows[0].MemPct != 0 {
		t.Fatalf("want 1 row with pct=0 (unknown-limit contract), got %+v", rows)
	}
}

func TestPodMetricsSurfacesAuthError(t *testing.T) {
	srv := fakeQuerier(t, "right-token", nil)
	defer srv.Close()

	s := New()
	_, err := s.PodMetrics(context.Background(),
		ClusterConfig{Name: "c", URL: srv.URL, AuthType: "bearer", Token: "wrong", Enabled: true})
	if err == nil || !strings.Contains(err.Error(), "401") {
		t.Fatalf("want HTTP 401 error, got %v", err)
	}
}

func TestPodTrendMinuteBuckets(t *testing.T) {
	matrix := func(pairs string) string {
		return `{"status":"success","data":{"resultType":"matrix","result":[{"metric":{},"values":[` + pairs + `]}]}}`
	}
	srv := fakeQuerier(t, "", map[string]string{
		"container_cpu_usage_seconds_total":  matrix(`[1784271060,"0.2"],[1784271120,"0.4"]`),
		"container_memory_working_set_bytes": matrix(`[1784271060,"100"],[1784271120,"200"]`),
	})
	defer srv.Close()

	s := New()
	pts, err := s.PodTrend(context.Background(),
		ClusterConfig{Name: "c", URL: srv.URL, Enabled: true},
		"ns", "p", time.Unix(1784271000, 0), time.Unix(1784271200, 0))
	if err != nil {
		t.Fatalf("PodTrend: %v", err)
	}
	if len(pts) != 2 {
		t.Fatalf("want 2 minute buckets, got %d: %+v", len(pts), pts)
	}
	if pts[0].Bucket != 1784271060 || pts[1].Bucket != 1784271120 {
		t.Fatalf("buckets not minute-aligned ascending: %+v", pts)
	}
	if pts[0].CPUCores != 0.2 || pts[0].MemBytes != 100 ||
		pts[1].CPUCores != 0.4 || pts[1].MemBytes != 200 {
		t.Fatalf("values misplaced: %+v", pts)
	}
}

// v0.8.582 — node metrikleri (dar kapsam): 3 zorunlu + 2 best-effort
// sorgu merge'i, kube_node_info ad güzelleştirmesi, çekirdeksiz
// pct=0 sözleşmesi.

func nodeSample(inst, v string) string {
	return fmt.Sprintf(`{"metric":{"instance":"%s"},"value":[1784271068,"%s"]}`, inst, v)
}

func TestNodeMetricsMergeAndNamePrettify(t *testing.T) {
	srv := fakeQuerier(t, "", map[string]string{
		`mode!="idle"`:                   vec(nodeSample("10.0.1.5:9100", "2.5")),
		"node_memory_MemTotal_bytes":     vec(nodeSample("10.0.1.5:9100", "1000")),
		"node_memory_MemAvailable_bytes": vec(nodeSample("10.0.1.5:9100", "400")),
		`mode="idle"`:                    vec(nodeSample("10.0.1.5:9100", "8")),
		"kube_node_info": `{"status":"success","data":{"resultType":"vector","result":[
			{"metric":{"node":"worker-1","internal_ip":"10.0.1.5"},"value":[1784271068,"1"]}]}}`,
	})
	defer srv.Close()

	s := New()
	rows, err := s.NodeMetrics(context.Background(), ClusterConfig{Name: "c", URL: srv.URL, Enabled: true})
	if err != nil {
		t.Fatalf("NodeMetrics: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("want 1 node, got %+v", rows)
	}
	r := rows[0]
	if r.Node != "worker-1" {
		t.Fatalf("node adı güzelleşmedi: %q", r.Node)
	}
	if r.CPUCores != 2.5 || r.MemBytes != 600 {
		t.Fatalf("usage yanlış: %+v", r)
	}
	// 2.5/8 çekirdek = %31.25; (1000-400)/1000 = %60.
	if r.CPUPct != 31.25 || r.MemPct != 60 {
		t.Fatalf("pct yanlış (want 31.25/60): %+v", r)
	}
}

func TestNodeMetricsBestEffortDegradation(t *testing.T) {
	// Çekirdek sayısı + kube_node_info YOK: CPUPct 0 kalır, ad
	// instance kalır; MemPct zorunlu aileden yine dolu.
	srv := fakeQuerier(t, "", map[string]string{
		`mode!="idle"`:                   vec(nodeSample("10.0.1.7:9100", "1")),
		"node_memory_MemTotal_bytes":     vec(nodeSample("10.0.1.7:9100", "2000")),
		"node_memory_MemAvailable_bytes": vec(nodeSample("10.0.1.7:9100", "1500")),
		`mode="idle"`:                    `{"status":"error","errorType":"execution","error":"nope"}`,
		"kube_node_info":                 `{"status":"error","errorType":"execution","error":"nope"}`,
	})
	defer srv.Close()

	s := New()
	rows, err := s.NodeMetrics(context.Background(), ClusterConfig{Name: "c", URL: srv.URL, Enabled: true})
	if err != nil {
		t.Fatalf("best-effort hataları okumayı düşürmemeli: %v", err)
	}
	if len(rows) != 1 || rows[0].Node != "10.0.1.7:9100" || rows[0].CPUPct != 0 || rows[0].MemPct != 25 {
		t.Fatalf("degradation sözleşmesi: %+v", rows)
	}
}

func TestNodeMetricsMandatoryFailureSurfaces(t *testing.T) {
	srv := fakeQuerier(t, "", map[string]string{
		`mode!="idle"`: `{"status":"error","errorType":"unavailable","error":"tenancy port"}`,
	})
	defer srv.Close()
	s := New()
	if _, err := s.NodeMetrics(context.Background(),
		ClusterConfig{Name: "c", URL: srv.URL, Enabled: true}); err == nil {
		t.Fatal("zorunlu sorgu hatası yüzeye çıkmalı")
	}
}

func TestInstanceHost(t *testing.T) {
	cases := []struct{ in, want string }{
		{"10.0.1.5:9100", "10.0.1.5"},
		{"10.0.1.5", "10.0.1.5"},
		{"worker-1.example:9100", "worker-1.example"},
		{"[::1]:9100", "::1"},
	}
	for _, c := range cases {
		if got := instanceHost(c.in); got != c.want {
			t.Fatalf("instanceHost(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

// v0.8.586 — kart özeti: skaler sorgular, kısmi başarı (tenancy
// senaryosu: node ailesi boş, pod sayısı dolu), tam başarısızlıkta
// hata.
func scalarVec(v string) string {
	return `{"status":"success","data":{"resultType":"vector","result":[{"metric":{},"value":[1784271068,"` + v + `"]}]}}`
}

func TestSummaryPartialBestEffort(t *testing.T) {
	srv := fakeQuerier(t, "", map[string]string{
		// node ailesi hata (tenancy port) — pod sayısı çalışıyor.
		"node_cpu_seconds_total":            `{"status":"error","errorType":"unavailable","error":"tenancy"}`,
		"node_memory_MemTotal":              `{"status":"error","errorType":"unavailable","error":"tenancy"}`,
		"container_cpu_usage_seconds_total": scalarVec("42"),
	})
	defer srv.Close()
	s := New()
	sum, err := s.Summary(context.Background(), ClusterConfig{Name: "c", URL: srv.URL, Enabled: true})
	if err != nil {
		t.Fatalf("kısmi başarı hataya dönüşmemeli: %v", err)
	}
	if sum.Pods != 42 || sum.Nodes != 0 || sum.CPUUsedCores != 0 {
		t.Fatalf("partial summary yanlış: %+v", sum)
	}
}

func TestSummaryAllFailSurfaces(t *testing.T) {
	srv := fakeQuerier(t, "must-fail", nil) // her sorgu 401
	defer srv.Close()
	s := New()
	if _, err := s.Summary(context.Background(),
		ClusterConfig{Name: "c", URL: srv.URL, AuthType: "bearer", Token: "wrong", Enabled: true}); err == nil {
		t.Fatal("dört sorgu da düşünce hata yüzeye çıkmalı")
	}
}

func TestSummaryFullCounts(t *testing.T) {
	srv := fakeQuerier(t, "", map[string]string{
		`mode="idle"`:                       scalarVec("6"),
		"container_cpu_usage_seconds_total": scalarVec("237"),
		`mode!="idle"`:                      scalarVec("14.5"),
		"node_memory_MemTotal":              scalarVec("123456789"),
	})
	defer srv.Close()
	s := New()
	sum, err := s.Summary(context.Background(), ClusterConfig{Name: "c", URL: srv.URL, Enabled: true})
	if err != nil {
		t.Fatalf("Summary: %v", err)
	}
	if sum.Nodes != 6 || sum.Pods != 237 || sum.CPUUsedCores != 14.5 || sum.MemUsedBytes != 123456789 {
		t.Fatalf("summary yanlış: %+v", sum)
	}
}

// v0.8.588 — namespace rollup: sum by (namespace) merge'i + pod
// sayısı best-effort + boş-namespace eleme.
func nsRollupSample(ns, v string) string {
	return fmt.Sprintf(`{"metric":{"namespace":"%s"},"value":[1784271068,"%s"]}`, ns, v)
}

func TestNamespaceMetricsMerge(t *testing.T) {
	srv := fakeQuerier(t, "", map[string]string{
		"rate(container_cpu_usage_seconds_total": vec(
			nsRollupSample("payments", "3.5"),
			nsRollupSample("infra", "0.5")),
		"container_memory_working_set_bytes": vec(
			nsRollupSample("payments", "1000")),
		"count by (namespace) (count by (namespace, pod)": vec(
			nsRollupSample("payments", "12")),
	})
	defer srv.Close()

	s := New()
	rows, err := s.NamespaceMetrics(context.Background(), ClusterConfig{Name: "c", URL: srv.URL, Enabled: true})
	if err != nil {
		t.Fatalf("NamespaceMetrics: %v", err)
	}
	if len(rows) != 2 {
		t.Fatalf("want 2 namespaces, got %+v", rows)
	}
	var pay *NamespaceRow
	for i := range rows {
		if rows[i].Namespace == "payments" {
			pay = &rows[i]
		}
	}
	if pay == nil || pay.CPUCores != 3.5 || pay.MemBytes != 1000 || pay.Pods != 12 || pay.Cluster != "c" {
		t.Fatalf("payments rollup yanlış: %+v", pay)
	}
}

func TestNamespaceMetricsMandatoryFailure(t *testing.T) {
	srv := fakeQuerier(t, "", map[string]string{
		"rate(container_cpu_usage_seconds_total": `{"status":"error","errorType":"x","error":"y"}`,
	})
	defer srv.Close()
	s := New()
	if _, err := s.NamespaceMetrics(context.Background(),
		ClusterConfig{Name: "c", URL: srv.URL, Enabled: true}); err == nil {
		t.Fatal("zorunlu cpu sorgusu hatası yüzeye çıkmalı")
	}
}

func TestSnapshotMasksTokens(t *testing.T) {
	s := New()
	s.Configure(Settings{Clusters: []ClusterConfig{
		{Name: "a", URL: "https://x", AuthType: "bearer", Token: "secret", Enabled: true},
		{Name: "b", URL: "https://y", Enabled: false},
	}})
	snap := s.Snapshot()
	if len(snap.Clusters) != 2 {
		t.Fatalf("want 2 clusters, got %d", len(snap.Clusters))
	}
	if !snap.Clusters[0].HasToken || snap.Clusters[1].HasToken {
		t.Fatalf("hasToken wrong: %+v", snap.Clusters)
	}
	// The masked type carries no token field at all — but pin the
	// JSON too so a future refactor can't leak it.
	if strings.Contains(fmt.Sprintf("%+v", snap), "secret") {
		t.Fatal("token leaked into snapshot")
	}
}

func TestClusterByNameOnlyEnabled(t *testing.T) {
	s := New()
	s.Configure(Settings{Clusters: []ClusterConfig{
		{Name: "on", Enabled: true},
		{Name: "off", Enabled: false},
	}})
	if _, ok := s.ClusterByName("on"); !ok {
		t.Fatal("enabled cluster not found")
	}
	if _, ok := s.ClusterByName("off"); ok {
		t.Fatal("disabled cluster must not resolve")
	}
	if !s.HasEnabledClusters() {
		t.Fatal("HasEnabledClusters false with one enabled")
	}
}
