package api

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// v0.9.1118 (AI Faz 0.2) kaynak-pinleri — 503 ön-kapısı sınıfının
// mezar taşları. Bu blok 20 handler'a kopyalanmıştı ve üç kez
// (v0.9.1071/1080/1101) kapısız yeni uç regresyonu yaşandı. İki kural:
//
//  1. ai_routes.go'daki HER POST /api/copilot/ kaydı requireCopilot
//     ile sarılı olmalı — sarımsız yeni uç bu testte kırmızı yanar.
//  2. Paket içinde handler-içi `!s.copilot.Active()` kapısı kalmamalı;
//     izinli istisnalar middleware'in kendisi (ai_routes.go) ve
//     domain'e gömülü rootcause verdict gövdesi (rootcause.go).
func TestRequireCopilotRouteCoverage(t *testing.T) {
	b, err := os.ReadFile("ai_routes.go")
	if err != nil {
		t.Fatalf("ai_routes.go okunamadı: %v", err)
	}
	for _, line := range strings.Split(string(b), "\n") {
		if strings.Contains(line, `"POST   /api/copilot/`) &&
			!strings.Contains(line, "s.requireCopilot(") {
			t.Errorf("requireCopilot sarımı olmayan copilot route'u: %s", strings.TrimSpace(line))
		}
	}
}

// v0.9.1129 — kapı İKİ yazılışla taranıyor.
//
// Ders (feedback-gate-signal-must-cover-both-spellings): v0.9.1118'de
// tarama tek dizeye (`!s.copilot.Active()`) bakıyordu. Faz 2.1 kapıyı
// `s.copilotReady()` yardımcısına taşıdı — tek dizeli bir tarama, o
// yardımcıyı handler'a kopyalayan bir regresyonu YEŞİL geçirirdi.
// Yardımcı, kapının yeni ADI; tarayıcı ikisini de bilmek zorunda.
//
// İzinli istisnalar, her biri GEREKÇELİ:
//   - ai_routes.go — middleware'in ve yardımcının kendi evi;
//   - rootcause.go — domain'e gömülü verdict gövdesi (v0.9.1118);
//   - insight.go — kart, LLM yokluğunu 503 ile DEĞİL cevap şekliyle
//     (AIOff) anlatıyor; sözleşme tam olarak "AI kapalıyken de cevap
//     ver" olduğu için bu bir kapı kopyası değil, ürün kararı.
func TestNoInlineCopilotGates(t *testing.T) {
	spellings := []struct {
		needle  string
		allowed map[string]bool
		hint    string
	}{
		{
			needle:  "!s.copilot.Active()",
			allowed: map[string]bool{"ai_routes.go": true, "rootcause.go": true},
			hint:    "route'u ai_routes.go'da requireCopilot ile sar",
		},
		{
			needle:  "s.copilotReady()",
			allowed: map[string]bool{"ai_routes.go": true, "rootcause.go": true, "insight.go": true},
			hint:    "kapının yeni adı da kapıdır — requireCopilot kullan (ya da gerekçesini bu listeye yaz)",
		},
	}
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatalf("%s okunamadı: %v", f, err)
		}
		src := string(b)
		// v0.9.1281 — muafiyet ADA değil MEKANİZMAYA bağlandı.
		//
		// Kapının adı "handler-içi copilot kapısı" ve korktuğu şey somut:
		// bir HTTP ucunun 503 ön-kapısını middleware yerine gövdesine
		// kopyalaması. HİÇ handler İÇERMEYEN bir dosya bu hatayı tanım
		// gereği yapamaz — requireCopilot HTTP middleware'i, sarabileceği
		// bir route yoksa reçete de uygulanamaz.
		//
		// Somut vaka: rca_auto_verdict.go (v0.9.1281). Arka plan işçisi
		// tarafından çağrılan bir metot; ResponseWriter yok, route yok,
		// dönecek 503 yok. Doğru davranış ProblemExplainer'ınkinin aynısı
		// — sessizce atlamak — ve o da `!s.copilot.Active()` yazılışını
		// gerektiriyor.
		//
		// Muafiyet DAR: dosyaya bir handler eklenir eklenmez kapı geri
		// gelir. Yani bu, muhafızı gevşetmek değil, korktuğu şeyi
		// dosya adı listesi yerine mekanizmayla ifade etmek.
		if !strings.Contains(src, "http.ResponseWriter") {
			continue
		}
		for _, sp := range spellings {
			if sp.allowed[f] {
				continue
			}
			if strings.Contains(src, sp.needle) {
				t.Errorf("%s: handler-içi copilot kapısı (%s) — %s", f, sp.needle, sp.hint)
			}
		}
	}
}

// TestWorkerCopilotGateStaysGuarded — yukarıdaki muafiyetin karşı ağırlığı
// (v0.9.1281).
//
// Muafiyet "handler yoksa serbest" diyor. Bu test, muaf tutulan dosyanın
// GERÇEKTEN handler'sız olduğunu ve kapıyı da GERÇEKTEN taşıdığını
// pinliyor. İkisi birden gerekli: kapısız kalırsa arka plan işçisi Copilot
// kapalıyken sağlayıcıyı döver (v0.9.200'ün devre kesicisini kuran olay),
// handler kazanırsa muafiyet geçersizleşir ve üstteki tarama zaten yanar.
func TestWorkerCopilotGateStaysGuarded(t *testing.T) {
	b, err := os.ReadFile("rca_auto_verdict.go")
	if err != nil {
		t.Fatalf("rca_auto_verdict.go okunamadı: %v", err)
	}
	src := string(b)
	if strings.Contains(src, "http.ResponseWriter") {
		t.Fatal("rca_auto_verdict.go artık HTTP handler içeriyor — muafiyet " +
			"geçersiz; ucu ai_routes.go'da requireCopilot ile sar")
	}
	for _, needle := range []string{"s.copilot.Active()", "QuotaBackoffActive()", "AutoExplainEnabled()"} {
		if !strings.Contains(src, needle) {
			t.Errorf("otomatik verdict yolu %s kapısını kaybetmiş — arka plan "+
				"tüketicisi Copilot kapalıyken/kotada sağlayıcıyı dövmeye devam eder "+
				"(ProblemExplainer.tickIfLeader ile aynı üçlü)", needle)
		}
	}
}

// TestInsightRouteNotCopilotGated — namespace kararının pini
// (v0.9.1129, Faz 2.1).
//
// Kart, AI kapalıyken de tam değerli cevap vermek ZORUNDA: sinyaller ve
// linkler deterministik, prose opsiyonel bir katman. requireCopilot bu
// ucu 503'lerse mockup sözleşmesi sessizce kırılır — kart boş çizilir ve
// operatör "AI arızalı" sanır. Yukarıdaki kapı-testleri "sarılmamış uç"
// arıyor; bu test TERSİNİ arıyor, çünkü buradaki doğru hâl sarılmamış
// olmak ve bir sonraki geliştirici bunu "eksik kapı" sanabilir.
func TestInsightRouteNotCopilotGated(t *testing.T) {
	b, err := os.ReadFile("ai_routes.go")
	if err != nil {
		t.Fatalf("ai_routes.go okunamadı: %v", err)
	}
	found := false
	for _, line := range strings.Split(string(b), "\n") {
		code := line
		if i := strings.Index(code, "//"); i >= 0 {
			code = code[:i]
		}
		if !strings.Contains(code, `"GET    /api/insight/`) {
			continue
		}
		found = true
		if strings.Contains(code, "s.requireCopilot(") {
			t.Errorf("insight route'u requireCopilot ile sarılmış: %s\n"+
				"Kart AI KAPALIYKEN de sinyal+link döndürmeli (insight.go, "+
				"\"NEDEN /api/copilot/ DEĞİL\"). 503 burada yanlış cevaptır.",
				strings.TrimSpace(line))
		}
		if strings.Contains(code, "auth.RequireRole") || strings.Contains(code, "auth.RequireAnyRole") {
			t.Errorf("insight route'una rol kapısı eklenmiş: %s\n"+
				"Projelediği veri (problem/exception) viewer'a AÇIK; kartı "+
				"viewer'dan saklamak durumu okunamaz kılar.", strings.TrimSpace(line))
		}
	}
	if !found {
		t.Error("ai_routes.go'da GET /api/insight/ kaydı yok — route taşındıysa bu pini güncelle")
	}
}
