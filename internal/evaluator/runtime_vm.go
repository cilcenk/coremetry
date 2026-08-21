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
)

// runtimeVMGroupBy — seri kimliği: servis + pod coalesce zinciri.
// Sıra vmPodFromTuple'ın okuduğu konumlarla ÇAKILI.
var runtimeVMGroupBy = []string{"service.name", "k8s.pod.name", "host.name", "service.instance.id"}

// vmPodFromTuple — chstore.runtimePodExpr'in (k8s.pod.name → host.name →
// service.instance.id[:8]) istemci-taraflı ikizi. SAF.
func vmPodFromTuple(groupKey []string) string {
	get := func(i int) string {
		if i < len(groupKey) {
			return groupKey[i]
		}
		return ""
	}
	if p := get(1); p != "" {
		return p
	}
	if h := get(2); h != "" {
		return h
	}
	if id := get(3); id != "" {
		r := []rune(id)
		if len(r) > 8 {
			return string(r[:8])
		}
		return id
	}
	return ""
}

// vmGCDerive — iki pencere-toplamından üç metrik. SAF.
func vmGCDerive(sumSec, cnt, windowSec float64) (pauseMs, sharePct, ratePerMin float64) {
	if windowSec <= 0 || cnt <= 0 || sumSec < 0 {
		return 0, 0, 0
	}
	pauseMs = sumSec / cnt * 1000
	sharePct = sumSec / windowSec * 100
	ratePerMin = cnt / (windowSec / 60)
	return pauseMs, sharePct, ratePerMin
}

// seriesWindowTotal — increase serisinin pencere toplamı: nokta
// değerlerinin toplamı (step=pencere iken tek nokta; VM adım kaydırırsa
// birden çok nokta gelebilir, toplamak iki durumda da doğru).
func seriesWindowTotal(s chstore.SpanMetricSeries) float64 {
	t := 0.0
	for _, p := range s.Points {
		t += p.Value
	}
	return t
}

// vmGCSamples — iki VM sorgusu → reconcile makinelerinin beklediği
// örnek şekilleri. Hata = VM erişilemez: tik atlanır, sessiz boş YOK
// (boş kabul etmek tüm açık GC problemlerini yanlışlıkla çözerdi).
func (e *Evaluator) vmGCSamples(ctx context.Context) ([]chstore.CapacitySample, []chstore.GCActivitySample, error) {
	now := time.Now()
	win := chstore.RuntimePodWindow
	base := chstore.MetricQueryFilter{
		GroupBy:       runtimeVMGroupBy,
		Aggregation:   "increase",
		From:          now.Add(-win),
		To:            now,
		StepSeconds:   int(win.Seconds()),
		MaxDataPoints: 2,
	}
	sumF := base
	sumF.Name = "jvm_gc_duration_seconds_sum"
	cntF := base
	cntF.Name = "jvm_gc_duration_seconds_count"

	sums, err := e.vmetrics.QueryMetric(ctx, sumF)
	if err != nil {
		return nil, nil, fmt.Errorf("vm gc sum: %w", err)
	}
	cnts, err := e.vmetrics.QueryMetric(ctx, cntF)
	if err != nil {
		return nil, nil, fmt.Errorf("vm gc count: %w", err)
	}
	return joinVMGCSeries(sums, cnts, win.Seconds())
}

// joinVMGCSeries — SAF birleşim: (servis, pod) anahtarında sum×cnt.
// Karşılığı olmayan seri atlanır (yarım veri sample üretmez).
func joinVMGCSeries(sums, cnts []chstore.SpanMetricSeries, windowSec float64) ([]chstore.CapacitySample, []chstore.GCActivitySample, error) {
	type key struct{ svc, pod string }
	cntBy := map[key]float64{}
	for _, s := range cnts {
		svc := ""
		if len(s.GroupKey) > 0 {
			svc = s.GroupKey[0]
		}
		pod := vmPodFromTuple(s.GroupKey)
		if svc == "" || pod == "" {
			continue
		}
		cntBy[key{svc, pod}] += seriesWindowTotal(s)
	}
	var pauses []chstore.CapacitySample
	var acts []chstore.GCActivitySample
	for _, s := range sums {
		svc := ""
		if len(s.GroupKey) > 0 {
			svc = s.GroupKey[0]
		}
		pod := vmPodFromTuple(s.GroupKey)
		if svc == "" || pod == "" {
			continue
		}
		cnt := cntBy[key{svc, pod}]
		pauseMs, sharePct, ratePerMin := vmGCDerive(seriesWindowTotal(s), cnt, windowSec)
		if cnt <= 0 {
			continue
		}
		pauses = append(pauses, chstore.CapacitySample{Instance: svc, Subkey: pod, Usage: pauseMs})
		acts = append(acts, chstore.GCActivitySample{Service: svc, Pod: pod, SharePct: sharePct, RatePerMin: ratePerMin})
	}
	return pauses, acts, nil
}

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
