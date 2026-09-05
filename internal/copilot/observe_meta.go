package copilot

// observe_meta.go — v0.10.409 (CoSRE denetimi E2/O3/O4): ai_calls'a giren
// prompt sürümü, hata sınıfı ve akış istatistikleri.

import (
	"context"
	"errors"
	"fmt"
	"hash/fnv"
	"net"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	aiprov "github.com/cilcenk/coremetry/internal/ai/provider"
)

// promptVersionRegistry — prompts.go'daki modele giden HER metin sabiti
// (ad → değer). Sürüm = sıralı adların ve değerlerin FNV-64a'sı; bir prompt
// değişince ai_calls.prompt_version değişir ve /ai kırılımı "önce/sonra"
// karşılaştırmasını veriyle yapar. observe_meta_test AD üzerinden pinler:
// prompts.go'da literal taşıyan her `const` burada olmak zorunda (bileşik
// sabitler — systemChatRoundCap gibi kuyruk literali olanlar — dahil;
// saf bileşimler parçalarıyla kapsanır).
var promptVersionRegistry = map[string]string{
	"systemTraceBody":         systemTraceBody,
	"systemSpan":              systemSpan,
	"systemProblem":           systemProblem,
	"systemExceptionBody":     systemExceptionBody,
	"systemIncident":          systemIncident,
	"systemAnomaly":           systemAnomaly,
	"systemServiceHealth":     systemServiceHealth,
	"systemRunbook":           systemRunbook,
	"systemSelfMeta":          systemSelfMeta,
	"systemCompareTraces":     systemCompareTraces,
	"systemDeployImpact":      systemDeployImpact,
	"systemSLOBurn":           systemSLOBurn,
	"systemServiceTags":       systemServiceTags,
	"systemSlowQuery":         systemSlowQuery,
	"systemNLToQuery":         systemNLToQuery,
	"systemCHQueryOptimize":   systemCHQueryOptimize,
	"systemRCAVerdict":        systemRCAVerdict,
	"systemCodeAddendum":      systemCodeAddendum,
	"systemServiceCharts":     systemServiceCharts,
	"systemShiftSummary":      systemShiftSummary,
	"systemAlertNoise":        systemAlertNoise,
	"systemLogPatterns":       systemLogPatterns,
	"systemPostmortem":        systemPostmortem,
	"systemRunbookUpdate":     systemRunbookUpdate,
	"systemServiceAnalysis":   systemServiceAnalysis,
	"DataNotInstruction":      DataNotInstruction,
	"systemGuidedChat":        systemGuidedChat,
	"systemDrawerChat":        systemDrawerChat,
	"systemRAGChat":           systemRAGChat,
	"systemChat":              systemChat,
	"systemChatRoundCap":      systemChatRoundCap,
	"systemIntentClassify":    systemIntentClassify,
	"IntentNoInstructionLine": IntentNoInstructionLine,
	"systemGeneralChat":       systemGeneralChat,
	"AnswerInTurkish":         AnswerInTurkish,
}

var (
	promptVersionOnce sync.Once
	promptVersionVal  string
)

// PromptVersion — 16 hex; süreç ömrü boyunca sabit, süreçler arası
// deterministik (anahtarlar sıralı yazılır).
func PromptVersion() string {
	promptVersionOnce.Do(func() {
		keys := make([]string, 0, len(promptVersionRegistry))
		for k := range promptVersionRegistry {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		h := fnv.New64a()
		for _, k := range keys {
			h.Write([]byte(k))
			h.Write([]byte{0})
			h.Write([]byte(promptVersionRegistry[k]))
			h.Write([]byte{0})
		}
		promptVersionVal = fmt.Sprintf("%016x", h.Sum64())
	})
	return promptVersionVal
}

// Hata sınıfları — LowCardinality; /ai "kaç timeout, kaç refusal".
const (
	ErrClassTimeout         = "timeout"
	ErrClassQuota           = "quota"
	ErrClassRefusal         = "refusal"
	ErrClassContextOverflow = "context_overflow"
	ErrClassHTTP4xx         = "http_4xx"
	ErrClassHTTP5xx         = "http_5xx"
	ErrClassUnavailable     = "unavailable"
	// ErrClassCancelled — istemci iptali (çekmece kapandı, sekme gitti);
	// sağlayıcı zaman aşımı DEĞİL (inceleme bulgusu: ikisi aynı kovaya
	// düşünce /ai "timeout" sayısı operatör davranışını ölçüyordu).
	ErrClassCancelled = "cancelled"
	ErrClassOther     = "other"
)

// httpStatusRe — metin yolunda durum kodu: boşluk/iki nokta/parantezle
// sınırlı 3 hane ("turn 5: openai 502: bad gateway" → 502; "450ms" değil).
var httpStatusRe = regexp.MustCompile(`(?:^|[\s:(])([45][0-9]{2})(?:[\s:)]|$)`)

// ClassifyAIError — typed hata → sınıf; typed değilse metne düşer.
func ClassifyAIError(err error) string {
	if err == nil {
		return ""
	}
	if errors.Is(err, context.Canceled) {
		return ErrClassCancelled
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return ErrClassTimeout
	}
	var nerr net.Error
	if errors.As(err, &nerr) && nerr.Timeout() {
		return ErrClassTimeout
	}
	var operr *net.OpError
	if errors.As(err, &operr) {
		return ErrClassUnavailable
	}
	var herr *aiprov.HTTPError
	if errors.As(err, &herr) {
		switch {
		case herr.Status == 429:
			return ErrClassQuota
		case herr.Status >= 500:
			return ErrClassHTTP5xx
		case herr.Status >= 400:
			if c := ClassifyAIErrorText(herr.Body); c == ErrClassContextOverflow {
				return c
			}
			return ErrClassHTTP4xx
		}
	}
	return ClassifyAIErrorText(err.Error())
}

// ClassifyAIErrorText — yalnız mesaj varken (chat.go RecordUsage).
func ClassifyAIErrorText(msg string) string {
	m := strings.ToLower(msg)
	switch {
	case m == "":
		return ""
	case strings.Contains(m, "refusal") || strings.Contains(m, "reddetti"):
		return ErrClassRefusal
	case strings.Contains(m, "context length") || strings.Contains(m, "maximum context") ||
		strings.Contains(m, "context window") || strings.Contains(m, "too many tokens") || strings.Contains(m, "prompt is too long"):
		return ErrClassContextOverflow
	case strings.Contains(m, "context canceled") || strings.Contains(m, "operation was canceled"):
		return ErrClassCancelled
	case strings.Contains(m, "deadline exceeded") || strings.Contains(m, "timeout") || strings.Contains(m, "timed out"):
		return ErrClassTimeout
	case isQuotaErr(errors.New(msg)):
		return ErrClassQuota
	case strings.Contains(m, "connection refused") || strings.Contains(m, "no such host") || strings.Contains(m, "connection reset"):
		return ErrClassUnavailable
	}
	if sm := httpStatusRe.FindStringSubmatch(m); sm != nil {
		switch {
		case sm[1] == "429":
			return ErrClassQuota
		case sm[1][0] == '5':
			return ErrClassHTTP5xx
		default:
			return ErrClassHTTP4xx
		}
	}
	return ErrClassOther
}

// streamStats — StreamText çağrısı başına ilk-token damgası + düşüş bayrağı.
type streamStats struct {
	mu       sync.Mutex
	started  time.Time
	first    time.Time
	fallback bool
}

func (st *streamStats) markFirst() {
	st.mu.Lock()
	if st.first.IsZero() {
		st.first = time.Now()
	}
	st.mu.Unlock()
}

func (st *streamStats) ttftMs() uint32 {
	st.mu.Lock()
	defer st.mu.Unlock()
	if st.first.IsZero() {
		return 0
	}
	return uint32(st.first.Sub(st.started).Milliseconds())
}

// fallbackSet — kilit altında okuma (yazıcı markStreamFallback ile aynı kilit).
func (st *streamStats) fallbackSet() bool {
	st.mu.Lock()
	defer st.mu.Unlock()
	return st.fallback
}

type streamStatsKey struct{}

func withStreamStats(ctx context.Context, st *streamStats) context.Context {
	return context.WithValue(ctx, streamStatsKey{}, st)
}

func streamStatsFromContext(ctx context.Context) *streamStats {
	st, _ := ctx.Value(streamStatsKey{}).(*streamStats)
	return st
}

func markStreamFallback(ctx context.Context) {
	if st := streamStatsFromContext(ctx); st != nil {
		st.mu.Lock()
		st.fallback = true
		st.mu.Unlock()
	}
}
