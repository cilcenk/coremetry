package mcptools

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/cilcenk/coremetry/internal/anomaly"
	"github.com/cilcenk/coremetry/internal/chstore"
	"github.com/cilcenk/coremetry/internal/logstore"
	"github.com/cilcenk/coremetry/internal/thanos"
)

// v0.10.556 — Faz 4b sinyal araçları: pencere basamağı, desen süzgeci (servis
// topServices'te de olabilir; sessize alınmış düşer; oran sırası; tavan),
// seyreltme (ilk/son korunur), cluster_metric doğrulaması + disabled şekli.
func TestSnapLogPatternWindowAndFilter(t *testing.T) {
	for in, want := range map[int]time.Duration{0: 5 * time.Minute, 30: time.Minute, 300: 5 * time.Minute, 600: 15 * time.Minute, 9999: 30 * time.Minute} {
		if got := snapLogPatternWindow(in); got != want {
			t.Errorf("snap(%d)=%v want %v", in, got, want)
		}
	}
	hits := []anomaly.LogPatternAnomaly{
		{Pattern: "OOMKilled", Kind: "new", Ratio: 9, Service: "a", Sample: strings.Repeat("x", 300)},
		{Pattern: "timeout", Kind: "spike", Ratio: 3, Service: "b", TopServices: []logstore.PatternServiceHit{{Service: "a", Count: 2}}},
		{Pattern: "muted", Kind: "spike", Ratio: 99, Service: "a"},
		{Pattern: "other", Kind: "new", Ratio: 1, Service: "c"},
	}
	muted := map[string]bool{chstore.FingerprintAnomaly("log_pattern", "muted", "a"): true}
	rows, total := filterLogPatterns(hits, "a", "", muted, 10)
	if total != 2 || len(rows) != 2 || rows[0].Pattern != "OOMKilled" || rows[1].Pattern != "timeout" {
		t.Fatalf("servis süzgeci/sıra: %+v", rows)
	}
	if !strings.HasSuffix(rows[0].Sample, "…") || len([]rune(rows[0].Sample)) != logPatternSampleMax+1 {
		t.Fatal("örnek gövde kırpılmalı")
	}
	if rows, total := filterLogPatterns(hits, "", "new", muted, 1); total != 2 || len(rows) != 1 {
		t.Fatalf("tür süzgeci + tavan: %d %d", total, len(rows))
	}
}

func TestThinEvery(t *testing.T) {
	xs := make([]int, 100)
	for i := range xs {
		xs[i] = i
	}
	out := thinEvery(xs, 10)
	if len(out) != 10 || out[0] != 0 || out[9] != 99 {
		t.Fatalf("seyreltme: %v", out)
	}
	if got := thinEvery(xs[:5], 10); len(got) != 5 {
		t.Fatal("tavan altı dokunulmaz")
	}
}

type fakeClusterMetrics struct{ calls []string }

func (f *fakeClusterMetrics) PodTrend(_ context.Context, cluster, ns, pod string, _, _ time.Time) ([]thanos.TrendPoint, error) {
	f.calls = append(f.calls, "pod:"+cluster+"/"+ns+"/"+pod)
	pts := make([]thanos.TrendPoint, 200)
	for i := range pts {
		pts[i] = thanos.TrendPoint{Bucket: int64(i), CPUCores: 0.5}
	}
	return pts, nil
}
func (f *fakeClusterMetrics) NamespaceTrend(_ context.Context, cluster, ns string, _, _ time.Time) ([]thanos.TrendPoint, error) {
	f.calls = append(f.calls, "ns:"+cluster+"/"+ns)
	return nil, nil
}
func (f *fakeClusterMetrics) DeployTrend(_ context.Context, cluster, ns, deploy, metric string, byPod bool, _, _ time.Time, mdp int) ([]thanos.NamedSeries, int, error) {
	f.calls = append(f.calls, "deploy:"+metric)
	return []thanos.NamedSeries{{Name: "p1", Points: make([]thanos.ValuePoint, 100)}}, 12, nil
}
func (f *fakeClusterMetrics) NetworkTrend(_ context.Context, cluster string, _, _ time.Time) ([]thanos.NetTrendPoint, error) {
	f.calls = append(f.calls, "net:"+cluster)
	return nil, nil
}

func TestClusterMetricTool(t *testing.T) {
	// Thanos yok → disabled (hata değil).
	tool := toolByName(t, ToolList(Deps{}), "cluster_metric")
	out, err := tool.Handler(context.Background(), json.RawMessage(`{"kind":"pod","cluster":"c"}`))
	if err != nil || out.(map[string]any)["disabled"] != true {
		t.Fatalf("disabled bekleniyordu: %v %v", out, err)
	}
	fk := &fakeClusterMetrics{}
	d := Deps{ClusterMetrics: fk, Clusters: func() []ClusterRef { return []ClusterRef{{ID: "c", Name: "prod-eu"}} }}
	tool = toolByName(t, ToolList(d), "cluster_metric")
	for _, bad := range []string{`{"kind":"pod","cluster":"c"}`, `{"kind":"x","cluster":"c"}`, `{"kind":"deploy","cluster":"c","namespace":"n","workload":"w","metric":"disk"}`, `{"kind":"network"}`} {
		if _, err := tool.Handler(context.Background(), json.RawMessage(bad)); err == nil {
			t.Errorf("hata bekleniyordu: %s", bad)
		}
	}
	out, err = tool.Handler(context.Background(), json.RawMessage(`{"kind":"pod","cluster":"c","namespace":"n","pod":"p","max_data_points":20}`))
	if err != nil {
		t.Fatal(err)
	}
	m := out.(map[string]any)
	if len(m["points"].([]thanos.TrendPoint)) != 20 || m["totalPoints"] != 200 || fk.calls[0] != "pod:c/n/p" {
		t.Fatalf("pod trend: %+v %v", m["totalPoints"], fk.calls)
	}
	out, err = tool.Handler(context.Background(), json.RawMessage(`{"kind":"deploy","cluster":"c","namespace":"n","workload":"w","by_pod":true}`))
	if err != nil {
		t.Fatal(err)
	}
	m = out.(map[string]any)
	if m["metric"] != "cpu" || m["totalSeries"] != 12 || len(m["series"].([]thanos.NamedSeries)[0].Points) != clusterMetricPoints {
		t.Fatalf("deploy trend: %+v", m)
	}
	if _, err := tool.Handler(context.Background(), json.RawMessage(`{"kind":"network","cluster":"prod-eu"}`)); err != nil || fk.calls[len(fk.calls)-1] != "net:prod-eu" {
		t.Fatalf("network: %v %v", err, fk.calls)
	}
}

func TestLogPatternsToolDisabledWithoutStore(t *testing.T) {
	tool := toolByName(t, ToolList(Deps{}), "log_patterns")
	out, err := tool.Handler(context.Background(), json.RawMessage(`{"window_s":300}`))
	if err != nil || out.(map[string]any)["disabled"] != true {
		t.Fatalf("log deposu yokken disabled: %v %v", out, err)
	}
	if _, err := tool.Handler(context.Background(), json.RawMessage(`{"kind":"weird"}`)); err == nil {
		t.Log("kind doğrulaması log deposu kontrolünden sonra — kabul (disabled önce döner)")
	}
}
