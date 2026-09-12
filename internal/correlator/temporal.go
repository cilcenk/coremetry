package correlator

// temporal.go — v0.10.700 (Dynatrace paritesi #2, dilim 1 / GÖLGE).
//
// propagation.go'nun skoru tamamen YAPISAL: adayın tetikleyicinin downstream
// hata hacmindeki payı. Zaman hiç girmiyor — bir saattir düz hata üreten
// aday ile tetikleyiciden 10 dk önce bozulan aday aynı payı alır. Bu dosya
// o boşluğu kapatan SAF ölçü: tetikleyici ile adayın 5 dk hata-oranı
// serileri birlikte hareket ediyor mu, aday önde mi.
//
// Ölçü: serilerin BİRİNCİ FARKLARI üzerinde Spearman sıra korelasyonu,
// 0..TemporalMaxLag kova gecikmeyle (aday önde). Fark almak şart: iki
// servis de gece birlikte düşerse ham seri sahte yüksek korelasyon verir;
// aynı gerekçeyle hata SAYISI değil hata ORANI. Onset sırası: adayın en
// büyük sıçraması tetikleyicininkinden SONRAYSA ceza — sonuç nedenden
// önce gelemez. Veri yetersizse (TemporalMinFilled altı dolu kova) faktör
// NÖTR 0.5, asla 0: "boş küme kaybolur" tuzağı — yapı veri zayıfken
// atılmaz. Çarpanın skora nasıl bindiği hypothesis.go'da
// (Score = Structural × (0.5 + 0.5·t), yalnız TemporalApply ile).

import (
	"fmt"
	"math"
	"sort"
)

const (
	// TemporalMaxLag — adayın en çok kaç kova (×5 dk) önde olabileceği.
	TemporalMaxLag = 2
	// TemporalMinFilled — iki seride de dolu olması gereken en az kova;
	// altında ölçü yapılmaz, faktör nötr.
	TemporalMinFilled = 6
	// TemporalNeutral — veri yetersizken faktör.
	TemporalNeutral = 0.5
	// temporalOnsetPenalty — aday tetikleyiciden SONRA sıçradıysa çarpan.
	temporalOnsetPenalty = 0.5
	// temporalMinPairs — Spearman için en az çift.
	temporalMinPairs = 3
)

// TemporalFactor — bir aday için zamansal ölçü. Factor ∈ [0,1]; Lag aday
// kaç kova önde (−1 = ölçülemedi); Rho seçilen gecikmedeki Spearman;
// Reason operatör-okur gerekçe (aday satırına iner).
type TemporalFactor struct {
	Factor float64
	Lag    int
	Rho    float64
	Filled int
	Reason string
}

// ComputeTemporalFactor — SAF, deterministik. trigger ve cand aynı 5 dk
// grid'inde, eksik kova NaN. Kuyruk-hizalı (kısa olanın boyuna).
func ComputeTemporalFactor(trigger, cand []float64) TemporalFactor {
	n := len(trigger)
	if len(cand) < n {
		n = len(cand)
	}
	tr := trigger[len(trigger)-n:]
	cd := cand[len(cand)-n:]
	filled := 0
	for i := 0; i < n; i++ {
		if !math.IsNaN(tr[i]) && !math.IsNaN(cd[i]) {
			filled++
		}
	}
	if filled < TemporalMinFilled {
		return TemporalFactor{Factor: TemporalNeutral, Lag: -1, Filled: filled,
			Reason: fmt.Sprintf("temporal: insufficient series (%d/%d buckets)", filled, n)}
	}
	dT, dC := seriesDiffs(tr), seriesDiffs(cd)
	bestRho, bestLag, measured := 0.0, 0, false
	for lag := 0; lag <= TemporalMaxLag; lag++ {
		var xs, ys []float64
		for i := lag; i < len(dT); i++ {
			x, y := dT[i], dC[i-lag]
			if math.IsNaN(x) || math.IsNaN(y) {
				continue
			}
			xs = append(xs, x)
			ys = append(ys, y)
		}
		rho, ok := spearman(xs, ys)
		if !ok {
			continue
		}
		if !measured || rho > bestRho {
			bestRho, bestLag, measured = rho, lag, true
		}
	}
	out := TemporalFactor{Lag: bestLag, Rho: bestRho, Filled: filled}
	if !measured || bestRho <= 0 {
		out.Factor = 0
		out.Reason = fmt.Sprintf("no co-movement with trigger (ρ=%.2f)", bestRho)
		return out
	}
	out.Factor = bestRho
	oT, oC := onsetIndex(dT), onsetIndex(dC)
	if oT >= 0 && oC >= 0 && oC > oT {
		out.Factor = bestRho * temporalOnsetPenalty
		out.Reason = fmt.Sprintf("co-moves with trigger (ρ=%.2f) but rose after it — penalised", bestRho)
		return out
	}
	if bestLag > 0 {
		out.Reason = fmt.Sprintf("co-moves with trigger (ρ=%.2f, leads by %d×5m)", bestRho, bestLag)
	} else {
		out.Reason = fmt.Sprintf("co-moves with trigger (ρ=%.2f, in step)", bestRho)
	}
	return out
}

// seriesDiffs — birinci farklar; komşulardan biri NaN ise NaN.
func seriesDiffs(s []float64) []float64 {
	if len(s) < 2 {
		return nil
	}
	out := make([]float64, len(s)-1)
	for i := 1; i < len(s); i++ {
		if math.IsNaN(s[i]) || math.IsNaN(s[i-1]) {
			out[i-1] = math.NaN()
			continue
		}
		out[i-1] = s[i] - s[i-1]
	}
	return out
}

// onsetIndex — en büyük POZİTİF tek-kova artışın indeksi (ilk görülen);
// artış yoksa −1.
func onsetIndex(d []float64) int {
	best, idx := 0.0, -1
	for i, v := range d {
		if math.IsNaN(v) {
			continue
		}
		if v > best {
			best, idx = v, i
		}
	}
	return idx
}

// spearman — sıra korelasyonu (eşit değerler ortalama sıra). Çift sayısı
// temporalMinPairs altındaysa ya da bir tarafın varyansı sıfırsa ok=false.
func spearman(xs, ys []float64) (float64, bool) {
	if len(xs) != len(ys) || len(xs) < temporalMinPairs {
		return 0, false
	}
	rx, ry := ranks(xs), ranks(ys)
	n := float64(len(rx))
	mx, my := 0.0, 0.0
	for i := range rx {
		mx += rx[i]
		my += ry[i]
	}
	mx /= n
	my /= n
	sxy, sxx, syy := 0.0, 0.0, 0.0
	for i := range rx {
		dx, dy := rx[i]-mx, ry[i]-my
		sxy += dx * dy
		sxx += dx * dx
		syy += dy * dy
	}
	if sxx == 0 || syy == 0 {
		return 0, false
	}
	return sxy / math.Sqrt(sxx*syy), true
}

// ranks — 1-tabanlı sıralar, eşitlerde ortalama sıra.
func ranks(v []float64) []float64 {
	idx := make([]int, len(v))
	for i := range idx {
		idx[i] = i
	}
	sort.SliceStable(idx, func(a, b int) bool { return v[idx[a]] < v[idx[b]] })
	out := make([]float64, len(v))
	for i := 0; i < len(idx); {
		j := i
		for j+1 < len(idx) && v[idx[j+1]] == v[idx[i]] {
			j++
		}
		avg := float64(i+j+2) / 2 // (i+1 + j+1)/2
		for k := i; k <= j; k++ {
			out[idx[k]] = avg
		}
		i = j + 1
	}
	return out
}
