// selfhealth.go — v0.9.1279: sentetik self-health alarm ailesi
// (Dynatrace-parite top-10 #8 / EK A §A1).
//
// KAPATTIĞI SINIF: "izleme sistemi kendisi öldü ve kimse bilmedi".
//
// KANIT (2026-08-20 prod olayı): metric_points spool'u 3 milyon dosya /
// 965 GiB'a çıktı. Teşhis ürüne v0.9.985'te girmişti — ama PANEL olarak.
// O sürümün kendi başlık yorumu sınıfı birebir yazıyor: "3.5 saatlik ölü
// ingest tüm ekranlarda yeşil görünüyordu". Panel, kimse bakmıyorsa
// alarm değildir; bu dosya aynı ölçümleri PROBLEM'e çeviriyor.
//
// TASARIM: yeni bir yaşam döngüsü İCAT EDİLMEDİ. db_capacity.go /
// fatal_exception.go / shared_exception.go ile birebir aynı hat:
// leader-locked evaluateAll tik'i → deterministik problem ID → tik
// başında bir kez okunan OpenProblems snapshot'ı ile dedup →
// UpsertProblem (ReplacingMergeTree aynı satıra çöker) → açılışta
// notify fan-out → koşul geçince auto-resolve.
//
// AİLENİN EN BÜYÜK RİSKİ YANLIŞ POZİTİF, çünkü bu alarmlar operatörün
// "sisteme güvenebilir miyim" sorusunun cevabı. Dört kural da bu yüzden
// bir kanıt kapısı taşıyor:
//   - ingest-stall: "ÖNCE VARDI" şartı — taze kurulumda hiç açılmaz.
//   - spool-depth : tek-düğümde kavram yok → hiç okunmaz; ölçülemedi ≠
//     temiz (v0.9.984 fail-open dersi) → o tik atlanır, hüküm verilmez.
//   - disk-eta    : büyümüyorsa/küçülüyorsa ETA YOKTUR; zayıf uyum
//     (R²) tahmin üretmez; seri boot'ta boştur, ilk yarım saat sessiz.
//   - channel     : kanal HÂLÂ YAPILANDIRILMIŞ ve AÇIK olmalı — silinmiş
//     bir test kanalının kuyruk hatası sonsuza dek alarm çalamaz.
//
// (d) HACİM SIÇRAMASI BİLEREK DIŞARIDA: parite raporunun kendi efor notu
// "ilk üç kontrol" diyor ve sıçrama tespiti baseline/mevsimsellik
// gerektiriyor — davranış motorunun (internal/anomaly) işi, eşik
// alarmının değil.
package evaluator

import (
	"context"
	"fmt"
	"log"
	"sort"
	"time"

	"github.com/cilcenk/coremetry/internal/chstore"
)

// ── Kimlikler ───────────────────────────────────────────────────────────
//
// rule_id'ler `self-` önekli ve sabit: /problems'ta aile tek filtreyle
// süzülebilsin, ve EnrichProblemsWithRunbooks bu id'lerden runbook
// çözebilsin (selfHealthRunbooks, chstore/problem.go).
const (
	selfIngestRuleID  = "self-ingest-stall"
	selfSpoolRuleID   = "self-spool-depth"
	selfDiskRuleID    = "self-disk-eta"
	selfChannelRuleID = "self-channel-broken"
)

// selfHealthRules — tahliye (aile kapatıldığında / kural artık üretmiyorken)
// hangi rule_id'lerin bu dosyaya ait olduğunu bilen tek liste.
var selfHealthRules = map[string]bool{
	selfIngestRuleID:  true,
	selfSpoolRuleID:   true,
	selfDiskRuleID:    true,
	selfChannelRuleID: true,
	selfVolumeRuleID:  true, // v0.9.1294 — selfhealth_volume.go
}

// ── Disk ETA serisi ─────────────────────────────────────────────────────

const (
	// selfDiskMinPoints / selfDiskMinSpanS — projeksiyonun ALT SINIRI.
	// İki nokta bir doğru çizer ama bir eğilim göstermez; yarım saat,
	// tek bir merge/part-drop dalgalanmasının slope'u ele geçirmesini
	// engelleyecek kadar uzun.
	selfDiskMinPoints = 4
	selfDiskMinSpanS  = 30 * 60
	// selfDiskMinR2 — zayıf uyumda tahmin ÜRETİLMEZ (capacityETA ile
	// aynı bar). Disk doluluğu basamaklı bir seridir; düz-sonra-sıçrama
	// şekli düşük R² verir ve orada "6 gün sonra dolacak" demek uydurma
	// olur.
	selfDiskMinR2 = 0.6
	// selfDiskSeriesMax — düğüm başına saklanan örnek tavanı. 1 dk'lık
	// tik'te 360 örnek = 6 saatlik pencere: retention silmesi gibi bir
	// düşüşü unutacak kadar kısa, gündüz/gece dalgasını yutacak kadar
	// uzun.
	selfDiskSeriesMax = 360
)

// diskSample — bir volume'ün tek ölçümü. Seri BELLEKTE ve YALNIZ
// liderde yaşar; lider değişiminde sıfırlanır ve yarım saat boyunca
// tahmin üretilmez. Bu bir eksiklik değil, tercih: disk doluluk
// geçmişini kalıcılaştırmak yeni bir tablo + yeni bir yazma yolu
// demekti, oysa soru "önümüzdeki günlerde dolar mı" — altı saatlik
// bellek-içi pencere onu cevaplar.
type diskSample struct {
	tSec int64
	used float64
}

// ── Saf karar çekirdekleri (tablo testli — selfhealth_test.go) ──────────

// ingestStallDecision — ingest DURDU mu?
//
// Üç dallı ve her dal ayrı bir hatayı engelliyor:
//
//  1. curActive → alarm YOK. Veri akıyorsa tartışma bitti (açık problem
//     de bu dalda kapanır).
//  2. wasOpen → AÇIK KALIR. "önce vardı" şartı AÇILIŞ kapısıdır, TUTMA
//     kapısı değil: üç saatlik bir kesintide ikinci tikten itibaren
//     "önceki pencere de boş" olur ve şart tutma kapısı olsaydı problem
//     10 dakikada kendini kapatırdı — tam da gizlemeye çalıştığımız
//     sessiz arızayı geri getirirdi.
//  3. prevActive → YENİ arıza. Taze kurulumda (hiç veri gelmemiş)
//     prevActive false'tur ve alarm HİÇ açılmaz. Bu satırı `return true`
//     yapan mutasyon, taze-kurulum testini kızartır.
func ingestStallDecision(curActive, prevActive, wasOpen bool) bool {
	if curActive {
		return false
	}
	if wasOpen {
		return true
	}
	return prevActive
}

// selfSpoolDim — spool ihlalinin HANGİ boyuttan geldiği. Problem satırının
// Value/Threshold çifti bu boyutu taşır, yoksa "132.418 / 100.000" yerine
// bayt eşiğini kıran ama dosya eşiğinin altında kalan bir arıza UI'da
// "ihlal yok" gibi görünürdü.
type selfSpoolDim string

const (
	spoolDimNone  selfSpoolDim = ""
	spoolDimFiles selfSpoolDim = "files"
	spoolDimBytes selfSpoolDim = "bytes"
)

// spoolBreachDecision — saf eşik kararı. İki boyut OR'lu: az sayıda
// devasa dosya da, çok sayıda küçük dosya da aynı arızadır (2026-08-20:
// 3M dosya / 965 GiB — ikisi de eşiğin çok üstünde).
//
// Dosya boyutu ÖNCELİKLİ çünkü asıl sinyal odur (v0.9.985 ölçümü:
// sağlıklı tablolar 0-1 dosya); bayt eşiği disk taşmasını yakalayan
// ikinci kapıdır.
func spoolBreachDecision(files, bytes, maxFiles, maxBytes uint64) selfSpoolDim {
	if maxFiles > 0 && files >= maxFiles {
		return spoolDimFiles
	}
	if maxBytes > 0 && bytes >= maxBytes {
		return spoolDimBytes
	}
	return spoolDimNone
}

// diskETADays — SAF projeksiyon: örnek serisi + kapasite → kaç gün sonra
// dolar.
//
// ok=false: tahmin YOK. Dört sebep, hepsi bilinçli:
//   - kısa seri / dar zaman aralığı (henüz eğilim yok),
//   - eğim ≤ 0 (büyümüyor ya da eriyor — "dolacak" iddiası kurulamaz),
//   - zayıf uyum (R² < eşik),
//   - kapasite ≤ 0 (okunamayan disk).
//
// ETA, regresyon doğrusunun SON noktadaki DEĞERİNDEN kalan yola bakar,
// gürültülü son örnekten değil (capacityETA ile aynı gerekçe).
// Doğrunun bugünü zaten kapasiteyi aşıyorsa cevap 0 gündür — "dolu"
// bir diskte tahmin yok demek, alarmın kaçırılması demekti.
func diskETADays(points []diskSample, capacityBytes uint64) (days float64, ok bool) {
	n := len(points)
	if n < selfDiskMinPoints || capacityBytes == 0 {
		return 0, false
	}
	if points[n-1].tSec-points[0].tSec < selfDiskMinSpanS {
		return 0, false
	}
	limit := float64(capacityBytes)

	// Doğrusal regresyon (x = ilk örneğe göre saniye — taşma önlemi).
	x0 := points[0].tSec
	var sx, sy, sxx, sxy float64
	for _, p := range points {
		x := float64(p.tSec - x0)
		sx += x
		sy += p.used
		sxx += x * x
		sxy += x * p.used
	}
	fn := float64(n)
	den := fn*sxx - sx*sx
	if den == 0 {
		return 0, false // tüm örnekler aynı anda — eğim tanımsız
	}
	slope := (fn*sxy - sx*sy) / den // bayt/saniye
	if slope <= 0 {
		return 0, false // düz ya da eriyor
	}
	intercept := (sy - slope*sx) / fn

	// R² = 1 − SSres/SStot.
	meanY := sy / fn
	var ssRes, ssTot float64
	for _, p := range points {
		fit := intercept + slope*float64(p.tSec-x0)
		ssRes += (p.used - fit) * (p.used - fit)
		ssTot += (p.used - meanY) * (p.used - meanY)
	}
	if ssTot == 0 {
		return 0, false // sabit seri — eğim iddiası kurulamaz
	}
	if r2 := 1 - ssRes/ssTot; r2 < selfDiskMinR2 {
		return 0, false
	}

	lastFit := intercept + slope*float64(points[n-1].tSec-x0)
	if lastFit >= limit {
		return 0, true // doğru zaten tavanda: sıfır gün
	}
	return (limit - lastFit) / slope / 86400, true
}

// channelBrokenDecision — bir bildirim kanalı ÖLÜ mü?
//
// configured/enabled kapıları ŞART. notification_log geçmiştir; silinmiş
// ya da kapatılmış bir kanalın son üç hatası sonsuza dek orada durur ve
// bu kapılar olmadan asla kapanmayan bir problem üretirdi (lokal
// kurulumda dünkü v0.9.1278 testinden kalan iki kanal tam olarak bu
// şekildeydi). Kanalı silmek/kapatmak da geçerli bir düzeltmedir.
func channelBrokenDecision(consecFails, threshold int, configured, enabled bool) bool {
	if !configured || !enabled || threshold <= 0 {
		return false
	}
	return consecFails >= threshold
}

// ── Tik ─────────────────────────────────────────────────────────────────

// selfProblem — bu tikte AÇIK OLMASI GEREKEN bir problemin tarifi.
// Reconcile döngüsü bu listeyi snapshot'la kıyaslar; dört kural da aynı
// açma/tazeleme/kapatma yolunu paylaşsın diye ara tip.
type selfProblem struct {
	id       string
	ruleID   string
	ruleName string
	metric   string
	// service — v0.9.1294. Dört kural için BOŞ (özne Coremetry'nin
	// kendisi; sahte bir servis adı uydurmak v0.9.401/402'de düzeltilen
	// kırık servis bağlantısını geri getirirdi). self-volume-spike'ta
	// DOLU: orada özne, span yazan gerçek bir servis.
	service     string
	comparator  string
	description string
	severity    string
	value       float64
	threshold   float64
}

// evaluateSelfHealth — evaluateAll geçişi. snap tik başında zaten
// okundu; bu geçiş YENİ problem sorgusu yapmaz.
//
// snap == nil (snapshot hatası) → HİÇBİR ŞEY YAPILMAZ. Dedup güvencesi
// olmadan açmak her tikte yeni bir satır, kapatmak da körlemesine veri
// kaybı olurdu.
func (e *Evaluator) evaluateSelfHealth(ctx context.Context, snap *chstore.OpenProblems) {
	if snap == nil {
		return
	}
	cfg := e.store.GetSelfHealth(ctx)
	if !cfg.SelfHealthOn() {
		// Aile kapatıldı: açık self-* problemleri KAPANIR. Aksi hâlde
		// operatör vidayı kapatır ama inbox'ta ölümsüz satırlar kalırdı
		// (drainRetiredRuntimeProblems ile aynı gerekçe).
		e.reconcileSelfHealth(ctx, snap, nil, selfHealthRules)
		return
	}

	var want []selfProblem
	// covered — bu tikte GERÇEKTEN ölçülebilmiş kurallar. Yalnız bunların
	// açık problemleri kapatılabilir: ölçemediği bir kuralı "temiz" sayıp
	// kapatmak, düzeltmenin kendini iptal etmesi olurdu (v0.9.984).
	covered := map[string]bool{}

	if p, ok := e.selfIngestStall(ctx, cfg, snap); ok {
		covered[selfIngestRuleID] = true
		want = append(want, p...)
	}
	if p, ok := e.selfSpoolDepth(ctx, cfg); ok {
		covered[selfSpoolRuleID] = true
		want = append(want, p...)
	}
	if p, ok := e.selfDiskETA(ctx, cfg); ok {
		covered[selfDiskRuleID] = true
		want = append(want, p...)
	}
	if p, ok := e.selfChannelHealth(ctx, cfg); ok {
		covered[selfChannelRuleID] = true
		want = append(want, p...)
	}
	// v0.9.1294 — hacim sıçraması. Kendi ölçüm sıklığını (saatlik) içeride
	// yönetir; her tikte son ölçümü döndürdüğü için yaşam döngüsü
	// kardeşleriyle aynı hızda koşar.
	if p, ok := e.selfVolumeSpike(ctx, cfg); ok {
		covered[selfVolumeRuleID] = true
		want = append(want, p...)
	}

	e.reconcileSelfHealth(ctx, snap, want, covered)
}

// ── (1) self-ingest-stall ───────────────────────────────────────────────

func (e *Evaluator) selfIngestStall(ctx context.Context, cfg chstore.SelfHealthConfig, snap *chstore.OpenProblems) ([]selfProblem, bool) {
	window := time.Duration(cfg.IngestStallMin) * time.Minute
	w, err := e.store.ReadIngestWindows(ctx, window)
	if err != nil {
		log.Printf("[evaluator] self-health ingest penceresi okunamadı: %v", err)
		return nil, false
	}

	// Metrik tarafında pencere TOPLAMI yerine "en taze nokta" okunuyor
	// (metric_catalog'un zaten hesapladığı değer). Dönüşüm birebir:
	//   cur  = son nokta [now-w, now) içinde mi
	//   prev = son nokta [now-2w, now) içinde mi — cur false iken bu
	//          "önceki pencerede vardı" demektir.
	// LastSeen 0 (katalog boş, hiç metrik gelmemiş) her iki dalda da
	// false: yokluk, iddia değildir.
	metricsCur := w.MetricsLastSeenNs > 0 && w.NowNs-w.MetricsLastSeenNs <= window.Nanoseconds()
	metricsPrev := w.MetricsLastSeenNs > 0 && w.NowNs-w.MetricsLastSeenNs <= 2*window.Nanoseconds()

	// VE değil VEYA: "tamamen kayıp" sınıfı yalnız İKİ sinyal de sustuğunda
	// kurulur. Yalnız span'lerin durması (metrikler akarken) başka bir
	// arızadır ve bu alarmın P1 ağırlığını hak etmez; ayrıca metrik
	// göndermeyen (yalnız-trace) bir kurulumda metrik dalı hep sessizdir
	// ve VEYA olsaydı o kurulum sürekli alarm çalardı.
	curActive := w.SpansCur > 0 || metricsCur
	prevActive := w.SpansPrev > 0 || metricsPrev

	wasOpen := snap.ByID(selfIngestRuleID) != nil
	if !ingestStallDecision(curActive, prevActive, wasOpen) {
		return nil, true
	}

	// Value 0 / Threshold 1 SEÇİLMİŞ bir çift: computePriority'nin
	// "tam kayıp" dalı (v0.9.825) tam olarak bunu arıyor ve problemi
	// P1'e koyuyor. Triage kuralındaki "tamamen kayıp = P1" satırının
	// kod karşılığı burasıdır.
	return []selfProblem{{
		id:          selfIngestRuleID,
		ruleID:      selfIngestRuleID,
		ruleName:    "Coremetry · ingest durdu",
		metric:      "self.ingest_stall",
		severity:    "critical",
		value:       0,
		threshold:   1,
		description: ingestStallReason(w, cfg.IngestStallMin, metricsPrev),
	}}, true
}

// ingestStallReason — SAF gerekçe metni (tablo testli). Operatörün ilk
// sorusu "gerçekten durdu mu, yoksa hiç başlamadı mı" — cümle bu yüzden
// ÖNCEKİ pencerede ne aktığını da yazar.
func ingestStallReason(w chstore.IngestWindows, stallMin int, metricsPrev bool) string {
	prev := "önceki pencere de boş"
	switch {
	case w.SpansPrev > 0 && metricsPrev:
		prev = fmt.Sprintf("önceki %d dk: %s span + metrik akıyordu", stallMin, fmtInt(w.SpansPrev))
	case w.SpansPrev > 0:
		prev = fmt.Sprintf("önceki %d dk: %s span akıyordu", stallMin, fmtInt(w.SpansPrev))
	case metricsPrev:
		prev = fmt.Sprintf("önceki %d dk: metrik akıyordu", stallMin)
	}
	return fmt.Sprintf(
		"INGEST DURDU: son %d dakikada ClickHouse'a HİÇ span ve HİÇ metrik yazılmadı (%s). "+
			"Tüm ekranlar bu pencerede boş/bayat gösteriyor. Sırayla: OTel collector ayakta mı, "+
			"Distributed spool derinliği (/admin/stats), CH bellek/disk limitleri.",
		stallMin, prev)
}

// ── (2) self-spool-depth ────────────────────────────────────────────────

func (e *Evaluator) selfSpoolDepth(ctx context.Context, cfg chstore.SelfHealthConfig) ([]selfProblem, bool) {
	q := e.store.CollectDistributionQueue(ctx)
	if q == nil {
		// Tek düğüm: Distributed tablo yok, spool kavramı yok. Kural
		// KAPSANMIŞ sayılır (açık bir satır varsa kapansın — kurulum
		// dağıtıktan tek düğüme dönmüş olabilir).
		return nil, true
	}
	if !q.Measured {
		// "Ölçemedim" ≠ "temiz": ne açarız ne kapatırız (v0.9.984).
		return nil, false
	}
	dim := spoolBreachDecision(q.Files, q.Bytes, cfg.SpoolMaxFiles, cfg.SpoolMaxBytes)
	if dim == spoolDimNone {
		return nil, true
	}
	value, threshold := float64(q.Files), float64(cfg.SpoolMaxFiles)
	if dim == spoolDimBytes {
		value, threshold = float64(q.Bytes), float64(cfg.SpoolMaxBytes)
	}
	return []selfProblem{{
		id:       selfSpoolRuleID,
		ruleID:   selfSpoolRuleID,
		ruleName: "Coremetry · Distributed spool birikti",
		metric:   "self.spool_depth",
		// critical + oran<2 → P2 (triage hedefi). Eşiğin İKİ KATINA
		// çıkan bir spool P1'e yükselir; dört saatten uzun açık kalan
		// da öyle (stale-critical). İkisi de doğru: 2026-08-20'de
		// arıza saatlerce sürdü ve 3M dosyaya ulaştı.
		severity:    "critical",
		value:       value,
		threshold:   threshold,
		description: spoolReason(q, dim, cfg),
	}}, true
}

// spoolReason — SAF gerekçe (tablo testli). Hangi tablonun en derin
// olduğu + son istisna metne giriyor: operatörün ilk sorusu "neden
// inmiyor" ve cevap zaten ölçümün içinde.
func spoolReason(q *chstore.DistributionQueue, dim selfSpoolDim, cfg chstore.SelfHealthConfig) string {
	limit := fmt.Sprintf("eşik %s dosya", fmtInt(cfg.SpoolMaxFiles))
	if dim == spoolDimBytes {
		limit = fmt.Sprintf("eşik %s", chstore.HumanBytes(cfg.SpoolMaxBytes))
	}
	worst := ""
	var top chstore.DistributionQueueEntry
	for _, t := range q.Tables {
		if t.Files > top.Files {
			top = t
		}
	}
	if top.Table != "" {
		worst = fmt.Sprintf(", en derini %s (%s dosya)", top.Table, fmtInt(top.Files))
	}
	scope := ""
	if q.Partial {
		scope = " [yalnız bu düğüm — küme geneli okuma düştü]"
	}
	out := fmt.Sprintf(
		"Distributed spool %s dosya / %s (%s)%s%s. ClickHouse INSERT'i kabul edip diske "+
			"spool'luyor ama arka plan göndericisi *_local'a basamıyor: uygulama 'yazdım' "+
			"sanıyor, veri İNMİYOR.",
		fmtInt(q.Files), chstore.HumanBytes(q.Bytes), limit, worst, scope)
	if top.LastError != "" {
		out += " Son hata: " + top.LastError
	}
	out += " Müdahale: /admin/stats → spool kartı → Runbook."
	return out
}

// ── (3) self-disk-eta ───────────────────────────────────────────────────

func (e *Evaluator) selfDiskETA(ctx context.Context, cfg chstore.SelfHealthConfig) ([]selfProblem, bool) {
	disks, err := e.store.CollectDisks(ctx)
	if err != nil {
		log.Printf("[evaluator] self-health disk okunamadı: %v", err)
		return nil, false
	}
	now := time.Now().Unix()
	e.diskMu.Lock()
	if e.diskSeries == nil {
		e.diskSeries = map[string][]diskSample{}
	}
	seen := make(map[string]bool, len(disks))
	for _, d := range disks {
		if d.Total == 0 || d.Free > d.Total {
			continue // stat edilemeyen disk: "%100 dolu" iddia ETME
		}
		k := chstore.DiskKey(d.Host, d.Disk)
		seen[k] = true
		s := append(e.diskSeries[k], diskSample{tSec: now, used: float64(d.Total - d.Free)})
		if len(s) > selfDiskSeriesMax {
			s = s[len(s)-selfDiskSeriesMax:]
		}
		e.diskSeries[k] = s
	}
	// Kaybolan diskin serisi BÜYÜMESİN: aksi hâlde uzun ömürlü liderde
	// map sessizce sızar (bir düğüm kalıcı olarak gittiğinde).
	for k := range e.diskSeries {
		if !seen[k] {
			delete(e.diskSeries, k)
		}
	}
	series := make(map[string][]diskSample, len(e.diskSeries))
	for k, v := range e.diskSeries {
		series[k] = append([]diskSample(nil), v...)
	}
	e.diskMu.Unlock()

	var out []selfProblem
	for _, d := range disks {
		if d.Total == 0 || d.Free > d.Total {
			continue
		}
		k := chstore.DiskKey(d.Host, d.Disk)
		days, ok := diskETADays(series[k], d.Total)
		if !ok || days >= cfg.DiskEtaDays {
			continue
		}
		usedPct := float64(d.Total-d.Free) / float64(d.Total) * 100
		// severity: <2 gün kritik, üstü uyarı. Okuma anındaki merdiven
		// (computePriority) bunu "<" karşılaştırmasıyla birleştirip
		// P1/P2/P3 üretir ve merdiven KOŞU PAYINDA MONOTONDUR: 2 günün
		// altı P1, ~3.5 güne kadar P2, ötesi P3. Beş günlük payı "şimdi"
		// diye işaretlemek P1'i sulandırırdı.
		sev := "warning"
		if days < selfDiskCriticalDays {
			sev = "critical"
		}
		out = append(out, selfProblem{
			id:       selfDiskRuleID + ":" + k,
			ruleID:   selfDiskRuleID,
			ruleName: "Coremetry · disk dolacak",
			metric:   "self.disk_eta_days",
			// "<" ŞART: ihlal eşiğin ALTINA inmektir. Comparator olmadan
			// öncelik merdiveni oranı ters çevirmez (v0.9.976) ve iki
			// günlük bir koşu payı P2'de kalırdı.
			comparator:  "<",
			severity:    sev,
			value:       days,
			threshold:   cfg.DiskEtaDays,
			description: diskReason(d.Host, d.Disk, days, usedPct, d.Free, cfg.DiskEtaDays),
		})
	}
	return out, true
}

// selfDiskCriticalDays — bunun altındaki koşu payı kritiktir (operatör
// hedefi: "ETA < 2 gün ise P1"). Ayrı sabit, çünkü açılış eşiği
// (cfg.DiskEtaDays) operatör vidası, bu ise şiddet sınırı.
const selfDiskCriticalDays = 2.0

// diskReason — SAF gerekçe (tablo testli). Yüzde + kalan alan + ETA
// birlikte: yalnız ETA "ne kadar yer kaldı"yı, yalnız yüzde "ne zaman"ı
// söylemez.
func diskReason(host, disk string, days, usedPct float64, freeBytes uint64, limitDays float64) string {
	where := disk
	if host != "" {
		where = host + " · " + disk
	}
	return fmt.Sprintf(
		"%s diski %s içinde DOLACAK (şu an %%%.0f dolu, %s boş; eşik %s). "+
			"Projeksiyon son altı saatin doğrusal eğilimi. Seçenekler: retention kısaltma "+
			"(/settings → Retention), disk büyütme, ya da eski parçaların taşınması.",
		where, fmtDays(days), usedPct, chstore.HumanBytes(freeBytes), fmtDays(limitDays))
}

// fmtDays — gün sayısının okunur hâli. Bir günün altında saat yazılır:
// "0.4 gün" operatöre hiçbir şey söylemez, "9 saat" acil olduğunu söyler.
// (v0.6.36 birim-karışımı dersi: değer+birim taşıyan her şablon HER
// birimiyle tablo-testli.)
func fmtDays(days float64) string {
	if days < 1 {
		h := days * 24
		if h < 1 {
			return fmt.Sprintf("%d dakika", int(h*60+0.5))
		}
		return fmt.Sprintf("%.0f saat", h)
	}
	if days < 10 {
		return fmt.Sprintf("%.1f gün", days)
	}
	return fmt.Sprintf("%.0f gün", days)
}

// ── (4) self-channel-broken ─────────────────────────────────────────────

func (e *Evaluator) selfChannelHealth(ctx context.Context, cfg chstore.SelfHealthConfig) ([]selfProblem, bool) {
	channels, err := e.store.ListChannels(ctx)
	if err != nil {
		log.Printf("[evaluator] self-health kanal listesi okunamadı: %v", err)
		return nil, false
	}
	health, err := e.store.ChannelHealth(ctx)
	if err != nil {
		log.Printf("[evaluator] self-health kanal sağlığı okunamadı: %v", err)
		return nil, false
	}
	// Kimlik (kind, name) — ChannelHealth'in kimliği (v0.9.1278) ile
	// birebir. Yapılandırma tarafında Type alanı o kimliğin "kind"i.
	type ck struct{ kind, name string }
	enabled := make(map[ck]bool, len(channels))
	for _, c := range channels {
		enabled[ck{c.Type, c.Name}] = c.Enabled
	}

	var out []selfProblem
	for _, h := range health {
		k := ck{h.ChannelKind, h.ChannelName}
		on, configured := enabled[k]
		if !channelBrokenDecision(h.ConsecFails, cfg.ChannelConsecFails, configured, on) {
			continue
		}
		out = append(out, selfProblem{
			id:       selfChannelRuleID + ":" + h.ChannelKind + ":" + h.ChannelName,
			ruleID:   selfChannelRuleID,
			ruleName: "Coremetry · bildirim kanalı ölü",
			metric:   "self.channel_consec_fails",
			// critical + oran<2 → P2. Eşiğin iki katı ardışık hata
			// (varsayılanda 6) P1'e çıkar: kanal düzelmiyor demektir.
			severity:    "critical",
			value:       float64(h.ConsecFails),
			threshold:   float64(cfg.ChannelConsecFails),
			description: channelReason(h, cfg.ChannelConsecFails),
		})
	}
	return out, true
}

// channelReason — SAF gerekçe (tablo testli).
//
// NOT (bilinçli kabul): bu problemin duyurusu NORMAL fan-out'la gider,
// yani KIRIK KANALA DA bir gönderim denenir ve o gönderim yine hata
// yazar. Sağlıklı kanallar haberi alır — asıl amaç budur. Kırık kanala
// yapılan denemeyi ayıklamak, bildirim hattına bu dedektöre özel bir
// istisna eklemek demekti; kazancı bir satır log gürültüsü, bedeli
// notify hattında dedektör-farkındalığı olurdu.
func channelReason(h chstore.ChannelHealthRow, threshold int) string {
	out := fmt.Sprintf(
		"%s kanalı '%s' ÖLÜ: son %d gönderimin hepsi başarısız (eşik %d). "+
			"Bu kanala giden alarmlar KİMSEYE ULAŞMIYOR.",
		h.ChannelKind, h.ChannelName, h.ConsecFails, threshold)
	if h.LastError != "" {
		out += " Son hata: " + h.LastError
	}
	out += " Kanalı düzeltip Settings → Channels → Test'e basın (başarılı bir gönderim " +
		"problemi kapatır); kanalı silmek ya da kapatmak da kapatır."
	return out
}

// ── Ortak reconcile ─────────────────────────────────────────────────────

// reconcileSelfHealth — dört kuralın ortak açma/tazeleme/kapatma yolu.
//
// covered, bu tikte GERÇEKTEN ölçülebilmiş rule_id kümesidir. Kapatma
// YALNIZ o kümede yapılır: ölçülemeyen bir kuralın açık problemini
// kapatmak, arızanın tam ortasında sinyali silmek olurdu.
func (e *Evaluator) reconcileSelfHealth(ctx context.Context, snap *chstore.OpenProblems, want []selfProblem, covered map[string]bool) {
	now := time.Now().UnixNano()
	wanted := make(map[string]bool, len(want))

	// Deterministik sıra: log akışı ve testler tekrarlanabilir olsun.
	sort.Slice(want, func(i, j int) bool { return want[i].id < want[j].id })

	for _, w := range want {
		wanted[w.id] = true
		existing := snap.ByID(w.id)
		if existing == nil || existing.ID == "" {
			p := chstore.Problem{
				ID:          w.id,
				RuleID:      w.ruleID,
				RuleName:    w.ruleName,
				Severity:    w.severity,
				Service:     w.service, // dört kuralda BOŞ (özne Coremetry'nin kendisi — sahte bir servis adı uydurmak v0.9.401/402'de düzelttiğimiz kırık servis bağlantısını geri getirirdi); self-volume-spike'ta span yazan gerçek servis.
				Metric:      w.metric,
				Comparator:  w.comparator,
				Value:       w.value,
				Threshold:   w.threshold,
				Status:      "open",
				Description: w.description,
				StartedAt:   now,
			}
			if err := e.store.UpsertProblem(ctx, p); err != nil {
				log.Printf("[evaluator] self-health problemi açılamadı %s: %v", w.id, err)
				continue
			}
			e.countOpened()
			log.Printf("[evaluator] PROBLEM OPENED (%s): %s", w.metric, w.description)
			if _, err := e.store.AttachProblemToIncident(ctx, p); err != nil {
				log.Printf("[evaluator] self-health incident attach %s: %v", w.id, err)
			}
			if e.notifier != nil {
				go e.notifier.SendProblemAlert(context.Background(), p)
			}
			continue
		}
		// Tazeleme: canlı değer + gerekçe güncellenir, StartedAt KORUNUR.
		// Severity yaş-eskalasyon tabanına kelepçelenir (v0.8.309 dersi:
		// düz yazım, süpürmenin yükselttiği critical'ı her tikte geri
		// indirip yeniden sayfalama seli üretiyordu).
		existing.Value = w.value
		existing.Threshold = w.threshold
		existing.Comparator = w.comparator
		existing.Description = w.description
		// v0.9.1294 — muaf kurallarda YAŞ KELEPÇESİ YOK: hacim sıçraması
		// tanımı gereği 24 saat açık kalır ve eskalasyon onu birkaç saat
		// içinde critical→P1'e taşırdı (escalationExempt'in doc bloğu).
		if escalationExempt(w.ruleID) {
			existing.Severity = w.severity
		} else {
			existing.Severity = effectiveSeverity(w.severity,
				time.Since(time.Unix(0, existing.StartedAt)), e.escalationCfg(ctx))
		}
		if err := e.store.UpsertProblem(ctx, *existing); err != nil {
			log.Printf("[evaluator] self-health problemi tazelenemedi %s: %v", w.id, err)
		}
	}

	for _, p := range snap.All() {
		if p == nil || p.Status != "open" || !selfHealthRules[p.RuleID] {
			continue
		}
		if wanted[p.ID] || !covered[p.RuleID] {
			continue
		}
		resolved := *p
		chstore.MarkResolved(&resolved, now)
		if err := e.store.UpsertProblem(ctx, resolved); err != nil {
			log.Printf("[evaluator] self-health problemi kapatılamadı %s: %v", p.ID, err)
			continue
		}
		e.countResolved()
		log.Printf("[evaluator] PROBLEM RESOLVED (%s): %s", p.Metric, p.ID)
	}
}

// fmtInt — 132418 → "132.418". chstore.fmtCount'un dışa açık olmayan
// ikizi; aynı iş, ama evaluator paketinden çağrılabilir.
func fmtInt(n uint64) string {
	s := fmt.Sprintf("%d", n)
	if len(s) <= 3 {
		return s
	}
	out := make([]byte, 0, len(s)+len(s)/3)
	pre := len(s) % 3
	if pre > 0 {
		out = append(out, s[:pre]...)
	}
	for i := pre; i < len(s); i += 3 {
		if len(out) > 0 {
			out = append(out, '.')
		}
		out = append(out, s[i:i+3]...)
	}
	return string(out)
}
