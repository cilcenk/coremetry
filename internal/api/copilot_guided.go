package api

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/cilcenk/coremetry/internal/anomaly"
	"github.com/cilcenk/coremetry/internal/chstore"
	"github.com/cilcenk/coremetry/internal/copilot"
	"github.com/cilcenk/coremetry/internal/logstore"
)

// Guided chat mode (v0.8.397 — AI audit A3, Davis-CoPilot-style).
//
// The free agentic tool loop (copilot_chat.go: up to 5 rounds × 11
// JSON tool schemas) is unreliable on the 2B-class model that is the
// PRIMARY production target (qwen3.5-2b on vLLM): schema soup, wrong
// tool picks, empty answers. Guided mode inverts the control flow —
// a deterministic intent router recognises the highest-value question
// shapes (Turkish + English), the SERVER prefetches the relevant data
// with the existing bounded chstore/logstore reads, renders a compact
// Turkish evidence block, and the model makes exactly ONE tool-less
// narration call (the analyze-service pattern, copilot_aianalyze.go).
//
// Mode selection is config-free: the router runs first for EVERY
// provider — for these five shapes a deterministic prefetch beats
// tool-roulette even on frontier models. Unmatched questions fall
// through to the free tool loop UNCHANGED (frontier models keep full
// power; on small models unmatched questions may still flounder —
// accepted trade-off, documented in docs/ai-enhancement-audit.md §3).
//
// SSE contract is unchanged: the prefetches emit the same `step`
// events the tool loop emits (CopilotChat.tsx renders e.tool chips),
// then one `answer`, then `done`. The single Explain call self-records
// one ai_calls row under the "chat-guided" surface so the /ai page can
// track guided-path quality separately from the free loop.
//
// v0.8.398 (AI audit env-awareness slice): the router also extracts a
// deployment environment against the LIVE env list ("uat ortamındaki
// hatalar", "errors in the uat environment", bare "uat") and threads
// it into the bundles — problems via ProblemFilter.Env (service-
// scoped), slow traces via TraceFilter.Env (deploy_env conjunct);
// env-less data paths (service RED context, logs, deploys) state the
// limitation in the evidence instead of silently ignoring the ask.

// ─── Intent router (pure, table-tested) ─────────────────────────────

type guidedIntent string

const (
	guidedNone          guidedIntent = ""
	guidedProblems      guidedIntent = "problems"
	guidedServiceHealth guidedIntent = "service_health"
	guidedSlowTraces    guidedIntent = "slow_traces"
	guidedDeployImpact  guidedIntent = "deploy_impact"
	guidedLogErrors     guidedIntent = "log_errors"
)

type guidedRoute struct {
	Intent  guidedIntent
	Service string // extracted entity, "" = none/global
	// Env (v0.8.398 — AI audit env-awareness slice) is the deployment
	// environment extracted from the question against the LIVE env
	// list (ListEnvironments), "" = no env narrowing. Threaded into
	// the prefetch bundles: problems → ProblemFilter.Env (service-
	// scoped, env_members.go), slow_traces → TraceFilter.Env (direct
	// deploy_env conjunct). Logs/deploys carry no env path yet
	// (env-separation Phase 4 pending) — those bundles SAY so in the
	// evidence instead of silently ignoring the ask.
	Env string
}

// normalizeGuidedMsg lowercases for matching. Go's ToLower maps the
// Turkish dotted capital İ to "i"+U+0307 (combining dot above); we
// strip the combining dot so "İstek" matches keyword "istek".
func normalizeGuidedMsg(s string) string {
	return strings.ReplaceAll(strings.ToLower(s), "̇", "")
}

// guidedTokens splits a normalized message into word tokens. The
// charset keeps service-name characters ([a-z0-9._-]) AND Turkish
// letters together so both "mobile-bff-uat" and "loglarında" survive
// as single tokens. Apostrophes are boundaries, which conveniently
// detaches Turkish possessive suffixes ("checkout-service'in" →
// "checkout-service", "in").
func guidedTokens(msg string) []string {
	return strings.FieldsFunc(msg, func(r rune) bool {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			return false
		case r == '.' || r == '_' || r == '-':
			return false
		case r == 'ç' || r == 'ğ' || r == 'ı' || r == 'ö' || r == 'ş' || r == 'ü':
			return false
		}
		return true
	})
}

// tokenHasPrefix reports whether any token starts with any of the
// given stems. Prefix (not equality) matching absorbs Turkish
// agglutination ("hata" matches "hatalar", "hataları", "hatası").
func tokenHasPrefix(tokens []string, stems ...string) bool {
	for _, t := range tokens {
		for _, s := range stems {
			if strings.HasPrefix(t, s) {
				return true
			}
		}
	}
	return false
}

func hasSlowTraceSignal(msg string) bool {
	for _, p := range []string{"en yavaş", "slowest", "slow trace", "yavaş trace", "en uzun"} {
		if strings.Contains(msg, p) {
			return true
		}
	}
	return false
}

func hasDeploySignal(tokens []string) bool {
	return tokenHasPrefix(tokens, "deploy", "rollout", "sürüm", "release")
}

// hasLogSignal: token-bounded so "login" / "catalog" / "topology"
// never trigger the logs intent. Covers the common Turkish case
// suffixes (loglar, loglarında, logunda, logda, logta).
func hasLogSignal(tokens []string) bool {
	for _, t := range tokens {
		if t == "log" || t == "logs" ||
			strings.HasPrefix(t, "loglar") || strings.HasPrefix(t, "logu") ||
			strings.HasPrefix(t, "logd") || strings.HasPrefix(t, "logt") {
			return true
		}
	}
	return false
}

func hasErrorSignal(tokens []string) bool {
	return tokenHasPrefix(tokens, "hata", "error", "exception", "fail", "başarısız", "5xx", "500")
}

func hasProblemSignal(tokens []string) bool {
	return tokenHasPrefix(tokens, "problem", "sorun", "alarm", "alert", "incident", "arıza", "wrong")
}

func hasHealthSignal(tokens []string) bool {
	return tokenHasPrefix(tokens, "sağl", "health", "durum", "nasıl", "yavaş", "slow",
		"gecikme", "latency", "performan", "p99", "p95", "iyi")
}

// hasGuidedSignal is the cheap precheck the handler runs BEFORE
// fetching the live service list — a message with no guided keyword
// at all skips the catalogue read and goes straight to the tool loop.
func hasGuidedSignal(msg string) bool {
	toks := guidedTokens(msg)
	return hasSlowTraceSignal(msg) || hasDeploySignal(toks) ||
		hasLogSignal(toks) || hasErrorSignal(toks) ||
		hasProblemSignal(toks) || hasHealthSignal(toks)
}

// guidedStopwords are message tokens that must never be treated as a
// service-name candidate in the unique-prefix fallback.
var guidedStopwords = map[string]bool{
	"servis": true, "servisi": true, "servisin": true, "service": true, "services": true,
	"trace": true, "traces": true, "log": true, "logs": true, "error": true, "errors": true,
	"deploy": true, "deploys": true, "deployment": true, "release": true, "rollout": true,
	"slow": true, "slowest": true, "health": true, "healthy": true, "latency": true,
	"son": true, "last": true, "the": true, "for": true, "and": true, "show": true,
	"what": true, "olan": true, "neden": true, "most": true, "hour": true, "hours": true,
	"minute": true, "minutes": true, "day": true, "days": true, "with": true, "problem": true,
	"problems": true, "alert": true, "alerts": true, "incident": true, "how": true,
}

// asciiNameToken reports whether the token could be a service name
// (chstore's serviceTokenRe convention: ascii-only, so Turkish words
// with diacritics never collide with a service).
func asciiNameToken(t string) bool {
	for _, r := range t {
		ok := (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '.' || r == '_' || r == '-'
		if !ok {
			return false
		}
	}
	return true
}

func isNameChar(b byte) bool {
	return (b >= 'a' && b <= 'z') || (b >= '0' && b <= '9') || b == '.' || b == '_' || b == '-'
}

// indexBounded finds sub inside msg with word-ish boundaries: the
// characters on either side must not be service-name characters, so
// "mobile-bff" never matches inside "mobile-bff-uat".
func indexBounded(msg, sub string) int {
	for start := 0; ; {
		i := strings.Index(msg[start:], sub)
		if i < 0 {
			return -1
		}
		i += start
		leftOK := i == 0 || !isNameChar(msg[i-1])
		r := i + len(sub)
		rightOK := r >= len(msg) || !isNameChar(msg[r])
		if leftOK && rightOK {
			return i
		}
		start = i + 1
	}
}

// extractServiceEntity matches the message against the LIVE service
// list (never a guess): first the longest bounded full-name substring
// (so the suffixed "mobile-bff-uat" beats its "mobile-bff" prefix
// sibling), then a unique-prefix token fallback so "checkout servisi"
// resolves to "checkout-service" when exactly one service starts with
// that token. Ambiguous prefixes (2+ matches) return "" — deterministic
// beats clever.
func extractServiceEntity(msg string, services, envs []string) string {
	best := ""
	for _, svc := range services {
		ls := strings.ToLower(svc)
		if len(ls) < 3 || len(ls) <= len(best) {
			continue
		}
		if indexBounded(msg, ls) >= 0 {
			best = svc
		}
	}
	if best != "" {
		return best
	}
	// v0.8.398 — a token that names a LIVE deployment environment is
	// never a service-PREFIX candidate: "uat ortamındaki hatalar" must
	// not resolve to a "uat-gateway" service. The bounded full-name
	// pass above still wins when the operator literally types the
	// service name ("uat-gateway hataları").
	envTok := map[string]bool{}
	for _, e := range envs {
		envTok[strings.ToLower(e)] = true
	}
	for _, t := range guidedTokens(msg) {
		if len(t) < 3 || guidedStopwords[t] || envTok[t] || !asciiNameToken(t) {
			continue
		}
		match, n := "", 0
		for _, svc := range services {
			ls := strings.ToLower(svc)
			if !strings.HasPrefix(ls, t) {
				continue
			}
			if len(ls) > len(t) && ls[len(t)] != '-' && ls[len(t)] != '_' && ls[len(t)] != '.' {
				continue // "check" must not claim "checkout-service"
			}
			n++
			match = svc
			if n > 1 {
				break
			}
		}
		if n == 1 {
			return match
		}
	}
	return ""
}

// extractEnvEntity matches the message against the LIVE environment
// list (ListEnvironments — never a guess), the env twin of
// extractServiceEntity's bounded full-name pass (v0.8.398). Bounded
// matching gives both asks for free: the bare name ("uat hataları")
// and the phrased forms ("uat ortamında/ortamı", "uat environment",
// "env uat") all contain the standalone env token, while an
// env-suffixed SERVICE name ("mobile-bff-uat") never leaks an env —
// '-' is a name char, so the inner "uat" fails the boundary check.
// Longest match wins ("preprod" beats "prod"; "prod" can't match
// inside "preprod" anyway). Unknown env words return "" — the bundle
// then runs env-less, deterministic beats clever. No prefix fallback:
// env names are short, exact vocabulary.
func extractEnvEntity(msg string, envs []string) string {
	best := ""
	for _, env := range envs {
		le := strings.ToLower(env)
		if len(le) < 2 || len(le) <= len(best) {
			continue
		}
		if indexBounded(msg, le) >= 0 {
			best = env
		}
	}
	return best
}

// routeGuidedIntent is THE router: normalized keyword matching over
// the five shapes, most-specific first. Pure — table-tested in
// copilot_guided_test.go with Turkish + English variants.
func routeGuidedIntent(raw string, services, envs []string) guidedRoute {
	msg := normalizeGuidedMsg(raw)
	toks := guidedTokens(msg)
	svc := extractServiceEntity(msg, services, envs)
	env := extractEnvEntity(msg, envs)
	switch {
	case hasSlowTraceSignal(msg):
		return guidedRoute{guidedSlowTraces, svc, env}
	case hasDeploySignal(toks):
		return guidedRoute{guidedDeployImpact, svc, env}
	case hasLogSignal(toks) && hasErrorSignal(toks):
		return guidedRoute{guidedLogErrors, svc, env}
	case hasProblemSignal(toks):
		return guidedRoute{guidedProblems, svc, env}
	case svc != "" && (hasHealthSignal(toks) || hasErrorSignal(toks)):
		return guidedRoute{guidedServiceHealth, svc, env}
	case hasErrorSignal(toks):
		return guidedRoute{guidedProblems, "", env}
	}
	return guidedRoute{guidedNone, "", ""}
}

// guidedRangeRe extracts "son 2 saat" / "last 30 minutes" style
// windows. Longer unit spellings come first in the alternation so
// "minutes" isn't half-eaten by "min".
var guidedRangeRe = regexp.MustCompile(`(\d+)\s*(gün|gun|days|day|saat|hours|hour|hrs|hr|dakika|dk|minutes|minute|mins|min)`)

// guidedRangeS derives the lookback window (seconds) from the
// question. Default 1800 (30m, the chat tools' default); bare unit
// words ("son bir saat", "today") map to 1h/1d. Clamped to
// [300, 86400] so a typo can't trigger a week-wide scan.
func guidedRangeS(raw string) int64 {
	msg := normalizeGuidedMsg(raw)
	rangeS := int64(1800)
	if m := guidedRangeRe.FindStringSubmatch(msg); m != nil {
		n := int64(0)
		fmt.Sscanf(m[1], "%d", &n)
		switch unit := m[2]; {
		// "dk"/"dakika" also start with 'd' — day units must be
		// matched by full stem, never a bare 'd' prefix (this exact
		// branch is pinned by TestGuidedRangeS, the unit-mixing rule).
		case strings.HasPrefix(unit, "gün") || strings.HasPrefix(unit, "gun") || strings.HasPrefix(unit, "day"):
			rangeS = n * 86400
		case strings.HasPrefix(unit, "saat") || strings.HasPrefix(unit, "hour") || strings.HasPrefix(unit, "hr"):
			rangeS = n * 3600
		default: // dakika | dk | minute | min
			rangeS = n * 60
		}
	} else if strings.Contains(msg, "saat") || strings.Contains(msg, "hour") {
		rangeS = 3600
	} else if strings.Contains(msg, "gün") || strings.Contains(msg, "day") ||
		strings.Contains(msg, "bugün") || strings.Contains(msg, "today") {
		rangeS = 86400
	}
	if rangeS < 300 {
		rangeS = 300
	}
	if rangeS > 86400 {
		rangeS = 86400
	}
	return rangeS
}

// fmtAgoTR renders "how long ago" in compact Turkish units. EVERY
// unit branch is exercised by TestFmtAgoTR (the Nh/Nd unit-mixing
// rule). Negative deltas (clock skew) clamp to 0.
func fmtAgoTR(seconds int64) string {
	if seconds < 0 {
		seconds = 0
	}
	switch {
	case seconds < 60:
		return fmt.Sprintf("%dsn", seconds)
	case seconds < 3600:
		return fmt.Sprintf("%ddk", seconds/60)
	case seconds < 86400:
		h, m := seconds/3600, (seconds%3600)/60
		if m == 0 {
			return fmt.Sprintf("%dsa", h)
		}
		return fmt.Sprintf("%dsa %ddk", h, m)
	default:
		d, h := seconds/86400, (seconds%86400)/3600
		if h == 0 {
			return fmt.Sprintf("%dgün", d)
		}
		return fmt.Sprintf("%dgün %dsa", d, h)
	}
}

// ─── Narration prompt (Turkish-native, analyze-service posture) ─────

// guidedChatPrompt frames the single narration call. Turkish-native
// instructions (the 2B lesson from copilot_aianalyze.go: English
// instructions + Turkish answers is a code-switching tax on a small
// model). Prose output — the chat panel renders text, not JSON.
const guidedChatPrompt = `Sen Coremetry'nin gözlemlenebilirlik asistanısın. Sana operatörün sorusu ve
sunucunun canlı telemetriden topladığı ÖZET VERİ bloğu verilir.

KURALLAR:
- SADECE verilen veriye dayan. Veride olmayan servis adı, sayı veya trace ID UYDURMA.
- Önce sorunun cevabını 1-2 cümlede ver, sonra kanıt olan somut sayıları sırala.
- latency, span, p99, timeout, deploy, trace gibi teknik terimleri ÇEVİRME.
- Veri boş veya yetersizse bunu açıkça söyle; tahmin yürütme.
- Kısa ve taranabilir yaz: madde işaretleri kullan, 8 maddeyi geçme.` + copilot.AnswerInTurkish

// ─── Entry point (called from copilotChat before the tool loop) ─────

// copilotChatGuided tries the guided path for the last user message.
// Returns handled=false when the router doesn't match or a primary
// prefetch fails — the caller then runs the free tool loop unchanged.
// handled=true means the exchange is complete (answer or error
// emitted); ok mirrors the `done` event's success flag.
func (s *Server) copilotChatGuided(ctx context.Context, emit func(string, any), msgs []copilot.ChatMessage) (handled, ok bool) {
	question := strings.TrimSpace(lastUserText(msgs))
	if question == "" {
		return false, false
	}
	norm := normalizeGuidedMsg(question)
	if !hasGuidedSignal(norm) {
		return false, false // zero-cost fast path: no catalogue read
	}
	route := routeGuidedIntent(question, s.guidedServiceNames(ctx), s.guidedEnvNames(ctx))
	if route.Intent == guidedNone {
		return false, false
	}
	rangeS := guidedRangeS(question)
	to := time.Now()
	from := to.Add(-time.Duration(rangeS) * time.Second)

	var evidence, sources string
	var err error
	switch route.Intent {
	case guidedProblems:
		evidence, sources, err = s.guidedProblemsBundle(ctx, emit, route.Service, route.Env)
	case guidedServiceHealth:
		evidence, sources, err = s.guidedServiceHealthBundle(ctx, emit, route.Service, route.Env, from, to, rangeS)
	case guidedSlowTraces:
		evidence, sources, err = s.guidedSlowTracesBundle(ctx, emit, route.Service, route.Env, from, to, rangeS)
	case guidedDeployImpact:
		evidence, sources, err = s.guidedDeployBundle(ctx, emit, route.Service, route.Env, rangeS)
	case guidedLogErrors:
		evidence, sources, err = s.guidedLogErrorsBundle(ctx, emit, route.Service, route.Env, from, to, rangeS)
	}
	if err != nil {
		// Prefetch failed hard → let the free loop try; its tools may
		// route differently. The steps already emitted just render as
		// extra progress chips.
		return false, false
	}

	// The ONE self-recording model call, via the surface-explicit
	// wrapper so the ai_calls row lands as "chat-guided" — quality
	// tracking for the guided path, separate from the free-loop
	// "chat" rows.
	// v0.8.404 — token streaming: the narration call streams its
	// answer tokens as `delta` events on the chat SSE. The `answer`
	// event below stays the UNCHANGED source of truth (full text +
	// exchangeId feedback anchor) — old frontends that ignore `delta`
	// render exactly as before, and when the endpoint can't stream
	// (vLLM builds that 400 on stream:true) StreamText falls back to
	// the buffered call transparently: zero deltas, same answer.
	user := "SORU: " + question + "\n\nVERİ:\n" + evidence
	raw, exErr := s.copilotStreamSurface(ctx, "chat-guided", guidedChatPrompt, user, func(delta string) {
		emit("delta", map[string]string{"text": delta})
	})
	if exErr != nil {
		emit("error", map[string]string{"error": exErr.Error()})
		return true, false
	}
	// Deterministic provenance footer — appended server-side, never
	// trusted to the model.
	// exchangeId (v0.8.399): the id the chat handler minted rides in
	// on CallMeta — the Explain call above already recorded it on the
	// "chat-guided" ai_calls row, so the UI's thumbs up/down joins
	// back to it exactly like the free tool loop's answers.
	answer := strings.TrimSpace(raw) + "\n\nKaynak: " + sources
	emit("answer", map[string]string{
		"text": answer, "exchangeId": copilot.MetaFromContext(ctx).ExchangeID,
	})
	return true, true
}

// guidedServiceNames returns the live service-name list for entity
// extraction, Redis-cached for 60s so chat traffic costs at most one
// catalogue read per minute per replica. Soft-fails to nil — the
// router still handles the entity-free intents.
func (s *Server) guidedServiceNames(ctx context.Context) []string {
	const key = "copilot:guided:svcnames"
	if b, ok, _ := s.cache.Get(ctx, key); ok && len(b) > 0 {
		var names []string
		if json.Unmarshal(b, &names) == nil {
			return names
		}
	}
	names, _, err := s.store.ListServiceNames(ctx, "", 2000, 0)
	if err != nil {
		return nil
	}
	if b, merr := json.Marshal(names); merr == nil {
		_ = s.cache.Set(ctx, key, b, 60*time.Second)
	}
	return names
}

// guidedEnvNames returns the live deployment-environment list for
// env-entity extraction (v0.8.398), the env twin of guidedServiceNames:
// Redis-cached 60s so chat traffic costs at most one enumeration per
// minute per replica. ListEnvironments with zero from/to resolves to
// the last hour and is count-ordered (busiest env first — extraction's
// equal-length tie-break follows list order, so the busiest wins).
// Soft-fails to nil — the router then runs env-blind, which is the
// pre-v0.8.398 behaviour.
func (s *Server) guidedEnvNames(ctx context.Context) []string {
	const key = "copilot:guided:envnames"
	if b, ok, _ := s.cache.Get(ctx, key); ok && len(b) > 0 {
		var names []string
		if json.Unmarshal(b, &names) == nil {
			return names
		}
	}
	names, _, err := s.store.ListEnvironments(ctx, time.Time{}, time.Time{}, "", 200)
	if err != nil {
		return nil
	}
	if b, merr := json.Marshal(names); merr == nil {
		_ = s.cache.Set(ctx, key, b, 60*time.Second)
	}
	return names
}

func emitGuidedStep(emit func(string, any), tool, args string) {
	emit("step", map[string]string{"tool": tool, "args": args})
}

// withEnvArg appends the applied env to a step-event args echo so the
// operator's progress chip shows env=uat when the bundle read was
// env-narrowed (v0.8.398). Pure — table-tested. Bundles that CANNOT
// apply the env (logs/deploys, Phase 4 pending) never call this —
// the step echo only ever shows filters that were actually applied.
func withEnvArg(argsJSON, env string) string {
	if env == "" {
		return argsJSON
	}
	if argsJSON == "" || argsJSON == "{}" {
		return `{"env":"` + env + `"}`
	}
	return strings.TrimSuffix(argsJSON, "}") + `,"env":"` + env + `"}`
}

// guidedEnvlessNoteTR flags an env ask on a bundle whose data path has
// no env dimension yet (logs + deploy markers — env-separation Phase 4
// pending): the evidence SAYS the filter was not applied instead of
// silently ignoring it. The narration prompt forbids inventing, so
// this line is what keeps the 2B model from claiming "uat'ta ...".
// Pure — table-tested (v0.8.398).
func guidedEnvlessNoteTR(what, env string) string {
	if env == "" {
		return ""
	}
	return fmt.Sprintf("Not: %s ortam boyutu taşımıyor (env-ayrımı Faz 4 bekliyor) — %q ortam filtresi UYGULANMADI; sayılar tüm ortamların toplamı.\n", what, env)
}

// guidedProblemFilter builds the problems prefetch filter. Extracted
// pure so the env threading (ProblemFilter.Env — service-scoped
// semantics, env_members.go) is pinned by a table test (v0.8.398).
func guidedProblemFilter(service, env string, limit int) chstore.ProblemFilter {
	return chstore.ProblemFilter{Status: "open", Service: service, Env: env, Limit: limit}
}

// guidedTraceFilter builds the slow-traces prefetch filter. Extracted
// pure so the env threading (TraceFilter.Env — direct deploy_env
// conjunct, raw-fallback path) is pinned by a table test (v0.8.398).
func guidedTraceFilter(service, env string, from, to time.Time) chstore.TraceFilter {
	return chstore.TraceFilter{
		Service: service, Env: env, From: from, To: to,
		Sort: "duration", Order: "desc", Limit: 10, CountMode: "skip",
	}
}

// ─── Prefetch bundles (bounded, existing reads only) ────────────────

// (a) "errors/problems now" → open problems + triage priority + the
// persisted deterministic root-cause hypotheses (v0.8.394 enrichment).
// env (v0.8.398) rides ProblemFilter.Env — the service-scoped
// semantics from env_members.go; the evidence spells that out so the
// narration never oversells the filter.
func (s *Server) guidedProblemsBundle(ctx context.Context, emit func(string, any), service, env string) (string, string, error) {
	emitGuidedStep(emit, "list_problems", withEnvArg(`{"status":"open"}`, env))
	probs, err := s.store.ListProblems(ctx, guidedProblemFilter(service, env, 50))
	if err != nil {
		return "", "", err
	}
	probs = chstore.EnrichProblemsWithPriority(probs)
	emitGuidedStep(emit, "root_cause_hypotheses", "")
	probs = s.store.EnrichProblemsWithRootCause(ctx, probs)
	evidence := renderProblemsEvidenceTR(probs, service, env, time.Now())
	src := "açık problemler + triage önceliği + kök-neden hipotezleri (canlı)"
	if env != "" {
		evidence += fmt.Sprintf("Not: problem kayıtları ortam boyutu taşımaz — %q filtresi servis üyeliğiyle uygulandı (bu ortamda koşan servislerin problemleri + global kurallar).\n", env)
		src += fmt.Sprintf(", ortam: %s (servis kapsamlı)", env)
	}
	return evidence, src, nil
}

// (b) "service X sağlığı/health/slow" → the analyze-service context
// bundle (buildServiceContext + renderServiceSnapshot, reused
// verbatim) + the service's open problems with root-cause.
// buildServiceContext is env-BLIND (its MV reads have no env
// dimension) — an env ask (v0.8.398) narrows only the problems
// sub-read and prepends an honest one-liner so the model attributes
// the RED numbers correctly instead of claiming they're env-scoped.
func (s *Server) guidedServiceHealthBundle(ctx context.Context, emit func(string, any), service, env string, from, to time.Time, rangeS int64) (string, string, error) {
	emitGuidedStep(emit, "service_context", `{"service":"`+service+`"}`)
	cx := s.buildServiceContext(ctx, service, from, to)
	var b strings.Builder
	if env != "" {
		fmt.Fprintf(&b, "Not: RED değerleri tüm ortamların toplamı (servis bağlamı ortam kırılımı yapmıyor); açık problemler %q ortamına daraltıldı.\n", env)
	}
	b.WriteString(renderServiceSnapshot(cx))
	if cx.Current.Spans == 0 {
		b.WriteString("Bu pencerede span verisi yok.\n")
	}
	emitGuidedStep(emit, "list_problems", withEnvArg(`{"service":"`+service+`"}`, env))
	probs, perr := s.store.ListProblems(ctx, guidedProblemFilter(service, env, 10))
	if perr == nil {
		probs = chstore.EnrichProblemsWithPriority(probs)
		probs = s.store.EnrichProblemsWithRootCause(ctx, probs)
		if len(probs) == 0 {
			b.WriteString("Açık problem yok.\n")
		} else {
			b.WriteString(renderProblemsEvidenceTR(probs, service, env, time.Now()))
		}
	}
	src := fmt.Sprintf("servis RED özeti + baseline + en sık hatalar + deploy işaretçileri + açık problemler (son %s)", fmtAgoTR(rangeS))
	if env != "" {
		src += fmt.Sprintf("; RED tüm ortamlar, problemler ortam: %s", env)
	}
	return b.String(), src, nil
}

// (c) "en yavaş/slowest traces [service]" → duration-ranked trace
// summaries from the trace_summary_5m fast path (Sort=duration,
// CountMode=skip — the same shape /traces uses). env (v0.8.398) rides
// TraceFilter.Env — the direct deploy_env conjunct; a non-empty env
// takes the raw-fallback path exactly like the /traces ?env= pick.
func (s *Server) guidedSlowTracesBundle(ctx context.Context, emit func(string, any), service, env string, from, to time.Time, rangeS int64) (string, string, error) {
	emitGuidedStep(emit, "slow_traces", withEnvArg(`{"service":"`+service+`","sort":"duration"}`, env))
	rows, _, _, err := s.store.GetTraces(ctx, guidedTraceFilter(service, env, from, to))
	if err != nil {
		return "", "", err
	}
	src := fmt.Sprintf("duration'a göre sıralı trace listesi (son %s)", fmtAgoTR(rangeS))
	if env != "" {
		src += fmt.Sprintf(", ortam: %s", env)
	}
	return renderSlowTracesEvidenceTR(rows, service, env, rangeS), src, nil
}

// guidedDeployRef unifies the two deploy reads (global
// RecentDeployEntry vs per-service Deploy) for the renderer.
type guidedDeployRef struct {
	Service string
	Version string
	TimeNs  int64
}

// (d) "deploy etkisi/son deploy" → recent rollouts + before/after RED
// impact (ComputeDeployImpact, single bounded CH pass per deploy,
// capped at 3). Deploy markers carry NO env dimension (env-separation
// Phase 4 pending) — an env ask (v0.8.398) is answered honestly via
// guidedEnvlessNoteTR instead of silently ignored; the step echo also
// omits env because the filter was not applied.
func (s *Server) guidedDeployBundle(ctx context.Context, emit func(string, any), service, env string, rangeS int64) (string, string, error) {
	// Deploy questions imply a wider horizon than the default 30m chat
	// window — "son deploy" is rarely in the last half hour. Floor the
	// lookback at 6h, cap 24h (GetRecentDeploys scales its CH timeout
	// with the window).
	lookback := time.Duration(rangeS) * time.Second
	if lookback < 6*time.Hour {
		lookback = 6 * time.Hour
	}
	var refs []guidedDeployRef
	emitGuidedStep(emit, "recent_deploys", `{"service":"`+service+`"}`)
	if service != "" {
		now := time.Now()
		deps, err := s.store.GetServiceDeploys(ctx, service, now.Add(-lookback), now)
		if err != nil {
			return "", "", err
		}
		for _, d := range deps {
			refs = append(refs, guidedDeployRef{Service: service, Version: d.Version, TimeNs: d.TimeUnixNs})
		}
	} else {
		deps, err := s.store.GetRecentDeploys(ctx, lookback, 10)
		if err != nil {
			return "", "", err
		}
		for _, d := range deps {
			refs = append(refs, guidedDeployRef{Service: d.Service, Version: d.Version, TimeNs: d.FirstSeenNs})
		}
	}
	// Newest first, impact for the top 3 only (bounded CH cost).
	sort.Slice(refs, func(i, j int) bool { return refs[i].TimeNs > refs[j].TimeNs })
	if len(refs) > 5 {
		refs = refs[:5]
	}
	impacts := make([]*chstore.DeployImpact, len(refs))
	for i, ref := range refs {
		if i >= 3 {
			break
		}
		emitGuidedStep(emit, "deploy_impact", `{"service":"`+ref.Service+`","version":"`+ref.Version+`"}`)
		if imp, ierr := s.store.ComputeDeployImpact(ctx, ref.Service, ref.Version, ref.TimeNs, 600); ierr == nil {
			impacts[i] = imp
		}
	}
	src := "deploy işaretçileri + öncesi/sonrası RED etkisi (±10dk pencere)"
	if env != "" {
		src += "; ortam filtresi uygulanamadı (deploy verisi ortam boyutu taşımıyor)"
	}
	return guidedEnvlessNoteTR("deploy işaretçileri", env) +
		renderDeployEvidenceTR(refs, impacts, lookback, time.Now()), src, nil
}

// (e) "log hataları/log errors [service]" → severity histogram totals
// + the curated failure-pattern detector hits (both reads carry the
// existing ES/CH cost guards; the pattern window snaps to the same
// rungs the /anomalies endpoint uses). Logs carry NO env dimension
// (env-separation Phase 4 pending) — an env ask (v0.8.398) is
// answered honestly via guidedEnvlessNoteTR instead of silently
// ignored; the step echo omits env because the filter was not applied.
func (s *Server) guidedLogErrorsBundle(ctx context.Context, emit func(string, any), service, env string, from, to time.Time, rangeS int64) (string, string, error) {
	emitGuidedStep(emit, "log_severity_histogram", `{"service":"`+service+`"}`)
	bucketSec := int(rangeS / 30)
	if bucketSec < 60 {
		bucketSec = 60
	}
	series, err := s.logs.Histogram(ctx, logstore.Filter{Service: service, From: from, To: to}, bucketSec, "severity")
	if err != nil {
		return "", "", err
	}
	emitGuidedStep(emit, "log_patterns", "")
	pats, perr := anomaly.DetectLogPatterns(ctx, s.logs, snapAnomalyWindow(time.Duration(rangeS)*time.Second))
	if perr != nil {
		pats = nil // patterns are additive evidence — soft-fail
	}
	if service != "" {
		kept := pats[:0]
		for _, p := range pats {
			if p.Service == service {
				kept = append(kept, p)
				continue
			}
			for _, ts := range p.TopServices {
				if ts.Service == service {
					kept = append(kept, p)
					break
				}
			}
		}
		pats = kept
	}
	sort.Slice(pats, func(i, j int) bool { return pats[i].CurrentCount > pats[j].CurrentCount })
	if len(pats) > 5 {
		pats = pats[:5]
	}
	src := fmt.Sprintf("log severity histogramı + hata pattern tespitleri (son %s)", fmtAgoTR(rangeS))
	if env != "" {
		src += "; ortam filtresi uygulanamadı (log verisi ortam boyutu taşımıyor)"
	}
	return guidedEnvlessNoteTR("log verisi", env) +
		renderLogErrorsEvidenceTR(series, pats, service, rangeS), src, nil
}

// ─── Evidence renderers (pure, table-tested) ────────────────────────

const guidedMaxLines = 10

// guidedScopeTR renders the "(servis: X, ortam: Y)" scope fragment
// shared by the problems + slow-traces evidence headers (v0.8.398).
// Pure — table-tested. Empty parts drop out; both empty = "".
func guidedScopeTR(service, env string) string {
	var parts []string
	if service != "" {
		parts = append(parts, "servis: "+service)
	}
	if env != "" {
		parts = append(parts, "ortam: "+env)
	}
	if len(parts) == 0 {
		return ""
	}
	return ", " + strings.Join(parts, ", ")
}

func renderProblemsEvidenceTR(probs []chstore.Problem, service, env string, now time.Time) string {
	scope := ""
	if sc := guidedScopeTR(service, env); sc != "" {
		scope = " (" + sc[2:] + ")" // strip the leading ", " — header form
	}
	if len(probs) == 0 {
		return "Açık problem yok" + scope + ".\n"
	}
	var crit, warn, info int
	for _, p := range probs {
		switch p.Severity {
		case "critical":
			crit++
		case "warning":
			warn++
		default:
			info++
		}
	}
	var b strings.Builder
	fmt.Fprintf(&b, "Açık problemler%s: toplam %d (kritik %d, warning %d, info %d)\n",
		scope, len(probs), crit, warn, info)
	for i, p := range probs {
		if i >= guidedMaxLines {
			fmt.Fprintf(&b, "(ilk %d satır gösteriliyor)\n", guidedMaxLines)
			break
		}
		name := p.RuleName
		if name == "" {
			name = p.Metric
		}
		fmt.Fprintf(&b, "- [%s] %s — %s (%s, %s önce): değer %.2f / eşik %.2f",
			p.Priority, p.Service, name, p.Severity,
			fmtAgoTR(now.UnixNano()/1e9-p.StartedAt/1e9), p.Value, p.Threshold)
		if p.RootCause != nil && p.RootCause.TopSuspect != "" {
			fmt.Fprintf(&b, " | kök-neden şüphelisi: %s (güven %.2f)",
				p.RootCause.TopSuspect, p.RootCause.Confidence)
		}
		if p.PriorityReason != "" {
			fmt.Fprintf(&b, " | öncelik nedeni: %s", p.PriorityReason)
		}
		b.WriteString("\n")
	}
	return b.String()
}

func renderSlowTracesEvidenceTR(rows []chstore.TraceRow, service, env string, rangeS int64) string {
	scope := guidedScopeTR(service, env)
	if len(rows) == 0 {
		return fmt.Sprintf("En yavaş trace'ler (son %s%s): bu pencerede trace bulunamadı.\n", fmtAgoTR(rangeS), scope)
	}
	var b strings.Builder
	fmt.Fprintf(&b, "En yavaş trace'ler (son %s%s, duration'a göre):\n", fmtAgoTR(rangeS), scope)
	for _, r := range rows {
		flag := ""
		if r.HasError {
			flag = ", HATA"
		}
		fmt.Fprintf(&b, "- %.0fms — %s / %s (%d span%s) trace=%s\n",
			r.DurationMs, r.ServiceName, r.RootName, r.SpanCount, flag, r.TraceID)
	}
	return b.String()
}

func renderDeployEvidenceTR(refs []guidedDeployRef, impacts []*chstore.DeployImpact, lookback time.Duration, now time.Time) string {
	if len(refs) == 0 {
		return fmt.Sprintf("Son %s içinde deploy görülmedi.\n", fmtAgoTR(int64(lookback.Seconds())))
	}
	var b strings.Builder
	fmt.Fprintf(&b, "Son deploylar (son %s):\n", fmtAgoTR(int64(lookback.Seconds())))
	for i, ref := range refs {
		fmt.Fprintf(&b, "- %s %s (%s önce)", ref.Service, ref.Version,
			fmtAgoTR(now.UnixNano()/1e9-ref.TimeNs/1e9))
		if i < len(impacts) && impacts[i] != nil {
			imp := impacts[i]
			fmt.Fprintf(&b, " | etki (±10dk): p99 %.0fms→%.0fms (%%%+.1f), error %%%.2f→%%%.2f, rps %.1f→%.1f",
				imp.Before.P99Ms, imp.After.P99Ms, imp.P99DeltaPct,
				imp.Before.ErrorRate*100, imp.After.ErrorRate*100,
				imp.Before.RPS, imp.After.RPS)
		}
		b.WriteString("\n")
	}
	return b.String()
}

// guidedSeverityOrder pins the histogram render order worst-first so
// the model reads FATAL/ERROR before the INFO noise.
var guidedSeverityOrder = []string{"FATAL", "ERROR", "WARN", "INFO", "DEBUG", "TRACE"}

func renderLogErrorsEvidenceTR(series []logstore.LogSeries, pats []anomaly.LogPatternAnomaly, service string, rangeS int64) string {
	scope := ""
	if service != "" {
		scope = fmt.Sprintf(", servis: %s", service)
	}
	totals := map[string]int64{}
	var grand int64
	for _, s := range series {
		for _, p := range s.Points {
			totals[s.Name] += p.V
			grand += p.V
		}
	}
	var b strings.Builder
	fmt.Fprintf(&b, "Log severity dağılımı (son %s%s): ", fmtAgoTR(rangeS), scope)
	if grand == 0 {
		b.WriteString("bu pencerede log yok.\n")
	} else {
		var parts []string
		seen := map[string]bool{}
		for _, name := range guidedSeverityOrder {
			if v, ok := totals[name]; ok && v > 0 {
				parts = append(parts, fmt.Sprintf("%s %d", name, v))
				seen[name] = true
			}
		}
		// Non-canonical band names (backend-specific) trail, sorted for
		// deterministic output.
		var rest []string
		for name, v := range totals {
			if !seen[name] && v > 0 {
				rest = append(rest, fmt.Sprintf("%s %d", name, v))
			}
		}
		sort.Strings(rest)
		parts = append(parts, rest...)
		b.WriteString(strings.Join(parts, ", "))
		fmt.Fprintf(&b, " (toplam %d)\n", grand)
	}
	if len(pats) > 0 {
		b.WriteString("Öne çıkan hata pattern'leri:\n")
		for _, p := range pats {
			fmt.Fprintf(&b, "- %s ×%d (%s, %s", p.Pattern, p.CurrentCount, p.Service, p.Kind)
			if p.BaselineCount > 0 {
				fmt.Fprintf(&b, ", baseline %d", p.BaselineCount)
			}
			b.WriteString(")\n")
		}
	} else {
		b.WriteString("Bilinen hata pattern'lerinde eşleşme yok.\n")
	}
	return b.String()
}
