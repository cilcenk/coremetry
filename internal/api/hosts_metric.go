package api

// v0.10.365 — /hosts on the metricSource seam (VM dilim 3a).
//
// chstore.GetHosts / GetHostDetail are fixed-name readers pinned to
// ClickHouse `metric_points`; on a VictoriaMetrics-primary install the
// page came back EMPTY. The ClickHouse path is kept verbatim (one SQL
// round trip beats N seam calls — operator decision, spec §2); when the
// resolved source is VictoriaMetrics the same rows are assembled here
// from `QueryMetric(last, GroupBy host.name/service.name)` over the SAME
// candidate lists (chstore.Inst*Sources) so the two backends agree on
// which metric "is" CPU / memory / limit.
//
// Freshness: the ClickHouse reader knows every row's exact last sample
// time. Through the seam the resolution is one step, so `Up` = "last
// non-empty bucket is within 2×step of `to`" (spec open question 3,
// approved 2026-09-05). Step = window/60 floored at 60 s → ≤60 points
// per series regardless of window (window itself clamped to ≤6 h).

import (
	"context"
	"sort"
	"time"

	"github.com/cilcenk/coremetry/internal/chstore"
)

const (
	hostsMetricMaxPoints = 60
	hostsMetricRowCap    = 2000 // mirrors the ClickHouse LIMIT
	hostsDetailRowCap    = 100
	hostsServiceListCap  = 16 // mirrors groupUniqArray(16)
)

// hostsMetricStep — window/60 floored at 60 s, so a 6 h window costs 60
// points per series, a 15 min window costs 15.
func hostsMetricStep(from, to time.Time) int {
	sec := int(to.Sub(from).Seconds()) / hostsMetricMaxPoints
	if sec < 60 {
		sec = 60
	}
	return sec
}

type hostSample struct {
	host, service, zone string
	cpu, mem, lim       float64
	hasCPU, hasMem      bool
	hasLim              bool
	lastSeen            time.Time
}

// latestPoint — the last bucket of a series. VictoriaMetrics returns an
// empty vector for buckets without samples, so the last element IS the
// last observation (no NaN gaps to skip).
func latestPoint(ser chstore.SpanMetricSeries) (chstore.SpanMetricPoint, bool) {
	if len(ser.Points) == 0 {
		return chstore.SpanMetricPoint{}, false
	}
	return ser.Points[len(ser.Points)-1], true
}

// hostSeriesFor — one candidate metric through the seam; nil (not an
// error) when the backend has never seen the name, so callers walk the
// candidate list in order like the ClickHouse `metric IN (…)` chain.
func hostSeriesFor(ctx context.Context, src metricSource, name, agg string, groupBy []string, filters []chstore.FilterExpr, from, to time.Time, step int) ([]chstore.SpanMetricSeries, error) {
	ok, err := src.MetricExists(ctx, name)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, nil
	}
	return src.QueryMetric(ctx, chstore.MetricQueryFilter{
		Name:          name,
		Aggregation:   agg,
		GroupBy:       groupBy,
		Filters:       filters,
		From:          from,
		To:            to,
		StepSeconds:   step,
		MaxDataPoints: hostsMetricMaxPoints,
	})
}

func groupKeyAt(ser chstore.SpanMetricSeries, i int) string {
	if i < len(ser.GroupKey) {
		return ser.GroupKey[i]
	}
	return ""
}

// collectHostSamples walks a candidate list and fills one slot (cpu /
// mem / lim) per (host, service). First candidate WITH data wins for a
// given pair — the ClickHouse reader's argMaxIf over the IN-list picks
// the freshest sample across candidates instead; on a real install a
// pod emits exactly one of the candidates, so the two agree.
func collectHostSamples(ctx context.Context, src metricSource, names []string, groupBy []string, filters []chstore.FilterExpr, from, to time.Time, step int,
	samples map[[2]string]*hostSample, fill func(s *hostSample, ser chstore.SpanMetricSeries, p chstore.SpanMetricPoint)) error {
	filled := map[[2]string]bool{}
	for _, name := range names {
		series, err := hostSeriesFor(ctx, src, name, "last", groupBy, filters, from, to, step)
		if err != nil {
			return err
		}
		for _, ser := range series {
			p, ok := latestPoint(ser)
			if !ok {
				continue
			}
			key := [2]string{groupKeyAt(ser, 0), groupKeyAt(ser, 1)}
			if key[0] == "" || filled[key] {
				continue
			}
			filled[key] = true
			s := samples[key]
			if s == nil {
				s = &hostSample{host: key[0], service: key[1]}
				samples[key] = s
			}
			fill(s, ser, p)
			if t := time.Unix(0, p.Time); t.After(s.lastSeen) {
				s.lastSeen = t
			}
		}
	}
	return nil
}

var (
	hostGroupCPU = []string{"host.name", "service.name", "cloud.availability_zone"}
	hostGroupMem = []string{"host.name", "service.name"}
)

func fillCPU(s *hostSample, ser chstore.SpanMetricSeries, p chstore.SpanMetricPoint) {
	s.cpu, s.hasCPU = p.Value, true
	if s.zone == "" {
		s.zone = groupKeyAt(ser, 2)
	}
}
func fillMem(s *hostSample, _ chstore.SpanMetricSeries, p chstore.SpanMetricPoint) {
	s.mem, s.hasMem = p.Value, true
}
func fillLim(s *hostSample, _ chstore.SpanMetricSeries, p chstore.SpanMetricPoint) {
	s.lim, s.hasLim = p.Value, true
}

// buildHostsMetric — chstore.GetHosts through the seam.
func buildHostsMetric(ctx context.Context, src metricSource, from, to time.Time) ([]chstore.HostRow, error) {
	from, to = chstore.ClampHostWindow(from, to)
	step := hostsMetricStep(from, to)
	samples := map[[2]string]*hostSample{}
	if err := collectHostSamples(ctx, src, chstore.InstCPUSources, hostGroupCPU, nil, from, to, step, samples, fillCPU); err != nil {
		return nil, err
	}
	if err := collectHostSamples(ctx, src, chstore.InstMemSources, hostGroupMem, nil, from, to, step, samples, fillMem); err != nil {
		return nil, err
	}
	if err := collectHostSamples(ctx, src, chstore.InstLimSources, hostGroupMem, nil, from, to, step, samples, fillLim); err != nil {
		return nil, err
	}
	return foldHostRows(samples, to, step), nil
}

type hostAcc struct {
	row       chstore.HostRow
	cpuRaw    float64
	memCapped float64
	limSum    float64
	services  map[string]bool
	lastSeen  time.Time
}

// foldHostRows — the outer GROUP BY host_name of the ClickHouse query:
// sums across services, mem% only over services that report a limit,
// Up from the freshest bucket. Pure; the tests drive it directly.
func foldHostRows(samples map[[2]string]*hostSample, to time.Time, step int) []chstore.HostRow {
	fresh := to.Add(-2 * time.Duration(step) * time.Second)
	acc := map[string]*hostAcc{}
	for _, s := range samples {
		a := acc[s.host]
		if a == nil {
			a = &hostAcc{row: chstore.HostRow{Host: s.host}, services: map[string]bool{}}
			acc[s.host] = a
		}
		if s.hasCPU {
			a.cpuRaw += s.cpu
		}
		if s.hasMem {
			a.row.MemBytes += s.mem
			if s.hasLim && s.lim > 0 {
				a.memCapped += s.mem
			}
		}
		if s.hasLim {
			a.limSum += s.lim
		}
		if a.row.Zone == "" && s.zone != "" {
			a.row.Zone = s.zone
		}
		if s.service != "" {
			a.services[s.service] = true
		}
		if s.lastSeen.After(a.lastSeen) {
			a.lastSeen = s.lastSeen
		}
	}
	out := make([]chstore.HostRow, 0, len(acc))
	for _, a := range acc {
		r := a.row
		r.CPUPct = clampPct100(a.cpuRaw * 100)
		if a.limSum > 0 && a.memCapped > 0 {
			r.MemPct = clampPct100(a.memCapped / a.limSum * 100)
		}
		names := make([]string, 0, len(a.services))
		for n := range a.services {
			names = append(names, n)
		}
		sort.Strings(names)
		if len(names) > hostsServiceListCap {
			names = names[:hostsServiceListCap]
		}
		r.Services = names
		r.Up = a.lastSeen.After(fresh)
		r.LastSeen = a.lastSeen.UnixNano()
		out = append(out, r)
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].CPUPct != out[j].CPUPct {
			return out[i].CPUPct > out[j].CPUPct
		}
		return out[i].Host < out[j].Host
	})
	if len(out) > hostsMetricRowCap {
		out = out[:hostsMetricRowCap]
	}
	return out
}

func clampPct100(v float64) float64 {
	if v < 0 || v != v {
		return 0
	}
	if v > 100 {
		return 100
	}
	return v
}

var (
	detailGroupCPU = []string{"host.name", "service.name", "cloud.availability_zone"}
	detailGroupMem = []string{"host.name", "service.name"}
)

func hostFilter(host string) []chstore.FilterExpr {
	return []chstore.FilterExpr{{Key: "host.name", Op: "=", Values: []string{host}}}
}

// buildHostDetailMetric — chstore.GetHostDetail through the seam: latest
// per-service rows (LIMIT 100, cpu desc) + the summed minute trend via
// the SAME chstore.SumHostTrend carry logic the ClickHouse path uses.
func buildHostDetailMetric(ctx context.Context, src metricSource, host string, from, to time.Time) (*chstore.HostDetail, error) {
	from, to = chstore.ClampHostWindow(from, to)
	step := hostsMetricStep(from, to)
	flt := hostFilter(host)
	samples := map[[2]string]*hostSample{}
	if err := collectHostSamples(ctx, src, chstore.InstCPUSources, detailGroupCPU, flt, from, to, step, samples, fillCPU); err != nil {
		return nil, err
	}
	if err := collectHostSamples(ctx, src, chstore.InstMemSources, detailGroupMem, flt, from, to, step, samples, fillMem); err != nil {
		return nil, err
	}
	d := &chstore.HostDetail{Host: host, Services: []chstore.HostServiceRow{}}
	for _, s := range samples {
		if s.host != host {
			continue
		}
		r := chstore.HostServiceRow{Service: s.service, LastSeen: s.lastSeen.UnixNano()}
		if s.hasCPU {
			r.CPUPct = clampPct100(s.cpu * 100)
		}
		if s.hasMem {
			r.MemBytes = s.mem
		}
		if d.Zone == "" && s.zone != "" {
			d.Zone = s.zone
		}
		d.Services = append(d.Services, r)
	}
	sort.SliceStable(d.Services, func(i, j int) bool {
		if d.Services[i].CPUPct != d.Services[j].CPUPct {
			return d.Services[i].CPUPct > d.Services[j].CPUPct
		}
		return d.Services[i].Service < d.Services[j].Service
	})
	if len(d.Services) > hostsDetailRowCap {
		d.Services = d.Services[:hostsDetailRowCap]
	}
	trend, err := hostTrendSamples(ctx, src, host, from, to)
	if err != nil {
		return nil, err
	}
	d.Trend = chstore.SumHostTrend(trend)
	return d, nil
}

// hostTrendSamples — per-service minute buckets: cpu = avg over the
// minute, mem = last in the minute (the ClickHouse trend's avgIf /
// argMaxIf pair). v0.10.504 (dış denetim A6) — VictoriaMetrics'in kova-
// sonu damgası artık decode'da başlangıca çevriliyor (vmetrics
// bucketStartNs); buradaki v0.10.337 `t − step` telafisi kalktı — iki
// kaynak aynı damgayı taşır.
func hostTrendSamples(ctx context.Context, src metricSource, host string, from, to time.Time) ([]chstore.HostTrendSample, error) {
	const step = 60
	flt := hostFilter(host)
	minuteOf := func(p chstore.SpanMetricPoint) int64 {
		return p.Time / int64(time.Minute)
	}
	byKey := map[[2]any]*chstore.HostTrendSample{}
	var order [][2]any
	get := func(svc string, min int64) *chstore.HostTrendSample {
		k := [2]any{svc, min}
		s := byKey[k]
		if s == nil {
			s = &chstore.HostTrendSample{Service: svc, Minute: min}
			byKey[k] = s
			order = append(order, k)
		}
		return s
	}
	walk := func(names []string, agg string, apply func(s *chstore.HostTrendSample, v float64)) error {
		seen := map[string]bool{}
		for _, name := range names {
			series, err := hostSeriesFor(ctx, src, name, agg, []string{"service.name"}, flt, from, to, step)
			if err != nil {
				return err
			}
			for _, ser := range series {
				svc := groupKeyAt(ser, 0)
				if seen[svc] {
					continue
				}
				if len(ser.Points) > 0 {
					seen[svc] = true
				}
				for _, p := range ser.Points {
					apply(get(svc, minuteOf(p)), p.Value)
				}
			}
		}
		return nil
	}
	if err := walk(chstore.InstCPUSources, "avg", func(s *chstore.HostTrendSample, v float64) { s.CPU, s.HasCPU = v, true }); err != nil {
		return nil, err
	}
	if err := walk(chstore.InstMemSources, "last", func(s *chstore.HostTrendSample, v float64) { s.Mem, s.HasMem = v, true }); err != nil {
		return nil, err
	}
	out := make([]chstore.HostTrendSample, 0, len(order))
	for _, k := range order {
		out = append(out, *byKey[k])
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Minute < out[j].Minute })
	return out, nil
}
