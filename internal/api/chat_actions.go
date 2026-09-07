package api

import (
	"net/url"
	"strings"
)

// chat_actions.go — v0.10.542 (CoSRE v2 Faz 3.4): `action` bloğu.
//
// Kabul 6: cevaptaki "Filtreyi uygula" düğmesi operatörün AÇIK OLDUĞU
// sayfanın filtresini yeni sekme açmadan günceller. Aksiyon YALNIZ tool
// sonucundan türeyen bir linkten üretilir (deep_link / arg-türevi köprü) —
// model metninden asla (audit §2.8: injection'ın yürütme kanalı kapalı).
// Güvenlik: kök-göreli href, aynı pathname; istemci URL'yi
// mergeOpenHref ile birleştirir (sayfa-sahipli param'lar yer değiştirir,
// yabancı param'lar korunur) ve replace:true ile gezinir — sayfa URL'yi
// kaynak-of-truth olarak yeniden okur.

// actionForLink — link hedefi operatörün açık sayfasıysa (pagePath) apply
// aksiyonu; değilse yok (link çipi zaten navigasyon). Saf; tablo-testli.
func actionForLink(l guidedAnswerLink, pagePath string) (map[string]any, bool) {
	if pagePath == "" || l.Href == "" || !strings.HasPrefix(l.Href, "/") || strings.HasPrefix(l.Href, "//") {
		return nil, false
	}
	u, err := url.Parse(l.Href)
	if err != nil || u.Path != pagePath || u.RawQuery == "" {
		return nil, false
	}
	return map[string]any{"kind": "apply", "href": l.Href, "label": applyLabelTR(pagePath)}, true
}

func applyLabelTR(pagePath string) string {
	switch pagePath {
	case "/traces", "/logs", "/explore", "/endpoints", "/inbox", "/problems":
		return "Filtreyi bu sayfada uygula"
	}
	return "Bu sayfada uygula"
}
