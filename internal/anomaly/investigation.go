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
// MİMARİ İLKE: birincil model Gemma4-2B. Gemma ailesi native
// function-calling ile eğitilmiyor ve 2B'de araç seçimi zaten güvenilmez —
// serbest ajan döngüsü kurulamaz. SRE davranışını modele YAPTIRMAK yerine
// doğru katmana koyuyoruz:
//
//	nereye bakacağına karar vermek → KOD (buradaki saf playbook)
//	sinyalleri okumak              → mevcut sınırlı store okumaları
//	sebepleri sıralamak            → correlator.Synthesize (LLM'siz)
//	bulguyu anlatmak               → model, tek atış, kanıt önünde
//
// Modelin işi araştırmak değil, bulguyu anlatmak. 2B'nin gerçekten
// yapabildiği tek iş bu.
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

// CheckedSignal — denetim izinin bir satırı: NEYE bakıldı, bulundu mu, ne
// bulundu. v0.9.510'da yalnız kanıt metnine basılıyor; kalıcı kolon ayrı
// dilimde gelecek (şema değişikliği tek başına izlenebilsin diye).
//
// Bu iz opsiyonel bir süs DEĞİL: 2B modelin ürettiği anlatıma güvenmenin
// tek yolu hangi sinyallerin GERÇEKTEN okunduğunun görünür olması. İz
// "pod'lara bakıldı, anormallik yok" derken anlatım "pod restart'ları
// yüzünden" diyorsa çelişki operatöre görünür olur. Derin kanıtı denetim
// izi olmadan vermek modeli daha İDDİALI yapar, daha doğru değil.
type CheckedSignal struct {
	Family  signalFamily
	Found   bool
	Detail  string // tek satır insan-okur özet
	Records int    // okunan kayıt sayısı (0 = yok)
}

// DeepEvidence — playbook'un topladığı ek kanıt.
type DeepEvidence struct {
	Checked    []CheckedSignal
	Exceptions []chstore.ExceptionGroup
	Templates  []chstore.LogTemplate
	Heap       []chstore.CapacitySample
	GCPause    []chstore.CapacitySample
	Runtime    *chstore.ServiceRuntime
	SlowOps    []chstore.OperationSummary
	// Business — anahtar → hata-ağırlıklı kırılım (v0.9.511).
	Business map[string][]chstore.BusinessSlice
}

// deepEvidenceLimit — aile başına taşınan kayıt tavanı. Kanıt bloğu 2B'nin
// bağlamına sığmalı; ilk N zaten en ağırları (okumalar sıralı geliyor).
const deepEvidenceLimit = 5

// gatherDeepEvidence — plandaki her aile için BİR sınırlı okuma. Best-effort:
// bir okuma patlarsa o aile "bakılamadı" olarak ize girer ve soruşturma
// devam eder; kısmi kanıt hiç kanıttan iyidir ve iz dürüst kalır.
func gatherDeepEvidence(ctx context.Context, store *chstore.Store, p chstore.Problem, plan []signalFamily, from, to time.Time) DeepEvidence {
	var d DeepEvidence
	for _, f := range plan {
		switch f {
		case familyExceptions:
			gs, err := store.ListExceptionGroups(ctx, chstore.ExceptionGroupFilter{
				Service: p.Service, Limit: deepEvidenceLimit,
			})
			d.note(f, err, len(gs), func() string {
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
			d.note(f, err, len(ts), func() string {
				return fmt.Sprintf("%d baskın log şablonu", len(ts))
			})
			d.Templates = ts

		case familySaturation:
			heap, herr := store.JVMHeapPodUsage(ctx)
			heap = samplesForService(heap, p.Service)
			gc, gerr := store.JVMGCPodPause(ctx)
			gc = samplesForService(gc, p.Service)
			err := herr
			if err == nil {
				err = gerr
			}
			d.note(f, err, len(heap)+len(gc), func() string {
				return fmt.Sprintf("%d pod heap örneği, %d GC örneği", len(heap), len(gc))
			})
			d.Heap, d.GCPause = heap, gc

		case familyRuntime:
			rt, err := store.GetServiceRuntime(ctx, p.Service)
			n := 0
			if rt != nil {
				n = 1
			}
			d.note(f, err, n, func() string {
				if rt == nil {
					return "çalışma zamanı kaydı yok"
				}
				return "servis çalışma zamanı durumu okundu"
			})
			d.Runtime = rt

		case familyOperations:
			ops, err := store.GetOperationSummary(ctx, p.Service, to.Sub(from), from, to, false)
			ops = topSlowOps(ops)
			d.note(f, err, len(ops), func() string {
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
			d.note(f, firstErr, total, func() string {
				return fmt.Sprintf("%d iş boyutu değeri (%s)", total, strings.Join(businessDimKeys, ", "))
			})
		}
	}
	return d
}

// note — denetim izine bir satır ekler. Hata varsa "bakılamadı" olarak
// kaydeder: sessizce atlamak izi yalancı yapardı.
func (d *DeepEvidence) note(f signalFamily, err error, n int, detail func() string) {
	if err != nil {
		d.Checked = append(d.Checked, CheckedSignal{
			Family: f, Found: false, Detail: "okunamadı: " + err.Error(),
		})
		return
	}
	if n == 0 {
		d.Checked = append(d.Checked, CheckedSignal{
			Family: f, Found: false, Detail: "bakıldı, kayıt yok",
		})
		return
	}
	d.Checked = append(d.Checked, CheckedSignal{
		Family: f, Found: true, Detail: detail(), Records: n,
	})
}

// samplesForService — CapacitySample'da Instance servis, Subkey pod
// (guided pod bundle ile aynı eşleme, copilot_guided.go:1150).
func samplesForService(in []chstore.CapacitySample, service string) []chstore.CapacitySample {
	out := in[:0:0]
	for _, s := range in {
		if s.Instance == service && s.Limit > 0 {
			out = append(out, s)
			if len(out) >= deepEvidenceLimit {
				break
			}
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
func renderDeepEvidence(sb *strings.Builder, d DeepEvidence) {
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
		fmt.Fprintf(sb, "  - %s: heap %%%.0f\n", s.Subkey, s.Usage/s.Limit*100)
	}
	for i, s := range d.GCPause {
		if i == 0 {
			sb.WriteString("GC duraklamaları:\n")
		}
		fmt.Fprintf(sb, "  - %s: GC duraklama %.0f\n", s.Subkey, s.Usage)
	}
	for _, key := range businessDimKeys {
		sl := d.Business[key]
		if len(sl) == 0 {
			continue
		}
		fmt.Fprintf(sb, "%s kırılımı (en çok hata üreten önce):\n", key)
		for _, b := range sl {
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

// truncate — kanıt bloğu 2B'nin bağlamına sığmalı; uzun mesaj/şablon
// kırpılır ama kırpıldığı belli olur.
func truncate(s string, n int) string {
	s = strings.TrimSpace(s)
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
