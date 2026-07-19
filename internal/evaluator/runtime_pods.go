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
	jvmGCWarnMs    = 500.0
	jvmGCCritMs    = 1000.0
	jvmGCHystMs    = 50.0
)

// jvmHeapDecision — saf eşik çekirdeği (tablo testli). capacityDecision'la
// aynı şekil ama runtime eşikleri bağımsız evrilebilsin diye ayrı.
func jvmHeapDecision(usage, limit float64, wasOpen bool) (open bool, severity string, pct float64) {
	if limit <= 0 {
		return false, "", 0
	}
	pct = usage / limit * 100
	switch {
	case pct >= jvmHeapCritPct:
		return true, "critical", pct
	case pct >= jvmHeapWarnPct:
		return true, "warning", pct
	case wasOpen && pct >= jvmHeapWarnPct-jvmHeapHystPct:
		return true, "warning", pct
	default:
		return false, "", pct
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

// runtimeService — Problem.service kolonu: servis·pod (capacityService
// emsali). Pod anahtarı boşsa (attr'sız kurulum) servis düzeyinde tek
// Problem.
func runtimeService(service, pod string) string {
	if pod != "" {
		return service + "·" + pod
	}
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
	}
}

func (e *Evaluator) reconcileRuntimeHeap(ctx context.Context, s chstore.CapacitySample) {
	const ruleID = "runtime:jvm-heap"
	service := runtimeService(s.Instance, s.Subkey)
	existing, err := e.store.FindOpenProblem(ctx, ruleID, service)
	hasOpen := err == nil && existing != nil && existing.ID != ""
	open, sev, pct := jvmHeapDecision(s.Usage, s.Limit, hasOpen)
	gb := func(b float64) float64 { return b / (1024 * 1024 * 1024) }
	reason := fmt.Sprintf("JVM heap %.0f%% (%.1f/%.1f GB) on %s",
		pct, gb(s.Usage), gb(s.Limit), service)
	thr := jvmHeapWarnPct
	if sev == "critical" {
		thr = jvmHeapCritPct
	}
	e.reconcileRuntime(ctx, runtimeReconcile{
		ruleID: ruleID, ruleName: "Runtime · JVM heap", metric: "runtime.jvm_heap",
		service: service, problemID: runtimeProblemID("jvm-heap", s.Instance, s.Subkey),
		open: open, hasOpen: hasOpen, existing: existing,
		severity: sev, value: pct, threshold: thr, reason: reason,
	})
}

func (e *Evaluator) reconcileRuntimeGC(ctx context.Context, s chstore.CapacitySample) {
	const ruleID = "runtime:jvm-gc"
	service := runtimeService(s.Instance, s.Subkey)
	existing, err := e.store.FindOpenProblem(ctx, ruleID, service)
	hasOpen := err == nil && existing != nil && existing.ID != ""
	open, sev := jvmGCPauseDecision(s.Usage, hasOpen)
	reason := fmt.Sprintf("JVM GC pause avg %.0fms on %s", s.Usage, service)
	thr := jvmGCWarnMs
	if sev == "critical" {
		thr = jvmGCCritMs
	}
	e.reconcileRuntime(ctx, runtimeReconcile{
		ruleID: ruleID, ruleName: "Runtime · JVM GC pause", metric: "runtime.jvm_gc",
		service: service, problemID: runtimeProblemID("jvm-gc", s.Instance, s.Subkey),
		open: open, hasOpen: hasOpen, existing: existing,
		severity: sev, value: s.Usage, threshold: thr, reason: reason,
	})
}

// runtimeReconcile — reconcileCapacity'nin open/refresh/resolve üçlüsünün
// parametreli hâli (iki denetim paylaşır).
type runtimeReconcile struct {
	ruleID, ruleName, metric, service, problemID string
	open, hasOpen                                bool
	existing                                     *chstore.Problem
	severity                                     string
	value, threshold                             float64
	reason                                       string
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
		r.existing.Value = r.value
		r.existing.Severity = effectiveSeverity(r.severity, time.Since(time.Unix(0, r.existing.StartedAt)))
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
