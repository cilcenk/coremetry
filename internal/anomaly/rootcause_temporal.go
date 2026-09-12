package anomaly

// rootcause_temporal.go — v0.10.700 (Dynatrace paritesi #2, dilim 1 / GÖLGE).
//
// Hipotez sentezinden hemen önce: anchor + yapısal komşular (Kind == "",
// en çok temporalMaxCandidates) için tek seri okuması, her komşuya
// correlator.ComputeTemporalFactor, sonuç SynthesisInput.TemporalFactors.
// TemporalApply operatör anahtarından (AnomalySensitivity().TemporalRankingOn);
// varsayılan gölge: faktör yazılır, sıra değişmez. Okuma düşerse faktör
// hiç yazılmaz (soft-fail, dürüst boşluk) — hipotez yine üretilir.

import (
	"context"
	"log"
	"time"

	"github.com/cilcenk/coremetry/internal/correlator"
)

const (
	// temporalMaxCandidates — seri okunan komşu sayısı tavanı (anchor hariç).
	temporalMaxCandidates = 10
	// temporalMaxWindow — pencere tavanı: uzun süredir açık bir problemde
	// [onset−60m, now] saatler olur ve korelasyon sulanır; son 3 saat yeter.
	temporalMaxWindow = 3 * time.Hour
)

// temporalServices — SAF: anchor ilk sırada, ardından yapısal komşular
// (node/rollout adayının serisi yok), tekrarsız, tavanlı.
func temporalServices(anchor string, nbs []correlator.ScoredCause, max int) []string {
	out := []string{anchor}
	seen := map[string]bool{anchor: true}
	for _, nb := range nbs {
		if nb.Kind != "" || nb.Service == "" || seen[nb.Service] {
			continue
		}
		if len(out)-1 >= max {
			break
		}
		seen[nb.Service] = true
		out = append(out, nb.Service)
	}
	return out
}

// temporalWindow — SAF: [onset−evidenceWindow, son tam kova), 5 dk grid,
// temporalMaxWindow tavanı; onset bilinmiyorsa son evidenceWindow.
func temporalWindow(onsetNs int64, now time.Time) (from, to time.Time) {
	to = lastCompleteBucketStart(now.UTC())
	if onsetNs <= 0 {
		from = to.Add(-evidenceWindow)
	} else {
		from = time.Unix(0, onsetNs).UTC().Add(-evidenceWindow)
	}
	if lo := to.Add(-temporalMaxWindow); from.Before(lo) {
		from = lo
	}
	if from.After(to) {
		from = to.Add(-evidenceWindow)
	}
	return from.Truncate(5 * time.Minute), to
}

// attachTemporal — seri okuması + faktörler; in yerinde güncellenir.
func (s *RootCauseSynthesizer) attachTemporal(ctx context.Context, anchor string, onsetNs int64, now time.Time, in *correlator.SynthesisInput) {
	if s == nil || s.store == nil || in == nil {
		return
	}
	svcs := temporalServices(anchor, in.Neighbours, temporalMaxCandidates)
	if len(svcs) < 2 {
		return // ölçülecek yapısal aday yok
	}
	from, to := temporalWindow(onsetNs, now)
	series, err := s.store.ServiceErrorRateSeries5m(ctx, svcs, from, to)
	if err != nil {
		log.Printf("[rootcause-synth] temporal series %s: %v — bu anchor için zamansal çarpan yok", anchor, err)
		return
	}
	trig, ok := series[anchor]
	if !ok {
		return
	}
	in.TemporalFactors = make(map[string]correlator.TemporalFactor, len(svcs)-1)
	for _, svc := range svcs[1:] {
		in.TemporalFactors[svc] = correlator.ComputeTemporalFactor(trig, series[svc])
	}
	in.TemporalApply = s.store.AnomalySensitivity().TemporalRankingOn()
}
