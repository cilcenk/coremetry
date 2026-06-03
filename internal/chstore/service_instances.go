package chstore

import (
	"context"
	"fmt"
	"strings"
	"time"
)

// ServiceInstance is one pod/host emitting telemetry for a service —
// the per-pod row in the Service Overview "Instances" card. Latest CPU /
// memory (and memory % when a limit is reported) plus a liveness flag,
// derived entirely from metric_points grouped by host_name. No raw-spans
// scan — invariant #3 stays intact (metric_points is small vs spans).
type ServiceInstance struct {
	ID       string  `json:"id"`        // host_name (pod identity)
	Zone     string  `json:"zone"`      // cloud.availability_zone / k8s zone res-attr, "" if absent
	CPUPct   float64 `json:"cpuPct"`    // 0-100 (utilization fraction × 100)
	MemBytes float64 `json:"memBytes"`  // latest RSS / used bytes
	MemPct   float64 `json:"memPct"`    // 0-100 when a memory limit is reported, else 0
	Up       bool    `json:"up"`        // saw a sample within the freshness window
	LastSeen int64   `json:"lastSeen"`  // unix ns of the most recent sample
}

// Per-pod source candidates — fraction-based CPU utilisation (×100 = %),
// used/RSS memory, and the memory limit when the runtime reports one
// (JVM does; Go's runtime does not, so MemPct stays 0 and the UI gauges
// memory relative to the busiest pod instead).
var (
	instCPUSources = []string{"jvm.cpu.recent_utilization", "process.runtime.cpu.utilization", "process.cpu.utilization"}
	instMemSources = []string{"jvm.memory.used", "process.runtime.memory.rss", "process.memory.usage"}
	instLimSources = []string{"jvm.memory.limit", "k8s.pod.memory.limit", "container.memory.limit"}
)

// ServiceInstances returns one row per host_name emitting metrics for the
// service in the window, with the latest CPU / memory per pod. ONE bounded
// metric_points query (service + time prefix prune, LowCardinality host_name
// group, capped rows + wall-clock).
func (s *Store) ServiceInstances(ctx context.Context, service string, from, to time.Time) ([]ServiceInstance, error) {
	if service == "" {
		return nil, fmt.Errorf("service required")
	}
	all := append(append(append([]string{}, instCPUSources...), instMemSources...), instLimSources...)
	holders := make([]string, len(all))
	args := []any{service, from, to}
	for i, n := range all {
		holders[i] = "?"
		args = append(args, n)
	}
	cpuIn := inList(instCPUSources)
	memIn := inList(instMemSources)
	limIn := inList(instLimSources)

	rows, err := s.conn.Query(ctx, `
		SELECT
		  host_name,
		  argMaxIf(value, time, metric IN (`+cpuIn+`)) AS cpu_raw,
		  argMaxIf(value, time, metric IN (`+memIn+`)) AS mem_raw,
		  argMaxIf(value, time, metric IN (`+limIn+`)) AS mem_lim,
		  anyLast(res_values[indexOf(res_keys, 'cloud.availability_zone')]) AS zone,
		  max(time) AS last_seen
		FROM metric_points
		WHERE service_name = ?
		  AND time >= ? AND time <= ?
		  AND host_name != ''
		  AND metric IN (`+strings.Join(holders, ",")+`)
		GROUP BY host_name
		ORDER BY cpu_raw DESC
		LIMIT 200
		SETTINGS max_execution_time = 10`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	fresh := to.Add(-2 * time.Minute)
	out := []ServiceInstance{}
	for rows.Next() {
		var (
			host     string
			cpuRaw   float64
			memRaw   float64
			memLim   float64
			zone     string
			lastSeen time.Time
		)
		if err := rows.Scan(&host, &cpuRaw, &memRaw, &memLim, &zone, &lastSeen); err != nil {
			return nil, err
		}
		inst := ServiceInstance{
			ID:       host,
			Zone:     zone,
			CPUPct:   clampPct(cpuRaw * 100),
			MemBytes: memRaw,
			Up:       lastSeen.After(fresh),
			LastSeen: lastSeen.UnixNano(),
		}
		if memLim > 0 && memRaw > 0 {
			inst.MemPct = clampPct(memRaw / memLim * 100)
		}
		out = append(out, inst)
	}
	return out, rows.Err()
}

// inList renders a SQL string-literal IN list from constant metric names
// (no untrusted input — these are the fixed source tables above).
func inList(names []string) string {
	q := make([]string, len(names))
	for i, n := range names {
		q[i] = "'" + n + "'"
	}
	return strings.Join(q, ",")
}

func clampPct(v float64) float64 {
	if v < 0 {
		return 0
	}
	if v > 100 {
		return 100
	}
	return v
}
