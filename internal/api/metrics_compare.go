package api

import (
	"context"
	"fmt"
	"math"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/cilcenk/coremetry/internal/chstore"
)

// metrics_compare.go — v0.10.294 (docs/audit/vm-metrics-migration.md
// Dilim 1c, Aşama 2 "sorgu bazında doğrulama"): aynı MetricQueryFilter'ı
// ClickHouse ve VictoriaMetrics kaynaklarında koşturup nokta-nokta kıyaslar.
//
// Saf çekirdek (compareMetricSeries) Server'sız tablo-testli; sapma sınıfı
// cmd/paritycheck'in üç sınıfıyla AYNI eşikte (oransal ≤ 1e-9 → tolere):
//   identical  — her nokta bire bir
//   tolerated  — kayan nokta gürültüsü (rel ≤ 1e-9)
//   mismatch   — değer sapması, ya da seri/zaman yalnız bir kaynakta
//
// Kafes hizası ayrı raporlanır (onlyA/onlyB zaman damgaları): iki kaynak
// farklı step'e otursa değerler eşit olsa da "matched" düşer — bu bir
// doğruluk değil kafes sorunudur ve raporda öyle görünür.

const metricCompareTolerance = 1e-9

type metricCompareSeries struct {
	GroupKey          []string `json:"groupKey"`
	Class             string   `json:"class"`
	Points            int      `json:"points"`
	OnlyA             int      `json:"onlyA"`
	OnlyB             int      `json:"onlyB"`
	Mismatches        int      `json:"mismatches"`
	MaxAbs            float64  `json:"maxAbs"`
	MaxRel            float64  `json:"maxRel"`
	FirstMismatchTime int64    `json:"firstMismatchTime,omitempty"`
}

type metricCompareReport struct {
	A         string                `json:"a"`
	B         string                `json:"b"`
	Class     string                `json:"class"`
	Tolerance float64               `json:"tolerance"`
	SeriesA   int                   `json:"seriesA"`
	SeriesB   int                   `json:"seriesB"`
	Matched   int                   `json:"matched"`
	Series    []metricCompareSeries `json:"series"`
	NoteA     string                `json:"noteA,omitempty"`
	NoteB     string                `json:"noteB,omitempty"`
}

func classRank(c string) int {
	switch c {
	case "identical":
		return 0
	case "tolerated":
		return 1
	}
	return 2
}

func worstClass(a, b string) string {
	if classRank(b) > classRank(a) {
		return b
	}
	return a
}

func relDiff(a, b float64) float64 {
	if a == b {
		return 0
	}
	if (math.IsNaN(a) && math.IsNaN(b)) || (math.IsInf(a, 0) && math.IsInf(b, 0) && math.Signbit(a) == math.Signbit(b)) {
		return 0
	}
	d := math.Abs(a - b)
	m := math.Max(math.Abs(a), math.Abs(b))
	if m == 0 {
		return d
	}
	return d / m
}

// compareMetricSeries — saf çekirdek. GroupKey birleşimi seri kimliğidir;
// zaman damgası nokta kimliğidir.
func compareMetricSeries(a, b []chstore.SpanMetricSeries, tol float64) metricCompareReport {
	rep := metricCompareReport{Class: "identical", Tolerance: tol, SeriesA: len(a), SeriesB: len(b)}
	key := func(s chstore.SpanMetricSeries) string { return strings.Join(s.GroupKey, "\x1f") }
	bm := make(map[string]chstore.SpanMetricSeries, len(b))
	for _, s := range b {
		bm[key(s)] = s
	}
	seen := map[string]bool{}
	for _, sa := range a {
		k := key(sa)
		seen[k] = true
		row := metricCompareSeries{GroupKey: sa.GroupKey, Class: "identical"}
		sb, ok := bm[k]
		if !ok {
			row.Class = "mismatch"
			row.OnlyA = len(sa.Points)
			rep.Series = append(rep.Series, row)
			rep.Class = "mismatch"
			continue
		}
		rep.Matched++
		bp := make(map[int64]float64, len(sb.Points))
		for _, p := range sb.Points {
			bp[p.Time] = p.Value
		}
		ap := make(map[int64]bool, len(sa.Points))
		for _, p := range sa.Points {
			ap[p.Time] = true
			v, ok := bp[p.Time]
			if !ok {
				row.OnlyA++
				continue
			}
			row.Points++
			d := math.Abs(p.Value - v)
			r := relDiff(p.Value, v)
			if d > row.MaxAbs {
				row.MaxAbs = d
			}
			if r > row.MaxRel {
				row.MaxRel = r
			}
			switch {
			case r == 0:
			case r <= tol:
				row.Class = worstClass(row.Class, "tolerated")
			default:
				row.Mismatches++
				if row.FirstMismatchTime == 0 {
					row.FirstMismatchTime = p.Time
				}
				row.Class = "mismatch"
			}
		}
		for _, p := range sb.Points {
			if !ap[p.Time] {
				row.OnlyB++
			}
		}
		if row.OnlyA > 0 || row.OnlyB > 0 {
			row.Class = "mismatch"
		}
		rep.Class = worstClass(rep.Class, row.Class)
		rep.Series = append(rep.Series, row)
	}
	for _, sb := range b {
		if seen[key(sb)] {
			continue
		}
		rep.Series = append(rep.Series, metricCompareSeries{GroupKey: sb.GroupKey, Class: "mismatch", OnlyB: len(sb.Points)})
		rep.Class = "mismatch"
	}
	sort.SliceStable(rep.Series, func(i, j int) bool { return classRank(rep.Series[i].Class) > classRank(rep.Series[j].Class) })
	return rep
}

// getMetricsCompare — GET /api/metrics/compare, /api/metrics/query ile aynı
// parametreler; iki kaynak (ch, vm) ZORLA — ?metricsrc= burada anlamsız.
// Admin: iki backend'e birden yük bindiren tanı ucu.
func (s *Server) getMetricsCompare(w http.ResponseWriter, r *http.Request) {
	if s.vmetrics == nil || !s.vmetrics.Configured() {
		writeJSONError(w, http.StatusBadRequest, "victoriametrics not configured — set a base URL in Settings → Metrics backend")
		return
	}
	q := r.URL.Query()
	name := strings.TrimSpace(q.Get("name"))
	if name == "" {
		writeJSONError(w, http.StatusBadRequest, "name required")
		return
	}
	step, _ := strconv.Atoi(q.Get("step"))
	maxDP, _ := strconv.Atoi(q.Get("maxDataPoints"))
	if maxDP < 0 {
		maxDP = 0
	} else if maxDP > 4000 {
		maxDP = 4000
	}
	svc, agg := q.Get("service"), q.Get("agg")
	groupByRaw, filtersRaw := q.Get("groupBy"), q.Get("filters")
	inst, engine := strings.TrimSpace(q.Get("instance")), strings.TrimSpace(q.Get("engine"))
	from, to := parseTime(q.Get("from")), parseTime(q.Get("to"))
	if from.IsZero() || to.IsZero() {
		to = time.Now()
		from = to.Add(-time.Hour)
	}
	chSrc, vmSrc := chMetricSource{s.store}, vmMetricSource{s.vmetrics}
	key := fmt.Sprintf("metrics-compare:v1%s:name=%s:svc=%s:agg=%s:step=%d:mdp=%d:gb=%s:f=%s:inst=%s:eng=%s:from=%d:to=%d:mx=%s",
		s.metricNameRuleTag(vmSrc), name, svc, agg, step, maxDP, groupByRaw, filtersRaw, inst, engine,
		from.Unix()/60, to.Unix()/60, s.store.MetricExclusions().Digest())
	s.serveCached(w, r, key, 30*time.Second, func(ctx context.Context) (any, error) {
		mfilters, ferr := parseFilters(filtersRaw)
		if ferr != nil {
			return nil, ferr
		}
		f := chstore.MetricQueryFilter{
			Name: name, Service: svc, Instance: inst, Engine: engine, Filters: mfilters,
			GroupBy: splitNonEmpty(groupByRaw, ','), Aggregation: agg, From: from, To: to,
			StepSeconds: step, MaxDataPoints: maxDP,
		}
		sa, noteA, err := queryMetricNoted(ctx, chSrc, f)
		if err != nil {
			return nil, fmt.Errorf("clickhouse: %w", err)
		}
		sb, noteB, err := queryMetricNoted(ctx, vmSrc, f)
		if err != nil {
			return nil, fmt.Errorf("victoriametrics: %w", err)
		}
		rep := compareMetricSeries(sa, sb, metricCompareTolerance)
		rep.A, rep.B, rep.NoteA, rep.NoteB = chSrc.Name(), vmSrc.Name(), noteA, noteB
		return rep, nil
	})
}
