package devops

import (
	"context"
	"os"
	"strings"
	"testing"

	"github.com/cilcenk/coremetry/internal/stackparse"
)

// resource_hunt_test.go — v0.10.73.
//
// Kod bağlamı yalnız stack frame'lerinin .java dosyalarını çekiyordu.
// Bir sorgu hatasında asıl kanıt çoğu zaman mapper XML'i ya da SQL
// parçasıdır — ve operatörün ekranında model o dosyadan söz etti ama
// dosya HİÇ GÖNDERİLMEMİŞTİ; adı çıkarımla üretilmişti.
//
// v0.10.72'den sonra model bunu yapamıyor (uydurma yasak), yani dosya
// gerçekten gönderilmedikçe kanıt EKSİK kalır. Bu av o boşluğu kapatıyor.

func TestResourceIsFetchedWhenNamedInTheError(t *testing.T) {
	paths := []string{
		"/src/main/java/com/example/repo/OrderRepo.java",
		"/src/main/resources/mapper/OrderMapper.xml",
		"/src/main/resources/mapper/OtherMapper.xml",
	}
	fetched := []string{}
	fetch := func(_ context.Context, p string) (string, error) {
		fetched = append(fetched, p)
		return "<mapper>\n  <select id=\"selectById\">SELECT 1</select>\n</mapper>\n", nil
	}
	refs := stackparse.ResourceRefs("### Error querying database; com.example.repo.OrderMapper.selectById")

	out := huntResources(context.Background(), refs, paths, fetch)
	if len(out) != 1 {
		t.Fatalf("pencere=%d, 1 bekleniyordu (çekilen: %v)", len(out), fetched)
	}
	if !out[0].Resource {
		t.Error("pencere KAYNAK diye işaretlenmedi — prompt onu 'hata burada' diye sunar")
	}
	if !strings.HasSuffix(out[0].Path, "/OrderMapper.xml") {
		t.Errorf("yanlış dosya çekildi: %s", out[0].Path)
	}
	// v0.10.113 — Frame artık "statement id: …" TAŞIYABİLİR (sorgu bloğu);
	// yasak olan, bir ÇAĞRI YIĞINI frame'i gibi görünmesi ("X.java:N").
	if f := out[0].Frame; f != "" && !strings.HasPrefix(f, "statement id: ") {
		t.Errorf("kaynak penceresine stack frame'i yazılmış — çağrı yığınından gelmiyor: %q", f)
	}
}

// TestBaseNameMatchIsExact — YANLIŞ DOSYA KANITTAN KÖTÜDÜR.
//
// Gevşek eşleşme (`OrderMapper` → `OrderMapperTest.xml`) modeli yanlış
// tanıma bakarak "uyuşmazlık yok" dedirtirdi.
func TestBaseNameMatchIsExact(t *testing.T) {
	paths := []string{"/res/OrderMapperTest.xml", "/res/MyOrderMapper.xml"}
	got := bestPathForResource(paths, stackparse.ResourceRef{Base: "OrderMapper"})
	if got != "" {
		t.Errorf("gevşek eşleşme kabul edildi: %s", got)
	}
	paths = append(paths, "/res/OrderMapper.xml")
	if got := bestPathForResource(paths, stackparse.ResourceRef{Base: "OrderMapper"}); got != "/res/OrderMapper.xml" {
		t.Errorf("birebir eşleşme bulunamadı: %q", got)
	}
}

// TestResourceBudgetIsBounded — KOD BÜTÇESİNİ YEMESİN.
//
// Kod pencereleri ASIL kanıt; kaynak onları DESTEKLER. Sınırsız kaynak
// çekimi prompt bütçesini yer ve kod pencereleri kırpılır.
func TestResourceBudgetIsBounded(t *testing.T) {
	var paths []string
	var refs []stackparse.ResourceRef
	for _, n := range []string{"A", "B", "C", "D", "E"} {
		paths = append(paths, "/res/"+n+"Mapper.xml")
		refs = append(refs, stackparse.ResourceRef{Base: n + "Mapper", Ext: ".xml"})
	}
	fetches := 0
	fetch := func(context.Context, string) (string, error) {
		fetches++
		return "<mapper/>\n", nil
	}
	out := huntResources(context.Background(), refs, paths, fetch)
	if len(out) != resourceFetchLimit || fetches != resourceFetchLimit {
		t.Errorf("pencere=%d çekim=%d; tavan %d olmalıydı", len(out), fetches, resourceFetchLimit)
	}
}

// TestNoRefsNoFetch — en sık yol; ağa hiç çıkılmamalı.
func TestNoRefsNoFetch(t *testing.T) {
	fetch := func(context.Context, string) (string, error) {
		t.Fatal("aday yokken çekim yapıldı")
		return "", nil
	}
	if out := huntResources(context.Background(), nil, []string{"/a.xml"}, fetch); out != nil {
		t.Errorf("aday yokken pencere üretildi: %+v", out)
	}
}

// TestPromptLabelsResourceDistinctly — MODEL NE OKUDUĞUNU BİLMELİ.
//
// Kaynak dosyada "hata satırı" YOK. Frame penceresiyle aynı etiketle
// sunulursa model XML'de olmayan bir satırı suçlar.
func TestPromptLabelsResourceDistinctly(t *testing.T) {
	cc := CodeContext{
		Repo: "r",
		Windows: []CodeWindow{
			{Path: "/src/A.java", Frame: "com.x.A.m(A.java:10)", Line: 10, FromLine: 1, ToLine: 20, Content: "  10  x"},
			{Path: "/res/OrderMapper.xml", Resource: true, ToLine: 200, Content: "<mapper/>"},
		},
	}
	block := cc.PromptBlock()
	if !strings.Contains(block, "hata metninin ANDIĞI dosya") {
		t.Error("kaynak penceresi ayırt edilmiyor")
	}
	if !strings.Contains(block, "stack buraya işaret etmiyor") {
		t.Error("modele 'burada hata satırı yok' denmiyor")
	}
}

// TestResourceHuntIsReachable — ⚠ MUHAFIZ ULAŞILABİLİR OLMALI.
//
// Yukarıdaki testler huntResources'ı DOĞRUDAN çağırıyor. Mutasyon
// denemesinde FetchCode'un çağrısını `nil` ile değiştirdim ve HİÇBİR TEST
// KIRILMADI: saf çekirdek yeşil, çağrıldığı yer pinsiz — bu deponun
// tekrar eden sınıfı ([[feedback-tested-but-unreachable]]).
//
// Bu kapı zinciri uçtan uca çiviliyor: hata metni → aday → av.
func TestResourceHuntIsReachable(t *testing.T) {
	src := readDevopsSource(t, "code.go")
	if !strings.Contains(flatWSDevops(src), "huntResources(ctx, refs, paths,") {
		t.Error("FetchCode kaynak avını GERÇEK adaylarla çağırmıyor — av ölü yol")
	}
	// Ve adayların kaynağı: API katmanı ham stack metninden çıkarmalı.
	api := readRepoFileDevops(t, "../api/copilot_code.go")
	if !strings.Contains(api, "stackparse.ResourceRefs(stack)") {
		t.Error("API katmanı aday üretmiyor — FetchCode'a hep boş liste gider " +
			"ve kaynak avı hiç çalışmaz")
	}
}

func readRepoFileDevops(t *testing.T, rel string) string {
	t.Helper()
	b, err := os.ReadFile(rel)
	if err != nil {
		t.Fatalf("%s okunamadı: %v", rel, err)
	}
	return string(b)
}

func flatWSDevops(s string) string { return strings.Join(strings.Fields(s), " ") }
