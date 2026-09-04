package api

// endpoints_metric.go — v0.10.336: /endpoints "Kaynak: metrik" kipi.
//
// Operatör (2026-09-04): "Endpoint metriklerine vmden okuyabilir miyiz".
// Meslektaşının Grafana'sı VM'den `sum(rate(http_server_request_duration_
// seconds_count{service_name=…, http_route=~…, http_response_status_code
// !~"2.*|3.*"}[$__rate_interval])) by(http_route)` okuyor; /endpoints ise
// SPAN türevli (spanmetrics_1m MV, cluster/env'de ham span'ler). Collector
// örnekliyorsa span sayımı eksik kalır, metrik örneklenmez; span yolu
// giriş-span popülasyonuna bağlı, metrik enstrümanın saydığı her isteği
// taşır. "Kendi kendine hesaplıyor ve sağlıklı olmuyor" şikâyetinin iki
// olası sebebi de bu.
//
// Bu dosya AYNI tabloyu METRİKTEN doldurur: GET /api/endpoints/metric,
// zarfı /api/endpoints'inki (endpointsListResponse) + kaynak/teşhis
// alanları; frontend aynı satır tipini çizer. Her okuma metricsource.go
// dikişinden geçer (ClickHouse | VictoriaMetrics), s.store.<metric>
// çağrısı YOK. Kimlik: Service Overview'un tput bağı (loadTputBinding —
// job/name etiketli kurulumlar) yoksa OTLP varsayılanı service_name.
// Metrik adı: `service.throughput_metric` ayarı + aday listesi
// (resolveThroughputMetric) — meslektaşının ailesi varsayılanın kendisi.
//
// PENCERE MATEMATİĞİ — dürüst olan yer:
//   - çağrı/hata = ince adımlı `increase` serilerinin toplamı (≤60 adım,
//     ≥60 s). VM noktayı pencere SONUNA, CH kova BAŞINA damgalar; toplam
//     sınırda en çok bir adımlık kayar (≤%2). Tek-adımlı "tüm pencere"
//     sorgusu VM'de start hizasına bağlı kayabildiği için tercih edilmedi.
//   - avg/p50/p95/p99 = adım değerlerinin ÇAĞRI-AĞIRLIKLI ortalaması —
//     gerçek pencere yüzdeliği değil, yüzdeliklerin ortalaması. Not bunu
//     söyler; p90 bu kipte yok (dikiş p50/p95/p99 üretir).
//   - hata = durum kodu 5xx (`http.response.status_code` semconv, yoksa
//     eski `http.status_code`); ikisi de yoksa hata BİLİNMİYOR (0 değil) ve
//     zarf errorsUnknown der. 4xx hata SAYILMAZ (operatör kararı
//     2026-09-04: span yoluyla aynı; 4xx Status pillerinde görünür).
//   - süre birimi kaynaktan (MetricUnit) ya da Prometheus ad
//     sözleşmesinden (`_seconds`); ikisi de yoksa ham sayı +
//     latencyUnitKnown=false — sessizce "ms" varsayılmaz.
//   - cluster filtresi metrikte ifade edilemez → clusterIgnored (liste
//     DAHA GENİŞ, daha dar değil); env VM'de ifade edilemezse envAmbiguous
//     (withEnvFilter sözleşmesi, v0.9.1268).
//   - exemplar kısayolları (⚡/✖) gelmez: metrikte trace id yok.
//
// Varsayılan DEĞİŞMEDİ: /endpoints span'dan okumaya devam eder; bu kip
// `?src=metric` ile seçilir (operatör kuralı: davranış değişikliği
// sorulmadan gemiye çıkmaz — burada yalnız ek seçenek).

import (
	"context"
	"fmt"
	"math"
	"net/http"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/cilcenk/coremetry/internal/chstore"
)

func init() { registerRoutesExtra("endpoints-metric", (*Server).registerEndpointsMetricRoutes) }

// registerEndpointsMetricRoutes — /api/endpoints ile aynı duruş: çıplak
// (okuma-yalnız liste, kimlikli her rol).
func (s *Server) registerEndpointsMetricRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/endpoints/metric", s.getEndpointsMetric)
}

const (
	// endpointsMetricSlots — sparkline + ağırlık ızgarası. 60 × 800 route =
	// 48k satır, chstore.SpanMetricRowCap (50k) altında.
	endpointsMetricSlots = 60
	// endpointsMetricMinStep — export aralığının altına inmesin (60 s, VM
	// rate penceresi tabanı ile aynı sınıf).
	endpointsMetricMinStep = 60
	// endpointsMetricKeyLookback — durum-kodu etiketi keşfi penceresi
	// (ListMetricNames'in 7 g'siyle aynı: "var" aynı anlama gelsin).
	endpointsMetricKeyLookback = 7 * 24 * time.Hour
	endpointsMetricSource      = "metric"
)

// endpointsMetricStatusKeys — sırayla: OTel semconv (≥1.23) sonra eski
// yazım. İlk MEVCUT olan seçilir; ikisi de yoksa hata bilinmiyor.
var endpointsMetricStatusKeys = []string{"http.response.status_code", "http.status_code"}

// endpointsMetricPlan — istekten çıkan SAF karar; test buraya bakar.
type endpointsMetricPlan struct {
	From, To    time.Time
	Service     string
	Search      string
	Env         string
	Cluster     string // yalnız "uygulanmadı" ilanı için; sorguya girmez
	Limit       int
	Compare     bool
	BySignature bool
	Sort, Dir   string
	StepSec     int
}

// endpointsMetricStep — pencereyi ≤60 adıma böler, adım ≥60 s.
func endpointsMetricStep(from, to time.Time) int {
	sec := int(to.Sub(from).Seconds())
	if sec <= 0 {
		return endpointsMetricMinStep
	}
	step := (sec + endpointsMetricSlots - 1) / endpointsMetricSlots
	if step < endpointsMetricMinStep {
		step = endpointsMetricMinStep
	}
	return step
}

// endpointsMetricKey — TÜM girdiler (v0.5.187 sınıfı): depo adı ayrı segment
// (metricsource.go sözleşmesi), dışlama özeti (CH rate yolu kuralları uygular).
func endpointsMetricKey(p endpointsMetricPlan, srcName, mx string) string {
	return fmt.Sprintf("endpoints-metric:v1:src=%s:%s:svc=%s:q=%s:env=%s:cl=%s:lim=%d:cmp=%v:sig=%v:sort=%s:dir=%s:step=%d:mx=%s",
		srcName, cacheBucket(p.From, p.To), p.Service, p.Search, p.Env, p.Cluster, p.Limit, p.Compare, p.BySignature, p.Sort, p.Dir, p.StepSec, mx)
}

// endpointsMetricIdentity — hangi metrik, hangi kimlik.
type endpointsMetricIdentity struct {
	Metric     string // throughput ailesi (VM'de `_count` yazımı olabilir)
	RTMetric   string // değer okuyan ad (avg / yüzdelik) — LatencyMetricName
	Instrument string // sum | histogram
	Service    string // service_name eşleşmesi (bağ etiketliyse boş)
	Filters    []chstore.FilterExpr
	MatchedBy  string
	Tried      []string
}

func (s *Server) endpointsMetricIdentity(ctx context.Context, src metricSource, service, env string) (endpointsMetricIdentity, bool) {
	id := endpointsMetricIdentity{Service: service, MatchedBy: "service_name"}
	if service != "" {
		// Service Overview'un çözdüğü kimlik (job/name etiketi, `_count`
		// yazımı, instrument) — keşfi tekrarlama.
		if b := s.loadTputBinding(ctx, service, env, src.Name()); b != nil && !b.None && b.Metric != "" {
			id.Metric, id.Instrument = b.Metric, b.Instrument
			id.Service, id.Filters = b.Service, b.Filters
			id.MatchedBy = "binding:" + b.MatchedBy
			id.RTMetric = src.LatencyMetricName(b.Metric)
			return id, true
		}
	}
	resolved, tried := s.resolveThroughputMetric(ctx, src, "")
	id.Tried = tried
	if resolved == "" {
		return id, false
	}
	id.Metric = resolved
	id.RTMetric = src.LatencyMetricName(resolved)
	id.Instrument = src.MetricInstrument(ctx, resolved, service)
	if id.Instrument == "" {
		id.Instrument = src.MetricInstrument(ctx, resolved, "")
	}
	return id, true
}

// endpointsMetricResponse — /api/endpoints zarfı + kaynak/teşhis. Zarf
// alanları 200 ile döner; metrik yoksa da 200 + metricExists=false + not
// (tput ucunun sözleşmesi: bulamamak operatörün sorusuna cevaptır).
type endpointsMetricResponse struct {
	endpointsListResponse
	Source           string   `json:"source"`
	Backend          string   `json:"backend"`
	Metric           string   `json:"metric,omitempty"`
	MetricExists     bool     `json:"metricExists"`
	MatchedBy        string   `json:"matchedBy,omitempty"`
	Instrument       string   `json:"instrument,omitempty"`
	StatusKey        string   `json:"statusKey,omitempty"`
	ErrorsUnknown    bool     `json:"errorsUnknown,omitempty"`
	LatencyUnit      string   `json:"latencyUnit,omitempty"`
	LatencyUnitKnown bool     `json:"latencyUnitKnown"`
	LatencyUnitFrom  string   `json:"latencyUnitFrom,omitempty"` // source | name
	EnvAmbiguous     bool     `json:"envAmbiguous,omitempty"`
	ClusterIgnored   bool     `json:"clusterIgnored,omitempty"`
	StepSeconds      int      `json:"stepSeconds"`
	Tried            []string `json:"tried,omitempty"`
	Partial          []string `json:"partial,omitempty"`
	Note             string   `json:"note"`
}

// latencyScaleMs — birim → ms çarpanı. Kaynak birimi önce; yoksa Prometheus
// ad sözleşmesi (`_seconds` / `_milliseconds`); o da yoksa 1 + bilinmiyor.
func latencyScaleMs(unit, metric string) (scale float64, known bool, from string) {
	switch strings.ToLower(strings.TrimSpace(unit)) {
	case "s", "seconds":
		return 1000, true, "source"
	case "ms", "milliseconds":
		return 1, true, "source"
	case "us", "microseconds":
		return 0.001, true, "source"
	case "ns", "nanoseconds":
		return 1e-6, true, "source"
	}
	n := strings.ToLower(metric)
	switch {
	case strings.Contains(n, "milliseconds"):
		return 1, true, "name"
	case strings.Contains(n, "seconds"):
		return 1000, true, "name"
	}
	return 1, false, ""
}

var (
	epSigUUID = regexp.MustCompile(chstore.OpSigReUUID)
	epSigHex  = regexp.MustCompile(chstore.OpSigReHex)
	epSigNum  = regexp.MustCompile(chstore.OpSigReNum)
)

// endpointPathSignature — "group by shape" (span yolu opSigWrap ile aynı
// sıra: UUID → ':id', uzun hex → '/:id', sayı → '/:id').
func endpointPathSignature(path string) string {
	path = epSigUUID.ReplaceAllString(path, ":id")
	path = epSigHex.ReplaceAllString(path, "/:id")
	return epSigNum.ReplaceAllString(path, "/:id")
}

type epKey struct{ service, path string }

// epMetricAcc — bir (service, path) toplayıcısı. v/w: avg,p50,p95,p99
// ağırlıklı toplam / ağırlık.
type epMetricAcc struct {
	calls, errors float64
	countAt       []float64 // slot → çağrı (ağırlık)
	errAt         []float64
	p99At         []float64
	v, w          [4]float64
}

func newEPAcc(n int) *epMetricAcc {
	return &epMetricAcc{countAt: make([]float64, n), errAt: make([]float64, n), p99At: make([]float64, n)}
}

// endpointsMetricDetailMax — 2. aşamaya (ayrıntı sorguları) giren route
// sayısı tavanı: regex alternation uzunluğu ve VM seri sayısı sınırı.
const endpointsMetricDetailMax = 300

// endpointsMetricRankSlots — 1. aşama (sıralama) adım sayısı: yalnız çağrı
// toplamı gerekir, 12 kaba adım yeter (60'ın beşte biri).
const endpointsMetricRankSlots = 12

// endpointsMetricRouteRegex — SAF: route listesinden `=~` alternation'ı
// (QuoteMeta; iki uç da kalıbı tam-eşleşme olarak çapalar).
func endpointsMetricRouteRegex(vals []string) string {
	parts := make([]string, 0, len(vals))
	for _, v := range vals {
		parts = append(parts, regexp.QuoteMeta(v))
	}
	return strings.Join(parts, "|")
}

// buildEndpointsMetric — SAF (kaynak arayüz): sorgular → satırlar.
// Sıralama/limit ÇAĞIRANDA (prior birleşiminden sonra: p99Delta).
//
// v0.10.362 — Operator-reported (prod, servissiz + 30 dk + compare): "çok
// yavaş, Failed to load endpoints". Eski şekil ALTI aralık sorgusunu tüm
// servislerin tüm route'ları için 60 adımla koşuyordu (compare ile 12) —
// VM'de binlerce seri × 60 nokta, süre aşımı. Sayfalama çözmez: sıralama
// için yine her route hesaplanmalı. İKİ AŞAMA:
//  1. SIRALAMA — tek sorgu, çağrı toplamı, 12 kaba adım, tüm route'lar →
//     arama süzgeci → çağrıya göre ilk N (N = limit, ≤ 300).
//  2. AYRINTI — hata / avg / p50 / p95 / p99 / sparkline (60 adım) YALNIZ o
//     route'lar için (`http.route =~ r1|…|rN`, servissizde `service.name`
//     de daraltılır; superset satırlar Go'da elenir).
//
// Diğer sıralamalar (p99, hata…) bu N içinde uygulanır; zarf `pool` /
// `poolCapped` bunu ilan eder (span yolunun p99Delta havuz sözleşmesi).
func buildEndpointsMetric(ctx context.Context, src metricSource, id endpointsMetricIdentity, p endpointsMetricPlan) (endpointsMetricResponse, error) {
	resp := endpointsMetricResponse{
		endpointsListResponse: endpointsListResponse{Rows: []chstore.EndpointRow{}},
		Source:                endpointsMetricSource,
		Backend:               src.Name(),
		Metric:                id.Metric,
		MetricExists:          true,
		MatchedBy:             id.MatchedBy,
		Instrument:            id.Instrument,
		StepSeconds:           p.StepSec,
		ClusterIgnored:        strings.TrimSpace(p.Cluster) != "",
	}
	step := p.StepSec
	if step <= 0 {
		step = endpointsMetricStep(p.From, p.To)
	}
	allServices := p.Service == "" // kimliksiz: servis de gruplanır
	groupBy := []string{"http.route"}
	if allServices {
		groupBy = []string{"service.name", "http.route"}
	}
	base := chstore.MetricQueryFilter{
		Name: id.Metric, Service: id.Service, Filters: id.Filters,
		From: p.From, To: p.To, StepSeconds: step, RateWindowSec: step,
		GroupBy: groupBy,
	}
	if p.Env != "" {
		_, applied := src.EnvFilterExpr(p.Env)
		resp.EnvAmbiguous = !applied
		base = withEnvFilter(base, p.Env, src)
	}
	if keys := src.MetricPresentKeys(ctx, id.RTMetric, endpointsMetricStatusKeys, endpointsMetricKeyLookback); len(keys) > 0 {
		resp.StatusKey = keys[0]
	} else {
		resp.ErrorsUnknown = true
	}
	unit := src.MetricUnit(ctx, id.RTMetric, p.Service)
	scale, unitKnown, unitFrom := latencyScaleMs(unit, id.RTMetric)
	resp.LatencyUnit, resp.LatencyUnitKnown, resp.LatencyUnitFrom = unit, unitKnown, unitFrom
	if unit == "" && unitKnown {
		resp.LatencyUnit = "s"
		if scale == 1 {
			resp.LatencyUnit = "ms"
		}
	}

	rate := src.QueryMetricRate
	if id.Instrument == "histogram" {
		rate = src.QueryMetricCountRate
	}
	keyOf := func(gk []string) (epKey, bool) {
		svc, path := p.Service, ""
		if allServices {
			if len(gk) >= 2 {
				svc, path = gk[0], gk[1]
			}
		} else if len(gk) >= 1 {
			path = gk[len(gk)-1]
		}
		if path == "" || svc == "" {
			return epKey{}, false // rotasız/servissiz seri: span yolu da listelemez
		}
		if p.BySignature {
			path = endpointPathSignature(path)
		}
		return epKey{svc, path}, true
	}
	search := strings.ToLower(strings.TrimSpace(p.Search))

	// ── 1. AŞAMA: sıralama (kaba adım, yalnız çağrı) ──────────────────────
	rankStep := int(p.To.Sub(p.From).Seconds()) / endpointsMetricRankSlots
	if rankStep < endpointsMetricMinStep {
		rankStep = endpointsMetricMinStep
	}
	rankF := base
	rankF.StepSeconds, rankF.RateWindowSec = rankStep, rankStep
	rankSer, err := rate(ctx, rankF, "increase")
	if err != nil {
		return resp, err
	}
	rankCalls := map[epKey]float64{}
	rawRoutes := map[string]bool{} // ham route (sinyatür öncesi) — 2. aşama regex'i
	rawSvcs := map[string]bool{}
	for _, ser := range rankSer {
		k, ok := keyOf(ser.GroupKey)
		if !ok {
			continue
		}
		if search != "" && !strings.Contains(strings.ToLower(k.path), search) {
			continue
		}
		for _, pt := range ser.Points {
			if pt.Value > 0 && !math.IsNaN(pt.Value) && !math.IsInf(pt.Value, 0) {
				rankCalls[k] += pt.Value
			}
		}
		if len(ser.GroupKey) > 0 {
			rawRoutes[ser.GroupKey[len(ser.GroupKey)-1]] = true
			if allServices && len(ser.GroupKey) >= 2 {
				rawSvcs[ser.GroupKey[0]] = true
			}
		}
	}
	if len(rankCalls) == 0 {
		return resp, nil
	}
	ranked := make([]epKey, 0, len(rankCalls))
	for k := range rankCalls {
		ranked = append(ranked, k)
	}
	sort.Slice(ranked, func(i, j int) bool {
		if rankCalls[ranked[i]] == rankCalls[ranked[j]] {
			return ranked[i].path < ranked[j].path
		}
		return rankCalls[ranked[i]] > rankCalls[ranked[j]]
	})
	pool := p.Limit
	if pool <= 0 || pool > endpointsMetricDetailMax {
		pool = endpointsMetricDetailMax
	}
	resp.PoolCapped = len(ranked) > pool
	if len(ranked) > pool {
		ranked = ranked[:pool]
	}
	resp.Pool = len(ranked)
	keep := make(map[epKey]bool, len(ranked))
	for _, k := range ranked {
		keep[k] = true
	}
	// 2. aşama daraltması: seçilen anahtarların HAM route'ları (sinyatür
	// modunda birden çok ham route bir anahtara düşer → hepsi).
	var detailRoutes, detailSvcs []string
	for r := range rawRoutes {
		path := r
		if p.BySignature {
			path = endpointPathSignature(r)
		}
		for _, k := range ranked {
			if k.path == path {
				detailRoutes = append(detailRoutes, r)
				break
			}
		}
	}
	for s := range rawSvcs {
		for _, k := range ranked {
			if k.service == s {
				detailSvcs = append(detailSvcs, s)
				break
			}
		}
	}
	sort.Strings(detailRoutes)
	sort.Strings(detailSvcs)
	detail := base
	detail.Filters = append(append([]chstore.FilterExpr(nil), base.Filters...),
		chstore.FilterExpr{Key: "http.route", Op: "=~", Values: []string{endpointsMetricRouteRegex(detailRoutes)}})
	if allServices && len(detailSvcs) > 0 {
		detail.Filters = append(detail.Filters,
			chstore.FilterExpr{Key: "service.name", Op: "=~", Values: []string{endpointsMetricRouteRegex(detailSvcs)}})
	}

	// ── 2. AŞAMA: ayrıntı (yalnız ilk N) ───────────────────────────────────
	nSlots := int(p.To.Sub(p.From).Seconds()) / step
	if nSlots < 1 {
		nSlots = 1
	}
	if nSlots > endpointsMetricSlots*2 {
		nSlots = endpointsMetricSlots * 2
	}
	endStamped := src.Name() == metricSourceVM
	slotOf := func(tNs int64) int {
		off := tNs/1e9 - p.From.Unix()
		if endStamped {
			off--
		}
		sl := int(off / int64(step))
		if sl < 0 {
			return 0
		}
		if sl >= nSlots {
			return nSlots - 1
		}
		return sl
	}
	acc := map[epKey]*epMetricAcc{}
	get := func(k epKey) *epMetricAcc {
		a := acc[k]
		if a == nil {
			a = newEPAcc(nSlots)
			acc[k] = a
		}
		return a
	}
	callsSer, err := rate(ctx, detail, "increase")
	if err != nil {
		return resp, err
	}
	for _, ser := range callsSer {
		k, ok := keyOf(ser.GroupKey)
		if !ok || !keep[k] {
			continue
		}
		a := get(k)
		for _, pt := range ser.Points {
			if pt.Value <= 0 || math.IsNaN(pt.Value) || math.IsInf(pt.Value, 0) {
				continue
			}
			a.calls += pt.Value
			a.countAt[slotOf(pt.Time)] += pt.Value
		}
	}
	if resp.StatusKey != "" {
		ef := detail
		ef.Filters = append(append([]chstore.FilterExpr(nil), detail.Filters...),
			chstore.FilterExpr{Key: resp.StatusKey, Op: "=~", Values: []string{"^5[0-9][0-9]$"}})
		errSer, eerr := rate(ctx, ef, "increase")
		if eerr != nil {
			resp.ErrorsUnknown = true
			resp.StatusKey = ""
			resp.Partial = append(resp.Partial, "errors: "+eerr.Error())
		} else {
			for _, ser := range errSer {
				k, ok := keyOf(ser.GroupKey)
				if !ok || !keep[k] {
					continue
				}
				a := get(k)
				for _, pt := range ser.Points {
					if pt.Value <= 0 || math.IsNaN(pt.Value) || math.IsInf(pt.Value, 0) {
						continue
					}
					a.errors += pt.Value
					a.errAt[slotOf(pt.Time)] += pt.Value
				}
			}
		}
	}
	aggs := []string{"avg", "p50", "p95", "p99"}
	for i, ag := range aggs {
		vf := detail
		vf.Name = id.RTMetric
		vf.Aggregation = ag
		ser, verr := src.QueryMetric(ctx, vf)
		if verr != nil {
			resp.Partial = append(resp.Partial, ag+": "+verr.Error())
			continue
		}
		for _, sr := range ser {
			k, ok := keyOf(sr.GroupKey)
			if !ok || !keep[k] {
				continue
			}
			a := get(k)
			for _, pt := range sr.Points {
				if math.IsNaN(pt.Value) || math.IsInf(pt.Value, 0) || pt.Value < 0 {
					continue
				}
				sl := slotOf(pt.Time)
				w := a.countAt[sl]
				if w <= 0 {
					w = 1
				}
				a.v[i] += pt.Value * w
				a.w[i] += w
				if i == 3 {
					a.p99At[sl] = pt.Value * scale
				}
			}
		}
	}

	minutes := p.To.Sub(p.From).Minutes()
	if minutes <= 0 {
		minutes = 1
	}
	rows := make([]chstore.EndpointRow, 0, len(ranked))
	for _, k := range ranked {
		a := acc[k]
		if a == nil {
			a = newEPAcc(nSlots) // ayrıntı gelmediyse sıralama sayısı kalır
			a.calls = rankCalls[k]
		}
		mean := func(i int) float64 {
			if a.w[i] <= 0 {
				return 0
			}
			return a.v[i] / a.w[i] * scale
		}
		r := chstore.EndpointRow{
			Service: k.service, Path: k.path,
			Calls:     uint64(math.Round(a.calls)),
			Errors:    uint64(math.Round(a.errors)),
			AvgMs:     mean(0),
			P50Ms:     mean(1),
			P95Ms:     mean(2),
			P99Ms:     mean(3),
			ReqPerMin: a.calls / minutes,
			Sparkline: a.countAt, ErrorsSparkline: a.errAt, P99Sparkline: a.p99At,
		}
		if a.calls > 0 {
			r.ErrorRate = a.errors / a.calls * 100
		}
		if resp.StatusKey != "" {
			r.Http5xx = r.Errors
		}
		rows = append(rows, r)
	}
	resp.Rows = rows
	return resp, nil
}

// endpointsMetricSort — span yolunun beyaz listesiyle aynı kimlikler
// (chstore.endpointsOrderBy); bilinmeyen → calls DESC.
func endpointsMetricSort(rows []chstore.EndpointRow, sortBy, dir string) {
	val := func(r chstore.EndpointRow) float64 {
		switch sortBy {
		case "errors":
			return float64(r.Errors)
		case "errorRate":
			return r.ErrorRate
		case "avgMs":
			return r.AvgMs
		case "p50Ms":
			return r.P50Ms
		case "p95Ms":
			return r.P95Ms
		case "p99Ms":
			return r.P99Ms
		case "reqPerMin":
			return r.ReqPerMin
		case "impact":
			return float64(r.Calls) * r.P99Ms * (1 + r.ErrorRate/100)
		case "totalTime":
			return float64(r.Calls) * r.AvgMs
		case "p99Delta":
			return r.P99Ms - r.PriorP99Ms
		}
		return float64(r.Calls)
	}
	desc := strings.ToLower(dir) != "asc"
	textKey := sortBy == "path" || sortBy == "service"
	sort.SliceStable(rows, func(i, j int) bool {
		a, b := rows[i], rows[j]
		if textKey {
			x, y := a.Path, b.Path
			if sortBy == "service" {
				x, y = a.Service, b.Service
			}
			if x == y {
				return a.Path < b.Path
			}
			if desc {
				return x > y
			}
			return x < y
		}
		va, vb := val(a), val(b)
		if va == vb {
			return a.Path < b.Path // deterministik
		}
		if desc {
			return va > vb
		}
		return va < vb
	})
}

// mergePriorEndpointRows — (service, path) ile prior değerlerini bindirir;
// prior'da yoksa sıfır kalır (UI "NEW" çizer, span yoluyla aynı sözleşme).
func mergePriorEndpointRows(rows, prior []chstore.EndpointRow) {
	idx := make(map[epKey]chstore.EndpointRow, len(prior))
	for _, r := range prior {
		idx[epKey{r.Service, r.Path}] = r
	}
	for i := range rows {
		if pr, ok := idx[epKey{rows[i].Service, rows[i].Path}]; ok {
			rows[i].PriorCalls = pr.Calls
			rows[i].PriorErrors = pr.Errors
			rows[i].PriorAvgMs = pr.AvgMs
			rows[i].PriorP99Ms = pr.P99Ms
		}
	}
}

// endpointsMetricNote — operatöre okunan sözleşme; her sessiz genişleme
// / bilinmezlik burada İLAN edilir.
func endpointsMetricNote(r endpointsMetricResponse, p endpointsMetricPlan) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Kaynak: METRİK (%s)", r.Backend)
	if !r.MetricExists {
		b.WriteString(" — metrik bulunamadı")
		if len(r.Tried) > 0 {
			b.WriteString(" (denenen: " + strings.Join(r.Tried, ", ") + ")")
		}
		b.WriteString(". Settings → Service throughput metric adını kontrol et ya da bu servis için Service Overview'daki metrik panelini bir kez aç.")
		return b.String()
	}
	fmt.Fprintf(&b, " — %s, kimlik %s. Çağrı ve hata %d s adımların toplamı; avg/p50/p95/p99 adım değerlerinin çağrı-ağırlıklı ortalaması (gerçek pencere yüzdeliği değil), p90 bu kipte yok.",
		r.Metric, r.MatchedBy, r.StepSeconds)
	if r.ErrorsUnknown {
		b.WriteString(" Hata BİLİNMİYOR: metrikte durum-kodu etiketi yok (http.response.status_code / http.status_code) — Errors ve Rate boş.")
	} else {
		fmt.Fprintf(&b, " Hata = %s 5xx; 4xx hata sayılmaz.", r.StatusKey)
	}
	if r.LatencyUnitKnown {
		fmt.Fprintf(&b, " Süre birimi %s", r.LatencyUnit)
		if r.LatencyUnitFrom == "name" {
			b.WriteString(" (metrik adından)")
		}
		b.WriteString(".")
	} else {
		b.WriteString(" Süre birimi BİLİNMİYOR: ham değer gösteriliyor.")
	}
	if r.EnvAmbiguous {
		b.WriteString(" env filtresi bu depoda ifade edilemiyor, liste TÜM ortamları kapsıyor.")
	}
	if r.ClusterIgnored {
		b.WriteString(" cluster filtresi metrikte yok, uygulanmadı.")
	}
	b.WriteString(" Exemplar kısayolları (⚡/✖) metrikte yok.")
	if len(r.Partial) > 0 {
		b.WriteString(" Eksik: " + strings.Join(r.Partial, "; ") + ".")
	}
	return b.String()
}

// getEndpointsMetric — GET /api/endpoints/metric
func (s *Server) getEndpointsMetric(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	from, to := parseFromTo(r, time.Hour)
	limit := parseInt(q.Get("limit"), 500)
	if limit <= 0 {
		limit = 500
	}
	if limit > 10000 {
		limit = 10000
	}
	p := endpointsMetricPlan{
		From: from, To: to,
		Service: strings.TrimSpace(q.Get("service")), Search: q.Get("search"),
		Env: strings.TrimSpace(q.Get("env")), Cluster: strings.TrimSpace(q.Get("cluster")),
		Limit: limit, Compare: q.Get("compare") == "prior",
		BySignature: q.Get("groupBy") == "signature",
		Sort:        q.Get("sort"), Dir: q.Get("dir"),
		StepSec: endpointsMetricStep(from, to),
	}
	if p.Sort == "p99Delta" {
		p.Compare = true // delta prior'suz tanımsız (span yoluyla aynı)
	}
	src, err := s.metricSourceFor(r)
	if err != nil {
		writeErr(w, err)
		return
	}
	mx := s.store.MetricExclusions().Digest()
	key := endpointsMetricKey(p, src.Name(), mx)
	s.serveCached(w, r, key, 30*time.Second, func(ctx context.Context) (any, error) {
		id, ok := s.endpointsMetricIdentity(ctx, src, p.Service, p.Env)
		if !ok {
			resp := endpointsMetricResponse{
				endpointsListResponse: endpointsListResponse{Rows: []chstore.EndpointRow{}},
				Source:                endpointsMetricSource, Backend: src.Name(), Tried: id.Tried, StepSeconds: p.StepSec,
			}
			resp.Note = endpointsMetricNote(resp, p)
			return resp, nil
		}
		resp, err := buildEndpointsMetric(ctx, src, id, p)
		if err != nil {
			return nil, err
		}
		if p.Compare {
			prior := p
			dur := to.Sub(from)
			prior.From, prior.To = from.Add(-dur), from
			if pr, perr := buildEndpointsMetric(ctx, src, id, prior); perr == nil {
				mergePriorEndpointRows(resp.Rows, pr.Rows)
			} else {
				resp.Partial = append(resp.Partial, "prior: "+perr.Error())
			}
		}
		endpointsMetricSort(resp.Rows, p.Sort, p.Dir)
		if len(resp.Rows) > p.Limit {
			resp.Rows = resp.Rows[:p.Limit]
		}
		resp.Note = endpointsMetricNote(resp, p)
		return resp, nil
	})
}
