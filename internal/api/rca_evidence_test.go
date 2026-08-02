// v0.9.559 — RCA kanıt kataloğu testleri.
//
// Korunan sözleşme tek cümle: BULUNAMAYAN bir sinyal, bir kök nedenin
// kanıtı olamaz.
//
// Bu dosyanın varlık sebebi, gelecekte birinin "Checked listesini
// olduğu gibi kataloğa al" diye sadeleştirmesini engellemek. O
// sadeleştirme derlenir, testsiz geçer ve modelin "pod restart
// döngüsünde — kanıt: pod restart kaydı BULUNAMADI" demesine izin
// verir. Daha kötüsü: kalkan geçtiği için sunucu o negatif metni
// iddianın yanına basar ve uydurma DOĞRULANMIŞ görünür.
package api

import (
	"strings"
	"testing"

	"github.com/cilcenk/coremetry/internal/chstore"
)

func TestBuildRCAEvidenceCatalogSeparatesNegatives(t *testing.T) {
	h := &chstore.RootCauseHypothesis{
		Service:    "odeme-api",
		TopSuspect: "odeme-db",
		TopScore:   8.4,
		Confidence: 0.72,
		Candidates: []chstore.ScoredCause{
			{Service: "odeme-db", Score: 8.4},
			{Service: "kimlik-api", Score: 3.1, Hops: 2, Reason: "gecikme artışı"},
		},
		Deep: &chstore.DeepEvidence{
			Checked: []chstore.CheckedSignal{
				{Family: "log", Found: true, Records: 1240, Detail: "connection pool exhausted"},
				{Family: "pod", Found: false, Detail: "restart kaydı bulunamadı"},
				{Family: "heap", Found: false, Detail: "JVM metriği akmıyor"},
			},
		},
	}

	cat := buildRCAEvidenceCatalog(h)

	// Pozitifler: baş şüpheli + 1 diğer aday + 1 bulunan sinyal.
	if got := len(cat.positiveIDs()); got != 3 {
		t.Errorf("pozitif kanıt sayısı %d, beklenen 3 — %v", got, cat.positiveIDs())
	}
	// Negatifler: iki bulunamayan sinyal.
	negs := cat.negativeIDs()
	if len(negs) != 2 {
		t.Fatalf("negatif kanıt sayısı %d, beklenen 2 — %v", len(negs), negs)
	}

	// ASIL İDDİA: bulunamayan sinyaller N uzayında, E'de DEĞİL.
	for _, id := range negs {
		if !strings.HasPrefix(id, "N") {
			t.Errorf("bulunamayan sinyal %q E uzayında — kök nedene dayanak yapılabilir hâle gelir", id)
		}
		ref, ok := cat.lookup(id)
		if !ok {
			t.Fatalf("%s katalogda bulunamadı", id)
		}
		if ref.Kind != rcaNegative {
			t.Errorf("%s kind=%q, beklenen N", id, ref.Kind)
		}
	}

	// Negatif kanıt varlık beyaz listesini BESLEMEMELİ.
	for _, e := range rcaAllowedEntities(cat) {
		if e == "" {
			t.Error("beyaz listeye boş varlık girmiş")
		}
	}
}

func TestNegativeEvidenceDoesNotWidenEntityWhitelist(t *testing.T) {
	// Yalnız bulunamayan sinyalleri olan bir hipotez: beyaz liste
	// SADECE ankor servisini içermeli. Aksi hâlde K3 kapısı, N
	// uzayının arka kapısına dönerdi.
	h := &chstore.RootCauseHypothesis{
		Service: "odeme-api",
		Deep: &chstore.DeepEvidence{
			Checked: []chstore.CheckedSignal{
				{Family: "pod", Found: false, Detail: "yok"},
			},
		},
	}
	cat := buildRCAEvidenceCatalog(h)
	got := rcaAllowedEntities(cat)
	if len(got) != 1 || got[0] != "odeme-api" {
		t.Errorf("beyaz liste %v, beklenen yalnız [odeme-api]", got)
	}
}

func TestRCACatalogIDsAreDeterministic(t *testing.T) {
	// Aynı hipotez her çağrıda AYNI kimlikleri üretmeli. Kaymış bir
	// kimlik, önbelleğe alınmış bir verdict'in sessizce BAŞKA bir
	// kanıta atıf yapması demektir — ve bunu hiçbir kalkan yakalayamaz,
	// çünkü kimlik hâlâ geçerlidir.
	h := &chstore.RootCauseHypothesis{
		Service:    "a",
		TopSuspect: "b",
		Candidates: []chstore.ScoredCause{{Service: "b"}, {Service: "c"}, {Service: "d"}},
		Deep: &chstore.DeepEvidence{Checked: []chstore.CheckedSignal{
			{Family: "log", Found: true}, {Family: "pod", Found: false},
		}},
	}
	first := buildRCAEvidenceCatalog(h)
	for i := 0; i < 5; i++ {
		again := buildRCAEvidenceCatalog(h)
		if len(again.Refs) != len(first.Refs) {
			t.Fatalf("katalog uzunluğu değişti: %d vs %d", len(again.Refs), len(first.Refs))
		}
		for j := range first.Refs {
			if again.Refs[j].ID != first.Refs[j].ID || again.Refs[j].Text != first.Refs[j].Text {
				t.Fatalf("kimlik kaydı %d kaydı: %+v vs %+v", j, again.Refs[j], first.Refs[j])
			}
		}
	}
}

func TestRCACatalogNilHypothesis(t *testing.T) {
	cat := buildRCAEvidenceCatalog(nil)
	if len(cat.Refs) != 0 {
		t.Errorf("nil hipotezde %d kanıt üretilmiş", len(cat.Refs))
	}
	if _, ok := cat.lookup("E1"); ok {
		t.Error("boş katalogda E1 bulundu")
	}
}

func TestRenderCatalogStatesNegativeConstraint(t *testing.T) {
	// Kısıt VERİNİN YANINDA yazılı olmalı. Küçük modelde kuralı uzakta
	// bir yerde bir kez söylemek yetmiyor.
	h := &chstore.RootCauseHypothesis{
		Service: "a",
		Deep: &chstore.DeepEvidence{Checked: []chstore.CheckedSignal{
			{Family: "pod", Found: false, Detail: "yok"},
		}},
	}
	out := renderRCAEvidenceCatalog(buildRCAEvidenceCatalog(h))
	for _, want := range []string{"ÇÜRÜTMEK", "ASLA kök nedenin kanıtı DEĞİLDİR"} {
		if !strings.Contains(out, want) {
			t.Errorf("katalog metni %q kısıtını taşımıyor:\n%s", want, out)
		}
	}
}

func TestRenderCatalogOmitsNegativeSectionWhenEmpty(t *testing.T) {
	// Negatif kanıt yoksa bölüm hiç basılmamalı — boş bir "BULUNAMAYAN"
	// başlığı küçük modelde var olmayan bir kısıt kategorisi uydurmaya
	// davet eder.
	h := &chstore.RootCauseHypothesis{Service: "a", TopSuspect: "b"}
	out := renderRCAEvidenceCatalog(buildRCAEvidenceCatalog(h))
	if strings.Contains(out, "BULUNAMAYAN") {
		t.Errorf("negatif kanıt yokken bölüm basılmış:\n%s", out)
	}
}
