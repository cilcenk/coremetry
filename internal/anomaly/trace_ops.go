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
}

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
		basePerWindow := uint64(float64(r.BaseErrs) * windowRatio)
		var kind string
		var ratio float64
		switch {
		case r.BaseErrs == 0 && r.CurErrs >= 3:
			kind, ratio = "new_error", float64(r.CurErrs)
		case r.BaseErrs > 0 && basePerWindow == 0:
			// Baseline var ama pencere-normalize edilince sıfıra
			// yuvarlanıyor (çok seyrek tarihî hata). Eski SQL bu dalda
			// cur/(base*ratio) ile KALİFİYE eder (payda <1 → oran şişer),
			// raporlanan ratio'yu ise cur olarak verirdi — birebir aynı.
			// v0.9.47 — CurErrs >= 3 tabanı (operatör: 1-2 occurrence
			// anlık blip'tir, event olmasın; new_error'ın 3 tabanıyla
			// simetrik).
			if r.CurErrs >= 3 && float64(r.CurErrs)/(float64(r.BaseErrs)*windowRatio) >= 2 {
				kind, ratio = "error_spike", float64(r.CurErrs)
			}
		case basePerWindow > 0 && r.CurErrs >= 3 && float64(r.CurErrs)/float64(basePerWindow) >= 2:
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
	conn := store.Conn()
	now := time.Now()

	// Tam-bucket hizası: MV bucket'ı kapanmadan sayımı eksiktir.
	alignedNow := now.Truncate(traceOpBucketLen)
	curBuckets := int(window / traceOpBucketLen)
	if curBuckets < 1 {
		curBuckets = 1
	}
	curWindow := time.Duration(curBuckets) * traceOpBucketLen
	curStart := alignedNow.Add(-curWindow)

	baseLookback := time.Hour
	if 12*curWindow > baseLookback {
		baseLookback = 12 * curWindow
	}
	if baseLookback > 24*time.Hour {
		baseLookback = 24 * time.Hour
	}
	baseStart := curStart.Add(-baseLookback)
	windowRatio := float64(curWindow) / float64(baseLookback)

	// Tek MV geçişi: iç seviye (pair, is_cur) bazında state merge, dış
	// seviye cur/base'i yan yana koyar. Kaba eleme SQL'de kalır ki
	// LIMIT anlamlı olsun (eşiğin gevşek hâli); kesin eşik/kind
	// sınıflaması Go'da (classifyTraceOps, tablo-testli).
	rows, err := conn.Query(ctx, `
		SELECT service_name, name,
		       sumIf(errs, is_cur = 1)  AS cur_errs,
		       sumIf(errs, is_cur = 0)  AS base_errs
		FROM (
		  SELECT service_name, name,
		         time_bucket >= ? AS is_cur,
		         countIfMerge(error_count_state) AS errs
		  FROM operation_summary_5m
		  WHERE time_bucket >= ? AND time_bucket < ?
		  GROUP BY service_name, name, is_cur
		)
		GROUP BY service_name, name
		HAVING cur_errs > 0 AND (
		  (base_errs = 0 AND cur_errs >= 3) OR
		  (base_errs > 0 AND cur_errs >= 2 * base_errs * ?)
		)
		ORDER BY cur_errs DESC
		LIMIT 200
		SETTINGS max_execution_time = 30`,
		curStart, baseStart, alignedNow, windowRatio,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	buckets := []traceOpBucket{}
	for rows.Next() {
		var b traceOpBucket
		if err := rows.Scan(&b.Service, &b.Operation, &b.CurErrs, &b.BaseErrs); err != nil {
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
