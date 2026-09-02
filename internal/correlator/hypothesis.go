package correlator

import (
	"fmt"
	"sort"
	"strings"

	"github.com/cilcenk/coremetry/internal/chstore"
)

// Automatic anomaly → root-cause synthesis (rc #2). Synthesize is the PURE
// fuser: it takes the evidence the worker already gathered (a deploy that lines
// up in time, the propagation-ranked downstream neighbours, and the count of
// co-firing same-service problems) and collapses them into ONE ranked,
// confidence-weighted hypothesis. No I/O, no store, no ctx — the worker does
// the bounded CH reads, this just ranks. Deterministic + total-ordered so the
// same evidence always produces the same hypothesis (table-driven tested).
//
// Why a separate fuser from anomaly.buildEvidenceBundle: the bundle is the raw
// corroboration set ("here is everything that lines up"); Synthesize is the
// JUDGEMENT on top of it ("here is what most likely CAUSED it, ranked"). Keeping
// it in correlator (alongside the propagation scorer it consumes) means the
// ranking logic lives next to the 0.5^hops error-share model it builds on, and
// stays import-cycle-free (anomaly imports correlator, never the reverse — so
// the worker destructures its EvidenceBundle into the primitive inputs below).
//
// ── Ranking weights ─────────────────────────────────────────────────────────
// The priority order is deploy ≫ propagation-ranked neighbour ≫ co-firing,
// reflecting how an SRE actually triages: "what CHANGED?" first (a deploy is
// the single most actionable, most-often-correct suspect), then "what
// downstream is the structural source?" (the propagation share), then "what
// else is unhappy on this same service?" (co-firing — corroboration, rarely the
// root). The blended score keeps these tiers separated so a deploy always
// outranks a propagation suspect, which always outranks a co-firing problem,
// regardless of the raw propagation magnitude:
//
//	deploy candidate  : score = deployBaseScore + freshnessBonus   (∈ [0.80, 0.95])
//	propagation cand. : score = propTierBase * propagation.Score   (∈ (0, 0.70])
//	co-firing cand.   : score = cofiringScore                      (= 0.20, flat)
//
// Confidence (0..1) is a blend of HOW MANY independent evidence types lined up
// (the fusion.go confidence model — more corroboration = more confident) AND
// how strong the top signal is (a fresh deploy or a high propagation share
// lifts it). Empty evidence → 0 confidence + empty candidates + empty suspect
// (an honest "no clear cause"), never a fabricated guess.
const (
	// deployBaseScore — a same-service deploy in the lookback window is the
	// strongest single signal; it floors above any propagation suspect.
	deployBaseScore = 0.80
	// deployFreshnessBonusMax — added on top of the base, scaled by how
	// recently the deploy landed before onset (a deploy 1m before onset is a
	// stronger suspect than one 29m before). Caps the deploy tier at 0.95.
	deployFreshnessBonusMax = 0.15
	// propTierBase — scales propagation scores (∈ [0,1]) into the tier BELOW
	// the deploy floor, so even a share-1.0 downstream suspect (0.70) ranks
	// under a deploy (≥0.80) but above co-firing (0.20).
	propTierBase = 0.70
	// cofiringScore — flat weight for a co-firing same-service problem.
	// Corroborates, rarely causes — lowest tier.
	cofiringScore = 0.20

	// Signal tier (v0.8.571) — active log_pattern / trace_op anomalies on
	// the anchor's OWN service. Stronger than co-firing (a specific error
	// signature, not just "something else is open"), weaker than a deploy
	// (a SYMPTOM, not a change). Band [0.30, 0.60] deliberately OVERLAPS
	// propagation: a same-service signature with a high ratio outranks a
	// weak 3-hop guess, but the 0.60 ceiling keeps every strong 1-hop
	// downstream (and every deploy) above it. NOTE the tiers were never
	// fully disjoint anyway — raw propagation < 0.286 already lands under
	// co-firing; the only hard floor is the deploy base.
	signalTierBase = 0.30
	// signalRatioSpan — how much of the band the over-baseline ratio can
	// climb. ratio 2 (the detector's trigger floor) → +0; ratio ≥ 10 → full
	// span. A "new template" event records ratio 0 and clamps to the base.
	signalRatioSpan     = 0.25
	signalRatioFloor    = 2.0
	signalRatioSaturate = 10.0
	// signalLogPatternBonus — log_pattern over trace_op at equal ratio: the
	// pattern carries the error TEXT (the most diagnostic artefact for the
	// LLM and the operator); a trace_op only says "this operation is off",
	// which the anchor problem usually already implies. A high-ratio
	// trace_op still outranks a low-ratio log_pattern — the bands overlap
	// on purpose.
	signalLogPatternBonus = 0.05
	// signalMaxCandidates — the prompt already cuts at 3
	// (maxHypothesisCandidates, rootcause_prompt.go); emitting more only
	// buries the other tiers' candidates.
	signalMaxCandidates = 3

	// ── Komşu-sinyal tier'ı (v0.9.1056, Faz 1.1) ───────────────────────
	// Topoloji komşusunda AKTİF anomali sinyali (log_pattern/trace_op).
	// Fusion bunları tikin toplu okumasında ZATEN belleğe alıyordu ve
	// aynı-servis filtresiyle çöpe atıyordu (K1) — komşunun imza taşıyan
	// anomalisi, komşunun problem satırından daha erken ve daha teşhis
	// edici bir kök-neden ipucudur (problem henüz terfi etmemiş olabilir).
	// Bant [0.28, 0.52] (+log_pattern bonusu ≤0.57): kendi-servis sinyal
	// bandının hemen altında — kendi semptomuna doğrudan bağlı imza bir
	// tık önde kalır; kenar hata payı taşıyorsa komşu zaten propagation
	// tier'ında (≤0.70) ayrıca yarışır. Upstream (called_by) çarpanı 0.6:
	// yukarı-akış anomalisi genelde KURBANDIR, nadiren neden — ama artık
	// tamamen kör de değiliz (K1'in upstream yarısı).
	neighborSignalTierBase       = 0.28
	neighborSignalRatioSpan      = 0.24
	neighborSignalUpstreamFactor = 0.6
	neighborSignalMaxCandidates  = 3
)

// Confidence model weights — the blend of breadth (distinct evidence types) and
// strength (top candidate score). Sum of the two coefficients is the cap, so a
// hypothesis with maximal breadth AND a maximal-strength top signal approaches
// 1.0; thin evidence stays honestly low.
const (
	// confidenceBreadthWeight — contribution from evidence breadth. The
	// breadth fraction is (distinctTypes / maxEvidenceTypes), matching the
	// fusion.go "distinct corroborating types" confidence intuition.
	confidenceBreadthWeight = 0.5
	// confidenceStrengthWeight — contribution from the top candidate's score
	// (already in [0,1] after the tiering above).
	confidenceStrengthWeight = 0.5
	// maxEvidenceTypes — deploy, propagation neighbours, same-service
	// signals, co-firing. Four independent corroboration channels feed the
	// breadth fraction; distinctTypes still counts tier PRESENCE, never
	// element counts.
	maxEvidenceTypes = 4
)

// SynthesisInput is the evidence the fuser ranks. The worker fills this by
// destructuring its anomaly.EvidenceBundle (deploy + neighbours + co-firing)
// for the anchor's service — Synthesize itself touches no store.
type SynthesisInput struct {
	// Deploy — a same-service deploy that landed in the lookback window before
	// onset, or nil. FreshnessFrac ∈ [0,1] says how close to onset it landed
	// (1 = right at onset, 0 = at the far edge of the lookback window); the
	// worker computes it from (onset - deployTime) / lookback.
	Deploy        *chstore.RecentDeploy
	FreshnessFrac float64
	// DeployImpact (v0.9.1059, Faz 1.4) — deploy'un önce/sonra RED
	// kıyası (ComputeDeployImpact; worker deploy adayı varken tek
	// sınırlı okumayla doldurur, iki pencere de trafik taşıyorsa).
	// nil = ölçüm yok; aday gerekçesi ve RecentDeploy.Impact o zaman
	// bugünküyle bayt-bayt aynı.
	DeployImpact *chstore.DeployImpact
	// Neighbours — the propagation-ranked downstream suspects (best first),
	// straight from RankRootCausesFromEdges. Score ∈ [0,1] is the error-share,
	// hop-decayed. Empty when nothing downstream carries error volume.
	Neighbours []ScoredCause
	// CoFiringServices — distinct OTHER services with an open problem co-firing
	// on the SAME anchor service's incident. In practice these are same-service
	// (so usually the anchor service itself) — the worker passes the service
	// label per co-firing problem so the candidate carries a name. Deduped +
	// sorted by the fuser for determinism.
	CoFiringServices []string
	// Signals — active log_pattern / trace_op anomalies on the anchor's own
	// service (v0.8.571). Collected into EvidenceBundle since v0.8.3xx but
	// never threaded here — the hypothesis was blind to the one evidence
	// type that carries an error SIGNATURE. Minimal shape on purpose: the
	// pure fuser takes only what it scores.
	Signals []SignalEvidence
	// NeighbourSignals (v0.9.1056, Faz 1.1) — TOPOLOJİ KOMŞULARINDAKİ
	// aktif anomali sinyalleri. Fusion bunları tikte zaten belleğe
	// alıyordu ve aynı-servis filtresiyle atıyordu; komşunun imzalı
	// anomalisi, komşu problem satırı doğmadan önce gelen en erken
	// kök-neden ipucu. Breadth sayımında AYRI kanal değildir — komşu
	// kanalına (Neighbours ∪ NeighbourSignals) katlanır ki mevcut
	// hipotezlerin confidence'ı değişmesin (payda 4 kalır).
	NeighbourSignals []NeighbourSignalEvidence

	// Rollouts — v0.10.241 Problem↔Rollout korelasyonu: rollout.Rank'ın
	// puanladığı ≤3 aday. Deploy ile AYNI "ne değişti" kanıt türünü
	// paylaşır: ikisi birden varsa distinctTypes bir kez artar; imaj tag'i
	// deploy sürümüyle eşleşen aday deploy adayına KATLANIR (tek satır,
	// puan yükselir), eşleşmeyen ayrı Kind="rollout" adayı olur.
	Rollouts []RolloutCandidate
}

// RolloutCandidate — anomaly paketinin rollout.Scored'dan indirgediği
// aday; correlator rollout paketini import etmez (katman sırası).
type RolloutCandidate struct {
	Subject  string  // "rollout:<cluster>/<ns>/<workload>@<rev>" (rollout.SubjectID)
	ImageTag string  // deploy sürümüyle eşleştirme anahtarı
	Score    float64 // rollout.Score çıktısı (≤0.98)
	Reason   string
}

// CauseKindRollout — ScoredCause.Kind: özne bir servis değil, bir rollout
// kaydı; FE /rollouts detayına linkler, topolojide aramaz.
const CauseKindRollout = "rollout"

// rolloutCorroborationBonus — deploy adayı bir rollout kaydıyla
// doğrulanınca (aynı imaj tag'i) eklenen puan; tavan deployMaxScore.
const rolloutCorroborationBonus = 0.05

// NeighbourSignalEvidence is one active anomaly signal on a topology
// neighbour of the anchor service.
type NeighbourSignalEvidence struct {
	Service    string
	Kind       string // "log_pattern" | "trace_op"
	Pattern    string
	Ratio      float64 // max(current, peak) over baseline; 0 for new-template
	Downstream bool    // anchor → neighbour (calls); false = upstream (called_by)
	Hops       int     // topology distance from the anchor (≥1)
}

// SignalEvidence is one active anomaly signal on the anchor service.
type SignalEvidence struct {
	Kind    string  // "log_pattern" | "trace_op"
	Pattern string  // pattern name (logs) or operation name (trace ops)
	Ratio   float64 // max(current, peak) over baseline; 0 for new-template events
}

// deployImpactSummary — önce/sonra RED kıyasının tek cümlelik, kararlı
// özeti (v0.9.1059). Saf + tablo-testli: aynı impact aynı baytları
// üretir. p99 her zaman yazılır; hata oranı yalnız kıpırdadıysa
// (|Δ| ≥ 0.1 puan) eklenir — "err %0.0→%0.0" gürültüsü basılmaz.
func deployImpactSummary(imp *chstore.DeployImpact) string {
	s := fmt.Sprintf("after it p99 %.0f→%.0fms (%+.0f%%)",
		imp.Before.P99Ms, imp.After.P99Ms, imp.P99DeltaPct)
	if imp.ErrorRateDeltaPct >= 0.1 || imp.ErrorRateDeltaPct <= -0.1 {
		s += fmt.Sprintf(", err %.1f%%→%.1f%%",
			imp.Before.ErrorRate*100, imp.After.ErrorRate*100)
	}
	return s
}

// neighbourSignalScore maps one neighbour signal into the [0.28, 0.52]
// band (+log_pattern bonus), then applies the upstream factor. Pure.
func neighbourSignalScore(sig NeighbourSignalEvidence) float64 {
	norm := (sig.Ratio - signalRatioFloor) / (signalRatioSaturate - signalRatioFloor)
	if norm < 0 {
		norm = 0
	}
	if norm > 1 {
		norm = 1
	}
	score := neighborSignalTierBase + neighborSignalRatioSpan*norm
	if sig.Kind == "log_pattern" {
		score += signalLogPatternBonus
	}
	if !sig.Downstream {
		score *= neighborSignalUpstreamFactor
	}
	return score
}

// signalScore maps one signal into the [0.30, 0.60] band. Pure.
func signalScore(sig SignalEvidence) float64 {
	norm := (sig.Ratio - signalRatioFloor) / (signalRatioSaturate - signalRatioFloor)
	if norm < 0 {
		norm = 0
	}
	if norm > 1 {
		norm = 1
	}
	score := signalTierBase + signalRatioSpan*norm
	if sig.Kind == "log_pattern" {
		score += signalLogPatternBonus
	}
	return score
}

// Synthesize fuses the evidence into ONE ranked, confidence-weighted
// hypothesis. anchorKind/anchorID/service stamp the anchor; computedAtNs stamps
// when (the worker passes a single now() so a batch tick shares one timestamp).
// Returns a chstore.RootCauseHypothesis ready to UpsertHypothesis — the worker
// does no further shaping.
//
// Determinism: candidates are built tier-by-tier (deploy, then propagation in
// the order RankRootCausesFromEdges already total-orders, then co-firing sorted
// by name), then a STABLE sort by score desc with a Service-name tie-break, so
// identical evidence always yields byte-identical output (the table-driven test
// asserts the tie-break).
func Synthesize(
	anchorKind, anchorID, service string,
	computedAtNs int64,
	in SynthesisInput,
) chstore.RootCauseHypothesis {
	h := chstore.RootCauseHypothesis{
		AnchorKind: anchorKind,
		AnchorID:   anchorID,
		Service:    service,
		ComputedAt: computedAtNs,
		Candidates: []chstore.ScoredCause{},
	}

	var cands []chstore.ScoredCause
	distinctTypes := 0

	// Tier 1 — the deploy. Strongest single signal; floors above propagation.
	if in.Deploy != nil {
		distinctTypes++
		frac := in.FreshnessFrac
		if frac < 0 {
			frac = 0
		}
		if frac > 1 {
			frac = 1
		}
		score := deployBaseScore + deployFreshnessBonusMax*frac
		ageMin := in.Deploy.AgeSeconds / 60
		reason := fmt.Sprintf("deployed %s %dm before onset — prime 'what changed' suspect",
			in.Deploy.Version, ageMin)
		// v0.9.1059 (Faz 1.4 / K8) — ölçülmüş etki gerekçeye iner:
		// "deploy şüpheli" iddiası artık önce/sonra rakam taşır.
		// Deterministik: aynı impact aynı cümleyi üretir (tablo-testli).
		if in.DeployImpact != nil {
			reason += " — " + deployImpactSummary(in.DeployImpact)
			d := *in.Deploy
			d.Impact = in.DeployImpact
			h.RecentDeploy = &d
		} else {
			h.RecentDeploy = in.Deploy
		}
		cands = append(cands, chstore.ScoredCause{
			Service: service, // a deploy of the anchor's OWN service is the suspect
			Score:   score,
			Hops:    0,
			Path:    []string{service},
			Reason:  reason,
		})
	}
	if len(in.Rollouts) > 0 {
		if in.Deploy == nil {
			distinctTypes++ // deploy ile aynı kanıt türü; ikisi varsa bir kez
		}
		for _, rc := range in.Rollouts {
			if rc.Subject == "" {
				continue
			}
			if in.Deploy != nil && rc.ImageTag != "" && deployMatchesTag(in.Deploy.Version, rc.ImageTag) {
				// Deploy olayı + rollout kaydı aynı imaj: tek aday, doğrulanmış.
				for i := range cands {
					if cands[i].Service == service && cands[i].Kind == "" && cands[i].Hops == 0 {
						sc := cands[i].Score
						if rc.Score > sc {
							sc = rc.Score
						}
						sc += rolloutCorroborationBonus
						if sc > deployBaseScore+deployFreshnessBonusMax {
							sc = deployBaseScore + deployFreshnessBonusMax
						}
						cands[i].Score = sc
						cands[i].Reason += " — rollout kaydıyla doğrulandı: " + rc.Reason
						break
					}
				}
				continue
			}
			cands = append(cands, chstore.ScoredCause{
				Service: rc.Subject,
				Score:   rc.Score,
				Hops:    0,
				Path:    []string{service},
				Kind:    CauseKindRollout,
				Reason:  rc.Reason,
			})
		}
	}

	// Tier 2 — propagation-ranked downstream neighbours. Already best-first.
	// Breadth: komşu KANALI tek sayılır (Neighbours ∪ NeighbourSignals) —
	// v0.9.1056 yeni tier eklerken paydayı (maxEvidenceTypes=4) değiştirmedi,
	// mevcut hipotez confidence'ları oynamasın diye.
	if len(in.Neighbours) > 0 || len(in.NeighbourSignals) > 0 {
		distinctTypes++
	}
	if len(in.Neighbours) > 0 {
		for _, nb := range in.Neighbours {
			if nb.Service == "" || nb.Score <= 0 {
				continue
			}
			reason := fmt.Sprintf("downstream dependency — %.0f%% of error share, %d-hop",
				nb.Score*100, nb.Hops)
			if nb.Kind == chstore.NodeKindNode {
				// v0.10.94 — dikey eksen adayı: skor hata payı değil,
				// aynı node'daki alarmda-komşu oranı; cümle onu söyler.
				reason = fmt.Sprintf("same-node placement — %.0f%% of co-tenant services also firing",
					nb.Score*100)
			}
			cands = append(cands, chstore.ScoredCause{
				Service: nb.Service,
				Score:   propTierBase * nb.Score,
				Hops:    nb.Hops,
				Path:    nb.Path,
				Kind:    nb.Kind,
				Reason:  reason,
			})
		}
	}

	// Tier 2.5 (v0.8.571) — active anomaly signals on the anchor's own
	// service. Emit order is pre-sorted (score desc, Kind asc, Pattern asc):
	// every signal candidate carries Service == anchor, so the global
	// Score/Service tie-break cannot separate equal-score signals — without
	// this pre-sort, map/input order would leak into the output and break
	// the byte-identical determinism the permutation test pins. "log_pattern"
	// sorts before "trace_op" naturally, matching the bonus's intent.
	if len(in.Signals) > 0 {
		distinctTypes++
		sigs := append([]SignalEvidence(nil), in.Signals...)
		sort.SliceStable(sigs, func(i, j int) bool {
			si, sj := signalScore(sigs[i]), signalScore(sigs[j])
			if si != sj {
				return si > sj
			}
			if sigs[i].Kind != sigs[j].Kind {
				return sigs[i].Kind < sigs[j].Kind
			}
			return sigs[i].Pattern < sigs[j].Pattern
		})
		if len(sigs) > signalMaxCandidates {
			sigs = sigs[:signalMaxCandidates]
		}
		for _, sig := range sigs {
			noun := "anomalous trace operation"
			if sig.Kind == "log_pattern" {
				noun = "anomalous log pattern"
			}
			reason := fmt.Sprintf("%s %q on the service", noun, sig.Pattern)
			if sig.Ratio > 0 {
				reason += fmt.Sprintf(" — %.1fx over baseline", sig.Ratio)
			} else {
				reason += " — new, no baseline yet"
			}
			cands = append(cands, chstore.ScoredCause{
				Service: service,
				Score:   signalScore(sig),
				Hops:    0,
				Path:    []string{service},
				Reason:  reason,
			})
		}
	}

	// Tier 2.6 (v0.9.1056) — komşu servislerdeki aktif anomali sinyalleri.
	// Kendi-sinyal tier'ıyla aynı determinizm disiplini: skor desc,
	// Service/Kind/Pattern asc ön-sıralama + tavan. Breadth'e AYRI kanal
	// olarak SAYILMAZ — komşu kanalına katlanır (aşağıda), payda 4 kalır
	// ve komşu-sinyalsiz mevcut girdiler bayt-bayt aynı çıktıyı üretir.
	if len(in.NeighbourSignals) > 0 {
		sigs := append([]NeighbourSignalEvidence(nil), in.NeighbourSignals...)
		sort.SliceStable(sigs, func(i, j int) bool {
			si, sj := neighbourSignalScore(sigs[i]), neighbourSignalScore(sigs[j])
			if si != sj {
				return si > sj
			}
			if sigs[i].Service != sigs[j].Service {
				return sigs[i].Service < sigs[j].Service
			}
			if sigs[i].Kind != sigs[j].Kind {
				return sigs[i].Kind < sigs[j].Kind
			}
			return sigs[i].Pattern < sigs[j].Pattern
		})
		if len(sigs) > neighborSignalMaxCandidates {
			sigs = sigs[:neighborSignalMaxCandidates]
		}
		for _, sig := range sigs {
			if sig.Service == "" {
				continue
			}
			noun := "anomalous trace operation"
			if sig.Kind == "log_pattern" {
				noun = "anomalous log pattern"
			}
			rel := "downstream dependency"
			if !sig.Downstream {
				rel = "upstream caller"
			}
			reason := fmt.Sprintf("%s %q on %s (%s)", noun, sig.Pattern, sig.Service, rel)
			if sig.Ratio > 0 {
				reason += fmt.Sprintf(" — %.1fx over baseline", sig.Ratio)
			} else {
				reason += " — new, no baseline yet"
			}
			hops := sig.Hops
			if hops < 1 {
				hops = 1
			}
			cands = append(cands, chstore.ScoredCause{
				Service: sig.Service,
				Score:   neighbourSignalScore(sig),
				Hops:    hops,
				Path:    []string{service, sig.Service},
				Reason:  reason,
			})
		}
	}

	// Tier 3 — co-firing same-service problems. Corroboration, lowest tier.
	// Dedup + sort the service labels so the candidate order is deterministic
	// and we don't emit two co-firing candidates for the same service.
	if len(in.CoFiringServices) > 0 {
		distinctTypes++
		seen := map[string]bool{}
		coServices := make([]string, 0, len(in.CoFiringServices))
		for _, sv := range in.CoFiringServices {
			if sv == "" || seen[sv] {
				continue
			}
			seen[sv] = true
			coServices = append(coServices, sv)
		}
		sort.Strings(coServices)
		for _, sv := range coServices {
			cands = append(cands, chstore.ScoredCause{
				Service: sv,
				Score:   cofiringScore,
				Hops:    0,
				Path:    []string{sv},
				Reason:  "co-firing problem on the same incident — corroborating, not necessarily the cause",
			})
		}
	}

	// Total order: score desc, then Service name asc. Stable so equal-score
	// candidates keep their tier-insertion order before the name tie-break
	// settles it deterministically.
	sort.SliceStable(cands, func(i, j int) bool {
		if cands[i].Score != cands[j].Score {
			return cands[i].Score > cands[j].Score
		}
		return cands[i].Service < cands[j].Service
	})
	if len(cands) > 0 {
		h.Candidates = cands
		h.TopSuspect = cands[0].Service
		h.TopScore = cands[0].Score
	}

	// Confidence = breadth (distinct evidence types) blended with the top
	// signal's strength. Zero evidence → zero confidence, honestly.
	breadth := float64(distinctTypes) / float64(maxEvidenceTypes)
	if breadth > 1 {
		breadth = 1
	}
	h.Confidence = confidenceBreadthWeight*breadth + confidenceStrengthWeight*h.TopScore
	if h.Confidence > 1 {
		h.Confidence = 1
	}
	return h
}

// deployMatchesTag — deploy olayının sürümü ("1.5.0", "v1.5.0",
// "api:1.5.0") ile rollout imaj tag'i aynı şeyi mi anlatıyor. Tam eşitlik
// ya da sürümün son ':'/'@' sonrası parçası; boşlar eşleşmez.
func deployMatchesTag(version, tag string) bool {
	version = strings.TrimSpace(version)
	tag = strings.TrimSpace(tag)
	if version == "" || tag == "" {
		return false
	}
	if version == tag || strings.TrimPrefix(version, "v") == strings.TrimPrefix(tag, "v") {
		return true
	}
	if i := strings.LastIndexAny(version, ":@"); i >= 0 && i+1 < len(version) {
		return version[i+1:] == tag
	}
	return false
}
