package api

// span_metric_batch.go — v0.10.655. POST /api/spans/metric-batch handler'ı ve
// cache anahtarı api.go'dan buraya (api.go BÜYÜMEZ: ratchet). Kayıt satırı
// api.go'daki registerRoutes içinde kaldı (tek satır).
//
// v0.10.655 (operatör, prod: "filtreli sorguda 34 trace çıkıyor ama histogram
// milyon gösteriyor"): gövde `filterGroup` alır (gruplu OR / iç içe yüklem,
// /traces + /aggregate + /facets ile aynı kodek); düz `filters` ile AND'lenir
// ve anahtara girer (hash-all-inputs, v0.5.187). /traces histogramı artık
// tabloyla aynı kümeyi çizer.

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/cilcenk/coremetry/internal/chstore"
	"hash/fnv"
	"net/http"
	"sort"
	"strconv"
	"time"
)

// spanMetricBatchFilterKeyInput — SAF: düz filters JSON'u ile filterGroup'u tek
// anahtar girdisine katlar. NUL ayırıcı: filters JSON'unun içinde "fg=" geçse
// bile çakışma yok. filterGroup boşsa girdi bayt bayt eski hâliyle aynı
// (mevcut anahtarlar/önbellek girdileri değişmez).
func spanMetricBatchFilterKeyInput(filters, filterGroup string) string {
	if filterGroup == "" {
		return filters
	}
	return filters + "\x00fg=" + filterGroup
}

// spanMetricBatch runs N aggregations over the same span
// selection in a single CH query. The Service detail page's
// RED chart row used to fire 3 separate /api/spans/metric
// calls (rate, error_rate, p99) — each scanning the same
// spans, just running a different aggregation. This endpoint
// collapses all three into one pass, dropping cold-cache
// time from ~3× to ~1× single. Compare-period fires the same
// reduction on the second chart row.
//
// Request body shape:
//
//	{
//	  "from":  <unix ns>, "to":  <unix ns>,
//	  "step":  <seconds | 0 for auto>,
//	  "groupBy": ["name", ...],
//	  "filters": [{"key":"service.name","op":"=","values":["foo"]}, ...],
//	  "dsl":   "service.name = 'foo'",   // optional, OR with filters
//	  "aggs": [
//	    {"name":"rate",       "agg":"rate"},
//	    {"name":"error_rate", "agg":"error_rate"},
//	    {"name":"p99",        "agg":"p99", "field":"duration_ms"}
//	  ]
//	}
//
// Response:
//
//	{ "rate": [SpanMetricSeries…],
//	  "error_rate": […],
//	  "p99": […] }
func (s *Server) spanMetricBatch(w http.ResponseWriter, r *http.Request) {
	var body struct {
		From    int64           `json:"from"`
		To      int64           `json:"to"`
		Step    int             `json:"step"`
		GroupBy []string        `json:"groupBy"`
		Filters json.RawMessage `json:"filters"`
		// FilterGroup — v0.10.655: gruplu (OR / iç içe) yüklem; /traces
		// histogramı tabloyla aynı kümeyi çizsin. Düz filters ile AND.
		FilterGroup string `json:"filterGroup"`
		DSL         string `json:"dsl"`
		// Search (v0.9.601) — serbest metin yüklemi. spanMetric'in
		// ?search= parametresiyle AYNI alana gidiyor; eksikliği bu
		// yüzeyi /traces için kullanılamaz kılıyordu: hacim şeridi
		// aramayı yok sayıp filtrelenmemiş seriyi çizerdi, tablo ise
		// filtreli sonucu gösterirdi — sessiz ve yanıltıcı bir ayrışma.
		//
		// Maliyet sınıfı: Search != "" iken batch yolu da MV/rollup
		// fast-path'lerini ATLAR (QuerySpanMetricMulti'deki fastPathOK
		// kapısı) — tek-agg yoluyla aynı davranış.
		//
		// ⚠ v0.9.601'de bu yorum spanmetric.go:157'yi gerekçe
		// gösteriyordu; o satır TEK-AGG yolunu yönetiyor, bu çağrı
		// yolunu DEĞİL. Batch tarafında kapı YOKTU ve arama sessizce
		// düşebiliyordu (v0.9.618 ekledi). Yanlış gerekçe, eksik
		// kapıdan tehlikeliydi: okuyan "korunuyor" sanıp bir daha
		// bakmaz.
		Search string `json:"search"`
		// v0.10.484 — /traces Root / Errors bayrakları histogramda da (tablo ile aynı küme).
		RootOnly bool `json:"rootOnly"`
		HasError bool `json:"hasError"`
		// v0.9.391 (grafik-audit Faz B) — panel nokta bütçesi; 0 = eski
		// davranış + 2000 emniyet tavanı. queryMetric ile aynı clamp.
		MaxDataPoints int `json:"maxDataPoints"`
		// v0.9.723 — Prometheus rate[W] kayan penceresi (saniye).
		// 0 = kapalı. Clamp [0,600] chstore'da (spanMetricWindow).
		RateWindow int `json:"rateWindow"`
		Aggs       []struct {
			Name  string `json:"name"`
			Agg   string `json:"agg"`
			Field string `json:"field"`
		} `json:"aggs"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "invalid JSON: "+err.Error(), http.StatusBadRequest)
		return
	}
	if len(body.Aggs) == 0 {
		http.Error(w, "at least one agg required", http.StatusBadRequest)
		return
	}
	filters, err := parseFiltersAndDSL(string(body.Filters), body.DSL)
	if err == nil && body.HasError {
		// v0.10.489 (Astra #5) — hata bayrağı ÇİP olarak: dar rollup fast-path'i
		// status_code boyutunu bilir; bool olarak geçseydi ham yola düşerdi.
		filters = append(filters, chstore.FilterExpr{Key: "status", Op: "=", Values: []string{"error"}})
	}
	if err != nil {
		http.Error(w, "invalid query DSL: "+err.Error(), http.StatusBadRequest)
		return
	}
	root, gerr := parseFilterGroup(body.FilterGroup) // v0.10.655
	if gerr != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid filterGroup: "+gerr.Error())
		return
	}
	specs := make([]chstore.SpanMetricAggSpec, len(body.Aggs))
	for i, a := range body.Aggs {
		specs[i] = chstore.SpanMetricAggSpec{
			Name:        a.Name,
			Aggregation: a.Agg,
			Field:       a.Field,
		}
	}
	if body.MaxDataPoints < 0 {
		body.MaxDataPoints = 0
	}
	if body.MaxDataPoints > 4000 {
		body.MaxDataPoints = 4000
	}
	f := chstore.SpanMetricBatchFilter{
		Filters:       filters,
		FilterRoot:    root, // v0.10.655
		Search:        body.Search,
		RootOnly:      body.RootOnly, // v0.10.484
		HasError:      body.HasError,
		GroupBy:       body.GroupBy,
		From:          time.Unix(0, body.From),
		To:            time.Unix(0, body.To),
		StepSeconds:   body.Step,
		MaxDataPoints: body.MaxDataPoints,
		RateWindowSec: body.RateWindow,
		Aggs:          specs,
	}
	// v0.9.229 — this endpoint had NO cache at all. It is the Service
	// Overview's main payload (the page fires TWO of these: the RED bundle
	// and the kafka-excluded latency set), and every load, range change and
	// remount ran the full multi-aggregation over raw spans. Measured on a
	// 9-service local demo the repeats never dropped below ~80ms because
	// nothing was ever served from cache; at real scale that is the page's
	// dominant cost. Operator: "Service Overview eskiye göre daha geç
	// yükleniyor / redisi çok kullandığımızı düşünmüyorum" — for this
	// endpoint that was literally true.
	//
	// 30s soft TTL matches the client's staleTime, so a back-navigation
	// inside the window is free; serveCached's SWR keeps it usable to 90s
	// with a background refresh, and there is no poll on this query so the
	// TTL×staleFactor ≤ interval trap (v0.9.228) can't apply here.
	// v0.9.391 — yanıt zarfı {series, stepSeconds}: frontend bucket
	// genişliğini artık TAHMİN etmiyor (bar genişliği/gap eşiği için
	// sözleşme; rollup /api/rollup/red planıyla AYNI kontrat).
	s.serveCached(w, r, spanMetricBatchKey(body.From, body.To, body.Step, body.MaxDataPoints,
		body.RateWindow, body.GroupBy, spanMetricBatchFilterKeyInput(string(body.Filters), body.FilterGroup), body.DSL, body.Search, specs, body.RootOnly, body.HasError), 30*time.Second,
		func(ctx context.Context) (any, error) {
			series, stepSec, err := s.store.QuerySpanMetricMulti(ctx, f)
			if err != nil {
				return nil, err
			}
			return map[string]any{"series": series, "stepSeconds": stepSec}, nil
		})
}

// spanMetricBatchKey hashes EVERY input that changes the result: the
// minute-bucketed window (raw ns would never repeat, so an unbucketed key
// caches nothing), step, group-by set, the filter JSON, the DSL, and each
// agg's (name, aggregation, field) triple.
//
// The aggs are sorted before hashing so two callers asking for the same
// metrics in a different order share one entry, and every component is
// NUL-separated — without the separator "rate"+"p99" and "ratep"+"99"
// would collide, the v0.5.187 rule applied to strings. Fields are hashed
// individually rather than summarised by count, which is the same rule
// applied to the set.
// v0.10.489 (Astra #11) — bayraklar ayrı alan olarak hash'lenir (search'e
// gizlenmiş kuyruk yerine); ikisi de boşken anahtar eski anahtarla aynı.
func spanMetricBatchKey(fromNs, toNs int64, step, maxDataPoints, rateWindow int, groupBy []string,
	filters, dsl, search string, aggs []chstore.SpanMetricAggSpec, flags ...bool) string {
	h := fnv.New64a()
	write := func(parts ...string) {
		for _, p := range parts {
			h.Write([]byte(p))
			h.Write([]byte{0})
		}
	}
	gb := append([]string(nil), groupBy...)
	sort.Strings(gb)
	for _, g := range gb {
		write("gb", g)
	}
	// v0.9.601 — search ANAHTARA GİRER. Girmeseydi iki farklı arama aynı
	// önbellek girdisini paylaşırdı: operatör "timeout" arar, sonraki
	// "refused" arar ve ilkinin serisini görür. CLAUDE.md sert kısıtı
	// (v0.5.187 çapraz-zehirlenme) — anahtar TÜM girdileri hash'ler.
	write("f", filters, "dsl", dsl, "q", search)
	// v0.10.484/489 — Root / Errors bayrakları (CLAUDE.md: anahtar TÜM girdileri
	// hash'ler; girmeseydi kök-yalnız seri tüm-span serisiyle çapraz zehirlenirdi).
	if len(flags) >= 2 && (flags[0] || flags[1]) {
		write("root", strconv.FormatBool(flags[0]), "err", strconv.FormatBool(flags[1]))
	}
	// v0.9.391 — mdp key'de: farklı genişlikteki paneller farklı çözünürlük
	// ister; key'e girmezse birbirinin çözünürlüğünü zehirler (v0.5.187).
	write("mdp", strconv.Itoa(maxDataPoints))
	// v0.9.723 — rateWindow anahtara girer: pencereli/penceresiz seri
	// kümeleri farklıdır; girmese v2 panel ile eski panel birbirini
	// zehirlerdi (v0.5.187 kuralı).
	write("rw", strconv.Itoa(rateWindow))
	specs := append([]chstore.SpanMetricAggSpec(nil), aggs...)
	sort.Slice(specs, func(i, j int) bool { return specs[i].Name < specs[j].Name })
	for _, a := range specs {
		write("a", a.Name, a.Aggregation, a.Field)
	}
	return fmt.Sprintf("span-metric-batch:from=%d:to=%d:step=%d:h=%x",
		time.Unix(0, fromNs).Truncate(time.Minute).UnixNano(),
		time.Unix(0, toNs).Truncate(time.Minute).UnixNano(),
		step, h.Sum64())
}
