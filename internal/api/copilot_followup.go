package api

import (
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
