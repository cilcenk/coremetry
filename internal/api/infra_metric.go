package api

// v0.10.365 — service infra sparklines (cpu / memory / rps / runtime /
// heap) on the metricSource seam (VM dilim 3a).
//
// chstore.GetInfraMetrics is a fixed-name `metric_points` reader, so on
// a VictoriaMetrics-primary install the /service infra panel was empty.
// Same slot table (chstore.InfraSlotCatalog), same fallback order: the
// first candidate the backend knows AND that has points for the service
// fills the slot. ClickHouse keeps its single-query path.

import (
	"context"
	"fmt"
	"sort"
	"time"

	"github.com/cilcenk/coremetry/internal/chstore"
)

// infraMetricBucket — the ClickHouse reader's bucket rule (since /
// SparklineBuckets, floored at 10 s) so both backends draw the same
// number of points.
func infraMetricBucket(since time.Duration) time.Duration {
	b := since / chstore.SparklineBuckets
	if b < 10*time.Second {
		b = 10 * time.Second
	}
	return b
}

func buildInfraMetric(ctx context.Context, src metricSource, service string, since time.Duration, now time.Time) ([]chstore.InfraMetricSeries, error) {
	if service == "" {
		return nil, fmt.Errorf("service required")
	}
	if since <= 0 {
		since = 10 * time.Minute
	}
	bucket := infraMetricBucket(since)
	from := now.Add(-since)
	out := []chstore.InfraMetricSeries{}
	for _, sl := range chstore.InfraSlotCatalog() {
		for _, name := range sl.Sources {
			ok, err := src.MetricExists(ctx, name)
			if err != nil {
				return nil, err
			}
			if !ok {
				continue
			}
			series, err := src.QueryMetric(ctx, chstore.MetricQueryFilter{
				Name:          name,
				Service:       service,
				Aggregation:   "avg",
				From:          from,
				To:            now,
				StepSeconds:   int(bucket.Seconds()),
				MaxDataPoints: chstore.SparklineBuckets,
			})
			if err != nil {
				return nil, err
			}
			pts := flattenInfraPoints(series)
			if len(pts) == 0 {
				continue
			}
			out = append(out, chstore.InfraMetricSeries{Metric: sl.Slot, Source: name, Unit: sl.Unit, Points: pts})
			break
		}
	}
	return out, nil
}

// flattenInfraPoints — no GroupBy → one series per backend, but a
// backend may still split by an identity label; average per timestamp
// so the panel gets one line, like the ClickHouse `avg(value)`.
func flattenInfraPoints(series []chstore.SpanMetricSeries) []chstore.Point {
	if len(series) == 0 {
		return nil
	}
	if len(series) == 1 {
		pts := make([]chstore.Point, 0, len(series[0].Points))
		for _, p := range series[0].Points {
			pts = append(pts, chstore.Point{TimeNs: p.Time, Value: p.Value})
		}
		return pts
	}
	sum := map[int64]float64{}
	n := map[int64]int{}
	var order []int64
	for _, ser := range series {
		for _, p := range ser.Points {
			if _, ok := sum[p.Time]; !ok {
				order = append(order, p.Time)
			}
			sum[p.Time] += p.Value
			n[p.Time]++
		}
	}
	sort.Slice(order, func(i, j int) bool { return order[i] < order[j] })
	pts := make([]chstore.Point, 0, len(order))
	for _, t := range order {
		pts = append(pts, chstore.Point{TimeNs: t, Value: sum[t] / float64(n[t])})
	}
	return pts
}
