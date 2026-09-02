package anomaly

// rollout_causes.go — v0.10.242 Problem↔Rollout korelasyonu (D2, worker
// yarısı). Audit: docs/audit/deployment-problem-correlation.md §7.
//
// Sentezleyici her kritik açık Problem için (RecentDeploy'a DOKUNMADAN):
//   1. servisin [onset−120dk, onset+5dk] penceresinde span ürettiği
//      (cluster, ns, workload, revision) kümesi  → MV (RolloutRefsForService)
//   2. problemin pod'u varsa pod → revizyon         → spans (RolloutRefForPod)
//   3. span cluster değeri → EffectiveID            → thanos küme ayarı
//   4. o iş yüklerinin pencerede BAŞLAYAN rollout'ları → workload_rollouts
//   5. rollout.Rank → ≤3 puanlı aday → SynthesisInput.Rollouts (deploy
//      tier'ıyla birleşir) + DeepEvidence.Rollouts (UI kanıtı)
//
// Kapı: rollouts ayarı kapalıysa (Resolved().Enabled=false) hiçbir sorgu
// koşmaz — /rollouts sayfası da aynı bayrakla kapalı.
//
// Saf çekirdek (rolloutWindow / clusterIDMap / workloadKeys /
// buildRolloutCandidates / rolloutEvidenceFrom) tablo-testli; CH'ye
// dokunan tek yer rolloutCauses.

import (
	"context"
	"log"
	"time"

	"github.com/cilcenk/coremetry/internal/chstore"
	"github.com/cilcenk/coremetry/internal/correlator"
	"github.com/cilcenk/coremetry/internal/rollout"
)

// rolloutWindow — Problem.StartedAt (unix ns) → [onset−LowBandMaxMin,
// onset+AfterOnsetToleranceMin]. ns/ms sınırı BURADA: StartedAt ns'dir,
// rollout.started_at DateTime64(3); dönüşüm time.Time üzerinden, birim
// karışımı yok (test: 10 dk önceki rollout AgeMin=10).
func rolloutWindow(startedAtNs int64) (from, to time.Time) {
	onset := time.Unix(0, startedAtNs).UTC()
	return onset.Add(-time.Duration(rollout.LowBandMaxMin) * time.Minute),
		onset.Add(time.Duration(rollout.AfterOnsetToleranceMin) * time.Minute)
}

// clusterIDMap — span cluster değeri (k8s.cluster.name…) → EffectiveID.
// Bir küme birden çok span değeri taşıyabilir (SpanClusterValues).
func clusterIDMap(refs []rollout.ClusterRef) map[string]string {
	m := map[string]string{}
	for _, c := range refs {
		if c.SpanClusterValue != "" {
			m[c.SpanClusterValue] = c.ID
		}
		for _, v := range c.SpanClusterValues {
			if v != "" {
				m[v] = c.ID
			}
		}
	}
	return m
}

// resolveClusterID — span değeri → EffectiveID; harita boşsa (küme
// tanımsız / tek-küme kurulum) değer olduğu gibi geçer, harita doluyken
// bilinmeyen değer eşleşmez (ok=false).
func resolveClusterID(m map[string]string, spanValue string) (string, bool) {
	if len(m) == 0 {
		return spanValue, true
	}
	id, ok := m[spanValue]
	return id, ok
}

// workloadKeys — referanslardan tekil (EffectiveID, ns, workload) anahtarları.
func workloadKeys(refs []chstore.WorkloadRevisionRef, podRef *chstore.WorkloadRevisionRef, m map[string]string) []rollout.Key {
	seen := map[rollout.Key]bool{}
	var out []rollout.Key
	add := func(r chstore.WorkloadRevisionRef) {
		id, ok := resolveClusterID(m, r.Cluster)
		if !ok || r.Workload == "" {
			return
		}
		k := rollout.Key{ClusterID: id, Namespace: r.Namespace, Workload: r.Workload}
		if !seen[k] {
			seen[k] = true
			out = append(out, k)
		}
	}
	for _, r := range refs {
		add(r)
	}
	if podRef != nil {
		add(*podRef)
	}
	return out
}

// buildRolloutCandidates — rollout satırlarını referanslarla eşler.
// Servis eşlemesi: aynı (cluster, ns, workload) — revizyondan bağımsız
// (rollout sürerken servis hâlâ eski revizyondan span üretebilir; şüpheli
// yine o rollout). Pod eşlemesi: aynı iş yükü VE aynı revizyon.
func buildRolloutCandidates(refs []chstore.WorkloadRevisionRef, podRef *chstore.WorkloadRevisionRef, rows []chstore.RolloutRow, m map[string]string) []rollout.Candidate {
	svcKeys := map[rollout.Key]bool{}
	for _, r := range refs {
		if id, ok := resolveClusterID(m, r.Cluster); ok && r.Workload != "" {
			svcKeys[rollout.Key{ClusterID: id, Namespace: r.Namespace, Workload: r.Workload}] = true
		}
	}
	var podKey rollout.Key
	podRev := ""
	if podRef != nil {
		if id, ok := resolveClusterID(m, podRef.Cluster); ok && podRef.Workload != "" {
			podKey = rollout.Key{ClusterID: id, Namespace: podRef.Namespace, Workload: podRef.Workload}
			podRev = podRef.Revision
		}
	}
	var out []rollout.Candidate
	for _, row := range rows {
		k := rollout.Key{ClusterID: row.ClusterID, Namespace: row.Namespace, Workload: row.Workload}
		if podRev != "" && k == podKey && row.Revision == podRev {
			out = append(out, rollout.Candidate{Rollout: row.Rollout, MatchedBy: rollout.MatchPod})
			continue
		}
		if svcKeys[k] || (podRev != "" && k == podKey) {
			out = append(out, rollout.Candidate{Rollout: row.Rollout, MatchedBy: rollout.MatchService})
		}
	}
	return out
}

// rolloutEvidenceFrom — puanlı aday → DeepEvidence satırı.
func rolloutEvidenceFrom(s rollout.Scored) chstore.RolloutEvidence {
	r := s.Rollout
	return chstore.RolloutEvidence{
		ClusterID: r.ClusterID, Namespace: r.Namespace, Workload: r.Workload, Kind: r.Kind,
		Revision: r.Revision, StartedAtNs: r.StartedAt.UnixNano(), Status: r.Status,
		ImageTag: r.ImageTag, PrevImageTag: r.PrevImageTag, DetectedBy: r.DetectedBy,
		MatchedBy: s.MatchedBy, AgeMin: s.AgeMin, Band: s.Band, Score: s.Score, Reason: s.Reason,
	}
}

// SetRollouts — v0.10.242: küme kaynağı (span değeri → EffectiveID) ve
// bayrak. Start() öncesi kurulur; tick'ler tek goroutine'de.
func (s *RootCauseSynthesizer) SetRollouts(clusters rollout.ClusterSource, enabled func() bool) {
	if s == nil {
		return
	}
	s.rolloutClusters = clusters
	s.rolloutEnabled = enabled
}

// rolloutCauses — CH'ye dokunan tek fonksiyon; hata = kanıt yok (log),
// sentez devam eder. in.Rollouts'u doldurur, DeepEvidence satırlarını
// döndürür.
func (s *RootCauseSynthesizer) rolloutCauses(ctx context.Context, p chstore.Problem, in *correlator.SynthesisInput) []chstore.RolloutEvidence {
	if s.rolloutEnabled == nil || !s.rolloutEnabled() || s.rolloutClusters == nil {
		return nil
	}
	if p.Service == "" || p.StartedAt <= 0 {
		return nil
	}
	from, to := rolloutWindow(p.StartedAt)
	m := clusterIDMap(s.rolloutClusters.Clusters())
	refs, err := s.store.RolloutRefsForService(ctx, p.Service, p.Clusters, from, to)
	if err != nil {
		log.Printf("[rootcause-synth] rollout refs %s: %v", p.Service, err)
		return nil
	}
	var podRef *chstore.WorkloadRevisionRef
	if p.Pod != "" {
		r, ok, err := s.store.RolloutRefForPod(ctx, p.Service, p.Pod, from, to)
		if err != nil {
			log.Printf("[rootcause-synth] rollout pod ref %s/%s: %v", p.Service, p.Pod, err)
		} else if ok {
			podRef = &r
		}
	}
	keys := workloadKeys(refs, podRef, m)
	if len(keys) == 0 {
		return nil
	}
	rows, err := s.store.RolloutsForWorkloads(ctx, keys, from, to)
	if err != nil {
		log.Printf("[rootcause-synth] rollouts for %s: %v", p.Service, err)
		return nil
	}
	scored := rollout.Rank(time.Unix(0, p.StartedAt).UTC(), buildRolloutCandidates(refs, podRef, rows, m))
	var ev []chstore.RolloutEvidence
	for _, sc := range scored {
		in.Rollouts = append(in.Rollouts, correlator.RolloutCandidate{
			Subject: rollout.SubjectID(sc.Rollout), ImageTag: sc.Rollout.ImageTag,
			Score: sc.Score, Reason: sc.Reason,
		})
		ev = append(ev, rolloutEvidenceFrom(sc))
	}
	return ev
}
