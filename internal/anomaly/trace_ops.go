package anomaly

import (
	"context"
	"sort"
	"time"

	"github.com/cilcenk/coremetry/internal/chstore"
)

// TraceOpAnomaly is a per-(service, operation) error or latency
// signal that's either brand new or up sharply over baseline.
// Different from the service-wide metric anomaly detector in
// that it pinpoints the SPECIFIC operation that's misbehaving —
// the SRE's first question after "is service X broken" is "which
// endpoint inside X".
type TraceOpAnomaly struct {
	Service        string  `json:"service"`
	Operation      string  `json:"operation"`
	Kind           string  `json:"kind"` // "new_error" | "error_spike"
	CurrentErrors  uint64  `json:"currentErrors"`
	BaselineErrors uint64  `json:"baselineErrors"`
	Ratio          float64 `json:"ratio"`         // current / max(baseline, 1)
	// CurrentCalls — the denominator the qualification now insists on
	// (v0.9.327). Shipping it means the row can say "42 errors of 3,100
	// calls" instead of a bare count the operator has to go look up.
	CurrentCalls uint64  `json:"currentCalls"`
	ErrorShare   float64 `json:"errorShare"` // CurrentErrors / CurrentCalls, 0..1
	SampleTraceID  string  `json:"sampleTraceId"` // representative trace for one-click drill-in
	LastSeenNs     int64   `json:"lastSeenNs"`
}

// traceOpBucket is one (service, operation) pair's cur/base error
// counts as read from the MV — input to the pure classifier.
type traceOpBucket struct {
	Service   string
	Operation string
	CurErrs   uint64
	BaseErrs  uint64 // RAW baseline count over the whole lookback (un-normalized)
	CurCalls  uint64 // total calls in the current window — the denominator
}

// Qualification thresholds — v0.9.327, operator (prod): "active anomalyler
// daha sıkı kuralları olsun, prodta çok daha az tetiklensin. Anomaly olmasa
// da şu an event oluşuyor."
//
// Measured at the time of the change: the median firing event carried
// current_count = 3-4, minimum 3. That is what the old floor allowed, and at
// prod volume it is not an anomaly — it is arithmetic on small numbers. A
// "12× spike" of three errors against a baseline of a quarter-error is noise
// wearing a big multiplier.
const (
	// traceOpMinErrs — absolute floor. Under a handful of errors in a
	// five-minute window, nothing above is worth an operator's attention at
	// 1000s of services.
	traceOpMinErrs = 10

	// traceOpMinRatio — a real baseline has to be beaten by more than the
	// diurnal wobble. 2× is inside normal day/night variation for most
	// operations; 3× is not.
	traceOpMinRatio = 3.0

	// traceOpMinErrShare — THE missing rule. Both detectors qualified on
	// error COUNT alone and never looked at the denominator, so 3 errors out
	// of 500,000 calls opened an event exactly like 3 errors out of 3.
	//
	// 1% is not a new invention: it is the SAME floor this codebase's metric
	// policy already applies to error_rate ("<%1 = birkaç-hata gürültüsü,
	// açma", anomaly.go metricPolicies absFloor). Extending it here makes the
	// two detectors agree about what counts as an error problem instead of
	// contradicting each other.
	traceOpMinErrShare = 0.01
)

// classifyTraceOps applies the qualification thresholds and produces
// the sorted anomaly list. Pure — the v0.8.504 MV rewrite extracted it
// from the SQL so the eşik mantığı tablo-testlidir:
//
//   - raw baseline == 0 AND current_errors >= 3 → "new_error"
//   - baseline > 0 AND current >= 2× pencere-normalize baseline → "error_spike"
//
// windowRatio = current window length / baseline lookback; the raw
// baseline is normalized with it so a 12× longer baseline doesn't
// inflate base counts and mask spikes.
func classifyTraceOps(rows []traceOpBucket, windowRatio float64) []TraceOpAnomaly {
	out := []TraceOpAnomaly{}
	for _, r := range rows {
		if r.CurErrs == 0 {
			continue
		}
		// v0.9.327 — the two floors that apply to EVERY branch, checked once
		// up front rather than repeated into each case (which is how the
		// old `>= 3` drifted between them).
		if r.CurErrs < traceOpMinErrs {
			continue
		}
		// Share floor. A zero denominator means the MV had errors but no
		// call count for the pair — refuse rather than divide: an unknown
		// denominator is not evidence of a high rate.
		if r.CurCalls == 0 || float64(r.CurErrs)/float64(r.CurCalls) < traceOpMinErrShare {
			continue
		}
		basePerWindow := uint64(float64(r.BaseErrs) * windowRatio)
		var kind string
		var ratio float64
		switch {
		case r.BaseErrs == 0:
			kind, ratio = "new_error", float64(r.CurErrs)
		case basePerWindow == 0:
			// Baseline var ama pencere-normalize edilince sıfıra
			// yuvarlanıyor (çok seyrek tarihî hata). Eski SQL bu dalda
			// cur/(base*ratio) ile KALİFİYE eder (payda <1 → oran şişer),
			// raporlanan ratio'yu ise cur olarak verirdi — birebir aynı.
			if float64(r.CurErrs)/(float64(r.BaseErrs)*windowRatio) >= traceOpMinRatio {
				kind, ratio = "error_spike", float64(r.CurErrs)
			}
		case float64(r.CurErrs)/float64(basePerWindow) >= traceOpMinRatio:
			kind, ratio = "error_spike", float64(r.CurErrs)/float64(basePerWindow)
		}
		if kind == "" {
			continue
		}
		out = append(out, TraceOpAnomaly{
			Service:        r.Service,
			Operation:      r.Operation,
			Kind:           kind,
			CurrentErrors:  r.CurErrs,
			CurrentCalls:   r.CurCalls,
			ErrorShare:     float64(r.CurErrs) / float64(r.CurCalls),
			BaselineErrors: basePerWindow,
			Ratio:          ratio,
		})
	}
	// Stable order: new errors first (always more interesting
	// than amplified existing ones), then spikes by ratio desc.
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Kind != out[j].Kind {
			return out[i].Kind == "new_error"
		}
		if out[i].Ratio != out[j].Ratio {
			return out[i].Ratio > out[j].Ratio
		}
		return out[i].CurrentErrors > out[j].CurrentErrors
	})
	if len(out) > 50 {
		out = out[:50]
	}
	return out
}

const traceOpBucketLen = 5 * time.Minute

// DetectTraceOpAnomalies finds per-operation error spikes over
// the last `window` against a longer trailing baseline (1h or
// 12×window, whichever is larger, capped at 24h).
//
// v0.8.504 (perf raporu #1): pre-MV sürüm her koşuda raw spans'i
// İKİ kez tarıyordu (window cur + 1-24h base GROUP BY) — 60s tick'te
// lokalde bile 10-19s/koşu, ~700K satır; 1B span/gün'de dakikada
// milyonlarca satır. Sayımlar artık operation_summary_5m'den okunur
// (MV-first invariant: "raw spans for an aggregate = bug"); raw
// spans'e yalnız KALİFİYE ≤50 çiftin örnek trace'i için dar,
// service_name-prefix'li ikinci sorgu gider. Bedel: pencereler 5m
// bucket'a hizalanır — tespit en fazla ~5dk gecikir (v0.8.315/316'da
// kabul edilmiş desen).
//
// The asymmetric baseline (window vs ≥1h trailing) keeps fresh
// spikes visible for ~1 hour — a window-vs-window comparison would
// have the spike fall into baseline within minutes, flickering the
// anomaly section as windows slide.
func DetectTraceOpAnomalies(ctx context.Context, store *chstore.Store, window time.Duration) ([]TraceOpAnomaly, error) {
	conn := store.TelemetryReadConn()
	now := time.Now()

	// Tam-bucket hizası: MV bucket'ı kapanmadan sayımı eksiktir.
	alignedNow := now.Truncate(traceOpBucketLen)
	curBuckets := int(window / traceOpBucketLen)
	if curBuckets < 1 {
		curBuckets = 1
	}
	curWindow := time.Duration(curBuckets) * traceOpBucketLen
	curStart := alignedNow.Add(-curWindow)

	// v0.9.334 — taban penceresi 1 saat → 24 saat.
	//
	// Operatör (prod, v0.9.327'den SONRA): "Prodta hâlâ çok anomali var."
	//
	// Sertleştirilen eşikler `base_errs > 0` dalını kısıyordu ama "new_error"
	// dalını değil: taban penceresinde hiç hata yoksa oran şartı HİÇ
	// uygulanmıyor. 1 saatlik pencereyle, iki saatte bir tekrarlayan bir hata
	// HER SEFERİNDE "yeni" sayılıyor — kalıcı bir olay üreteci. 24 saatte
	// "yeni", gerçekten "bir gündür görülmedi" demek.
	//
	// Normalizasyon bunu ücretsiz kılıyor: base 24 saatlik toplam olduğu için
	// windowRatio da 24× küçülüyor, yani basePerWindow (dolayısıyla spike
	// eşiği) değişmiyor. Değişen tek şey "taban sıfır" iddiasının ne kadar
	// zor olduğu.
	//
	// Maliyet ölçüldü (yerel, operation_summary_5m): 1h 0.050s / 24h 0.058s.
	// MV önceden toplandığı için genişleme çıktı kardinalitesini değil yalnız
	// birleştirilen bucket sayısını artırıyor.
	baseLookback := 24 * time.Hour
	if 12*curWindow > baseLookback {
		baseLookback = 12 * curWindow
	}
	baseStart := curStart.Add(-baseLookback)
	windowRatio := float64(curWindow) / float64(baseLookback)

	// Tek MV geçişi: iç seviye (pair, is_cur) bazında state merge, dış
	// seviye cur/base'i yan yana koyar. Kaba eleme SQL'de kalır ki
	// LIMIT anlamlı olsun (eşiğin gevşek hâli); kesin eşik/kind
	// sınıflaması Go'da (classifyTraceOps, tablo-testli).
	rows, err := conn.Query(ctx, `
		SELECT service_name, name,
		       sumIf(errs,  is_cur = 1) AS cur_errs,
		       sumIf(errs,  is_cur = 0) AS base_errs,
		       sumIf(calls, is_cur = 1) AS cur_calls
		FROM (
		  SELECT service_name, name,
		         time_bucket >= ? AS is_cur,
		         countIfMerge(error_count_state) AS errs,
		         countMerge(span_count_state)    AS calls
		  FROM operation_summary_5m
		  WHERE time_bucket >= ? AND time_bucket < ?
		  GROUP BY service_name, name, is_cur
		)
		GROUP BY service_name, name
		-- v0.9.327 — the coarse filter now carries the SAME floors the Go
		-- classifier applies. It used to be deliberately looser, which meant
		-- the LIMIT 200 filled with pairs Go would then reject: the ranking
		-- was spent on rows that could never qualify. Same lesson as the
		-- inbox status narrow (v0.9.322) — the LIMIT has to bite on rows
		-- that can actually survive.
		HAVING cur_errs >= ? AND cur_calls > 0
		   AND cur_errs >= ? * cur_calls
		   AND ((base_errs = 0) OR (cur_errs >= ? * base_errs * ?))
		ORDER BY cur_errs DESC
		LIMIT 200
		SETTINGS max_execution_time = 25`,
		curStart, baseStart, alignedNow,
		traceOpMinErrs, traceOpMinErrShare, traceOpMinRatio, windowRatio,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	buckets := []traceOpBucket{}
	for rows.Next() {
		var b traceOpBucket
		if err := rows.Scan(&b.Service, &b.Operation, &b.CurErrs, &b.BaseErrs, &b.CurCalls); err != nil {
			return nil, err
		}
		buckets = append(buckets, b)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	out := classifyTraceOps(buckets, windowRatio)
	if len(out) == 0 {
		return out, nil
	}

	// İkinci, DAR raw-spans sorgusu: yalnız kalifiye çiftlerin örnek
	// trace'i + son görülme anı. service_name PK-prefix + zaman
	// sınırı + LIMIT — 1B span/gün'de bile küçük bir dilim. İki ayrı
	// IN listesi kesişimin ÜST-kümesini tarar (sürücüde tuple-IN bind
	// emsali yok); kesin çift eşlemesi aşağıdaki map'te — fazla
	// gruplar sadece atlanır. LIMIT = 50×50 kartezyen tavanı.
	svcs := make([]string, 0, len(out))
	ops := make([]string, 0, len(out))
	for _, a := range out {
		svcs = append(svcs, a.Service)
		ops = append(ops, a.Operation)
	}
	srows, err := conn.Query(ctx, `
		SELECT service_name, name,
		       argMax(trace_id, time)           AS sample,
		       toUnixTimestamp64Nano(max(time)) AS last_ns
		FROM spans
		WHERE time >= ? AND time < ?
		  AND status_code = 'error'
		  AND service_name IN ?
		  AND name IN ?
		GROUP BY service_name, name
		LIMIT 2500
		SETTINGS max_execution_time = 10`,
		curStart, alignedNow, svcs, ops,
	)
	if err != nil {
		return nil, err
	}
	defer srows.Close()
	type sampleKey struct{ svc, op string }
	type sampleVal struct {
		trace  string
		lastNs int64
	}
	samples := map[sampleKey]sampleVal{}
	for srows.Next() {
		var svc, op, trace string
		var lastNs int64
		if err := srows.Scan(&svc, &op, &trace, &lastNs); err != nil {
			return nil, err
		}
		samples[sampleKey{svc, op}] = sampleVal{trace, lastNs}
	}
	if err := srows.Err(); err != nil {
		return nil, err
	}
	for i := range out {
		if s, ok := samples[sampleKey{out[i].Service, out[i].Operation}]; ok {
			out[i].SampleTraceID = s.trace
			out[i].LastSeenNs = s.lastNs
		}
	}
	return out, nil
}
