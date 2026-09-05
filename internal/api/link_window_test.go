package api

// link_window_test.go — v0.9.1321 (§3.1 K6).
//
// Symptom (audit-found): backend'in ürettiği 33 ürün linkinden 32'si
// penceresizdi. AI cevabı "14:32'de payments p99 fırladı" dedikten sonra
// altındaki "Trace'ler →" çipi operatörü 14:32'ye değil sticky penceresine
// götürüyordu — cevabın konuştuğu olay ekranda YOK.
//
// Asıl düzeltme testte değil İMZADA: guidedAnswerLinks ve toolCallLink
// artık pencereyi argüman olarak alıyor, yani 34'üncü siteyi yazan kişi
// karar vermek zorunda. İmzanın ifade EDEMEDİĞİ üç şey burada pinleniyor:
//
//	1. penceresiz ham üreticiler (…Targets) yalnız sarmalayıcıdan çağrılır
//	   — kimse pencereyi atlayarak yan kapıdan geçemesin;
//	2. K4 ölü-param: zaman ekseni OLMAYAN hedeflere range YAZILMAZ;
//	3. hedefin kendi (daha dar) penceresi ezilmez.

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"strings"
	"testing"
	"time"
)

func TestLinkWindowApply(t *testing.T) {
	now := time.UnixMilli(1_700_000_000_000)
	win := linkWindowRelative(now, 3600) // 1_699_996_400_000 .. 1_700_000_000_000
	const q = "range=custom:1699996400000-1700000000000"

	cases := []struct {
		name, in, want string
		w              linkWindow
	}{
		{"paramsız hedef ? alır", "/databases", "/databases?" + q, win},
		{"paramlı hedef & alır", "/traces?service=x", "/traces?service=x&" + q, win},
		{"pencere yoksa dokunulmaz", "/traces?service=x", "/traces?service=x", noLinkWindow()},
		// Hedef her zaman kazanır — navHref'in frontend'deki kuralıyla aynı.
		{"hedefin kendi penceresi ezilmez",
			"/logs?q=abc&range=custom:1-2", "/logs?q=abc&range=custom:1-2", win},
		// K4: zaman ekseni olmayan üç sayfa.
		{"/inbox ölü param almaz", "/inbox?kind=exception", "/inbox?kind=exception", win},
		{"/problems ölü param almaz", "/problems?service=x", "/problems?service=x", win},
		{"/anomalies ölü param almaz", "/anomalies", "/anomalies", win},
		// Bilinmeyen bir yol da güvenli tarafta kalır: listede yoksa yazma.
		{"bilinmeyen yol", "/whatever", "/whatever", win},
		// `range` bir DEĞERİN içinde geçiyorsa anahtar sayılmaz.
		{"değerin içindeki range= anahtar değildir",
			"/logs?q=range%3Dfoo", "/logs?q=range%3Dfoo&" + q, win},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := c.w.apply(c.in); got != c.want {
				t.Errorf("apply(%q) = %q, beklenen %q", c.in, got, c.want)
			}
		})
	}
}

func TestLinkWindowConstructors(t *testing.T) {
	now := time.UnixMilli(1_700_000_000_000)
	cases := []struct {
		name string
		w    linkWindow
		set  bool
	}{
		{"between: normal", linkWindowBetween(now.Add(-time.Hour), now), true},
		{"between: ters aralık", linkWindowBetween(now, now.Add(-time.Hour)), false},
		{"between: sıfır genişlik", linkWindowBetween(now, now), false},
		{"between: sıfır zaman", linkWindowBetween(time.Time{}, now), false},
		{"relative: normal", linkWindowRelative(now, 300), true},
		{"relative: rangeS=0", linkWindowRelative(now, 0), false},
		{"relative: negatif rangeS", linkWindowRelative(now, -1), false},
		{"relative: sıfır now", linkWindowRelative(time.Time{}, 300), false},
		{"noLinkWindow", noLinkWindow(), false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if c.w.set != c.set {
				t.Errorf("set = %v, beklenen %v", c.w.set, c.set)
			}
			// Kurulmamış pencere hiçbir href'i değiştirmemeli.
			if !c.set && c.w.apply("/traces") != "/traces" {
				t.Errorf("kurulmamış pencere href'e yazdı: %q", c.w.apply("/traces"))
			}
		})
	}
}

// TestGuidedAnswerLinksCarryWindow — sarmalayıcı gerçekten uyguluyor mu.
// guidedServiceHealth iki link döndürür (/service + /traces), ikisi de
// zaman eksenli.
func TestGuidedAnswerLinksCarryWindow(t *testing.T) {
	now := time.UnixMilli(1_700_000_000_000)
	links := guidedAnswerLinks(
		guidedRoute{Intent: guidedServiceHealth, Service: "payments"},
		linkWindowBetween(now.Add(-time.Hour), now))
	if len(links) != 2 {
		t.Fatalf("iki link beklenirdi: %+v", links)
	}
	for _, l := range links {
		if !hasQueryKey(l.Href, "range") {
			t.Errorf("çip pencereyi düşürdü: %q", l.Href)
		}
	}
	// Aynı rota penceresiz çağrıldığında href'ler eski hâlinde kalmalı —
	// pencere uygulaması başka hiçbir şeyi değiştirmiyor.
	plain := guidedAnswerLinks(guidedRoute{Intent: guidedServiceHealth, Service: "payments"}, noLinkWindow())
	if plain[0].Href != "/service?name=payments" || plain[1].Href != "/traces?service=payments" {
		t.Errorf("penceresiz hâl değişti: %+v", plain)
	}
}

// TestRequestIDLogLinkKeepsItsOwnWindow — v0.9.1321'in korumak zorunda
// olduğu gerileme: request-ID rotası kendi DAHA DAR penceresini yazıyor
// (tek istek anı, copilot_followup.go). Cevabın geniş penceresi onu
// ezerse operatör istek anını değil bütün aralığı görür.
func TestRequestIDLogLinkKeepsItsOwnWindow(t *testing.T) {
	now := time.UnixMilli(1_700_000_000_000)
	links := guidedAnswerLinks(guidedRoute{
		Intent: guidedRequestID, RequestID: "req-1",
		ReqWindowFromMs: 1_699_999_000_000, ReqWindowToMs: 1_699_999_060_000,
	}, linkWindowBetween(now.Add(-time.Hour), now))
	if len(links) != 1 {
		t.Fatalf("tek log çipi beklenirdi: %+v", links)
	}
	const want = "/logs?q=req-1&range=custom:1699999000000-1699999060000"
	if links[0].Href != want {
		t.Errorf("dar pencere ezildi:\n got %q\nwant %q", links[0].Href, want)
	}
}

func hasQueryKey(href, key string) bool {
	for i := 0; i+len(key)+1 <= len(href); i++ {
		if (href[i] == '?' || href[i] == '&') && href[i+1:i+1+len(key)] == key &&
			i+1+len(key) < len(href) && href[i+1+len(key)] == '=' {
			return true
		}
	}
	return false
}

// TestWindowlessProducersHaveOneCaller — imzanın ifade edemediği tek şey:
// penceresiz HAM üreticilere yan kapıdan girilemez. Bir yerde
// guidedAnswerLinkTargets / toolCallLinkTarget doğrudan çağrılırsa
// pencere sessizce düşer ve K6 geri gelir.
//
// AST ile ölçülür, metin taramasıyla değil — bir yorumda geçen ad
// çağrı sayılmasın (aynı gerekçe: internal/chstore/mv_positional_test.go).
func TestWindowlessProducersHaveOneCaller(t *testing.T) {
	// üretici adı → onu çağırmasına izin verilen TEK sarmalayıcı
	allowed := map[string]string{
		"guidedAnswerLinkTargets": "guidedAnswerLinks",
		"toolCallLinkTarget":      "toolCallLink",
	}
	fset := token.NewFileSet()
	pkgs, err := parser.ParseDir(fset, ".", func(fi fs.FileInfo) bool {
		return !strings.HasSuffix(fi.Name(), "_test.go")
	}, 0)
	if err != nil {
		t.Fatalf("parse internal/api: %v", err)
	}
	counts := map[string]map[string]int{} // üretici → çağıran fonksiyon → adet
	for _, pkg := range pkgs {
		for path, f := range pkg.Files {
			for _, decl := range f.Decls {
				fn, ok := decl.(*ast.FuncDecl)
				if !ok {
					continue
				}
				ast.Inspect(fn, func(n ast.Node) bool {
					call, ok := n.(*ast.CallExpr)
					if !ok {
						return true
					}
					id, ok := call.Fun.(*ast.Ident)
					if !ok {
						return true
					}
					if _, tracked := allowed[id.Name]; !tracked {
						return true
					}
					if counts[id.Name] == nil {
						counts[id.Name] = map[string]int{}
					}
					counts[id.Name][fn.Name.Name+" ("+path+")"]++
					return true
				})
			}
		}
	}
	for producer, wrapper := range allowed {
		callers := counts[producer]
		if len(callers) == 0 {
			t.Errorf("%s hiç çağrılmıyor — ölü kod mu, yoksa ad mı değişti?", producer)
			continue
		}
		for caller, n := range callers {
			if !strings.HasPrefix(caller, wrapper+" ") {
				t.Errorf("%s, %s içinden çağrılıyor (%d kez) — pencere atlanır; %s kullan",
					producer, caller, n, wrapper)
			}
		}
	}
}

// TestGuidedHandlerPassesARealWindow — imzanın yakalayamadığı İKİNCİ
// şey: bir çağıran derlemeyi memnun etmek için noLinkWindow() yazıp
// bug'ı geri getirebilir. Mutasyon turunda tam bu oldu — handler'ın
// çağrısını noLinkWindow()'a çevirmek HİÇBİR testi kırmadı, çünkü
// copilot_guided.go'nun akış handler'ı birim testi taşımıyor.
//
// Sayı değil SÖZLEŞME pinlenir: guided handler, linklere kendi
// hesapladığı aralıktan türeyen bir pencere vermek ZORUNDA
// (linkWindowBetween / linkWindowRelative). Pencereyi bilerek
// düşürmek isteyen biri bu testi de değiştirmek zorunda kalsın —
// sessizce yapamasın.
func TestGuidedHandlerPassesARealWindow(t *testing.T) {
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "copilot_guided.go", nil, 0)
	if err != nil {
		t.Fatalf("parse copilot_guided.go: %v", err)
	}
	found := 0
	ast.Inspect(f, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		id, ok := call.Fun.(*ast.Ident)
		if !ok || id.Name != "guidedAnswerLinks" {
			return true
		}
		found++
		if len(call.Args) != 2 {
			t.Errorf("%s: guidedAnswerLinks iki argüman almalı", fset.Position(call.Pos()))
			return true
		}
		arg, ok := call.Args[1].(*ast.CallExpr)
		if !ok {
			t.Errorf("%s: pencere argümanı bir yapıcı çağrısı olmalı", fset.Position(call.Pos()))
			return true
		}
		name, _ := arg.Fun.(*ast.Ident)
		if name == nil || (name.Name != "linkWindowBetween" && name.Name != "linkWindowRelative") {
			t.Errorf("%s: guided handler linklere GERÇEK bir pencere vermeli "+
				"(linkWindowBetween/linkWindowRelative); %v verilmiş — §3.1 K6 geri gelir",
				fset.Position(call.Pos()), name)
		}
		return true
	})
	// v0.10.434 (D7b) — ikinci çağrı open_page'in LLM'siz cevap yolu
	// (runGuidedRoute başı); aynı linkWindowBetween(from, to) sözleşmesi,
	// yukarıdaki AST denetimi her çağrıyı ayrı ayrı doğrular.
	if found != 2 {
		t.Errorf("copilot_guided.go'da %d guidedAnswerLinks çağrısı var, 2 bekleniyordu — "+
			"yeni bir çağrı eklendiyse pencere sözleşmesi onun için de doğrulanmalı", found)
	}
}
