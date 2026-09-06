package vmetrics

// v0.10.366 — DB capacity reads through VictoriaMetrics (VM dilim 3b-1).
//
// chstore.Store answers UsageLimit / DimensionedUsageLimit / RateGauge /
// UsageTrend from `metric_points` with a coalesced instance identity
// (`instance` attribute → `service.name` resource → service_name column).
// A PromQL backend cannot coalesce inside GROUP BY, so the reader groups
// by every candidate label and coalesces the tuple client-side in the
// SAME order (capacityInstanceFromTuple). `exported_instance` sits in
// the chain because Prometheus-flavoured ingestion renames an incoming
// `instance` label when the scrape target already owns one.
//
// Windows and rounding mirror the ClickHouse reader: 10 min freshness,
// 5 min trend buckets, per-second rate over the window, positive limit
// required, empty subkey dropped. Steps are whole windows (one bucket)
// so a read is one small range query per metric.

import (
	"context"
	"fmt"
	"sort"
	"time"

	"github.com/cilcenk/coremetry/internal/chstore"
)

var capacityInstanceLabels = []string{"instance", "exported_instance", "service.name"}

// capacityInstanceFromTuple — first non-empty of the instance
// candidates; the tuple is laid out as capacityInstanceLabels followed
// by the optional dimension key.
func capacityInstanceFromTuple(groupKey []string) string {
	for i := range capacityInstanceLabels {
		if i < len(groupKey) && groupKey[i] != "" {
			return groupKey[i]
		}
	}
	return ""
}

func capacitySubkeyFromTuple(groupKey []string) string {
	if i := len(capacityInstanceLabels); i < len(groupKey) {
		return groupKey[i]
	}
	return ""
}

func capacityGroupBy(attrKey string) []string {
	g := append([]string{}, capacityInstanceLabels...)
	if attrKey != "" {
		g = append(g, attrKey)
	}
	return g
}

// lastValue — the newest bucket of a series. VictoriaMetrics returns no
// entry for empty buckets, so the last element is the last observation.
func lastValue(s chstore.SpanMetricSeries) (float64, bool) {
	if len(s.Points) == 0 {
		return 0, false
	}
	return s.Points[len(s.Points)-1].Value, true
}

// latestByKey — one QueryMetric(last) over the freshness window, keyed
// by (instance, subkey).
func (s *Service) latestByKey(ctx context.Context, metric, attrKey string, now time.Time) (map[[2]string]float64, error) {
	series, err := s.QueryMetric(ctx, chstore.MetricQueryFilter{
		Name:          metric,
		Aggregation:   "last",
		GroupBy:       capacityGroupBy(attrKey),
		From:          now.Add(-chstore.CapacityWindow),
		To:            now,
		StepSeconds:   int(chstore.CapacityWindow.Seconds()),
		MaxDataPoints: 2,
	})
	if err != nil {
		return nil, fmt.Errorf("vm capacity %s: %w", metric, err)
	}
	out := map[[2]string]float64{}
	for _, ser := range series {
		inst := capacityInstanceFromTuple(ser.GroupKey)
		if inst == "" {
			continue
		}
		v, ok := lastValue(ser)
		if !ok {
			continue
		}
		out[[2]string{inst, capacitySubkeyFromTuple(ser.GroupKey)}] = v
	}
	return out, nil
}

func (s *Service) usageLimit(ctx context.Context, usageMetric, limitMetric, attrKey string) ([]chstore.CapacitySample, error) {
	now := time.Now()
	usage, err := s.latestByKey(ctx, usageMetric, attrKey, now)
	if err != nil {
		return nil, err
	}
	limit, err := s.latestByKey(ctx, limitMetric, attrKey, now)
	if err != nil {
		return nil, err
	}
	out := []chstore.CapacitySample{}
	for key, u := range usage {
		lim := limit[key]
		if lim <= 0 || (attrKey != "" && key[1] == "") {
			continue
		}
		out = append(out, chstore.CapacitySample{Instance: key[0], Subkey: key[1], Usage: u, Limit: lim})
	}
	sortCapacitySamples(out)
	return out, nil
}

// UsageLimit — chstore.Store.UsageLimit through VictoriaMetrics.
func (s *Service) UsageLimit(ctx context.Context, usageMetric, limitMetric string) ([]chstore.CapacitySample, error) {
	return s.usageLimit(ctx, usageMetric, limitMetric, "")
}

// DimensionedUsageLimit — per (instance, attrKey value) pair.
func (s *Service) DimensionedUsageLimit(ctx context.Context, usageMetric, limitMetric, attrKey string) ([]chstore.CapacitySample, error) {
	return s.usageLimit(ctx, usageMetric, limitMetric, attrKey)
}

// RateGauge — per-second rate of a cumulative counter over the
// freshness window: increase(m[10m]) / 600 s. The ClickHouse reader
// divides (max−min) by the OBSERVED span; PromQL's increase already
// extrapolates to the window, so dividing by the window is the same
// quantity. Negative (reset) clamps to 0 like the ClickHouse path.
func (s *Service) RateGauge(ctx context.Context, metric string) ([]chstore.CapacitySample, error) {
	now := time.Now()
	win := chstore.CapacityWindow
	series, err := s.QueryMetricRate(ctx, chstore.MetricQueryFilter{
		Name:          metric,
		GroupBy:       capacityGroupBy(""),
		From:          now.Add(-win),
		To:            now,
		StepSeconds:   int(win.Seconds()),
		MaxDataPoints: 2,
	}, "increase")
	if err != nil {
		return nil, fmt.Errorf("vm capacity rate %s: %w", metric, err)
	}
	out := []chstore.CapacitySample{}
	for _, ser := range series {
		inst := capacityInstanceFromTuple(ser.GroupKey)
		if inst == "" {
			continue
		}
		inc, ok := lastValue(ser)
		if !ok {
			continue
		}
		rate := inc / win.Seconds()
		if rate < 0 {
			rate = 0
		}
		out = append(out, chstore.CapacitySample{Instance: inst, Usage: rate})
	}
	sortCapacitySamples(out)
	return out, nil
}

const capacityTrendStepSec = 300

// UsageTrend — 5 min buckets per (instance[, subkey]) ascending in
// time, keyed with chstore.CapacityTrendKey so the evaluator's ETA
// regression reads either backend identically. VictoriaMetrics stamps
// a bucket at its END; the ClickHouse reader at its START
// (toStartOfFiveMinutes) — shifted here so a mixed history never shows
// a 5 min jump.
//
// Bucket op is MAX, not the ClickHouse reader's avg: an unfiltered avg
// is refused by the bucket-family guard (buildPromQL cannot know a
// fill-level gauge from a histogram base name, and a decorative
// `instance=~".*"` matcher would disarm the guard for everyone). For a
// slowly filling gauge the 5 min max tracks the 5 min mean within the
// sampling interval; the slope the ETA fits is the same line.
func (s *Service) UsageTrend(ctx context.Context, usageMetric, attrKey string, window time.Duration) (map[string][]chstore.CapacityTrendPoint, error) {
	now := time.Now()
	series, err := s.QueryMetric(ctx, chstore.MetricQueryFilter{
		Name:          usageMetric,
		Aggregation:   "max",
		GroupBy:       capacityGroupBy(attrKey),
		From:          now.Add(-window),
		To:            now,
		StepSeconds:   capacityTrendStepSec,
		MaxDataPoints: int(window.Seconds())/capacityTrendStepSec + 2,
	})
	if err != nil {
		return nil, fmt.Errorf("vm capacity trend %s: %w", usageMetric, err)
	}
	out := map[string][]chstore.CapacityTrendPoint{}
	for _, ser := range series {
		inst := capacityInstanceFromTuple(ser.GroupKey)
		if inst == "" {
			continue
		}
		k := chstore.CapacityTrendKey(inst, capacitySubkeyFromTuple(ser.GroupKey))
		for _, p := range ser.Points {
			out[k] = append(out[k], chstore.CapacityTrendPoint{
				// v0.10.504 (A6) — kova başlangıcı artık decode'da
				// (bucketStartNs); buradaki `− capacityTrendStepSec` telafisi
				// kalktı (promStep adımı genişlettiğinde yanlıştı da).
				TSec:  p.Time / int64(time.Second),
				Usage: p.Value,
			})
		}
	}
	for k := range out {
		pts := out[k]
		sort.Slice(pts, func(i, j int) bool { return pts[i].TSec < pts[j].TSec })
		out[k] = pts
	}
	return out, nil
}

func sortCapacitySamples(out []chstore.CapacitySample) {
	sort.Slice(out, func(i, j int) bool {
		if out[i].Instance != out[j].Instance {
			return out[i].Instance < out[j].Instance
		}
		return out[i].Subkey < out[j].Subkey
	})
}

// compile-time: the VM service satisfies the evaluator's reader.
var _ chstore.CapacityReader = (*Service)(nil)
