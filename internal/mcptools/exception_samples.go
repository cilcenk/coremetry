package mcptools

// exception_samples.go — get_exception_samples (v0.9.1233): istisna
// zincirinin ÇIKMAZINI kapatır.
//
// list_exception_groups (v0.9.1091) grupları veriyor ama ExceptionGroup
// satırında NE stacktrace NE trace id var (chstore/exception_inbox.go):
// model "bu serviste hangi hatalar patlıyor"a cevap buluyor, "NEDEN" ve
// "hangi trace" sorularında duruyordu — get_problem_root_cause bile
// istisnaları tip/mesaj/sayıya indiriyor, explain_exception prompt'u ise
// hiçbir tool'un dolduramadığı bir `stacktrace` arg'ı ilan ediyordu.
//
// Bu tool UI'daki karşılığını köprüler: grubun satır-içi açılımı
// (GET /api/exception-groups/{fp}/samples → Store.GetExceptionGroupSamples).
// (service, exception.type) adayları grubun KENDİ penceresinde partili
// taranır ve fingerprint satır başına Go'da yeniden hesaplanır. Aynı
// okuma; YENİ SQL YOK.
//
// PENCERE DÜRÜSTLÜĞÜ (Faz 3.2 doktrini: kabul edilip yok sayılan arg
// YASAK). Tarama penceresi ÇAĞIRANIN DEĞİL — grubun first_seen-1h ..
// last_seen+2m aralığı (exceptionScanWindow, v0.9.795). range_s bu yüzden
// maliyeti DÜŞÜRMEZ; yalnız DÖNEN örnekleri "şu kadar saniyeden yeni" diye
// süzer. Süzme TAM: tarama yeniden-eskiye ilerler, dolayısıyla kesim
// noktasının altında atılan bir satırın ÜSTÜNDE kaçırılmış bir satır
// olamaz. Şema da açıklama da bunu söylüyor.
//
// SATIR ŞEKLİ (guided_parity doktrini): chstore tipi değil, alt küme +
// snake_case. Metinler RUNE tavanlı — byte kesmesi UTF-8'i böler — ve
// kesildiği METNİN İÇİNDE söylenir, çünkü modelin gördüğü tek yer orası
// (v0.9.842'nin truncate dersi). Örnekler AĞIR: her biri bir stack taşır,
// o yüzden varsayılan 3 / tavan 10 (liste tool'larının 50/200'ü değil).

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/cilcenk/coremetry/internal/chstore"
	"github.com/cilcenk/coremetry/internal/mcp"
)

const (
	exSamplesDefaultRangeS = 3600
	exSamplesMaxRangeS     = 7 * 86400
	exSamplesDefaultRows   = 3
	exSamplesMaxRows       = 10
	// Metin tavanları RUNE cinsinden. Mesaj tek satırlık bir kimliktir;
	// stack ise modelin asıl okuduğu şey ama tek başına bağlam penceresini
	// yiyebilir (JVM stack'leri 200+ satır).
	exSampleMsgMaxRunes   = 300
	exSampleStackMaxRunes = 2000
)

// exSamplesWindowS — range_s → efektif saniye. Kova TABANI YOK: bu okuma
// bir pre-aggregate değil, ham span taraması üzerinde satır süzmesidir;
// 60 saniyelik bir pencere gerçekten 60 saniyedir. Saf, tablo testli.
func exSamplesWindowS(rangeS int) int {
	if rangeS <= 0 {
		return exSamplesDefaultRangeS
	}
	if rangeS > exSamplesMaxRangeS {
		return exSamplesMaxRangeS
	}
	return rangeS
}

// capRunesSpoken — rune-güvenli kesim; kesildiğini metnin İÇİNDE söyler.
// İkinci dönüş "kesildi mi". Saf, tablo testli (çok baytlı girdiyle).
func capRunesSpoken(s string, max int) (string, bool) {
	r := []rune(s)
	if max <= 0 || len(r) <= max {
		return s, false
	}
	return string(r[:max]) + fmt.Sprintf("\n…[truncated: first %d of %d characters]", max, len(r)), true
}

// ExceptionSampleRow — bir istisna oluşumu. Service/ExType satır başına
// TEKRARLANIR (zarf da taşır) çünkü model satırı bağlamından kopararak
// alıntılıyor; stack'in hangi servise ait olduğu satırın kendisinde
// yazmazsa yanlış servise atfedilir.
type ExceptionSampleRow struct {
	Service    string `json:"service"`
	ExType     string `json:"ex_type"`
	Message    string `json:"message,omitempty"`
	Stacktrace string `json:"stacktrace,omitempty"`
	TraceID    string `json:"trace_id,omitempty"`
	SpanID     string `json:"span_id,omitempty"`
	SpanName   string `json:"span_name,omitempty"`
	TimeISO    string `json:"time_iso"`
}

// exSampleRows — chstore örnekleri → tool satırları + "metin kesildi mi".
// Sıra korunur (yeniden-eskiye). Saf, tablo testli.
func exSampleRows(service, exType string, in []chstore.ExceptionSample) ([]ExceptionSampleRow, bool) {
	out := make([]ExceptionSampleRow, 0, len(in))
	anyCut := false
	for _, s := range in {
		msg, cutMsg := capRunesSpoken(s.Message, exSampleMsgMaxRunes)
		stack, cutStack := capRunesSpoken(s.Stacktrace, exSampleStackMaxRunes)
		anyCut = anyCut || cutMsg || cutStack
		out = append(out, ExceptionSampleRow{
			Service:    service,
			ExType:     exType,
			Message:    msg,
			Stacktrace: stack,
			TraceID:    s.TraceID,
			SpanID:     s.SpanID,
			SpanName:   s.SpanName,
			TimeISO:    time.Unix(0, s.Time).UTC().Format(time.RFC3339),
		})
	}
	return out, anyCut
}

// exSamplesInWindow — çağıranın penceresine göre süz; ikinci dönüş
// pencere DIŞINDA kaldığı için düşen satır sayısı (boş cevabın sebebi).
//
// Satır satır süzer, önek kesmez: tarama bugün yeniden-eskiye geliyor ama
// sıraya bağlı bir kesim, okuma sırası değişirse SESSİZCE yanlış satırları
// atardı. Saf, tablo testli.
func exSamplesInWindow(in []chstore.ExceptionSample, cutoffNs int64) ([]chstore.ExceptionSample, int) {
	kept := make([]chstore.ExceptionSample, 0, len(in))
	dropped := 0
	for _, s := range in {
		if s.Time >= cutoffNs {
			kept = append(kept, s)
			continue
		}
		dropped++
	}
	return kept, dropped
}

// exSamplesReasons — BOŞ liste dört ayrı şey demek olabilir ve yalnız zarf
// hangisi olduğunu bilir (v0.9.463 dürüstlük A11 + v0.9.795 zarfının MCP
// karşılığı). Sırayla: grup yok → pencere dışı → aday bütçesi doldu →
// pencere sonuna kadar okundu (span retention). Saf, tablo testli.
func exSamplesReasons(groupFound bool, env chstore.ExceptionSamples, droppedOutOfWindow, windowS int) []string {
	if !groupFound {
		return []string{
			"no exception group with this fingerprint — it may belong to another Coremetry instance, or the group row was purged; re-run list_exception_groups and copy the fingerprint verbatim",
		}
	}
	if droppedOutOfWindow > 0 {
		return []string{
			fmt.Sprintf("the group HAS samples, but all %d found are older than range_s (%ds) — widen range_s (last_seen_iso says how far back)", droppedOutOfWindow, windowS),
		}
	}
	if env.ScanCapped {
		return []string{
			"the candidate scan hit its ceiling before finding members of this group — the window is dominated by sibling exceptions sharing the same (service, exception type); retry, or read the group's own counts with list_exception_groups",
		}
	}
	if env.WindowExhausted {
		return []string{
			"the group's whole window was read and no member spans are left — exception groups outlive the spans they were built from, so this is span retention, not an error",
		}
	}
	return []string{
		"no samples returned for this group in this window — widen range_s, or verify the fingerprint with list_exception_groups",
	}
}

type getExceptionSamplesArgs struct {
	Fingerprint string `json:"fingerprint"`
	RangeS      int    `json:"range_s,omitempty"`
	Limit       int    `json:"limit,omitempty"`
}

func getExceptionSamplesTool(d Deps) mcp.Tool {
	return mcp.Tool{
		Name:             "get_exception_samples",
		ShortDescription: "Bir istisna grubunun GERÇEK oluşumları: stacktrace, mesaj ve trace_id. list_exception_groups'un devamı — grup sayı verir, bu stack'i ve trace pivotunu verir. Örnek başına stack taşır, varsayılan 3 satır.",
		Description: "Fetch real occurrences of ONE exception group (by fingerprint): stacktrace, per-occurrence message, service, operation, and the trace_id to pivot with. " +
			"This is the second half of the exception chain and the only tool that carries a stack: use it right after list_exception_groups (which returns counts and fingerprints but NO stack and NO trace id), " +
			"then take trace_id into get_trace / get_logs_for_trace to see what the request was doing when it threw. " +
			"BOUNDS: samples are HEAVY (one stack each) — default 3, max 10; message is capped at 300 characters and stacktrace at 2000, and a cut is stated inside the text itself, so never claim to have read a whole stack while it says truncated. " +
			"COST: the scan window is the GROUP's own life (first_seen..last_seen), NOT range_s — range_s only filters which returned occurrences are recent enough, it does not make the call cheaper. " +
			"An empty list is NOT an error and never means 'this exception is fine': `reasons` distinguishes a wrong fingerprint, a window with no recent occurrence, an exhausted candidate budget, and span retention having outlived the group row.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"fingerprint": map[string]any{
					"type":        "string",
					"description": "Exception group fingerprint, exactly as list_exception_groups returns it. Required — samples are per-group. Never invent one.",
				},
				"range_s": map[string]any{
					"type":    "integer",
					"minimum": 0,
					"maximum": exSamplesMaxRangeS,
					"description": "Keep only occurrences newer than this many seconds. Default 3600 (1h), max 604800 (7d). " +
						"Does NOT bound the scan (that window comes from the group's own first/last seen) — it only filters the rows you get back.",
				},
				"limit": map[string]any{
					"type":        "integer",
					"minimum":     1,
					"maximum":     exSamplesMaxRows,
					"description": "Max samples. Default 3, max 10 — each one carries a stacktrace.",
				},
			},
			"required": []string{"fingerprint"},
		},
		// Salt-okunur; REST eşi GET /api/exception-groups/{fp}/samples
		// kapısız (viewer-açık, api.go:850).
		MinRole: "",
		Handler: func(ctx context.Context, raw json.RawMessage) (any, error) {
			var a getExceptionSamplesArgs
			if len(raw) > 0 {
				if err := json.Unmarshal(raw, &a); err != nil {
					return nil, fmt.Errorf("decode args: %w", err)
				}
			}
			if a.Fingerprint == "" {
				return nil, fmt.Errorf("fingerprint is required — get it from list_exception_groups")
			}
			windowS := exSamplesWindowS(a.RangeS)
			limit := clampLimit(a.Limit, exSamplesDefaultRows, exSamplesMaxRows)

			// Grup satırı ÖNCE okunur: satır şekli (service, ex_type)
			// oradan gelir ve "böyle bir parmak izi yok" cevabı ancak
			// burada ayırt edilir. GetExceptionGroupSamples aynı satırı
			// kendi içinde bir kez daha okuyor — küçük bir
			// ReplacingMergeTree FINAL okuması; alternatifi chstore'da
			// ikinci bir imza açmaktı ve bu tool zaten grubu gerektiriyor.
			g, err := d.Store.GetExceptionGroup(ctx, a.Fingerprint)
			if err != nil {
				return nil, err
			}
			out := map[string]any{
				"fingerprint": a.Fingerprint,
				"window_s":    windowS,
				"samples":     []ExceptionSampleRow{},
				"count":       0,
				"truncated":   false,
			}
			if g == nil {
				out["reasons"] = exSamplesReasons(false, chstore.ExceptionSamples{}, 0, windowS)
				return out, nil
			}
			out["service"] = g.Service
			out["ex_type"] = g.Type
			out["last_seen_iso"] = time.Unix(0, g.LastSeen).UTC().Format(time.RFC3339)
			out["occurrences"] = g.Occurrences

			res, err := d.Store.GetExceptionGroupSamples(ctx, a.Fingerprint, limit)
			if err != nil {
				return nil, err
			}
			cutoff := time.Now().Add(-time.Duration(windowS) * time.Second).UnixNano()
			kept, dropped := exSamplesInWindow(res.Samples, cutoff)
			rows, anyCut := exSampleRows(g.Service, g.Type, kept)
			out["samples"] = rows
			out["count"] = len(rows)
			out["truncated"] = anyCut
			// Tarama zarfı (v0.9.795) — "boş" ile "vazgeçtik" farkını
			// modelin de görmesi için, UI'da olduğu gibi.
			out["scanned"] = res.Scanned
			out["scan_capped"] = res.ScanCapped
			out["window_exhausted"] = res.WindowExhausted
			if len(rows) == 0 {
				out["reasons"] = exSamplesReasons(true, res, dropped, windowS)
			}
			return out, nil
		},
	}
}
