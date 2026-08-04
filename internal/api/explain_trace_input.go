package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"time"

	"github.com/cilcenk/coremetry/internal/logstore"
)

// explain_trace_input.go — trace explain'in KANIT paketi kurucusu
// (v0.9.482, operatör raporu).
//
// Semptom (prod fotoğrafı, v0.9.479 takibi): "Explain trace" hem trace'i
// hem ilişkili LOGLARI okuyup cevaplıyor; ama "Chat'te devam et"ten sonra
// çekmece sohbeti yalnız açıklamanın METNİNİ görüyordu. "logda ne yazıyor",
// "BKMQR-39 neden" gibi takipler ham kanıta erişemediği için kör cevap
// alıyordu.
//
// Çözüm: kanıt montajı handler'dan çıkarıldı; AYNI kurucu hem
// copilotExplainTrace hem copilotChatDrawer tarafından kullanılır — emsal
// anomaly.BuildExceptionExplainInput (v0.9.415, exception hattında aynı
// "tek kurucu, iki çağıran" kararı).
//
// Sözleşme: handler'ın ürettiği prompt BAYT-BAYT eskisiyle aynıdır
// (explain_trace_input_test.go bunu pinler) — bu bir davranış değişikliği
// değil, çıkarma işlemidir.

// errExplainTraceNotFound — trace'in span'i yok. Handler 404'e çevirir;
// çekmece yolunda sessizce kanıtsız devam edilir (soft-fail).
var errExplainTraceNotFound = errors.New("trace not found")

// traceLite — modele giden kompakt span. Tam attribute haritaları büyük
// trace'lerde prompt'u patlatır; kıdemli bir mühendisin waterfall'da
// bakacağı alanlar kalır. (v0.9.462'de handler içindeki anonim `lite`
// tipiydi; v0.9.482'de paket düzeyine çıktı — kanıt seçimi saf ve
// tablo-testli olabilsin.)
type traceLite struct {
	Name       string  `json:"name"`
	Service    string  `json:"service"`
	Kind       string  `json:"kind"`
	ParentSpan string  `json:"parent,omitempty"`
	SpanID     string  `json:"id"`
	DurationMs float64 `json:"durMs"`
	Status     string  `json:"status,omitempty"`
	StatusMsg  string  `json:"statusMsg,omitempty"`
}

// traceExplainInput — kurulan girdi + deterministik kanıt.
type traceExplainInput struct {
	User     string   // explain/narration user prompt'unun gövdesi
	Evidence []string // kanıt span id'leri (waterfall kutulaması)
	// LogsBlock — User'ın İÇİNDEKİ log bölümü, ayrıca taşınır: çekmece
	// sohbetinde bütçe aşılırsa span listesi budanır, LOGLAR KORUNUR
	// (operatörün takip soruları log içeriğine dair — clampDrawerEvidence).
	LogsBlock string
}

// buildTraceExplainInput — trace'in span'lerini çekip kompakt JSON'a
// indirger, ilişkili logları (varsa) ekler ve deterministik kanıt
// span'lerini hesaplar. Log sorgusu SADECE bu çağrı içinde, TEK pass;
// hata/boşluk durumunda sessizce trace-only'e düşer (v0.9.166 maliyet
// disiplini: proaktif/poll YOK).
func (s *Server) buildTraceExplainInput(ctx context.Context, id string) (traceExplainInput, error) {
	// v0.9.632 — operator-reported: Tempo fallback trace'i bulup
	// waterfall'ı çizerken explain "trace not found" diyordu. Burası
	// doğrudan s.store.GetTrace çağırıyordu; /api/traces/{id} ise CH
	// ıskalayınca Tempo'ya düşüyordu. Tek kural, tek yer:
	// resolveTraceSpans (trace_resolve.go).
	spans, _, err := s.resolveTraceSpans(ctx, id)
	if err != nil {
		return traceExplainInput{}, err
	}
	if len(spans) == 0 {
		return traceExplainInput{}, errExplainTraceNotFound
	}
	// Trace penceresi (log sorgusu için) — cap'ten ÖNCE, tüm span'lar üstünden.
	minT, maxT := spans[0].StartTime, spans[0].EndTime
	for _, sp := range spans {
		if sp.StartTime < minT {
			minT = sp.StartTime
		}
		if sp.EndTime > maxT {
			maxT = sp.EndTime
		}
	}
	// v0.9.462 (dürüstlük A6) — prompt tavanı 100 span'de kalır ama seçim
	// artık HEAD dilimi değil: kronolojik ilk-100, hatalı ve en yavaş
	// span'lar 100'ün dışındaysa modele HİÇ ulaşmıyordu — "en yavaş span"
	// kanıtı waterfall'da yanlış span'ı kutulayabiliyordu. pickExplainSpans
	// hataları + en yavaşları garantiler, kalanı kronolojik doldurur ve
	// seçimi zaman sırasına geri koyar (model akışı sırayla okur).
	totalSpans := len(spans)
	spans = pickExplainSpans(spans, 100)
	compact := make([]traceLite, 0, len(spans))
	for _, sp := range spans {
		dur := float64(sp.EndTime-sp.StartTime) / 1e6
		l := traceLite{Name: sp.Name, Service: sp.ServiceName, Kind: sp.Kind,
			ParentSpan: sp.ParentSpanID, SpanID: sp.SpanID, DurationMs: dur}
		if sp.StatusCode == "error" {
			l.Status = "error"
			l.StatusMsg = sp.StatusMessage
		}
		compact = append(compact, l)
	}
	payload, _ := json.Marshal(compact)

	// Trace'in LOGLARI — SADECE kullanıcı bu explain'i tetiklediğinde, log
	// store'a TEK sorgu (poll/proaktif YOK; operatör isteği v0.9.166).
	// Elastic loglarında stacktrace'ler span event'lerinden zengin
	// olabildiğinden trace + logları BİRLİKTE yorumlatırız. Pencere trace
	// span'larından ±1dk, bounded limit 30, hata-öncelikli, gövde/stack
	// truncate'li (2B prompt bütçesi). Log store yok/yavaş/boşsa sessizce
	// trace-only'e düşer — explain'i asla düşürmez.
	var logsBlock string
	if s.logs != nil {
		from := time.Unix(0, minT).Add(-time.Minute)
		to := time.Unix(0, maxT).Add(time.Minute)
		lctx, cancel := context.WithTimeout(ctx, 6*time.Second)
		if page, lerr := logstore.LogsForTrace(lctx, s.logs, id, from, to, 30); lerr == nil && page != nil && len(page.Logs) > 0 {
			type liteLog struct {
				Sev    string `json:"sev,omitempty"`
				Svc    string `json:"svc,omitempty"`
				ExType string `json:"exType,omitempty"`
				Stack  string `json:"stack,omitempty"`
				Body   string `json:"body,omitempty"`
			}
			logs := page.Logs
			sort.SliceStable(logs, func(i, j int) bool { return logs[i].Severity > logs[j].Severity })
			ll := make([]liteLog, 0, 15)
			for _, lg := range logs {
				if len(ll) >= 15 {
					break
				}
				e := liteLog{Sev: lg.SeverityText, Svc: lg.ServiceName, Body: truncate(lg.Body, 600)}
				if lg.Attributes != nil {
					e.ExType = lg.Attributes["exception.type"]
					e.Stack = truncate(lg.Attributes["exception.stacktrace"], 900)
				}
				ll = append(ll, e)
			}
			if lp, e := json.Marshal(ll); e == nil {
				logsBlock = fmt.Sprintf("\n\nBu trace'in ilişkili LOGLARI (log store; stacktrace burada span event'lerinden zengin olabilir), yüksek severity önce:\n```json\n%s\n```\n\nTrace waterfall'ı VE logları BİRLİKTE yorumla — hata/stacktrace varsa kök nedeni logdaki stacktrace + exception.type'a dayandır ve ilgili span'ın StatusMsg'ıyla eşleştir.", string(lp))
			}
		}
		cancel()
	}

	return traceExplainInput{
		User:      traceExplainUser(id, len(compact), totalSpans, string(payload), logsBlock),
		Evidence:  traceEvidenceSpanIDs(compact),
		LogsBlock: logsBlock,
	}, nil
}

// traceEvidenceSpanIDs — v0.9.408 (operatör: "kök neden kısımları
// kutulanmıyor") kanıt sözleşmesi: kanıt span'leri DETERMİNİSTİK, LLM
// çıktısını parse etmek yerine modele beslediğimiz AYNI veriden
// hesaplanır (gemma4 güvenilirliğinden bağımsız). Hata span'leri (≤5) +
// en yavaş span; UI bunları waterfall'da kutular. Saf; tablo-testli.
func traceEvidenceSpanIDs(compact []traceLite) []string {
	evidence := make([]string, 0, 6)
	slowestID, slowestDur := "", float64(-1)
	for _, c := range compact {
		if c.Status == "error" && len(evidence) < 5 {
			evidence = append(evidence, c.SpanID)
		}
		if c.DurationMs > slowestDur {
			slowestDur, slowestID = c.DurationMs, c.SpanID
		}
	}
	if slowestID != "" {
		dup := false
		for _, e := range evidence {
			if e == slowestID {
				dup = true
				break
			}
		}
		if !dup {
			evidence = append(evidence, slowestID)
		}
	}
	return evidence
}

// traceExplainUser — kanıt paketinin SON montajı. v0.9.482 çıkarmasında
// bu ifade bayt-bayt korunmalıydı (iki çağıran: explain handler'ı ve
// çekmece sohbeti) — golden testi bunu pinler.
//
// analyzed < total ise dürüstlük notu eklenir: hem modele hem operatöre
// aynı gerçek — analiz kısmi ama seçim hata+yavaşlık öncelikli, "head"
// değil (v0.9.462).
func traceExplainUser(id string, analyzed, total int, payload, logsBlock string) string {
	analyzedNote := ""
	if total > analyzed {
		analyzedNote = fmt.Sprintf(" (trace'in tamamı %d span; hatalar + en yavaşlar öncelikli %d span analiz edildi)", total, analyzed)
	}
	return fmt.Sprintf("Trace %s with %d spans%s:\n```json\n%s\n```%s", id, analyzed, analyzedNote, payload, logsBlock)
}
