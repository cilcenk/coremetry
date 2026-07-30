package api

import (
	"net/url"
	"strings"
	"unicode/utf8"

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
}

// guidedSuggestions (v0.9.411) — cevap sonrası konuya-duyarlı takip
// önerileri. Frontend'in statik FOLLOWUPS çipleri her cevapta aynıydı;
// bunlar rotadan türetilir ("payments nasıl?" → "payments hata
// logları?"). HER öneri guided router'da kendi başına yönlenen bir
// şekildedir (testte pinli) — çipe tıklamak asla serbest döngüye
// düşürmez. Deterministik; LLM'e sorulmaz.
func guidedSuggestions(route guidedRoute) []string {
	svc := route.Service
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
	case guidedFamilyHealth:
		return []string{"Açık problemler?", "En yavaş trace'ler?", "Son 1 saatteki log hataları?"}
	case guidedMyServices:
		return []string{"Takımımın açık problemleri?", "En yavaş trace'ler?"}
	case guidedMyProblems:
		return []string{"Takımımın servisleri nasıl?", "En yavaş trace'ler?"}
	case guidedPodHealth:
		if svc != "" {
			return []string{svc + " sağlığı nasıl?", svc + " hata logları?", svc + " son deploy etkisi?"}
		}
		return []string{"Açık problemler?", "Takımımın servisleri nasıl?"}
	case guidedShiftSummary:
		return []string{"Açık problemler?", "En yavaş trace'ler?", "Takımımın servisleri nasıl?"}
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
}

// guidedAnswerLinks (v0.9.419, CoSRE fikir #4) — cevabın konusuna giden
// deterministik uygulama-içi linkler. LLM çıktısından DEĞİL rotadan
// üretilir (gemma4'e link biçimletmeyiz); frontend çip olarak çizer ve
// SPA navigate eder. Saf — copilot_followup_test.go.
func guidedAnswerLinks(route guidedRoute) []guidedAnswerLink {
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
		if svc != "" {
			return []guidedAnswerLink{{Label: "Loglar (error)", Href: "/logs?service=" + svcQ + "&minSev=17"}}
		}
		return []guidedAnswerLink{{Label: "Loglar (error)", Href: "/logs?minSev=17"}}
	case guidedFamilyHealth:
		return []guidedAnswerLink{{Label: "Servisler", Href: "/services"}}
	case guidedMyServices:
		return []guidedAnswerLink{{Label: "Servisler", Href: "/services"}}
	case guidedMyProblems:
		return []guidedAnswerLink{{Label: "Problemler", Href: "/problems"}}
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
func applyFollowUpContext(route guidedRoute, question string, prior []string, services, envs []string) (guidedRoute, int64, string, bool) {
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
		pr := routeGuidedIntent(p, services, envs, "")
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
