package anomaly

import (
	"context"
	"sort"
	"time"

	"github.com/cilcenk/coremetry/internal/chstore"
)

// op_latency.go — operasyon düzeyi GECİKME anomalisi (v0.9.1064,
// Faz 2.3 / G5). trace_ops hattı yalnız HATA sayıyordu: trafiğin %2'si
// olan bir endpoint 10× yavaşlarsa servis p99'u kıpırdamaz ve "hangi
// endpoint" — SRE'nin ikinci sorusu — cevapsız kalırdı.
// operation_summary_5m zaten duration_q_state taşıyor; trace_ops'un
// cur/base şekli p99'a uygulanır (aynı MV-pivot, aynı LIMIT/timeout
// disiplini, aynı dar örnek-trace ikinci sorgusu).

// OpLatencyAnomaly — kalifiye bir (servis, operasyon) gecikme sıçraması.
type OpLatencyAnomaly struct {
	Service       string  `json:"service"`
	Operation     string  `json:"operation"`
	CurP99Ms      float64 `json:"curP99Ms"`
	BaseP99Ms     float64 `json:"baseP99Ms"`
	Ratio         float64 `json:"ratio"` // cur/base
	CurCalls      uint64  `json:"curCalls"`
	SampleTraceID string  `json:"sampleTraceId"` // en yavaş cari span'in trace'i
	LastSeenNs    int64   `json:"lastSeenNs"`
}

// Kalifikasyon eşikleri — trace_ops'un v0.9.327 felsefesiyle aynı:
// küçük sayılar üstünde aritmetik olay değildir.
const (
	// opLatencyMinCalls — İKİ pencerede de hacim tabanı. 30 çağrının
	// p99'u tek kuyruk isteğidir; baseline tarafında da aynı taban
	// (oturmamış baseline'a karşı oran anlamsız).
	opLatencyMinCalls = 30
	// opLatencyMinRatio — günlük dalgalanmanın dışı. Hata hattının
	// traceOpMinRatio'suyla aynı sayı; iki dedektör "sıçrama" için
	// aynı şeyi söylesin.
	opLatencyMinRatio = 3.0
	// opLatencyMinP99Ms — mutlak taban. 20ms'lik bir operasyonun 3×'i
	// filo ölçeğinde operatör dikkati değildir; 200ms üstü kuyruk
	// gerçek kullanıcı acısıdır.
	opLatencyMinP99Ms = 200.0
)

// opLatencyBucket — MV pivotunun ham satırı (saf sınıflayıcı girdisi).
type opLatencyBucket struct {
	Service   string
	Operation string
	CurP99Ms  float64
	BaseP99Ms float64
	CurCalls  uint64
	BaseCalls uint64
}

// classifyOpLatency — kesin eşikler + sıralama (saf, tablo-testli;
// classifyTraceOps'un ikizi). SQL'in kaba HAVING'i LIMIT'i anlamlı
// tutar; nihai hüküm burada.
func classifyOpLatency(rows []opLatencyBucket) []OpLatencyAnomaly {
	out := []OpLatencyAnomaly{}
	for _, r := range rows {
		if r.CurCalls < opLatencyMinCalls || r.BaseCalls < opLatencyMinCalls {
			continue
		}
		if r.BaseP99Ms <= 0 || r.CurP99Ms < opLatencyMinP99Ms {
			continue
		}
		ratio := r.CurP99Ms / r.BaseP99Ms
		if ratio < opLatencyMinRatio {
			continue
		}
		out = append(out, OpLatencyAnomaly{
			Service:   r.Service,
			Operation: r.Operation,
			CurP99Ms:  r.CurP99Ms,
			BaseP99Ms: r.BaseP99Ms,
			Ratio:     ratio,
			CurCalls:  r.CurCalls,
		})
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Ratio != out[j].Ratio {
			return out[i].Ratio > out[j].Ratio
		}
		return out[i].CurCalls > out[j].CurCalls
	})
	if len(out) > 50 {
		out = out[:50]
	}
	return out
}

// DetectOpLatencyAnomalies — cari pencere p99 vs 24h (ya da 12×pencere)
// kuyruk baseline p99, operation_summary_5m üzerinden tek pivot geçişi.
// Örnek trace yalnız kalifiye ≤50 çift için dar, PK-prefix'li raw
// sorgudan (DetectTraceOpAnomalies ile aynı bedel modeli; pencereler 5m
// bucket'a hizalı, tespit ≤~5dk gecikir — kabul edilmiş desen).
func DetectOpLatencyAnomalies(ctx context.Context, store *chstore.Store, window time.Duration) ([]OpLatencyAnomaly, error) {
	conn := store.TelemetryReadConn()
	now := time.Now()

	alignedNow := now.Truncate(traceOpBucketLen)
	curBuckets := int(window / traceOpBucketLen)
	if curBuckets < 1 {
		curBuckets = 1
	}
	curWindow := time.Duration(curBuckets) * traceOpBucketLen
	curStart := alignedNow.Add(-curWindow)
	baseLookback := 24 * time.Hour
	if 12*curWindow > baseLookback {
		baseLookback = 12 * curWindow
	}
	baseStart := curStart.Add(-baseLookback)

	rows, err := conn.Query(ctx, `
		SELECT service_name, name,
		       maxIf(p99, is_cur = 1)   AS cur_p99,
		       maxIf(p99, is_cur = 0)   AS base_p99,
		       sumIf(calls, is_cur = 1) AS cur_calls,
		       sumIf(calls, is_cur = 0) AS base_calls
		FROM (
		  SELECT service_name, name,
		         time_bucket >= ? AS is_cur,
		         arrayElement(quantilesTDigestMerge(0.5, 0.95, 0.99)(duration_q_state), 3) / 1e6 AS p99,
		         countMerge(span_count_state) AS calls
		  FROM operation_summary_5m
		  WHERE time_bucket >= ? AND time_bucket < ?
		  GROUP BY service_name, name, is_cur
		)
		GROUP BY service_name, name
		HAVING cur_calls >= ? AND base_calls >= ?
		   AND base_p99 > 0 AND cur_p99 >= ? * base_p99 AND cur_p99 >= ?
		ORDER BY cur_p99 / base_p99 DESC
		LIMIT 200
		SETTINGS max_execution_time = 25`,
		curStart, baseStart, alignedNow,
		opLatencyMinCalls, opLatencyMinCalls, opLatencyMinRatio, opLatencyMinP99Ms,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	buckets := []opLatencyBucket{}
	for rows.Next() {
		var b opLatencyBucket
		if err := rows.Scan(&b.Service, &b.Operation, &b.CurP99Ms, &b.BaseP99Ms, &b.CurCalls, &b.BaseCalls); err != nil {
			return nil, err
		}
		buckets = append(buckets, b)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	out := classifyOpLatency(buckets)
	if len(out) == 0 {
		return out, nil
	}

	// Örnek: cari penceredeki EN YAVAŞ span'in trace'i (hata hattının
	// argMax(trace_id, time)'ından farkla duration-argMax — gecikme
	// olayının temsilcisi en yavaş istek). Aynı üst-küme IN deseni.
	svcs := make([]string, 0, len(out))
	ops := make([]string, 0, len(out))
	for _, a := range out {
		svcs = append(svcs, a.Service)
		ops = append(ops, a.Operation)
	}
	srows, err := conn.Query(ctx, `
		SELECT service_name, name,
		       argMax(trace_id, duration)       AS sample,
		       toUnixTimestamp64Nano(max(time)) AS last_ns
		FROM spans
		WHERE time >= ? AND time < ?
		  AND service_name IN ?
		  AND name IN ?
		GROUP BY service_name, name
		LIMIT 2500
		SETTINGS max_execution_time = 10`,
		curStart, alignedNow, svcs, ops)
	if err != nil {
		// Örnek yoksa anomaliler yine döner — sample best-effort.
		return out, nil
	}
	defer srows.Close()
	type sk struct{ s, o string }
	samples := map[sk]struct {
		trace string
		last  int64
	}{}
	for srows.Next() {
		var s, o, tr string
		var last int64
		if err := srows.Scan(&s, &o, &tr, &last); err != nil {
			continue
		}
		samples[sk{s, o}] = struct {
			trace string
			last  int64
		}{tr, last}
	}
	for i := range out {
		if sm, ok := samples[sk{out[i].Service, out[i].Operation}]; ok {
			out[i].SampleTraceID = sm.trace
			out[i].LastSeenNs = sm.last
		}
	}
	return out, nil
}
