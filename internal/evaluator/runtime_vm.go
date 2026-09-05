package evaluator

// runtime_vm.go — JVM GC ALARMLARININ VICTORIAMETRICS DÖNÜŞÜ
// (v0.9.1213). v0.9.1075 emekliliği operatör kararıydı: "şimdilik
// kaldıralım; VictoriaMetrics geçme planım var, o zaman düşünürüz."
// Geçiş gerçekleşti (VM prod'da okuma backend'i, v0.9.1150+); saf karar
// çekirdekleri (jvmGCPauseDecision/jvmGCShareDecision) o gün bu dönüş
// için tablo testleriyle yerinde bırakılmıştı — kaynak değişiyor, eşik
// semantiği değişmiyor.
//
// Kaynak YALNIZ VM: vmetrics yapılandırılmamışsa GC değerlendirmesi
// emekliliğinde kalır ve tahliye açık satırları kapatmaya devam eder
// (operatör önermesi CH-tabanlı alarm istemiyordu; sessiz CH dönüşü o
// kararı arkadan dolanmak olurdu). VM sonradan kapatılırsa açık GC
// problemleri bir sonraki tikte tahliyeyle, nedeni yazılarak kapanır.
//
// SORGU ŞEKLİ — iki `increase`, üç türetme:
//   sum:  increase(jvm_gc_duration_seconds_sum[10m])   → pencere GC saniyesi
//   cnt:  increase(jvm_gc_duration_seconds_count[10m]) → pencere koleksiyon sayısı
//   pauseMs   = sum/cnt*1000   (ağırlıklı ortalama duraklama)
//   sharePct  = sum/600*100
//   ratePerMin= cnt/10
// Filtresiz histogram-`avg` BİLİNÇLİ kullanılmadı: vmetrics'in
// bucket-tarama guard'ı (v0.9.1164) onu haklı olarak reddeder; increase
// guard'dan muaf ve pencere-toplamı zaten istediğimiz şeyin kendisi.
// Adlar Prometheus yazımıyla VERBATIM verilir (nameSpellings verbatim'i
// ilk aday tutar); pod kimliği chstore.runtimePodExpr'in üç-katmanlı
// coalesce'inin istemci-taraflı ikizidir (vmPodFromTuple).

import (
	"context"
	"fmt"
	"log"
	"time"

	"github.com/cilcenk/coremetry/internal/chstore"
	"github.com/cilcenk/coremetry/internal/vmetrics"
)

// runtimeVMGroupBy — seri kimliği: servis + pod coalesce zinciri.
// Sıra vmPodFromTuple'ın okuduğu konumlarla ÇAKILI.
// v0.10.374 — the pure pieces moved to vmetrics/runtime_pods.go so the
// anomaly investigation and the MCP pod-health tool read JVM pods from
// the SAME translation; the names below stay as thin delegations (the
// evaluator's tests pin them).
var runtimeVMGroupBy = vmetrics.RuntimePodGroupBy

func vmPodFromTuple(groupKey []string) string { return vmetrics.PodFromTuple(groupKey) }

func vmGCDerive(sumSec, cnt, windowSec float64) (pauseMs, sharePct, ratePerMin float64) {
	return vmetrics.GCDerive(sumSec, cnt, windowSec)
}

func joinVMGCSeries(sums, cnts []chstore.SpanMetricSeries, windowSec float64) ([]chstore.CapacitySample, []chstore.GCActivitySample, error) {
	return vmetrics.JoinGCSeries(sums, cnts, windowSec)
}

func (e *Evaluator) vmGCSamples(ctx context.Context) ([]chstore.CapacitySample, []chstore.GCActivitySample, error) {
	now := time.Now()
	return e.vmetrics.JVMGCPodStats(ctx, now.Add(-chstore.RuntimePodWindow), now)
}

// joinVMGCSeries — SAF birleşim: (servis, pod) anahtarında sum×cnt.
// Karşılığı olmayan seri atlanır (yarım veri sample üretmez).

// ── Reconcile makineleri — v0.9.1074'ten geri (v0.9.1213) ──────────────
//
// v0.9.1075 değerlendirmeyle birlikte bu makineleri de silmişti; VM
// dönüşünde bayt-uyumlu geri geldiler (tek fark: örnekler artık CH
// yerine VM'den geliyor — karar çekirdekleri ve problem kimlikleri aynı,
// yani eski bir kurulumdan kalan açık GC problemi yeni değerlendirmede
// AYNI kimlikle bulunur ve doğal resolve kolundan kapanır).

func (e *Evaluator) reconcileRuntimeGC(ctx context.Context, s chstore.CapacitySample, cfg chstore.RuntimeAlertConfig, snap *chstore.OpenProblems) {
	const ruleID = "runtime:jvm-gc"
	service := runtimeService(s.Instance, s.Subkey)
	existing := snap.ByID(runtimeProblemID("jvm-gc", s.Instance, s.Subkey))
	hasOpen := existing != nil && existing.ID != ""
	open, sev := jvmGCPauseDecision(s.Usage, hasOpen, cfg)
	reason := fmt.Sprintf("JVM GC pause avg %.0fms on %s · pod %s", s.Usage, service, s.Subkey)
	thr := cfg.GCPauseWarnMs
	if sev == "critical" {
		thr = cfg.GCPauseCritMs
	}
	e.reconcileRuntime(ctx, runtimeReconcile{
		ruleID: ruleID, ruleName: "Runtime · JVM GC pause", metric: "runtime.jvm_gc",
		service: service, pod: s.Subkey, problemID: runtimeProblemID("jvm-gc", s.Instance, s.Subkey),
		open: open, hasOpen: hasOpen, existing: existing,
		severity: sev, value: s.Usage, threshold: thr, reason: reason,
	})
}

func (e *Evaluator) reconcileRuntimeGCShare(ctx context.Context, a chstore.GCActivitySample, cfg chstore.RuntimeAlertConfig, snap *chstore.OpenProblems) {
	const ruleID = "runtime:jvm-gc-share"
	service := runtimeService(a.Service, a.Pod)
	existing := snap.ByID(runtimeProblemID("jvm-gc-share", a.Service, a.Pod))
	hasOpen := existing != nil && existing.ID != ""
	open, sev := jvmGCShareDecision(a.SharePct, hasOpen, cfg)
	reason := fmt.Sprintf("JVM zamanının %%%.1f'i GC'de (≈%.0f koleksiyon/dk, 10dk penceresi) on %s · pod %s",
		a.SharePct, a.RatePerMin, service, a.Pod)
	thr := cfg.GCShareWarnPct
	if sev == "critical" {
		thr = cfg.GCShareCritPct
	}
	e.reconcileRuntime(ctx, runtimeReconcile{
		ruleID: ruleID, ruleName: "Runtime · JVM GC zaman payı", metric: "runtime.jvm_gc_share",
		service: service, pod: a.Pod, problemID: runtimeProblemID("jvm-gc-share", a.Service, a.Pod),
		open: open, hasOpen: hasOpen, existing: existing,
		severity: sev, value: a.SharePct, threshold: thr, reason: reason,
	})
}

type runtimeReconcile struct {
	ruleID, ruleName, metric, service, problemID, pod string
	open, hasOpen                                     bool
	existing                                          *chstore.Problem
	severity                                          string
	value, threshold                                  float64
	reason                                            string
}

func (e *Evaluator) reconcileRuntime(ctx context.Context, r runtimeReconcile) {
	switch {
	case r.open && !r.hasOpen:
		now := time.Now()
		p := chstore.Problem{
			ID:          r.problemID,
			RuleID:      r.ruleID,
			RuleName:    r.ruleName,
			Severity:    r.severity,
			Service:     r.service,
			Pod:         r.pod,
			Metric:      r.metric,
			Value:       r.value,
			Threshold:   r.threshold,
			Status:      "open",
			Description: r.reason,
			StartedAt:   now.UnixNano(),
		}
		if err := e.store.UpsertProblem(ctx, p); err != nil {
			log.Printf("[evaluator] runtime open %s/%s: %v", r.ruleID, r.service, err)
			return
		}
		e.countOpened()
		log.Printf("[evaluator] PROBLEM OPENED (%s): %s", r.metric, p.Description)
		if _, err := e.store.AttachProblemToIncident(ctx, p); err != nil {
			log.Printf("[evaluator] runtime incident attach: %v", err)
		}
		if e.notifier != nil {
			go e.notifier.SendProblemAlert(context.Background(), p)
		}

	case r.open && r.hasOpen:
		// Canlı değer + severity tazele; StartedAt korunur; yaş-bazlı
		// eskalasyon tabanına clamp (v0.8.309). v0.9.401 service self-heal.
		r.existing.Service = r.service
		r.existing.Pod = r.pod
		r.existing.Value = r.value
		r.existing.Severity = effectiveSeverity(r.severity, time.Since(time.Unix(0, r.existing.StartedAt)), e.escalationCfg(ctx))
		r.existing.Threshold = r.threshold
		r.existing.Description = r.reason
		if err := e.store.UpsertProblem(ctx, *r.existing); err != nil {
			log.Printf("[evaluator] runtime refresh %s/%s: %v", r.ruleID, r.service, err)
		}

	case !r.open && r.hasOpen:
		// v0.9.977 — ihlal değeri korunur.
		chstore.MarkResolved(r.existing, time.Now().UnixNano())
		if err := e.store.UpsertProblem(ctx, *r.existing); err != nil {
			log.Printf("[evaluator] runtime resolve %s/%s: %v", r.ruleID, r.service, err)
		} else {
			e.countResolved()
			log.Printf("[evaluator] PROBLEM RESOLVED (%s): %s", r.metric, r.reason)
		}
	}
}
