package chstore

// v0.8.449 — /hosts inventory read layer (SigNoz/Uptrace gap-closure
// Wave 3 / A4). The global sibling of ServiceInstances
// (service_instances.go): one row per host/pod emitting metrics in the
// window, with the latest CPU / memory and which services run there.
// Same metric-source coalesce chains, same argMaxIf idiom — no
// raw-spans scan.
//
// Scale note: unlike ServiceInstances this query carries no
// service_name prefix, so it scans the window's metric rows for the
// fixed source-metric set. Guards: window clamped to ≤6h, LIMIT +
// max_execution_time, 60s server cache. If prod p99 breaches the
// budget the read promotes to a host_summary_5m MV as its own release
// (measure first — the doorway lesson).

import (
	"context"
	"strings"
	"time"
)

// HostRow is one row of the /hosts overview.
type HostRow struct {
	Host     string   `json:"host"`
	Zone     string   `json:"zone,omitempty"`
	Services []string `json:"services"`
	CPUPct   float64  `json:"cpuPct"`
	MemBytes float64  `json:"memBytes"`
	MemPct   float64  `json:"memPct"` // 0 when no limit is reported
	Up       bool     `json:"up"`
	LastSeen int64    `json:"lastSeen"` // unix ns
}

// HostServiceRow is the per-service breakdown in the host drawer.
type HostServiceRow struct {
	Service  string  `json:"service"`
	CPUPct   float64 `json:"cpuPct"`
	MemBytes float64 `json:"memBytes"`
	LastSeen int64   `json:"lastSeen"`
}

// HostTrendPoint is one minute of the host's CPU/memory trend; CPU is
// the sum of per-service utilisation on the host (≈ pod CPU), memory
// the sum of per-service latest RSS in that minute.
type HostTrendPoint struct {
	Bucket   int64   `json:"bucket"` // unix seconds
	CPUPct   float64 `json:"cpuPct"`
	MemBytes float64 `json:"memBytes"`
}

// HostDetail is the drawer payload.
type HostDetail struct {
	Host     string           `json:"host"`
	Zone     string           `json:"zone,omitempty"`
	Services []HostServiceRow `json:"services"`
	Trend    []HostTrendPoint `json:"trend"`
}

// clampHostWindow bounds the scan the page can trigger — the
// inventory question is "what runs where NOW", not archaeology.
func clampHostWindow(from, to time.Time) (time.Time, time.Time) {
	if to.IsZero() {
		to = time.Now()
	}
	if lo := to.Add(-6 * time.Hour); from.Before(lo) {
		from = lo
	}
	return from, to
}

// hostMetricArgs renders the shared "metric IN (…)" holder list +
// args for the union of CPU/mem/limit source metrics.
func hostMetricArgs() (string, []any) {
	all := append(append(append([]string{}, instCPUSources...), instMemSources...), instLimSources...)
	holders := make([]string, len(all))
	args := make([]any, len(all))
	for i, n := range all {
		holders[i] = "?"
		args[i] = n
	}
	return strings.Join(holders, ","), args
}

// GetHosts returns every host/pod seen in the (clamped) window,
// busiest CPU first.
//
// v0.8.449 review-fix: the aggregation is two-pass — the inner query
// picks each SERVICE's latest cpu/mem/limit on the host, the outer
// sums across services. A flat argMaxIf per host (the ServiceInstances
// idiom) is only correct when scoped to one service; host-wide it
// returns whichever service exported last, so multi-service hosts
// flip-flopped between services' values and MemPct could divide
// service A's usage by service B's limit. The pct now pairs each
// service's usage with its OWN limit: only limit-reporting services
// count in the numerator.
func (s *Store) GetHosts(ctx context.Context, from, to time.Time) ([]HostRow, error) {
	from, to = clampHostWindow(from, to)
	holders, metricArgs := hostMetricArgs()
	args := append([]any{from, to}, metricArgs...)

	rows, err := s.conn.Query(ctx, `
		SELECT
		  host_name,
		  sum(cpu_s)              AS cpu_raw,
		  sum(mem_s)              AS mem_raw,
		  sumIf(mem_s, lim_s > 0) AS mem_capped,
		  sum(lim_s)              AS lim_sum,
		  anyLast(zone_s)         AS zone,
		  arraySort(groupUniqArray(16)(service_name)) AS services,
		  max(last_seen_s)        AS last_seen
		FROM (
		  SELECT
		    host_name, service_name,
		    argMaxIf(value, time, metric IN (`+inList(instCPUSources)+`)) AS cpu_s,
		    argMaxIf(value, time, metric IN (`+inList(instMemSources)+`)) AS mem_s,
		    argMaxIf(value, time, metric IN (`+inList(instLimSources)+`)) AS lim_s,
		    anyLast(res_values[indexOf(res_keys, 'cloud.availability_zone')]) AS zone_s,
		    max(time) AS last_seen_s
		  FROM metric_points
		  WHERE time >= ? AND time <= ?
		    AND host_name != ''
		    AND metric IN (`+holders+`)
		  GROUP BY host_name, service_name
		)
		GROUP BY host_name
		ORDER BY cpu_raw DESC
		LIMIT 2000
		SETTINGS max_execution_time = 10`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	fresh := to.Add(-2 * time.Minute)
	out := []HostRow{}
	for rows.Next() {
		var (
			h         HostRow
			cpuRaw    float64
			memCapped float64
			limSum    float64
			lastSeen  time.Time
		)
		if err := rows.Scan(&h.Host, &cpuRaw, &h.MemBytes, &memCapped, &limSum,
			&h.Zone, &h.Services, &lastSeen); err != nil {
			return nil, err
		}
		h.CPUPct = clampPct(cpuRaw * 100)
		if limSum > 0 && memCapped > 0 {
			h.MemPct = clampPct(memCapped / limSum * 100)
		}
		h.Up = lastSeen.After(fresh)
		h.LastSeen = lastSeen.UnixNano()
		out = append(out, h)
	}
	return out, rows.Err()
}

// GetHostDetail returns the drawer payload for one host: per-service
// breakdown + per-minute CPU/mem trend. Both queries carry the
// host_name filter, so they touch a sliver of the window.
func (s *Store) GetHostDetail(ctx context.Context, host string, from, to time.Time) (*HostDetail, error) {
	from, to = clampHostWindow(from, to)
	holders, metricArgs := hostMetricArgs()
	d := &HostDetail{Host: host}

	args := append([]any{host, from, to}, metricArgs...)
	rows, err := s.conn.Query(ctx, `
		SELECT
		  service_name,
		  argMaxIf(value, time, metric IN (`+inList(instCPUSources)+`)) AS cpu_raw,
		  argMaxIf(value, time, metric IN (`+inList(instMemSources)+`)) AS mem_raw,
		  anyLast(res_values[indexOf(res_keys, 'cloud.availability_zone')]) AS zone,
		  max(time) AS last_seen
		FROM metric_points
		WHERE host_name = ?
		  AND time >= ? AND time <= ?
		  AND metric IN (`+holders+`)
		GROUP BY service_name
		ORDER BY cpu_raw DESC
		LIMIT 100
		SETTINGS max_execution_time = 10`, args...)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var (
			r        HostServiceRow
			cpuRaw   float64
			zone     string
			lastSeen time.Time
		)
		if err := rows.Scan(&r.Service, &cpuRaw, &r.MemBytes, &zone, &lastSeen); err != nil {
			rows.Close()
			return nil, err
		}
		r.CPUPct = clampPct(cpuRaw * 100)
		r.LastSeen = lastSeen.UnixNano()
		if d.Zone == "" && zone != "" {
			d.Zone = zone
		}
		d.Services = append(d.Services, r)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if d.Services == nil {
		d.Services = []HostServiceRow{}
	}

	// Per-minute trend. v0.8.449 review-fix: the cross-service sum
	// happens in Go (sumHostTrend), not SQL — CPU/RSS are GAUGES, and
	// a (service, minute) bucket that merely missed a sample (60s
	// export aralığının dakika sınırı jitter'ı) SQL toplamında 0
	// sayılıp sahte testere-dişi düşüşler çiziyordu. Go tarafı boş
	// dakikalarda servisin son değerini ≤3 dk taşır (sonrası: servis
	// gerçekten gitti, katkısı düşer).
	targs := append([]any{host, from, to}, metricArgs...)
	trows, err := s.conn.Query(ctx, `
		SELECT service_name,
		       toStartOfMinute(time) AS b,
		       avgIf(value, metric IN (`+inList(instCPUSources)+`)) AS cpu_s,
		       countIf(metric IN (`+inList(instCPUSources)+`)) AS cpu_n,
		       argMaxIf(value, time, metric IN (`+inList(instMemSources)+`)) AS mem_s,
		       countIf(metric IN (`+inList(instMemSources)+`)) AS mem_n
		FROM metric_points
		WHERE host_name = ?
		  AND time >= ? AND time <= ?
		  AND metric IN (`+holders+`)
		GROUP BY service_name, b
		ORDER BY b ASC
		LIMIT 5000
		SETTINGS max_execution_time = 10`, targs...)
	if err != nil {
		return nil, err
	}
	defer trows.Close()
	var samples []hostTrendSample
	for trows.Next() {
		var (
			sm     hostTrendSample
			bucket time.Time
			cpuN   uint64
			memN   uint64
		)
		if err := trows.Scan(&sm.Service, &bucket, &sm.CPU, &cpuN, &sm.Mem, &memN); err != nil {
			return nil, err
		}
		sm.Minute = bucket.Unix() / 60
		sm.HasCPU, sm.HasMem = cpuN > 0, memN > 0
		samples = append(samples, sm)
	}
	if err := trows.Err(); err != nil {
		return nil, err
	}
	d.Trend = sumHostTrend(samples)
	return d, nil
}

// hostTrendSample is one (service, minute) reading from the drawer
// trend query. HasCPU/HasMem distinguish "no sample this minute" from
// a genuine zero (avgIf yields nan, argMaxIf yields 0 on no match).
type hostTrendSample struct {
	Service string
	Minute  int64 // unix minutes
	CPU     float64
	HasCPU  bool
	Mem     float64
	HasMem  bool
}

// trendCarryMinutes — how long a service's last gauge value survives a
// sample gap before the service stops contributing (it's genuinely
// gone, not jittered). One 60s export interval + slack.
const trendCarryMinutes = 3

// sumHostTrend sums per-service gauge series into the host trend,
// forward-filling per-service gaps ≤ trendCarryMinutes. Pure —
// table-tested in hosts_trend_test.go (v0.8.449 review-fix: SQL-side
// summing counted missing gauge samples as 0 → false sawtooth dips).
func sumHostTrend(samples []hostTrendSample) []HostTrendPoint {
	if len(samples) == 0 {
		return []HostTrendPoint{}
	}
	lo, hi := samples[0].Minute, samples[0].Minute
	perSvc := map[string]map[int64]hostTrendSample{}
	for _, sm := range samples {
		if sm.Minute < lo {
			lo = sm.Minute
		}
		if sm.Minute > hi {
			hi = sm.Minute
		}
		m := perSvc[sm.Service]
		if m == nil {
			m = map[int64]hostTrendSample{}
			perSvc[sm.Service] = m
		}
		m[sm.Minute] = sm
	}

	type carry struct {
		cpu, mem           float64
		cpuSeen, memSeen   int64 // minute of the last real sample
		hasCPU, hasMem     bool
	}
	state := map[string]*carry{}
	out := make([]HostTrendPoint, 0, hi-lo+1)
	for min := lo; min <= hi; min++ {
		var cpu, mem float64
		var any bool
		for svc, series := range perSvc {
			c := state[svc]
			if c == nil {
				c = &carry{}
				state[svc] = c
			}
			if sm, ok := series[min]; ok {
				if sm.HasCPU && !isNaN(sm.CPU) {
					c.cpu, c.cpuSeen, c.hasCPU = sm.CPU, min, true
				}
				if sm.HasMem {
					c.mem, c.memSeen, c.hasMem = sm.Mem, min, true
				}
			}
			if c.hasCPU && min-c.cpuSeen <= trendCarryMinutes {
				cpu += c.cpu
				any = true
			}
			if c.hasMem && min-c.memSeen <= trendCarryMinutes {
				mem += c.mem
				any = true
			}
		}
		if !any {
			continue // no live series this minute — skip, don't draw 0
		}
		out = append(out, HostTrendPoint{Bucket: min * 60, CPUPct: cpu * 100, MemBytes: mem})
	}
	return out
}

func isNaN(f float64) bool { return f != f }
