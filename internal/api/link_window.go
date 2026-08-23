package api

// link_window.go — v0.9.1320+1 (§3.1 K6). Backend'in ürettiği ürün
// linklerinin ZAMAN PENCERESİ.
//
// BUG: guidedAnswerLinks (25 site) ve toolCallLink (8 site) penceresiz
// href basıyordu. Yani AI cevabı "14:32'de payments p99 fırladı" dedikten
// sonra altındaki "Trace'ler →" çipi operatörü 14:32'ye değil sticky
// penceresine götürüyordu. Frontend'de aynı hatanın YAPISAL cevabı var —
// pivotHref ailesi pencereyi ZORUNLU argüman yapar (pivotHref.ts:17-19)
// ve o ailede penceresiz tek site yok. Backend'de böyle bir baskı yoktu:
// pencereyi yazan tek site copilot_followup.go'nun request-ID log
// köprüsüydü, geri kalan 32'si sessizce düşürüyordu.
//
// ÇÖZÜM ŞEKLİ: kapı değil İMZA. Grep eden bir Go kapısı, 34'üncü siteyi
// yazan kişinin onu görmesine bağlıdır; pencereyi parametre yapmak ise
// derlemeyi durdurur. Bu yüzden linkWindow bir ARGÜMAN — ve "pencere
// yok" da AÇIKÇA yazılmak zorunda (noLinkWindow()), sessizce "" geçmek
// yerine.
//
// K4 ÖLÜ-PARAM DENETİMİ (v0.9.1130 sınıfı — link, hedef sayfanın
// OKUMADIĞI paramı vaat edemez). Bu, "her href'e range ekle"nin neden
// YANLIŞ olduğu: ölçüldü (frontend kaynak taraması, useUrlRange /
// usePageZoomRange / <Topbar range=…>), üç hedefin zaman ekseni YOK —
// /inbox, /problems, /anomalies. Onlara range yazmak, operatöre
// tutulmayacak bir söz vermek olurdu.

import (
	"net/url"
	"strconv"
	"strings"
	"time"
)

// rangeReadingRoutes — `range=` param'ını GERÇEKTEN okuyan hedef yollar.
// Kaynak: frontend'de useUrlRange / usePageZoomRange çağıran sayfalar.
// Listede OLMAYAN bir yola pencere yazılmaz.
//
// Ölçüm (2026-08-24, frontend/src):
//
//	/service      pages/Service.tsx        useUrlRange ✓
//	/services     pages/Services.tsx       useUrlRange ✓
//	/traces       pages/Traces.tsx         useUrlRange ✓
//	/trace        pages/Trace.tsx          useUrlRange ✓
//	/logs         pages/Logs.tsx           useUrlRange ✓
//	/endpoints    pages/Endpoints.tsx      useUrlRange ✓
//	/databases    pages/Databases.tsx      useUrlRange ✓
//	/messaging    pages/Messaging.tsx      useUrlRange ✓
//	/service-map  pages/ServiceMap.tsx     useUrlRange ✓
//	/inbox        pages/Inbox.tsx          YOK — Topbar'da range prop'u bile yok
//	/problems     features/anomalies       YOK
//	/anomalies    features/anomalies       YOK
var rangeReadingRoutes = map[string]bool{
	"/service":     true,
	"/services":    true,
	"/traces":      true,
	"/trace":       true,
	"/logs":        true,
	"/endpoints":   true,
	"/databases":   true,
	"/messaging":   true,
	"/service-map": true,
}

// linkWindow — bir ürün linkine yazılacak pencere. Sıfır değeri
// "pencere yok" demektir ve çağıranın onu noLinkWindow() ile AÇIKÇA
// söylemesi beklenir.
type linkWindow struct {
	fromMs, toMs int64
	set          bool
}

// noLinkWindow — pencere yok. Bilinçli bir karar olarak yazılır:
// yanlış aralıklı bir link, linksiz bir linkten kötüdür
// (copilot_followup.go:355 aynı gerekçeyi taşıyor).
func noLinkWindow() linkWindow { return linkWindow{} }

// linkWindowBetween — cevabın ÜZERİNDE hesaplandığı mutlak pencere.
// Ters ya da sıfır uçlu bir aralık pencere SAYILMAZ: uydurma bir
// aralığı linke yazmaktansa hiç yazmamak doğrudur.
func linkWindowBetween(from, to time.Time) linkWindow {
	if from.IsZero() || to.IsZero() || !to.After(from) {
		return noLinkWindow()
	}
	return linkWindow{fromMs: from.UnixMilli(), toMs: to.UnixMilli(), set: true}
}

// linkWindowRelative — "son N saniye" biçimindeki bir sorgunun penceresi.
// Serbest tool döngüsü bunu kullanır: tool'lar range_s alır, yani
// gerçekten [now-range_s, now] aralığını sorgulamışlardır. rangeS<=0
// (arg verilmemiş) → pencere yok; tool'un varsayılanını TAHMİN etmek
// yanlış aralık yazmanın sessiz yoludur.
func linkWindowRelative(now time.Time, rangeS int64) linkWindow {
	if now.IsZero() || rangeS <= 0 {
		return noLinkWindow()
	}
	return linkWindowBetween(now.Add(-time.Duration(rangeS)*time.Second), now)
}

// apply — href'e `range=custom:<fromMs>-<toMs>` ekler.
//
// Üç durumda DOKUNMAZ:
//   - pencere yok (set=false),
//   - hedef yolun zaman ekseni yok (rangeReadingRoutes),
//   - href ZATEN bir range taşıyor — daha dar/özel bir pencereyi
//     (ör. request-ID log köprüsü) ezmek gerileme olurdu. Aynı kural
//     frontend'de navHref'te de var: "hedef her zaman kazanır".
func (w linkWindow) apply(href string) string {
	if !w.set {
		return href
	}
	base, query, _ := strings.Cut(href, "?")
	if !rangeReadingRoutes[base] {
		return href
	}
	// Anahtar ADIYLA bakılır; `strings.Contains(href, "range=")` bir
	// param DEĞERİNİN içine denk gelebilirdi.
	if q, err := url.ParseQuery(query); err == nil && q.Has("range") {
		return href
	}
	sep := "?"
	if query != "" {
		sep = "&"
	}
	return href + sep + "range=custom:" +
		strconv.FormatInt(w.fromMs, 10) + "-" + strconv.FormatInt(w.toMs, 10)
}

// applyAll — bir link diliminin tamamına pencereyi uygular. Üreticiler
// bunu TEK çıkışta çağırır, böylece yeni bir href satırı eklemek
// pencereyi düşürmez.
func (w linkWindow) applyAll(links []guidedAnswerLink) []guidedAnswerLink {
	for i := range links {
		links[i].Href = w.apply(links[i].Href)
	}
	return links
}
