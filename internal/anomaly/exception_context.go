package anomaly

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/cilcenk/coremetry/internal/chstore"
	"github.com/cilcenk/coremetry/internal/promptfmt"
	"github.com/cilcenk/coremetry/internal/logstore"
	"github.com/cilcenk/coremetry/internal/stackparse"
)

// exception_context.go — exception kök-sebep girdi kurucusu (v0.9.415).
// v0.9.414'te copilot_exception.go içinde doğan prefetch buraya taşındı:
// operatör-tıklı explain (internal/api) ile proaktif ExceptionExplainer
// işçisi AYNI girdiyi kurar — iki kopya sürüklenmez.
//
// Sözleşme (v0.9.414'ten aynen): tüm zenginleştirmeler best-effort;
// kanıt trace/span'leri LLM'den bağımsız, girdiye giren veriden
// deterministik. Saf montaj (assembleExceptionPrompt) ve deploy seçimi
// (PickDeploysAroundStart) tablo-testli — exception_context_test.go.

// ExceptionExplainInput — kurulan girdi + deterministik kanıt.
// pickExceptionStack — v0.9.1225 saf çekirdek: prompt kopyası (1800
// kırpık, User bayt-parite) ile kod-çekici istihkakı (HAM) ayrımı +
// log-fallback kararı. Örneklerden ilk dolu stack kazanır (ham döner —
// "kesik bir satır konumlandırılamaz", v0.9.831 gerekçesi); hiçbiri
// yoksa loglardan yakalanan stack ve onu atan servis döner (v0.9.1182'nin
// exception yüzeyindeki eksik yarısı — prompt logdaki stack'i gösterip
// kod çekici "stacktrace yok" diyordu). Fallback'te forPrompt boş kalır:
// prompt'un stack bölümü eskiden de boştu, bayt-parite korunur.
func pickExceptionStack(sampleStacks []string, logStack, logStackSvc string) (forPrompt, raw, svc string) {
	for _, st := range sampleStacks {
		if st != "" {
			return truncRunes(st, 1800), st, ""
		}
	}
	if logStack != "" {
		return "", logStack, logStackSvc
	}
	return "", "", ""
}

// liteLog — prompt'a giren log satırı. Paket düzeyinde: Stack alanı
// artık döngüde değil, temsilî stack seçildikten SONRA dolduruluyor
// (v0.9.1239) ve tip iki yerden görünmek zorunda.
type liteLog struct {
	Sev    string `json:"sev,omitempty"`
	Svc    string `json:"svc,omitempty"`
	ExType string `json:"exType,omitempty"`
	Stack  string `json:"stack,omitempty"`
	Body   string `json:"body,omitempty"`
}

// dupStackRef — tekrar eden stack'in yerine geçen tek satır.
const dupStackRef = "(stack yukarıdakiyle aynı)"

// foldDuplicateLogStacks — aynı stack'in prompt'taki KOPYALARINI tek
// satırlık referansa katlar (v0.9.1239). Saf; dönen dilim girdiyle
// aynı uzunluktadır.
//
// # Neden
//
// Bir exception grubunun örnek trace'inde 12 loga kadar satır var ve
// retry fırtınasında hepsi AYNI exception'ın stack'ini taşıyor. Temsilî
// stack (1800 rune) + 12×900 rune = tek metnin 13 kopyası, ~12k rune.
// Taşma yeniden-denemesi (copilot_code.go) yalnız KOD bloğunu yarıya
// indiriyor: benzersiz kanıt küçülürken tekrar aynen kalıyordu.
// Öncelik doktrini: kod + taze kanıt > tekrarlanan stack.
//
// # İki tuzak
//
//  1. primary, prompt'ta GÖRÜNEN stack olmalı (stackForPrompt), ham
//     olan değil. Örnekler stack taşımıyorsa temsilî bölüm BOŞ basılır
//     ve stack yalnız logda vardır; ham stack'e karşı katlamak
//     prompt'taki TEK stack'i silerdi.
//  2. Kırpma farkı: temsilî kopya 1800, log kopyası 900 rune. Birebir
//     eşitlik aramak hiçbir şeyi katlamazdı — kısa olan, uzun olanın
//     ÖN EKİ ise aynı metindir.
func foldDuplicateLogStacks(stacks []string, primary string) []string {
	out := make([]string, len(stacks))
	// seen — prompt'ta TAM hâliyle görünen stack'ler; sonraki kopyalar
	// bunlardan herhangi birine katlanır ("yukarıdaki" hepsini kapsar).
	var seen []string
	if primary != "" {
		seen = append(seen, primary)
	}
	for i, st := range stacks {
		if st == "" {
			continue
		}
		dup := false
		for _, s := range seen {
			if sameStackText(st, s) {
				dup = true
				break
			}
		}
		if dup {
			out[i] = dupStackRef
			continue
		}
		out[i] = truncRunes(st, 900)
		seen = append(seen, st)
	}
	return out
}

// sameStackText — iki stack metni AYNI stack'in kopyası mı? Saf.
//
// Kırpılmış kopyalar için önek karşılaştırması yapılır; kısa olan
// metinlerde önek eşleşmesi yanlış katlama üretebileceği için
// (ör. iki farklı exception'ın ortak ilk satırı) minimum uzunluk
// altında BİREBİR eşitlik aranır.
func sameStackText(a, b string) bool {
	x, y := normStackText(a), normStackText(b)
	if x == "" || y == "" {
		return false
	}
	if x == y {
		return true
	}
	if len(x) > len(y) {
		x, y = y, x
	}
	// dupStackMinPrefix — önek eşleşmesinin geçerli sayıldığı en kısa
	// metin. Bir Java stack'inin ilk satırı + iki frame'i rahat aşar;
	// altında kalan metinler için kesme zaten olmamıştır.
	const dupStackMinPrefix = 120
	if len([]rune(x)) < dupStackMinPrefix {
		return false
	}
	return strings.HasPrefix(y, x)
}

// normStackText — karşılaştırma için normalleştirme: satır sonları,
// baş/son boşluk ve truncRunes'un eklediği "…" eki.
func normStackText(s string) string {
	s = strings.ReplaceAll(s, "\r\n", "\n")
	s = strings.TrimSpace(s)
	return strings.TrimSuffix(s, "…")
}

type ExceptionExplainInput struct {
	User     string   // narration user prompt'u
	EvTraces []string // kanıt trace id'leri (örnek tablosu kutulaması)
	EvSpans  []string // kanıt span id'leri
	// LogsBlock — User'ın İÇİNDEKİ log bölümü, ayrıca taşınır (v0.9.482).
	// AI çekmecesi sohbeti bu paketi narration bütçesine sığdırırken önce
	// span/trace listesini budar, LOGLARI KORUR — operatörün takip
	// soruları ("logda ne yazıyor") log içeriğine dairdir. Explain
	// yolunda kullanılmaz; User bayt-bayt eskisidir.
	LogsBlock string
	// Stack — User'ın İÇİNDEKİ temsilî stacktrace, ayrıca ham olarak
	// taşınır (v0.9.831). "Kodu da incele" yolu bunu stackparse'a
	// verip kaynak penceresi çeker.
	//
	// Yeniden okumak yerine taşınıyor: örnek seçimi (ilk stack'li
	// örnek) burada yapılıyor ve iki yerde tekrarlanırsa model bir
	// stack'i, kod çekici BAŞKA bir stack'i görebilir — sessizce
	// yanlış dosya. User bayt-bayt eskisidir. v0.9.1225'ten beri HAM
	// (kırpıksız) taşınır — prompt kendi 1800'lük kopyasını ayrı alır.
	Stack string
	// StackService — Stack log-fallback'ten geldiyse logu atan servis
	// (v0.9.1225): depo çözümü grubun servisi yerine buna gitmeli.
	// Boşsa grup servisi geçerli.
	StackService string
	// DBStatements / ErrorText (v0.10.115) — örnek trace'in hata
	// span'larındaki SQL ifadeleri (≤3) ve grup tipi+mesajı+stack başı:
	// şema kanıtının girdisi (api buildSchemaEvidence).
	DBStatements []string
	ErrorText    string

	// ── v0.9.1129 (AI Faz 2.1) — insight kartının YAPISAL yarısı ──
	//
	// Kart, prose'dan bağımsız deterministik sinyaller çiziyor
	// (internal/ai/insight). O sinyaller prompt'un GÖRDÜĞÜ veriden
	// türemek ZORUNDA: ikinci bir okuma, kartta "deploy v5" yazarken
	// modelin v4'ten bahsettiği bir hâl üretebilir. Stack/LogsBlock
	// alanlarının gerekçesiyle aynı — burada seçim yapılıyor, o yüzden
	// seçilen şey ham olarak da taşınıyor.
	//
	// Hepsi opsiyonel: veri yoksa nil/boş, kart o satırı çizmez.
	TraceID string          // örnek hata trace'i (kartın "Örnek trace" çipi)
	Trend   *ExceptionTrend // occurrence özeti (User içindeki trend satırının kaynağı)
	Deploys []NearbyDeploy  // deployBlock'un YAPISAL hâli (yön bayraklı)
}

// ExceptionTrend — occurrence serisinin sıkıştırılmış hâli (v0.9.1129).
// Eskiden yalnız prompt satırı olarak vardı; kart aynı sayıları
// göstereceği için sayılar tek yerde hesaplanıp iki yere veriliyor.
type ExceptionTrend struct {
	Total    uint64
	Last24   uint64
	Peak     uint64
	PeakAtNs int64
	Buckets  int
}

// SummarizeExceptionOccurrences — occurrence noktalarından trend özeti.
// SAF (nowNs parametre — time.Now() okuyan bir özetleyici test
// edilemez). Tablo-testli.
func SummarizeExceptionOccurrences(occ []chstore.OccurrencePoint, nowNs int64) ExceptionTrend {
	t := ExceptionTrend{Buckets: len(occ)}
	cut := nowNs - int64(24*time.Hour)
	for _, p := range occ {
		t.Total += p.Count
		if p.Time >= cut {
			t.Last24 += p.Count
		}
		if p.Count > t.Peak {
			t.Peak, t.PeakAtNs = p.Count, p.Time
		}
	}
	return t
}

// PromptLine — trendin prompt'a giren satırı. v0.9.1129 öncesindeki
// fmt.Sprintf ile BAYT BAYT aynı: refactor prompt'u değiştirmemeli,
// yoksa altın-örnek testleri ve modelin okuduğu şekil birlikte kayar.
func (t ExceptionTrend) PromptLine() string {
	return fmt.Sprintf("toplam=%d son24h=%d tepe=%d@%s bucket=%d",
		t.Total, t.Last24, t.Peak,
		time.Unix(0, t.PeakAtNs).UTC().Format("2006-01-02 15:04"), t.Buckets)
}

// BuildExceptionExplainInput — grup meta + occurrence trendi + temsilî
// stacktrace + en yeni örneğin TAM trace'i + o trace'in logları +
// FirstSeen-merkezli deploy penceresi. logs nil olabilir (CH-only
// kurulum ya da işçi bağlamı) — log bloğu atlanır.
func BuildExceptionExplainInput(ctx context.Context, store *chstore.Store, logs logstore.Store, g *chstore.ExceptionGroup) ExceptionExplainInput {
	sres, _ := store.GetExceptionGroupSamples(ctx, g.Fingerprint, 5)
	samples := sres.Samples

	trend := ""
	var trendRef *ExceptionTrend
	if occ, oerr := store.GetExceptionOccurrences(ctx, g.Fingerprint); oerr == nil && len(occ) > 0 {
		t := SummarizeExceptionOccurrences(occ, time.Now().UnixNano())
		trend, trendRef = t.PromptLine(), &t
	}

	// En yeni trace'li örnek → tam trace + kanıt (error span'ler GARANTİLİ
	// — v0.9.414 verify bulgusu: düz head-cap derindeki hatayı düşürüyordu).
	type liteSpan struct {
		Name       string  `json:"name"`
		Service    string  `json:"service"`
		Kind       string  `json:"kind"`
		ParentSpan string  `json:"parent,omitempty"`
		SpanID     string  `json:"id"`
		DurationMs float64 `json:"durMs"`
		Status     string  `json:"status,omitempty"`
		StatusMsg  string  `json:"statusMsg,omitempty"`
		// v0.10.115 — yalnız hata span'larında: SQL hatasında çalışan ifade.
		DBSystem    string `json:"dbSystem,omitempty"`
		DBStatement string `json:"dbStatement,omitempty"`
	}
	var traceBlock, logsBlock string
	var dbStmts []string
	seenStmt := map[string]bool{}
	// v0.9.1225 — kod çekicinin log-fallback istihkakı (aşağıdaki logs
	// döngüsünde dolar; yalnız örnekler stack taşımıyorsa kullanılır).
	var logStack, logStackSvc string
	// v0.9.1239 — log satırları ve HAM stack'leri; JSON'a çevirme
	// pickExceptionStack'ten SONRAYA ertelendi (bkz. foldDuplicateLogStacks).
	var logLines []liteLog
	var logStacks []string
	var evTraces, evSpans []string
	traceID := ""
	var traceMinT, traceMaxT int64
	for _, sm := range samples {
		if sm.TraceID != "" {
			traceID = sm.TraceID
			break
		}
	}
	if traceID != "" {
		tctx, cancel := context.WithTimeout(ctx, 8*time.Second)
		spans, terr := store.GetTrace(tctx, traceID)
		cancel()
		if terr == nil && len(spans) > 0 {
			evTraces = append(evTraces, traceID)
			traceMinT, traceMaxT = spans[0].StartTime, spans[0].EndTime
			for _, sp := range spans {
				if sp.StartTime < traceMinT {
					traceMinT = sp.StartTime
				}
				if sp.EndTime > traceMaxT {
					traceMaxT = sp.EndTime
				}
			}
			include := make([]bool, len(spans))
			kept, errKept := 0, 0
			for i, sp := range spans {
				if sp.StatusCode == "error" && errKept < 20 {
					include[i] = true
					kept++
					errKept++
				}
			}
			for i := range spans {
				if kept >= 60 {
					break
				}
				if !include[i] {
					include[i] = true
					kept++
				}
			}
			compact := make([]liteSpan, 0, kept)
			for i, sp := range spans {
				if !include[i] {
					continue
				}
				l := liteSpan{Name: sp.Name, Service: sp.ServiceName, Kind: sp.Kind,
					ParentSpan: sp.ParentSpanID, SpanID: sp.SpanID,
					DurationMs: float64(sp.EndTime-sp.StartTime) / 1e6}
				if sp.StatusCode == "error" {
					l.Status = "error"
					l.StatusMsg = sp.StatusMessage
					if len(evSpans) < 5 {
						evSpans = append(evSpans, sp.SpanID)
					}
					if sp.DBStatement != "" {
						l.DBSystem = sp.DBSystem
						l.DBStatement = truncRunes(sp.DBStatement, 600)
						if len(dbStmts) < 3 && !seenStmt[l.DBStatement] {
							seenStmt[l.DBStatement] = true
							dbStmts = append(dbStmts, l.DBStatement)
						}
					}
				}
				compact = append(compact, l)
			}
			if tp, e := json.Marshal(compact); e == nil {
				traceBlock = fmt.Sprintf("\n\nÖrnek hata TRACE'i (%s, %d span):\n```json\n%s\n```",
					traceID, len(compact), string(tp))
			}
		}
	}

	// Trace'in logları — tek pivot sorgusu; trace yüklenemediyse (1970
	// penceresi) HİÇ gitme (v0.9.414 verify bulgusu, ES maliyet disiplini).
	if logs != nil && traceID != "" && traceMaxT > 0 {
		from := time.Unix(0, traceMinT).Add(-time.Minute)
		to := time.Unix(0, traceMaxT).Add(time.Minute)
		lctx, cancel := context.WithTimeout(ctx, 6*time.Second)
		if page, lerr := logstore.LogsForTrace(lctx, logs, traceID, from, to, 30); lerr == nil && page != nil && len(page.Logs) > 0 {
			lgs := page.Logs
			sort.SliceStable(lgs, func(i, j int) bool { return lgs[i].Severity > lgs[j].Severity })
			ll := make([]liteLog, 0, 12)
			for _, lg := range lgs {
				if len(ll) >= 12 {
					break
				}
				// v0.9.1182 — kardeş yolla AYNI çözücü. Burası da tek yazıma
				// (`exception.stacktrace`) bakıyordu; ECS kurulumlarında alan
				// `error.stack_trace` ve Java'nın yaygın deseninde stack
				// gövdenin içinde. Trace-explain tarafını düzeltip burayı
				// bırakmak, aynı bug'ın bilinen bir kopyasını bilerek yerinde
				// bırakmak olurdu.
				stackText, stackFromBody := stackparse.FromLog(lg.Attributes, lg.Body)
				// v0.9.1225 — kardeş yolun (explain_trace_input.go rawStack/
				// stackService) eksik yarısı: span-örnekleri stack taşımıyorsa
				// kod çekicinin istihkakı LOGLARDAN gelir. Servis de birlikte
				// taşınır — logu atan servis g.Service'ten farklı olabilir ve
				// bsa- depo çözümü yanlış depoya gitmesin.
				if logStack == "" && stackText != "" {
					logStack, logStackSvc = stackText, lg.ServiceName
				}
				bodyForPrompt := lg.Body
				if stackFromBody {
					bodyForPrompt = stackparse.MessageHead(lg.Body)
				}
				e := liteLog{Sev: lg.SeverityText, Svc: lg.ServiceName, Body: truncRunes(bodyForPrompt, 500)}
				e.ExType = lg.Attributes["exception.type"] // nil map okuması güvenli
				// Stack HAM biriktirilir; kırpma + tekrar katlaması
				// temsilî stack seçildikten SONRA yapılır.
				ll = append(ll, e)
				logStacks = append(logStacks, stackText)
			}
			logLines = ll
		}
		cancel()
	}

	// Deploy penceresi — FirstSeen'e YAKINLIĞA göre seçim + önce/sonra
	// açık etiket (v0.9.414 verify bulguları).
	deployBlock := ""
	var nearby []NearbyDeploy
	if g.Service != "" {
		dFrom := time.Unix(0, g.FirstSeen).Add(-6 * time.Hour)
		dTo := time.Unix(0, g.LastSeen)
		if deps, derr := store.GetServiceDeploys(ctx, g.Service, dFrom, dTo); derr == nil && len(deps) > 0 {
			// TEK seçim, iki gösterim: prompt satırları ve kartın yapısal
			// adayları AYNI listeden türer (v0.9.1129).
			nearby = PickDeploysAroundStartRefs(deps, g.FirstSeen)
			if parts := renderNearbyDeploys(nearby); len(parts) > 0 {
				deployBlock = "\n\nAynı servisin yakın DEPLOY'ları: " + fmt.Sprintf("%v", parts) +
					"\nGrubun başlangıcı bir deploy'un hemen SONRASINA denk geliyorsa o deploy'u kök neden adayı olarak öne al."
			}
		}
	}

	// v0.9.1225 — prompt kopyası ile kod-çekici istihkakı AYRILDI. User
	// bayt-bayt eski (1800 rune'luk kırpık); Stack ise HAM taşınır —
	// kardeş yol explain_trace_input.go v0.9.831'de aynı gerekçeyle
	// ("kesik bir satır konumlandırılamaz") ham taşıyordu, burası kırpığı
	// veriyordu: derin JBoss stack'lerinde Caused-by uygulama frame'leri
	// 1800'ün altında kalıp pencereleme hiç isabet etmiyordu.
	sampleStacks := make([]string, 0, len(samples))
	for _, sm := range samples {
		sampleStacks = append(sampleStacks, sm.Stacktrace)
	}
	stackForPrompt, stackRaw, stackSvc := pickExceptionStack(sampleStacks, logStack, logStackSvc)

	// v0.9.1239 — log bloğu ANCAK ŞİMDİ kurulabilir: her logun stack'i
	// prompt'ta GÖRÜNEN temsilî stack'e karşı katlanıyor ve o seçim
	// (pickExceptionStack) log döngüsünün kendi çıktısına bağlı.
	if len(logLines) > 0 {
		folded := foldDuplicateLogStacks(logStacks, stackForPrompt)
		for i := range logLines {
			if i < len(folded) {
				logLines[i].Stack = folded[i]
			}
		}
		if lp, e := json.Marshal(logLines); e == nil {
			logsBlock = fmt.Sprintf("\n\nBu trace'in ilişkili LOGLARI (yüksek severity önce):\n```json\n%s\n```", string(lp))
		}
	}

	return ExceptionExplainInput{
		User:         assembleExceptionPrompt(g, trend, stackForPrompt, traceBlock, logsBlock, deployBlock),
		EvTraces:     evTraces,
		EvSpans:      evSpans,
		LogsBlock:    logsBlock,
		Stack:        stackRaw,
		StackService: stackSvc,
		DBStatements: dbStmts,
		ErrorText:    truncRunes(g.Type+": "+g.Message+"\n"+stackRaw, 2000),
		TraceID:      traceID,
		Trend:        trendRef,
		Deploys:      nearby,
	}
}

// NearbyDeploy — grubun başlangıcı çevresinde SEÇİLMİŞ deploy
// (v0.9.1129). OffsetSec MUTLAK uzaklık; yön ayrı bayrak.
//
// Yönü işaretle kodlamak yanlış olurdu: sub-saniyelik bir "sonra"
// deploy'u ns→sn kırpmasında 0'a düşer ve 0'ın işareti YOK. Seçimi
// yapan yer yönü BİLİYOR — tahmine bırakmıyoruz.
// (insight.DeployCandidate aynı şekil; dönüşüm internal/api'de.)
type NearbyDeploy struct {
	Version   string
	OffsetSec int64
	After     bool // true = grubun başlangıcından SONRA → kök neden OLAMAZ
}

// PickDeploysAroundStart — GetServiceDeploys ASC döner; FirstSeen
// ÖNCESİNDEN son 3 + SONRASINDAN ilk 2 seçilir ve yön açıkça yazılır.
// Saf — exception_context_test.go: düz "son 5" kesimi uzun ömürlü
// gruplarda asıl adayı (başlangıçtan hemen önceki deploy) düşürüyordu,
// negatif "önce" ise LLM'e yanlış kanıt oluyordu (v0.9.414 bulguları).
//
// v0.9.1129: seçim PickDeploysAroundStartRefs'e, biçimleme
// renderNearbyDeploys'a ayrıldı; bu sarmalayıcı prompt satırlarını
// BAYT BAYT eskisi gibi üretir (mevcut testler tam metni pinliyor).
func PickDeploysAroundStart(deps []chstore.Deploy, firstSeen int64) []string {
	return renderNearbyDeploys(PickDeploysAroundStartRefs(deps, firstSeen))
}

// PickDeploysAroundStartRefs — seçim yarısı: yön + mutlak uzaklık.
func PickDeploysAroundStartRefs(deps []chstore.Deploy, firstSeen int64) []NearbyDeploy {
	split := len(deps)
	for i, d := range deps {
		if d.TimeUnixNs > firstSeen {
			split = i
			break
		}
	}
	before, after := deps[:split], deps[split:]
	if len(before) > 3 {
		before = before[len(before)-3:]
	}
	if len(after) > 2 {
		after = after[:2]
	}
	out := make([]NearbyDeploy, 0, len(before)+len(after))
	for _, d := range before {
		out = append(out, NearbyDeploy{
			Version: d.Version, OffsetSec: (firstSeen - d.TimeUnixNs) / int64(time.Second)})
	}
	for _, d := range after {
		out = append(out, NearbyDeploy{
			Version: d.Version, OffsetSec: (d.TimeUnixNs - firstSeen) / int64(time.Second),
			After: true})
	}
	return out
}

// renderNearbyDeploys — biçimleme yarısı. Dakika kırpması iç içe taban
// bölmesi (sn → dk) ile eski tek-adım (ns → dk) bölmesiyle AYNI sonucu
// verir; exception_context_test.go tam metinleri pinliyor.
func renderNearbyDeploys(nd []NearbyDeploy) []string {
	parts := make([]string, 0, len(nd))
	for _, d := range nd {
		mins := d.OffsetSec / 60
		if d.After {
			parts = append(parts, fmt.Sprintf("%s (grubun başlangıcından %d dk SONRA — kök neden OLAMAZ, olsa olsa etki/çözüm denemesi)", d.Version, mins))
			continue
		}
		parts = append(parts, fmt.Sprintf("%s (grubun başlangıcından %d dk ÖNCE)", d.Version, mins))
	}
	return parts
}

// assembleExceptionPrompt — saf montaj; boş bloklar atlanır.
func assembleExceptionPrompt(g *chstore.ExceptionGroup, trend, stack, traceBlock, logsBlock, deployBlock string) string {
	meta := map[string]any{
		"type": g.Type, "message": truncRunes(g.Message, 400), "service": g.Service,
		"state": g.State, "occurrences": g.Occurrences,
		"firstSeen": time.Unix(0, g.FirstSeen).UTC().Format(time.RFC3339),
		"lastSeen":  time.Unix(0, g.LastSeen).UTC().Format(time.RFC3339),
	}
	mp, _ := json.Marshal(meta)
	var sb strings.Builder
	fmt.Fprintf(&sb, "Exception GRUBU:\n```json\n%s\n```", promptfmt.FenceSafe(string(mp))) // v0.10.404 — çit kaçışı
	if trend != "" {
		sb.WriteString("\n\nOccurrence trendi: " + trend)
	}
	if stack != "" {
		sb.WriteString("\n\nTemsilî STACKTRACE:\n```\n" + promptfmt.FenceSafe(stack) + "\n```")
	}
	sb.WriteString(traceBlock)
	sb.WriteString(logsBlock)
	sb.WriteString(deployBlock)
	sb.WriteString("\n\nStacktrace + trace + logları BİRLİKTE yorumla: kök nedeni stack'in EN DERİN \"Caused by\" bölümündeki ilk uygulama-frame'ine (yoksa en üst uygulama-frame'ine) ve trace'te hatanın DOĞDUĞU (en derin error) span'a dayandır; yayılan (propagate) hataları kök sanma.")
	return sb.String()
}

// truncRunes — rune-güvenli kesme (api.truncate byte keserdi; Türkçe
// çok-baytlı mesajlarda U+FFFD üretiyordu — v0.9.414 verify notu).
func truncRunes(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…"
}
