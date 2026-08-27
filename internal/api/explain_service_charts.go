package api

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/cilcenk/coremetry/internal/chstore"
)

// explain_service_charts — Service → Details grafiklerinin AI özeti için
// KANIT TOPLAMA katmanı (onaylı mockup: "Ne oldu · İlişkili sinyaller ·
// Sonraki adım").
//
// Neden ayrı, SAF bir dosya: v0.9.482'de öğrenilen kural — bir explain
// yüzeyinin kanıt kurulumu handler'ın İÇİNDE yaşarsa çekmece-içi sohbet
// (ikinci çağıran) onu yeniden kuramaz. `explain_trace_input.go` emsali.
// Buradaki her şey saf: girdi struct'ı → metin/karar, CH'ye dokunmaz,
// tablo-güdümlü testlenir (explain_service_charts_test.go).
//
// Küçük-model sözleşmesi (project-copilot-runtime): sinyaller SUNUCUDA
// toplanır, model YALNIZ anlatır. Tool-loop yok, serbest sorgu yok.

// Grafik kapsamı — mockup'taki Ⓑ giriş noktası (kart başlığındaki ✨) tek
// bir karta daraltır; Ⓐ (toolbar) tüm kartları kapsar. Değerler ?ai=
// kodeğinin 5. segmentiyle BİREBİR aynı (frontend lib/aiSubject.ts).
const (
	chartScopeAll = "all"
	chartScopeRPS = "rps"
	chartScopeErr = "err"
	chartScopeDur = "dur"
)

// normalizeChartScope — bilinmeyen/boş kapsam "all"a düşer. URL elle
// düzenlenebilir; tanınmayan bir kapsam yüzünden 4xx dönmek yerine en
// geniş (ve her zaman doğru) kapsamı anlatmak daha dürüst.
func normalizeChartScope(raw string) string {
	switch strings.TrimSpace(strings.ToLower(raw)) {
	case chartScopeRPS:
		return chartScopeRPS
	case chartScopeErr:
		return chartScopeErr
	case chartScopeDur:
		return chartScopeDur
	default:
		return chartScopeAll
	}
}

// chartScopeLabel — çekmece başlığındaki kapsam çipi + prompt'taki
// "şu grafiğe odaklan" cümlesi AYNI dizeden beslenir; ikisi ayrışırsa
// operatör başka bir kartın cevabını okuduğunu sanır.
func chartScopeLabel(scope string) string {
	switch normalizeChartScope(scope) {
	case chartScopeRPS:
		return "RPS by operation"
	case chartScopeErr:
		return "Error rate by operation"
	case chartScopeDur:
		return "P99 latency by operation"
	default:
		return "Tüm RED grafikleri"
	}
}

// OpDelta — bir operasyonun pencere vs bir-önceki-eş-pencere değişimi.
// Mockup'taki "operasyon" satırı: en kötü N + "diğer M: değişim yok".
type OpDelta struct {
	Name string `json:"name"`
	// Calls — MEVCUT pencerenin çağrı sayısı (sıralama tie-break'i ve
	// "bu gerçekten hacimli mi" sorusunun cevabı).
	Calls uint64 `json:"calls"`
	// P95Ratio — cur.P95 / prior.P95. 1.0 = değişim yok. Prior p95 sıfır
	// veya yoksa 0 (== "oran hesaplanamadı"), asla +Inf.
	P95Ratio float64 `json:"p95Ratio"`
	// ErrDeltaPP — hata oranı farkı YÜZDE PUANI cinsinden.
	// chstore.OperationSummary.ErrorRate 0..100 ÖLÇEĞİNDE (repo.go:703
	// `errorCount/spanCount*100`), yani bu çıkarma doğrudan pp verir.
	// Oran (0..1) sanıp 100 ile çarpmak klasik birim-karışımı olurdu.
	ErrDeltaPP float64 `json:"errDeltaPp"`
	// IsNew — önceki pencerede HİÇ görülmemiş operasyon (deploy'un yeni
	// ucu). Sıfır-çağrılı eski operasyondan ayrıdır: HasPrior taşır.
	IsNew bool `json:"isNew"`
}

// opDeltaMinCalls — bu eşiğin altındaki operasyonlar "en kötü" listesine
// GİRMEZ. 2 çağrılık bir operasyonun p95'i üçe katlandığında bu gürültüdür,
// bulgu değil; listeyi tek-örnekli uçlar doldurursa tablo yalan söyler.
const opDeltaMinCalls = 5

// Bozulma bariyerleri — bunların ALTINDA kalan bir operasyon "değişim yok"
// kovasına gider. Mockup'ın "diğer 14 operasyon: değişim yok" satırı bu
// ayrımın görünen hâli.
const (
	opDeltaP95RatioBar = 1.2 // %20 yavaşlama
	opDeltaErrPPBar    = 0.5 // yarım yüzde puanı hata artışı
)

// selectOpDeltas — GetOperationSummaryCompared satırlarından "en kötü topN"
// operasyonu seçer ve geri kalanın SAYISINI döndürür.
//
// Sözleşme (testle pinli):
//   - Hiçbir satırda HasPrior yoksa karşılaştırma YAPILAMAZ: (nil, len(rows)).
//     İlk pencere / MV boşluğu durumunda her operasyonu "yeni" diye
//     raporlamak uydurma olurdu.
//   - Dönen liste bozulma skoruna göre azalan; eşitlikte çağrı sayısı
//     azalan, sonra ad artan — sıralama DETERMİNİST (aynı veri = aynı
//     prompt = tekrarlanabilir anlatım).
//   - otherCount = len(rows) - len(seçilen). "Diğer M" HER ZAMAN toplamı
//     tamamlar; ayrı bir filtreden geçmez.
func selectOpDeltas(rows []chstore.OperationSummary, topN int) ([]OpDelta, int) {
	if topN <= 0 || len(rows) == 0 {
		return nil, len(rows)
	}
	anyPrior := false
	for _, r := range rows {
		if r.HasPrior {
			anyPrior = true
			break
		}
	}
	if !anyPrior {
		return nil, len(rows)
	}

	type scored struct {
		d     OpDelta
		score float64
	}
	cands := make([]scored, 0, len(rows))
	for _, r := range rows {
		if r.SpanCount < opDeltaMinCalls {
			continue
		}
		d := OpDelta{Name: r.Name, Calls: r.SpanCount, IsNew: !r.HasPrior}
		if r.HasPrior {
			if r.PriorP95Ms > 0 {
				d.P95Ratio = r.P95Ms / r.PriorP95Ms
			}
			d.ErrDeltaPP = r.ErrorRate - r.PriorErrorRate
		}
		// Bariyer: en az bir boyutta gerçekten kötüleşmiş olmalı.
		if !(d.IsNew || d.P95Ratio >= opDeltaP95RatioBar || d.ErrDeltaPP >= opDeltaErrPPBar) {
			continue
		}
		// Skor — iki boyutu tek eksene indirir. 5pp hata artışı ≈ 1×
		// yavaşlama ağırlığında; yeni operasyon sabit 1.0 taban alır
		// (kıyaslanacak öncesi yok, ama deploy'un getirdiği uç önemlidir).
		score := 0.0
		if d.P95Ratio > 1 {
			score += d.P95Ratio - 1
		}
		if d.ErrDeltaPP > 0 {
			score += d.ErrDeltaPP / 5
		}
		if d.IsNew {
			score += 1
		}
		cands = append(cands, scored{d: d, score: score})
	}
	sort.SliceStable(cands, func(i, j int) bool {
		if cands[i].score != cands[j].score {
			return cands[i].score > cands[j].score
		}
		if cands[i].d.Calls != cands[j].d.Calls {
			return cands[i].d.Calls > cands[j].d.Calls
		}
		return cands[i].d.Name < cands[j].d.Name
	})
	if len(cands) > topN {
		cands = cands[:topN]
	}
	out := make([]OpDelta, 0, len(cands))
	for _, c := range cands {
		out = append(out, c.d)
	}
	return out, len(rows) - len(out)
}

// ChartDeploySignal / ChartProblemSignal / ChartAnomalySignal — çekmecenin
// "İlişkili sinyaller" tablosunun satırları. Frontend PİVOT LİNKLERİNİ
// bunlardan KENDİ kurar (rotalar frontend'in bilgisi; Go'da /traces?…
// dizesi üretmek yönlendirmeyi iki yere kopyalardı).
type ChartDeploySignal struct {
	TimeUnixNs    int64  `json:"timeUnixNs"`
	Kind          string `json:"kind"` // "deploy" | "restart"
	VersionBefore string `json:"versionBefore,omitempty"`
	VersionAfter  string `json:"versionAfter,omitempty"`
	PodsReplaced  int    `json:"podsReplaced"`
}

type ChartProblemSignal struct {
	ID        string  `json:"id"`
	Title     string  `json:"title"`
	Severity  string  `json:"severity"`
	Priority  string  `json:"priority,omitempty"`
	StartedAt int64   `json:"startedAt"`
	Metric    string  `json:"metric,omitempty"`
	Value     float64 `json:"value"`
	Threshold float64 `json:"threshold"`
}

type ChartAnomalySignal struct {
	ID        string  `json:"id"`
	Kind      string  `json:"kind"`
	Pattern   string  `json:"pattern"`
	StartedAt int64   `json:"startedAt"`
	PeakRatio float64 `json:"peakRatio"`
	Status    string  `json:"status"`
}

// ServiceChartsSignals — endpoint'in anlatımın YANINDA döndürdüğü YAPISAL
// kanıt. Mockup'ın "İlişkili sinyaller" tablosu bunu çizer; model metni
// bozsa/kotayı doldursa bile tablo DOĞRU kalır (anlatım ile kanıt ayrı
// yollardan gelir — dürüstlük sözleşmesi).
type ServiceChartsSignals struct {
	Deploy    *ChartDeploySignal   `json:"deploy,omitempty"`
	Problems  []ChartProblemSignal `json:"problems,omitempty"`
	Anomalies []ChartAnomalySignal `json:"anomalies,omitempty"`
	OpDeltas  []OpDelta            `json:"opDeltas,omitempty"`
	OtherOps  int                  `json:"otherOps"`
}

// chartSeriesStat — bir serinin prompt'a giren sıkıştırılmış hâli. Nokta
// listesi token-ağır; modele şekil (min/max/ort/son) yeter.
type chartSeriesStat struct {
	Name    string  `json:"name"`
	Min     float64 `json:"min"`
	Max     float64 `json:"max"`
	Avg     float64 `json:"avg"`
	Current float64 `json:"current"`
}

// summarizeSeries — SpanMetricSeries[] → en hacimli topN serinin şekli.
// (v0.9.477 öncesi handler'ın içindeki kapanıştı; testlenebilsin diye
// paket düzeyine çıktı ve sıralaması O(n²) seçmeli sıralamadan
// sort.SliceStable'a geçti — determinist tie-break dahil.)
func summarizeSeries(rows []chstore.SpanMetricSeries, topN int) []chartSeriesStat {
	if topN <= 0 {
		return nil
	}
	type bag struct {
		name              string
		sum, mn, mx, last float64
		cnt               int
	}
	bags := make([]bag, 0, len(rows))
	for _, s := range rows {
		b := bag{name: strings.Join(s.GroupKey, " / "), mn: math_MaxFloat}
		for _, p := range s.Points {
			b.sum += p.Value
			b.cnt++
			if p.Value < b.mn {
				b.mn = p.Value
			}
			if p.Value > b.mx {
				b.mx = p.Value
			}
			b.last = p.Value
		}
		if b.cnt > 0 {
			bags = append(bags, b)
		}
	}
	sort.SliceStable(bags, func(i, j int) bool {
		if bags[i].sum != bags[j].sum {
			return bags[i].sum > bags[j].sum
		}
		return bags[i].name < bags[j].name
	})
	if len(bags) > topN {
		bags = bags[:topN]
	}
	out := make([]chartSeriesStat, 0, len(bags))
	for _, b := range bags {
		avg := 0.0
		if b.cnt > 0 {
			avg = b.sum / float64(b.cnt)
		}
		out = append(out, chartSeriesStat{
			Name: b.name, Min: b.mn, Max: b.mx, Avg: avg, Current: b.last,
		})
	}
	return out
}

// math.MaxFloat64 yerine yerel sabit: tek kullanım için math import'u
// açmaya değmez ve "başlangıç minimumu" niyeti burada okunur kalır.
const math_MaxFloat = 1e308

// serviceChartsInput — prompt kurucunun TÜM girdisi. Handler CH'den ne
// topladıysa buraya koyar; kurucu başka hiçbir yere bakmaz.
type serviceChartsInput struct {
	Service  string
	Scope    string
	From, To time.Time
	RPS      []chartSeriesStat
	ErrRate  []chartSeriesStat
	P99      []chartSeriesStat
	Signals  ServiceChartsSignals
}

// buildServiceChartsUser — sinyaller → modelin göreceği TEK kullanıcı
// mesajı. Model burada YAZILMAYAN hiçbir sayıyı uyduramasın diye
// bölümler açıkça etiketlenir; kapsam daraltıldığında ilgili grafiğin
// hangisi olduğu ilk satırda söylenir.
func buildServiceChartsUser(in serviceChartsInput) string {
	scope := normalizeChartScope(in.Scope)
	var b strings.Builder
	fmt.Fprintf(&b, "Servis: %s\n", in.Service)
	fmt.Fprintf(&b, "Pencere: %s → %s\n",
		in.From.UTC().Format(time.RFC3339), in.To.UTC().Format(time.RFC3339))
	fmt.Fprintf(&b, "Grafik kapsamı: %s\n", chartScopeLabel(scope))

	writeStats := func(title string, unit string, st []chartSeriesStat) {
		if len(st) == 0 {
			return
		}
		fmt.Fprintf(&b, "\n%s (operasyon bazlı, en hacimli %d):\n", title, len(st))
		for _, s := range st {
			fmt.Fprintf(&b, "  • %s: şimdi=%.2f%s ort=%.2f min=%.2f max=%.2f\n",
				s.Name, s.Current, unit, s.Avg, s.Min, s.Max)
		}
	}
	// Kapsam daraldığında DİĞER serileri prompt'tan tamamen çıkarmıyoruz:
	// "throughput sabit, yani davranış değişikliği" gibi çıkarımlar tam
	// da diğer iki grafiğe bakmayı gerektiriyor (mockup'ın ikinci
	// paragrafı). Kapsam, modele NEYE ODAKLANACAĞINI söyler; neyi
	// GÖRECEĞİNİ değil.
	writeStats("Throughput / RPS", " rps", in.RPS)
	writeStats("Hata oranı", "%", in.ErrRate)
	writeStats("P99 gecikme", " ms", in.P99)

	sg := in.Signals
	if sg.Deploy != nil {
		d := sg.Deploy
		verb := "rollout"
		if d.Kind == "deploy" {
			verb = "deploy"
		}
		fmt.Fprintf(&b, "\nDeploy/rollout: %s · %s · %d pod değişti",
			verb, time.Unix(0, d.TimeUnixNs).UTC().Format(time.RFC3339), d.PodsReplaced)
		if d.VersionAfter != "" {
			fmt.Fprintf(&b, " · sürüm %s→%s", orDash(d.VersionBefore), d.VersionAfter)
		}
		b.WriteString("\n")
	}
	if len(sg.Problems) > 0 {
		b.WriteString("\nAçık problemler:\n")
		for _, p := range sg.Problems {
			fmt.Fprintf(&b, "  • [%s] %s — %s: değer=%.2f eşik=%.2f (başlangıç %s)\n",
				strings.ToUpper(p.Severity), p.Title, p.Metric, p.Value, p.Threshold,
				time.Unix(0, p.StartedAt).UTC().Format(time.RFC3339))
		}
	}
	if len(sg.Anomalies) > 0 {
		b.WriteString("\nAnomaliler:\n")
		for _, a := range sg.Anomalies {
			fmt.Fprintf(&b, "  • [%s] %s — tepe oran %.1f× (%s)\n",
				a.Kind, a.Pattern, a.PeakRatio, a.Status)
		}
	}
	if len(sg.OpDeltas) > 0 {
		b.WriteString("\nOperasyon değişimi (pencere vs bir önceki eş pencere):\n")
		for _, d := range sg.OpDeltas {
			switch {
			case d.IsNew:
				fmt.Fprintf(&b, "  • %s: YENİ operasyon · %d çağrı\n", d.Name, d.Calls)
			default:
				fmt.Fprintf(&b, "  • %s: p95 %s · hata %+.2fpp · %d çağrı\n",
					d.Name, ratioText(d.P95Ratio), d.ErrDeltaPP, d.Calls)
			}
		}
		if sg.OtherOps > 0 {
			fmt.Fprintf(&b, "  • diğer %d operasyon: kayda değer değişim yok\n", sg.OtherOps)
		}
	} else if sg.OtherOps > 0 {
		fmt.Fprintf(&b, "\nOperasyon değişimi: %d operasyonun hiçbirinde kayda değer değişim yok.\n",
			sg.OtherOps)
	}
	return b.String()
}

// ratioText — 0 oranı "ölçülemedi" demektir, "0×" DEĞİL. Modelin
// "gecikme sıfıra düştü" diye anlatmasına yol açan tam olarak bu tür
// sessiz sıfırlardır.
func ratioText(r float64) string {
	if r <= 0 {
		return "oran ölçülemedi"
	}
	return fmt.Sprintf("%.2f×", r)
}

func orDash(s string) string {
	if s == "" {
		return "?"
	}
	return s
}

// ChartsExplainResult — /api/copilot/explain-charts yanıtı.
type ChartsExplainResult struct {
	Explanation string               `json:"explanation"`
	Scope       string               `json:"scope"`
	Signals     ServiceChartsSignals `json:"signals"`
	// Error — ANLATIM üretilemedi (kota/timeout/sağlayıcı hatası). Kanıt
	// yine döner; alan boşsa anlatım sağlıklıdır.
	Error string `json:"error,omitempty"`
}

// buildChartsResult — anlatım sonucu + ölçülmüş kanıt → yanıt.
//
// SÖZLEŞME: model çağrısı düşerse istek BAŞARISIZ OLMAZ. Sinyaller
// (deploy, problem, anomali, op-delta) CH'den deterministik toplandı ve
// zaten elimizde; anlatım koptu diye onları çöpe atmak, operatöre
// ölçülmüş veriyi göstermemek demektir. Çekmecenin "kanıt anlatımdan
// bağımsızdır" vaadi tam olarak burada tutulur — v0.9.482'nin
// soft-fail dersi (kanıt kurulamazsa anlatıma düş) bu yüzeyde
// TERSİNE çalışır: anlatım kurulamazsa kanıta düş.
//
// Yalnız copilot TAMAMEN kapalıyken 503 dönülür (handler'ın başındaki
// Active() kapısı) — o zaman gösterilecek bir yüzey de yoktur.
func buildChartsResult(scope string, sg ServiceChartsSignals, out string, nerr error) *ChartsExplainResult {
	res := &ChartsExplainResult{Scope: scope, Signals: sg}
	if nerr != nil {
		res.Error = nerr.Error()
		return res
	}
	res.Explanation = out
	return res
}
