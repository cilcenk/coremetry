// metric_rollup_route_read_test.go — v0.9.777, 0008 route tier'ının SAF
// çekirdekleri. Canlı CH gerekmez.
package chstore

import (
	"strconv"
	"strings"
	"testing"
	"time"
)

// ─────────────────────── promoteServiceFilter ───────────────────────

func TestPromoteServiceFilter(t *testing.T) {
	base := MetricQueryFilter{Name: "http.server.duration", GroupBy: []string{"http.route"}}

	withFilters := func(fs ...FilterExpr) MetricQueryFilter {
		f := base
		f.Filters = fs
		return f
	}

	tests := []struct {
		name        string
		in          MetricQueryFilter
		wantOK      bool
		wantService string
	}{
		{
			name:        "service.name çipi terfi eder",
			in:          withFilters(FilterExpr{Key: "service.name", Op: "=", Values: []string{"api-gateway"}}),
			wantOK:      true,
			wantService: "api-gateway",
		},
		{
			name:        "service_name yazımı da terfi eder",
			in:          withFilters(FilterExpr{Key: "service_name", Op: "=", Values: []string{"web-bff"}}),
			wantOK:      true,
			wantService: "web-bff",
		},
		{
			name:        "kısa 'service' anahtarı",
			in:          withFilters(FilterExpr{Key: "service", Op: "=", Values: []string{"web-bff"}}),
			wantOK:      true,
			wantService: "web-bff",
		},
		{
			// Explore filtreleri resource. önekiyle de gelebiliyor.
			name:        "resource.service.name",
			in:          withFilters(FilterExpr{Key: "resource.service.name", Op: "=", Values: []string{"tpp-gateway"}}),
			wantOK:      true,
			wantService: "tpp-gateway",
		},
		{
			// EN ÖNEMLİ RET: ikinci çipi düşürmek rollup'tan DAHA GENİŞ bir
			// küme okumak, yani yanlış sayı demekti.
			name: "iki çip → terfi YOK",
			in: withFilters(
				FilterExpr{Key: "service.name", Op: "=", Values: []string{"api-gateway"}},
				FilterExpr{Key: "http.method", Op: "=", Values: []string{"GET"}},
			),
			wantOK: false,
		},
		{
			name:   "!= operatörü → terfi YOK",
			in:     withFilters(FilterExpr{Key: "service.name", Op: "!=", Values: []string{"api-gateway"}}),
			wantOK: false,
		},
		{
			name:   "IN operatörü → terfi YOK",
			in:     withFilters(FilterExpr{Key: "service.name", Op: "IN", Values: []string{"a", "b"}}),
			wantOK: false,
		},
		{
			// '=' ama çok değerli — kısayol tek servis taşıyor.
			name:   "= ama iki değer → terfi YOK",
			in:     withFilters(FilterExpr{Key: "service.name", Op: "=", Values: []string{"a", "b"}}),
			wantOK: false,
		},
		{
			name:   "boş değer → terfi YOK",
			in:     withFilters(FilterExpr{Key: "service.name", Op: "=", Values: []string{""}}),
			wantOK: false,
		},
		{
			name:   "servis olmayan anahtar → terfi YOK",
			in:     withFilters(FilterExpr{Key: "http.route", Op: "=", Values: []string{"/x"}}),
			wantOK: false,
		},
		{
			name:   "hiç filtre yok → terfi YOK",
			in:     base,
			wantOK: false,
		},
		{
			name: "Service zaten dolu → terfi YOK",
			in: func() MetricQueryFilter {
				f := withFilters(FilterExpr{Key: "service.name", Op: "=", Values: []string{"x"}})
				f.Service = "already"
				return f
			}(),
			wantOK: false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			// GİRDİ KOPYASI: terfi çağıranın filtresini MUTASYONA
			// UĞRATMAMALI. Ham yol aynı f'i kullanıyor; burada bir
			// paylaşılan slice kırpılsaydı ham SQL sessizce değişirdi.
			inFilters := append([]FilterExpr{}, tc.in.Filters...)
			inService := tc.in.Service

			got, ok := promoteServiceFilter(tc.in)
			if ok != tc.wantOK {
				t.Fatalf("ok = %v, beklenen %v", ok, tc.wantOK)
			}

			if len(tc.in.Filters) != len(inFilters) || tc.in.Service != inService {
				t.Fatalf("GİRDİ değişti: Service %q→%q, filtre %d→%d",
					inService, tc.in.Service, len(inFilters), len(tc.in.Filters))
			}
			for i := range inFilters {
				if tc.in.Filters[i].Key != inFilters[i].Key ||
					tc.in.Filters[i].Op != inFilters[i].Op ||
					strings.Join(tc.in.Filters[i].Values, "\x00") != strings.Join(inFilters[i].Values, "\x00") {
					t.Fatalf("GİRDİ filtresi[%d] değişti", i)
				}
			}

			if !tc.wantOK {
				if got.Service != tc.in.Service || len(got.Filters) != len(tc.in.Filters) {
					t.Errorf("ret durumunda f aynen dönmeli, %+v döndü", got)
				}
				return
			}
			if got.Service != tc.wantService {
				t.Errorf("Service = %q, beklenen %q", got.Service, tc.wantService)
			}
			if len(got.Filters) != 0 {
				t.Errorf("çip listeden düşmedi: %+v", got.Filters)
			}
		})
	}
}

// ─────────────────────── metricRollupRoutePlan ───────────────────────

// routePlanBase — her ret koşulunun TEK BAŞINA test edilebilmesi için
// geçerli bir taban. Bu taban plan'ı AÇMALI (ilk alt-test bunu pinler).
func routePlanBase() MetricQueryFilter {
	now := time.Date(2026, 8, 8, 12, 0, 0, 0, time.UTC)
	return MetricQueryFilter{
		Name:        "http.server.duration",
		Service:     "api-gateway",
		GroupBy:     []string{"http.route"},
		Aggregation: "avg",
		StepSeconds: 60,
		From:        now.Add(-2 * time.Hour),
		To:          now,
	}
}

func TestMetricRollupRoutePlan(t *testing.T) {
	now := time.Date(2026, 8, 8, 12, 0, 0, 0, time.UTC)

	mut := func(fn func(*MetricQueryFilter)) MetricQueryFilter {
		f := routePlanBase()
		fn(&f)
		return f
	}

	tests := []struct {
		name        string
		f           MetricQueryFilter
		instrument  string
		temporality string
		wantOK      bool
		wantTable   string
	}{
		{
			name: "taban: histogram+delta, 1m kademe",
			f:    routePlanBase(), instrument: "histogram", temporality: "delta",
			wantOK: true, wantTable: "rollup_metrics_route_1m",
		},
		{
			name: "gauge temporality'ye bakmaz",
			f:    routePlanBase(), instrument: "gauge", temporality: "",
			wantOK: true, wantTable: "rollup_metrics_route_1m",
		},
		{
			name: "sum + delta açılır",
			f:    routePlanBase(), instrument: "sum", temporality: "delta",
			wantOK: true, wantTable: "rollup_metrics_route_1m",
		},
		{
			name: "sum + cumulative RET",
			f:    routePlanBase(), instrument: "sum", temporality: "cumulative",
			wantOK: false,
		},
		{
			name: "histogram + cumulative RET",
			f:    routePlanBase(), instrument: "histogram", temporality: "cumulative",
			wantOK: false,
		},
		{
			name: "exp_histogram + delta açılır",
			f:    routePlanBase(), instrument: "exp_histogram", temporality: "delta",
			wantOK: true, wantTable: "rollup_metrics_route_1m",
		},
		{
			name: "bilinmeyen enstrüman RET",
			f:    routePlanBase(), instrument: "summary", temporality: "delta",
			wantOK: false,
		},
		{
			name: "metrik adı boş RET",
			f:    mut(func(f *MetricQueryFilter) { f.Name = "" }),
			instrument: "gauge", wantOK: false,
		},
		{
			name: "servis boş RET (terfi de kurtaramadıysa ham)",
			f:    mut(func(f *MetricQueryFilter) { f.Service = "" }),
			instrument: "gauge", wantOK: false,
		},
		{
			name: "kalan filtre RET",
			f: mut(func(f *MetricQueryFilter) {
				f.Filters = []FilterExpr{{Key: "http.method", Op: "=", Values: []string{"GET"}}}
			}),
			instrument: "gauge", wantOK: false,
		},
		{
			name:       "Instance RET",
			f:          mut(func(f *MetricQueryFilter) { f.Instance = "db-1" }),
			instrument: "gauge", wantOK: false,
		},
		{
			name:       "Engine RET",
			f:          mut(func(f *MetricQueryFilter) { f.Engine = "postgresql" }),
			instrument: "gauge", wantOK: false,
		},
		{
			name:       "gruplama yok RET",
			f:          mut(func(f *MetricQueryFilter) { f.GroupBy = nil }),
			instrument: "gauge", wantOK: false,
		},
		{
			name:       "iki grup anahtarı RET",
			f:          mut(func(f *MetricQueryFilter) { f.GroupBy = []string{"http.route", "http.method"} }),
			instrument: "gauge", wantOK: false,
		},
		{
			name:       "başka tek anahtar RET",
			f:          mut(func(f *MetricQueryFilter) { f.GroupBy = []string{"http.method"} }),
			instrument: "gauge", wantOK: false,
		},
		{
			// 0008'de last_value kolonu YOK.
			name:       "agg=last RET",
			f:          mut(func(f *MetricQueryFilter) { f.Aggregation = "last" }),
			instrument: "gauge", wantOK: false,
		},
		{
			// 0008'de bucket YOK — yüzdelik vaadi yok.
			name:       "agg=p95 RET",
			f:          mut(func(f *MetricQueryFilter) { f.Aggregation = "p95" }),
			instrument: "histogram", temporality: "delta", wantOK: false,
		},
		{
			name:       "agg boş = avg, açılır",
			f:          mut(func(f *MetricQueryFilter) { f.Aggregation = "" }),
			instrument: "gauge", wantOK: true, wantTable: "rollup_metrics_route_1m",
		},
		{
			name:       "agg=sum/min/max açılır (min)",
			f:          mut(func(f *MetricQueryFilter) { f.Aggregation = "min" }),
			instrument: "gauge", wantOK: true, wantTable: "rollup_metrics_route_1m",
		},
		{
			name:       "step=0 (auto) RET",
			f:          mut(func(f *MetricQueryFilter) { f.StepSeconds = 0 }),
			instrument: "gauge", wantOK: false,
		},
		{
			// 90 sn hiçbir grain'e (3600/300/60) tam bölünmez.
			name:       "step grain'e bölünmüyor RET",
			f:          mut(func(f *MetricQueryFilter) { f.StepSeconds = 90 }),
			instrument: "gauge", wantOK: false,
		},
		{
			name:       "step=300 → 5m kademesi",
			f:          mut(func(f *MetricQueryFilter) { f.StepSeconds = 300 }),
			instrument: "gauge", wantOK: true, wantTable: "rollup_metrics_route_5m",
		},
		{
			name:       "step=3600 → 1h kademesi (en kaba önce)",
			f:          mut(func(f *MetricQueryFilter) { f.StepSeconds = 3600 }),
			instrument: "gauge", wantOK: true, wantTable: "rollup_metrics_route_1h",
		},
		{
			// 1m TTL'i 14 gün; 30 günlük pencere yalnız daha uzun TTL'li
			// kademelerde karşılanır ve step=60 onlara bölünmez.
			name: "30 günlük pencere + step=60 RET (1m TTL 14g)",
			f: mut(func(f *MetricQueryFilter) {
				f.From = now.Add(-30 * 24 * time.Hour)
			}),
			instrument: "gauge", wantOK: false,
		},
		{
			// step=300 → 5m (TTL 90g) 30 günü taşır.
			name: "30 günlük pencere + step=300 → 5m",
			f: mut(func(f *MetricQueryFilter) {
				f.From = now.Add(-30 * 24 * time.Hour)
				f.StepSeconds = 300
			}),
			instrument: "gauge", wantOK: true, wantTable: "rollup_metrics_route_5m",
		},
		{
			// 200 gün: 5m'in 90g TTL'ini aşar, 1h'e (13 ay) düşer —
			// ama step=300 3600'e bölünmez, dolayısıyla RET.
			name: "200 günlük pencere + step=300 RET",
			f: mut(func(f *MetricQueryFilter) {
				f.From = now.Add(-200 * 24 * time.Hour)
				f.StepSeconds = 300
			}),
			instrument: "gauge", wantOK: false,
		},
		{
			name: "200 günlük pencere + step=3600 → 1h",
			f: mut(func(f *MetricQueryFilter) {
				f.From = now.Add(-200 * 24 * time.Hour)
				f.StepSeconds = 3600
			}),
			instrument: "gauge", wantOK: true, wantTable: "rollup_metrics_route_1h",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			tr, ok := metricRollupRoutePlan(tc.f, tc.instrument, tc.temporality, now)
			if ok != tc.wantOK {
				t.Fatalf("ok = %v, beklenen %v (tablo %q)", ok, tc.wantOK, tr.table)
			}
			if ok && tr.table != tc.wantTable {
				t.Errorf("tablo = %q, beklenen %q", tr.table, tc.wantTable)
			}
			// Şekil ön-elemesi plan'dan DAHA GEVŞEK olamaz: gevşek olsaydı
			// plan'ın reddettiği bir sorgu için boşuna iki CH probu atardık;
			// SIKI olsaydı plan hiç çağrılmadan tier sessizce kapanırdı.
			if ok && !rollupRouteShapeOK(tc.f) {
				t.Error("plan açıldı ama rollupRouteShapeOK false — ön eleme tier'ı sessizce kapatır")
			}
		})
	}
}

// ─────────────────────── SQL pinleri ───────────────────────

func TestRollupRouteAggExpr(t *testing.T) {
	tests := []struct {
		agg, instrument string
		want            string
		wantOK          bool
	}{
		// avg'ın İKİ dalı — v0.9.776 düzeltmesinin rollup ikizi.
		{"avg", "histogram", "sum(sum_value_sum) / nullIf(sum(obs_count), 0)", true},
		{"avg", "exp_histogram", "sum(sum_value_sum) / nullIf(sum(obs_count), 0)", true},
		{"", "histogram", "sum(sum_value_sum) / nullIf(sum(obs_count), 0)", true},
		{"avg", "gauge", "sum(value_sum) / nullIf(sum(point_count), 0)", true},
		{"avg", "sum", "sum(value_sum) / nullIf(sum(point_count), 0)", true},
		{"", "gauge", "sum(value_sum) / nullIf(sum(point_count), 0)", true},
		// Toplamsal/uç agg'ler enstrümandan bağımsız.
		{"sum", "histogram", "sum(value_sum)", true},
		{"min", "gauge", "min(value_min)", true},
		{"max", "gauge", "max(value_max)", true},
		{"MAX", "gauge", "max(value_max)", true},
		// 0008'in taşımadıkları.
		{"last", "gauge", "", false},
		{"p95", "histogram", "", false},
		{"bilinmeyen", "gauge", "", false},
	}
	for _, tc := range tests {
		t.Run(tc.agg+"/"+tc.instrument, func(t *testing.T) {
			got, ok := rollupRouteAggExpr(tc.agg, tc.instrument)
			if ok != tc.wantOK {
				t.Fatalf("ok = %v, beklenen %v", ok, tc.wantOK)
			}
			if got != tc.want {
				t.Errorf("= %q, beklenen %q", got, tc.want)
			}
		})
	}
}

// TestBuildRollupRouteSQL_Parity — ham yolla parite noktalarının PİNİ.
// Bunlardan biri kayarsa kademe geçişi panelde görünür bir kırılma yapar
// (1000× kaymış zaman ekseni, değişen lejant, farklı kırpma eşiği).
func TestBuildRollupRouteSQL_Parity(t *testing.T) {
	sql := buildRollupRouteSQL("rollup_metrics_route_5m", "sum(value_sum)")

	mustContain := []string{
		// Kaynak tablo.
		"FROM rollup_metrics_route_5m",
		// service_key ÇOKLU aday (env ekli/eksiz) — tek '=' değil.
		"service_key IN (?)",
		// Sessiz ms/s karışımına kapı yok.
		"instrument = ?",
		"unit = ?",
		// CH-bounds: zaman sınırı + tavan + duvar saati.
		"ts >= ?",
		"ts <= ?",
		"LIMIT 50000",
		"max_execution_time = 10",
		// Bucket NANOSANİYE (ham yol da *1000000000 yazıyor).
		"* 1000000000 AS bucket",
		// GroupKey tek elemanlı DİZİ — ham yolun strings.Join anahtarıyla
		// aynı şekil.
		"[route] AS gk",
		// Ham yolun sırası.
		"ORDER BY gk, bucket",
	}
	for _, want := range mustContain {
		if !strings.Contains(sql, want) {
			t.Errorf("SQL %q içermiyor:\n%s", want, sql)
		}
	}

	// LIMIT ham yolun tavanıyla AYNI olmalı — SeriesRowsCapped iki yolda
	// aynı eşikte "kırpıldı" demeli.
	if !strings.Contains(sql, "LIMIT "+strconv.Itoa(SpanMetricRowCap)) {
		t.Errorf("LIMIT SpanMetricRowCap(%d) değil:\n%s", SpanMetricRowCap, sql)
	}

	// Enstrümana göre agg ifadesi gövdeye giriyor mu.
	histSQL := buildRollupRouteSQL("rollup_metrics_route_1m", "sum(sum_value_sum) / nullIf(sum(obs_count), 0)")
	if !strings.Contains(histSQL, "sum(sum_value_sum) / nullIf(sum(obs_count), 0)") {
		t.Errorf("histogram avg ifadesi SQL'e girmedi:\n%s", histSQL)
	}
}

// TestRollupRouteServiceKeys — env ekli adlar için aday üretimi.
func TestRollupRouteServiceKeys(t *testing.T) {
	tests := []struct {
		in   string
		want []string
	}{
		{"api-gateway", []string{"api-gateway"}},
		{"payments-int", []string{"payments-int", "payments"}},
		{"payments-uat", []string{"payments-uat", "payments"}},
		{"payments-prod", []string{"payments-prod", "payments"}},
		{"payments-prep", []string{"payments-prep", "payments"}},
	}
	for _, tc := range tests {
		t.Run(tc.in, func(t *testing.T) {
			got := rollupRouteServiceKeys(tc.in)
			if len(got) != len(tc.want) {
				t.Fatalf("= %q, beklenen %q", got, tc.want)
			}
			for i := range got {
				if got[i] != tc.want[i] {
					t.Errorf("[%d] = %q, beklenen %q", i, got[i], tc.want[i])
				}
			}
		})
	}
}

// TestRawMetricSQLUnchangedByPromotion — EN KRİTİK PİN.
//
// promoteServiceFilter bir KOPYA döndürmezse (ya da çağıran terfi edilmiş
// f'i aşağı geçirirse) ham yolun WHERE'i `service_name = ?` kazanır ve
// filtre çipi düşer. Sonuç: rollup kapalıyken bile ham SQL değişir —
// sessiz bir davranış kayması. Bu test terfiden ÖNCE ve SONRA üretilen ham
// SQL'in ve arg sayısının BAYT-BAYT aynı kaldığını pinler.
func TestRawMetricSQLUnchangedByPromotion(t *testing.T) {
	now := time.Date(2026, 8, 8, 12, 0, 0, 0, time.UTC)
	f := MetricQueryFilter{
		Name:        "http.server.duration",
		GroupBy:     []string{"http.route"},
		Aggregation: "avg",
		StepSeconds: 60,
		From:        now.Add(-1 * time.Hour),
		To:          now,
		Filters:     []FilterExpr{{Key: "service.name", Op: "=", Values: []string{"api-gateway"}}},
	}

	before, argsBefore, err := buildMetricQuerySQL(f, now, "histogram", "delta")
	if err != nil {
		t.Fatalf("beklenmeyen hata: %v", err)
	}

	if _, ok := promoteServiceFilter(f); !ok {
		t.Fatal("terfi bekleniyordu — test kurulumu bozuk")
	}

	after, argsAfter, err := buildMetricQuerySQL(f, now, "histogram", "delta")
	if err != nil {
		t.Fatalf("beklenmeyen hata: %v", err)
	}
	if before != after {
		t.Errorf("terfi HAM SQL'i değiştirdi:\nönce:\n%s\nsonra:\n%s", before, after)
	}
	if len(argsBefore) != len(argsAfter) {
		t.Errorf("arg sayısı %d → %d", len(argsBefore), len(argsAfter))
	}
	// Çip HÂLÂ f üzerinde durmalı — terfi onu tüketmiş olsaydı ham yol
	// servis daraltmasını tamamen kaybederdi (her servisi toplayan panel).
	if len(f.Filters) != 1 || f.Filters[0].Key != "service.name" {
		t.Errorf("terfi çağıranın filtresini tüketti: %+v", f.Filters)
	}
}
