package api

// v0.9.1134 (operatör istegi) — guided chat'in TAKIM farkındalığı:
// "kullanıcıyı ilgili servise/trace'e yönlendir; TAKIMININ servislerini
// listele; takım bilinmiyorsa SOR ve o takımın servislerini EN ÇOK HATA
// ALAN önce getir."
//
// Bu dosya dört kapıyı kilitler:
//  1. extractTeamEntity CANLI katalogla eşleşir (katlama + çıplak ad +
//     sınır + servis-adı çakışması),
//  2. router yerleşimi — çıplak takım adı SERVİSLE aynı ad olsa bile
//     takıma gider (yoksa "hangi takım?" çipi sessizce servis sağlığına
//     düşerdi), ama ada ek servis sinyali varken SERVİS kazanır,
//  3. sıralama sözleşmesi: hata ORANI azalan, eşitlikte hata SAYISI
//     (operatörün cümlesi), üçüncü anahtar ad — deterministik,
//  4. "hangi takım?" akışı: takımsız kullanıcıda çipler canlı takım
//     adlarını taşır ve o çipler kendi başına yönlenir (sunucuda
//     konuşma durumu YOK).

import (
	"os"
	"strings"
	"testing"

	"github.com/cilcenk/coremetry/internal/chstore"
)

// Canlı takım kataloğu — testlerin eşleştiği liste. "payment-service"
// BİLEREK guidedTestServices ile aynı: takım/servis ad çakışması bu
// dosyanın asıl konusu. "sy" 3 karakterden kısa (atlanmalı).
var teamTestTeams = []string{
	"Avengersy",
	"SY-Dijital Bankacılık",
	"payment-service",
	"sy",
}

func TestExtractTeamEntity(t *testing.T) {
	cases := []struct {
		name string
		msg  string
		want string
	}{
		{"çıplak ad (çip turu)", "avengersy", "Avengersy"},
		{"çıplak ad + soru işareti", "Avengersy?", "Avengersy"},
		{"cümle içinde", "avengersy takımının servisleri nasıl", "Avengersy"},
		{"büyük harf", "AVENGERSY servisleri", "Avengersy"},
		// Türkçe katlama: katalogda "Bankacılık", operatör ASCII yazıyor.
		{"ı/i katlaması (ascii yazım)", "sy-dijital bankacilik servisleri", "SY-Dijital Bankacılık"},
		{"ı/i katlaması (tam büyük)", "SY-DIJITAL BANKACILIK", "SY-Dijital Bankacılık"},
		{"aksanlı yazım aynen", "sy-dijital bankacılık", "SY-Dijital Bankacılık"},
		// En uzun ad kazanır: "sy" zaten atlanıyor, ama uzun ad kısa
		// adayı gölgelemeli.
		{"en uzun eşleşme kazanır", "sy-dijital bankacılık takımı", "SY-Dijital Bankacılık"},
		{"servisle aynı ad — çıkarım katalogdan, karar router'ın", "payment-service", "payment-service"},
		{"sınır: ad-içi eşleşme yok", "avengersy-legacy nasıl", ""},
		{"3 karakterden kısa takım atlanır", "sy", ""},
		{"katalogda olmayan", "checkout-service nasıl", ""},
		{"boş katalog", "avengersy", ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			teams := teamTestTeams
			if c.name == "boş katalog" {
				teams = nil
			}
			if got := extractTeamEntity(normalizeGuidedMsg(c.msg), teams); got != c.want {
				t.Errorf("extractTeamEntity(%q) = %q, want %q", c.msg, got, c.want)
			}
		})
	}
}

func TestIsBareTeamAsk(t *testing.T) {
	cases := []struct {
		msg, team string
		want      bool
	}{
		{"avengersy", "Avengersy", true},
		{"Avengersy?", "Avengersy", true},
		{"  avengersy .", "Avengersy", true},
		{"sy-dijital bankacılık", "SY-Dijital Bankacılık", true},
		{"avengersy servisleri", "Avengersy", false},
		{"avengersy takımı nasıl", "Avengersy", false},
		{"avengersy 5", "Avengersy", false},
		{"avengersy", "", false},
	}
	for _, c := range cases {
		if got := isBareTeamAsk(normalizeGuidedMsg(c.msg), c.team); got != c.want {
			t.Errorf("isBareTeamAsk(%q, %q) = %v, want %v", c.msg, c.team, got, c.want)
		}
	}
}

// mayNameTeam — katalog okumasının ÇIPLAK takım adı için de göze
// alındığı ucuz kapı. Bu kapı olmadan "hangi takım?" çipi hiçbir guided
// kelime taşımadığı için serbest tool döngüsüne düşerdi.
func TestMayNameTeam(t *testing.T) {
	cases := []struct {
		msg  string
		want bool
	}{
		{"avengersy", true},
		{"sy-dijital bankacılık", true},
		{"Avengersy takımı", true},
		{"", false},
		{"a b", false}, // 3 karakterden uzun token yok
		{"bu uzun bir cümle ve beş kelimeden fazla token taşıyor", false},
		{"çok çok çok çok uzun bir takım adı olamayacak kadar uzun metin burada", false},
	}
	for _, c := range cases {
		if got := mayNameTeam(normalizeGuidedMsg(c.msg)); got != c.want {
			t.Errorf("mayNameTeam(%q) = %v, want %v", c.msg, got, c.want)
		}
	}
}

// Router yerleşimi — takım dalı iyelik dallarından SONRA, servis
// dallarından ÖNCE; kapı üç hâlle sınırlı.
func TestRouteGuidedIntentTeamPlacement(t *testing.T) {
	cases := []struct {
		name   string
		msg    string
		intent guidedIntent
		team   string
		svc    string
	}{
		// (a) çıplak ad — "hangi takım?" cevabı.
		{"çıplak takım adı", "avengersy", guidedTeamServices, "Avengersy", ""},
		// GÖLGELEME KARARI: ad hem takım hem SERVİS. Çıplakken takım
		// kazanır, yoksa çip sessizce servis sağlığına düşerdi.
		{"çıplak ad servisle çakışıyor → takım", "payment-service", guidedTeamServices, "payment-service", ""},
		// (b) takım/ekip kelimesi.
		{"takım kelimesi", "avengersy takımının servisleri", guidedTeamServices, "Avengersy", ""},
		{"ekip kelimesi", "avengersy ekibinin durumu nasıl", guidedTeamServices, "Avengersy", ""},
		{"team english", "avengersy team services", guidedTeamServices, "Avengersy", ""},
		// (c) servis çözülmedi + liste/sağlık/hata şekli.
		{"servis listesi şekli", "avengersy servisleri", guidedTeamServices, "Avengersy", ""},
		{"sağlık şekli", "avengersy nasıl", guidedTeamServices, "Avengersy", ""},
		{"hata şekli", "avengersy hataları", guidedTeamServices, "Avengersy", ""},
		// Kapı KAPALI: ad servisle çakışıyor VE servis sinyali var →
		// SERVİS kazanır (takım dalı svc == "" istiyor).
		{"servis sinyali gölgelenmiyor (health)", "payment-service nasıl", guidedServiceHealth, "", "payment-service"},
		{"servis sinyali gölgelenmiyor (hata)", "payment-service hataları", guidedServiceHealth, "", "payment-service"},
		{"servis sinyali gölgelenmiyor (neden)", "payment-service neden yavaşladı", guidedRootCause, "", "payment-service"},
		{"servis sinyali gölgelenmiyor (trace)", "payment-service en yavaş trace'ler", guidedSlowTraces, "", "payment-service"},
		// Takım kelimesi VARSA takım kapsamı kazanır (v0.9.375'in
		// hasTeamSelfSignal emsali: kapsam kelimesi sessizce düşmesin).
		{"takım kelimesi servis sinyalini yener", "payment-service takımı nasıl", guidedTeamServices, "payment-service", ""},
		// İYELİK her zaman önce — kimlikten çözülür, ad taşımaz.
		{"iyelik takım dalı önce", "takımımın servisleri nasıl", guidedMyServices, "", ""},
		{"iyelik + ad → yine iyelik", "benim takımım avengersy servisleri", guidedMyServices, "", ""},
		// Takım kataloğu YOKKEN davranış bayt-bayt eskisi.
		{"takım kataloğu yok → eski davranış", "avengersy servisleri", guidedNone, "", ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			teams := teamTestTeams
			if c.name == "takım kataloğu yok → eski davranış" {
				teams = nil
			}
			got := routeGuidedIntent(c.msg, guidedTestServices, guidedTestEnvs, teams, "")
			if got.Intent != c.intent {
				t.Fatalf("intent = %q, want %q (msg %q)", got.Intent, c.intent, c.msg)
			}
			if got.Team != c.team {
				t.Errorf("team = %q, want %q", got.Team, c.team)
			}
			if got.Service != c.svc {
				t.Errorf("service = %q, want %q", got.Service, c.svc)
			}
		})
	}
}

// Ekran bağlamı (ctxService) çıplak takım adını gölgeleyemez: operatör
// bir servis sayfasında "hangi takım?" çipine bastığında da takım
// cevabını almalı.
func TestRouteTeamBeatsContextService(t *testing.T) {
	got := routeGuidedIntent("avengersy", guidedTestServices, guidedTestEnvs, teamTestTeams, "checkout-service")
	if got.Intent != guidedTeamServices || got.Team != "Avengersy" {
		t.Fatalf("ekran servisi çıplak takım adını gölgeledi: %+v", got)
	}
}

// v0.9.1244 — TestTeamCatalogueOrderAndDedup ve TestSortServicesByErrorRate
// bu dosyadan TAŞINDI: katalog sayımı/sırası ile hata-oranı sıralaması artık
// mcptools'ta yaşıyor (list_teams / get_team_services aynı çözümlemeyi
// kullanmak zorunda ve mcptools api'yi import edemez). Aynı vakalar,
// implementasyonun yanında: internal/mcptools/team_ownership_test.go
// (TestTeamCatalogueOrderAndDedup, TestSortServicesByErrorRate). Buradaki
// router + kanıt-metni kapıları yerinde kaldı — onların gövdesi hâlâ api'de.

// Kanıt bloğu: satır tavanı + üç dürüstlük satırı.
func TestRenderTeamServicesEvidenceTR(t *testing.T) {
	rows := make([]chstore.ServiceSummary, 0, 20)
	for i := 0; i < 20; i++ {
		rows = append(rows, chstore.ServiceSummary{
			Name: string(rune('a'+i)) + "-svc", ErrorRate: float64(20 - i),
			ErrorCount: uint64(20 - i), P99Ms: 100, SpanCount: 1000,
		})
	}
	// 22 servis okundu (2'si pencerede sessiz), 3'ü tavana takıldı.
	out := renderTeamServicesEvidenceTR("Avengersy", rows, 22, 3, 3600, "uat")
	if !strings.Contains(out, "Takım: Avengersy — 25 servis") {
		t.Errorf("başlık toplam servis sayısını (okunan+kırpılan) taşımalı:\n%s", out)
	}
	if !strings.Contains(out, "ilk 100 servis okundu, 3 servis dışarıda kaldı") {
		t.Errorf("tavan dürüstlük satırı yok:\n%s", out)
	}
	if !strings.Contains(out, "… ve 5 servis daha") {
		t.Errorf("satır tavanı dürüstlük satırı yok (20 satır, tavan %d):\n%s", teamServicesMaxRows, out)
	}
	if !strings.Contains(out, "takımın 2 servisi bu pencerede hiç span üretmedi") {
		t.Errorf("sessiz servis satırı yok:\n%s", out)
	}
	if !strings.Contains(out, `"uat" ortam daraltması bu listede UYGULANMADI`) {
		t.Errorf("env dürüstlük satırı yok (RED'in ortam boyutu yok):\n%s", out)
	}
	// Kanıt satırı dört ölçüyü de taşır (oran, sayı, p99, span).
	if !strings.Contains(out, "hata oranı %20.00 (20 hata), p99=100ms, 1000 span") {
		t.Errorf("satır biçimi dört ölçüyü taşımalı:\n%s", out)
	}
	// 16. satır basılmamalı.
	if strings.Contains(out, "p-svc") {
		t.Errorf("tavan aşıldı — 16. satır basılmış:\n%s", out)
	}
	// YÖNLENDİRME: liste tek başına cevap değil — model en kötü servisi
	// adıyla söylemeli (operatörün "ilgili servise yönlendir" istegi).
	if !strings.Contains(out, "EN KÖTÜ servisi adıyla") {
		t.Errorf("yönlendirme kuralı yok — cevap 15 satırlık tabloda kalır:\n%s", out)
	}

	// Veri yoksa SESSİZ geçilmez.
	empty := renderTeamServicesEvidenceTR("Avengersy", nil, 4, 0, 1800, "")
	if !strings.Contains(empty, "span verisi yok") {
		t.Errorf("boş pencerede dürüst satır yok:\n%s", empty)
	}
	if strings.Contains(empty, "dışarıda kaldı") || strings.Contains(empty, "UYGULANMADI") {
		t.Errorf("gereksiz dürüstlük satırı basılmış:\n%s", empty)
	}
}

// "hangi takım?" AKIŞI — çipler canlı takım adlarını taşır ve her biri
// KENDİ BAŞINA guidedTeamServices'e yönlenir. Akışın tek dayanağı bu:
// sunucuda konuşma durumu yok.
func TestGuidedTeamAskFlowSuggestionsRoute(t *testing.T) {
	route := guidedRoute{Intent: guidedMyServices, TeamOptions: []string{"Avengersy", "SY-Dijital Bankacılık"}}
	sugg := guidedSuggestions(route)
	if strings.Join(sugg, "|") != "Avengersy|SY-Dijital Bankacılık" {
		t.Fatalf("çipler takım adlarını taşımalı, got %v", sugg)
	}
	for _, q := range sugg {
		r := routeGuidedIntent(q, guidedTestServices, guidedTestEnvs, teamTestTeams, "")
		if r.Intent != guidedTeamServices {
			t.Errorf("takım çipi %q yönlenemiyor (%q) — diyalog kapanmaz", q, r.Intent)
		}
		if r.Team == "" {
			t.Errorf("takım çipi %q takımı çözemedi", q)
		}
		// Kapının ucuz ön-şartı da geçmeli, yoksa handler katalog
		// okumasına hiç girmez ve çip serbest döngüye düşer.
		if !mayNameTeam(normalizeGuidedMsg(q)) && !hasGuidedSignal(normalizeGuidedMsg(q)) {
			t.Errorf("takım çipi %q hızlı-çıkışa takılır (mayNameTeam=false)", q)
		}
	}
	// TeamOptions BOŞKEN my_services davranışı bayt-bayt eskisi.
	old := guidedSuggestions(guidedRoute{Intent: guidedMyServices})
	if len(old) == 0 || old[0] == "Avengersy" {
		t.Errorf("takım seçeneği yokken eski öneriler kalmalı: %v", old)
	}
}

// team_services cevabının çipleri EN KÖTÜ servise iner (v0.9.651 dersi:
// jenerik çip operatörü servis SEÇEMEZ hâlde bırakıyordu).
func TestGuidedTeamServicesSuggestions(t *testing.T) {
	route := guidedRoute{
		Intent: guidedTeamServices, Team: "Avengersy",
		TeamServices: []string{"payment-service", "checkout-service"},
	}
	sugg := guidedSuggestions(route)
	if len(sugg) == 0 || !strings.HasPrefix(sugg[0], "payment-service") {
		t.Fatalf("ilk çip en kötü servise inmeli: %v", sugg)
	}
	for _, q := range sugg {
		if r := routeGuidedIntent(q, guidedTestServices, guidedTestEnvs, teamTestTeams, ""); r.Intent == guidedNone {
			t.Errorf("öneri %q yönlenemiyor — serbest döngüye düşer", q)
		}
	}
	// Servis listesi boşken de çip üretilir (boş şerit çıkmazdır).
	if len(guidedSuggestions(guidedRoute{Intent: guidedTeamServices, Team: "Avengersy"})) == 0 {
		t.Error("servissiz takım cevabında çip yok")
	}
}

// ÖLÜ-PARAM DİSİPLİNİ (v0.9.1130 emsali) — /services sayfası takım
// süzgecini URL'den OKUMUYOR (Services.tsx: ownerTeam/sreTeam useState,
// searchParams yalnız page/compare/cluster/namespace). Bu yüzden takım
// linki DÜZ /services olmalı; param'lı hâli filtreli liste vaat edip
// filtresiz listeyi açardı.
func TestTeamServicesLinksAvoidDeadServicesParam(t *testing.T) {
	links := guidedAnswerLinks(guidedRoute{
		Intent: guidedTeamServices, Team: "Avengersy",
		TeamServices: []string{"päy ments", "checkout-service"},
	})
	if len(links) != 2 {
		t.Fatalf("takım cevabı iki link taşımalı (Servisler + en kötü servis): %+v", links)
	}
	for _, l := range links {
		if !strings.HasPrefix(l.Href, "/") || l.Label == "" {
			t.Errorf("link uygulama-köklü ve etiketli olmalı: %+v", l)
		}
		if strings.Contains(l.Href, "ownerTeam=") || strings.Contains(l.Href, "sreTeam=") {
			t.Errorf("ölü param: /services bu adları URL'den okumaz: %s", l.Href)
		}
	}
	if links[0].Href != "/services" {
		t.Errorf("takım linki düz /services olmalı, got %s", links[0].Href)
	}
	if !strings.Contains(links[1].Href, "p%C3%A4y+ments") {
		t.Errorf("servis adı escape edilmeli: %s", links[1].Href)
	}
	// Kaynak-pin: yasak adlar dosyanın hiçbir yerinde geçmesin (çip
	// metni değişse de kural kalsın).
	b, err := os.ReadFile("copilot_followup.go")
	if err != nil {
		t.Fatalf("kaynak okunamadı: %v", err)
	}
	if src := string(b); strings.Contains(src, "/services?ownerTeam") || strings.Contains(src, "/services?sreTeam") {
		t.Error("copilot_followup.go /services'e takım param'ı basıyor — sayfa onu okumuyor")
	}
}

// Takım rotası SOMUT öznedir: çekmece bağlamı onu bastırmamalı (bastırma
// kuralı "özneye oturmayan rota" içindi).
func TestDrawerDoesNotSuppressTeamRoute(t *testing.T) {
	route := guidedRoute{Intent: guidedTeamServices, Team: "Avengersy"}
	if drawerSuppressesGuided("ekrandaki açıklama metni", route, "avengersy servisleri") {
		t.Error("takım rotası çekmece bağlamıyla bastırıldı — guided gerçek RED'i getirmeli")
	}
}
