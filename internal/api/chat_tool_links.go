package api

// chat_tool_links.go — v0.9.1228 (CoSRE denetimi, chat-entegrasyon):
// serbest tool-döngüsünün çağrılarını ürün görünümüne bağlayan köprü.
// Guided yol v0.9.419'dan beri rota-türevi linkler basıyor
// (guidedAnswerLinks); free-loop, drawer'daki çipler ve cevaplar ise
// çıkmaz sokaktı — model hangi tool'u hangi arg'la çağırdığını sunucu
// YAPISAL biliyorken operatör bulguyu üründe açamıyordu.
//
// GİRDİ GÜVENİ: args HAM tc.Input'tur (modelin ürettiği JSON) —
// render_chart intercept'inin uyarısı buraya da geçerli: değere
// GÜVENİLMEZ, yalnız beklenen alan çekilir ve url.QueryEscape'ten
// geçer. Var olmayan bir servis adı en kötü boş liste açar — href
// asla SQL/komut değildir.
//
// K4 ÖLÜ-PARAM DENETİMİ (v0.9.1130 sınıfı — link, hedef sayfanın
// OKUMADIĞI paramı vaat edemez; her hedef koddan doğrulandı):
//   /service?name=        Sidebar/Overview'un kanonik paramı (guided emsali)
//   /endpoints?service=   Endpoints.tsx:190 searchParams.get('service')
//   /traces?service=      Traces.tsx:234
//   /logs?service=&q=     logsUrl.ts:42 'service', :44 'q'
//   /problems?service=    guidedProblems emsali (v0.9.419 zinciri)
//   /inbox?kind=&service= Inbox.tsx:144 'kind' (KIND_ALL 'exception'
//                         içerir, :51), :149 'service'
//   /service-map          App.tsx:132 gerçek rota (/topology redirect)
//   /databases /messaging düz sayfalar, param yok

import (
	"encoding/json"
	"net/url"
)

// toolLinkArgs — köprünün okuduğu TEK alt küme. Bilinmeyen alanlar
// sessizce yok sayılır (json.Unmarshal davranışı) — tool şemaları
// büyüyünce köprü kırılmaz.
type toolLinkArgs struct {
	Service string `json:"service"`
	Query   string `json:"query"`
}

// toolCallLink — tool adı + HAM arg JSON'u → ürün linki. Saf, tablo
// testli. ok=false → bu tool için köprü yok (ör. render_chart: grafik
// zaten cevabın içine gömülü; list_services: hedef sayfa aramanın
// kendisi değil).
func toolCallLink(tool string, rawArgs json.RawMessage) (guidedAnswerLink, bool) {
	var a toolLinkArgs
	if len(rawArgs) > 0 {
		_ = json.Unmarshal(rawArgs, &a) // hata = alanlar boş; köprü yine kurulabilir
	}
	svcQ := url.QueryEscape(a.Service)
	withSvc := func(base, sep string) string {
		if a.Service == "" {
			return base
		}
		return base + sep + "service=" + svcQ
	}
	switch tool {
	case "get_service_health":
		if a.Service == "" {
			return guidedAnswerLink{}, false
		}
		return guidedAnswerLink{Label: a.Service + " · Overview", Href: "/service?name=" + svcQ}, true
	case "get_operation_health":
		if a.Service == "" {
			return guidedAnswerLink{}, false
		}
		return guidedAnswerLink{Label: "Endpoint'ler · " + a.Service, Href: "/endpoints?service=" + svcQ}, true
	case "search_traces":
		return guidedAnswerLink{Label: "Trace'ler", Href: withSvc("/traces", "?")}, true
	case "search_logs":
		href := withSvc("/logs", "?")
		if a.Query != "" {
			sep := "?"
			if a.Service != "" {
				sep = "&"
			}
			href += sep + "q=" + url.QueryEscape(a.Query)
		}
		return guidedAnswerLink{Label: "Loglar", Href: href}, true
	case "list_problems":
		return guidedAnswerLink{Label: "Problemler", Href: withSvc("/problems", "?")}, true
	case "list_exception_groups":
		return guidedAnswerLink{Label: "Exception inbox", Href: withSvc("/inbox?kind=exception", "&")}, true
	case "get_topology", "get_blast_radius":
		return guidedAnswerLink{Label: "Servis haritası", Href: "/service-map"}, true
	case "get_db_health":
		return guidedAnswerLink{Label: "Veritabanları", Href: "/databases"}, true
	case "get_messaging_health":
		return guidedAnswerLink{Label: "Messaging", Href: "/messaging"}, true
	}
	return guidedAnswerLink{}, false
}

// mergeToolLinks — döngü boyunca biriken köprüler: href'e göre tekil,
// ilk-çağrılan-önce, tavan 4 (çip şeridi taşmasın). Saf.
func mergeToolLinks(acc []guidedAnswerLink, l guidedAnswerLink) []guidedAnswerLink {
	for _, e := range acc {
		if e.Href == l.Href {
			return acc
		}
	}
	if len(acc) >= 4 {
		return acc
	}
	return append(acc, l)
}
