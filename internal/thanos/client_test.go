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
