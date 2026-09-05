package vmetrics

// runtime_pods.go — v0.10.374 (VM dilim 3c): JVM pod reads through
// VictoriaMetrics for every consumer, not just the evaluator.
//
// v0.9.1213 gave the evaluator its own VM turn for GC alarms
// (evaluator/runtime_vm.go); the anomaly investigation's saturation
// evidence and the MCP pod-health tool kept calling *chstore.Store and
// came back empty on a VM-primary install. The pure pieces of that turn
// (pod identity from the group tuple, GC derivation, the sum/count
// join) now live here, exported; the evaluator delegates to them so the
// two paths cannot drift.
//
// Heap: the ClickHouse reader sums the heap pools per timestamp and
// averages over the window. PromQL: `sum by (svc, pod)` per minute
// bucket, averaged client-side — same quantity. The `jvm.memory.type =
// heap` filter also satisfies the bucket-family guard (avg is never
// asked unfiltered here anyway).

import (
	"context"
	"fmt"
	"sort"
	"time"

	"github.com/cilcenk/coremetry/internal/chstore"
)

// RuntimePodGroupBy — service first, then the pod identity candidates
// in the ClickHouse runtimePodExpr order (pod → host → instance id).
var RuntimePodGroupBy = []string{"service.name", "k8s.pod.name", "host.name", "service.instance.id"}

// PodFromTuple — the pod identity from a RuntimePodGroupBy tuple.
func PodFromTuple(groupKey []string) string {
	get := func(i int) string {
		if i < len(groupKey) {
			return groupKey[i]
		}
		return ""
	}
	if p := get(1); p != "" {
		return p
	}
	if h := get(2); h != "" {
		return h
	}
	if id := get(3); id != "" {
		r := []rune(id)
		if len(r) > 8 {
			return string(r[:8])
		}
		return id
	}
	return ""
}

// GCDerive — pause ms per collection, share of wall time, collections
// per minute from the window totals.
func GCDerive(sumSec, cnt, windowSec float64) (pauseMs, sharePct, ratePerMin float64) {
	if windowSec <= 0 || cnt <= 0 || sumSec < 0 {
		return 0, 0, 0
	}
	pauseMs = sumSec / cnt * 1000
	sharePct = sumSec / windowSec * 100
	ratePerMin = cnt / (windowSec / 60)
	return pauseMs, sharePct, ratePerMin
}

func seriesWindowTotal(s chstore.SpanMetricSeries) float64 {
	t := 0.0
	for _, p := range s.Points {
		t += p.Value
	}
	return t
}

// JoinGCSeries — the sum/count join keyed by (service, pod).
func JoinGCSeries(sums, cnts []chstore.SpanMetricSeries, windowSec float64) ([]chstore.CapacitySample, []chstore.GCActivitySample, error) {
	type key struct{ svc, pod string }
	cntBy := map[key]float64{}
	for _, s := range cnts {
		svc := ""
		if len(s.GroupKey) > 0 {
			svc = s.GroupKey[0]
		}
		pod := PodFromTuple(s.GroupKey)
		if svc == "" || pod == "" {
			continue
		}
		cntBy[key{svc, pod}] += seriesWindowTotal(s)
	}
	var pauses []chstore.CapacitySample
	var acts []chstore.GCActivitySample
	for _, s := range sums {
		svc := ""
		if len(s.GroupKey) > 0 {
			svc = s.GroupKey[0]
		}
		pod := PodFromTuple(s.GroupKey)
		if svc == "" || pod == "" {
			continue
		}
		cnt := cntBy[key{svc, pod}]
		pauseMs, sharePct, ratePerMin := GCDerive(seriesWindowTotal(s), cnt, windowSec)
		if cnt <= 0 {
			continue
		}
		pauses = append(pauses, chstore.CapacitySample{Instance: svc, Subkey: pod, Usage: pauseMs})
		acts = append(acts, chstore.GCActivitySample{Service: svc, Pod: pod, SharePct: sharePct, RatePerMin: ratePerMin})
	}
	return pauses, acts, nil
}

// JVMGCPodStats — GC pause + activity per (service, pod) over [from,to]
// from the jvm.gc.duration histogram's _sum / _count increase.
func (s *Service) JVMGCPodStats(ctx context.Context, from, to time.Time) ([]chstore.CapacitySample, []chstore.GCActivitySample, error) {
	win := to.Sub(from)
	if win <= 0 {
		return nil, nil, fmt.Errorf("vm gc: empty window")
	}
	base := chstore.MetricQueryFilter{
		GroupBy:       RuntimePodGroupBy,
		Aggregation:   "increase",
		From:          from,
		To:            to,
		StepSeconds:   int(win.Seconds()),
		MaxDataPoints: 2,
	}
	sumF := base
	sumF.Name = "jvm_gc_duration_seconds_sum"
	cntF := base
	cntF.Name = "jvm_gc_duration_seconds_count"
	sums, err := s.QueryMetric(ctx, sumF)
	if err != nil {
		return nil, nil, fmt.Errorf("vm gc sum: %w", err)
	}
	cnts, err := s.QueryMetric(ctx, cntF)
	if err != nil {
		return nil, nil, fmt.Errorf("vm gc count: %w", err)
	}
	return JoinGCSeries(sums, cnts, win.Seconds())
}

// JVMGCPodPause — chstore.RuntimePodReader: pause ms per (service, pod).
func (s *Service) JVMGCPodPause(ctx context.Context, from, to time.Time) ([]chstore.CapacitySample, error) {
	pauses, _, err := s.JVMGCPodStats(ctx, from, to)
	if err != nil {
		return nil, err
	}
	if pauses == nil {
		pauses = []chstore.CapacitySample{}
	}
	return pauses, nil
}

var heapTypeFilter = []chstore.FilterExpr{{Key: "jvm.memory.type", Op: "=", Values: []string{"heap"}}}

// heapAvgByPod — one heap metric: sum over pools per minute bucket,
// averaged over the buckets, keyed by (service, pod). `positiveOnly`
// mirrors the ClickHouse avgIf(postgc, postgc > 0).
func (s *Service) heapAvgByPod(ctx context.Context, metric string, from, to time.Time, positiveOnly bool) (map[[2]string]float64, error) {
	series, err := s.QueryMetric(ctx, chstore.MetricQueryFilter{
		Name:          metric,
		Aggregation:   "sum",
		GroupBy:       RuntimePodGroupBy,
		Filters:       heapTypeFilter,
		From:          from,
		To:            to,
		StepSeconds:   60,
		MaxDataPoints: int(to.Sub(from).Seconds())/60 + 2,
	})
	if err != nil {
		return nil, fmt.Errorf("vm heap %s: %w", metric, err)
	}
	out := map[[2]string]float64{}
	for _, ser := range series {
		svc := ""
		if len(ser.GroupKey) > 0 {
			svc = ser.GroupKey[0]
		}
		pod := PodFromTuple(ser.GroupKey)
		if svc == "" || pod == "" {
			continue
		}
		sum, n := 0.0, 0
		for _, p := range ser.Points {
			if positiveOnly && p.Value <= 0 {
				continue
			}
			sum += p.Value
			n++
		}
		if n > 0 {
			out[[2]string{svc, pod}] = sum / float64(n)
		}
	}
	return out, nil
}

// JVMHeapPodUsage — chstore.RuntimePodReader: heap used / limit /
// used-after-last-GC per (service, pod); rows without a positive limit
// are dropped like the ClickHouse HAVING lim > 0.
func (s *Service) JVMHeapPodUsage(ctx context.Context, from, to time.Time) ([]chstore.CapacitySample, error) {
	used, err := s.heapAvgByPod(ctx, "jvm.memory.used", from, to, false)
	if err != nil {
		return nil, err
	}
	lim, err := s.heapAvgByPod(ctx, "jvm.memory.limit", from, to, false)
	if err != nil {
		return nil, err
	}
	postgc, err := s.heapAvgByPod(ctx, "jvm.memory.used_after_last_gc", from, to, true)
	if err != nil {
		return nil, err
	}
	out := []chstore.CapacitySample{}
	for k, u := range used {
		l := lim[k]
		if l <= 0 {
			continue
		}
		out = append(out, chstore.CapacitySample{Instance: k[0], Subkey: k[1], Usage: u, Limit: l, PostGC: postgc[k]})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Instance != out[j].Instance {
			return out[i].Instance < out[j].Instance
		}
		return out[i].Subkey < out[j].Subkey
	})
	return out, nil
}

// runtimePodsOr — per-call resolver: VictoriaMetrics when configured,
// the fallback (ClickHouse store) otherwise. Decided at CALL time so a
// Settings change flips the source without a restart, like the
// evaluator's vmActive rule.
type runtimePodsOr struct {
	vm       *Service
	fallback chstore.RuntimePodReader
}

func (r runtimePodsOr) pick() chstore.RuntimePodReader {
	if r.vm != nil && r.vm.Configured() {
		return r.vm
	}
	return r.fallback
}
func (r runtimePodsOr) JVMHeapPodUsage(ctx context.Context, from, to time.Time) ([]chstore.CapacitySample, error) {
	return r.pick().JVMHeapPodUsage(ctx, from, to)
}
func (r runtimePodsOr) JVMGCPodPause(ctx context.Context, from, to time.Time) ([]chstore.CapacitySample, error) {
	return r.pick().JVMGCPodPause(ctx, from, to)
}

// RuntimePodsOr — the reader main.go hands to the anomaly worker and
// the MCP tools.
func RuntimePodsOr(vm *Service, fallback chstore.RuntimePodReader) chstore.RuntimePodReader {
	return runtimePodsOr{vm: vm, fallback: fallback}
}

var _ chstore.RuntimePodReader = (*Service)(nil)
