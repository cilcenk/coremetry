package api

import (
	"strings"
	"testing"
	"time"

	"github.com/cilcenk/coremetry/internal/chstore"
)

// v0.10.21 — Copilot denetimi bulgu #2: SUNUCU KENDİSİ yanlış sayı
// üretiyordu.
//
// `renderProblemsEvidenceTR` başlığı "toplam %d" derken `len(probs)`
// basıyordu ve `probs` bir SQL LIMIT'inin çıktısıydı (guided yolların
// üçünde 10, ikisinde 50). 47 açık problemi olan bir serviste modele
// "toplam 10" gidiyordu.
//
// ⚠ BU, UYDURMADAN FARKLI VE DAHA KÖTÜ BİR SINIF. Model uydurmuyor;
// prompt kurallarına mükemmel uyup kendisine verilen yanlış sayıyı
// sadakatle aktarıyor. Hiçbir anti-uydurma kuralı, hiçbir sıcaklık
// ayarı, hiçbir kalkan bunu yakalayamaz — çünkü kusur modelde değil,
// kanıtı üreten kodda.

func TestCountableFilter(t *testing.T) {
	for _, tc := range []struct {
		name string
		f    chstore.ProblemFilter
		want bool
	}{
		{"guided süzgeci — sayılabilir", chstore.ProblemFilter{Status: "open", Service: "a", Env: "uat"}, true},
		{"boş", chstore.ProblemFilter{}, true},
		// ⚠ CountProblems bu üç yüklemi BİLMİYOR; sayarsa daha GENİŞ bir
		// küme sayar ve "düzeltme" yeni bir yalan üretir.
		{"Services dilimi — sayılamaz", chstore.ProblemFilter{Services: []string{"a"}}, false},
		{"boş Services dilimi de sayılamaz", chstore.ProblemFilter{Services: []string{}}, false},
		{"NotStatuses — sayılamaz", chstore.ProblemFilter{NotStatuses: []string{"resolved"}}, false},
		{"IDs — sayılamaz", chstore.ProblemFilter{IDs: []string{"p1"}}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := countableFilter(tc.f); got != tc.want {
				t.Errorf("countableFilter = %v; want %v", got, tc.want)
			}
		})
	}
}

func TestProblemsCountPhrase(t *testing.T) {
	for _, tc := range []struct {
		name  string
		total problemsTotal
		shown int
		want  string
	}{
		{"bilinen toplam", problemsTotal{n: 47, known: true}, 10, "toplam 47"},
		{"kırpma yok", problemsTotal{n: 3, known: true}, 3, "toplam 3"},
		{"sıfır", problemsTotal{n: 0, known: true}, 0, "toplam 0"},
		// Bilinmiyorsa KESİN bir sayı iddia edilmemeli.
		{"bilinmiyor", problemsTotal{}, 10, "en az 10"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := problemsCountPhraseTR(tc.total, tc.shown); got != tc.want {
				t.Errorf("= %q; want %q", got, tc.want)
			}
		})
	}
}

// TestBreakdownCaveatOnlyWhenTruncated — gürültü disiplini.
//
// Her başlıkta duran bir açıklama, hiçbir başlıkta okunmayan bir
// açıklamadır. Ama kırpma varken "toplam 47 (kritik 1, warning 1,
// info 0)" kendi içinde çelişir (1+1+0 ≠ 47) ve model bu çelişkiyi ya
// görmezden gelir ya da yanlış çözer.
func TestBreakdownCaveatOnlyWhenTruncated(t *testing.T) {
	if got := problemsBreakdownCaveatTR(problemsTotal{n: 3, known: true}, 3); got != "" {
		t.Errorf("kırpma yokken açıklama çıktı: %q", got)
	}
	if got := problemsBreakdownCaveatTR(problemsTotal{n: 47, known: true}, 10); got == "" {
		t.Error("kırpma varken açıklama YOK — başlık kendi içinde çelişir")
	}
	if got := problemsBreakdownCaveatTR(problemsTotal{}, 10); got == "" {
		t.Error("toplam bilinmiyorken açıklama YOK")
	}
}

// TestTruncationNoteReachesTheModel — ESKİ KUSURUN TA KENDİSİ.
//
// Eski ifşa dalı `i >= guidedMaxLines` idi (guidedMaxLines = 10) ve
// limit=10 rotalarında YAPISAL OLARAK ULAŞILAMAZDI: liste en fazla 10
// eleman, döngü 0..9, indeks hiç 10'a çıkmaz. Yani model yanlış toplamı
// üstüne hiçbir uyarı almadan alıyordu.
func TestTruncationNoteReachesTheModel(t *testing.T) {
	// 47 problem var, 10 gösteriliyor — İFŞA ŞART.
	note := problemsTruncationNoteTR(problemsTotal{n: 47, known: true}, 10, 10)
	if note == "" {
		t.Fatal("47/10 kırpmasında ifşa YOK — eski yapısal-ulaşılamaz dalın regresyonu")
	}
	if !strings.Contains(note, "47") {
		t.Errorf("ifşa gerçek toplamı taşımıyor: %q", note)
	}

	// Kırpma yok → ilan edilecek bir şey de yok.
	if got := problemsTruncationNoteTR(problemsTotal{n: 3, known: true}, 3, 3); got != "" {
		t.Errorf("kırpma yokken ifşa çıktı: %q", got)
	}

	// Toplam bilinmiyor + liste tavana dayanmış → sustuğunu söyle.
	if got := problemsTruncationNoteTR(problemsTotal{}, 10, 10); got == "" {
		t.Error("toplam ölçülemediğinde de sessiz kalınmış — liste EKSİK olabilir ve model bunu bilmiyor")
	}
}

// TestHeaderCarriesTheRealTotal — uçtan uca cümle.
//
// Kusur bir CÜMLE kusuruydu; düzeltmenin de cümle düzeyinde
// doğrulanması gerek.
func TestHeaderCarriesTheRealTotal(t *testing.T) {
	now := time.Now()
	probs := make([]chstore.Problem, 10)
	for i := range probs {
		probs[i] = chstore.Problem{
			ID: "p", RuleName: "r", Severity: "critical",
			Service: "checkout-service", StartedAt: now.Add(-time.Hour).UnixNano(),
			Priority: "P1",
		}
	}
	out := renderProblemsEvidenceTR(probs, "checkout-service", "", now,
		problemsTotal{n: 47, known: true})

	if !strings.Contains(out, "toplam 47") {
		t.Errorf("başlık gerçek toplamı taşımıyor:\n%s", out)
	}
	// ⚠ Eski davranışın pini: "toplam 10" bir daha ASLA çıkmamalı.
	if strings.Contains(out, "toplam 10") {
		t.Errorf("başlık hâlâ len(probs)'u toplam sanıyor:\n%s", out)
	}
	if !strings.Contains(out, "dağılım yalnız gösterilen") {
		t.Errorf("şiddet dağılımının kısmi olduğu söylenmiyor — 1+1+0 ≠ 47 çelişkisi:\n%s", out)
	}
	if !strings.Contains(out, "10 satır gösteriliyor") {
		t.Errorf("kırpma ifşası yok:\n%s", out)
	}
}

// TestUnknownTotalNeverClaimsOne — Services dilimi yolu.
//
// O yolda CountProblems kapsamı yeniden kuramıyor. Metin kesin bir
// toplam iddia ETMEMELİ; "en az N" demeli.
func TestUnknownTotalNeverClaimsOne(t *testing.T) {
	now := time.Now()
	probs := []chstore.Problem{{ID: "p1", RuleName: "r", Severity: "warning", Service: "a", StartedAt: now.UnixNano()}}
	out := renderProblemsEvidenceTR(probs, "", "", now, problemsTotal{})
	// ⚠ "toplam" KELİMESİNİ aramak yanlış olur: dürüst ifşanın kendisi
	// ("toplam sayı bu kapsamda ölçülemedi") o kelimeyi taşıyor. Yasak
	// olan şey bir SAYI İDDİASI, kelimenin kendisi değil. İlk yazımda
	// kelimeyi aradım ve test kırmızı oldu — kod doğruydu, iddia yanlıştı.
	if strings.Contains(out, "toplam 1 ") || strings.Contains(out, ": toplam") {
		t.Errorf("toplam ölçülemezken kesin sayı iddia edildi:\n%s", out)
	}
	if !strings.Contains(out, "en az 1") {
		t.Errorf("belirsizlik söylenmiyor:\n%s", out)
	}
}
