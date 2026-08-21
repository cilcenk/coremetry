package anomaly

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/cilcenk/coremetry/internal/chstore"
)

// investigation.go — v0.9.510. CoSRE P1 soruşturması.
//
// Operatör: "bir hata olduğunda gerekirse pod'larına, metriklerine,
// loglarına baksın, ona göre sonuç çıkarsın, kaydetsin. Gerçek bir SRE gibi."
//
// MİMARİ İLKE — birincil model gemma4-26b-a4b-it (26B toplam / 4B aktif
// MoE, instruction-tuned; v0.9.517'de operatör ekranından DOĞRULANDI).
//
// Gerekçe model kapasitesi DEĞİL, üç somut sebep:
//  1. MALİYET — her P1'de serbest ajan döngüsü N tur LLM çağrısı demek;
//     deterministik playbook tek anlatım çağrısıyla bitiyor.
//  2. DENETLENEBİLİRLİK — hangi sinyale bakıldığı KOD, dolayısıyla
//     tablo-testli ve tekrarlanabilir. Model seçseydi her koşuda
//     farklı bakabilirdi.
//  3. SINIRLILIK — okumalar sınırlı kalmalı (LIMIT/timeout/pencere);
//     modele araç verirsek bu sınırları o seçer.
// SRE davranışını doğru katmana koyuyoruz:
//
//	nereye bakacağına karar vermek → KOD (buradaki saf playbook)
//	sinyalleri okumak              → mevcut sınırlı store okumaları
//	sebepleri sıralamak            → correlator.Synthesize (LLM'siz)
//	bulguyu anlatmak               → model, tek atış, kanıt önünde
//
// Modelin işi araştırmak değil, bulguyu anlatmak.
//
// MALİYET: bugünkü kanıt toplama tick başına BEŞ okuma yapıp tüm batch'te
// paylaşıyor (fusion.go gatherEvidenceInputs). Koşullu derinleşme bunu
// bozar, o yüzden yalnız P1 anchor'larında koşar — prod'da 105 açık
// problem varken hepsine derin soruşturma ClickHouse'u ezerdi.

// signalFamily — playbook'un çekmeye karar verebileceği ek kanıt ailesi.
type signalFamily string

const (
	familyExceptions signalFamily = "exceptions" // exception grupları
	familyLogs       signalFamily = "logs"       // baskın log şablonları
	familySaturation signalFamily = "saturation" // heap / GC / pod doygunluğu
	familyRuntime    signalFamily = "runtime"    // servis çalışma zamanı durumu
	familyOperations signalFamily = "operations" // yavaşlayan operasyonlar
	// familyBusiness (v0.9.511) — İŞ boyutu: hangi kanal / hangi fonksiyon
	// kodu etkileniyor. Teknik cevabı ("hata oranı %4") işin duyduğu
	// cevaba ("mobil kanaldan gelen işlemler patlıyor") çeviren boyut.
	familyBusiness signalFamily = "business"
)

// businessDimKeys — kırılımı alınan attribute anahtarları. Sabit liste:
// yüksek kardinaliteli rastgele bir attribute buraya girerse hem sorgu
// pahalılaşır hem kanıt bloğu gürültüye boğulur.
var businessDimKeys = []string{"CHANNEL_CODE", "FUNCTION_CODE"}

// investigationPlan — SAF dallanma: bu problem şekli için hangi ek kanıt
// aileleri okunmalı. Tablo-testli (investigation_test.go).
//
// Gerçek bir SRE'nin refleksi: hata oranı fırladıysa exception'lara ve
// loglara bakarsın; gecikme fırladıysa pod doygunluğuna ve hangi
// operasyonun yavaşladığına; servis düştüyse pod durumuna. Hepsine birden
// bakmak hem pahalı hem de sinyali gürültüye boğar.
//
// Boş plan = derin soruşturma yok (bugünkü sığ paket yeterli).
func investigationPlan(p chstore.Problem) []signalFamily {
	metric := strings.ToLower(strings.TrimSpace(p.Metric))
	rule := strings.ToLower(p.RuleName)

	switch {
	// Hata oranı / exception — "ne patlıyor" sorusu.
	case strings.Contains(metric, "error"), strings.Contains(rule, "exception"):
		return []signalFamily{familyExceptions, familyLogs, familyBusiness}

	// Gecikme — "neden yavaşladı" sorusu. Doygunluk önce (heap/GC bir
	// servisi sessizce yavaşlatan en sık sebep), sonra hangi operasyon.
	case strings.Contains(metric, "p99"), strings.Contains(metric, "p95"),
		strings.Contains(metric, "p50"), strings.Contains(metric, "avg"),
		strings.Contains(metric, "duration"), strings.Contains(metric, "latency"):
		return []signalFamily{familySaturation, familyOperations, familyBusiness}

	// Ayakta mı — pod durumu, sonra loglar (crash sebebi logda olur).
	case strings.Contains(metric, "down"), strings.Contains(metric, "availab"),
		strings.Contains(metric, "health"), strings.Contains(rule, "down"):
		return []signalFamily{familyRuntime, familyLogs}

	// Bellek / GC / doygunluk kuralları doğrudan doygunluğa.
	case strings.Contains(metric, "heap"), strings.Contains(metric, "memory"),
		strings.Contains(metric, "gc"), strings.Contains(metric, "cpu"),
		strings.Contains(metric, "restart"):
		return []signalFamily{familySaturation, familyRuntime}

	// Trafik/hacim kuralları — hacim düşüşü genelde yukarı akıştan gelir,
	// derin sinyal eklemez; bugünkü komşuluk paketi zaten doğru araç.
	case strings.Contains(metric, "request_rate"), strings.Contains(metric, "throughput"),
		strings.Contains(metric, "count"):
		return nil
	}
	return nil
}

// CheckedSignal / DeepEvidence artık chstore'da (v0.9.516): hipotezle
// birlikte KALICI olarak saklanıyorlar, dolayısıyla en alt katmana
// taşındılar. Tüm üyeleri zaten chstore tipleriydi.

// deepEvidenceLimit — aile başına taşınan kayıt tavanı. Kanıt bloğu
// odaklı kalmalı: uzun blok asıl sinyali (sıralı adaylar, hata sayıları)
// gürültüye boğar. İlk N zaten en ağırları (okumalar sıralı geliyor).
const deepEvidenceLimit = 5

// gatherDeepEvidence — plandaki her aile için BİR sınırlı okuma. Best-effort:
// bir okuma patlarsa o aile "bakılamadı" olarak ize girer ve soruşturma
// devam eder; kısmi kanıt hiç kanıttan iyidir ve iz dürüst kalır.
func gatherDeepEvidence(ctx context.Context, store *chstore.Store, p chstore.Problem, plan []signalFamily, from, to time.Time) chstore.DeepEvidence {
	var d chstore.DeepEvidence
	for _, f := range plan {
		switch f {
		case familyExceptions:
			// v0.9.1053 (Faz 0.2) — yalnız INCIDENT PENCERESİNDE aktif
			// gruplar: eski hâl ömür-boyu last_seen sırasıyla ilk 5'i
			// alıyordu — 40 dk önceki incident'a bugünün grupları
			// yazılabiliyordu.
			gs, err := store.ListExceptionGroups(ctx, chstore.ExceptionGroupFilter{
				Service: p.Service, Limit: deepEvidenceLimit,
				ActiveFromNs: from.UnixNano(), ActiveToNs: to.UnixNano(),
			})
			noteChecked(&d, f, err, len(gs), func() string {
				return fmt.Sprintf("%d exception grubu, en sık: %s", len(gs), firstExcTitle(gs))
			})
			d.Exceptions = gs

		case familyLogs:
			// ListLogTemplatesFilter'da servis alanı YOK — şablonlar filo
			// geneli tutuluyor ve satır kendi Services listesini taşıyor.
			// Sınırlı okuma + istemci tarafı süzme; tavan geniş tutuluyor
			// ki tek servisin şablonu filo sıralamasında kaybolmasın.
			all, err := store.ListLogTemplates(ctx, chstore.ListLogTemplatesFilter{
				SinceNs: from.UnixNano(), SortBy: "count", Limit: 100,
			})
			ts := templatesForService(all, p.Service)
			noteChecked(&d, f, err, len(ts), func() string {
				return fmt.Sprintf("%d baskın log şablonu", len(ts))
			})
			d.Templates = ts

		case familySaturation:
			// v0.9.1053 (Faz 0.2) — INCIDENT penceresi. Eski çağrılar sabit
			// now-10dk okuyordu: 40 dk önce açılmış problemin kanıtına ŞU
			// ANKİ heap yazılıyor, denetim izi Found:true diyordu — pozitif
			// tarafta yanlış-kanıt (rca_evidence'ın negatif-kanıt uyarısının
			// tersi).
			heap, herr := store.JVMHeapPodUsage(ctx, from, to)
			heap = samplesForService(heap, p.Service, true)
			// v0.9.1206 — GC tavan istemez: JVMGCPodPause Limit doldurmaz,
			// eski koşulsuz Limit>0 filtresi GC'nin tamamını eliyordu.
			gc, gerr := store.JVMGCPodPause(ctx, from, to)
			gc = samplesForService(gc, p.Service, false)
			err := herr
			if err == nil {
				err = gerr
			}
			noteChecked(&d, f, err, len(heap)+len(gc), func() string {
				return fmt.Sprintf("%d pod heap örneği, %d GC örneği", len(heap), len(gc))
			})
			d.Heap, d.GCPause = heap, gc

		case familyRuntime:
			// v0.9.1053 — parmak izi incident penceresinin SONU itibarıyla.
			rt, err := store.GetServiceRuntime(ctx, p.Service, to)
			n := 0
			if rt != nil {
				n = 1
			}
			noteChecked(&d, f, err, n, func() string {
				if rt == nil {
					return "çalışma zamanı kaydı yok"
				}
				return "servis çalışma zamanı durumu okundu"
			})
			d.Runtime = rt

		case familyOperations:
			// env "" — anomaly root-cause investigation is env-agnostic
			// (it explains a Problem's own service across all environments).
			ops, err := store.GetOperationSummary(ctx, p.Service, to.Sub(from), from, to, false, "")
			ops = topSlowOps(ops)
			noteChecked(&d, f, err, len(ops), func() string {
				return fmt.Sprintf("%d operasyon, en ağırı: %s", len(ops), firstOpName(ops))
			})
			d.SlowOps = ops

		case familyBusiness:
			// Anahtar başına BİR sınırlı okuma. Kırılım boşsa (kurulum bu
			// attribute'ları yaymıyor) iz "kayıt yok" der ve anlatım o
			// boyuttan bahsetmez — sessizce atlamak yerine dürüst kalır.
			d.Business = map[string][]chstore.BusinessSlice{}
			var firstErr error
			total := 0
			for _, key := range businessDimKeys {
				sl, err := store.BusinessBreakdown(ctx, p.Service, key, from, to)
				if err != nil && firstErr == nil {
					firstErr = err
				}
				if len(sl) > 0 {
					d.Business[key] = sl
					total += len(sl)
				}
			}
			d.CodeMeaning = resolveBusinessCodes(ctx, store, d.Business)
			noteChecked(&d, f, firstErr, total, func() string {
				return fmt.Sprintf("%d iş boyutu değeri (%s), %d kodun anlamı çözüldü",
					total, strings.Join(businessDimKeys, ", "), len(d.CodeMeaning))
			})
		}
	}
	return d
}

// resolveBusinessCodes — iş kodlarının İNSAN anlamını RAG'dan çözer
// (v0.9.512). Operatör kanal kodu sözlüğünü RAG dokümanı olarak
// yüklüyor; burada her kod için BİR sözcüksel arama yapılır.
//
// Neden sözcüksel (TopKRagChunksByContent), semantik değil: kanal kodu
// TAM EŞLEŞME aranan bir jeton. Semantik benzerlik burada yanlış araç
// olurdu, üstelik gömme yolu bge-m3'e bağlı — sözcüksel yol o blokörden
// bağımsız çalışır.
//
// Chunk'ın TAMAMI değil, kodu içeren SATIR alınır: koca bir doküman
// parçası asıl kanıtı (kırılım sayıları) bastırır.
func resolveBusinessCodes(ctx context.Context, store *chstore.Store, business map[string][]chstore.BusinessSlice) map[string]string {
	if len(business) == 0 {
		return nil
	}
	out := map[string]string{}
	lookups := 0
	for _, slices := range business {
		for _, b := range slices {
			if b.Value == "" || out[b.Value] != "" {
				continue
			}
			// Maliyet tavanı: P1 başına en fazla bu kadar arama. Kodlar
			// az sayıda olmalı; yanlış yapılandırılmış yüksek kardinaliteli
			// bir attribute soruşturmayı arama fırtınasına çevirmemeli.
			if lookups >= businessCodeLookupMax {
				return out
			}
			lookups++
			hits, err := store.TopKRagChunksByContent(ctx, b.Value, 1)
			if err != nil || len(hits) == 0 {
				continue
			}
			if line := codeLineFrom(hits[0].Content, b.Value); line != "" {
				out[b.Value] = line
			}
		}
	}
	return out
}

// businessCodeLookupMax — P1 başına RAG arama tavanı.
const businessCodeLookupMax = 8

// codeLineFrom — chunk içinden kodu İÇEREN ilk satırı döndürür. Saf —
// tablo-testli. Kod hiçbir satırda geçmiyorsa boş döner (chunk sözcüksel
// skorla gelmiş olabilir ama kod başka bir jetondan eşleşmiş olabilir;
// o durumda yanlış satır göstermektense hiç göstermemek doğru).
func codeLineFrom(content, code string) string {
	if code == "" {
		return ""
	}
	for _, ln := range strings.Split(content, "\n") {
		if strings.Contains(ln, code) {
			ln = strings.TrimSpace(ln)
			if ln == "" {
				continue
			}
			return truncate(ln, 120)
		}
	}
	return ""
}

// note — denetim izine bir satır ekler. Hata varsa "bakılamadı" olarak
// kaydeder: sessizce atlamak izi yalancı yapardı.
func noteChecked(d *chstore.DeepEvidence, f signalFamily, err error, n int, detail func() string) {
	if err != nil {
		d.Checked = append(d.Checked, chstore.CheckedSignal{
			Family: string(f), Found: false, Detail: "okunamadı: " + err.Error(),
		})
		return
	}
	if n == 0 {
		d.Checked = append(d.Checked, chstore.CheckedSignal{
			Family: string(f), Found: false, Detail: "bakıldı, kayıt yok",
		})
		return
	}
	d.Checked = append(d.Checked, chstore.CheckedSignal{
		Family: string(f), Found: true, Detail: detail(), Records: n,
	})
}

// samplesForService — CapacitySample'da Instance servis, Subkey pod
// (guided pod bundle ile aynı eşleme, copilot_guided.go:1150).
//
// v0.9.1206 (Faz 6.2) — requireLimit parametresi. Eski hâl KOŞULSUZ
// `s.Limit > 0` istiyordu; JVMGCPodPause Limit'i HİÇ doldurmadığından
// (runtime_pods.go yalnız Instance/Subkey/Usage tarar) GC örneklerinin
// TAMAMI eleniyordu: d.GCPause hep boş, render dalı ölü kod, denetim
// izi "N örnek" derken GC payı hep 0'dı. Heap tavan ister (yüzde onunla
// anlamlı); GC istemez (Usage ms cinsinden mutlak değer).
func samplesForService(in []chstore.CapacitySample, service string, requireLimit bool) []chstore.CapacitySample {
	out := in[:0:0]
	for _, s := range in {
		if s.Instance != service {
			continue
		}
		if requireLimit && s.Limit <= 0 {
			continue
		}
		out = append(out, s)
		if len(out) >= deepEvidenceLimit {
			break
		}
	}
	return out
}

// templatesForService — şablon satırı kendi Services listesini taşıyor.
func templatesForService(in []chstore.LogTemplate, service string) []chstore.LogTemplate {
	out := in[:0:0]
	for _, t := range in {
		for _, svc := range t.Services {
			if svc == service {
				out = append(out, t)
				break
			}
		}
		if len(out) >= deepEvidenceLimit {
			break
		}
	}
	return out
}

// topSlowOps — p99'a göre en ağır N operasyon.
func topSlowOps(in []chstore.OperationSummary) []chstore.OperationSummary {
	sort.Slice(in, func(i, j int) bool { return in[i].P99Ms > in[j].P99Ms })
	if len(in) > deepEvidenceLimit {
		in = in[:deepEvidenceLimit]
	}
	return in
}

func firstExcTitle(gs []chstore.ExceptionGroup) string {
	if len(gs) == 0 {
		return "—"
	}
	return gs[0].Type
}

func firstOpName(ops []chstore.OperationSummary) string {
	if len(ops) == 0 {
		return "—"
	}
	return ops[0].Name
}

// renderDeepEvidence — kanıt bloğuna "neye bakıldı, ne bulundu" bölümünü
// basar. Model bu metni okuyup anlatır; operatör de aynı metni görebilir.
func renderDeepEvidence(sb *strings.Builder, d chstore.DeepEvidence) {
	if len(d.Checked) == 0 {
		return
	}
	sb.WriteString("\nSORUŞTURMA — bakılan sinyaller:\n")
	for _, c := range d.Checked {
		mark := "yok"
		if c.Found {
			mark = "VAR"
		}
		fmt.Fprintf(sb, "  - %s: %s — %s\n", c.Family, mark, c.Detail)
	}
	for i, g := range d.Exceptions {
		if i == 0 {
			sb.WriteString("Exception grupları:\n")
		}
		fmt.Fprintf(sb, "  - %s: %s (%d kez)\n", g.Type, truncate(g.Message, 90), g.Occurrences)
	}
	for i, t := range d.Templates {
		if i == 0 {
			sb.WriteString("Baskın log şablonları:\n")
		}
		fmt.Fprintf(sb, "  - %s (%d)\n", truncate(strings.TrimSpace(t.Template), 110), t.TotalCount)
	}
	for i, s := range d.Heap {
		if i == 0 {
			sb.WriteString("Pod heap kullanımı:\n")
		}
		// v0.9.1206 — Limit==0 render guard'ı. Bölme paniklemez ama +Inf
		// üretir ve "heap %+Inf" satırı modele çöp olarak girer. Girdi
		// SAKLANAN JSON (eski yazıcı, gevşemiş filtre) — kaynak filtresine
		// güvenmek yetmez, guard render'da da durmalı (CapacitySample
		// dokümanı Limit=0'ı meşru durum sayar: ham-rate kontrolleri).
		if s.Limit > 0 {
			fmt.Fprintf(sb, "  - %s: heap %%%.0f\n", s.Subkey, s.Usage/s.Limit*100)
		} else {
			fmt.Fprintf(sb, "  - %s: heap %.3g (tavan bilinmiyor)\n", s.Subkey, s.Usage)
		}
	}
	for i, s := range d.GCPause {
		if i == 0 {
			sb.WriteString("GC duraklamaları:\n")
		}
		fmt.Fprintf(sb, "  - %s: GC duraklama %.0f\n", s.Subkey, s.Usage)
	}
	// v0.9.1206 (Faz 6.2) — Runtime render branşı. Alan TOPLANIYOR ve
	// hipotez JSON'unda SAKLANIYORDU ama hiç render edilmiyordu: denetim
	// izi "çalışma zamanı durumu okundu" derken model içeriği hiç
	// görmüyordu. Öncelik FE emsaliyle aynı (language+runtimeVersion >
	// runtimeName > sdkVersion); GetServiceRuntime satır yokken boş-alanlı
	// non-nil döner — o durumda satır hiç basılmaz.
	if rt := d.Runtime; rt != nil {
		parts := []string{}
		switch {
		case rt.Language != "" && rt.RuntimeVersion != "":
			parts = append(parts, rt.Language+" "+rt.RuntimeVersion)
		case rt.Language != "":
			parts = append(parts, rt.Language)
		case rt.RuntimeName != "":
			parts = append(parts, rt.RuntimeName)
		case rt.SDKVersion != "":
			parts = append(parts, "sdk "+rt.SDKVersion)
		}
		if rt.Host != "" {
			parts = append(parts, "host "+rt.Host)
		}
		if rt.OS != "" {
			parts = append(parts, "os "+rt.OS)
		}
		if len(parts) > 0 {
			fmt.Fprintf(sb, "Çalışma zamanı: %s\n", strings.Join(parts, ", "))
		}
	}
	for _, key := range businessDimKeys {
		sl := d.Business[key]
		if len(sl) == 0 {
			continue
		}
		fmt.Fprintf(sb, "%s kırılımı (en çok hata üreten önce):\n", key)
		for _, b := range sl {
			if m := d.CodeMeaning[b.Value]; m != "" {
				fmt.Fprintf(sb, "  - %s [%s]: %d çağrı, %d hata (%%%.1f)\n",
					b.Value, m, b.Calls, b.Errors, b.ErrPct)
				continue
			}
			fmt.Fprintf(sb, "  - %s: %d çağrı, %d hata (%%%.1f)\n", b.Value, b.Calls, b.Errors, b.ErrPct)
		}
	}
	for i, o := range d.SlowOps {
		if i == 0 {
			sb.WriteString("En ağır operasyonlar:\n")
		}
		fmt.Fprintf(sb, "  - %s: p99 %.0fms, %d çağrı\n", o.Name, o.P99Ms, o.SpanCount)
	}
}

// truncate — kanıt bloğu odaklı kalmalı; uzun mesaj/şablon kırpılır
// ama kırpıldığı belli olur.
func truncate(s string, n int) string {
	s = strings.TrimSpace(s)
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
