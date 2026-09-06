package api

import (
	"encoding/json"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/cilcenk/coremetry/internal/chstore"
	"github.com/cilcenk/coremetry/internal/copilot"
)

// copilot_followup.go — konuşma bağlamı devralma (v0.9.410, operatör
// istegi: "chat gerçek bir sohbet gibi olsun"). Guided router yalnız
// SON kullanıcı mesajına bakıyordu; "payments nasıl?" → cevap →
// "peki hata oranı?" ikinci soru servissiz kaldığı için serbest tool
// döngüsüne düşüyor ve küçük model (gemma4) orada bocalıyordu.
//
// İki devralma şekli — ikisi de DETERMİNİSTİK (LLM'e sorulmaz):
//   A) Mevcut soru kendi başına YÖNLENEMEZKEN ("peki payments?",
//      "son 24 saatte?") en yakın yönlenebilen önceki kullanıcı
//      mesajının rotası devralınır; mevcut sorudaki servis/env/range
//      varsa üstüne yazılır.
//   B) Mevcut soru yönlenmiş ama konusu BOŞ kalmışken ("peki hata
//      logları?") filo-geneli cevap yerine önceki turun servisi
//      doldurulur — "tüm/filo/all" gibi açık filo-kapsam kelimeleri
//      doldurmayı iptal eder (operatör bilerek genele çıkıyordur).
//
// Saf çekirdek applyFollowUpContext — copilot_followup_test.go ile
// tablo-testli. Şeffaflık: çağıran (copilotChatGuided) devralma
// olduğunda bir `step` chip'i emit eder.

// followUpMaxRunes — bundan uzun mesaj bağımsız bir sorudur, takip
// değil. Kısa tutulmuş ki "peki bu arada başka bir konu..." gibi
// serbest metinler önceki rotayı kaçırmasın.
const followUpMaxRunes = 80

// followUpScanDepth — geriye doğru en fazla bu kadar önceki kullanıcı
// mesajı taranır (rota + servis çözümü katalog × mesaj maliyeti).
const followUpScanDepth = 8

// priorUserTexts — aktif (son) kullanıcı sorusu HARİÇ, yeniden eskiye
// kullanıcı metinleri. Tool-result taşıyan boş-metin user turları
// atlanır (lastUserText ile aynı kural).
func priorUserTexts(msgs []copilot.ChatMessage) []string {
	out := make([]string, 0, followUpScanDepth)
	seenLast := false
	for i := len(msgs) - 1; i >= 0 && len(out) < followUpScanDepth; i-- {
		if msgs[i].Role != "user" || strings.TrimSpace(msgs[i].Text) == "" {
			continue
		}
		if !seenLast {
			seenLast = true
			continue
		}
		out = append(out, msgs[i].Text)
	}
	return out
}

// isFollowUpCue — normalize edilmiş mesaj bir takip sorusu ipucu
// taşıyor mu? Bilerek dar: bağlaç şekilleri ("peki", "aynı", iyelik
// zamirleri) ya da açık range ("son 24 saatte?"). Tek başına "bu"/
// "o"/"ya" YETERLİ DEĞİL — "peki bu ne demek?" gibi doküman/RAG
// sorularını kaçırmamak devralmaktan iyidir (deterministic beats
// clever). Uzun mesaj hiç takip sayılmaz.
func isFollowUpCue(norm string) bool {
	if utf8.RuneCountInString(norm) > followUpMaxRunes {
		return false
	}
	toks := guidedTokens(norm)
	if tokenHasPrefix(toks, "peki", "aynı", "ayni", "tekrar", "again", "onun", "bunun", "onu", "bunu") {
		return true
	}
	_, explicit := guidedRangeSExplicit(norm)
	return explicit
}

// wantsFleetScope — operatör açıkça filo genelini istiyor; servis
// doldurma (şekil B) iptal.
func wantsFleetScope(toks []string) bool {
	return tokenHasPrefix(toks, "tüm", "tum", "bütün", "butun", "hepsi", "filo", "fleet", "all", "genel", "her")
}

// followUpFillable — konusu boş kaldığında önceki turun servisiyle
// doldurulabilen (aksi halde sessizce filo-geneli cevaplayan)
// intent'ler. serviceHealth servissiz zaten yönlenemez; my* kimlikten,
// familyHealth aile listesinden gelir — üçü de doldurulmaz.
var followUpFillable = map[guidedIntent]bool{
	guidedProblems:     true,
	guidedSlowTraces:   true,
	guidedDeployImpact: true,
	guidedLogErrors:    true,
	guidedPodHealth:    true,
	guidedLogField:     true, // v0.10.433 (D5)
	guidedOpenPage:     true, // v0.10.434 (D7b)
	guidedTraceSearch:  true, // v0.10.436 (D2b)
	guidedCallPeriod:   true, // v0.10.438 (D3)
}

// guidedSuggestions (v0.9.411) — cevap sonrası konuya-duyarlı takip
// önerileri. Frontend'in statik FOLLOWUPS çipleri her cevapta aynıydı;
// bunlar rotadan türetilir ("payments nasıl?" → "payments hata
// logları?"). HER öneri guided router'da kendi başına yönlenen bir
// şekildedir (testte pinli) — çipe tıklamak asla serbest döngüye
// düşürmez. Deterministik; LLM'e sorulmaz.
func guidedSuggestions(route guidedRoute) []string {
	svc := route.Service
	// v0.9.1134 — "hangi takım?" turu HER ŞEYDEN önce: bundle takım
	// listesini rotaya yazdıysa (kimlik/takım çözülemedi) çipler o
	// takımların ÇIPLAK adları olur. Çipe tıklamak o adı yeni bir mesaj
	// olarak gönderir ve router çıplak adı guidedTeamServices'e yönlendirir
	// — diyalog sunucuda durum tutmadan kapanıyor. Intent'e bakılmaz:
	// my_services/my_problems/my_exceptions üçü de aynı degrade'i paylaşıyor.
	if len(route.TeamOptions) > 0 {
		return route.TeamOptions
	}
	// v0.10.429 (D1) — "hangi servis?" turu: çip = adayın TAM kılavuz cümlesi
	// (askServiceChip), sorulan niyetle; çıplak ad kapıdan geçmezdi.
	if len(route.ServiceOptions) > 0 {
		return askServiceChipFor(route) // v0.10.436 (D2) — çift/arama çipleri diğer yarıyı taşır
	}
	switch route.Intent {
	case guidedServiceHealth:
		return []string{
			svc + " en yavaş trace'ler?",
			svc + " hata logları?",
			svc + " son deploy etkisi?",
			svc + " pod'ları nasıl?",
		}
	case guidedProblems:
		if svc != "" {
			return []string{svc + " sağlığı nasıl?", svc + " hata logları?", svc + " en yavaş trace'ler?"}
		}
		return []string{"Takımımın açık problemleri?", "En yavaş trace'ler?", "Son 1 saatteki log hataları?"}
	case guidedSlowTraces:
		if svc != "" {
			return []string{svc + " sağlığı nasıl?", svc + " hata logları?", svc + " son deploy etkisi?"}
		}
		return []string{"Açık problemler?", "Takımımın servisleri nasıl?"}
	case guidedDeployImpact:
		if svc != "" {
			return []string{svc + " sağlığı nasıl?", svc + " en yavaş trace'ler?", svc + " hata logları?"}
		}
		return []string{"Açık problemler?", "En yavaş trace'ler?"}
	case guidedLogErrors:
		if svc != "" {
			return []string{svc + " problemleri?", svc + " sağlığı nasıl?", svc + " en yavaş trace'ler?"}
		}
		return []string{"Açık problemler?", "En yavaş trace'ler?"}
	case guidedLogField: // v0.10.433 (D5)
		if svc != "" {
			return []string{svc + " hata logları?", svc + " sağlığı nasıl?", svc + " en yavaş trace'ler?"}
		}
		return []string{"Açık problemler?", "En yavaş trace'ler?"}
	case guidedFamilyTraces: // v0.10.465 (D2) — üye başına drill-down (≤3 üye)
		var out []string
		for i, m := range route.Family {
			if i >= 3 {
				break
			}
			if route.TraceErrorsOnly {
				out = append(out, m+" hata logları?")
			} else {
				out = append(out, m+" sağlığı nasıl?")
			}
		}
		if len(out) == 0 {
			return []string{"Açık problemler?", "En yavaş trace'ler?"}
		}
		return out
	case guidedFindEntity: // v0.10.463 (D1) — kart: eylem çipleri; liste: bulunan adlar (çıplak ad → kart)
		if route.FindList {
			if len(route.TeamServices) > 0 {
				return route.TeamServices
			}
			return []string{"Açık problemler?", "En yavaş trace'ler?"}
		}
		if svc != "" {
			return []string{svc + " sağlığı nasıl?", svc + " en yavaş trace'ler?", svc + " hata logları?", svc + " sayfasını aç"}
		}
		return []string{"Açık problemler?", "En yavaş trace'ler?"}
	case guidedOpenPage: // v0.10.434 (D7b)
		if svc != "" {
			return []string{svc + " sağlığı nasıl?", svc + " problemleri?", svc + " hata logları?"}
		}
		return []string{"Açık problemler?", "En yavaş trace'ler?"}
	case guidedPairRequests: // v0.10.436 (D2a)
		out := []string{route.PairFrom + " sağlığı nasıl?", route.PairFrom + " en yavaş trace'ler?"}
		if route.PairToKind == "service" && route.PairTo != "" {
			out = append(out, route.PairTo+" sağlığı nasıl?")
		}
		return out
	case guidedTraceSearch: // v0.10.436 (D2b)
		if svc != "" {
			return []string{svc + " en yavaş trace'ler?", svc + " hata logları?", svc + " sağlığı nasıl?"}
		}
		return []string{"En yavaş trace'ler?", "Açık problemler?"}
	case guidedWindowCompare: // v0.10.437 (D6)
		if svc != "" {
			return []string{svc + " sağlığı nasıl?", svc + " en yavaş trace'ler?", svc + " son deploy etkisi?"}
		}
		return []string{"Açık problemler?"}
	case guidedCallPeriod: // v0.10.438 (D3)
		if route.PairFrom != "" && route.PairTo != "" {
			return []string{route.PairFrom + "'dan " + route.PairTo + "'ye giden istekler", route.PairFrom + " sağlığı nasıl?"}
		}
		if svc != "" {
			return []string{svc + " sağlığı nasıl?", svc + " en yavaş trace'ler?"}
		}
		return []string{"Açık problemler?"}
	case guidedFanout: // v0.10.439 (D4)
		return []string{route.PairFrom + "'dan " + route.PairTo + "'ye giden istekler", route.PairTo + " sağlığı nasıl?", route.PairTo + " en yavaş trace'ler?"}
	case guidedFamilyHealth:
		return []string{"Açık problemler?", "En yavaş trace'ler?", "Son 1 saatteki log hataları?"}
	case guidedMyServices:
		// v0.9.651 (operatör: "takımıma ait servisleri listeledikten
		// sonra SEÇECEĞİ servisle ilgili hatalar / logları / en yavaş
		// trace'leri") — çipler artık takımın GERÇEK servislerini
		// adlandırıyor.
		//
		// Öncesi jenerikti ("Açık problemler?") ve operatör bir servis
		// seçemiyordu: cevap servisleri sayıyor, çipler onlara
		// dokunmuyordu. Servis-kapsamlı çipler (svc + " hata logları?",
		// " en yavaş trace'ler?") ZATEN vardı — eksik olan tek halka
		// buydu.
		//
		// "sağlığı nasıl?" seçildi çünkü TEK tık ile service_health'e
		// giriyor ve ORASI dört drill-down'un hepsini açıyor (yavaş
		// trace, hata logları, deploy etkisi, pod'lar). Servis başına
		// iki ayrı çip koymak listeyi altıya çıkarır ve v0.9.579'da
		// kaldırılan "menü" hissini geri getirirdi.
		//
		// ÜÇ servisle sınırlı: takım 100 servis taşıyabiliyor.
		if n := len(route.TeamServices); n > 0 {
			out := make([]string, 0, 4)
			for i, sv := range route.TeamServices {
				if i >= 3 {
					break
				}
				out = append(out, sv+" sağlığı nasıl?")
			}
			return append(out, "Takımımın açık problemleri?")
		}
		return []string{"Takımımın açık problemleri?", "En yavaş trace'ler?"}
	case guidedMyProblems:
		return []string{"Takımımın servisleri nasıl?", "Takımımın exception'ları?", "En yavaş trace'ler?"}
	// v0.9.1134 — takım listesinden sonraki doğal adım EN KÖTÜ servise
	// inmek. TeamServices hata oranına göre sıralı geliyor, yani [0] en
	// kötü; my_services'in v0.9.651 dersini izliyor (jenerik çip operatörü
	// bir servis SEÇEMEZ hâlde bırakıyordu).
	case guidedTeamServices:
		if len(route.TeamServices) > 0 {
			worst := route.TeamServices[0]
			out := []string{worst + " sağlığı nasıl?", worst + " problemleri?", worst + " en yavaş trace'ler?"}
			if len(route.TeamServices) > 1 {
				out = append(out, route.TeamServices[1]+" sağlığı nasıl?")
			}
			return out
		}
		return []string{"Açık problemler?", "En yavaş trace'ler?"}
	// v0.9.650 — exception cevabından sonraki doğal adımlar: aynı takımın
	// açık PROBLEM'leri (farklı yüzey, aynı kapsam) ve servis sağlığı.
	case guidedMyExceptions:
		return []string{"Takımımın açık problemleri?", "Takımımın servisleri nasıl?"}
	case guidedPodHealth:
		if svc != "" {
			return []string{svc + " sağlığı nasıl?", svc + " hata logları?", svc + " son deploy etkisi?"}
		}
		return []string{"Açık problemler?", "Takımımın servisleri nasıl?"}
	case guidedShiftSummary:
		return []string{"Açık problemler?", "En yavaş trace'ler?", "Takımımın servisleri nasıl?"}
	// v0.9.1142 — kimlik çözüldüyse doğal adım O SERVİSE inmek. Servis
	// rotaya bundle tarafından yazılıyor (log kaydının servisi); yazılmadıysa
	// (kimlik bulunamadı) jenerik çip vermiyoruz — cevabın konusu tek bir
	// istek, filo geneli bir öneri konuyu dağıtırdı.
	case guidedRequestID:
		if svc != "" {
			return []string{svc + " sağlığı nasıl?", svc + " hata logları?", svc + " en yavaş trace'ler?"}
		}
		return nil
	case guidedDBHealth:
		return []string{"En yavaş trace'ler?", "Açık problemler?", "Son 1 saatteki log hataları?"}
	case guidedMessagingHealth:
		return []string{"En yavaş trace'ler?", "Açık problemler?"}
	}
	return nil
}

// guidedAnswerLink — cevap altındaki derin-link çipi (v0.9.419).
type guidedAnswerLink struct {
	Label string `json:"label"`
	Href  string `json:"href"`
	// ID (v0.10.35) — linkin türediği HAM kimlik; yalnız kimlik-avlayan
	// üreticiler dolduruyor (request_id). Arayüz bunu cevap METNİNDE
	// bulup satır içi link olarak sarıyor.
	//
	// ⚠ Neden sunucudan: hangi kimliğin linklenebilir olduğuna karar
	// veren mantık burada (anahtar-kelime kapısı, şablon geçerliliği,
	// env seçimi). İstemcide naif bir regex'le yeniden avlamak, aynı
	// kararı iki yerde vermek olurdu ve ikisi sessizce ayrışırdı.
	ID string `json:"id,omitempty"`
}

// inboxTeamExceptionsLink (v0.9.1246) — takım-filtreli exception kuyruğu
// çipi. ok=false → takım çözülmedi, çip YOK (yanlış kapsamlı bir link
// linksizlikten kötüdür).
//
// K4 ÖLÜ-PARAM DENETİMİ (v0.9.1130 sınıfı) — her iki param da hedefte
// GERÇEKTEN okunuyor ve bu sıra bilinçli: sayfa okuması ÖNCE geldi
// (v0.9.1246'nın ilk yarısı), köprü sonra.
//
//	kind=exception → Inbox.tsx searchParams.get('kind'), KIND_ALL içinde
//	team=<ad>      → Inbox.tsx readInboxTeam (lib/inboxUrl.ts,
//	                 INBOX_TEAM_PARAM) → useInbox({team}) → /api/inbox
//
// Ad KATALOG yazımıyla gider (mcptools.TeamDisplayName): sunucu tarafı
// zaten katlamalı eşleştiriyor ("sy" = "SY"), ama URL operatöre çip
// olarak görünüyor ve orada kataloğun yazımı doğru olan.
func inboxTeamExceptionsLink(team string) (guidedAnswerLink, bool) {
	if strings.TrimSpace(team) == "" {
		return guidedAnswerLink{}, false
	}
	return guidedAnswerLink{
		Label: team + " · Exceptions",
		Href:  "/inbox?kind=exception&team=" + url.QueryEscape(team),
	}, true
}

// guidedAnswerLinks (v0.9.419, CoSRE fikir #4) — cevabın konusuna giden
// deterministik uygulama-içi linkler. LLM çıktısından DEĞİL rotadan
// üretilir (gemma4'e link biçimletmeyiz); frontend çip olarak çizer ve
// SPA navigate eder. Saf — copilot_followup_test.go.
//
// v0.9.1321 (§3.1 K6) — pencere ZORUNLU argüman. Aşağıdaki 25 href
// satırından yalnız biri (request-ID log köprüsü) pencere yazıyordu;
// geri kalanı operatörü cevabın konuştuğu ana değil sticky penceresine
// götürüyordu. Burada `win linkWindow` bir parametre çünkü bunu bir
// grep-kapısıyla korumak, 26'ncı satırı yazan kişinin kuralı
// hatırlamasına bağlı olurdu; imza ise derlemeyi durdurur. Pencere
// TEK ÇIKIŞTA uygulanır (applyAll), yani yeni bir case eklemek onu
// düşüremez.
func guidedAnswerLinks(route guidedRoute, win linkWindow) []guidedAnswerLink {
	return win.applyAll(guidedAnswerLinkTargets(route))
}

// guidedAnswerLinkTargets — penceresiz HAM hedefler. guidedAnswerLinks
// dışından çağrılmamalı; link_window_test.go tek-çağıran sözleşmesini
// pinler (imzanın ifade edemediği tek şey bu).
func guidedAnswerLinkTargets(route guidedRoute) []guidedAnswerLink {
	svc := route.Service
	svcQ := url.QueryEscape(svc)
	switch route.Intent {
	case guidedServiceHealth:
		return []guidedAnswerLink{
			{Label: svc + " · Overview", Href: "/service?name=" + svcQ},
			{Label: "Trace'ler", Href: "/traces?service=" + svcQ},
		}
	case guidedProblems:
		if svc != "" {
			return []guidedAnswerLink{
				{Label: "Problemler", Href: "/problems?service=" + svcQ},
				{Label: svc + " · Overview", Href: "/service?name=" + svcQ},
			}
		}
		return []guidedAnswerLink{{Label: "Problemler", Href: "/problems"}}
	case guidedSlowTraces:
		if svc != "" {
			return []guidedAnswerLink{{Label: "Trace'ler (en yavaş)", Href: "/traces?service=" + svcQ + "&sort=duration"}}
		}
		return []guidedAnswerLink{{Label: "Trace'ler (en yavaş)", Href: "/traces?sort=duration"}}
	case guidedDeployImpact:
		if svc != "" {
			return []guidedAnswerLink{{Label: svc + " · Overview", Href: "/service?name=" + svcQ}}
		}
		return nil
	case guidedLogErrors:
		// v0.9.1130 — param adı `severity` (logsUrl.ts:58 okuyucusu).
		// Eski ad /logs'un HİÇ okumadığı bir paramdı → çip "error
		// logları" vaat edip tüm seviyeleri açıyordu (K4 ölü-param
		// sınıfı; kaynak-pin testi yasak adı adıyla arar, o yüzden
		// burada anılmıyor).
		if svc != "" {
			return []guidedAnswerLink{{Label: "Loglar (error)", Href: "/logs?service=" + svcQ + "&severity=17"}}
		}
		return []guidedAnswerLink{{Label: "Loglar (error)", Href: "/logs?severity=17"}}
	case guidedFamilyTraces: // v0.10.465 (D2) — aynı süzgeçle /traces
		label := "Trace'ler (en yavaş)"
		if route.TraceErrorsOnly {
			label = "Trace'ler (hatalı)"
		}
		out := []guidedAnswerLink{{Label: label, Href: familyTracesHref(route.Family, route.TraceErrorsOnly)}}
		for i, m := range route.Family {
			if i >= 2 {
				break
			}
			out = append(out, guidedAnswerLink{Label: m + " · Overview", Href: "/service?name=" + url.QueryEscape(m)})
		}
		return out
	case guidedFindEntity: // v0.10.463 (D1)
		if route.FindList {
			return []guidedAnswerLink{{Label: "Servisler", Href: "/services"}}
		}
		if svc == "" {
			return nil
		}
		return []guidedAnswerLink{
			{Label: svc + " · Overview", Href: "/service?name=" + svcQ},
			{Label: "Trace'ler", Href: "/traces?service=" + svcQ},
			{Label: "Loglar", Href: "/logs?service=" + svcQ},
		}
	case guidedOpenPage: // v0.10.434 (D7b) — sayfa türüne göre; overview özne ister
		withSvc := func(base string) string {
			if svc != "" {
				return base + "?service=" + svcQ
			}
			return base
		}
		switch route.Page {
		case "problems":
			return []guidedAnswerLink{{Label: "Problemler", Href: withSvc("/problems")}}
		case "logs":
			return []guidedAnswerLink{{Label: "Loglar", Href: withSvc("/logs")}}
		case "traces":
			return []guidedAnswerLink{{Label: "Trace'ler", Href: withSvc("/traces")}}
		case "endpoints":
			return []guidedAnswerLink{{Label: "Endpoint'ler", Href: withSvc("/endpoints")}}
		}
		if svc == "" {
			return nil
		}
		return []guidedAnswerLink{{Label: svc + " · Overview", Href: "/service?name=" + svcQ}}
	case guidedPairRequests: // v0.10.436 (D2a) — eş-görünüm linki (etiket "birlikte içeren")
		a, b := url.QueryEscape(route.PairFrom), url.QueryEscape(route.PairTo)
		if route.PairToKind == "service" {
			return []guidedAnswerLink{
				{Label: "Trace'ler (" + route.PairFrom + " ve " + route.PairTo + "'ı birlikte içeren)", Href: "/traces?services=" + a + "," + b + "&view=list&rootOnly=false"},
				{Label: "Servis haritası · " + route.PairFrom, Href: "/service-map?focus=" + a},
			}
		}
		return []guidedAnswerLink{
			{Label: "Trace'ler (" + route.PairFrom + ", " + route.PairTo + ")", Href: "/traces?service=" + a + "&search=" + b},
			{Label: "Servis haritası · " + route.PairFrom, Href: "/service-map?focus=" + a},
		}
	case guidedTraceSearch: // v0.10.436 (D2b)
		href := "/traces?search=" + url.QueryEscape(route.SearchText)
		if route.SearchSQL {
			fe, _ := json.Marshal([]chstore.FilterExpr{{Key: "db.statement", Op: "LIKE", Values: []string{route.SearchText}}})
			href = "/traces?filters=" + url.QueryEscape(string(fe))
		}
		if svc != "" {
			href += "&service=" + svcQ
		}
		return []guidedAnswerLink{{Label: "Trace'ler (arama)", Href: href}}
	case guidedFanout: // v0.10.439 (D4)
		a, b, c := url.QueryEscape(route.PairFrom), url.QueryEscape(route.PairTo), url.QueryEscape(route.FanoutTo)
		out := []guidedAnswerLink{{Label: "Servis haritası · " + route.PairTo, Href: "/service-map?focus=" + b}}
		if route.FanoutToKind == "service" {
			out = append([]guidedAnswerLink{{Label: "Trace'ler (" + route.PairFrom + ", " + route.PairTo + " ve " + route.FanoutTo + "'yi birlikte içeren)", Href: "/traces?services=" + a + "," + b + "," + c + "&view=list&rootOnly=false"}}, out...)
		} else {
			out = append([]guidedAnswerLink{{Label: "Trace'ler (" + route.PairFrom + " ve " + route.PairTo + "'yi birlikte içeren)", Href: "/traces?services=" + a + "," + b + "&view=list&rootOnly=false"}}, out...)
		}
		return out
	case guidedCallPeriod: // v0.10.438 (D3)
		a := route.PairFrom
		if a == "" {
			a = svc
		}
		if a == "" {
			return nil
		}
		out := []guidedAnswerLink{{Label: "Servis haritası · " + a, Href: "/service-map?focus=" + url.QueryEscape(a)}, {Label: a + " · Overview", Href: "/service?name=" + url.QueryEscape(a)}}
		if route.PairToKind == "service" && route.PairTo != "" {
			out = append(out, guidedAnswerLink{Label: route.PairTo + " · Overview", Href: "/service?name=" + url.QueryEscape(route.PairTo)})
		}
		return out
	case guidedWindowCompare: // v0.10.437 (D6) — her pencere kendi range'iyle (applyAll dokunmaz)
		if svc == "" || len(route.Windows) == 0 {
			return nil
		}
		loc := route.Windows[0].From.Location()
		var out []guidedAnswerLink
		for i, w := range route.Windows {
			out = append(out, guidedAnswerLink{
				Label: fmt.Sprintf("Pencere %d · %s", i+1, absWindowLabel(w, loc)),
				Href:  fmt.Sprintf("/service?name=%s&range=custom:%d-%d", svcQ, w.From.UnixMilli(), w.To.UnixMilli()),
			})
		}
		return out
	case guidedLogField: // v0.10.433 (D5) — bundle'ın koştuğu sorgu; yoksa backend'siz şekil
		q := route.LogQuery
		if q == "" {
			q, _ = logFieldSearchQuery(route.LogField, route.LogValue, route.LogContains, "")
		}
		href := "/logs?q=" + url.QueryEscape(q)
		if svc != "" {
			href += "&service=" + svcQ
		}
		return []guidedAnswerLink{{Label: "Loglar (" + route.LogField + ")", Href: href}}
	case guidedFamilyHealth:
		return []guidedAnswerLink{{Label: "Servisler", Href: "/services"}}
	case guidedMyServices:
		return []guidedAnswerLink{{Label: "Servisler", Href: "/services"}}
	case guidedMyProblems:
		return []guidedAnswerLink{{Label: "Problemler", Href: "/problems"}}
	// v0.9.1134 — takım cevabının derin linkleri.
	//
	// ÖLÜ-PARAM DENETİMİ (K4 sınıfı, v0.9.1130'un dersi): api istemcisi
	// takım süzgecini SUNUCUYA gönderiyor ama /services sayfası o iki
	// alanı URL'den OKUMUYOR — Services.tsx'te ikisi de `useState('')`,
	// searchParams yalnız page/compare/cluster/namespace için okunuyor.
	// Takım param'lı bir çip filtreli liste VAAT EDİP filtresiz listeyi
	// açardı; link o yüzden DÜZ /services ve daraltma yerine EN KÖTÜ
	// servisin kendi sayfası veriliyor (asıl gidilecek yer orası).
	// Sayfa URL okumasını kazanırsa (frontend işi) link yükseltilebilir.
	// Yasak param adları burada YAZILMIYOR — kaynak-pin testi onları
	// adıyla arıyor (copilot_team_services_test.go).
	case guidedTeamServices:
		links := []guidedAnswerLink{{Label: "Servisler", Href: "/services"}}
		if len(route.TeamServices) > 0 {
			worst := route.TeamServices[0]
			links = append(links, guidedAnswerLink{
				Label: worst + " · Overview", Href: "/service?name=" + url.QueryEscape(worst),
			})
		}
		// v0.9.1246 — adı geçen takımın exception'larına köprü. "X
		// takımının exception'ları" bu rotaya düşüyor (iyelik sinyali
		// yoksa my_* dalları açılmaz), ve cevap RED metrikleri
		// anlatırken operatörün asıl gideceği yer o takımın hata
		// kuyruğuydu — link olmadan elle owner/SRE seçmesi gerekiyordu.
		if l, ok := inboxTeamExceptionsLink(route.Team); ok {
			links = append(links, l)
		}
		return links
	case guidedMyExceptions:
		// Exceptions sekmesi Inbox'ta tür süzgeciyle açılıyor.
		//
		// v0.9.1246 (operatör: "Takımımın exceptionları dediğinde o takım
		// filtreli exceptions açabilir") — link artık TAKIMI da taşıyor.
		// Eskiden cevap takımın exception'larını sayıyor, link ise TÜM
		// filonun kuyruğunu açıyordu: operatör sayfada kendi takımını
		// aramak zorundaydı ve iki sayı (cevaptaki ile sayfadaki)
		// birbirini tutmuyordu.
		if l, ok := inboxTeamExceptionsLink(route.Team); ok {
			return []guidedAnswerLink{l}
		}
		return []guidedAnswerLink{{Label: "Exceptions", Href: "/inbox?kind=exception"}}
	case guidedPodHealth:
		if svc != "" {
			return []guidedAnswerLink{{Label: svc + " · Pods", Href: "/service?name=" + svcQ + "&tab=pods"}}
		}
		return nil
	case guidedShiftSummary:
		links := []guidedAnswerLink{{Label: "Inbox", Href: "/inbox"}, {Label: "Problemler", Href: "/problems"}}
		if svc != "" {
			links = append(links, guidedAnswerLink{Label: svc + " · Overview", Href: "/service?name=" + svcQ})
		}
		return links
	// v0.9.1142 — yapılandırılmış request kimliği rotası.
	//
	// Trace çözüldüyse asıl gidilecek yer trace detayı (/trace?id=).
	// Yanına loglar: `q` + `range=custom:<fromMs>-<toMs>` /logs'un
	// GERÇEKTEN okuduğu iki param (logsUrl.ts readLogsParams +
	// logsRangeParam) — ÖLÜ-PARAM DENETİMİ yapıldı, K4 sınıfı (v0.9.1130)
	// tekrar etmesin. Pencere kimliğin damgasından geliyor, yani çip
	// operatörü tam o ana götürür; bundle doldurmadıysa (çözülemedi)
	// pencere param'ı hiç YAZILMAZ — yanlış aralıklı bir link,
	// linksizlikten kötü.
	case guidedRequestID:
		var links []guidedAnswerLink
		if route.TraceID != "" {
			links = append(links, guidedAnswerLink{
				Label: "Trace", Href: "/trace?id=" + url.QueryEscape(route.TraceID),
			})
		}
		if svc != "" {
			links = append(links, guidedAnswerLink{
				Label: svc + " · Overview", Href: "/service?name=" + svcQ,
			})
		}
		if route.RequestID != "" && route.ReqWindowFromMs > 0 && route.ReqWindowToMs > route.ReqWindowFromMs {
			links = append(links, guidedAnswerLink{
				Label: "Loglar (istek penceresi)",
				Href: "/logs?q=" + url.QueryEscape(route.RequestID) +
					"&range=custom:" + strconv.FormatInt(route.ReqWindowFromMs, 10) +
					"-" + strconv.FormatInt(route.ReqWindowToMs, 10),
			})
		}
		return links
	case guidedDBHealth:
		return []guidedAnswerLink{{Label: "Databases", Href: "/databases"}}
	case guidedMessagingHealth:
		return []guidedAnswerLink{{Label: "Messaging", Href: "/messaging"}}
	}
	return nil
}

// applyFollowUpContext — devralma çekirdeği. route = mevcut sorunun
// kendi rotası (guidedNone olabilir). Dönenler: yeni rota, rangeS,
// devralınan temel mesaj (operasyon çözümü için) ve değişiklik bayrağı.
// changed=false → çağıran kendi route/rangeS'iyle devam eder.
// teams (v0.9.1134) — canlı takım kataloğu; önceki turun yeniden
// yönlendirilmesinde takım rotası da tanınsın diye taşınıyor ("avengersy
// takımı" → "peki son 24 saatte?").
func applyFollowUpContext(route guidedRoute, question string, prior []string, services, envs, teams []string) (guidedRoute, int64, string, bool) {
	msg := normalizeGuidedMsg(question)
	if !isFollowUpCue(msg) || len(prior) == 0 {
		return route, 0, "", false
	}
	toks := guidedTokens(msg)
	curSvc := extractServiceEntity(msg, services, envs)
	curEnv := extractEnvEntity(msg, envs)
	curRange, curExplicit := guidedRangeSExplicit(msg)

	// Şekil B: yönlenmiş ama konusuz — önceki turdan servis doldur.
	if route.Intent != guidedNone {
		if route.Service == "" && len(route.Family) == 0 &&
			followUpFillable[route.Intent] && !wantsFleetScope(toks) {
			for _, p := range prior {
				if svc := extractServiceEntity(normalizeGuidedMsg(p), services, envs); svc != "" {
					route.Service = svc
					rangeS := guidedRangeS(question)
					if !curExplicit {
						if v, ok := guidedRangeSExplicit(normalizeGuidedMsg(p)); ok {
							rangeS = v
						}
					}
					return route, rangeS, p, true
				}
			}
		}
		return route, 0, "", false
	}

	// Şekil A: hiç yönlenememiş takip sorusu. Devralmak için mevcut
	// mesajın SOMUT bir katkısı olmalı (servis/env/range ya da yarım
	// kalmış bir guided sinyali) — "peki bu ne demek?" gibi katkısız
	// sorular RAG/serbest döngüye kalır.
	if curSvc == "" && curEnv == "" && !curExplicit && !hasGuidedSignal(msg) {
		return route, 0, "", false
	}
	for _, p := range prior {
		pr := routeGuidedIntent(p, services, envs, teams, "")
		if pr.Intent == guidedNone {
			continue
		}
		if curSvc != "" {
			pr.Service, pr.Family = curSvc, nil
		}
		if curEnv != "" {
			pr.Env = curEnv
		}
		rangeS := int64(1800)
		if curExplicit {
			rangeS = curRange
		} else if v, ok := guidedRangeSExplicit(normalizeGuidedMsg(p)); ok {
			rangeS = v
		}
		return pr, rangeS, p, true
	}
	return route, 0, "", false
}

// askServiceChip — v0.10.429 (D1): aday servis + sorulan niyet → router'ın
// DETERMİNİSTİK olarak aynı niyete çözdüğü tam cümle (near_names_test
// gidiş-dönüşü pinler: çipe tıklamak serbest döngüye düşmez).
// askServiceChipFor — v0.10.436 (D2): çift/arama sorularının çipleri
// sorulan tarafın dışındaki yarıyı da taşır ki çip yeniden AYNI rotaya
// çözülsün ("checkout-service'dan payment-service'ye giden istekler").
func askServiceChipFor(route guidedRoute) []string {
	var out []string
	for _, opt := range route.ServiceOptions {
		switch route.AskIntent {
		case guidedPairRequests:
			if route.PairMissing == "to" {
				out = append(out, route.PairFrom+"'dan "+opt+"'ye giden istekler")
			} else {
				out = append(out, opt+"'dan "+route.PairTo+"'ye giden istekler")
			}
		case guidedTraceSearch:
			out = append(out, opt+" servisinde içinde \""+route.SearchText+"\" geçen trace'ler")
		case guidedLogField: // v0.10.443 — alan/değer çipe biner (eskiden varsayılan "sağlığı nasıl?")
			mode := "geçen"
			if !route.LogContains {
				mode = "olan"
			}
			out = append(out, opt+" loglarında "+route.LogField+" alanında \""+route.LogValue+"\" "+mode+" loglar")
		case guidedWindowCompare: // v0.10.437 (D6) — çip pencere metnini aynen taşır
			out = append(out, opt+" "+route.WindowText+" arası kıyas")
		case guidedCallPeriod: // v0.10.438 (D3)
			out = append(out, opt+" isteklerinde periyot var mı")
		case guidedFanout: // v0.10.439 (D4)
			out = append(out, opt+"'dan "+route.PairTo+"'ye gidenlerin hepsi "+route.FanoutTo+"'ye gidiyor mu")
		default:
			out = append(out, askServiceChip(route.AskIntent, opt))
		}
	}
	return out
}

func askServiceChip(intent guidedIntent, svc string) string {
	switch intent {
	case guidedRootCause:
		return svc + " neden yavaş?"
	case guidedSlowTraces:
		return svc + " en yavaş trace'ler?"
	case guidedDeployImpact:
		return svc + " son deploy etkisi?"
	case guidedLogErrors:
		return svc + " hata logları?"
	case guidedFindEntity: // v0.10.463 (D1) — çıplak tam ad bu kademede tek servise çözülür
		return svc
	case guidedOpenPage: // v0.10.434 (D7b)
		return svc + " sayfasını aç"
	case guidedTraceSearch: // v0.10.436 (D2b) — parça route'tan (askServiceChipFor)
		return svc + " servisinde içinde \"…\" geçen trace'ler"
	case guidedPodHealth:
		return svc + " pod'ları nasıl?"
	case guidedDBHealth:
		return svc + " db sorguları nasıl?"
	case guidedMessagingHealth:
		return svc + " kafka gecikmesi nasıl?"
	case guidedProblems:
		return svc + " problemleri?"
	}
	return svc + " sağlığı nasıl?"
}
