package mcptools

import (
	"context"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// anchor_test.go — v0.10.50.
//
// v0.10.33 operatörün geçmişe zoom'unu sohbete taşıdı ama YALNIZ guided
// kademesinde uyguladı. Serbest tool döngüsünde çıpa operatöre (çip) ve
// modele (önsöz) İLAN EDİLİYOR, araçlar ise `time.Now()` okuyordu.
//
// ⚠ Bu, düzeltmenin kusuru KÖTÜLEŞTİRDİĞİ bir durumdu: öncesinde cevap
// sessizce yanlış pencereden geliyordu, sonrasında hem çip hem cevap
// metni operatöre YANLIŞ pencereyi TEYİT ediyordu. Etiketli yanlış,
// etiketsiz yanlıştan daha tehlikeli.

func TestRangeWindowHonorsAnchor(t *testing.T) {
	anchor := time.Date(2026, 8, 25, 4, 0, 0, 0, time.UTC)
	ctx := WithAnchor(context.Background(), anchor)

	from, to := rangeWindow(ctx, 3600)

	if !to.Equal(anchor) {
		t.Errorf("pencere SONU çıpaya oturmadı: %v (beklenen %v) — sunucu bir "+
			"pencere ilan edip başka bir pencereyi okuyor", to, anchor)
	}
	if want := anchor.Add(-time.Hour); !from.Equal(want) {
		t.Errorf("pencere BAŞI = %v; beklenen %v", from, want)
	}
}

// TestRangeWindowWithoutAnchorUsesNow — göreli pencere BOZULMAMALI.
//
// En sık yol bu: çıpa yokken pencere şimdiye kadar gelmeli. Düzeltmenin
// kendi üreteceği bir gerileme burada olurdu.
func TestRangeWindowWithoutAnchorUsesNow(t *testing.T) {
	before := time.Now()
	_, to := rangeWindow(context.Background(), 1800)
	after := time.Now()

	if to.Before(before) || to.After(after) {
		t.Errorf("çıpasız pencere sonu %v; [%v, %v] aralığında olmalıydı", to, before, after)
	}
}

// TestZeroAnchorNeverPinsTheWindowToYearOne — ÖRTÜŞEN İKİ MUHAFIZ.
//
// Göreli bir pencereyi mutlakmış gibi sabitlemek, düzeltmenin üreteceği
// YENİ bir yanlış olurdu (v0.10.33'ün ikinci yarısındaki ayrım). Sıfır
// zaman geçerli bir çıpa sanılırsa pencere 0001-01-01'e çakılır ve HER
// sorgu boş döner.
//
// ⚠ DÜRÜSTLÜK NOTU. Bu testin ilk hâli "sıfır çıpa context'e YAZILMAZ"
// diye iddia ediyordu ve o iddia BOŞTU: WithAnchor'daki koruma
// kaldırıldığında test yeşil kaldı. Sebebi, anchorOf'un saklanmış sıfır
// ile hiç-saklanmamışı ayırt edememesi — ikisi de sıfır döner.
//
// Yani bu özelliği İKİ muhafız birden koruyor (WithAnchor'ın erken
// dönüşü + rangeWindow'un IsZero dalı) ve BİRİ tek başına yetiyor. Bir
// mutasyonun ısırmaması burada ölü dal değil, örtüşme demek
// ([[feedback-overlapping-guards-shadow-mutations]]).
//
// Test artık ölçebildiği şeyi ölçüyor: UÇTAN UCA sonuç. İkisi birden
// düşerse bu kırmızıya döner.
func TestZeroAnchorNeverPinsTheWindowToYearOne(t *testing.T) {
	ctx := WithAnchor(context.Background(), time.Time{})
	_, to := rangeWindow(ctx, 600)
	if to.Year() < 2020 {
		t.Errorf("sıfır çıpa pencereyi geçmişe çakmış: %v — her sorgu boş dönerdi", to)
	}
}

// TestAnchorSurvivesDerivedContext — ARA KATMAN ÇIPAYI DÜŞÜRMEMELİ.
//
// Sohbet döngüsü her tool çağrısını `context.WithTimeout` ile sarıyor
// (copilot_chat.go runChatTool). Türetilmiş context değerleri taşır — ama
// bir gün araya değer taşımayan bir sarmalayıcı girerse çıpa SESSİZCE
// düşer ve kusur aynen geri gelir.
func TestAnchorSurvivesDerivedContext(t *testing.T) {
	anchor := time.Date(2026, 8, 25, 4, 0, 0, 0, time.UTC)
	base := WithAnchor(context.Background(), anchor)

	derived, cancel := context.WithTimeout(base, 20*time.Second)
	defer cancel()

	if _, to := rangeWindow(derived, 900); !to.Equal(anchor) {
		t.Errorf("türetilmiş context'te çıpa kayboldu: %v", to)
	}
}

// TestEveryToolWindowGoesThroughRangeWindow — TEK GEÇİT.
//
// Çıpanın araçlara ulaşmasının TEK yolu rangeWindow. Bir handler kendi
// penceresini `time.Now()` ile kurarsa çıpayı atlar ve o araç sessizce
// bugünü okur — kusurun bu dosyanın kapattığı tam biçimi.
//
// İmza değişikliği (v0.10.50) unutulan ÇAĞRI YERİNİ derleyiciye
// yakalatıyor; bu test ise BAŞKA bir pencere kurma yolunu yakalıyor.
//
// ⚠ Tarama AST üzerinden, metin üzerinden DEĞİL. Bu gece beş kez bir
// gate, aradığı deseni KENDİ yorumunda bulup kendini ısırdı; AST yorumları
// hiç görmez, yani o sınıf burada yapısal olarak imkânsız.
func TestEveryToolWindowGoesThroughRangeWindow(t *testing.T) {
	fset := token.NewFileSet()
	pkgs, err := parser.ParseDir(fset, ".", func(fi os.FileInfo) bool {
		return !strings.HasSuffix(fi.Name(), "_test.go")
	}, 0) // 0 = yorumları hiç ayrıştırma
	if err != nil {
		t.Fatalf("paket ayrıştırılamadı: %v", err)
	}
	for _, pkg := range pkgs {
		for name, f := range pkg.Files {
			base := filepath.Base(name)
			// anchor.go çıpanın kendi tanımı; tools.go rangeWindow'un
			// meşru now() dalını taşıyor (çıpa yoksa şimdi).
			if base == "anchor.go" || base == "tools.go" {
				continue
			}
			ast.Inspect(f, func(n ast.Node) bool {
				call, ok := n.(*ast.CallExpr)
				if !ok {
					return true
				}
				sel, ok := call.Fun.(*ast.SelectorExpr)
				if !ok || sel.Sel.Name != "Now" {
					return true
				}
				if id, ok := sel.X.(*ast.Ident); ok && id.Name == "time" {
					t.Errorf("%s:%d — time.Now() ile kendi penceresini kuruyor; "+
						"çıpayı ATLAR ve o araç sessizce BUGÜNÜ okur. Pencereyi "+
						"rangeWindow(ctx, …) üzerinden kur.",
						base, fset.Position(call.Pos()).Line)
				}
				return true
			})
		}
	}
}
