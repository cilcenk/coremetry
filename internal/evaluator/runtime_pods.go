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
	jvmHeapHystPct = 2.0 // eşikler v0.9.485'te RuntimeAlertConfig'e taşındı
	// v0.9.426 (operator-reported, prod: "JVM hatası olmayan podlara
	// alert") — anlık used/max testere-dişlidir: sağlıklı JVM, GC
	// tetiklenmeden önce %85-95'e dokunur ve 10dk ortalaması bile
	// yüksek kalabilir. GC-SONRASI sinyal (used_after_last_gc) varsa
	// 85/90 eşikleri anlamlıdır ve o kullanılır; yoksa anlık sinyalin
	// eşiği 92/97'ye çıkar — gerçek doluluk baskısı ancak o bölgede
	// GC'ye rağmen inmeyen heap demektir.
	// v0.9.485 — GC pause eşikleri chstore.RuntimeAlertConfig'e taşındı
	// (operatör: "2-3 saniye pause olursa sorun gerçekten vardır";
	// 500/1000ms filonun yarısını alarma boğuyordu). Histerezis warn'ın
	// %10'u olarak türetilir (sabit 50ms, 2000ms eşiğinde anlamsız kalırdı).
	// v0.9.440 (operatör istegi: "çok uzun GC + GC sayısı yüksek podlar")
	// — GC ZAMAN PAYI: pencerede GC'de geçen sürenin yüzdesi. Tek ölçüde
	// iki şikâyet: uzun pause'lar da sık kısa pause'lar da payı şişirir.
	// >%10 = throughput acısı (soruşturulmalı), >%25 = ciddi; 2pp
	// histerezis (jvm-heap ile aynı sınıf).
	jvmGCShareHystPct = 2.0
)

// jvmHeapDecision — ŞU AN ÇAĞRILMIYOR (v0.9.551: heap alarmı false
// pozitif oranı yüzünden kaldırıldı; çağıran reconcileRuntimeHeap
// silindi). Fonksiyon ve tablo testi bilerek KALDI: operatör
// "daha sonra spesifik kurallarla geliriz" dedi ve buradaki
// iki-sinyalli eşik semantiği (post-GC varsa 85/90, yoksa 92/97)
// o dönüşte yeniden türetilmesi gereken en pahalı bilgi. Testler
// onu canlı tutar; ölü kod değil, korunmuş sözleşme.
//
// jvmHeapDecision — saf eşik çekirdeği (tablo testli). capacityDecision'la
// aynı şekil ama runtime eşikleri bağımsız evrilebilsin diye ayrı.
// postGC > 0 → GC-sonrası sinyal + 85/90; postGC yok → anlık used + 92/97.
// postBased dönüşü reason/threshold metnini dürüst kılar.
func jvmHeapDecision(usage, postGC, limit float64, wasOpen bool, cfg chstore.RuntimeAlertConfig) (open bool, severity string, pct float64, postBased bool) {
	if limit <= 0 {
		return false, "", 0, false
	}
	val, warn, crit := usage, cfg.HeapRawWarnPct, cfg.HeapRawCritPct
	if postGC > 0 {
		val, warn, crit, postBased = postGC, cfg.HeapPostGCWarnPct, cfg.HeapPostGCCritPct, true
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
func jvmGCPauseDecision(avgMs float64, wasOpen bool, cfg chstore.RuntimeAlertConfig) (open bool, severity string) {
	hyst := cfg.GCPauseWarnMs * 0.1
	switch {
	case avgMs >= cfg.GCPauseCritMs:
		return true, "critical"
	case avgMs >= cfg.GCPauseWarnMs:
		return true, "warning"
	case wasOpen && avgMs >= cfg.GCPauseWarnMs-hyst:
		return true, "warning"
	default:
		return false, ""
	}
}

// jvmGCShareDecision — saf eşik çekirdeği (tablo testli): GC zaman
// payı yüzdesi üzerinden warn/crit + histerezis.
func jvmGCShareDecision(sharePct float64, wasOpen bool, cfg chstore.RuntimeAlertConfig) (open bool, severity string) {
	switch {
	case sharePct >= cfg.GCShareCritPct:
		return true, "critical"
	case sharePct >= cfg.GCShareWarnPct:
		return true, "warning"
	case wasOpen && sharePct >= cfg.GCShareWarnPct-jvmGCShareHystPct:
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
	// v0.9.485 — eşikler tick başında BİR kez okunur (soft-fail →
	// varsayılanlar); operatör PUT'u bir sonraki tick'te devrede.
	cfg := e.store.GetRuntimeAlerts(ctx)
	// v0.9.522 — açık problemler tik başına BİR kez okunur.
	//
	// Öncesi: her pod × her metrik için ayrı FindOpenProblemByID. Prod
	// ölçümü (2026-08-02, self-telemetri havuz etiketiyle): bu tek sorgu
	// ŞEKLİ saatte ~10.000 çağrı, Coremetry'nin TÜM queryrow trafiğinin
	// ~%77'si. `problems` bir STATE tablosu, yani in-order ana bağlantıda
	// kalmak ZORUNDA (v0.9.486 /users tutarsızlığı) — dolayısıyla hepsi
	// ilk CH node'una gidiyor ve o node'un CPU'sunu şişiriyordu.
	//
	// Okuma havuzu bunu ASLA düzeltemezdi: sorgu doğru havuzda. Düzeltme
	// taşımak değil, çağrı sayısını düşürmek. v0.8.352'de evaluator'ın
	// measure() çağrılarında uygulanan desenin aynısı (105k→600).
	//
	// Hata halinde tik ATLANIR, boş kabul EDİLMEZ: boş kabul etmek "açık
	// problem yok" demek olurdu ve evaluator zaten açık olan problemleri
	// yeniden açardı (kopya + flapping).
	snap, err := e.store.OpenProblemsSnapshot(ctx)
	if err != nil {
		log.Printf("[evaluator] runtime pods: açık problem anlık görüntüsü alınamadı, tik atlanıyor: %v", err)
		return
	}
	// v0.9.551 — JVM heap alarmı KALDIRILDI (operatör: "çok fazla false
	// pozitif üretiyor, daha sonra spesifik kurallarla geliriz").
	//
	// Eşik ayarı yerine kaldırma seçildi çünkü sorun eşikte değil
	// sinyalde: heap doluluğu JVM'de tek başına bir arıza göstergesi
	// değildir — sağlıklı bir uygulama da GC'den hemen önce %95'e
	// çıkar. v0.9.426'nın iki-sinyalli (post-GC + ham) yaklaşımı bunu
	// azalttı ama bitirmedi. Gerçek sinyal GC BASKISI, o da zaten
	// ayrı iki kuralda ölçülüyor (jvm-gc duraklaması + jvm-gc-share);
	// ikisi de yerinde duruyor.
	//
	// Açık heap problemleri TAHLİYE EDİLİR (aşağıda). Sadece
	// değerlendirmeyi silmek, açık satırların kapatıcısını da
	// sildiği için onları sonsuza dek Problems'ta bırakırdı —
	// false pozitiflerden kurtulmak için onları KALICI hâle
	// getirmek, çözmeye çalıştığımız şikâyetin daha kötüsü olurdu.
	e.drainRetiredHeapProblems(ctx, snap)
	if present, err := e.store.MetricExists(ctx, "jvm.gc.duration"); err == nil && present {
		// v0.9.1053 — canlı semantik çağıranda: [now-RuntimePodWindow, now].
		gcNow := time.Now()
		if samples, err := e.store.JVMGCPodPause(ctx, gcNow.Add(-chstore.RuntimePodWindow), gcNow); err != nil {
			log.Printf("[evaluator] runtime jvm-gc read: %v", err)
		} else {
			for _, s := range samples {
				e.reconcileRuntimeGC(ctx, s, cfg, snap)
			}
		}
		// v0.9.440 — GC zaman payı (uzun + sık GC'yi tek ölçüde yakalar).
		if acts, err := e.store.JVMGCActivity(ctx); err != nil {
			log.Printf("[evaluator] runtime jvm-gc-share read: %v", err)
		} else {
			for _, a := range acts {
				e.reconcileRuntimeGCShare(ctx, a, cfg, snap)
			}
		}
	}
}

func (e *Evaluator) reconcileRuntimeGC(ctx context.Context, s chstore.CapacitySample, cfg chstore.RuntimeAlertConfig, snap *chstore.OpenProblems) {
	const ruleID = "runtime:jvm-gc"
	service := runtimeService(s.Instance, s.Subkey)
	// v0.9.401 — heap yoluyla aynı: dedup deterministik problemID'den.
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
		e.countOpened() // v0.9.550 — kalp atışı sayacı
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
		// v0.9.977 — ihlal değeri (GC payı / heap) korunur.
		chstore.MarkResolved(r.existing, time.Now().UnixNano())
		if err := e.store.UpsertProblem(ctx, *r.existing); err != nil {
			log.Printf("[evaluator] runtime resolve %s/%s: %v", r.ruleID, r.service, err)
		} else {
			e.countResolved() // v0.9.550 — kalp atışı sayacı
			log.Printf("[evaluator] PROBLEM RESOLVED (%s): %s", r.metric, r.reason)
		}
	}
}

// drainRetiredHeapProblems — v0.9.551. Emekliye ayrılan
// runtime:jvm-heap kuralının açık bıraktığı problemleri kapatır.
//
// Neden gerekli: bir kuralın değerlendirmesini silmek, o kuralın
// KAPATICI kolunu da siler. Açık satırlar Problems'ta sonsuza dek
// kalır ve hiçbir şey onları temizlemez — false pozitiflerden
// kurtulmak için onları kalıcılaştırmak, şikâyetin daha kötü hâli
// olurdu.
//
// Maliyet sıfır: tik başında zaten okunan snapshot üzerinden döner,
// yeni CH sorgusu yok. Açık heap problemi kalmayınca hiçbir iş
// yapmaz, yani kendini tüketen bir geçiş adımıdır. İleride "spesifik
// kurallarla" dönüldüğünde bu fonksiyon silinebilir.
func (e *Evaluator) drainRetiredHeapProblems(ctx context.Context, snap *chstore.OpenProblems) {
	now := time.Now().UnixNano()
	for _, p := range snap.All() {
		if !shouldDrainHeapProblem(p) {
			continue
		}
		resolved := *p
		chstore.MarkResolved(&resolved, now)
		// Açıklamaya SEBEBİ yazılır. Aksi hâlde operatör problemin
		// kendiliğinden düzeldiğini sanardı — oysa kural kaldırıldı,
		// heap iyileşmedi. İkisi farklı şeyler ve karıştırılmaları
		// gelecekteki bir incelemeyi yanlış yola sokar.
		resolved.Description = p.Description + " · (kural v0.9.551'de kaldırıldı — false pozitif oranı; GC baskısı kuralları yerinde)"
		if err := e.store.UpsertProblem(ctx, resolved); err != nil {
			log.Printf("[evaluator] emekli jvm-heap problemi kapatılamadı %s: %v", p.ID, err)
			continue
		}
		e.countResolved()
		log.Printf("[evaluator] PROBLEM RESOLVED (runtime.jvm_heap, kural kaldırıldı): %s", p.ID)
	}
}

// retiredHeapRuleID — v0.9.551'de emekliye ayrılan kuralın kimliği.
// runtimeProblemID("jvm-heap", ...) bu önekle ID üretiyordu.
const retiredHeapRuleID = "runtime:jvm-heap"

// shouldDrainHeapProblem — saf karar: bu satır, kaldırılan heap
// kuralının açık bıraktığı bir problem mi?
//
// Ayrı ve saf olmasının sebebi test edilebilirlik: evaluator'ın store
// alanı somut *chstore.Store, yani sahte store ile tahliye döngüsü
// uçtan uca koşturulamıyor. Karar burada izole edilince tablo testi
// gerçek davranışı sabitler.
//
// Üç eleme de gerekli ve üçü de farklı bir hatayı engelliyor:
//   - nil: snapshot map'i nil değer taşıyabilir (savunmacı).
//   - RuleID: BAŞKA kuralların problemlerini kapatmak sessiz veri
//     kaybı olurdu — tahliye yalnız kendi kuralını temizler.
//   - Status: zaten kapalı bir satırı yeniden kapatmak her tikte
//     ResolvedAt'i ileri iter ve problemin gerçek süresini bozar.
func shouldDrainHeapProblem(p *chstore.Problem) bool {
	return p != nil && p.RuleID == retiredHeapRuleID && p.Status == "open"
}
