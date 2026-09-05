package api

// call_period.go — v0.10.438 (CoSRE router boşlukları D3): "A'dan B'ye
// atılan isteklerde her 5 dk gibi bir periyot var mı?" Otokorelasyonla
// periyot tespiti, üç seri üzerinden:
//   1. A→B yönlü 5 dk seri (topology_edges_5m; ≥ 10 dk periyot — Nyquist,
//      kanıt söyler),
//   2. A'nın giden client span'leri / dk (tüm hedefler),
//   3. B'nin gelen server span'leri / dk (tüm çağıranlar)
// 2-3 servis kapsamlı ham spans GROUP BY (≤ 6 sa, spec kararı); yön kesin
// değil, kanıt bunu da söyler. Tek servisli soru ("checkout'a gelen
// isteklerde periyot var mı") yalnız 2-3'ü koşar. Yeni MV YOK.

import (
	"context"
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/cilcenk/coremetry/internal/chstore"
)

const (
	callPeriodMinuteWindow = 6 * time.Hour
	callPeriodEdgeWindow   = 24 * time.Hour
	callPeriodMinStrength  = 0.45
)

// hasPeriodSignal — periyot/düzenli aralık sözcükleri.
func hasPeriodSignal(toks []string) bool {
	if tokenHasPrefix(toks, "periyot", "periyod", "period", "cron", "zamanlanm", "interval", "aralıklarla", "araliklarla", "düzenli", "duzenli", "ritm", "ritim") {
		return true
	}
	// "her 5 dakika(da)", "every 5 min"
	for i, t := range toks {
		if (t == "her" || t == "every") && i+2 < len(toks) {
			if strings.HasPrefix(toks[i+2], "dk") || strings.HasPrefix(toks[i+2], "dakika") || strings.HasPrefix(toks[i+2], "saat") || strings.HasPrefix(toks[i+2], "min") || strings.HasPrefix(toks[i+2], "hour") || strings.HasPrefix(toks[i+2], "sn") || strings.HasPrefix(toks[i+2], "saniye") || strings.HasPrefix(toks[i+2], "sec") {
				return true
			}
		}
	}
	return false
}

// periodResult — SAF tespit çıktısı.
type periodResult struct {
	PeriodS  int64
	Strength float64
	Cycles   int
	OK       bool
	Reason   string
}

// detectPeriod — normalize otokorelasyon: ortalaması alınmış seride lag ∈
// [2, n/3] için r(lag) = Σ x_i·x_{i+lag} / Σ x_i². En güçlü lag periyot
// adayı; r ≥ 0.45 ve ≥ 3 döngü şart. Sabit/boş seri → yok.
func detectPeriod(values []float64, stepS int64) periodResult {
	n := len(values)
	if n < 9 || stepS <= 0 {
		return periodResult{Reason: "seri çok kısa"}
	}
	mean := 0.0
	for _, v := range values {
		mean += v
	}
	mean /= float64(n)
	x := make([]float64, n)
	var denom float64
	for i, v := range values {
		x[i] = v - mean
		denom += x[i] * x[i]
	}
	if denom == 0 {
		return periodResult{Reason: "seri sabit"}
	}
	bestLag, bestR := 0, -2.0
	maxLag := n / 3
	for lag := 2; lag <= maxLag; lag++ {
		var num float64
		for i := 0; i+lag < n; i++ {
			num += x[i] * x[i+lag]
		}
		r := num / denom
		if r > bestR {
			bestR, bestLag = r, lag
		}
	}
	if bestLag == 0 {
		return periodResult{Reason: "lag aralığı boş"}
	}
	res := periodResult{PeriodS: int64(bestLag) * stepS, Strength: bestR, Cycles: n / bestLag}
	if bestR < callPeriodMinStrength || res.Cycles < 3 {
		res.Reason = fmt.Sprintf("en güçlü lag %s (r=%.2f, %d döngü) eşiğin altında", fmtPeriodTR(res.PeriodS), bestR, res.Cycles)
		return res
	}
	res.OK = true
	return res
}

func fmtPeriodTR(sec int64) string {
	switch {
	case sec%3600 == 0:
		return fmt.Sprintf("%d sa", sec/3600)
	case sec%60 == 0:
		return fmt.Sprintf("%d dk", sec/60)
	}
	return fmt.Sprintf("%d sn", sec)
}

// fillSeries — v0.10.444: CH GROUP BY yalnız VAR OLAN kovaları döndürür;
// otokorelasyon eşit aralıklı seri varsayar. 30 dk'da bir ateşlenen cron
// 48 kova/24 sa üretir, hepsi ≈N → boşluksuz seri SABİT görünür ve
// "periyot yok" çıkardı — sorunun tam tersi. Izgara [from, to) adımla
// kurulur, eksik kovalar 0.
func fillSeries(times []int64, values []float64, stepS int64, from, to time.Time) ([]int64, []float64) {
	if stepS <= 0 || !to.After(from) {
		return times, values
	}
	step := stepS * int64(time.Second)
	start := from.UnixNano() / step * step
	n := int((to.UnixNano() - start + step - 1) / step)
	if n <= 0 || n > 100000 {
		return times, values
	}
	byIdx := map[int]float64{}
	for i := range times {
		idx := int((times[i] - start) / step)
		if idx >= 0 && idx < n {
			byIdx[idx] += values[i]
		}
	}
	outT := make([]int64, n)
	outV := make([]float64, n)
	for i := 0; i < n; i++ {
		outT[i] = start + int64(i)*step
		outV[i] = byIdx[i]
	}
	return outT, outV
}

// periodSeries — kanıt için bir seri.
type periodSeries struct {
	Label  string
	StepS  int64
	Times  []int64 // unix ns
	Values []float64
	Note   string
}

func (p periodSeries) topPeaks(k int) []string {
	type kv struct {
		t int64
		v float64
	}
	var kvs []kv
	for i := range p.Values {
		kvs = append(kvs, kv{p.Times[i], p.Values[i]})
	}
	// basit seçim: en büyük k
	var out []string
	for j := 0; j < k && len(kvs) > 0; j++ {
		bi := 0
		for i := range kvs {
			if kvs[i].v > kvs[bi].v {
				bi = i
			}
		}
		if kvs[bi].v <= 0 {
			break
		}
		out = append(out, fmt.Sprintf("%s %.0f", time.Unix(0, kvs[bi].t).UTC().Format("15:04"), kvs[bi].v))
		kvs = append(kvs[:bi], kvs[bi+1:]...)
	}
	return out
}

// renderCallPeriodTR — SAF.
func renderCallPeriodTR(route guidedRoute, series []periodSeries, minuteWin, edgeWin time.Duration) string {
	var b strings.Builder
	subject := route.Service
	if route.PairTo != "" {
		subject = route.PairFrom + " → " + route.PairTo
	}
	fmt.Fprintf(&b, "%s çağrı periyodu analizi (dakikalık seriler son %s, yönlü 5 dk seri son %s):\n", subject, fmtAgoTR(int64(minuteWin.Seconds())), fmtAgoTR(int64(edgeWin.Seconds())))
	if len(series) == 0 {
		b.WriteString("Seri okunamadı — veri yok; bunu dürüstçe söyle.\n")
		return b.String()
	}
	anyOK := false
	for _, s := range series {
		if len(s.Values) == 0 {
			fmt.Fprintf(&b, "- %s: veri yok.\n", s.Label)
			continue
		}
		var sum, maxV float64
		for _, v := range s.Values {
			sum += v
			if v > maxV {
				maxV = v
			}
		}
		res := detectPeriod(s.Values, s.StepS)
		fmt.Fprintf(&b, "- %s (%d nokta × %s, ort %.1f, tepe %.0f): ", s.Label, len(s.Values), fmtPeriodTR(s.StepS), sum/float64(len(s.Values)), maxV)
		if res.OK {
			anyOK = true
			fmt.Fprintf(&b, "PERİYOT ~%s (otokorelasyon r=%.2f, %d döngü).", fmtPeriodTR(res.PeriodS), res.Strength, res.Cycles)
		} else {
			fmt.Fprintf(&b, "belirgin periyot yok (%s).", res.Reason)
		}
		if peaks := s.topPeaks(3); len(peaks) > 0 {
			fmt.Fprintf(&b, " Tepeler (UTC): %s.", strings.Join(peaks, ", "))
		}
		if s.Note != "" {
			b.WriteString(" " + s.Note)
		}
		b.WriteString("\n")
	}
	if !anyOK {
		b.WriteString("Sonuç: hiçbir seride eşik üstü periyot yok — 'düzenli bir zamanlayıcı görünmüyor' de; ama dakikalık serilerin yönsüz, 5 dk serinin 10 dk altını göremediğini belirt.\n")
	} else {
		b.WriteString("Sonuç: periyot bulunan seriyi ve süresini söyle; yönlü seri (A→B) bulduysa kesindir, dakikalık seriler yalnız tarafı gösterir (tüm çağıranlar/hedefler). Kanıtta olmayan periyot ya da sayı uydurma.\n")
	}
	return b.String()
}

func spanMetricSeriesToPeriod(label string, ser []chstore.SpanMetricSeries, stepS int64, note string, from, to time.Time) periodSeries {
	p := periodSeries{Label: label, StepS: stepS, Note: note}
	if len(ser) == 0 {
		return p
	}
	for _, pt := range ser[0].Points {
		p.Times = append(p.Times, pt.Time)
		p.Values = append(p.Values, pt.Value)
	}
	p.Times, p.Values = fillSeries(p.Times, p.Values, stepS, from, to) // v0.10.444
	return p
}

// guidedCallPeriodBundle — seriler + kanıt. Pencere: dakikalık ≤ 6 sa,
// yönlü 24 sa (periyot için çok döngü gerekir), çıpa `to`.
func (s *Server) guidedCallPeriodBundle(ctx context.Context, emit func(string, any), route *guidedRoute, to time.Time) (string, string, error) {
	minFrom := to.Add(-callPeriodMinuteWindow)
	edgeFrom := to.Add(-callPeriodEdgeWindow)
	var series []periodSeries
	var srcParts []string
	if route.PairFrom != "" && route.PairTo != "" {
		edgeChild := route.PairTo
		if route.PairToKind != "service" {
			// v0.10.444 — dış/DB düğüm parçası ("osbprod") saklanan ada
			// ("ext:osbprod.example.com") çözülür; aksi hâlde IN listesi hiç
			// eşleşmiyor ve seri "veri yok" çıkıyordu.
			if edges, err := s.store.ReadServiceTopologyAggForFocus(ctx, edgeFrom, to, route.PairFrom, 1, 20000); err == nil {
				if matched, _ := matchPairEdges(edges, route.PairFrom, route.PairTo, false); len(matched) > 0 {
					edgeChild = matched[0].ChildNode
					route.PairTo = strings.TrimPrefix(strings.TrimPrefix(strings.TrimPrefix(edgeChild, "ext:"), "db:"), "q:")
				}
			}
		}
		n := emitGuidedStep(emit, "topology_edge_series", fmt.Sprintf(`{"from":%q,"to":%q,"step":"5m"}`, route.PairFrom, edgeChild))
		pts, err := s.store.TopologyEdgeSeries(ctx, route.PairFrom, edgeChild, edgeFrom, to)
		if err != nil {
			emitGuidedStepResult(emit, n, "topology_edge_series", "", err)
			return "", "", err
		}
		emitGuidedStepResult(emit, n, "topology_edge_series", fmt.Sprintf("%d kova", len(pts)), nil)
		ps := periodSeries{Label: route.PairFrom + " → " + route.PairTo + " yönlü çağrı", StepS: 300, Note: "(5 dk kova: 10 dk altındaki periyot görünmez — Nyquist.)"}
		for _, p := range pts {
			ps.Times = append(ps.Times, p.Time)
			ps.Values = append(ps.Values, float64(p.Calls))
		}
		if len(pts) > 0 {
			ps.Times, ps.Values = fillSeries(ps.Times, ps.Values, 300, edgeFrom, to) // v0.10.444
		}
		series = append(series, ps)
		srcParts = append(srcParts, "topology_edges_5m")
	}
	minute := func(service, kind, label, note string) error {
		n := emitGuidedStep(emit, "span_series", fmt.Sprintf(`{"service":%q,"kind":%q,"step":"1m"}`, service, kind))
		ser, err := s.store.QuerySpanMetric(ctx, chstore.SpanMetricFilter{
			Filters:     []chstore.FilterExpr{{Key: "service.name", Op: "=", Values: []string{service}}, {Key: "kind", Op: "=", Values: []string{kind}}},
			Aggregation: "count", From: minFrom, To: to, StepSeconds: 60,
		})
		if err != nil {
			emitGuidedStepResult(emit, n, "span_series", "", err)
			return err
		}
		emitGuidedStepResult(emit, n, "span_series", fmt.Sprintf("%d seri", len(ser)), nil)
		series = append(series, spanMetricSeriesToPeriod(label, ser, 60, note, minFrom, to))
		return nil
	}
	a := route.PairFrom
	if a == "" {
		a = route.Service
	}
	if a != "" {
		if err := minute(a, "client", a+" giden client span/dk", "(tüm hedefler — yön kesin değil)"); err != nil {
			return "", "", err
		}
		srcParts = append(srcParts, "spans (servis kapsamlı, 1 dk)")
	}
	if route.PairToKind == "service" && route.PairTo != "" {
		if err := minute(route.PairTo, "server", route.PairTo+" gelen server span/dk", "(tüm çağıranlar — yön kesin değil)"); err != nil {
			return "", "", err
		}
	} else if route.PairTo == "" && route.Service != "" {
		if err := minute(route.Service, "server", route.Service+" gelen server span/dk", "(tüm çağıranlar)"); err != nil {
			return "", "", err
		}
	}
	src := strings.Join(srcParts, " + ") + fmt.Sprintf(" (dakikalık son %s, yönlü son %s); otokorelasyon eşiği r≥%.2f, ≥3 döngü", fmtAgoTR(int64(callPeriodMinuteWindow.Seconds())), fmtAgoTR(int64(callPeriodEdgeWindow.Seconds())), callPeriodMinStrength)
	_ = math.Abs
	return renderCallPeriodTR(*route, series, callPeriodMinuteWindow, callPeriodEdgeWindow), src, nil
}
