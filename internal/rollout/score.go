package rollout

// score.go — v0.10.241 (Problem↔Rollout korelasyonu, D1 saf çekirdek).
//
// Bir Problem'in başlangıcına göre aday rollout'ları puanlar. Audit
// (docs/audit/deployment-problem-correlation.md §7) varsayılanları:
//
//   • rollout başlangıcı → problem başlangıcı  0–30 dk   → 0.90 (yüksek bant)
//   •                                           30–120 dk → 0.50 (düşük bant)
//   •                                           >120 dk   → aday değil
//   • problemden SONRA başlayan rollout aday değil (5 dk saat toleransı:
//     Problem.StartedAt kova başı, rollout started_at ilk span — aynı
//     dakikada başlayan ikisi "sonra" görünmesin)
//   • durum çarpanı: stalled / rolled_back ×1.10 (şüphe artar),
//     superseded ×0.70 (çekilmiş revizyon; yine de pencerede yaşadı)
//   • +0.05 KSM ikinci kanıt (detected_by "spans+ksm"), +0.05 pod eşlemesi
//     (problemin pod'u bu revizyonun ReplicaSet'inde)
//   • tavan 0.98 — deploy tier'ının 0.75+0.20 ile aynı üst sınır;
//     "kesin" (1.0) hiçbir korelasyon iddia etmez.
//
// Neden time.Time: Problem.StartedAt unix-ns, workload_rollouts.started_at
// DateTime64(3) — iki birimin toplandığı sınır burası DEĞİL, çağıran
// (anomaly paketi) dönüştürür; test ns/ms karışımını orada çiviler.
// Burada sadece iki time.Time'ın farkı var, birim yok.

import (
	"fmt"
	"sort"
	"strings"
	"time"
)

// MatchedBy — adayın Problem'e nasıl bağlandığı.
const (
	MatchService = "service" // workload_revision_activity_1m: servis bu revizyonda span üretti
	MatchPod     = "pod"     // problemin pod'u revizyonun ReplicaSet'ine ait
)

// Candidate — puanlanacak ham aday.
type Candidate struct {
	Rollout   Rollout
	MatchedBy string
}

// Scored — puanlanmış aday; Reason operatörün okuyacağı tek cümle.
type Scored struct {
	Rollout   Rollout
	MatchedBy string
	AgeMin    int // rollout başlangıcı → problem başlangıcı, dakika (negatif = problemden sonra)
	Band      string
	Score     float64
	Reason    string
}

const (
	BandHigh = "high"
	BandLow  = "low"

	HighBandMaxMin         = 30
	LowBandMaxMin          = 120
	AfterOnsetToleranceMin = 5

	highBandScore = 0.90
	lowBandScore  = 0.50
	ksmBonus      = 0.05
	podBonus      = 0.05
	maxScore      = 0.98

	// MaxScored — Rank'ın döndürdüğü en fazla aday (RootCausePanel 3 satır).
	MaxScored = 3
)

// RevisionKey — (cluster, ns, workload, revision) kimliği; tekilleştirme
// anahtarı. (reconcile.go'daki Key tipi kova-düzeyi anahtar; bu string.)
func RevisionKey(r Rollout) string {
	return r.ClusterID + "/" + r.Namespace + "/" + r.Workload + "@" + r.Revision
}

// SubjectID — ScoredCause.Service alanına yazılan özne ("rollout:" öneki
// ile FE ayırt eder; problemSubject.ts ext: emsali).
func SubjectID(r Rollout) string { return "rollout:" + RevisionKey(r) }

// Score — tek adayı puanlar; ok=false → pencere dışı, aday değil.
func Score(onset time.Time, c Candidate) (Scored, bool) {
	r := c.Rollout
	if onset.IsZero() || r.StartedAt.IsZero() {
		return Scored{}, false
	}
	ageMin := onset.Sub(r.StartedAt).Minutes()
	if ageMin < -AfterOnsetToleranceMin || ageMin > LowBandMaxMin {
		return Scored{}, false
	}
	s := Scored{Rollout: r, MatchedBy: c.MatchedBy, AgeMin: int(ageMin)}
	if ageMin <= HighBandMaxMin {
		s.Band, s.Score = BandHigh, highBandScore
	} else {
		s.Band, s.Score = BandLow, lowBandScore
	}
	switch r.Status {
	case StatusStalled, StatusRolledBack:
		s.Score *= 1.10
	case StatusSuperseded:
		s.Score *= 0.70
	}
	if strings.Contains(r.DetectedBy, "ksm") {
		s.Score += ksmBonus
	}
	if c.MatchedBy == MatchPod {
		s.Score += podBonus
	}
	if s.Score > maxScore {
		s.Score = maxScore
	}
	s.Score = float64(int(s.Score*1000+0.5)) / 1000
	s.Reason = reason(s)
	return s, true
}

// Rank — adayları puanlar, (cluster,ns,workload,revision) ile tekilleştirir
// (pod eşlemesi servis eşlemesini yener: daha yüksek puan kalır), puana
// göre sıralar (eşitlikte problem başlangıcına yakın olan önce) ve
// MaxScored ile keser.
func Rank(onset time.Time, cands []Candidate) []Scored {
	best := map[string]Scored{}
	for _, c := range cands {
		s, ok := Score(onset, c)
		if !ok {
			continue
		}
		k := RevisionKey(c.Rollout)
		if prev, seen := best[k]; !seen || s.Score > prev.Score {
			best[k] = s
		}
	}
	out := make([]Scored, 0, len(best))
	for _, s := range best {
		out = append(out, s)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Score != out[j].Score {
			return out[i].Score > out[j].Score
		}
		if out[i].AgeMin != out[j].AgeMin {
			return out[i].AgeMin < out[j].AgeMin
		}
		return RevisionKey(out[i].Rollout) < RevisionKey(out[j].Rollout)
	})
	if len(out) > MaxScored {
		out = out[:MaxScored]
	}
	return out
}

func reason(s Scored) string {
	r := s.Rollout
	what := "rollout"
	if r.ImageTag != "" && r.PrevImageTag != "" && r.ImageTag != r.PrevImageTag {
		what = fmt.Sprintf("deployment %s → %s", r.PrevImageTag, r.ImageTag)
	} else if r.ImageTag != "" {
		what = "rollout " + r.ImageTag
	}
	when := fmt.Sprintf("%d dk önce", s.AgeMin)
	if s.AgeMin <= 0 {
		when = "problemle aynı dakikada"
	}
	status := ""
	switch r.Status {
	case StatusStalled:
		status = ", rollout takıldı"
	case StatusRolledBack:
		status = ", geri alındı"
	case StatusSuperseded:
		status = ", sonraki revizyon devraldı"
	case StatusInProgress:
		status = ", hâlâ sürüyor"
	}
	via := ""
	if s.MatchedBy == MatchPod {
		via = " — problemin pod'u bu revizyonda"
	}
	return fmt.Sprintf("%s/%s %s problem başlangıcından %s%s%s",
		r.Namespace, r.Workload, what, when, status, via)
}
