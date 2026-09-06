package api

// find_entity.go — v0.10.463 (CoSRE sohbet paritesi D1): "bul / göster /
// listele" VARLIK kademesi. Operatör isteği (2026-09-06): "mobile bff
// dediğimde servisler arasında bulabilmeli."
//
// Ölçüm (v0.10.460 probu): çıplak ad ("mobile bff"), tam servis adı, "X'i
// bul", "X servisini göster", "servisleri listele" router'da HİÇBİR niyete
// oturmuyor → none → LLM sınıflandırıcısı / serbest döngü (yavaş, küçük
// modelde belirsiz). Burada üç deterministik cevap var, hiçbiri LLM
// çağırmaz (open_page emsali):
//   - TEK servis   → varlık kartı: ad, sahip takım (Service Catalog), son
//                    pencerede span hızı / hata oranı / p99, açık problem
//                    sayısı + Overview/Trace/Log linkleri + eylem çipleri.
//   - 2+ aday      → "Hangisini kastettin?" + adayların ÇIPLAK adları çip
//                    olarak (çıplak tam ad bu kademede tek servise çözülür —
//                    diyalog sunucuda durum tutmadan kapanır).
//   - liste sorusu → katalog boyutu + son pencerede en yoğun N servis tablosu.
//
// SIRA: routeGuidedIntent'in EN SONU — sağlık/hata/yavaş/log/deploy şekilleri
// kendi rotalarında kalır ("mobile bff hataları" aile rotası, "mobile bff
// yavaş traceler" ask_service). Yalnız hiçbir niyetin tanımadığı, AD-ŞEKİLLİ
// mesaj buraya düşer: her jeton ya bir servis adının parçası, ya bulma
// fiili, ya dolgu ("servisini", "var mı", "sahibi kim"), ya Türkçe ek
// artığı ("yi", "nin"). Açıklanamayan tek jeton → none (LLM'e bırak):
// "checkout müşterisi kim" varlık kartı DEĞİLDİR.

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/cilcenk/coremetry/internal/chstore"
	"github.com/cilcenk/coremetry/internal/mcptools"
)

// guidedFindEntity — varlık bulma niyeti (copilot_guided.go sabitlerinin
// kardeşi; dosyası burada ki tüm kademe tek yerde okunsun).
const guidedFindEntity guidedIntent = "find_entity"

// findEntityListMax — liste cevabındaki satır tavanı.
const findEntityListMax = 10

// familyListMax — v0.10.485: aile listesi tavanı (mobile*bff* gibi).
const familyListMax = 40

// findVerbs — bulma/gösterme fiilleri. "getir"/"aç" BİLEREK yok: "trace'lerini
// getir" trace yolunun, "sayfasını aç" open_page'in fiili; burada yalnız
// dolgu sayılırlar (findFiller), tek başlarına bulma sinyali üretmezler.
var findVerbs = map[string]bool{
	"bul": true, "bulur": true, "bulsana": true, "bulabilir": true, "bulabilirsin": true, "bulalım": true, "bulun": true,
	"ara": true, "arat": true, "arasana": true, "arayabilir": true,
	"göster": true, "gösterir": true, "gösterebilir": true, "göstersene": true, "gösterin": true,
	"nerede": true, "nerde": true, "hangi": true, "hangisi": true, "hangileri": true,
	"listele": true, "listeler": true, "listelesene": true, "liste": true, "listesi": true, "listesini": true,
	"find": true, "search": true, "lookup": true, "list": true, "where": true, "which": true, "locate": true,
}

// findFiller — varlık bilgisi taşımayan sözcükler (ad-şekil kapısında
// serbest). Stopword listesiyle (guidedStopwords) birlikte okunur.
var findFiller = map[string]bool{
	"servisini": true, "servisinin": true, "servisine": true, "servisler": true, "servisleri": true, "servislerin": true, "servislerini": true, "servislerimiz": true,
	"uygulama": true, "uygulaması": true, "uygulamasını": true, "app": true,
	"var": true, "yok": true, "mı": true, "mi": true, "mu": true, "mü": true, "misin": true, "mısın": true, "musun": true, "müsün": true,
	"ne": true, "nedir": true, "neler": true, "kaç": true, "hakkında": true, "bilgi": true, "bilgisi": true, "detay": true, "detayı": true, "detayları": true,
	"kim": true, "kimin": true, "sahibi": true, "sahip": true, "takımı": true, "ekibi": true, "owner": true, "team": true,
	"adlı": true, "isimli": true, "adında": true, "bir": true, "bu": true, "şu": true, "tüm": true, "bütün": true, "hepsi": true,
	"lütfen": true, "please": true, "bana": true, "bize": true, "me": true, "all": true,
	"getir": true, "aç": true, "açar": true, "açsana": true, "open": true,
}

// findSuffixDebris — kesme işaretiyle kopan Türkçe ekler (normalizeGuidedMsg
// "bff'yi" → "bff yi"). ≤2 karakterli her jeton zaten yok sayılır; 3'lükler
// burada.
var findSuffixDebris = map[string]bool{
	"nin": true, "nın": true, "nun": true, "nün": true, "den": true, "dan": true, "ten": true, "tan": true,
	"ile": true, "yle": true, "yla": true, "nda": true, "nde": true, "yin": true, "yın": true, "yun": true, "yün": true,
	// v0.10.470 — çoğul / bulunma / iyelik artıkları ("namespace'leri", "X'indeki").
	"ler": true, "lar": true, "leri": true, "ları": true, "lerin": true, "ların": true, "lere": true, "lara": true,
	"deki": true, "daki": true, "teki": true, "taki": true, "indeki": true, "ındaki": true, "undaki": true, "ündeki": true,
	"inde": true, "ında": true, "unda": true, "ünde": true, "ine": true, "ına": true, "une": true, "üne": true,
	"sini": true, "sını": true, "sinde": true, "sında": true, "sindeki": true, "sındaki": true, "lerde": true, "larda": true,
}

// trSuffixes — v0.10.485: Türkçe çoğul/iyelik/hâl ekleri (uzun önce). "bffleri"
// → "bff", "servisleri" → "servis". Yalnız kalan gövde ≥3 karakterse ve bir
// servis parçasına oturuyorsa kullanılır (kör kesme yok).
var trSuffixes = []string{"lerini", "larını", "lerin", "ların", "leri", "ları", "ler", "lar", "ini", "ını", "unu", "ünü", "nin", "nın", "nun", "nün", "de", "da", "te", "ta", "e", "a", "i", "ı", "u", "ü"}

// stripTRSuffix — jetonu, hits(gövde) doğru olana dek eklerden soyar; yoksa "".
func stripTRSuffix(t string, hits func(string) bool) string {
	for _, sfx := range trSuffixes {
		if strings.HasSuffix(t, sfx) {
			base := strings.TrimSuffix(t, sfx)
			if len(base) >= 3 && hits(base) {
				return base
			}
		}
	}
	return ""
}

// nearFiller — v0.10.485: yazım hatalı dolgu/fiil ("igetir" ≈ "getir",
// "listle" ≈ "listele"): ≥4 karakter ve mesafe ≤1.
func nearFiller(t string) bool {
	if len([]rune(t)) < 4 {
		return false
	}
	for w := range findFiller {
		if levenshtein(t, w) <= 1 {
			return true
		}
	}
	for w := range findVerbs {
		if levenshtein(t, w) <= 1 {
			return true
		}
	}
	return false
}

// familyFragments — v0.10.485: mesajın servis parçalarına oturan jetonları
// (ek soyulmuş) döndürür; "mobile bffleri listele" → [mobile bff].
func familyFragments(toks []string, envs []string, services []string) []string {
	envTok := map[string]bool{}
	for _, e := range envs {
		envTok[strings.ToLower(e)] = true
	}
	hitsAny := func(t string) bool {
		for _, s := range services {
			if tokenHitsService(t, strings.ToLower(s)) {
				return true
			}
		}
		return false
	}
	var out []string
	for _, t := range toks {
		if utf8.RuneCountInString(t) <= 2 || findSuffixDebris[t] || guidedStopwords[t] || findFiller[t] || findVerbs[t] || envTok[t] || nearFiller(t) {
			continue
		}
		if hitsAny(t) {
			out = append(out, t)
			continue
		}
		if b := stripTRSuffix(t, hitsAny); b != "" {
			out = append(out, b)
		}
	}
	return out
}

// familyMatches — SAF: TÜM parçaları segment/önek olarak taşıyan servisler (≤ max).
func familyMatches(frags []string, services []string, max int) []string {
	if len(frags) == 0 {
		return nil
	}
	var out []string
	for _, s := range services {
		ls := strings.ToLower(s)
		all := true
		for _, f := range frags {
			if !tokenHitsService(f, ls) {
				all = false
				break
			}
		}
		if all {
			out = append(out, s)
		}
	}
	sort.Strings(out)
	if len(out) > max {
		out = out[:max]
	}
	return out
}

func hasFindSignal(toks []string) bool {
	for _, t := range toks {
		if findVerbs[t] {
			return true
		}
	}
	return false
}

// isServiceListAsk — "servisleri listele" / "hangi servisler var" / "servis
// listesi" / "kaç servis var": ÇOĞUL ya da liste/sayı sözcüğü + servis kökü.
// Tekil "checkout servisini göster" liste DEĞİLDİR.
func isServiceListAsk(toks []string) bool {
	plural, listNoun, countQ, svcWord, fetchVerb := false, false, false, false, false
	for _, t := range toks {
		switch t {
		case "servisler", "servisleri", "servislerin", "servislerini", "servislerimiz", "services":
			plural = true
		case "kaç", "hangi", "hangileri", "neler", "tüm", "bütün", "hepsi", "all", "which":
			countQ = true
		case "getir", "göster", "gösterir", "ver", "çıkar", "dök", "fetch", "show", "get":
			fetchVerb = true
		}
		if strings.HasPrefix(t, "liste") || t == "list" {
			listNoun = true
		}
		if strings.HasPrefix(t, "servis") || strings.HasPrefix(t, "service") {
			svcWord = true
		}
		// v0.10.485 — yazım hatalı fiil ("igetir", "listle"): ≤1 mesafe.
		if !fetchVerb && !listNoun && len([]rune(t)) >= 4 {
			for _, w := range []string{"getir", "göster", "listele"} {
				if levenshtein(t, w) <= 1 {
					fetchVerb = true
				}
			}
		}
	}
	return (plural && (hasFindSignal(toks) || countQ || listNoun || fetchVerb)) || (svcWord && (listNoun || countQ))
}

// isListCue — v0.10.485: servis sözcüğü olmadan da liste isteği ("mobile
// bff'leri listele"): listele/liste/hangi + (çağıranın bulduğu) aile parçaları.
func isListCue(toks []string) bool {
	for _, t := range toks {
		if strings.HasPrefix(t, "liste") || t == "list" || t == "hangi" || t == "hangileri" || t == "neler" {
			return true
		}
	}
	return false
}

// tokenHitsService — jeton, adın sınırlı bir parçasına ya da parça önekine
// oturuyor mu (serviceCandidates ile aynı kural).
func tokenHitsService(t, svc string) bool {
	for _, sg := range nameTokens(svc) {
		if sg == t || strings.HasPrefix(sg, t) {
			return true
		}
	}
	return strings.EqualFold(t, svc)
}

// findEntityShaped — mesajın HER jetonu açıklanabiliyor mu: ad parçası,
// bulma fiili, dolgu, stopword, env adı ya da ek artığı. Açıklanamayan tek
// jeton → false; hiç ad-parçası yoksa (yalnız fiil/dolgu) → false.
func findEntityShaped(toks []string, envs []string, hits func(string) bool) bool {
	envTok := map[string]bool{}
	for _, e := range envs {
		envTok[strings.ToLower(e)] = true
	}
	named := false
	for _, t := range toks {
		if utf8.RuneCountInString(t) <= 2 || findSuffixDebris[t] || guidedStopwords[t] || findFiller[t] || findVerbs[t] || envTok[t] || nearFiller(t) {
			continue
		}
		if hits(t) || stripTRSuffix(t, hits) != "" { // v0.10.485 — "bffleri" → "bff"
			named = true
			continue
		}
		return false
	}
	return named
}

// routeFindEntity — routeGuidedIntent'in son basamağı (hiçbir niyet
// eşleşmediğinde). svc: mesajdan çözülen açık servis (ctxService devri
// DAHİL — "bul" tek başına servis sayfasında o servisi açıklar).
func routeFindEntity(msg string, toks []string, svc, env string, services, envs []string) (guidedRoute, bool) {
	if len(toks) == 0 || len(toks) > 12 {
		return guidedRoute{}, false
	}
	explicit := extractServiceEntity(msg, services, envs)
	if explicit == "" {
		// v0.10.485 (operatör: "mobile bff servisleri listele → RAG'a gidiyor"):
		// parçalı ad + liste/getir sözcüğü = AİLE listesi ("mobile*bff*" servisleri);
		// servis sözcüğü olmadan da ("mobile bff'leri listele").
		frags := familyFragments(toks, envs, services)
		listAsk := isServiceListAsk(toks)
		if (listAsk || (isListCue(toks) && len(frags) > 0)) && len(frags) > 0 && len(familyMatches(frags, services, familyListMax)) > 0 {
			return guidedRoute{Intent: guidedFindEntity, FindList: true, FindQuery: strings.Join(frags, " "), Env: env}, true
		}
		if listAsk {
			return guidedRoute{Intent: guidedFindEntity, FindList: true, Env: env}, true
		}
	}
	if svc != "" {
		ls := strings.ToLower(svc)
		if findEntityShaped(toks, envs, func(t string) bool { return tokenHitsService(t, ls) || strings.Contains(ls, t) }) {
			return guidedRoute{Intent: guidedFindEntity, Service: svc, Env: env}, true
		}
		return guidedRoute{}, false
	}
	opts := serviceCandidates(msg, services, envs, guidedServiceAskMax)
	if len(opts) == 0 {
		// v0.10.485 — ek soyulmuş parçalar ("mobile bffleri"): aile eşleşmesi.
		if frags := familyFragments(toks, envs, services); len(frags) > 0 {
			opts = familyMatches(frags, services, guidedServiceAskMax)
		}
	}
	if len(opts) == 0 {
		return guidedRoute{}, false
	}
	if !findEntityShaped(toks, envs, func(t string) bool {
		for _, s := range services {
			if tokenHitsService(t, strings.ToLower(s)) {
				return true
			}
		}
		return false
	}) {
		return guidedRoute{}, false
	}
	if len(opts) == 1 {
		return guidedRoute{Intent: guidedFindEntity, Service: opts[0], Env: env}, true
	}
	return guidedRoute{Intent: guidedFindEntity, ServiceOptions: opts, AskIntent: guidedFindEntity, Env: env, FindQuery: strings.TrimSpace(msg)}, true
}

// ── cevap ────────────────────────────────────────────────────────────

// findEntityCard — tek servisin kartı; SAF (test edilir), veri parçaları
// isteğe bağlı (okuma başarısızsa satır "bilinmiyor" der, kart yine çıkar).
type findEntityCard struct {
	Service   string
	OwnerTeam string
	SRETeam   string
	MetaOK    bool
	Row       *chstore.ServiceSummary // nil = okunamadı
	RangeS    int64
	Problems  int
	ProbOK    bool
}

func renderFindEntityCard(c findEntityCard) string {
	var b strings.Builder
	fmt.Fprintf(&b, "**%s**\n", c.Service)
	switch {
	case !c.MetaOK:
		b.WriteString("- Sahip takım: okunamadı\n")
	case c.OwnerTeam == "" && c.SRETeam == "":
		b.WriteString("- Sahip takım: atanmamış (Service Catalog)\n")
	default:
		fmt.Fprintf(&b, "- Sahip takım: %s", orDash(c.OwnerTeam))
		if c.SRETeam != "" {
			fmt.Fprintf(&b, " · SRE: %s", c.SRETeam)
		}
		b.WriteString("\n")
	}
	win := fmtAgoTR(c.RangeS)
	switch {
	case c.Row == nil:
		fmt.Fprintf(&b, "- Son %s: RED okunamadı\n", win)
	case c.Row.SpanCount == 0:
		fmt.Fprintf(&b, "- Son %s: span verisi yok\n", win)
	default:
		rate := float64(c.Row.SpanCount) / float64(maxInt64(c.RangeS, 1))
		fmt.Fprintf(&b, "- Son %s: %.1f span/s · hata %%%.2f (%d) · p99 %.0f ms\n", win, rate, c.Row.ErrorRate, c.Row.ErrorCount, c.Row.P99Ms)
	}
	if c.ProbOK {
		if c.Problems == 0 {
			b.WriteString("- Açık problem yok\n")
		} else {
			fmt.Fprintf(&b, "- Açık problem: %d\n", c.Problems)
		}
	}
	return b.String()
}

func maxInt64(a, b int64) int64 {
	if a > b {
		return a
	}
	return b
}

// renderFindEntityList — katalog boyutu + en yoğun N servis (span sayısına
// göre). SAF.
func renderFindEntityList(total int, rows []chstore.ServiceSummary, rangeS int64) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Katalogda **%d servis** var.", total)
	if len(rows) == 0 {
		fmt.Fprintf(&b, " Son %s içinde span verisi olan servis yok.\n", fmtAgoTR(rangeS))
		return b.String()
	}
	fmt.Fprintf(&b, " Son %s içinde en yoğun %d:\n\n| Servis | span/s | hata %% | p99 ms |\n|---|---:|---:|---:|\n", fmtAgoTR(rangeS), len(rows))
	for _, r := range rows {
		rate := float64(r.SpanCount) / float64(maxInt64(rangeS, 1))
		fmt.Fprintf(&b, "| %s | %.1f | %.2f | %.0f |\n", r.Name, rate, r.ErrorRate, r.P99Ms)
	}
	return b.String()
}

// renderFamilyServiceList — v0.10.485: aile listesi (ad + son pencere RED;
// pencerede verisi olmayan üye "—" ile yine listelenir — katalog gerçeği).
func renderFamilyServiceList(q string, names []string, rows []chstore.ServiceSummary, rangeS int64) string {
	byName := map[string]chstore.ServiceSummary{}
	for _, r := range rows {
		byName[r.Name] = r
	}
	var b strings.Builder
	fmt.Fprintf(&b, "**%s** ile eşleşen **%d servis** (son %s):\n\n| Servis | span/s | hata %% | p99 ms |\n|---|---:|---:|---:|\n", q, len(names), fmtAgoTR(rangeS))
	for _, n := range names {
		if r, ok := byName[n]; ok && r.SpanCount > 0 {
			rate := float64(r.SpanCount) / float64(maxInt64(rangeS, 1))
			fmt.Fprintf(&b, "| %s | %.1f | %.2f | %.0f |\n", n, rate, r.ErrorRate, r.P99Ms)
		} else {
			fmt.Fprintf(&b, "| %s | — | — | — |\n", n)
		}
	}
	if len(names) >= familyListMax {
		fmt.Fprintf(&b, "\nİlk %d gösterildi; daraltmak için parça ekle.\n", familyListMax)
	}
	return b.String()
}

func renderFindEntityAsk(q string, opts []string) string {
	if q != "" {
		return fmt.Sprintf("%q birden çok servise oturuyor (%d aday). Hangisini kastettin? Aşağıdaki çiplerden seç ya da tam adı yaz.", q, len(opts))
	}
	return fmt.Sprintf("Birden çok servis eşleşti (%d aday). Hangisini kastettin? Aşağıdaki çiplerden seç ya da tam adı yaz.", len(opts))
}

// guidedFindEntityAnswer — deterministik cevap; LLM yok, exchangeId yok
// (open_page emsali). Bulunan liste rotaya yazılır (çipler için).
func (s *Server) guidedFindEntityAnswer(ctx context.Context, emit func(string, any), route guidedRoute, from, to time.Time, rangeS int64) (handled, ok bool) {
	answer := func(text string, r guidedRoute) {
		emit("answer", map[string]any{
			"text": text, "suggestions": guidedSuggestions(r),
			"links": dedupLinksByHref(guidedAnswerLinks(r, linkWindowBetween(from, to))),
		})
	}
	switch {
	case route.FindList && strings.TrimSpace(route.FindQuery) != "":
		// v0.10.485 — AİLE listesi: parçaların hepsini taşıyan servisler + RED.
		frags := strings.Fields(strings.ToLower(route.FindQuery))
		names := familyMatches(frags, s.guidedServiceNames(ctx), familyListMax)
		n := emitGuidedStep(emit, "list_services", fmt.Sprintf(`{"name_contains":%q,"limit":%d}`, route.FindQuery, familyListMax))
		if len(names) == 0 {
			emitGuidedStepResult(emit, n, "list_services", "eşleşen servis yok", nil)
			answer(fmt.Sprintf("Katalogda %q parçalarını taşıyan servis yok.", route.FindQuery), route)
			return true, true
		}
		rows, err := mcptools.ReadTeamServicesRED(ctx, s.mcpDeps(), names, from, to)
		if err != nil {
			emitGuidedStepResult(emit, n, "list_services", "", err)
			return false, false
		}
		text := renderFamilyServiceList(route.FindQuery, names, rows, rangeS)
		emitGuidedStepResult(emit, n, "list_services", text, nil)
		route.TeamServices = names
		if len(route.TeamServices) > 8 {
			route.TeamServices = route.TeamServices[:8]
		}
		answer(text, route)
		return true, true
	case route.FindList:
		n := emitGuidedStep(emit, "list_services", fmt.Sprintf(`{"sort":"spanCount","limit":%d}`, findEntityListMax))
		total := len(s.guidedServiceNames(ctx))
		rows, err := s.store.GetServicesAggFiltered(ctx, from, to, "", "spanCount", "desc", findEntityListMax, 0)
		if err != nil {
			emitGuidedStepResult(emit, n, "list_services", "", err)
			return false, false // serbest döngü kendi tool'uyla dener
		}
		sort.SliceStable(rows, func(i, j int) bool { return rows[i].SpanCount > rows[j].SpanCount })
		text := renderFindEntityList(total, rows, rangeS)
		emitGuidedStepResult(emit, n, "list_services", text, nil)
		for _, r := range rows {
			route.TeamServices = append(route.TeamServices, r.Name)
		}
		answer(text, route)
		return true, true
	case len(route.ServiceOptions) > 0:
		answer(renderFindEntityAsk(route.FindQuery, route.ServiceOptions), route)
		return true, true
	case route.Service == "":
		return false, false
	}
	svc := route.Service
	n := emitGuidedStep(emit, "find_service", `{"service":`+jsonStr(svc)+`}`)
	card := findEntityCard{Service: svc, RangeS: rangeS}
	if meta, err := s.store.GetServiceMetadata(ctx, svc); err == nil && meta != nil {
		card.MetaOK, card.OwnerTeam, card.SRETeam = true, meta.OwnerTeam, meta.SRETeam
	}
	if rows, err := mcptools.ReadTeamServicesRED(ctx, s.mcpDeps(), []string{svc}, from, to); err == nil {
		if len(rows) > 0 {
			r := rows[0]
			card.Row = &r
		} else {
			card.Row = &chstore.ServiceSummary{Name: svc}
		}
	}
	// Toplam yalnız KESİNSE yazılır (problemsTotal.known); bilinmiyorsa satır
	// hiç çizilmez — kesin olmayan sayı kartta yalan olurdu.
	if _, total, err := s.guidedProblemsWithTotal(ctx, guidedProblemFilter(svc, route.Env, 1)); err == nil && total.known {
		card.ProbOK, card.Problems = true, int(total.n)
	}
	text := renderFindEntityCard(card)
	emitGuidedStepResult(emit, n, "find_service", text, nil)
	answer(text, route)
	return true, true
}
