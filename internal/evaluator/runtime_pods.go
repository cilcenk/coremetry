package evaluator

import (
	"context"
	"fmt"
	"log"
	"time"

	"github.com/cilcenk/coremetry/internal/chstore"
)

// runtime_pods.go — JVM pod-level runtime alerting (v0.9.90, operatör
// talebi: "sorunlu pod olursa haber versin / problem tetiklesin").
//
// Overview'daki Runtime paneli (v0.9.87-89) heap/GC'yi pod bazında
// GÖSTERİR; bu geçiş aynı sinyalleri PAGEABLE yapar. db_capacity.go
// deseninin birebiri: leader-locked evaluateAll tick'i, MetricExists
// probe'u (JVM metriği akmayan kurulum hiç Problem görmez — lokal dahil),
// FindOpenProblem/UpsertProblem dedup, notify fan-out, incident-attach.
//
// Denetimler (eşikler sabit — capacity emsali; Settings'e taşıma ayrı iş):
//   • jvm-heap: 10-dk ort. toplam heap / -Xmx. Warn ≥85%, crit ≥90%,
//     hysteresis 2pp (v0.8.320 emsali — sınıra park etmiş gauge churn'u).
//   • jvm-gc: 10-dk ort. GC pause. Warn ≥500ms, crit ≥1000ms, hysteresis
//     50ms. (Dynatrace'in "GC sağlığı" barı; per-export ortalama pause.)
//
// .NET v1'de bilinçli dışarıda: process.runtime.dotnet.* heap'inde limit
// gauge'u yok (oran kurulamaz), GC pause histogram'ı 6/8 runtime
// instrumentation'da hiç yok.
const (
	jvmHeapWarnPct = 85.0
	jvmHeapCritPct = 90.0
	jvmHeapHystPct = 2.0
	// v0.9.426 (operator-reported, prod: "JVM hatası olmayan podlara
	// alert") — anlık used/max testere-dişlidir: sağlıklı JVM, GC
	// tetiklenmeden önce %85-95'e dokunur ve 10dk ortalaması bile
	// yüksek kalabilir. GC-SONRASI sinyal (used_after_last_gc) varsa
	// 85/90 eşikleri anlamlıdır ve o kullanılır; yoksa anlık sinyalin
	// eşiği 92/97'ye çıkar — gerçek doluluk baskısı ancak o bölgede
	// GC'ye rağmen inmeyen heap demektir.
	jvmHeapRawWarnPct = 92.0
	jvmHeapRawCritPct = 97.0
	jvmGCWarnMs       = 500.0
	jvmGCCritMs       = 1000.0
	jvmGCHystMs       = 50.0
	// v0.9.440 (operatör istegi: "çok uzun GC + GC sayısı yüksek podlar")
	// — GC ZAMAN PAYI: pencerede GC'de geçen sürenin yüzdesi. Tek ölçüde
	// iki şikâyet: uzun pause'lar da sık kısa pause'lar da payı şişirir.
	// >%10 = throughput acısı (soruşturulmalı), >%25 = ciddi; 2pp
	// histerezis (jvm-heap ile aynı sınıf).
	jvmGCShareWarnPct = 10.0
	jvmGCShareCritPct = 25.0
	jvmGCShareHystPct = 2.0
)

// jvmHeapDecision — saf eşik çekirdeği (tablo testli). capacityDecision'la
// aynı şekil ama runtime eşikleri bağımsız evrilebilsin diye ayrı.
// postGC > 0 → GC-sonrası sinyal + 85/90; postGC yok → anlık used + 92/97.
// postBased dönüşü reason/threshold metnini dürüst kılar.
func jvmHeapDecision(usage, postGC, limit float64, wasOpen bool) (open bool, severity string, pct float64, postBased bool) {
	if limit <= 0 {
		return false, "", 0, false
	}
	val, warn, crit := usage, jvmHeapRawWarnPct, jvmHeapRawCritPct
	if postGC > 0 {
		val, warn, crit, postBased = postGC, jvmHeapWarnPct, jvmHeapCritPct, true
	}
	pct = val / limit * 100
	switch {
	case pct >= crit:
		return true, "critical", pct, postBased
	case pct >= warn:
		return true, "warning", pct, postBased
	case wasOpen && pct >= warn-jvmHeapHystPct:
		return true, "warning", pct, postBased
	default:
		return false, "", pct, postBased
	}
}

// jvmGCPauseDecision — saf eşik çekirdeği (tablo testli). avgMs = pencere
// ortalaması GC pause.
func jvmGCPauseDecision(avgMs float64, wasOpen bool) (open bool, severity string) {
	switch {
	case avgMs >= jvmGCCritMs:
		return true, "critical"
	case avgMs >= jvmGCWarnMs:
		return true, "warning"
	case wasOpen && avgMs >= jvmGCWarnMs-jvmGCHystMs:
		return true, "warning"
	default:
		return false, ""
	}
}

// jvmGCShareDecision — saf eşik çekirdeği (tablo testli): GC zaman
// payı yüzdesi üzerinden warn/crit + histerezis.
func jvmGCShareDecision(sharePct float64, wasOpen bool) (open bool, severity string) {
	switch {
	case sharePct >= jvmGCShareCritPct:
		return true, "critical"
	case sharePct >= jvmGCShareWarnPct:
		return true, "warning"
	case wasOpen && sharePct >= jvmGCShareWarnPct-jvmGCShareHystPct:
		return true, "warning"
	default:
		return false, ""
	}
}

// runtimeService — Problem.service kolonu: YALNIZ gerçek servis adı.
// v0.9.401 (operator-reported, prod): eski "servis·pod" birleşimi
// (capacityService emsalinden kopya) UI'nin service sözleşmesini
// kırıyordu — P1 listesinde servis adı görünmüyor, tıklama
// /service?name=<servis·pod> diye SAHTE servise gidiyordu (0 req/s
// boş overview). Pod kimliği problemID'de yaşamaya devam eder
// (per-pod dedup oradan) ve reason'da görünür.
func runtimeService(service, _ string) string {
	return service
}

func runtimeProblemID(check, service, pod string) string {
	id := "runtime:" + check + ":" + service
	if pod != "" {
		id += ":" + pod
	}
	return id
}

// evaluateRuntimePods — evaluateAll geçişi. Probe'lar JVM metriği hiç
// akmayan kurulumda her tick 1 ucuz count sorgusuyla çıkar.
func (e *Evaluator) evaluateRuntimePods(ctx context.Context) {
	if present, err := e.store.MetricExists(ctx, "jvm.memory.limit"); err == nil && present {
		if samples, err := e.store.JVMHeapPodUsage(ctx); err != nil {
			log.Printf("[evaluator] runtime jvm-heap read: %v", err)
		} else {
			for _, s := range samples {
				e.reconcileRuntimeHeap(ctx, s)
			}
		}
	}
	if present, err := e.store.MetricExists(ctx, "jvm.gc.duration"); err == nil && present {
		if samples, err := e.store.JVMGCPodPause(ctx); err != nil {
			log.Printf("[evaluator] runtime jvm-gc read: %v", err)
		} else {
			for _, s := range samples {
				e.reconcileRuntimeGC(ctx, s)
			}
		}
		// v0.9.440 — GC zaman payı (uzun + sık GC'yi tek ölçüde yakalar).
		if acts, err := e.store.JVMGCActivity(ctx); err != nil {
			log.Printf("[evaluator] runtime jvm-gc-share read: %v", err)
		} else {
			for _, a := range acts {
				e.reconcileRuntimeGCShare(ctx, a)
			}
		}
	}
}

func (e *Evaluator) reconcileRuntimeHeap(ctx context.Context, s chstore.CapacitySample) {
	const ruleID = "runtime:jvm-heap"
	service := runtimeService(s.Instance, s.Subkey)
	// v0.9.401 — dedup (ruleID, service) yerine deterministik problemID:
	// service artık pod taşımadığı için eski anahtar per-pod granülerliği
	// kaybederdi (iki pod tek probleme çökerdi). ID formatı DEĞİŞMEDİ —
	// prod'daki açık eski satırlar aynı ID'den bulunur ve refresh
	// yolunda service alanı kendini onarır.
	existing, err := e.store.FindOpenProblemByID(ctx, runtimeProblemID("jvm-heap", s.Instance, s.Subkey))
	hasOpen := err == nil && existing != nil && existing.ID != ""
	open, sev, pct, postBased := jvmHeapDecision(s.Usage, s.PostGC, s.Limit, hasOpen)
	gb := func(b float64) float64 { return b / (1024 * 1024 * 1024) }
	// v0.9.426 — reason sinyali söyler: GC-sonrası mı, anlık mı.
	var reason string
	if postBased {
		reason = fmt.Sprintf("JVM heap (GC sonrası) %.0f%% (%.1f/%.1f GB) on %s · pod %s",
			pct, gb(s.PostGC), gb(s.Limit), service, s.Subkey)
	} else {
		reason = fmt.Sprintf("JVM heap %.0f%% (%.1f/%.1f GB, anlık — GC-sonrası metrik yok) on %s · pod %s",
			pct, gb(s.Usage), gb(s.Limit), service, s.Subkey)
	}
	thr := jvmHeapRawWarnPct
	if postBased {
		thr = jvmHeapWarnPct
		if sev == "critical" {
			thr = jvmHeapCritPct
		}
	} else if sev == "critical" {
		thr = jvmHeapRawCritPct
	}
	e.reconcileRuntime(ctx, runtimeReconcile{
		ruleID: ruleID, ruleName: "Runtime · JVM heap", metric: "runtime.jvm_heap",
		service: service, pod: s.Subkey, problemID: runtimeProblemID("jvm-heap", s.Instance, s.Subkey),
		open: open, hasOpen: hasOpen, existing: existing,
		severity: sev, value: pct, threshold: thr, reason: reason,
	})
}

func (e *Evaluator) reconcileRuntimeGC(ctx context.Context, s chstore.CapacitySample) {
	const ruleID = "runtime:jvm-gc"
	service := runtimeService(s.Instance, s.Subkey)
	// v0.9.401 — heap yoluyla aynı: dedup deterministik problemID'den.
	existing, err := e.store.FindOpenProblemByID(ctx, runtimeProblemID("jvm-gc", s.Instance, s.Subkey))
	hasOpen := err == nil && existing != nil && existing.ID != ""
	open, sev := jvmGCPauseDecision(s.Usage, hasOpen)
	reason := fmt.Sprintf("JVM GC pause avg %.0fms on %s · pod %s", s.Usage, service, s.Subkey)
	thr := jvmGCWarnMs
	if sev == "critical" {
		thr = jvmGCCritMs
	}
	e.reconcileRuntime(ctx, runtimeReconcile{
		ruleID: ruleID, ruleName: "Runtime · JVM GC pause", metric: "runtime.jvm_gc",
		service: service, pod: s.Subkey, problemID: runtimeProblemID("jvm-gc", s.Instance, s.Subkey),
		open: open, hasOpen: hasOpen, existing: existing,
		severity: sev, value: s.Usage, threshold: thr, reason: reason,
	})
}

// runtimeReconcile — reconcileCapacity'nin open/refresh/resolve üçlüsünün
// parametreli hâli (iki denetim paylaşır).
type runtimeReconcile struct {
	ruleID, ruleName, metric, service, problemID, pod string
	open, hasOpen                                bool
	existing                                     *chstore.Problem
	severity                                     string
	value, threshold                             float64
	reason                                       string
}


// reconcileRuntimeGCShare (v0.9.440) — GC zaman payı kontrolü.
// reconcileRuntimeGC'nin ikizi; reason koleksiyon hızını da söyler ki
// "sayısı yüksek" şikâyeti alarm metninde görünür olsun.
func (e *Evaluator) reconcileRuntimeGCShare(ctx context.Context, a chstore.GCActivitySample) {
	const ruleID = "runtime:jvm-gc-share"
	service := runtimeService(a.Service, a.Pod)
	existing, err := e.store.FindOpenProblemByID(ctx, runtimeProblemID("jvm-gc-share", a.Service, a.Pod))
	hasOpen := err == nil && existing != nil && existing.ID != ""
	open, sev := jvmGCShareDecision(a.SharePct, hasOpen)
	reason := fmt.Sprintf("JVM zamanının %%%.1f'i GC'de (≈%.0f koleksiyon/dk, 10dk penceresi) on %s · pod %s",
		a.SharePct, a.RatePerMin, service, a.Pod)
	thr := jvmGCShareWarnPct
	if sev == "critical" {
		thr = jvmGCShareCritPct
	}
	e.reconcileRuntime(ctx, runtimeReconcile{
		ruleID: ruleID, ruleName: "Runtime · JVM GC zaman payı", metric: "runtime.jvm_gc_share",
		service: service, pod: a.Pod, problemID: runtimeProblemID("jvm-gc-share", a.Service, a.Pod),
		open: open, hasOpen: hasOpen, existing: existing,
		severity: sev, value: a.SharePct, threshold: thr, reason: reason,
	})
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
		log.Printf("[evaluator] PROBLEM OPENED (%s): %s", r.metric, p.Description)
		if _, err := e.store.AttachProblemToIncident(ctx, p); err != nil {
			log.Printf("[evaluator] runtime incident attach: %v", err)
		}
		if e.notifier != nil {
			go e.notifier.SendProblemAlert(context.Background(), p)
		}

	case r.open && r.hasOpen:
		// Canlı değer + severity tazele (warning critical'e kötüleşebilir);
		// StartedAt korunur; yaş-bazlı eskalasyon tabanına clamp (v0.8.309).
		// v0.9.401 — service self-heal: prod'daki eski "servis·pod"
		// birleşik satırlar aynı ID'den bulunup gerçek servis adıyla
		// yeniden yazılır (ReplacingMergeTree tam-satır upsert zaten).
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
		resolvedAt := time.Now().UnixNano()
		r.existing.Status = "resolved"
		r.existing.ResolvedAt = &resolvedAt
		r.existing.Value = r.value
		if err := e.store.UpsertProblem(ctx, *r.existing); err != nil {
			log.Printf("[evaluator] runtime resolve %s/%s: %v", r.ruleID, r.service, err)
		} else {
			log.Printf("[evaluator] PROBLEM RESOLVED (%s): %s", r.metric, r.reason)
		}
	}
}
