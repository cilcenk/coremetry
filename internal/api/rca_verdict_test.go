// v0.9.559 — RCA verdict orkestrasyon testleri.
//
// Kalkanları tek tek test etmek yetmez: asıl soru, GERÇEK akışta
// ateşleyip ateşlemedikleri. Bu dosya modelin üretebileceği somut
// saldırıları uçtan uca verdict'ten geçiriyor.
package api

import (
	"strings"
	"testing"
	"time"

	"github.com/cilcenk/coremetry/internal/chstore"
)

func verdictFixture() (*chstore.RootCauseHypothesis, rcaEvidenceCatalog) {
	h := &chstore.RootCauseHypothesis{
		AnchorKind: "problem", AnchorID: "p-1",
		Service:    "odeme-api",
		TopSuspect: "odeme-db",
		TopScore:   8.4,
		Confidence: 0.7,
		Candidates: []chstore.ScoredCause{
			{Service: "odeme-db", Score: 8.4},
			{Service: "kimlik-api", Score: 3.1},
		},
		Deep: &chstore.DeepEvidence{Checked: []chstore.CheckedSignal{
			{Family: "log", Found: true, Records: 12, Detail: "pool exhausted"},
			{Family: "pod", Found: false, Detail: "restart kaydı yok"},
		}},
	}
	return h, buildRCAEvidenceCatalog(h)
}

// SALDIRI: negatif kanıtla kök neden iddia etmek.
func TestVerdictDropsNegativeEvidenceSupport(t *testing.T) {
	h, cat := verdictFixture()
	neg := cat.NegativeIDs()[0]

	mv := rcaModelVerdict{
		Verdict: "root_cause_identified",
		Title:   "pod restart döngüsü",
		Summary: "odeme-db pod'ları restart döngüsünde.",
		RootCause: rcaModelRootCause{
			Entity: "odeme-db", FailureMode: "restart döngüsü",
			Evidence: []string{neg}, // BULUNAMAYAN sinyali kanıt gösteriyor
		},
		ModelConfidence: 0.95,
	}
	sh := rcaShieldReport{Parsed: true}
	out := applyRCAShieldsPure(h, cat, mv, &sh)

	if len(out.RootCause.Evidence) != 0 {
		t.Errorf("negatif kanıt destek olarak KALDI: %v", out.RootCause.Evidence)
	}
	if out.Verdict == "root_cause_identified" {
		t.Error("kanıtı düşen iddia hâlâ 'kök neden bulundu' diyor")
	}
	if len(sh.RejectedEvidence) == 0 {
		t.Error("reddedilen kanıt raporlanmadı — operatör neye rağmen üretildiğini göremez")
	}
	// Ekranda gösterilecek kanıt listesi de boş olmalı: sunucu
	// negatif metni iddianın yanına BASMAMALI.
	if len(out.Evidence) != 0 {
		t.Errorf("reddedilen kanıt yine de ekrana taşınmış: %+v", out.Evidence)
	}
}

// SALDIRI: aynı kanıtla sahte eleme yazıp güven tavanını atlamak.
func TestVerdictRejectsSelfRefutation(t *testing.T) {
	h, cat := verdictFixture()
	pos := cat.PositiveIDs()[0]

	mv := rcaModelVerdict{
		Verdict:   "root_cause_identified",
		Summary:   "odeme-db bağlantı havuzu tükendi.",
		RootCause: rcaModelRootCause{Entity: "odeme-db", Evidence: []string{pos}},
		RejectedHypotheses: []rcaModelRejected{{
			Hypothesis: "yük artışı (trafik/istek hacmi)",
			RefutedBy:  []string{pos}, // DESTEK kanıtının aynısı
			Reason:     "aynı kanıt bunu da çürütüyor",
		}},
		ModelConfidence: 0.95,
	}
	sh := rcaShieldReport{Parsed: true}
	out := applyRCAShieldsPure(h, cat, mv, &sh)

	if !sh.RefutationInvalid {
		t.Error("kendi kanıtıyla eleme geçerli sayıldı — tek kimlikle tavan atlanır")
	}
	if out.Confidence > rcaNoRefutationCap {
		t.Errorf("güven %v, eleme geçersizken %v tavanını aşamaz",
			out.Confidence, rcaNoRefutationCap)
	}
}

// Geçerli eleme tavanı kaldırmalı — kalkan doğru davranışı da ödüllendirmeli.
func TestVerdictValidRefutationLiftsCap(t *testing.T) {
	h, cat := verdictFixture()
	ids := cat.PositiveIDs()
	neg := cat.NegativeIDs()[0]

	mv := rcaModelVerdict{
		Verdict:   "root_cause_identified",
		Summary:   "odeme-db bağlantı havuzu tükendi.",
		RootCause: rcaModelRootCause{Entity: "odeme-db", Evidence: []string{ids[0]}},
		RejectedHypotheses: []rcaModelRejected{{
			Hypothesis: "yük artışı (trafik/istek hacmi)",
			RefutedBy:  []string{neg}, // yokluk, çürütmede GEÇERLİ
			Reason:     "pod restart kaydı yok, yeniden başlatma kaynaklı değil",
		}},
		ModelConfidence: 0.75,
	}
	sh := rcaShieldReport{Parsed: true}
	out := applyRCAShieldsPure(h, cat, mv, &sh)

	if sh.RefutationInvalid {
		t.Error("geçerli eleme geçersiz sayıldı")
	}
	// hipotez güveni 0.7 + pay 0.1 = 0.8 → model'in 0.75'i geçer.
	if out.Confidence != 0.75 {
		t.Errorf("güven %v, beklenen 0.75 (tavanların altında)", out.Confidence)
	}
}

// SALDIRI: enum'dan geçen varlık + serbest metinde uydurma servis.
func TestVerdictScansFreeTextEntities(t *testing.T) {
	h, cat := verdictFixture()
	pos := cat.PositiveIDs()[0]

	mv := rcaModelVerdict{
		Verdict:   "probable_cause",
		Summary:   "odeme-db havuzu tükendi.",
		RootCause: rcaModelRootCause{Entity: "odeme-db", Evidence: []string{pos}},
		CausalChain: []rcaModelChainStep{
			{Entity: "odeme-db", Effect: "oracle-rac-cluster node eviction sonrası havuz doldu"},
		},
		Remediation: []rcaModelRemediation{
			{Kind: "mitigate", Action: "oracle-rac-cluster node 2'yi yeniden başlat",
				Target: "oracle-rac-cluster", Risk: "medium"},
		},
		ModelConfidence: 0.6,
	}
	sh := rcaShieldReport{Parsed: true}
	applyRCAShieldsPure(h, cat, mv, &sh)

	if len(sh.UnknownEntities) == 0 {
		t.Fatal("zincirde ve aksiyonda geçen uydurma servis yakalanmadı — " +
			"operatör somut bir restart aksiyonu görürdü")
	}
	found := false
	for _, u := range sh.UnknownEntities {
		if u == "oracle-rac-cluster" {
			found = true
		}
	}
	if !found {
		t.Errorf("beklenen uydurma ad yok: %v", sh.UnknownEntities)
	}
}

// SALDIRI: beyaz listede olmayan varlığı kök neden yapmak (şema
// enum'u düşmüşse mümkün).
func TestVerdictRejectsUnknownRootEntity(t *testing.T) {
	h, cat := verdictFixture()
	mv := rcaModelVerdict{
		Verdict:         "root_cause_identified",
		Summary:         "bilinmeyen servis çöktü.",
		RootCause:       rcaModelRootCause{Entity: "hayalet-servis"},
		ModelConfidence: 0.9,
	}
	sh := rcaShieldReport{Parsed: true}
	out := applyRCAShieldsPure(h, cat, mv, &sh)

	if out.Verdict != "insufficient_evidence" {
		t.Errorf("kataloğda olmayan varlık kök neden kaldı, verdict=%q", out.Verdict)
	}
	if out.RootCause.Entity != "" {
		t.Errorf("uydurma varlık temizlenmedi: %q", out.RootCause.Entity)
	}
}

// Üç güven ayrı ayrı taşınmalı — aynı ekranda buluşuyorlar.
func TestVerdictCarriesThreeDistinctConfidences(t *testing.T) {
	h, cat := verdictFixture()
	mv := rcaModelVerdict{
		Verdict:         "probable_cause",
		Summary:         "x",
		RootCause:       rcaModelRootCause{Entity: "odeme-db"},
		ModelConfidence: 0.95,
	}
	sh := rcaShieldReport{Parsed: true}
	out := applyRCAShieldsPure(h, cat, mv, &sh)

	if out.ModelConfidence != 0.95 {
		t.Errorf("modelin beyanı kayboldu: %v", out.ModelConfidence)
	}
	if out.HypothesisConfidence != 0.7 {
		t.Errorf("deterministik güven kayboldu: %v", out.HypothesisConfidence)
	}
	if out.Confidence >= out.ModelConfidence {
		t.Errorf("nihai güven (%v) tavanlanmamış — eleme yokken 0.6 olmalıydı",
			out.Confidence)
	}
}

// Düşüş yolu: prose nil KALMALI, yoksa frontend'in dürüst-boş dalı ölür.
func TestFallbackVerdictLeavesProseEmpty(t *testing.T) {
	h, cat := verdictFixture()
	sh := rcaShieldReport{}
	out := fallbackRCAVerdict(h, cat, &sh)

	if out.Verdict != "insufficient_evidence" {
		t.Errorf("düşüş verdict'i %q", out.Verdict)
	}
	if sh.Parsed {
		t.Error("çözümlenmediği hâlde parsed=true — UI sahte 'kanıt yetersiz'i " +
			"gerçeğinden ayırt edemez")
	}
	if !strings.Contains(out.Summary, h.TopSuspect) {
		t.Errorf("düşüş özeti deterministik şüpheliyi taşımıyor: %q", out.Summary)
	}
	if out.Confidence != 0 {
		t.Errorf("düşüşte güven %v, 0 olmalı", out.Confidence)
	}
}

func TestProseFromEmptyIsNil(t *testing.T) {
	if proseFrom("   ") != nil {
		t.Error("boş özet prose'a yazıldı — dürüst-boş dalı ölür")
	}
	if p := proseFrom(" merhaba "); p == nil || *p != "merhaba" {
		t.Errorf("özet kırpılmadı: %v", p)
	}
}

func TestSalvageJSONObject(t *testing.T) {
	cases := []struct {
		in   string
		want string
		ok   bool
	}{
		{"```json\n{\"a\":1}\n```", `{"a":1}`, true},
		{"İşte sonuç: {\"a\":1} umarım yardımcı olur", `{"a":1}`, true},
		{`{"a":1}`, `{"a":1}`, true},
		{"hiç json yok", "", false},
		{"}{", "", false},
	}
	for _, c := range cases {
		got, ok := salvageJSONObject(c.in)
		if ok != c.ok || got != c.want {
			t.Errorf("salvageJSONObject(%q) = (%q,%v), beklenen (%q,%v)",
				c.in, got, ok, c.want, c.ok)
		}
	}
}

// Prompt, rakip listesini ve negatif kanıt kısıtını TAŞIMALI.
func TestVerdictPromptCarriesConstraints(t *testing.T) {
	h, cat := verdictFixture()
	rivals := buildRCARivalOptions(cat, h.TopSuspect, []string{"kimlik-api"})
	p := buildRCAVerdictPrompt(h, cat, rivals, nil, time.Unix(1_760_000_000, 0))

	for _, want := range []string{
		"RAKİP HİPOTEZLER",
		"kendi rakibini uydurma",
		"ASLA kök nedenin kanıtı DEĞİLDİR",
	} {
		if !strings.Contains(p, want) {
			t.Errorf("prompt %q kısıtını taşımıyor", want)
		}
	}
}

// v0.9.577 — "çözümlendi" ŞEMAYA UYMAYA bağlı, yalnız geçerli JSON'a değil.
//
// Model `{}` döndürürse json.Unmarshal BAŞARILI olur ve eski kod
// sh.Parsed=true yazıyordu; mv.Verdict boş kalıyor, panel boş rozetle
// çiziliyor ve shields.parsed=true "model cevap verdi" diye iddia
// ediyordu. Tam da bu tasarımın engellemek için kurulduğu şey: cevap
// VERİLMEMESİNİ cevap gibi göstermek.
func TestRCAVerdictEnumOK(t *testing.T) {
	for _, ok := range []string{"root_cause_identified", "probable_cause", "insufficient_evidence"} {
		if !rcaVerdictEnumOK(ok) {
			t.Errorf("%q geçerli sayılmadı", ok)
		}
	}
	for _, bad := range []string{"", "{}", "kok_neden", "ROOT_CAUSE_IDENTIFIED", "probable"} {
		if rcaVerdictEnumOK(bad) {
			t.Errorf("%q geçerli sayıldı — boş/geçersiz verdict panelde BOŞ rozet "+
				"çizer ve parsed=true 'model cevap verdi' diye yalan söyler", bad)
		}
	}
}

// Düşüş yolunda kanıt listesi BOŞ kalmalı: alanın sözleşmesi "modelin
// ATIF YAPTIĞI kanıtlar" ve model hiçbir şeye atıf yapmamıştır.
func TestFallbackVerdictCitesNoEvidence(t *testing.T) {
	h, cat := verdictFixture()
	if len(cat.Refs) == 0 {
		t.Fatal("test kataloğu boş — vaka anlamsız")
	}
	sh := rcaShieldReport{}
	out := fallbackRCAVerdict(h, cat, &sh)

	if len(out.Evidence) != 0 {
		t.Errorf("düşüşte %d kanıt basılmış — panel \"Model şu kanıtları gösterdi\" "+
			"başlığıyla çizer ve modelin SÖYLEMEDİĞİ bir şeyi ona söyletir",
			len(out.Evidence))
	}
}
