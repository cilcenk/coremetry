package api

// v0.9.1203 (Faz 6.1) — katalog genişlemesinin pinleri. Korunanlar:
// (a) KİMLİK KARARLILIĞI: taban ailelerin E/N kimlikleri extras'la
//     bayt-bayt aynı kalır (10 dk cache'li verdict'in sözleşmesi),
// (b) NEDENSELLİK AYRIMI: correlations komşusu beyaz listeye GİRER
//     (olası neden), blast çağıranı ve BubbleUp değeri GİRMEZ (mağdur/
//     boyut) — girseydi şemanın root_cause.entity enum'u mağdurları
//     kök neden ilan etmeye davet ederdi,
// (c) K3 gösterilen-jeton: katalogda bastığımız tireli değeri modelin
//     alıntılaması artık uydurma sayılmaz; gerçekten uydurulan ad
//     hâlâ yakalanır.

import (
	"strings"
	"testing"

	"github.com/cilcenk/coremetry/internal/chstore"
)

func extTestHypothesis() *chstore.RootCauseHypothesis {
	return &chstore.RootCauseHypothesis{
		AnchorKind: "problem", AnchorID: "p1", Service: "checkout",
		TopSuspect: "payment-db", TopScore: 3.2, Confidence: 0.8,
		Candidates: []chstore.ScoredCause{{Service: "payment-db", Score: 3.2}, {Service: "cache", Score: 1.1}},
		Deep: &chstore.DeepEvidence{Checked: []chstore.CheckedSignal{
			{Family: "exceptions", Found: true, Records: 12},
			{Family: "saturation", Found: false},
		}},
	}
}

func extTestExtras() rcaCatalogExtras {
	return rcaCatalogExtras{
		Blast: &chstore.BlastRadius{
			Service: "checkout", TotalCallers: 7, CascadingCallers: 2,
			Callers: []chstore.BlastRadiusCaller{
				{Service: "mobile-bff", ErrorRate: 12.5, HasOpenProblem: true},
				{Service: "web-gw", ErrorRate: 3.0},
			},
		},
		Correlations: []chstore.ChangedService{
			{Service: "checkout", P99DeltaPct: 90}, // ankorun kendisi — elenmeli
			{Service: "payment-db", P99DeltaPct: 240, ErrDeltaPct: 4.2, RateDeltaPct: -3},
			{Service: "auth-svc", P99DeltaPct: 55, ErrDeltaPct: 0.4, RateDeltaPct: 1},
			{Service: "c3", P99DeltaPct: 30}, {Service: "c4", P99DeltaPct: 20}, // kap: 3'ten sonrası düşer
		},
		BubbleUp: &chstore.BubbleUpResult{
			SelectionTotal: 40, BaselineTotal: 900,
			Attributes: []chstore.BubbleUpAttribute{
				{Key: "http.route", Values: []chstore.BubbleUpValue{{Value: "/v1/pay-now", SelectionPct: 80, BaselinePct: 11, Score: 69}}},
				{Key: "pod", Values: []chstore.BubbleUpValue{{Value: "api-gw-7f", SelectionPct: 60, BaselinePct: 30, Score: 30}}},
			},
		},
	}
}

func TestCatalogExtBaseIDsStayStable(t *testing.T) {
	h := extTestHypothesis()
	base := buildRCAEvidenceCatalog(h)
	ext := buildRCAEvidenceCatalogExt(h, extTestExtras())
	if len(ext.Refs) <= len(base.Refs) {
		t.Fatalf("extras katalogu genişletmedi: %d <= %d", len(ext.Refs), len(base.Refs))
	}
	for i, r := range base.Refs {
		if ext.Refs[i] != r {
			t.Fatalf("taban kimlik kaydı %d değişti: %+v != %+v — 10dk cache'li verdict yanlış kanıta atıf yapar", i, ext.Refs[i], r)
		}
	}
	// Boş extras = bugüne kadarki katalog, bayt-bayt.
	empty := buildRCAEvidenceCatalogExt(h, rcaCatalogExtras{})
	if len(empty.Refs) != len(base.Refs) {
		t.Fatalf("boş extras katalog değiştirdi: %d != %d", len(empty.Refs), len(base.Refs))
	}
}

func TestCatalogExtCausalitySplit(t *testing.T) {
	ext := buildRCAEvidenceCatalogExt(extTestHypothesis(), extTestExtras())

	if !ext.Entities["payment-db"] || !ext.Entities["auth-svc"] {
		t.Errorf("kötüleşen komşular beyaz listeye girmeli (olası neden): %+v", ext.Entities)
	}
	for _, victim := range []string{"mobile-bff", "web-gw"} {
		if ext.Entities[victim] {
			t.Errorf("blast çağıranı %q beyaz listeye GİRMEMELİ — mağdur, neden değil", victim)
		}
	}
	if ext.Entities["/v1/pay-now"] || ext.Entities["api-gw-7f"] {
		t.Errorf("BubbleUp değerleri beyaz listeye girmemeli: %+v", ext.Entities)
	}

	all := renderRCAEvidenceCatalog(ext)
	for _, want := range []string{
		"etki alanı: 7 çağıran servis (2'sinde kaskad problem)",
		"mobile-bff (hata %12.5, açık problemi var)",
		"kök neden adayı değildir",
		"aynı pencerede kötüleşen komşu: payment-db (p99 Δ+240%, hata Δ+4.2%, istek Δ-3%)",
		"hatalarda ayrışan boyut: http.route=/v1/pay-now (hatalı kümede %80, tabanda %11)",
		"hatalarda ayrışan boyut: pod=api-gw-7f",
	} {
		if !strings.Contains(all, want) {
			t.Errorf("katalog %q içermeli:\n%s", want, all)
		}
	}
	// Ankor elenir ve kap 3: payment-db + auth-svc + c3 girer, c4 düşer.
	if strings.Contains(all, "komşu: c4") || strings.Contains(all, "komşu: checkout") {
		t.Errorf("kap (3) ya da ankor-eleme delindi:\n%s", all)
	}
}

func TestCatalogExtSkipsEmptyFamilies(t *testing.T) {
	h := extTestHypothesis()
	// Hata yoksa BubbleUp ailesi hiç girmez (yokluk kanıt değildir).
	noErr := extTestExtras()
	noErr.BubbleUp = &chstore.BubbleUpResult{SelectionTotal: 0, Attributes: []chstore.BubbleUpAttribute{
		{Key: "pod", Values: []chstore.BubbleUpValue{{Value: "x", Score: 10}}},
	}}
	out := renderRCAEvidenceCatalog(buildRCAEvidenceCatalogExt(h, noErr))
	if strings.Contains(out, "hatalarda ayrışan boyut") {
		t.Errorf("SelectionTotal=0 iken BubbleUp satırı basılmamalı:\n%s", out)
	}
	// Ayrışmayan (Score<=0) boyut da girmez.
	flat := extTestExtras()
	flat.BubbleUp.Attributes = []chstore.BubbleUpAttribute{
		{Key: "pod", Values: []chstore.BubbleUpValue{{Value: "x", SelectionPct: 10, BaselinePct: 10, Score: 0}}},
	}
	out = renderRCAEvidenceCatalog(buildRCAEvidenceCatalogExt(h, flat))
	if strings.Contains(out, "hatalarda ayrışan boyut") {
		t.Errorf("Score<=0 boyut kanıt değildir:\n%s", out)
	}
}

func TestK3AllowsShownCatalogTokens(t *testing.T) {
	ext := buildRCAEvidenceCatalogExt(extTestHypothesis(), extTestExtras())

	// Modelin, katalogda BİZİM bastığımız tireli değerleri alıntılaması
	// uydurma değildir (v0.9.598 sınıfı).
	var sh rcaShieldReport
	checkRCAEntities(ext, []string{"hatalar api-gw-7f podunda ve mobile-bff çağıranında yoğunlaşıyor"}, &sh)
	if len(sh.UnknownEntities) != 0 {
		t.Fatalf("gösterilen jetonlar uydurma sayıldı: %v", sh.UnknownEntities)
	}
	// Gerçekten uydurulan ad hâlâ yakalanır — kalkan zayıflamadı.
	var sh2 rcaShieldReport
	checkRCAEntities(ext, []string{"kök neden fraud-engine servisinde"}, &sh2)
	if len(sh2.UnknownEntities) == 0 {
		t.Fatal("uydurulan tireli ad yakalanmalıydı — K3 gevşemiş")
	}
}
