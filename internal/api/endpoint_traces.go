package api

// endpoint_traces.go — v0.10.688 (operatör: "paymentApprove isteklerini getir
// dediğimde içinde /paymentapprove geçen endpoint'leri bulup kullanıcıya
// sormalı; evet derse listelemeli ve trace'lerini getirmeli").
//
// İki rota, ikisi de LLM'siz (find_entity emsali):
//   endpoint_candidates — ipucu (istek/endpoint/çağrı/request) + tanımlayıcı
//     token → son 1 sa'te http.route'unda token geçen endpoint'ler
//     (GetEndpointsMV Search: positionCaseInsensitive), ≤8 aday; cevap
//     adayları listeler ve SORAR. Tek adayda da sorar (operatör kararı).
//   endpoint_traces — onay: "evet/tamam/hepsi/olur/ok/getir" (LastRoute
//     endpoint_candidates iken) ya da aday çipi "/yol trace'lerini getir"
//     (ham metinde yol + trace kökü). Trace listesi: TraceFilter.Search
//     (name+http_route+attr haystack, harfe duyarsız), en yeni önce, 20
//     satır; trace_list bloğu (FE tablo) + metin özeti + /traces linki
//     (answer.open ile arkada açılır).
//
// Sunucuda tur-arası ONAY DURUMU YOK: çipler kendi başına yönlenen tam
// cümleler (D1 deseni); çıplak "evet" ise ChatContext.LastRoute'tan
// (zaten Redis'te, 24 sa) türer — endpointAffirmativeRoute SAF.
// Pencere varsayılan son 1 saat (operatör); açık pencere ve "son 6 saate
// genişlet" kipi kazanır (copilotChatGuided).

import (
	"context"
	"fmt"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/cilcenk/coremetry/internal/chstore"
)

const (
	guidedEndpointCandidates guidedIntent = "endpoint_candidates"
	guidedEndpointTraces     guidedIntent = "endpoint_traces"
)

const (
	endpointCandidatesMax = 8
	endpointTracesLimit   = 20
	endpointWindowS       = 3600
)

// endpointCandidate — aday satırı; rota alanı (EndpointOptions) olarak
// ChatContext.LastRoute'a JSON yazılır.
type endpointCandidate struct {
	Service string  `json:"service"`
	Method  string  `json:"method,omitempty"`
	Path    string  `json:"path"`
	Calls   uint64  `json:"calls"`
	P99Ms   float64 `json:"p99Ms"`
}

var endpointCueStems = []string{"istek", "isteği", "istegi", "endpoint", "çağrı", "cagri", "request"}

// endpointStopWords — tanımlayıcı token olamayacak sözcükler (ipucu/fiil/
// zaman/nitelik). Servis ve env adları çağıranın kataloğundan elenir.
var endpointStopWords = map[string]bool{
	"son": true, "saat": true, "saatte": true, "saatteki": true, "saatlik": true, "dakika": true, "dakikada": true, "dakikalık": true,
	"bugün": true, "bugun": true, "dün": true, "dun": true, "için": true, "icin": true, "olan": true, "gelen": true, "giden": true,
	"tüm": true, "tum": true, "bütün": true, "butun": true, "hepsi": true, "servis": true, "servisi": true, "servisin": true,
	"servisinde": true, "servisteki": true, "endpoint": true, "endpointi": true, "endpointleri": true, "endpointlerini": true,
	"istek": true, "istekler": true, "istekleri": true, "isteklerini": true, "isteklerine": true, "isteği": true, "istegi": true,
	"çağrı": true, "çağrıları": true, "çağrılarını": true, "cagri": true, "request": true, "requests": true,
	"getir": true, "göster": true, "goster": true, "listele": true, "bul": true, "ara": true, "aç": true, "ac": true, "ver": true,
	"çıkar": true, "cikar": true, "lütfen": true, "lutfen": true, "hatalı": true, "hatali": true, "hata": true, "hatalar": true,
	"yavaş": true, "yavas": true, "trace": true, "traceler": true, "traceleri": true, "lerini": true, "leri": true, "ler": true,
	"ile": true, "bir": true, "bu": true, "şu": true, "su": true, "veya": true, "and": true, "the": true, "with": true,
	"gelenler": true, "gidenler": true, "geçen": true, "gecen": true, "içinde": true, "icinde": true,
}

var endpointPathRe = regexp.MustCompile(`(?:^|\s)(/[A-Za-z0-9._~%{}\-]+(?:/[A-Za-z0-9._~%{}\-]*)*)`)

func hasEndpointRequestSignal(toks []string) bool { return tokenHasPrefix(toks, endpointCueStems...) }

// extractEndpointRequest — SAF. raw: orijinal metin (yol harfini korumak için);
// norm: normalizeGuidedMsg; toks: guidedTokens(norm). ok=false → bu kademe
// değil. confirmed=true → doğrudan trace listesi (yol + trace kökü).
func extractEndpointRequest(raw, norm string, toks []string, services, envs []string, svc string) (query string, confirmed, ok bool) {
	if len(toks) == 0 || anyToken(toks, questionWords...) {
		return "", false, false
	}
	// Çift ŞEKLİ ("A'dan B'ye giden istekler") pair_requests'e kalır; sinyal
	// değil şekil (hasPairRequestSignal her istek/trace sözcüğünü işaretler).
	if _, _, pair := splitPairFragments(raw); pair {
		return "", false, false
	}
	if m := endpointPathRe.FindStringSubmatch(raw); m != nil && len(m[1]) >= 3 {
		switch {
		case tokenHasPrefix(toks, "trace"):
			return m[1], true, true
		case hasEndpointRequestSignal(toks):
			return m[1], false, true
		}
		return "", false, false
	}
	if !hasEndpointRequestSignal(toks) || hasSlowTraceSignal(norm) {
		return "", false, false // "en yavaş istekler" slow_traces'in
	}
	// Canlı servis adı ya da aile parçası taşıyan mesaj servis rotalarına kalır.
	if svc != "" && strings.Contains(norm, svc) {
		return "", false, false
	}
	if len(extractServiceFamily(norm, services, envs)) > 0 {
		return "", false, false
	}
	known := map[string]bool{}
	for _, s := range services {
		known[strings.ToLower(s)] = true
	}
	for _, e := range envs {
		known[strings.ToLower(e)] = true
	}
	for _, t := range toks {
		t = strings.Trim(t, ".,;:!?'\"")
		if len([]rune(t)) < 4 || endpointStopWords[t] || known[t] || findSuffixDebris[t] {
			continue
		}
		if !strings.ContainsFunc(t, func(r rune) bool { return r >= 'a' && r <= 'z' }) {
			continue
		}
		// Tanımlayıcı çözülen servisin bir parçasıysa ("checkout" → checkout-service)
		// bu bir endpoint değil servis sorusudur.
		if svc != "" && (strings.HasPrefix(svc, t) || strings.Contains(svc, "-"+t) || strings.Contains(svc, t+"-")) {
			return "", false, false
		}
		return t, false, true
	}
	return "", false, false
}

var affirmativeHeads = map[string]bool{"evet": true, "tamam": true, "hepsi": true, "hepsini": true, "olur": true, "ok": true, "okey": true, "getir": true}
var affirmativeBody = map[string]bool{
	"evet": true, "tamam": true, "hepsi": true, "hepsini": true, "hepsinin": true, "olur": true, "ok": true, "okey": true, "getir": true,
	"trace": true, "lerini": true, "leri": true, "ler": true, "lütfen": true, "lutfen": true, "tümünü": true, "tumunu": true, "tamamını": true, "tamamini": true,
}

// isAffirmative — SAF: evet/tamam/hepsi/olur/ok/getir ile başlayan, yabancı
// sözcük taşımayan kısa mesaj ("evet ama hataları" değil).
func isAffirmative(norm string) bool {
	toks := guidedTokens(norm)
	if len(toks) == 0 || len(toks) > 6 {
		return false
	}
	for i, t := range toks {
		t = strings.Trim(t, ".,;:!?")
		if i == 0 && !affirmativeHeads[t] {
			return false
		}
		if i > 0 && !affirmativeBody[t] {
			return false
		}
	}
	return true
}

// endpointAffirmativeRoute — SAF: son rota aday turuysa evet → aynı sorguyla
// trace listesi.
func endpointAffirmativeRoute(c ChatContext) (guidedRoute, bool) {
	if c.LastRoute == nil || c.LastRoute.Intent != guidedEndpointCandidates || c.LastRoute.SearchText == "" {
		return guidedRoute{}, false
	}
	r := *c.LastRoute
	r.Intent = guidedEndpointTraces
	return r, true
}

func endpointCandidateChips(route guidedRoute) []string {
	out := []string{"Evet, hepsinin trace'lerini getir"}
	seen := map[string]bool{}
	for _, c := range route.EndpointOptions {
		if c.Path == "" || seen[c.Path] {
			continue
		}
		seen[c.Path] = true
		out = append(out, c.Path+" trace'lerini getir")
	}
	return out
}

func endpointTraceFilter(query string, errorsOnly bool, env string, from, to time.Time) chstore.TraceFilter {
	return chstore.TraceFilter{
		Search: query, Env: env, From: from, To: to,
		Sort: "time", Order: "desc", Limit: endpointTracesLimit, CountMode: "skip", HasError: errorsOnly,
	}
}

func endpointTracesHref(query string, errorsOnly bool) string {
	q := url.Values{}
	q.Set("search", query)
	q.Set("sort", "time")
	q.Set("order", "desc")
	if errorsOnly {
		q.Set("hasError", "true")
	}
	return "/traces?" + q.Encode()
}

type traceListRow struct {
	TraceID     string  `json:"traceId"`
	StartTime   int64   `json:"startTime"`
	ServiceName string  `json:"serviceName"`
	RootName    string  `json:"rootName"`
	DurationMs  float64 `json:"durationMs"`
	SpanCount   uint64  `json:"spanCount"`
	HasError    bool    `json:"hasError"`
}

type traceListWindow struct {
	FromNs int64 `json:"fromNs"`
	ToNs   int64 `json:"toNs"`
}

// traceListPayload — trace_list bloğu (blocks.TypeTraceList; FE ChatTraceList).
type traceListPayload struct {
	Query     string          `json:"query"`
	Window    traceListWindow `json:"window"`
	Traces    []traceListRow  `json:"traces"`
	DeepLink  string          `json:"deepLink"`
	Truncated bool            `json:"truncated"`
}

func endpointTraceListPayload(query string, rows []chstore.TraceRow, from, to time.Time, deepLink string) traceListPayload {
	p := traceListPayload{Query: query, Window: traceListWindow{FromNs: from.UnixNano(), ToNs: to.UnixNano()}, Traces: []traceListRow{}, DeepLink: deepLink, Truncated: len(rows) >= endpointTracesLimit}
	for _, r := range rows {
		p.Traces = append(p.Traces, traceListRow{TraceID: r.TraceID, StartTime: r.StartTime, ServiceName: r.ServiceName, RootName: r.RootName, DurationMs: r.DurationMs, SpanCount: r.SpanCount, HasError: r.HasError})
	}
	return p
}

func endpointCandidatesFromRows(rows []chstore.EndpointRow, max int) []endpointCandidate {
	out := make([]endpointCandidate, 0, len(rows))
	for _, r := range rows {
		if len(out) >= max {
			break
		}
		out = append(out, endpointCandidate{Service: r.Service, Method: r.Method, Path: r.Path, Calls: r.Calls, P99Ms: r.P99Ms})
	}
	return out
}

// fmtCountTR — 1200 → "1.200" (Türkçe binlik).
func fmtCountTR(n uint64) string {
	s := fmt.Sprintf("%d", n)
	if len(s) <= 3 {
		return s
	}
	var b strings.Builder
	pre := len(s) % 3
	if pre > 0 {
		b.WriteString(s[:pre])
	}
	for i := pre; i < len(s); i += 3 {
		if b.Len() > 0 {
			b.WriteByte('.')
		}
		b.WriteString(s[i : i+3])
	}
	return b.String()
}

func renderEndpointCandidatesTR(query string, cands []endpointCandidate, rangeS int64) string {
	if len(cands) == 0 {
		return fmt.Sprintf("Son %s içinde yolunda %q geçen endpoint yok. Değeri farklı yazmayı dene ya da pencereyi genişlet (\"son 6 saate genişlet\").", fmtAgoTR(rangeS), query)
	}
	var b strings.Builder
	fmt.Fprintf(&b, "Son %s içinde yolunda %q geçen %d endpoint buldum:\n", fmtAgoTR(rangeS), query, len(cands))
	for _, c := range cands {
		fmt.Fprintf(&b, "- %s · %s — %s çağrı, p99 %.0f ms\n", c.Service, strings.TrimSpace(c.Method+" "+c.Path), fmtCountTR(c.Calls), c.P99Ms)
	}
	b.WriteString("Hepsinin trace'lerini getireyim mi? (evet / listeden bir tanesini seç)")
	return b.String()
}

func renderEndpointTracesTR(query string, cands []endpointCandidate, rows []chstore.TraceRow, rangeS int64, errorsOnly bool) string {
	kind := "trace"
	if errorsOnly {
		kind = "hatalı trace"
	}
	var b strings.Builder
	if len(rows) == 0 {
		fmt.Fprintf(&b, "Son %s içinde yolunda %q geçen %s bulunamadı.", fmtAgoTR(rangeS), query, kind)
		return b.String()
	}
	fmt.Fprintf(&b, "Son %s içinde yolunda %q geçen %d %s (en yeni önce):\n", fmtAgoTR(rangeS), query, len(rows), kind)
	if len(cands) > 0 {
		names := make([]string, 0, len(cands))
		for _, c := range cands {
			names = append(names, c.Service+" "+strings.TrimSpace(c.Method+" "+c.Path))
		}
		fmt.Fprintf(&b, "Endpoint'ler: %s\n", strings.Join(names, "; "))
	}
	for _, r := range rows {
		flag := ""
		if r.HasError {
			flag = ", HATA"
		}
		fmt.Fprintf(&b, "- %s · %.0fms — %s / %s (%d span%s) trace=%s\n",
			time.Unix(0, r.StartTime).UTC().Format("15:04:05"), r.DurationMs, r.ServiceName, r.RootName, r.SpanCount, flag, r.TraceID)
	}
	if len(rows) >= endpointTracesLimit {
		b.WriteString("İlk 20 satır; tümü Traces sayfasında.")
	}
	return b.String()
}

// guidedEndpointAnswer — LLM'siz cevap; okuma başarısızsa serbest döngüye bırakır.
func (s *Server) guidedEndpointAnswer(ctx context.Context, emit func(string, any), route guidedRoute, from, to time.Time, rangeS int64) (handled, ok bool) {
	query := route.SearchText
	answer := func(text string, r guidedRoute, open string) {
		ans := map[string]any{
			"text": text, "suggestions": guidedSuggestions(r),
			"links": dedupLinksByHref(guidedAnswerLinks(r, linkWindowBetween(from, to))),
		}
		if open != "" {
			ans["open"] = open
		}
		emit("answer", ans)
	}
	listEndpoints := func(step string) ([]endpointCandidate, error) {
		n := emitGuidedStep(emit, step, withEnvArg(fmt.Sprintf(`{"search":%q,"limit":%d,"range_s":%d}`, query, endpointCandidatesMax, rangeS), route.Env))
		rows, err := s.store.GetEndpointsMV(ctx, chstore.EndpointsQuery{From: from, To: to, Env: route.Env, Search: query, Limit: endpointCandidatesMax, Sort: "calls", Dir: "desc"})
		if err != nil {
			emitGuidedStepResult(emit, n, step, "", err)
			return nil, err
		}
		cands := endpointCandidatesFromRows(rows, endpointCandidatesMax)
		emitGuidedStepResult(emit, n, step, fmt.Sprintf("%d endpoint", len(cands)), nil)
		return cands, nil
	}
	if route.Intent == guidedEndpointCandidates {
		cands, err := listEndpoints("list_endpoints")
		if err != nil {
			return false, false
		}
		route.EndpointOptions = cands
		answer(renderEndpointCandidatesTR(query, cands, rangeS), route, "")
		return true, true
	}
	// endpoint_traces — adaylar rotada yoksa (çıplak "evet": LastRoute'ta yalnız
	// sorgu var) başlık için yeniden listelenir; MV, ucuz.
	if len(route.EndpointOptions) == 0 {
		if cands, err := listEndpoints("list_endpoints"); err == nil {
			route.EndpointOptions = cands
		}
	}
	n := emitGuidedStep(emit, "search_traces", withEnvArg(fmt.Sprintf(`{"search":%q,"sort":"time","limit":%d,"errors_only":%v,"range_s":%d}`, query, endpointTracesLimit, route.TraceErrorsOnly, rangeS), route.Env))
	rows, _, _, err := s.store.GetTraces(ctx, endpointTraceFilter(query, route.TraceErrorsOnly, route.Env, from, to))
	if err != nil {
		emitGuidedStepResult(emit, n, "search_traces", "", err)
		return false, false
	}
	href := endpointTracesHref(query, route.TraceErrorsOnly)
	text := renderEndpointTracesTR(query, route.EndpointOptions, rows, rangeS, route.TraceErrorsOnly)
	emitGuidedStepResult(emit, n, "search_traces", text, nil)
	emit("trace_list", endpointTraceListPayload(query, rows, from, to, href))
	answer(text, route, href)
	return true, true
}
