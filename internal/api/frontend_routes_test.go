package api

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// frontend_routes_test.go — KAYITSIZ-ROTA KAPISI, depo geneli (v0.10.8).
//
// Sunucu-üretimi bir link yalnız yolu React router'da KAYITLIYSA çalışır.
// Kayıtlı olmayan bir yol 404 vermez — App.tsx'in catch-all'ına düşer
// (`path="*"` → <Navigate to="/" replace />) ve operatörü sessizce ANA
// SAYFAYA atar. Sessiz olduğu için kimse bildirmez.
//
// v0.9.1377 bu sınıfın CANLI bir örneğini buldu (AI içgörü kartının
// "İfade detayı" çipi `/slow-queries` üretiyordu, o yol kayıtlı değil) ve
// bir kapı yazdı — ama yalnız `internal/ai/insight` paketini tarıyordu.
// Sonraki ölçüm frontend yolu üreten BEŞ yer olduğunu gösterdi:
// ai/insight · notify · api/chat_tool_links · api/copilot_followup ·
// auth/custom_roles. Kapı birini koruyup dördünü açıkta bırakıyordu.
//
// Bu dosya o kapının YERİNİ ALIYOR (insight'taki sürüm silindi). İki
// kopya kapı yazmak, bu deponun tekrar eden hastalığıydı.
//
// ── DEDEKTÖRÜN ZOR KISMI: GİDEN ÇAĞRILARI AYIRMAK ────────────────────
//
// İlk prototipim "içinde `/` olan ve satırında url/link/base geçen her
// dizge" diyordu ve üç sahte pozitif verdi — hepsi Coremetry'nin DIŞARI
// yaptığı çağrılar: `/oauth/token`, `/v2/chat/users/me/messages`
// (Zoom), `/embeddings`. Böyle bir kapı ilk günden muafiyet listesiyle
// boğulur ve anlamsızlaşır.
//
// Bu yüzden dedektör ŞEKLE bakıyor, dizgeye değil. Üç emisyon şekli var
// ve üçü de link ÜRETİMİNE özgü:
//
//	href("/x", …)        — insight paketinin kurucusu
//	Href: "/x…"          — guidedAnswerLink / followup yapıları
//	base + "/x…"         — YALNIZ PublicURL() taşıyan fonksiyon gövdesinde
//
// Üçüncüsündeki kapsam şartı taşıyıcı: notify.go'da `base` hem UI taban
// URL'i hem giden API tabanı olarak kullanılıyor. UI linklerini üreten
// fonksiyonlar `base := n.PublicURL()` ile başlıyor; giden çağrılar
// başka bir tabandan. Fonksiyon kapsamı bu ikisini kesin ayırıyor.
//
// Ölçüm (v0.10.8): 12 yol bulundu, sahte pozitif SIFIR, hepsi kayıtlı.

const appTSXPath = "../../frontend/src/App.tsx"

// NOT: `stripGoComments` bu pakette ZATEN var (trace_resolve_test.go) ve
// aynı gerekçeyle yazılmış: "yorumları sıyırmadan tarayan bir test kendi
// açıklamasıyla eşleşir". İkinci bir kopya yazmak bu dosyanın eleştirdiği
// hastalığın ta kendisi olurdu — yeniden kullanılıyor.
//
// Burada da şart: notify.go'nun bir ŞERHİ `base + "/route"` yazıyor ve ham
// metne bakan dedektör onu emisyon sanıyordu.

var (
	reHrefCall   = regexp.MustCompile(`href\("(/[^"]*)"`)
	reHrefField  = regexp.MustCompile(`\bHref:\s*"(/[^"]*)"`)
	reBaseConcat = regexp.MustCompile(`\bbase\s*\+\s*"(/[^"]*)"`)
)

// FrontendPathsIn — bir Go kaynağının ÜRETTİĞİ frontend yolları.
//
// Saf: sentetik girdiyle sınanabilsin diye. Kapıyı yalnız canlı ağaçta
// koşturmak, ağaç yeşilken BOZUK bir dedektörü çalışandan ayırt
// edilemez kılar.
func FrontendPathsIn(src string) []string {
	code := stripGoComments(src)
	seen := map[string]struct{}{}
	add := func(p string) { seen[strings.SplitN(p, "?", 2)[0]] = struct{}{} }

	for _, m := range reHrefCall.FindAllStringSubmatch(code, -1) {
		add(m[1])
	}
	for _, m := range reHrefField.FindAllStringSubmatch(code, -1) {
		add(m[1])
	}
	// `base + "/x"` YALNIZ PublicURL() taşıyan fonksiyon gövdesinde sayılır
	// — aksi hâlde giden API çağrıları da yakalanır.
	for _, fn := range regexp.MustCompile(`\nfunc `).Split(code, -1) {
		if !strings.Contains(fn, "PublicURL()") {
			continue
		}
		for _, m := range reBaseConcat.FindAllStringSubmatch(fn, -1) {
			add(m[1])
		}
	}
	out := make([]string, 0, len(seen))
	for p := range seen {
		out = append(out, p)
	}
	sort.Strings(out)
	return out
}

func registeredRoutes(t *testing.T) map[string]bool {
	t.Helper()
	b, err := os.ReadFile(appTSXPath)
	if err != nil {
		t.Skipf("App.tsx okunamadı (%v) — frontend'siz checkout'ta kapı kapsam dışı", err)
	}
	out := map[string]bool{}
	for _, m := range regexp.MustCompile(`<Route\s+path="([^"]+)"`).FindAllStringSubmatch(string(b), -1) {
		out[m[1]] = true
	}
	if len(out) < 20 {
		// Regex kayarsa küme küçülür ve kapı HER ŞEYİ geçirir; yeşil
		// kalarak. Ölçülen gerçek sayı bugün 60+.
		t.Fatalf("App.tsx'ten yalnız %d rota çıkarıldı — regex kaymış olmalı", len(out))
	}
	if !out["*"] {
		t.Fatal("catch-all rota (`*`) yok — kapının tehdit modeli bu, yokluğu ölçümü geçersiz kılar")
	}
	return out
}

func TestFrontendPathsIn_Synthetic(t *testing.T) {
	for _, tc := range []struct {
		name string
		src  string
		want []string
	}{
		{"href kurucusu", `x := href("/traces", kv{"a","b"})`, []string{"/traces"}},
		{"Href alanı", `return link{Href: "/service?name=" + q}`, []string{"/service"}},
		{"sorgu dizgesi ayrılır", `href("/logs?service=x&q=y")`, []string{"/logs"}},
		{"YORUM sayılmaz", "// base + \"/route\" ile kurulur\n", nil},
		{"blok yorum sayılmaz", "/* Href: \"/nope\" */", nil},
		// Ayırt edici çift: aynı `base + "/x"` şekli, biri UI biri giden.
		{"PublicURL taşıyan fonksiyon SAYILIR",
			"\nfunc (n *N) u() string {\n\tbase := n.PublicURL()\n\treturn base + \"/problems?problem=\" + id\n}\n",
			[]string{"/problems"}},
		{"PublicURL YOKSA sayılmaz (giden API çağrısı)",
			"\nfunc (n *N) tok() string {\n\tbase := n.apiEndpoint\n\treturn base + \"/oauth/token\"\n}\n",
			nil},
		{"boş kaynak", ``, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := FrontendPathsIn(tc.src)
			if len(got) != len(tc.want) {
				t.Fatalf("FrontendPathsIn = %v; want %v", got, tc.want)
			}
			for i := range got {
				if got[i] != tc.want[i] {
					t.Errorf("FrontendPathsIn = %v; want %v", got, tc.want)
				}
			}
		})
	}
}

func TestServerEmittedPathsAreRegisteredRoutes(t *testing.T) {
	routes := registeredRoutes(t)

	found := map[string][]string{}
	err := filepath.Walk("..", func(p string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() || !strings.HasSuffix(p, ".go") || strings.HasSuffix(p, "_test.go") {
			return nil
		}
		b, rerr := os.ReadFile(p)
		if rerr != nil {
			return nil
		}
		for _, path := range FrontendPathsIn(string(b)) {
			found[path] = append(found[path], p)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("internal/ yürünemedi: %v", err)
	}
	if len(found) < 8 {
		// Boş küme tuzağı: dedektör bozulursa kapı sessizce her şeyi
		// geçirir. Ölçülen gerçek sayı 12.
		t.Fatalf("yalnız %d frontend yolu bulundu — dedektör kaymış olmalı", len(found))
	}

	var bad []string
	for p, srcs := range found {
		if !routes[p] {
			bad = append(bad, p+" ("+strings.Join(srcs, ", ")+")")
		}
	}
	sort.Strings(bad)
	if len(bad) > 0 {
		t.Errorf("KAYITSIZ rota üretiliyor — catch-all operatörü ana sayfaya atar:\n  %s",
			strings.Join(bad, "\n  "))
	}
}

// TestLegacyPageIDsMapToRegisteredRoutes — yönlendirme tablosu, emisyon
// DEĞİL.
//
// `auth/custom_roles.go`'daki `legacyPageIDs` eski sayfa kimliklerini
// güncel rotalara eşliyor. ANAHTARLARI kasten kayıtsız olabilir (emekli
// rotalar; `/admin/stats` bugün öyle) — o yüzden yukarıdaki kapı bu
// dosyayı zaten görmüyor, şekilleri farklı. Ama DEĞERLER kayıtlı olmak
// zorunda: değilse rol, kullanıcıyı var olmayan bir sayfaya "iyileştirir"
// ve AppShell muhafızı onu geri atar.
func TestLegacyPageIDsMapToRegisteredRoutes(t *testing.T) {
	routes := registeredRoutes(t)
	b, err := os.ReadFile("../auth/custom_roles.go")
	if err != nil {
		t.Skipf("custom_roles.go okunamadı: %v", err)
	}
	body := stripGoComments(string(b))
	m := regexp.MustCompile(`legacyPageIDs\s*=\s*map\[string\]string\{([\s\S]*?)\n\}`).FindStringSubmatch(body)
	if m == nil {
		t.Fatal("legacyPageIDs bulunamadı — yeniden adlandırıldıysa bu kapıyı da güncelle")
	}
	pairs := regexp.MustCompile(`"([^"]+)":\s*"([^"]+)"`).FindAllStringSubmatch(m[1], -1)
	if len(pairs) == 0 {
		t.Fatal("legacyPageIDs boş ayrıştırıldı — regex kaymış olmalı")
	}
	for _, p := range pairs {
		if !routes[p[2]] {
			t.Errorf("legacyPageIDs[%q] = %q ama o rota KAYITLI DEĞİL — rol kullanıcıyı olmayan bir sayfaya taşır", p[1], p[2])
		}
	}
}

// TestCatchAllStillRedirectsHome — kapının TEHDİT MODELİNİ pinler.
//
// Yukarıdaki kapılar "kayıtsız yol zararlıdır" varsayımına dayanıyor ve o
// varsayım App.tsx'in catch-all davranışından geliyor. Catch-all bir gün
// gerçek bir 404 sayfasına dönerse zarar DEĞİŞİR (sessiz yön değişimi
// yerine görünür hata) — kapı yine yararlıdır ama şerhleri yanlış olur.
func TestCatchAllStillRedirectsHome(t *testing.T) {
	b, err := os.ReadFile(appTSXPath)
	if err != nil {
		t.Skipf("App.tsx okunamadı: %v", err)
	}
	if !strings.Contains(string(b), `<Route path="*" element={<Navigate to="/" replace />} />`) {
		t.Error(`catch-all artık ana sayfaya yönlendirmiyor — bu dosyanın şerhlerini güncelle ` +
			`(kayıtsız yolun zararı "sessiz yön değişimi" olmaktan çıkmış olabilir)`)
	}
}
