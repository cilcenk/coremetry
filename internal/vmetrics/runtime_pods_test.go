package vmetrics

// v0.10.374 — VM dilim 3c: JVM pod reads through VictoriaMetrics for the
// anomaly investigation and the MCP pod-health tool. Fake server answers
// by metric spelling; assertions are the ClickHouse reader's contract:
// heap = per-minute pool sums averaged over the window, rows without a
// positive limit dropped, post-GC averaged over positive buckets only;
// GC = pause per collection from _sum / _count increase; pod identity
// from the k8s.pod.name → host.name → service.instance.id chain.

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/cilcenk/coremetry/internal/chstore"
)

func podFake(t *testing.T, answers map[string]string) (*Service, *[]string) {
	t.Helper()
	var queries []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query().Get("query")
		queries = append(queries, q)
		for needle, body := range answers {
			if strings.Contains(q, needle) {
				_, _ = w.Write([]byte(body))
				return
			}
		}
		_, _ = w.Write([]byte(`{"status":"success","data":{"resultType":"matrix","result":[]}}`))
	}))
	t.Cleanup(srv.Close)
	s := New()
	s.Configure(Settings{Enabled: true, BaseURL: srv.URL})
	return s, &queries
}

func podMatrix(rows ...string) string {
	return `{"status":"success","data":{"resultType":"matrix","result":[` + strings.Join(rows, ",") + `]}}`
}

func TestJVMHeapPodUsageAveragesPoolSumsAndGatesOnLimit(t *testing.T) {
	s, queries := podFake(t, map[string]string{
		"memory_used_after_last_gc": podMatrix(
			`{"metric":{"service_name":"svc","k8s_pod_name":"pod-1"},"values":[[1700000000,"0"],[1700000060,"300"],[1700000120,"500"]]}`,
		),
		"memory_used": podMatrix(
			`{"metric":{"service_name":"svc","k8s_pod_name":"pod-1"},"values":[[1700000000,"800"],[1700000060,"1000"]]}`,
			`{"metric":{"service_name":"svc","host_name":"host-9"},"values":[[1700000060,"10"]]}`,
			`{"metric":{"service_name":"","k8s_pod_name":"orphan"},"values":[[1700000060,"10"]]}`,
		),
		"memory_limit": podMatrix(
			`{"metric":{"service_name":"svc","k8s_pod_name":"pod-1"},"values":[[1700000060,"2000"]]}`,
			`{"metric":{"service_name":"svc","host_name":"host-9"},"values":[[1700000060,"0"]]}`,
		),
	})
	to := time.Unix(1700000600, 0)
	out, err := s.JVMHeapPodUsage(context.Background(), to.Add(-10*time.Minute), to)
	if err != nil {
		t.Fatal(err)
	}
	if len(out) != 1 {
		t.Fatalf("samples = %+v (want only pod-1: host-9 has limit 0, orphan has no service)", out)
	}
	p := out[0]
	if p.Instance != "svc" || p.Subkey != "pod-1" || p.Usage != 900 || p.Limit != 2000 || p.PostGC != 400 {
		t.Fatalf("pod-1 = %+v (want usage avg 900, limit 2000, postgc avg over positives 400)", p)
	}
	if len(*queries) != 3 {
		t.Fatalf("three heap queries expected, got %d", len(*queries))
	}
	for _, q := range *queries {
		if !strings.Contains(q, "heap") || !strings.HasPrefix(q, "sum") {
			t.Fatalf("heap query must be sum by pod filtered on the heap type: %s", q)
		}
	}
}

func TestJVMGCPodStatsJoinsSumAndCount(t *testing.T) {
	s, _ := podFake(t, map[string]string{
		"_seconds_sum": podMatrix(
			`{"metric":{"service_name":"svc","k8s_pod_name":"pod-1"},"values":[[1700000600,"30"]]}`,
			`{"metric":{"service_name":"svc","service_instance_id":"abcdefghijklmnop"},"values":[[1700000600,"6"]]}`,
		),
		"_seconds_count": podMatrix(
			`{"metric":{"service_name":"svc","k8s_pod_name":"pod-1"},"values":[[1700000600,"120"]]}`,
			`{"metric":{"service_name":"svc","service_instance_id":"abcdefghijklmnop"},"values":[[1700000600,"0"]]}`,
		),
	})
	to := time.Unix(1700000600, 0)
	pauses, acts, err := s.JVMGCPodStats(context.Background(), to.Add(-10*time.Minute), to)
	if err != nil {
		t.Fatal(err)
	}
	if len(pauses) != 1 || pauses[0].Subkey != "pod-1" || pauses[0].Usage != 250 {
		t.Fatalf("pauses = %+v (want pod-1 30 s / 120 = 250 ms; zero-count pod dropped)", pauses)
	}
	if len(acts) != 1 || acts[0].SharePct != 5 || acts[0].RatePerMin != 12 {
		t.Fatalf("acts = %+v (want 30/600 = 5 %%, 120/10 min = 12/min)", acts)
	}
	viaReader, err := s.JVMGCPodPause(context.Background(), to.Add(-10*time.Minute), to)
	if err != nil || len(viaReader) != 1 {
		t.Fatalf("JVMGCPodPause = %+v, %v", viaReader, err)
	}
	if PodFromTuple([]string{"svc", "", "", "abcdefghijklmnop"}) != "abcdefgh" {
		t.Fatal("instance id identity must be truncated to 8 runes")
	}
}

type fakePodReader struct{ tag string }

func (f fakePodReader) JVMHeapPodUsage(context.Context, time.Time, time.Time) ([]chstore.CapacitySample, error) {
	return []chstore.CapacitySample{{Instance: f.tag}}, nil
}
func (f fakePodReader) JVMGCPodPause(context.Context, time.Time, time.Time) ([]chstore.CapacitySample, error) {
	return []chstore.CapacitySample{{Instance: f.tag}}, nil
}

func TestRuntimePodsOrPicksAtCallTime(t *testing.T) {
	vm := New()
	r := RuntimePodsOr(vm, fakePodReader{tag: "ch"})
	now := time.Now()
	if got, _ := r.JVMHeapPodUsage(context.Background(), now, now); got[0].Instance != "ch" {
		t.Fatal("unconfigured VM → fallback")
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"status":"success","data":{"resultType":"matrix","result":[]}}`))
	}))
	t.Cleanup(srv.Close)
	vm.Configure(Settings{Enabled: true, BaseURL: srv.URL})
	if got, err := r.JVMGCPodPause(context.Background(), now.Add(-time.Minute), now); err != nil || len(got) != 0 {
		t.Fatalf("configured VM must answer (empty here): %v %v", got, err)
	}
	if RuntimePodsOr(nil, fakePodReader{tag: "ch"}).(runtimePodsOr).pick().(fakePodReader).tag != "ch" {
		t.Fatal("nil VM → fallback")
	}
}
