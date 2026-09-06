package api

// trace_nl_search.go — v0.10.436 (CoSRE router boşlukları D2): iki doğal
// dil trace sorusu.
//
// (a) pair_requests — "A'dan B'ye giden istekler(in tamamını göster)":
//     yönlü A→B sayısı topology_edges_5m'den (MV-first: A'nın çocuk
//     kenarları, B servis ya da dış/DB düğüm — "login external'dan osbprod'a"
//     gibi host hedefleri de kapsar), örnek trace'ler A ve B'yi BİRLİKTE
//     içeren trace'ler (RequireServices) ya da B düğümse A'nın içinde B
//     parçası geçen trace'ler (Search). Link /traces?services=A,B — eş-
//     görünüm: anlatım "birlikte içeren, doğrudan kenar garantisi değil"
//     der (FocusedNeighborhood v0.9.381 etiketi ile aynı dürüstlük).
//     Spec 2026-09-06 "200 trace'lik örnekten yönlü sayım" demişti; MV
//     kenarı hem tam hem ucuz (ham span üzerinden agregat = bug ilkesi),
//     örnek yalnız trace LİSTESİ için kullanılır.
// (b) trace_search — "X servisinden içinde <host/route/sorgu> geçen
//     trace'ler": servis (bulanık, D1 adayları) + serbest parça →
//     GetTraces Search (name + http_method + http_route + attr_values,
//     büyük/küçük harf duyarsız, TÜM jetonlar); parça SQL görünümlüyse
//     db.statement LIKE (db_statement haystack'te değil). Kimlik-önce
//     arama (v0.10.342) Search üzerinden zaten koşar.

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/cilcenk/coremetry/internal/chstore"
	"github.com/cilcenk/coremetry/internal/mcptools"
)

const guidedTraceSearchLimit = 10

var (
	// pairAblativeRe — "A'dan" / "A'den" / "A'tan" / "A’den" (ek apostroflu).
	pairAblativeRe = regexp.MustCompile(`^(.+?)['‘’](?:dan|den|tan|ten)$`)
	// pairDativeRe — "B'ye" / "B'ya" / "B'e" / "B'a".
	pairDativeRe = regexp.MustCompile(`^(.+?)['‘’](?:ye|ya|e|a)$`)
	pairArrowRe  = regexp.MustCompile(`^(.+?)\s*(?:->|→|=>)\s*(.+?)$`)
	sqlLikeRe    = regexp.MustCompile(`(?i)^\s*(select|insert|update|delete|with|merge)\b|\s(from|where|join)\s`)
)

func stripPairSuffix(tok string, re *regexp.Regexp) (string, bool) {
	if m := re.FindStringSubmatch(tok); m != nil {
		return m[1], true
	}
	return tok, false
}

// hasPairRequestSignal — istek/çağrı/trace sözcüğü ya da giden/gelen.
func hasPairRequestSignal(toks []string) bool {
	return tokenHasPrefix(toks, "istek", "çağrı", "cagri", "request", "call", "trace", "giden", "gelen", "atılan", "atilan", "yapılan", "yapilan")
}

// splitPairFragments — SAF: ham mesajı (kaynak parça, hedef parça) olarak
// böler. Şekiller: "A'dan B'ye …", "A servisinden B servisine …",
// "A servisinden B giden …", "from A to B", "A -> B". ok=false: çift yok.
func splitPairFragments(raw string) (from, to string, ok bool) {
	q := strings.TrimSpace(raw)
	if m := pairArrowRe.FindStringSubmatch(q); m != nil {
		return cleanPairFragment(m[1]), cleanPairFragment(strings.Fields(m[2])[0]), true
	}
	words := strings.Fields(q)
	fromEnd, toStart, toEnd := -1, -1, -1
	for i, w := range words {
		lw := strings.ToLower(w)
		if fromEnd < 0 {
			if lw == "servisinden" || lw == "servisten" || lw == "from" {
				if lw == "from" {
					// "from A to B requests": kaynak from'dan SONRA başlar, hedef
					// to'dan sonra ilk istek/çağrı sözcüğüne dek.
					for j := i + 1; j < len(words); j++ {
						if strings.ToLower(words[j]) == "to" {
							return cleanPairFragment(strings.Join(words[i+1:j], " ")), cleanPairFragment(strings.Join(cutAtRequestWord(words[j+1:]), " ")), j > i+1 && j+1 < len(words)
						}
					}
					return "", "", false
				}
				fromEnd = i // parça words[:i]
				toStart = i + 1
				continue
			}
			if base, hit := stripPairSuffix(w, pairAblativeRe); hit {
				words[i] = base
				fromEnd = i + 1
				toStart = i + 1
				continue
			}
			continue
		}
		if lw == "servisine" || lw == "servise" || lw == "to" {
			toEnd = i
			break
		}
		if base, hit := stripPairSuffix(w, pairDativeRe); hit {
			words[i] = base
			toEnd = i + 1
			break
		}
		if strings.HasPrefix(lw, "giden") || strings.HasPrefix(lw, "gelen") || strings.HasPrefix(lw, "atılan") || strings.HasPrefix(lw, "yapılan") || strings.HasPrefix(lw, "olan") {
			toEnd = i
			break
		}
	}
	if fromEnd <= 0 || toStart < 0 || toEnd <= toStart {
		return "", "", false
	}
	return cleanPairFragment(strings.Join(words[:fromEnd], " ")), cleanPairFragment(strings.Join(words[toStart:toEnd], " ")), true
}

// cutAtRequestWord — istek/çağrı/trace sözcüğünde keser ("payment requests" → "payment").
func cutAtRequestWord(words []string) []string {
	for i, w := range words {
		if hasPairRequestSignal([]string{strings.ToLower(w)}) {
			return words[:i]
		}
	}
	return words
}

// cleanPairFragment — parantezli açıklamayı ve noktalamayı atar.
func cleanPairFragment(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.Index(s, "("); i >= 0 {
		s = strings.TrimSpace(s[:i])
	}
	return strings.Trim(s, " ,.;:\"'")
}

// resolvePairSide — parça → canlı servis adayları; 1 aday çözüldü, 2+ sor,
// 0 servis değil (hedef tarafta dış düğüm olabilir).
func resolvePairSide(frag string, services, envs []string) []string {
	if frag == "" {
		return nil
	}
	// Tam (sınırlı) ad önce — "checkout-service" tireli tek jeton olarak
	// aday üreticinin parça eşine oturmaz; sonra bulanık adaylar.
	if exact := extractServiceEntity(normalizeGuidedMsg(frag), services, envs); exact != "" {
		return []string{exact}
	}
	return serviceCandidates(frag, services, envs, guidedServiceAskMax)
}

// nodeFragmentTokens — dış düğüm parçasının aranabilir jetonları (≥3,
// stopword değil): "osbprod" → ["osbprod"]; "osbprod.example.com" aynen.
func nodeFragmentTokens(frag string) []string {
	var out []string
	for _, t := range strings.Fields(strings.ToLower(frag)) {
		t = strings.Trim(t, "()[]{}\"',.;")
		if len(t) >= 3 && !guidedStopwords[t] {
			out = append(out, t)
		}
	}
	return out
}

// extractTraceSearch — "X servisinden içinde <parça> geçen trace'ler",
// "içinde "…" geçen trace'leri getir", "traces containing <parça>".
// Trace kökü ŞART (log'lar D5). Parça: tırnaklı değer ya da "içinde …
// geçen/olan/içeren" arası.
func extractTraceSearch(raw string, toks []string) (frag string, isSQL bool, ok bool) {
	if !tokenHasPrefix(toks, "trace") {
		return "", false, false
	}
	hasCue := tokenHasPrefix(toks, "içinde", "icinde", "geçen", "gecen", "içeren", "iceren", "contain", "with", "including")
	if !hasCue {
		return "", false, false
	}
	quoted := false
	if q, ok := findQuotedValue(raw); ok {
		frag, quoted = q, true
	} else {
		lw := strings.ToLower(raw)
		start := -1
		for _, cue := range []string{"içinde ", "icinde ", "containing ", "with "} {
			if i := strings.Index(lw, cue); i >= 0 {
				start = i + len(cue)
				break
			}
		}
		if start < 0 {
			return "", false, false
		}
		rest := raw[start:]
		end := len(rest)
		for _, stop := range []string{" geçen", " gecen", " olan", " içeren", " iceren", " trace"} {
			if i := strings.Index(strings.ToLower(rest), stop); i >= 0 && i < end {
				end = i
			}
		}
		frag = strings.TrimSpace(rest[:end])
	}
	frag = strings.Trim(frag, "\"'“”‘’` ")
	if frag == "" || len([]rune(frag)) > 256 {
		return "", false, false
	}
	// v0.10.443 — tırnaksız parça değer ŞEKLİNDE olmalı (host/yol/sorgu:
	// nokta, /, :, =, -, _ ya da rakam taşır, ya da SQL); "içinde hata olan
	// trace'ler" gibi düz sözcükler literal arama olmasın.
	if !quoted && !traceSearchValueOK(frag) {
		return "", false, false
	}
	return frag, sqlLikeRe.MatchString(frag), true
}

var traceSearchValueRe = regexp.MustCompile(`[./:=_\-0-9]`)

func traceSearchValueOK(frag string) bool {
	return traceSearchValueRe.MatchString(frag) || sqlLikeRe.MatchString(frag)
}

// traceSearchFilter — SAF: parça → TraceFilter (SQL parçası db.statement
// LIKE, gerisi haystack Search).
func traceSearchFilter(service, env, frag string, isSQL bool, from, to time.Time) chstore.TraceFilter {
	f := chstore.TraceFilter{Service: service, Env: env, From: from, To: to, Sort: "duration", Order: "desc", Limit: guidedTraceSearchLimit, CountMode: "skip"}
	if isSQL {
		f.Filters = []chstore.FilterExpr{{Key: "db.statement", Op: "LIKE", Values: []string{frag}}}
	} else {
		f.Search = frag
	}
	return f
}

// searchKeyPick — SAF (v0.10.476, F3-5): örneklem eşleşmelerinden aranacak
// anahtarlar — yalnız TAM eşleşenler, tipli/terfi kolonu olanlar önce, ≤3.
// Alt-dize eşleşmeleri (host url.full'un içinde) haystack aramasına kalır.
func searchKeyPick(matches []mcptools.AttrValueMatch) []string {
	var col, arr []string
	for _, m := range matches {
		if m.Match != "exact" {
			continue
		}
		if m.Column != "" {
			col = append(col, m.Key)
		} else {
			arr = append(arr, m.Key)
		}
	}
	out := append(col, arr...)
	if len(out) > 3 {
		out = out[:3]
	}
	return out
}

// mergeTraceRows — anahtar başına sonuçları trace_id ile tekilleştir, süreye
// göre sırala, limit.
func mergeTraceRows(sets [][]chstore.TraceRow, limit int) []chstore.TraceRow {
	seen := map[string]bool{}
	var out []chstore.TraceRow
	for _, rows := range sets {
		for _, r := range rows {
			if !seen[r.TraceID] {
				seen[r.TraceID] = true
				out = append(out, r)
			}
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].DurationMs > out[j].DurationMs })
	if len(out) > limit {
		out = out[:limit]
	}
	return out
}

func (s *Server) guidedTraceSearchBundle(ctx context.Context, emit func(string, any), route *guidedRoute, from, to time.Time, rangeS int64) (string, string, error) {
	errorsOnly := route.TraceErrorsOnly // v0.10.479 (F4-2) — "sadece hatalı olanlar" takibi
	// v0.10.476 (F3-5; audit kabul 3-4) — ÖNCE anahtar keşfi: değer hangi
	// attribute'ta? Servis kapsamlı örneklem (5000 span) + tam eşleşen
	// anahtarlar → anahtar başına ayrı süzgeçli arama (OR yerine; v0.10.343
	// dersi) → birleşim. Anahtar bulunamazsa eski haystack araması AYNEN.
	var keyNote string
	if !route.SearchSQL && route.Service != "" {
		nk := emitGuidedStep(emit, "find_attribute_by_value", fmt.Sprintf(`{"service":%q,"value":%q}`, route.Service, route.SearchText))
		attrs, aerr := s.store.GetScopedAttrs(ctx, chstore.AttrScope{Service: route.Service}, from, to, 100, 20)
		if aerr != nil {
			emitGuidedStepResult(emit, nk, "find_attribute_by_value", "", aerr)
		} else {
			matches := mcptools.MatchSamples(attrs, route.SearchText)
			keys := searchKeyPick(matches)
			var sub []string
			for _, m := range matches {
				if m.Match == "substring" {
					sub = append(sub, m.Key)
				}
			}
			route.SearchKeys = keys
			res := fmt.Sprintf("tam eşleşen anahtar: %s; alt-dize (örneklem): %s", strings.Join(keys, ", "), strings.Join(sub, ", "))
			emitGuidedStepResult(emit, nk, "find_attribute_by_value", res, nil)
			if len(keys) > 0 {
				keyNote = "Anahtar keşfi (servis örneklemi, 5000 span): tam eşleşme " + strings.Join(keys, ", ")
				if len(sub) > 0 {
					keyNote += "; alt-dize olarak " + strings.Join(sub, ", ")
				}
				var sets [][]chstore.TraceRow
				for _, k := range keys {
					f := traceSearchFilter(route.Service, route.Env, route.SearchText, false, from, to)
					f.Search = ""
					f.HasError = errorsOnly
					f.Filters = []chstore.FilterExpr{{Key: k, Op: "=", Values: []string{route.SearchText}}}
					n := emitGuidedStep(emit, "trace_search", fmt.Sprintf(`{"service":%q,"filter":{"key":%q,"op":"=","value":%q},"limit":%d}`, route.Service, k, route.SearchText, guidedTraceSearchLimit))
					rows, _, _, err := s.store.GetTraces(ctx, f)
					if err != nil {
						emitGuidedStepResult(emit, n, "trace_search", "", err)
						return "", "", err
					}
					emitGuidedStepResult(emit, n, "trace_search", fmt.Sprintf("%d trace (%s)", len(rows), k), nil)
					sets = append(sets, rows)
				}
				rows := mergeTraceRows(sets, guidedTraceSearchLimit)
				src := fmt.Sprintf("trace araması %s = %q (son %s%s; anahtar başına süzgeç, birleşim)", strings.Join(keys, " | "), route.SearchText, fmtAgoTR(rangeS), guidedScopeTR(route.Service, route.Env))
				return renderTraceSearchEvidenceTR(rows, *route, rangeS) + keyNote + "\n", src, nil
			}
		}
	}
	f := traceSearchFilter(route.Service, route.Env, route.SearchText, route.SearchSQL, from, to)
	f.HasError = errorsOnly
	args, _ := json.Marshal(map[string]any{"service": route.Service, "search": route.SearchText, "sql": route.SearchSQL, "limit": guidedTraceSearchLimit})
	n := emitGuidedStep(emit, "trace_search", string(args))
	rows, _, _, err := s.store.GetTraces(ctx, f)
	if err != nil {
		emitGuidedStepResult(emit, n, "trace_search", "", err)
		return "", "", err
	}
	emitGuidedStepResult(emit, n, "trace_search", fmt.Sprintf("%d trace", len(rows)), nil)
	src := fmt.Sprintf("trace araması %q (son %s%s; ad + http.route + attribute değerleri, büyük/küçük harf duyarsız)", route.SearchText, fmtAgoTR(rangeS), guidedScopeTR(route.Service, route.Env))
	if route.SearchSQL {
		src = fmt.Sprintf("trace araması db.statement LIKE %q (son %s%s)", route.SearchText, fmtAgoTR(rangeS), guidedScopeTR(route.Service, route.Env))
	}
	return renderTraceSearchEvidenceTR(rows, *route, rangeS), src, nil
}

func renderTraceSearchEvidenceTR(rows []chstore.TraceRow, route guidedRoute, rangeS int64) string {
	var b strings.Builder
	where := "ad/route/attribute değerlerinde"
	if route.SearchSQL {
		where = "db.statement içinde"
	}
	if len(route.SearchKeys) > 0 { // v0.10.476 (F3-5)
		where = strings.Join(route.SearchKeys, " | ") + " anahtarında (tam eşitlik)"
	}
	fmt.Fprintf(&b, "Trace araması — %s %q geçen trace'ler (son %s%s, en yavaş %d):\n", where, route.SearchText, fmtAgoTR(rangeS), guidedScopeTR(route.Service, route.Env), guidedTraceSearchLimit)
	if len(rows) == 0 {
		b.WriteString("Bu pencerede eşleşen trace yok — dürüstçe söyle; parça yazımı ya da servis farklı olabilir.\n")
		return b.String()
	}
	for _, r := range rows {
		flag := ""
		if r.HasError {
			flag = ", HATA"
		}
		fmt.Fprintf(&b, "- %.0fms — %s / %s (%d span%s) trace=%s\n", r.DurationMs, r.ServiceName, r.RootName, r.SpanCount, flag, r.TraceID)
	}
	b.WriteString("Yorum: kaç trace bulunduğunu, hata olup olmadığını ve ortak kök operasyonu söyle; listede olmayan trace ya da servis uydurma.\n")
	return b.String()
}

// guidedPairBundle — A→B: MV kenarları + örnek trace'ler.
func (s *Server) guidedPairBundle(ctx context.Context, emit func(string, any), route *guidedRoute, from, to time.Time, rangeS int64) (string, string, error) {
	a, b := route.PairFrom, route.PairTo
	n := emitGuidedStep(emit, "topology_edges", fmt.Sprintf(`{"from":%q,"to":%q}`, a, b))
	edges, err := s.store.ReadServiceTopologyAggForFocus(ctx, from, to, a, 1, 20000)
	if err != nil {
		emitGuidedStepResult(emit, n, "topology_edges", "", err)
		return "", "", err
	}
	matched, others := matchPairEdges(edges, a, b, route.PairToKind == "service")
	emitGuidedStepResult(emit, n, "topology_edges", fmt.Sprintf("%d kenar", len(matched)), nil)
	// Eşleşen düğüm adını rotaya yaz (link + çipler gerçek adı taşısın).
	if route.PairToKind != "service" && len(matched) > 0 {
		route.PairTo = strings.TrimPrefix(strings.TrimPrefix(strings.TrimPrefix(matched[0].ChildNode, "ext:"), "db:"), "q:")
	}
	var rows []chstore.TraceRow
	if len(matched) > 0 || route.PairToKind == "service" {
		f := chstore.TraceFilter{Service: a, Env: route.Env, From: from, To: to, Sort: "duration", Order: "desc", Limit: guidedTraceSearchLimit, CountMode: "skip"}
		if route.PairToKind == "service" {
			f.RequireServices = []string{a, b}
		} else {
			f.Search = route.PairTo
		}
		nt := emitGuidedStep(emit, "traces", fmt.Sprintf(`{"service":%q,"with":%q}`, a, route.PairTo))
		rows, _, _, err = s.store.GetTraces(ctx, f)
		if err != nil {
			emitGuidedStepResult(emit, nt, "traces", "", err)
			return "", "", err
		}
		emitGuidedStepResult(emit, nt, "traces", fmt.Sprintf("%d trace", len(rows)), nil)
	}
	src := fmt.Sprintf("topology_edges_5m (%s → %s, son %s) + örnek trace'ler", a, route.PairTo, fmtAgoTR(rangeS))
	return renderPairEvidenceTR(*route, matched, others, rows, rangeS), src, nil
}

// matchPairEdges — SAF: A'nın çocuk kenarlarından B'ye gidenler; B servis
// ise tam ad, düğümse parça eşleşmesi (ext:/db:/q: önekleri atılır).
// others: eşleşme yoksa "şu hedefler var" listesi için en çok çağrılan 8.
func matchPairEdges(edges []chstore.ServiceTopologyEdge, a, b string, bIsService bool) (matched, others []chstore.ServiceTopologyEdge) {
	frag := nodeFragmentTokens(b)
	for _, e := range edges {
		if e.ParentService != a {
			continue
		}
		child := strings.TrimPrefix(strings.TrimPrefix(strings.TrimPrefix(e.ChildNode, "ext:"), "db:"), "q:")
		hit := false
		if bIsService {
			hit = e.NodeKind == "service" && child == b
		} else {
			lc := strings.ToLower(child + " " + e.ExtDisplay)
			for _, t := range frag {
				if strings.Contains(lc, t) {
					hit = true
					break
				}
			}
		}
		if hit {
			matched = append(matched, e)
		} else {
			others = append(others, e)
		}
	}
	sort.Slice(others, func(i, j int) bool { return others[i].Calls > others[j].Calls })
	if len(others) > 8 {
		others = others[:8]
	}
	return matched, others
}

func renderPairEvidenceTR(route guidedRoute, matched, others []chstore.ServiceTopologyEdge, rows []chstore.TraceRow, rangeS int64) string {
	var b strings.Builder
	a, to := route.PairFrom, route.PairTo
	fmt.Fprintf(&b, "%s → %s istekleri (son %s):\n", a, to, fmtAgoTR(rangeS))
	if len(matched) == 0 {
		fmt.Fprintf(&b, "Bu pencerede %s'dan %s'a giden çağrı kenarı YOK (topology_edges_5m).", a, to)
		if len(others) > 0 {
			var names []string
			for _, e := range others {
				child := strings.TrimPrefix(strings.TrimPrefix(strings.TrimPrefix(e.ChildNode, "ext:"), "db:"), "q:")
				names = append(names, fmt.Sprintf("%s (%d)", child, e.Calls))
			}
			fmt.Fprintf(&b, " %s'ın hedefleri: %s.", a, strings.Join(names, ", "))
		}
		b.WriteString("\nYorum: kenar bulunmadığını söyle, hedef adı farklı yazılmış olabilir; hedef listesinden uygun olanı öner.\n")
		return b.String()
	}
	var calls, errs uint64
	var p99 float64
	for _, e := range matched {
		calls += e.Calls
		errs += e.Errors
		if e.P99Ms > p99 {
			p99 = e.P99Ms
		}
		proto := e.Protocol
		if proto == "" {
			proto = "-"
		}
		fmt.Fprintf(&b, "- %s → %s [%s/%s]: %d çağrı, %d hata (%.1f%%), ort %.0f ms, p99 %.0f ms\n", a, strings.TrimPrefix(strings.TrimPrefix(e.ChildNode, "ext:"), "db:"), e.NodeKind, proto, e.Calls, e.Errors, e.ErrorRate, e.AvgMs, e.P99Ms)
	}
	rate := 0.0
	if calls > 0 {
		rate = float64(errs) / float64(calls) * 100
	}
	fmt.Fprintf(&b, "Toplam: %d yönlü çağrı, %d hata (%.1f%%), p99 %.0f ms — 5 dk ön-toplamdan, tam sayım.\n", calls, errs, rate, p99)
	if len(rows) > 0 {
		if route.PairToKind == "service" {
			fmt.Fprintf(&b, "Örnek trace'ler (%s ve %s'ı BİRLİKTE içeren, en yavaş %d — doğrudan kenar garantisi değil, eş-görünüm):\n", a, to, len(rows))
		} else {
			fmt.Fprintf(&b, "Örnek trace'ler (%s içinde %q geçen, en yavaş %d):\n", a, to, len(rows))
		}
		for _, r := range rows {
			flag := ""
			if r.HasError {
				flag = ", HATA"
			}
			fmt.Fprintf(&b, "- %.0fms — %s / %s (%d span%s) trace=%s\n", r.DurationMs, r.ServiceName, r.RootName, r.SpanCount, flag, r.TraceID)
		}
	} else {
		b.WriteString("Örnek trace bulunamadı (kenar var ama pencerede eşleşen trace yok — örnekleme ya da saklama).\n")
	}
	b.WriteString("Yorum: sayı, hata oranı ve p99'u söyle; 'isteklerin tamamı' için linkteki listenin eş-görünüm (ikisini birlikte içeren trace'ler) olduğunu, doğrudan kenar garantisi vermediğini belirt; kanıtta olmayan servis/sayı uydurma.\n")
	return b.String()
}
