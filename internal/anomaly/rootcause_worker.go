package anomaly

import (
	"context"
	"log"
	"math"
	"strings"
	"time"

	"github.com/cilcenk/coremetry/internal/cache"
	"github.com/cilcenk/coremetry/internal/chstore"
	"github.com/cilcenk/coremetry/internal/correlator"
)

// shouldDeepInvestigate — derin soruşturma kapısı (v0.9.1060, Faz 1.5).
// Saf + tablo-testli: P1 (kayıp/aşım/yaş — operatör kararı, v0.9.516'dan
// beri) VEYA deploy-korelasyonlu (fuser'ın en güçlü şüpheli sınıfı;
// "ne değişti" belliyken kanıtsız kalmak K10'un tarif ettiği boşluktu).
func shouldDeepInvestigate(isP1, hasDeploy bool) bool {
	return isP1 || hasDeploy
}

// enrichDeployImpact — deploy adayı varsa önce/sonra RED kıyasını ölçüp
// girdiye iliştirir (v0.9.1059, Faz 1.4 / K8). ComputeDeployImpact
// yazılalı beri hiçbir kök-neden yolu çağırmıyordu — "deploy şüpheli"
// iddiası rakamsız gidiyordu. Tek sınırlı okuma, YALNIZ deploy adayı
// olan anchor'da (tik başına nadir). İki pencereden biri boşsa
// iliştirilmez: tek taraflı kıyas "%-100 düzeldi" gibi yalan üretir.
func (s *RootCauseSynthesizer) enrichDeployImpact(ctx context.Context, service string, in *correlator.SynthesisInput) {
	if in.Deploy == nil {
		return
	}
	imp, err := s.store.ComputeDeployImpact(ctx, service, in.Deploy.Version, in.Deploy.TimeUnixNs, 0)
	if err != nil || imp == nil || imp.Before.Count == 0 || imp.After.Count == 0 {
		return
	}
	in.DeployImpact = imp
}

// anchorExemplar — anchor penceresinin temsilî trace'i (v0.9.1057,
// Faz 1.2). spanmetrics rollup argMax okuması: MV, LIMIT 1, ucuz;
// tablo yoksa / pencere rollup öncesiyse / örnek yoksa fail-open "".
// Kind seçimi metrikten: error_rate → hata örneği, gerisi → en yavaş.
// Pencere [started, now], evidenceWindow'a kıstırılır (worker'ın kendi
// bakış ufku; sınırsız geçmiş taraması bu yolda da yasak).
func (s *RootCauseSynthesizer) anchorExemplar(ctx context.Context, service, metric string, startedNs int64, now time.Time) string {
	from := time.Unix(0, startedNs)
	if lo := now.Add(-evidenceWindow); from.Before(lo) {
		from = lo
	}
	if !from.Before(now) {
		from = now.Add(-5 * time.Minute)
	}
	kind := chstore.ExemplarSlow
	if strings.Contains(metric, "error") {
		kind = chstore.ExemplarError
	}
	ex, err := s.store.FindExemplarRollup(ctx, chstore.ExemplarReq{
		Service: service, From: from, To: now, Kind: kind,
	})
	if err != nil || ex == nil {
		return ""
	}
	return ex.TraceID
}

// RootCauseSynthesizer is the leader-gated background worker that pre-computes
// + persists a root-cause hypothesis per anchor (rc #2 of the anomaly →
// root-cause feature). Same shape as ProblemExplainer: a per-tick Redis-leader
// lock, a bounded batch, one shared evidence fetch per tick. For each candidate
// anchor (a recent/active AnomalyEvent OR a high-severity open Problem) it
// builds the SAME cross-signal EvidenceBundle the explainer + the on-demand
// /rootcause fan-out use, distils it into correlator.SynthesisInput, calls the
// PURE correlator.Synthesize fuser, and upserts the ranked hypothesis. The
// /anomalies + /problems ribbon (rc #3) then reads the persisted row with no
// per-request synthesis.
//
// Bounded by construction: gatherEvidenceInputs runs ONCE per tick (four
// already-bounded reads, shared across the batch — NO new unbounded CH query);
// the anchor lists are capped (top-N by severity / recency); the fuser is pure.
// Leader-gated so multi-pod runs don't duplicate-write. Worker/all modes only
// (wired next to the explainer in main.go) — ingest/api pods don't run it.
const synthesizerLockKey = "rootcause-synthesizer:lock"

const (
	// synthesizerBatch caps how many anchors one tick synthesizes — like the
	// explainer's critical-only batch, this keeps a thundering herd of opens
	// from blowing the tick's CH budget. Higher than the explainer's 16
	// because Synthesize is pure (no LLM round-trip) — the only cost is the
	// shared evidence fetch + the per-anchor upsert.
	synthesizerBatch = 64
	// synthesizerRecentAnomaly — only anomalies whose last_seen is within this
	// window are worth a hypothesis; older ones have cleared and nobody is
	// triaging them.
	synthesizerRecentAnomaly = 30 * time.Minute
)

// AutoVerdictFunc — derin soruşturma sonrası kök-neden VERDICT'ini üretip
// kalıcılaştıran kanca (v0.9.1281).
//
// Neden fonksiyon değeri, neden doğrudan çağrı değil: üretici
// api.Server'da yaşıyor (prompt + kalkanlar + kanıt kataloğu orada) ve
// api paketi zaten anomaly'yi import ediyor (insight.go). Ters yönde bir
// import derleme döngüsü olurdu. Paketi taşımak yerine — ki bu kalkan
// zincirinin tamamını kıpırdatırdı — main.go kablolamasında tek bir
// metot değeri enjekte ediliyor.
//
// nil ⇒ özellik kapalı (api/ingest kipleri, ya da worker'ın Server'dan
// önce başladığı bir kablolama). Kanca yokken davranış v0.9.1280 ile
// birebir aynı.
type AutoVerdictFunc func(ctx context.Context, anchorKind string, h *chstore.RootCauseHypothesis, anchorStartNs int64) error

type RootCauseSynthesizer struct {
	store    *chstore.Store
	lock     cache.Lock
	leader   *cache.LeaderHolder
	interval time.Duration
	batch    int
	// autoVerdict — bkz. AutoVerdictFunc. Start() öncesinde SetAutoVerdict
	// ile kuruluyor; tick'ler tek goroutine'de koştuğu için yarış yok.
	autoVerdict AutoVerdictFunc
}

// SetAutoVerdict — kancayı bağlar. Start()'tan ÖNCE çağrılmalı (main.go
// bunu yapıyor): sonrasında yazmak, koşan tick'le yarışırdı.
func (s *RootCauseSynthesizer) SetAutoVerdict(fn AutoVerdictFunc) {
	if s == nil {
		return
	}
	s.autoVerdict = fn
}

func NewRootCauseSynthesizer(store *chstore.Store, lock cache.Lock) *RootCauseSynthesizer {
	interval := 30 * time.Second
	return &RootCauseSynthesizer{
		store:    store,
		lock:     lock,
		leader:   cache.NewLeaderHolder(lock, synthesizerLockKey, cache.LeaderTTL(interval)),
		interval: interval,
		batch:    synthesizerBatch,
	}
}

// Start runs the synthesis loop until ctx is cancelled. Initial tick fires
// immediately so an anchor that opened during pod startup gets a hypothesis
// without waiting a full interval (same as the explainer).
func (s *RootCauseSynthesizer) Start(ctx context.Context) {
	if s == nil {
		return
	}
	s.leader.Start(ctx)
	t := time.NewTicker(s.interval)
	defer t.Stop()
	s.tickIfLeader(ctx)
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			s.tickIfLeader(ctx)
		}
	}
}

func (s *RootCauseSynthesizer) tickIfLeader(ctx context.Context) {
	if !s.leader.IsLeader() {
		return
	}
	s.run(ctx)
}

// run gathers the shared evidence once, then synthesizes + upserts a hypothesis
// for each candidate anchor up to the batch cap. Anomalies first (the feature's
// primary anchor), then high-severity open problems, sharing the SAME evidence
// inputs so the tick costs four bounded reads total regardless of anchor count.
func (s *RootCauseSynthesizer) run(ctx context.Context) {
	now := time.Now()
	in := gatherEvidenceInputs(ctx, s.store)

	done := 0

	// Anchor 1 — recent / active anomalies. ListAnomalyEvents is bounded by
	// last_seen >= sinceNs + LIMIT; we reuse the SinceNs the evidence fetch
	// already uses, but cap to the synthesizer's own recency window so a 60m
	// evidence window doesn't drag in already-cleared anomalies.
	events, err := s.store.ListAnomalyEvents(ctx, chstore.ListAnomalyEventsFilter{
		SinceNs: now.Add(-synthesizerRecentAnomaly).UnixNano(),
		Limit:   s.batch,
	})
	if err != nil {
		log.Printf("[rootcause-synth] list anomalies: %v", err)
	}
	for _, ev := range events {
		if done >= s.batch {
			break
		}
		synthIn := synthInputForAnomaly(ev, in)
		s.enrichDeployImpact(ctx, ev.Service, &synthIn) // v0.9.1059
		h := correlator.Synthesize(
			"anomaly", ev.ID, ev.Service, now.UnixNano(),
			synthIn,
		)
		// v0.9.1057 (Faz 1.2 / K5) — temsilî trace hipoteze. trace_op
		// olayında dedektörün örneği zaten trace id (recorder Sample'a
		// SampleTraceID yazar); diğerlerinde rollup argMax (MV, ucuz,
		// fail-open boş).
		if ev.Kind == "trace_op" && strings.TrimSpace(ev.Sample) != "" {
			h.ExemplarTraceID = strings.TrimSpace(ev.Sample)
		} else {
			h.ExemplarTraceID = s.anchorExemplar(ctx, ev.Service, "", ev.StartedAt, now)
		}
		if err := s.store.UpsertHypothesis(ctx, h); err != nil {
			log.Printf("[rootcause-synth] upsert anomaly %s: %v", ev.ID, err)
			continue
		}
		done++
	}

	// Anchor 2 — high-severity (critical) open problems, same as the
	// explainer's batch. buildEvidenceBundle is the existing pure fuser over
	// the shared inputs; we distil its bundle into the synthesis input.
	if done < s.batch {
		// v0.10.156 — snapshot (5 s memo); ayrı `problems FINAL` taraması yok.
		var problems []chstore.Problem
		if snap, err := s.store.OpenProblemsSnapshot(ctx); err != nil {
			log.Printf("[rootcause-synth] list problems: %v", err)
		} else {
			problems = snap.Filter("open", "critical", s.batch)
		}
		for _, p := range problems {
			if done >= s.batch {
				break
			}
			if synthesizerSkipsProblem(p) {
				continue
			}
			bundle := buildEvidenceBundle(p, in)
			synthIn := synthInputForProblem(p, bundle)
			appendNodeCauses(&synthIn, in, p.Service)
			s.enrichDeployImpact(ctx, p.Service, &synthIn) // v0.9.1059
			h := correlator.Synthesize(
				"problem", p.ID, p.Service, now.UnixNano(),
				synthIn,
			)
			// v0.9.1057 (Faz 1.2 / K5) — problemin penceresinden temsilî
			// trace; metrik hata ailesindeyse hata örneği.
			h.ExemplarTraceID = s.anchorExemplar(ctx, p.Service, p.Metric, p.StartedAt, now)
			// v0.9.516 — P1 SORUŞTURMASI ARTIK BURADA. v0.9.510'da
			// explainer'daydı ama denetim izini KALICI kılmak için
			// hipotezle aynı satıra yazılması gerekiyordu ve bu satırı
			// synthesizer yazıyor. ReplacingMergeTree tam-satır replace
			// olduğu için explainer'dan "sadece izi güncelle" demek diğer
			// alanları riske atardı (iki işçi aynı satıra yazar, kaybeden
			// diğerinin alanlarını siler).
			//
			// Tek toplama, tek yazma, iki okuyucu: explainer prompt için,
			// ribbon/denetim sorguları için.
			//
			// Maliyet kapısı v0.9.1060'ta GENİŞLEDİ (Faz 1.5 / K10):
			// "yalnız P1" → "P1 VEYA deploy-korelasyonlu". Deploy,
			// fuser'ın EN GÜÇLÜ şüpheli sınıfı (0.80 taban) ama problem
			// bağımsız olarak 2× aşmadıkça P1 kapısından geçemiyor ve
			// derin kanıt hiç toplanmıyordu — tam da "ne değişti"nin
			// belli olduğu vakada. Anchor kümesi zaten yalnız critical
			// açık problemler + tik batch tavanı; aile başına bir
			// sınırlı okuma sözleşmesi aynen.
			prio := chstore.EnrichProblemsWithPriority([]chstore.Problem{p})
			isP1 := len(prio) > 0 && prio[0].Priority == "P1"
			deep := shouldDeepInvestigate(isP1, bundle.Deploy != nil)
			if deep {
				if plan := investigationPlan(p); len(plan) > 0 {
					d := gatherDeepEvidence(ctx, s.store, p, plan, now.Add(-evidenceWindow), now)
					h.Deep = &d
				}
			}
			if err := s.store.UpsertHypothesis(ctx, h); err != nil {
				log.Printf("[rootcause-synth] upsert problem %s: %v", p.ID, err)
				continue
			}
			// v0.9.1281 — OTOMATİK VERDICT, derin soruşturma kapısında.
			//
			// Kapı KASTEN genişletilmedi: yalnız derin soruşturmanın
			// ZATEN koştuğu vakalar (P1 veya deploy-korelasyonlu).
			// Gerekçe iki katlı — (1) kota: yeni bir tetikleyici sınıf,
			// açık her critical problemi LLM çağrısına çevirirdi ve
			// prod'da o sayı 100'ün üstünde; (2) kalite: verdict'in
			// değeri kanıtın derinliğiyle orantılı, sığ paketle üretilen
			// bir karar hem pahalı hem zayıf olurdu.
			//
			// Hipotez YAZILDIKTAN SONRA çağrılıyor ve sırası önemli:
			// üretici h.Deep'i okuyor, yani kanıt kalıcı olmadan
			// üretilen bir verdict, dayandığı kanıtı gösteremezdi.
			//
			// EN İYİ ÇABA: hata hipotez yazımını geçersiz kılmaz,
			// yalnız loglanır. Bu döngünün asıl ürünü hipotez; verdict
			// onun üstüne bir ek.
			if deep && s.autoVerdict != nil {
				if err := s.autoVerdict(ctx, "problem", &h, p.StartedAt); err != nil {
					log.Printf("[rootcause-synth] auto verdict problem %s: %v", p.ID, err)
				}
			}
			done++
		}
	}

	if done > 0 {
		log.Printf("[rootcause-synth] synthesized %d hypothesis/es", done)
	}
}

// synthInputForProblem distils a problem's EvidenceBundle into the pure fuser's
// input: the deploy (with a freshness fraction relative to the lookback
// window), the propagation-ranked neighbour suspects (best first, already
// scored by buildEvidenceBundle), and the co-firing same-service problems'
// services. Pure helper — no store, no ctx.
func synthInputForProblem(p chstore.Problem, b EvidenceBundle) correlator.SynthesisInput {
	in := correlator.SynthesisInput{
		Deploy:        deployFromEntry(b.Deploy, p.StartedAt),
		FreshnessFrac: freshnessFrac(b.Deploy, p.StartedAt),
	}
	for _, nb := range b.Neighbors {
		in.Neighbours = append(in.Neighbours, correlator.ScoredCause{
			Service: nb.Problem.Service,
			Score:   nb.Score,
			Hops:    nb.Hops,
		})
	}
	for _, cp := range b.CoFiring {
		in.CoFiringServices = append(in.CoFiringServices, cp.Service)
	}
	// v0.8.571 — the bundle collected these since day one; the synthesis
	// never saw them. buildEvidenceBundle already filtered to same-service
	// + active, so this is a straight mapping. Ratio = the stronger of the
	// current and peak readings (a spike that just cooled still evidences).
	for _, sig := range b.Signals {
		in.Signals = append(in.Signals, correlator.SignalEvidence{
			Kind:    sig.Kind,
			Pattern: sig.Pattern,
			Ratio:   math.Max(sig.CurrentRatio, sig.PeakRatio),
		})
	}
	// v0.9.1056 (Faz 1.1) — komşu sinyalleri fuser'ın 2.6 tier'ına.
	for _, ns := range b.NeighborSignals {
		in.NeighbourSignals = append(in.NeighbourSignals, correlator.NeighbourSignalEvidence{
			Service:    ns.Event.Service,
			Kind:       ns.Event.Kind,
			Pattern:    ns.Event.Pattern,
			Ratio:      math.Max(ns.Event.CurrentRatio, ns.Event.PeakRatio),
			Downstream: ns.Direction == "calls",
			Hops:       ns.Hops,
		})
	}
	return in
}

// synthInputForAnomaly assembles the SAME synthesis input for an anomaly anchor
// directly from the shared evidence inputs — there is no Problem-shaped bundle
// for an anomaly, so we mirror buildEvidenceBundle's deploy + propagation +
// co-firing logic against the AnomalyEvent's service/onset. Pure helper.
func synthInputForAnomaly(ev chstore.AnomalyEvent, in evidenceInputs) correlator.SynthesisInput {
	out := correlator.SynthesisInput{}

	// Deploy of THIS service whose first-seen lands in the lookback window
	// before onset — the most recent such is the prime "what changed". Same
	// selection rule buildEvidenceBundle applies to a Problem.
	lo := ev.StartedAt - evidenceDeployLookback.Nanoseconds()
	var deploy *chstore.RecentDeployEntry
	for i := range in.deploys {
		d := &in.deploys[i]
		if d.Service != ev.Service || d.FirstSeenNs < lo || d.FirstSeenNs > ev.StartedAt {
			continue
		}
		if deploy == nil || d.FirstSeenNs > deploy.FirstSeenNs {
			deploy = d
		}
	}
	out.Deploy = deployFromEntry(deploy, ev.StartedAt)
	out.FreshnessFrac = freshnessFrac(deploy, ev.StartedAt)

	// Propagation-ranked downstream suspects over the weighted graph, anchored
	// on the anomaly's service. Same scorer the bundle uses. Only neighbours
	// that ALSO carry an open problem corroborate, but for the hypothesis we
	// surface the propagation suspects directly (the fuser drops zero-score
	// ones) so a downstream source shows even without its own open Problem.
	for _, sc := range correlator.RankRootCausesFromEdges(in.weightedAdjacency, ev.Service) {
		if sc.Service == "" || sc.Score <= 0 {
			continue
		}
		out.Neighbours = append(out.Neighbours, sc)
	}
	appendNodeCauses(&out, in, ev.Service)

	// Co-firing — other OPEN problems on the SAME service as the anomaly.
	for _, op := range in.openProblems {
		if op.Service == ev.Service {
			out.CoFiringServices = append(out.CoFiringServices, op.Service)
		}
	}
	// Signals (v0.8.571) — other active anomalies on the same service.
	// ID != ev.ID excludes the anchor itself (the mirror of the problem
	// path's op.ID exclusion in buildEvidenceBundle): an anomaly must not
	// corroborate its own hypothesis.
	//
	// v0.9.1056 — komşu sinyalleri de toplanır (buildEvidenceBundle'ın
	// aynası): 1-hop komşuluk in.adjacency'den, skorlu ≤2-hop şüpheliler
	// yukarıdaki propagation yürüyüşünden.
	dir := map[string]string{}
	for _, e := range in.adjacency {
		if e.Caller == ev.Service && e.Callee != ev.Service {
			dir[e.Callee] = "calls"
		}
		if e.Callee == ev.Service && e.Caller != ev.Service {
			if _, ok := dir[e.Caller]; !ok {
				dir[e.Caller] = "called_by"
			}
		}
	}
	scored := map[string]correlator.ScoredCause{}
	for _, sc := range out.Neighbours {
		scored[sc.Service] = sc
	}
	for _, sig := range in.events {
		if sig.Status != "active" || sig.ID == ev.ID {
			continue
		}
		if sig.Service == ev.Service {
			out.Signals = append(out.Signals, correlator.SignalEvidence{
				Kind:    sig.Kind,
				Pattern: sig.Pattern,
				Ratio:   math.Max(sig.CurrentRatio, sig.PeakRatio),
			})
			continue
		}
		sc, isScored := scored[sig.Service]
		direction := dir[sig.Service]
		if direction == "" && !isScored {
			continue
		}
		if direction == "" {
			direction = "calls"
		}
		out.NeighbourSignals = append(out.NeighbourSignals, correlator.NeighbourSignalEvidence{
			Service:    sig.Service,
			Kind:       sig.Kind,
			Pattern:    sig.Pattern,
			Ratio:      math.Max(sig.CurrentRatio, sig.PeakRatio),
			Downstream: direction == "calls",
			Hops:       sc.Hops,
		})
	}
	return out
}

// deployFromEntry converts the evidence bundle's *RecentDeployEntry into the
// *RecentDeploy shape the hypothesis persists (the same compact deploy signal
// the /problems + /anomalies lists already attach). AgeSeconds is positive when
// the deploy landed before onset (the typical correlate-with-incident case),
// computed from the anchor's onset minus the deploy's first-seen. Returns nil
// for a nil entry. Pure.
func deployFromEntry(d *chstore.RecentDeployEntry, onsetNs int64) *chstore.RecentDeploy {
	if d == nil {
		return nil
	}
	return &chstore.RecentDeploy{
		Version:    d.Version,
		TimeUnixNs: d.FirstSeenNs,
		AgeSeconds: (onsetNs - d.FirstSeenNs) / int64(time.Second),
	}
}

// freshnessFrac maps how recently a deploy landed before onset into [0,1] for
// the fuser's deploy freshness bonus: 1 = right at onset, 0 = at the far edge
// of the evidenceDeployLookback window (or no deploy). A deploy that somehow
// post-dates onset (clock skew) clamps to 1 (treated as maximally fresh, since
// it is still the closest-in-time change). Pure.
func freshnessFrac(d *chstore.RecentDeployEntry, onsetNs int64) float64 {
	if d == nil {
		return 0
	}
	lookback := float64(evidenceDeployLookback.Nanoseconds())
	if lookback <= 0 {
		return 0
	}
	ageNs := float64(onsetNs - d.FirstSeenNs) // >0 when deploy precedes onset
	frac := 1 - ageNs/lookback
	if frac < 0 {
		frac = 0
	}
	if frac > 1 {
		frac = 1
	}
	return frac
}

// appendNodeCauses — dikey eksen adaylarını her iki anchor yoluna da
// ekler (v0.10.94; anomali + problem — sözleşme iki yerde yaşıyor,
// tek yardımcı iki çağrı). "Alarmda" kümesi: anchor'ın KENDİSİ hariç,
// açık problemi ya da aktif anomalisi olan servisler. Aday sırası
// RankNodeCauses'ta deterministik — hipotezin bayt-özdeş determinizm
// pinleri bozulmaz.
func appendNodeCauses(dst *correlator.SynthesisInput, in evidenceInputs, anchor string) {
	if len(in.runsOn) == 0 {
		return
	}
	firing := map[string]bool{}
	for _, op := range in.openProblems {
		if op.Service != "" && op.Service != anchor {
			firing[op.Service] = true
		}
	}
	for _, ev := range in.events {
		if ev.Status == "active" && ev.Service != "" && ev.Service != anchor {
			firing[ev.Service] = true
		}
	}
	dst.Neighbours = append(dst.Neighbours,
		correlator.RankNodeCauses(in.runsOn, anchor, firing)...)
}

// synthesizerSkipsProblem (v0.10.229, Influx D4 audit E2) — dış kaynak
// (kind=external) anchor'ları sentezlenmez: özne servis değil (topoloji
// yok, aday yok) ve UpsertHypothesis TAM-SATIR yazar — buradan geçen bir
// external anchor, influx.Enricher'ın yazdığı DeepEvidence'ı SİLERDİ.
// SAF; rootcause_worker_external_test.go pinler.
func synthesizerSkipsProblem(p chstore.Problem) bool {
	return p.Kind == chstore.ProblemKindExternal
}
