package api

// open_page.go — v0.10.434 (CoSRE router boşlukları D7b): "sayfasını aç".
// Prod /ai: operatör bir servisi sorduktan sonra "sayfasını aç" yazıyor,
// router hiçbir şey anlamayıp serbest döngüye düşüyordu. Şimdi
// deterministik: özne (mesaj → sayfa bağlamı → önceki tur) + sayfa türü
// (overview / problems / logs / traces / endpoints) → LLM'SİZ cevap: link
// çipleri + `open` alanı (frontend SPA içinde o sayfaya gider; sohbet açık
// kalır). Özne yoksa "hangisini kastettin?" (D1 ask_service, çip
// "X sayfasını aç").

import "strings"

// hasOpenPageSignal — sayfa/ekran sözcüğü (yalın/belirtme: "sayfasını",
// "sayfayı", "sayfa", "page" — bulunma "sayfasında" DEĞİL) + açma fiili
// (aç/git/götür/open/navigate — "göster/show" DEĞİL: "X sayfasında hataları
// göster" bir veri sorusudur, v0.10.443).
func hasOpenPageSignal(toks []string) bool {
	pageWord := false
	for _, t := range toks {
		switch t {
		case "sayfa", "sayfayı", "sayfayi", "sayfası", "sayfasi", "sayfasını", "sayfasini", "sayfasına", "sayfasina", "sayfaya",
			"page", "ekran", "ekranı", "ekrani", "ekranını", "ekranini", "ekranına", "ekranina", "ekrana":
			pageWord = true
		}
	}
	if !pageWord {
		return false
	}
	for _, t := range toks {
		switch t {
		case "aç", "ac", "açar", "açsana", "açalım", "açın", "açabilir", "açabilirsin", "açarmısın", "git", "gidelim", "take":
			return true
		}
		if strings.HasPrefix(t, "götür") || strings.HasPrefix(t, "gotur") || strings.HasPrefix(t, "open") || strings.HasPrefix(t, "navigate") ||
			strings.HasPrefix(t, "göster") || strings.HasPrefix(t, "goster") || strings.HasPrefix(t, "show") {
			return true
		}
	}
	return false
}

// openPageKind — hangi sayfa: log/problem/trace/endpoint kökü yoksa servis
// Overview'ı.
func openPageKind(toks []string) string {
	switch {
	case hasLogSignal(toks):
		return "logs"
	case hasProblemSignal(toks):
		return "problems"
	case tokenHasPrefix(toks, "trace"):
		return "traces"
	case tokenHasPrefix(toks, "endpoint", "uç", "uc"):
		return "endpoints"
	}
	return "overview"
}

func openPageLabelTR(kind string) string {
	switch kind {
	case "logs":
		return "Loglar"
	case "problems":
		return "Problemler"
	case "traces":
		return "Trace'ler"
	case "endpoints":
		return "Endpoint'ler"
	}
	return "Overview"
}

// openPageAnswerTR — deterministik cevap metni.
func openPageAnswerTR(route guidedRoute) string {
	label := openPageLabelTR(route.Page)
	if route.Service != "" {
		return route.Service + " · " + label + " sayfası açılıyor."
	}
	return label + " sayfası açılıyor."
}

// newestPriorService — önceki kullanıcı turlarında (yeniden eskiye) açıkça
// çözülen ilk servis; "sayfasını aç" gibi öznesiz komutların devralma
// kaynağı (applyFollowUpContext'in takip-ipucu şartı olmadan).
func newestPriorService(prior []string, services, envs []string) string {
	for i := len(prior) - 1; i >= 0; i-- {
		if svc := extractServiceEntity(normalizeGuidedMsg(prior[i]), services, envs); svc != "" {
			return svc
		}
	}
	return ""
}
