package api

import (
	"strings"
	"testing"
)

// v0.9.650 — operatör: "Takımıma ait servislerin hataları (Exceptions)
// neler?"
//
// Problem ve Exception AYRI yüzeyler: Problem bir alarm kuralının açtığı
// kayıt, Exception ise span'lerden gruplanan ham hata. "Takımımın
// problemleri" ikincisini KAPSAMIYORDU ve soru sessizce yanlış yüzeye
// düşüyordu.
//
// Ayrım kılpayı: hasErrorSignal zaten "exception" kelimesini kapsıyor,
// yani takım+exception cümlesi problems dalına gidiyordu. Bu testler
// ayrımın DURDUĞUNU çiviliyor.

func routeOf(t *testing.T, q string) guidedRoute {
	t.Helper()
	return routeGuidedIntent(q, nil, nil, nil, "")
}

func TestTeamExceptionQuestionRoutesToExceptions(t *testing.T) {
	for _, q := range []string{
		"Takımıma ait servislerin hataları (Exceptions) neler?",
		"takımımın exception'ları neler",
		"benim takımın exceptionları",
		"takımımın servislerindeki istisnalar",
	} {
		if got := routeOf(t, q).Intent; got != guidedMyExceptions {
			t.Errorf("%q → %q, beklenen my_exceptions", q, got)
		}
	}
}

// EXPLICIT exception kelimesi YOKSA bugünkü davranış korunmalı:
// "takımımın hataları" açık PROBLEM'lere gitmeye devam etsin.
func TestTeamErrorQuestionStaysOnProblems(t *testing.T) {
	for _, q := range []string{
		"takımımın hataları neler",
		"takımımın açık problemleri",
		"benim takımda sorun var mı",
	} {
		if got := routeOf(t, q).Intent; got != guidedMyProblems {
			t.Errorf("%q → %q, beklenen my_problems (exception kelimesi yok)", q, got)
		}
	}
}

// Takım sinyali YOKSA exception kelimesi takım dalını AÇMAMALI —
// "exception'lar neler" filo geneli bir soru.
func TestExceptionWordAloneDoesNotClaimTeamScope(t *testing.T) {
	if got := routeOf(t, "exception'lar neler").Intent; got == guidedMyExceptions {
		t.Error("takım sinyali olmadan my_exceptions seçilmemeli")
	}
}

func TestHasExceptionWordIsNarrow(t *testing.T) {
	// hasErrorSignal geniş; hasExceptionWord DAR olmalı, yoksa ayrım yok.
	if hasExceptionWord([]string{"hata"}) {
		t.Error("'hata' exception kelimesi sayılmamalı — ayrımın tamamı bu")
	}
	if hasExceptionWord([]string{"error"}) {
		t.Error("'error' exception kelimesi sayılmamalı")
	}
	if !hasExceptionWord([]string{"exceptionlari"}) {
		t.Error("'exceptionlari' yakalanmalı (Türkçe ek)")
	}
}

// v0.9.650 — Türkçe iyelik SİMETRİSİ. Üstteki test bunu ORTAYA ÇIKARDI:
// hasTeamSelfSignal yalnız "takımım" ekini tanıyordu, "benim takımın" /
// "bizim takımda" gibi çok doğal kalıplar DÜŞÜYOR ve soru sessizce FİLO
// GENELİNE gidiyordu — operatör "takımımın" dediğini sanırken tüm
// kurumun problemlerini görüyordu.
func TestTurkishPossessiveTeamSignal(t *testing.T) {
	for _, q := range []string{
		"benim takımın problemleri",
		"bizim takımda sorun var mı",
		"benim ekibin servisleri",
	} {
		got := routeOf(t, q).Intent
		if got != guidedMyProblems && got != guidedMyServices {
			t.Errorf("%q → %q, takım-kapsamlı bir intent bekleniyordu", q, got)
		}
	}
}

// Aşırı eşleşme kontrolü: "benim" tek başına takım kapsamı AÇMAMALI.
func TestBenimAloneDoesNotClaimTeamScope(t *testing.T) {
	got := routeOf(t, "benim için en yavaş trace'ler").Intent
	if got == guidedMyProblems || got == guidedMyServices {
		t.Errorf("'benim' tek başına takım kapsamı açmamalı, alınan %q", got)
	}
}

// v0.9.651 — operatör: "takımıma ait servisleri listeledikten sonra
// SEÇECEĞİ servisle ilgili hatalar / logları / en yavaş trace'leri".
//
// Servis-kapsamlı çipler ZATEN vardı; eksik olan tek halka, takım
// listelendikten sonraki çiplerin servis ADI taşımamasıydı — operatör
// bir servis seçemiyordu.

func TestTeamServiceChipsNameRealServices(t *testing.T) {
	r := guidedRoute{Intent: guidedMyServices, TeamServices: []string{"checkout", "ledger", "auth", "search"}}
	got := guidedSuggestions(r)

	// İlk üç servis adlandırılmalı; dördüncü DEĞİL (menü hissi).
	for _, want := range []string{"checkout sağlığı nasıl?", "ledger sağlığı nasıl?", "auth sağlığı nasıl?"} {
		found := false
		for _, g := range got {
			if g == want {
				found = true
			}
		}
		if !found {
			t.Errorf("%q çipi yok, üretilen: %v", want, got)
		}
	}
	for _, g := range got {
		if g == "search sağlığı nasıl?" {
			t.Errorf("dördüncü servis çipi çizilmemeli (menü hissi): %v", got)
		}
	}
	if len(got) > 4 {
		t.Errorf("çip sayısı dördü aşmamalı, alınan %d: %v", len(got), got)
	}
}

// Takım çözülemediyse (kimlik yok, takım atanmamış, katalog boş) eski
// jenerik çipler kalmalı — servis adı olmayan bir çip üretmektense.
func TestTeamServiceChipsFallBackWhenUnresolved(t *testing.T) {
	got := guidedSuggestions(guidedRoute{Intent: guidedMyServices})
	if len(got) == 0 {
		t.Fatal("çip listesi boş kalmamalı")
	}
	for _, g := range got {
		if g == " sağlığı nasıl?" {
			t.Fatalf("boş servis adıyla çip üretilmiş: %v", got)
		}
	}
}

// Servis seçildikten SONRAKİ adım zaten çalışıyor olmalı: operatörün
// istediği iki drill-down (loglar, en yavaş trace'ler) service_health
// çiplerinde duruyor. Bu test o halkanın kopmadığını çiviliyor.
func TestServiceHealthChipsCarryOperatorDrilldowns(t *testing.T) {
	got := guidedSuggestions(guidedRoute{Intent: guidedServiceHealth, Service: "checkout"})
	want := map[string]bool{
		"checkout hata logları?":       false,
		"checkout en yavaş trace'ler?": false,
	}
	for _, g := range got {
		if _, ok := want[g]; ok {
			want[g] = true
		}
	}
	for k, seen := range want {
		if !seen {
			t.Errorf("%q çipi kayıp, üretilen: %v", k, got)
		}
	}
}

// v0.9.1246 — operatör: "Takımımın exceptionları dediğinde o takım
// filtreli exceptions açabilir."
//
// Cevabın altındaki derin link, dilimin OPERATÖRE GÖRÜNEN yarısı: cevap
// takımın exception'larını sayarken link TÜM filonun kuyruğunu açıyordu,
// yani operatör sayfada kendi takımını elle aramak zorundaydı ve iki sayı
// (cevaptaki ile sayfadaki) birbirini tutmuyordu.
//
// Takım adı SUNUCUDA çözülüyor (guidedMyTeamBundle: CallMeta → users →
// User.Team → katalog yazımı) ve URL'e KANONİK ADIYLA yazılıyor: "benim"
// kelimesi URL'e girseydi paylaşılan link, açan kişinin takımını
// gösterirdi.
func TestMyExceptionsLinkCarriesResolvedTeam(t *testing.T) {
	links := guidedAnswerLinks(guidedRoute{Intent: guidedMyExceptions, Team: "SY"}, noLinkWindow())
	if len(links) != 1 {
		t.Fatalf("tek link beklenir: %+v", links)
	}
	if links[0].Href != "/inbox?kind=exception&team=SY" {
		t.Errorf("link %q — takım-filtreli exception görünümü açmalı", links[0].Href)
	}
	if links[0].Label != "SY · Exceptions" {
		t.Errorf("etiket takımı söylemeli, got %q", links[0].Label)
	}

	// Takım ÇÖZÜLMEDİYSE (kimlik yok / takım atanmamış / takımın hiç
	// servisi yok) link filo geneline döner. Yanlış kapsamlı bir link
	// linksizlikten kötü, ama "hiç link yok" da çıkmazdır — kuyruğun
	// kendisi hâlâ doğru hedef.
	fallback := guidedAnswerLinks(guidedRoute{Intent: guidedMyExceptions}, noLinkWindow())
	if len(fallback) != 1 || fallback[0].Href != "/inbox?kind=exception" {
		t.Errorf("takımsız cevapta düz exception kuyruğu beklenir: %+v", fallback)
	}
}

// Kimlik URL'e SIZMAMALI: link paylaşılabilir olmalı.
func TestMyExceptionsLinkHasNoIdentityToken(t *testing.T) {
	for _, team := range []string{"SY", "UG", "Ödeme Takımı"} {
		l := guidedAnswerLinks(guidedRoute{Intent: guidedMyExceptions, Team: team}, noLinkWindow())[0]
		for _, bad := range []string{"benim", "me=", "user=", "self"} {
			if strings.Contains(strings.ToLower(l.Href), bad) {
				t.Errorf("link kimlik taşıyor (%q): %s", bad, l.Href)
			}
		}
	}
}
