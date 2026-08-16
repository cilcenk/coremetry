package evaluator

import (
	"context"
	"log"
	"time"

	"github.com/cilcenk/coremetry/internal/chstore"
)

// runtime_pods.go — JVM pod-level runtime alerting (v0.9.90, operatör
// talebi: "sorunlu pod olursa haber versin / problem tetiklesin").
//
// v0.9.1075 İTİBARIYLA TÜM DEĞERLENDİRME EMEKLİ: heap v0.9.551'de
// (false pozitif), GC çifti v0.9.1075'te (operatör kararı 2026-08-16,
// VictoriaMetrics geçişine dek). Dosyada yaşayan şey saf karar
// çekirdekleri (tablo testli, dönüş için) + emekli kuralların açık
// problemlerini kapatan tahliye geçişi. Aşağıdaki tarihçe yorumları
// bilinçli olarak yerinde — eşik değerlerinin NEDEN bu olduğu, kurallar
// geri geldiğinde yeniden gerekecek.
//
// Overview'daki Runtime paneli (v0.9.87-89) heap/GC'yi pod bazında
// GÖSTERİR; bu geçiş aynı sinyalleri PAGEABLE yapar. db_capacity.go
// deseninin birebiri: leader-locked evaluateAll tick'i, MetricExists
// probe'u (JVM metriği akmayan kurulum hiç Problem görmez — lokal dahil),
// FindOpenProblem/UpsertProblem dedup, notify fan-out, incident-attach.
//
// Denetimler (eşikler sabit — capacity emsali; Settings'e taşıma ayrı iş):
//   - jvm-heap: 10-dk ort. toplam heap / -Xmx. Warn ≥85%, crit ≥90%,
//     hysteresis 2pp (v0.8.320 emsali — sınıra park etmiş gauge churn'u).
//   - jvm-gc: 10-dk ort. GC pause. Warn ≥500ms, crit ≥1000ms, hysteresis
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

// jvmGCPauseDecision — ŞU AN ÇAĞRILMIYOR (v0.9.1075: GC alarmları
// operatör kararıyla askıda, VictoriaMetrics geçişine dek). Saf eşik
// çekirdeği tablo testiyle yerinde — dönüşte kaynak değişir, bu
// semantik değişmez. avgMs = pencere ortalaması GC pause.
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

// jvmGCShareDecision — ŞU AN ÇAĞRILMIYOR (v0.9.1075, jvmGCPauseDecision
// ile aynı karar). Saf eşik çekirdeği: GC zaman payı yüzdesi üzerinden
// warn/crit + histerezis.
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

// evaluateRuntimePods — evaluateAll geçişi. v0.9.1075 itibarıyla TÜM
// JVM runtime kuralları emekli (heap v0.9.551, GC çifti v0.9.1075) —
// geriye yalnız eski açık problemlerin tahliyesi kaldı. Snapshot tik
// başında zaten okunuyor (v0.9.522 maliyet dersi); tahliye açık emekli
// satır kalmayınca hiçbir iş yapmaz.
//
// Hata halinde tik ATLANIR, boş kabul EDİLMEZ: boş kabul etmek "açık
// problem yok" demek olurdu ve tahliye hiçbir şey kapatamazdı.
func (e *Evaluator) evaluateRuntimePods(ctx context.Context) {
	snap, err := e.store.OpenProblemsSnapshot(ctx)
	if err != nil {
		log.Printf("[evaluator] runtime pods: açık problem anlık görüntüsü alınamadı, tik atlanıyor: %v", err)
		return
	}
	e.drainRetiredRuntimeProblems(ctx, snap)
}

// drainRetiredRuntimeProblems — emekliye ayrılan runtime kurallarının
// açık bıraktığı problemleri kapatır. v0.9.551'de jvm-heap için
// kuruldu; v0.9.1075'te GC çiftini de kapsayacak şekilde genelleşti.
//
// Neden gerekli: bir kuralın değerlendirmesini silmek, o kuralın
// KAPATICI kolunu da siler. Açık satırlar Problems'ta sonsuza dek
// kalır ve hiçbir şey onları temizlemez — false pozitiflerden
// kurtulmak için onları kalıcılaştırmak, şikâyetin daha kötü hâli
// olurdu.
//
// Maliyet sıfır: tik başında zaten okunan snapshot üzerinden döner,
// yeni CH sorgusu yok. Açık emekli problem kalmayınca hiçbir iş
// yapmaz, yani kendini tüketen bir geçiş adımıdır.
func (e *Evaluator) drainRetiredRuntimeProblems(ctx context.Context, snap *chstore.OpenProblems) {
	now := time.Now().UnixNano()
	for _, p := range snap.All() {
		if !shouldDrainRetiredRuntimeProblem(p) {
			continue
		}
		resolved := *p
		chstore.MarkResolved(&resolved, now)
		// Açıklamaya SEBEBİ yazılır. Aksi hâlde operatör problemin
		// kendiliğinden düzeldiğini sanardı — oysa kural kaldırıldı,
		// sinyal iyileşmedi. İkisi farklı şeyler ve karıştırılmaları
		// gelecekteki bir incelemeyi yanlış yola sokar.
		resolved.Description = p.Description + " · (" + retiredRuntimeRules[p.RuleID] + ")"
		if err := e.store.UpsertProblem(ctx, resolved); err != nil {
			log.Printf("[evaluator] emekli runtime problemi kapatılamadı %s: %v", p.ID, err)
			continue
		}
		e.countResolved()
		log.Printf("[evaluator] PROBLEM RESOLVED (%s, kural kaldırıldı): %s", p.Metric, p.ID)
	}
}

// retiredRuntimeRules — emekli runtime kuralları → tahliye notu.
//   - jvm-heap  (v0.9.551): false pozitif oranı; sinyal heap doluluğu
//     değil GC baskısıydı.
//   - jvm-gc + jvm-gc-share (v0.9.1075, operatör kararı 2026-08-16):
//     "JVM GC alertlerini şimdilik kaldıralım; VictoriaMetrics geçişi
//     planım var, o zaman düşünürüz." Saf karar çekirdekleri
//     (jvmGCPauseDecision / jvmGCShareDecision) tablo testleriyle
//     yerinde duruyor — VM dönüşünde kaynak değişir, eşik semantiği
//     değişmez.
var retiredRuntimeRules = map[string]string{
	"runtime:jvm-heap":     "kural v0.9.551'de kaldırıldı — false pozitif oranı; GC baskısı kuralları o sürümde yerindeydi",
	"runtime:jvm-gc":       "kural v0.9.1075'te kaldırıldı — operatör kararı; VictoriaMetrics geçişinde yeniden değerlendirilecek",
	"runtime:jvm-gc-share": "kural v0.9.1075'te kaldırıldı — operatör kararı; VictoriaMetrics geçişinde yeniden değerlendirilecek",
}

// shouldDrainRetiredRuntimeProblem — saf karar: bu satır, emekli bir
// runtime kuralının açık bıraktığı bir problem mi?
//
// Ayrı ve saf olmasının sebebi test edilebilirlik: evaluator'ın store
// alanı somut *chstore.Store, yani sahte store ile tahliye döngüsü
// uçtan uca koşturulamıyor. Karar burada izole edilince tablo testi
// gerçek davranışı sabitler.
//
// Üç eleme de gerekli ve üçü de farklı bir hatayı engelliyor:
//   - nil: snapshot map'i nil değer taşıyabilir (savunmacı).
//   - RuleID: emekli OLMAYAN kuralların problemlerini kapatmak sessiz
//     veri kaybı olurdu — tahliye yalnız emekli seti temizler.
//   - Status: zaten kapalı bir satırı yeniden kapatmak her tikte
//     ResolvedAt'i ileri iter ve problemin gerçek süresini bozar.
func shouldDrainRetiredRuntimeProblem(p *chstore.Problem) bool {
	if p == nil || p.Status != "open" {
		return false
	}
	_, retired := retiredRuntimeRules[p.RuleID]
	return retired
}
