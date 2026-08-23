package api

import (
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/cilcenk/coremetry/internal/copilot"
	"github.com/cilcenk/coremetry/internal/reqid"
)

// reqid_route_test.go — v0.9.1142: yapılandırılmış request kimliği →
// trace çözümlemesinin sohbet/rota tarafı.
//
// TÜM DEĞERLER SENTETİK (fonksiyon kodu "ABCD001", müşteri
// "0000000042") — depo bir kurum/müşteri değeri taşımaz.
const (
	testReqID    = "ABCD0010599310513000000004220260817093440812086"
	testTraceHex = "9fc37145182089354c2c20a1c63e0817"
	testSpanHex  = "00f067aa0ba902b7"
)

func TestRouteStructuredRequestID(t *testing.T) {
	cases := []struct {
		name       string
		msg        string
		wantIntent guidedIntent
		wantReqID  string
	}{
		{"çıplak yapıştırma", testReqID, guidedRequestID, testReqID},
		{"soru içinde", "şu isteğe ne oldu " + testReqID + " ?", guidedRequestID, testReqID},
		{"başlıklı", "Request ID: " + testReqID, guidedRequestID, testReqID},
		// HARF KASASI: aramada kullanılacak token orijinal hâliyle
		// taşınmalı (ES keyword alanları harfe duyarlı olabilir).
		{"büyük harf korunur", "ne oldu " + testReqID + " bakar mısın", guidedRequestID, testReqID},
		// SIRA: açık 32-hex trace hâlâ en doğrudan çapa — log araması
		// yapmadan trace'e gidilir.
		{"32-hex trace kimlikten önce gelir",
			"trace " + testTraceHex + " ve request " + testReqID, guidedTraceByID, ""},
		// SIRA: kimlik 16-hex span'den ÖNCE — daha spesifik sinyal.
		{"kimlik 16-hex span'i yener",
			"span " + testSpanHex + " request " + testReqID, guidedRequestID, testReqID},
		{"kimliksiz span rotası bozulmadı", "span " + testSpanHex, guidedSpanByID, ""},
		{"kimliksiz trace rotası bozulmadı", "trace " + testTraceHex, guidedTraceByID, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			route := routeGuidedIntent(c.msg, nil, nil, nil, "")
			if route.Intent != c.wantIntent {
				t.Fatalf("intent = %q, beklenen %q", route.Intent, c.wantIntent)
			}
			if route.RequestID != c.wantReqID {
				t.Fatalf("RequestID = %q, beklenen %q", route.RequestID, c.wantReqID)
			}
		})
	}
}

// SIFIR-MALİYET KAPISI: yapıştırılan kimlik hiçbir guided KELİME
// taşımıyor. Sinyal listesine girmezse mesaj katalog okumasına bile
// gelmeden serbest döngüye düşer ve özellik ÖLÜ olur.
func TestStructuredRequestIDIsAGuidedSignal(t *testing.T) {
	if !hasGuidedSignal(normalizeGuidedMsg(testReqID)) {
		t.Fatal("çıplak kimlik guided sinyali sayılmıyor — hızlı yol hiç çalışmaz")
	}
	// Kimlik tespiti kendi başına yanlış pozitif üretmemeli (guided
	// sinyalinin başka dalları "nasıl" gibi kelimelerle zaten yanıyor,
	// burada ölçülen ŞEY kimlik dalı).
	for _, s := range []string{"bugün hava nasıl", "deploy v1.4.0-build.20260817", testTraceHex} {
		if hasStructuredRequestID(normalizeGuidedMsg(s)) {
			t.Fatalf("yanlış pozitif kimlik: %q", s)
		}
	}
}

// Çekmece bağlamı bu rotayı BASTIRMAMALI: yapıştırılan kimlik en somut
// öznedir ve çözümü (log → trace) yalnız guided yolda var.
func TestDrawerDoesNotSuppressRequestIDRoute(t *testing.T) {
	route := guidedRoute{Intent: guidedRequestID, RequestID: testReqID}
	if drawerSuppressesGuided("ekrandaki açıklama metni", route, "ne oldu "+testReqID) {
		t.Fatal("request_id rotası çekmece bağlamıyla bastırıldı")
	}
}

// Anlatıcı: çözülen kanıt bir TRACE paketi, yani anlatım prompt'u
// ✨ Explain ile aynı olmalı (v0.9.1131 dersi: aynı kanıt, aynı anlatı).
func TestRequestIDNarrationUsesTracePrompt(t *testing.T) {
	if guidedNarrationPrompt(guidedRequestID) != copilot.SystemPromptTrace() {
		t.Fatal("request_id anlatımı jenerik sohbet prompt'unu kullanıyor")
	}
}

func TestGuidedRequestIDAnswerLinks(t *testing.T) {
	id, ok := reqid.Parse(testReqID, reqid.Location(""))
	if !ok {
		t.Fatal("sentetik kimlik ayrıştırılamadı")
	}
	from, to := id.Window()

	t.Run("çözülmüş: trace + servis + pencereli loglar", func(t *testing.T) {
		links := guidedAnswerLinks(guidedRoute{
			Intent: guidedRequestID, RequestID: testReqID, TraceID: testTraceHex,
			Service:         "checkout-service",
			ReqWindowFromMs: from.UnixMilli(), ReqWindowToMs: to.UnixMilli(),
		}, noLinkWindow())
		if len(links) != 3 {
			t.Fatalf("çip sayısı %d: %+v", len(links), links)
		}
		if links[0].Href != "/trace?id="+testTraceHex {
			t.Fatalf("trace derin linki: %q", links[0].Href)
		}
		if !strings.HasPrefix(links[1].Href, "/service?name=checkout-service") {
			t.Fatalf("servis linki: %q", links[1].Href)
		}
		// /logs YALNIZ `q` + `range=custom:<fromMs>-<toMs>` okuyor
		// (logsUrl.ts). Başka param ölü yük olurdu (K4 sınıfı).
		want := "/logs?q=" + testReqID + "&range=custom:" +
			strconv.FormatInt(from.UnixMilli(), 10) + "-" + strconv.FormatInt(to.UnixMilli(), 10)
		if links[2].Href != want {
			t.Fatalf("log derin linki:\n got %q\nwant %q", links[2].Href, want)
		}
	})

	t.Run("pencere yoksa log çipi YAZILMAZ", func(t *testing.T) {
		links := guidedAnswerLinks(guidedRoute{
			Intent: guidedRequestID, RequestID: testReqID, TraceID: testTraceHex,
		}, noLinkWindow())
		for _, l := range links {
			if strings.HasPrefix(l.Href, "/logs") {
				t.Fatalf("penceresiz log çipi çizildi: %q", l.Href)
			}
		}
	})

	t.Run("hiçbir şey çözülmediyse çip yok", func(t *testing.T) {
		if got := guidedAnswerLinks(guidedRoute{Intent: guidedRequestID, RequestID: testReqID}, noLinkWindow()); len(got) != 0 {
			t.Fatalf("çözümsüz rotada çip var: %+v", got)
		}
	})
}

func TestGuidedRequestIDSuggestions(t *testing.T) {
	got := guidedSuggestions(guidedRoute{Intent: guidedRequestID, RequestID: testReqID, Service: "checkout-service"})
	if len(got) == 0 || !strings.HasPrefix(got[0], "checkout-service") {
		t.Fatalf("servis kapsamlı çip yok: %+v", got)
	}
	if got := guidedSuggestions(guidedRoute{Intent: guidedRequestID, RequestID: testReqID}); got != nil {
		t.Fatalf("servissiz rotada jenerik çip üretildi: %+v", got)
	}
}

// Köprü linki: ZAMAN yer tutucuları OPSİYONEL ve taşımayan şablonda
// çıktı bayt-bayt eskisi (v0.9.709 regresyon pini).
func TestBuildCorrelationLinkAtWindow(t *testing.T) {
	id, _ := reqid.Parse(testReqID, reqid.Location(""))
	from, to := id.Window()

	t.Run("zaman yer tutucusu yok → eski davranış", func(t *testing.T) {
		plain := "https://logs.example.com/x?requestId={value}"
		want := buildCorrelationLink(plain, testReqID)
		got := buildCorrelationLinkAt(plain, testReqID, from, to)
		if got != want {
			t.Fatalf("pencereli çağrı linki değiştirdi:\n got %q\nwant %q", got, want)
		}
	})

	t.Run("ISO ofsetli yer tutucular dolar", func(t *testing.T) {
		tplISO := "https://logs.example.com/x?id={value}&from={from}&to={to}"
		got := buildCorrelationLinkAt(tplISO, testReqID, from, to)
		for _, frag := range []string{"{from}", "{to}"} {
			if strings.Contains(got, frag) {
				t.Fatalf("%s doldurulmadı: %q", frag, got)
			}
		}
		// Ofset (+03:00) URL-encode edilmiş hâlde bulunmalı: log sistemi
		// hangi saat diliminde okuduğunu tahmin etmek zorunda kalmasın.
		if !strings.Contains(got, "%2B03%3A00") {
			t.Fatalf("ISO damga ofset taşımıyor: %q", got)
		}
	})

	t.Run("epoch ms yer tutucular dolar", func(t *testing.T) {
		tplMs := "https://logs.example.com/x?id={value}&f={from_ms}&t={to_ms}"
		got := buildCorrelationLinkAt(tplMs, testReqID, from, to)
		if !strings.Contains(got, "f="+strconv.FormatInt(from.UnixMilli(), 10)) ||
			!strings.Contains(got, "t="+strconv.FormatInt(to.UnixMilli(), 10)) {
			t.Fatalf("ms damgalar yanlış: %q", got)
		}
	})

	t.Run("kimlik çözülemediyse pencere ŞİMDİye çapalanır (kırık link yok)", func(t *testing.T) {
		tplMs := "https://logs.example.com/x?id={value}&f={from_ms}&t={to_ms}"
		got := buildCorrelationLinkAt(tplMs, "SERBEST-BICIM-ID-42", time.Time{}, time.Time{})
		if strings.Contains(got, "{from_ms}") || strings.Contains(got, "{to_ms}") {
			t.Fatalf("yer tutucu ham kaldı — link kırık: %q", got)
		}
		fromMs := extractParamInt(t, got, "f=")
		toMs := extractParamInt(t, got, "t=")
		if toMs <= fromMs {
			t.Fatalf("pencere ters/boş: %d → %d", fromMs, toMs)
		}
		nowMs := time.Now().UnixMilli()
		if d := nowMs - fromMs; d < int64(corrBridgeFallbackLookback/time.Millisecond)-5000 ||
			d > int64(corrBridgeFallbackLookback/time.Millisecond)+5000 {
			t.Fatalf("geri bakış %dms — beklenen ~%v", d, corrBridgeFallbackLookback)
		}
	})

	t.Run("zaman yer tutuculu şablon hâlâ geçerli sayılır", func(t *testing.T) {
		if !validCorrelationTemplate("https://h/x?id={value}&from={from}&to={to_ms}") {
			t.Fatal("zaman yer tutucusu şablonu geçersiz saydırdı")
		}
		// {value} hâlâ ZORUNLU: yoksa her kimlik aynı linke gider.
		if validCorrelationTemplate("https://h/x?from={from}&to={to}") {
			t.Fatal("{value} olmadan geçerli sayıldı")
		}
	})
}

// Metinden avlanan kimlik de YAPILANDIRILMIŞSA pencereyi kimliğin
// damgasından alır — köprü linki doğru aralıkta açılır.
func TestRequestIDLinksDeriveWindowFromEmbeddedTime(t *testing.T) {
	id, _ := reqid.Parse(testReqID, reqid.Location(""))
	from, to := id.Window()
	tplsWin := correlationTemplates{
		"default": "https://logs.example.com/x?id={value}&f={from_ms}&t={to_ms}",
	}
	links := requestIDLinks("Request ID:\n"+testReqID, "checkout-service", tplsWin, reqid.Location(""))
	if len(links) != 1 {
		t.Fatalf("çip sayısı %d", len(links))
	}
	if !strings.Contains(links[0].Href, "f="+strconv.FormatInt(from.UnixMilli(), 10)) ||
		!strings.Contains(links[0].Href, "t="+strconv.FormatInt(to.UnixMilli(), 10)) {
		t.Fatalf("pencere kimliğin damgasından gelmiyor: %q", links[0].Href)
	}
	// Yapılandırılmamış kimlikte davranış eskisi: pencere şimdiye çapalı
	// ama link üretiliyor (kırık bırakılmıyor).
	free := requestIDLinks("request id: SERBEST-BICIM-ID-4242", "checkout-service", tplsWin, reqid.Location(""))
	if len(free) != 1 || strings.Contains(free[0].Href, "{from_ms}") {
		t.Fatalf("serbest biçim kimlikte link bozuk: %+v", free)
	}
}

func TestDedupLinksByHref(t *testing.T) {
	in := []guidedAnswerLink{
		{Label: "rotadan", Href: "https://logs/x?id=1"},
		{Label: "metinden", Href: "https://logs/x?id=1"},
		{Label: "başka", Href: "https://logs/x?id=2"},
	}
	got := dedupLinksByHref(in)
	if len(got) != 2 {
		t.Fatalf("tekilleşme yok: %+v", got)
	}
	// İlk görülen kazanır: rotadan gelen deterministik çip önde kalsın.
	if got[0].Label != "rotadan" {
		t.Fatalf("sıra korunmadı: %+v", got)
	}
	if got := dedupLinksByHref(nil); got != nil {
		t.Fatalf("nil girdi değişti: %+v", got)
	}
}

// ÇAĞRI-YERİ KAPISI (v0.9.660 dersi, bu depoda tekrar eden sınıf): saf
// yardımcılar hiç ÇAĞRILMASA da yeşil kalır. Guided cevap emit'i hem
// rotadan gelen köprü çipini hem tekilleştirmeyi kullanmak zorunda.
func TestGuidedAnswerEmitWiresKnownRequestIDLinks(t *testing.T) {
	raw, err := os.ReadFile("copilot_guided.go")
	if err != nil {
		t.Fatalf("copilot_guided.go: %v", err)
	}
	src := stripAPILineComments(string(raw))
	for _, needle := range []string{
		"s.knownRequestIDLinks(ctx, route.RequestID, ctxService)",
		"dedupLinksByHref(links)",
		"s.guidedRequestIDBundle(ctx, emit, &route)",
	} {
		if !strings.Contains(src, needle) {
			t.Errorf("guided yol %q çağırmıyor — çip/rota kablosu kopmuş", needle)
		}
	}
}

// extractParamInt — "…&f=123&…" içinden sayı. Test yardımcısı.
func extractParamInt(t *testing.T, url, prefix string) int64 {
	t.Helper()
	i := strings.Index(url, prefix)
	if i < 0 {
		t.Fatalf("%q bulunamadı: %s", prefix, url)
	}
	rest := url[i+len(prefix):]
	if j := strings.IndexAny(rest, "&#"); j >= 0 {
		rest = rest[:j]
	}
	n, err := strconv.ParseInt(rest, 10, 64)
	if err != nil {
		t.Fatalf("%q sayı değil: %v", rest, err)
	}
	return n
}

// v0.9.1144 — operator-reported: şablona uymayan kimlik-benzeri token
// ("kY1d" sınıfı sürprizler, bozuk kopyalama) RAG doküman katmanına
// düşüp "yüklü dokümanlarda bu bilgi yok" diyordu. Gevşek eş de
// guidedRequestID'ye gider; bundle "biçim çözülemedi" der ama doküman
// QA'sı ASLA devreye girmez. (Parser'ın kendisi de gevşedi — alnum
// AltKod artık STRICT yoldan geçiyor; buradaki fikstür ay=13 ile
// bilerek parse-geçmez.)
func TestLooseRequestIDRoutesToGuided(t *testing.T) {
	loose := "ABCD0010599310513000000004220261317093440812086" // ay=13: parse geçmez
	route := routeGuidedIntent("bu isteğe ne oldu "+loose, nil, nil, nil, "")
	if route.Intent != guidedRequestID || route.RequestID != loose {
		t.Fatalf("gevşek kimlik guided'a gitmedi: %+v", route)
	}
	if !hasGuidedSignal(normalizeGuidedMsg(loose)) {
		t.Fatal("gevşek kimlik sinyal sayılmıyor — dal hiç çalışmaz")
	}
	// Çözümlenebilir sinyal (16-hex span) gevşek bloba tercih edilir.
	route = routeGuidedIntent("span "+testSpanHex+" blob "+loose, nil, nil, nil, "")
	if route.Intent != guidedSpanByID {
		t.Fatalf("span kazanmalıydı: %+v", route)
	}
}
