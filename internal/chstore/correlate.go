package chstore

import (
	"context"
	"math"
	"sort"
	"time"
)

// ChangedService is one row in the "what changed around this time"
// causal-correlation report. Driven by the operator clicking
// "Why did this fire?" on a Problem — we walk every service that
// emitted spans in the surrounding window and surface the ones
// whose RED metrics swung the most between the baseline window
// (before the problem started) and the current window (since).
//
// Score is a composite z-score-ish magnitude across the three
// signals so a single sort surfaces "anything weird" without the
// operator having to flip between rate/err/latency views. Real
// SREs read the deltas in the columns rather than the score —
// the score's job is just to rank.
type ChangedService struct {
	Service       string  `json:"service"`
	BaselineRate  float64 `json:"baselineRate"` // spans/sec, baseline window
	CurrentRate   float64 `json:"currentRate"`  // spans/sec, current window
	RateDeltaPct  float64 `json:"rateDeltaPct"`
	BaselineErr   float64 `json:"baselineErrorRate"` // 0..1
	CurrentErr    float64 `json:"currentErrorRate"`
	ErrDeltaPct   float64 `json:"errDeltaPct"`
	BaselineP99Ms float64 `json:"baselineP99Ms"`
	CurrentP99Ms  float64 `json:"currentP99Ms"`
	P99DeltaPct   float64 `json:"p99DeltaPct"`
	Score         float64 `json:"score"`
	// Reasons is the human-readable bullet form: each entry is a
	// short sentence the frontend renders verbatim. Saves the UI
	// from re-implementing the formatting logic and keeps the
	// "why did this surface?" answer co-located with the data.
	Reasons []string `json:"reasons"`
}

// GetCorrelatedChanges runs one ClickHouse pass that pivots span
// stats by service across two adjacent time windows: the baseline
// (before `at`) and the current (since `at`). Returns the top 20
// services whose composite anomaly score is highest, plus a
// per-row "reasons" list explaining what triggered the rank.
//
// Caller passes `at` (typically Problem.StartedAt), `windowSec`
// (how long since the problem fired — typically the SLO eval
// window, 5-15 min) and `baselineSec` (how far back to look for
// the comparison — typically 4× windowSec to give the baseline
// statistical weight).
//
// One query, partition-pruned + bounded by HAVING clauses so a
// long-tail service emitting one span every 10 min doesn't pollute
// the rank with a 100% rate change off a baseline of 1.
func (s *Store) GetCorrelatedChanges(
	ctx context.Context, at time.Time, windowSec, baselineSec int,
) ([]ChangedService, error) {
	if windowSec <= 0 {
		windowSec = 600 // 10 min default
	}
	if baselineSec <= 0 {
		baselineSec = windowSec * 4
	}
	winFrom := at
	winTo := at.Add(time.Duration(windowSec) * time.Second)
	if winTo.After(time.Now()) {
		winTo = time.Now()
	}
	baseFrom := at.Add(-time.Duration(baselineSec) * time.Second)
	baseTo := at

	// One row per service across both windows. countIf / quantileIf
	// fan-out across the time predicate so we only scan the union
	// once. The wide HAVING gate prunes services with neither
	// enough baseline traffic NOR enough current traffic — the
	// operator's "what's relevant near my problem" implicitly
	// excludes services that barely emitted anything.
	rows, err := s.conn.Query(ctx, `
		SELECT service_name,
		       countIf(time >= ? AND time < ?)                              AS base_cnt,
		       countIf(time >= ? AND time <= ?)                             AS cur_cnt,
		       countIf(time >= ? AND time < ?  AND status_code = 'error')   AS base_err,
		       countIf(time >= ? AND time <= ? AND status_code = 'error')   AS cur_err,
		       quantileIf(0.99)(duration, time >= ? AND time < ?)  / 1e6    AS base_p99,
		       quantileIf(0.99)(duration, time >= ? AND time <= ?) / 1e6    AS cur_p99
		FROM spans
		WHERE time >= ? AND time <= ?
		GROUP BY service_name
		HAVING base_cnt + cur_cnt >= 30
		LIMIT 500
		SETTINGS max_execution_time = 20`,
		baseFrom, baseTo, // base_cnt
		winFrom, winTo, // cur_cnt
		baseFrom, baseTo, // base_err
		winFrom, winTo, // cur_err
		baseFrom, baseTo, // base_p99
		winFrom, winTo, // cur_p99
		baseFrom, winTo) // outer WHERE
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []ChangedService{}
	baseSeconds := math.Max(1, float64(baselineSec))
	curSeconds := math.Max(1, winTo.Sub(winFrom).Seconds())
	for rows.Next() {
		var svc string
		var baseCnt, curCnt, baseErr, curErr uint64
		var baseP99, curP99 float64
		if err := rows.Scan(&svc, &baseCnt, &curCnt, &baseErr, &curErr, &baseP99, &curP99); err != nil {
			return nil, err
		}
		if c, ok := scoreChangedService(svc, baseCnt, curCnt, baseErr, curErr, baseP99, curP99, baseSeconds, curSeconds); ok {
			out = append(out, c)
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return rankChangedServices(out), nil
}

// scoreChangedService — bir servisin iki-pencere ham sayılarını skorlu
// ChangedService'e çevirir (v0.9.1062'de saf çıkarıldı: ham-spans ve MV
// okuyucuları AYNI skorlama/reasons mantığından geçer — iki kopya
// sürüklenmez; tablo-testli). ok=false: kayda değer değişim yok.
//
// Score: normalise each delta into a "how surprising is this"
// magnitude, sum the components.
//   - Error rate: a 5% absolute jump matters more than a 5% relative
//     jump, so we use the absolute change.
//   - Rate: relative pct change with a soft cap (no service gets
//     rewarded for going from 0.1 → 1 spans/sec).
//   - P99: relative pct change.
func scoreChangedService(
	svc string,
	baseCnt, curCnt, baseErr, curErr uint64,
	baseP99, curP99 float64,
	baseSeconds, curSeconds float64,
) (ChangedService, bool) {
	baseRate := float64(baseCnt) / math.Max(1, baseSeconds)
	curRate := float64(curCnt) / math.Max(1, curSeconds)
	baseErrRate := safeRate(baseErr, baseCnt)
	curErrRate := safeRate(curErr, curCnt)

	c := ChangedService{
		Service:       svc,
		BaselineRate:  baseRate,
		CurrentRate:   curRate,
		BaselineErr:   baseErrRate,
		CurrentErr:    curErrRate,
		BaselineP99Ms: baseP99,
		CurrentP99Ms:  curP99,
		RateDeltaPct:  pctChange(baseRate, curRate),
		ErrDeltaPct:   pctChange(baseErrRate, curErrRate),
		P99DeltaPct:   pctChange(baseP99, curP99),
	}
	errAbs := math.Abs(curErrRate-baseErrRate) * 100 // points
	rateAbs := math.Min(200, math.Abs(c.RateDeltaPct))
	p99Abs := math.Min(200, math.Abs(c.P99DeltaPct))
	c.Score = errAbs*4 + rateAbs*0.5 + p99Abs*0.5

	// Reasons: render human bullets so the UI is dumb.
	if errAbs > 1 { // ≥1 percentage point change in error rate
		c.Reasons = append(c.Reasons,
			fmtReason("error rate", baseErrRate*100, curErrRate*100, "%", true))
	}
	if math.Abs(c.RateDeltaPct) > 25 && (baseCnt+curCnt) > 100 {
		c.Reasons = append(c.Reasons,
			fmtReason("rate", baseRate, curRate, "/s", false))
	}
	if math.Abs(c.P99DeltaPct) > 25 && (baseCnt+curCnt) > 100 {
		c.Reasons = append(c.Reasons,
			fmtReason("P99", baseP99, curP99, "ms", false))
	}
	// Skip services with no notable change — they pad the list.
	if len(c.Reasons) == 0 {
		return ChangedService{}, false
	}
	return c, true
}

// rankChangedServices — skor desc + ilk 20 (saf).
func rankChangedServices(out []ChangedService) []ChangedService {
	sort.Slice(out, func(i, j int) bool { return out[i].Score > out[j].Score })
	if len(out) > 20 {
		out = out[:20]
	}
	return out
}

// GetCorrelatedChangesMV — GetCorrelatedChanges'in service_summary_5m
// sürümü (v0.9.1062, Faz 2.1 / K2). Ham okuyucu spans'i tarıyor (LIMIT
// + 20s cap ile güvenli ama pahalı — tam bu yüzden otomatik sentez
// hattı onu hiç çağıramıyordu, "ne değişti" yalnız on-demand'de
// kalıyordu). Pencereler ≥5dk ve aggregate sabit → invariant #3 gereği
// MV.
//
// Kova hizalaması ZORUNLU (pivot-audit dersi): pencere sınırları 5dk
// grid'e oturtulur, `at`'i içeren kova CARİ tarafa sayılır (kısmi kova
// baseline'ı kirletmesin). Oran paydaları da hizalanmış saniyeler —
// yoksa kenar kovası oranı sistematik saptırır. Skorlama/reasons ham
// okuyucuyla AYNI saf fonksiyondan geçer.
func (s *Store) GetCorrelatedChangesMV(
	ctx context.Context, at time.Time, windowSec, baselineSec int,
) ([]ChangedService, error) {
	if windowSec <= 0 {
		windowSec = 600
	}
	if baselineSec <= 0 {
		baselineSec = windowSec * 4
	}
	// v0.9.1172 — üst sınır DIŞLAYICI. Buradaki `is_cur` bölmesi TEK taramada
	// yapıldığı için pencereler ÖRTÜŞMÜYORDU (dbstmt'in v0.9.1167 hatası
	// burada yok), ama oran normalizasyonu bozuluyordu: aşağıdaki curSeconds
	// (winTo - atB) paydayı pencere genişliğinden alırken `<= winTo` sayacı
	// [winTo, +5dk) kovasından da topluyordu. Cari taraf beş dakikalık yabancı
	// trafik kadar şişip "trafik sıçraması" diye korele değişiklik üretebiliyordu
	// — yani hatanın belirtisi YANLIŞ BİR BULGU, eksik bir sayı değil.
	const grid = 5 * time.Minute
	atB := at.Truncate(grid) // `at` kovası dahil CARİ taraf
	winTo := at.Add(time.Duration(windowSec) * time.Second)
	if now := time.Now(); winTo.After(now) {
		winTo = now
	}
	baseFromB := at.Add(-time.Duration(baselineSec) * time.Second).Truncate(grid)
	if !baseFromB.Before(atB) {
		baseFromB = atB.Add(-grid)
	}

	rows, err := s.conn.Query(ctx, `
		SELECT service_name,
		       sumIf(cnt, NOT is_cur)  AS base_cnt,
		       sumIf(cnt, is_cur)      AS cur_cnt,
		       sumIf(errs, NOT is_cur) AS base_err,
		       sumIf(errs, is_cur)     AS cur_err,
		       maxIf(p99, NOT is_cur)  AS base_p99,
		       maxIf(p99, is_cur)      AS cur_p99
		FROM (
			SELECT service_name,
			       time_bucket >= ? AS is_cur,
			       countMerge(span_count_state)  AS cnt,
			       countMerge(error_count_state) AS errs,
			       arrayElement(quantilesTDigestMerge(0.5, 0.95, 0.99)(duration_q_state), 3) / 1e6 AS p99
			FROM service_summary_5m
			WHERE time_bucket >= ? AND time_bucket < ?
			GROUP BY service_name, is_cur
		)
		GROUP BY service_name
		HAVING base_cnt + cur_cnt >= 30
		LIMIT 500
		SETTINGS max_execution_time = 10`,
		atB, baseFromB, winTo)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []ChangedService{}
	baseSeconds := atB.Sub(baseFromB).Seconds()
	curSeconds := math.Max(grid.Seconds(), winTo.Sub(atB).Seconds())
	for rows.Next() {
		var svc string
		var baseCnt, curCnt, baseErr, curErr uint64
		var baseP99, curP99 float64
		if err := rows.Scan(&svc, &baseCnt, &curCnt, &baseErr, &curErr, &baseP99, &curP99); err != nil {
			return nil, err
		}
		if c, ok := scoreChangedService(svc, baseCnt, curCnt, baseErr, curErr, baseP99, curP99, baseSeconds, curSeconds); ok {
			out = append(out, c)
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return rankChangedServices(out), nil
}

func safeRate(num, denom uint64) float64 {
	if denom == 0 {
		return 0
	}
	return float64(num) / float64(denom)
}

func pctChange(base, cur float64) float64 {
	if base == 0 {
		if cur == 0 {
			return 0
		}
		return 100 // synthesis — a 0→nonzero jump is "100% increase"
	}
	return (cur - base) / base * 100
}

func fmtReason(label string, base, cur float64, unit string, isErrPct bool) string {
	dir := "↑"
	if cur < base {
		dir = "↓"
	}
	if isErrPct {
		return label + " " + fmtFloat(base) + unit + " → " + fmtFloat(cur) + unit + " " + dir
	}
	delta := pctChange(base, cur)
	return label + " " + fmtFloat(base) + unit + " → " + fmtFloat(cur) + unit + " (" + fmtFloat(delta) + "%) " + dir
}

func fmtFloat(v float64) string {
	if math.Abs(v) >= 100 {
		return fmtIntLike(v)
	}
	if math.Abs(v) >= 10 {
		return fmtOneDecimal(v)
	}
	return fmtTwoDecimal(v)
}

func fmtIntLike(v float64) string {
	if v < 0 {
		return "-" + fmtIntLike(-v)
	}
	return itoa(int64(v + 0.5))
}

func fmtOneDecimal(v float64) string {
	w := int64(v * 10)
	if w < 0 {
		return "-" + fmtOneDecimal(-v)
	}
	return itoa(w/10) + "." + itoa(w%10)
}

func fmtTwoDecimal(v float64) string {
	w := int64(v * 100)
	if w < 0 {
		return "-" + fmtTwoDecimal(-v)
	}
	hundredths := w % 100
	tens := hundredths / 10
	ones := hundredths % 10
	return itoa(w/100) + "." + itoa(tens) + itoa(ones)
}

// itoa avoids fmt.Sprintf in the hot reasoning path. The volumes
// are tiny (≤20 services × ≤3 reasons) but the strconv import was
// already in the package and this keeps the dependency footprint
// of correlate.go minimal.
func itoa(v int64) string {
	if v == 0 {
		return "0"
	}
	neg := v < 0
	if neg {
		v = -v
	}
	digits := []byte{}
	for v > 0 {
		digits = append([]byte{byte('0' + v%10)}, digits...)
		v /= 10
	}
	s := string(digits)
	if neg {
		return "-" + s
	}
	return s
}
