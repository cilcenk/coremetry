package chstore

import (
	"context"
	"fmt"
	"strings"
	"time"
)

// InfraMetricSeries is one named timeseries for the infra
// correlation panel on /service?name=…. Lightweight on purpose:
// a flat Points array per metric, no labels — one bucket per
// (service, metric, time-bucket). The frontend renders sparklines.
type InfraMetricSeries struct {
	Metric string  `json:"metric"`     // canonical key, e.g. "cpu" / "memory" / "rps"
	Source string  `json:"source"`     // raw OTel metric name, e.g. "process.runtime.cpu.utilization"
	Unit   string  `json:"unit"`       // "%", "bytes", "/s", …
	Points []Point `json:"points"`
}

type Point struct {
	TimeNs int64   `json:"t"`
	Value  float64 `json:"v"`
}

// curatedInfraMetrics is the canonical list the panel surfaces,
// grouped by SRE-meaningful slot (cpu / memory / rps / runtime).
// Each slot picks the FIRST source that has data for the service —
// e.g. a Java service uses jvm.cpu.recent_utilization for cpu while
// a Go service uses process.runtime.cpu.utilization. The slot
// ordering matters: more specific first, generic fallback last.
//
// Adding a new runtime is just appending to the list; the
// detector picks whatever the service actually emits.
type infraSlot struct {
	Slot    string   // "cpu" | "memory" | "rps" | "runtime"
	Sources []string // metric_name candidates in fallback order
	Unit    string
}

var infraSlots = []infraSlot{
	{"cpu", []string{
		"k8s.pod.cpu.usage",
		"container.cpu.usage",
		"jvm.cpu.recent_utilization",
		"process.runtime.cpu.utilization",
		"process.cpu.utilization",
	}, "%"},
	{"memory", []string{
		"k8s.pod.memory.working_set",
		"container.memory.usage",
		"jvm.memory.used",
		"process.runtime.memory.rss",
		"process.memory.usage",
	}, "bytes"},
	{"rps", []string{
		"http.server.requests",
		"http.server.request.count",
		"http.server.active_requests",
	}, "/s"},
	{"runtime", []string{
		"process.runtime.goroutines",
		"jvm.thread.count",
		"jvm.gc.duration",
	}, ""},
	// Heap is intentionally separate from "memory" (which is RSS /
	// pod working set). Each runtime has its own canonical heap
	// metric — the slot lists them in priority order so the panel
	// surfaces whichever the service actually emits without
	// requiring a runtime hint from the operator.
	//   Java semconv 1.27+ → jvm.memory.heap.used
	//   Java legacy        → process.runtime.jvm.memory.usage
	//   Go runtime         → process.runtime.go.mem.heap_alloc
	//   .NET runtime       → process.runtime.dotnet.gc.heap.size
	//   Node.js runtime    → process.runtime.nodejs.memory.heap.used
	{"heap", []string{
		"jvm.memory.heap.used",
		"process.runtime.jvm.memory.usage",
		"process.runtime.go.mem.heap_alloc",
		"process.runtime.dotnet.gc.heap.size",
		"process.runtime.nodejs.memory.heap.used",
	}, "bytes"},
}

// GetInfraMetrics returns the curated set of timeseries for one
// service over the requested window. One CH query that:
//   - Filters by service_name + the union of all candidate metric
//     names (LowCardinality + primary key prefix → granule prune).
//   - Buckets time by `bucket` so the frontend gets a fixed-size
//     sparkline regardless of point density.
//   - Picks the single most-specific source per slot via a
//     priority-ordered IN list — the per-row `metric` is then
//     stitched back to its slot in Go.
func (s *Store) GetInfraMetrics(ctx context.Context, service string, since, bucket time.Duration) ([]InfraMetricSeries, error) {
	if service == "" {
		return nil, fmt.Errorf("service required")
	}
	if since <= 0 {
		since = 10 * time.Minute
	}
	if bucket <= 0 {
		// Auto-scale: ~30 buckets across the window so sparklines
		// stay legible without overwhelming payload.
		bucket = since / 30
		if bucket < 10*time.Second {
			bucket = 10 * time.Second
		}
	}

	// Flat list of every candidate metric name across slots.
	allNames := []string{}
	for _, sl := range infraSlots {
		allNames = append(allNames, sl.Sources...)
	}
	// SQL IN (?, ?, …) literal — names are constants, no untrusted input.
	holders := make([]string, len(allNames))
	args := []any{service, time.Now().Add(-since)}
	for i, n := range allNames {
		holders[i] = "?"
		args = append(args, n)
	}

	rows, err := s.conn.Query(ctx, `
		SELECT
		  metric,
		  toStartOfInterval(time, INTERVAL `+fmt.Sprintf("%d SECOND", int(bucket.Seconds()))+`) AS bucket,
		  avg(value) AS v
		FROM metric_points
		WHERE service_name = ?
		  AND time >= ?
		  AND metric IN (`+strings.Join(holders, ",")+`)
		GROUP BY metric, bucket
		ORDER BY metric, bucket
		SETTINGS `+s.shardSkipSetting(), args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	bySource := map[string][]Point{}
	for rows.Next() {
		var (
			m  string
			b  time.Time
			v  float64
		)
		if err := rows.Scan(&m, &b, &v); err != nil {
			return nil, err
		}
		bySource[m] = append(bySource[m], Point{TimeNs: b.UnixNano(), Value: v})
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	// Stitch sources to slots via priority order — first source
	// that returned data wins. Caller gets at most one series per
	// canonical slot.
	out := []InfraMetricSeries{}
	for _, sl := range infraSlots {
		for _, src := range sl.Sources {
			if pts, ok := bySource[src]; ok && len(pts) > 0 {
				out = append(out, InfraMetricSeries{
					Metric: sl.Slot,
					Source: src,
					Unit:   sl.Unit,
					Points: pts,
				})
				break
			}
		}
	}
	return out, nil
}
