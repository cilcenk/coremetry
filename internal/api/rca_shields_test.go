// v0.9.559 — RCA kalkan testleri.
//
// Her test bir SALDIRIYI temsil ediyor: modelin kalkanı atlamak için
// yapabileceği somut hamle. Testler "fonksiyon çalışıyor mu" değil,
// "bu hamle geçer mi" sorusunu soruyor.
package api

import (
	"strings"
	"testing"

	"github.com/cilcenk/coremetry/internal/chstore"
)

func testCatalog() rcaEvidenceCatalog {
	return buildRCAEvidenceCatalog(&chstore.RootCauseHypothesis{
		Service:    "odeme-api",
		TopSuspect: "odeme-db",
		TopScore:   8.4,
		Confidence: 0.7,
		Candidates: []chstore.ScoredCause{
			{Service: "odeme-db", Score: 8.4},
			{Service: "kimlik-api", Score: 3.1},
		},
		Deep: &chstore.DeepEvidence{Checked: []chstore.CheckedSignal{
			{Family: "log", Found: true, Records: 12},
			{Family: "pod", Found: false, Detail: "restart kaydı yok"},
		}},
	})
}

// SALDIRI 1: negatif kanıtı kök nedene dayanak göstermek.
func TestShieldRejectsNegativeEvidenceAsSupport(t *testing.T) {
	cat := testCatalog()
	negs := cat.negativeIDs()
	if len(negs) == 0 {
		t.Fatal("test kataloğunda negatif kanıt yok")
	}
	var sh rcaShieldReport
	got := filterEvidenceIDs(cat, []string{negs[0]}, &sh)

	if len(got) != 0 {
		t.Errorf("negatif kanıt %q destek olarak KABUL EDİLDİ — 'pod restart "+
			"kaydı bulunamadı' satırı 'pod restart döngüsünde' iddiasının "+
			"kanıtı olurdu", negs[0])
	}
	if len(sh.RejectedEvidence) != 1 {
		t.Errorf("reddedilen kanıt kaydedilmemiş: %+v", sh)
	}
}

// Aynı negatif kanıt ÇÜRÜTMEDE geçerli olmalı — asimetri kasıtlı.
func TestShieldAcceptsNegativeEvidenceAsRefutation(t *testing.T) {
	cat := testCatalog()
	negs := cat.negativeIDs()
	var sh rcaShieldReport
	got := filterRefutationIDs(cat, negs[:1], &sh)
	if len(got) != 1 {
		t.Errorf("negatif kanıt çürütmede reddedildi — bir hipotezi çürütmenin "+
			"dayanağı pekâlâ bir YOKLUK olabilir; got=%v sh=%+v", got, sh)
	}
}

// SALDIRI 2: hiç var olmayan bir kanıt kimliği uydurmak.
func TestShieldRejectsInventedEvidenceID(t *testing.T) {
	cat := testCatalog()
	var sh rcaShieldReport
	got := filterEvidenceIDs(cat, []string{"E99", "N42"}, &sh)
	if len(got) != 0 {
		t.Errorf("uydurma kimlikler kabul edildi: %v", got)
	}
	if len(sh.RejectedEvidence) != 2 {
		t.Errorf("iki uydurma kimlik beklenirken %v", sh.RejectedEvidence)
	}
}

// SALDIRI 3: aynı kanıtı hem destek hem çürütme göstererek tavanı atlamak.
//
// Bu, birinci tasarımın en ciddi açığıydı: tek gerçek kimlikle sahte
// bir eleme yazmak, en yüksek verdict'i almaya yetiyordu.
func TestRefutationRejectsOverlappingEvidence(t *testing.T) {
	if refutationValid([]string{"E1", "E2"}, []string{"E1"}) {
		t.Error("destek ve çürütme AYNI kanıta dayanıyor ama eleme geçerli " +
			"sayıldı — aynı kanıt hem destek hem çürütme olamaz")
	}
	if !refutationValid([]string{"E1"}, []string{"E2", "N1"}) {
		t.Error("kesişmeyen geçerli eleme reddedildi")
	}
	if refutationValid([]string{"E1"}, nil) {
		t.Error("çürütme kanıtı YOKken eleme geçerli sayıldı")
	}
}

func TestCapRCAConfidence(t *testing.T) {
	cases := []struct {
		name       string
		model      float64
		hypothesis float64
		refuted    bool
		want       float64
	}{
		{"eleme yok → 0.6 tavanı", 0.95, 0.9, false, 0.6},
		{"eleme var ama hipotez düşük → hipotez+pay", 0.95, 0.5, true, 0.6},
		{"eleme var, hipotez yüksek → model geçer", 0.8, 0.9, true, 0.8},
		{"model zaten düşük → dokunma", 0.3, 0.9, true, 0.3},
		{"aralık dışı yüksek → 1'e kıstır sonra tavan", 1.7, 0.95, true, 1.0},
		{"negatif → 0", -0.4, 0.9, true, 0},
		// Deterministik motor hiç güven vermemişse (0), model de
		// neredeyse hiçbir şey iddia edememeli.
		{"hipotez güveni 0 → pay kadar", 0.9, 0, true, 0.1},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var sh rcaShieldReport
			got := capRCAConfidence(c.model, c.hypothesis, c.refuted, &sh)
			if got != c.want {
				t.Errorf("capRCAConfidence(%v,%v,%v) = %v, beklenen %v",
					c.model, c.hypothesis, c.refuted, got, c.want)
			}
		})
	}
}

// NaN savunması: güven doğrudan model JSON'undan geliyor.
func TestCapRCAConfidenceHandlesNaN(t *testing.T) {
	var sh rcaShieldReport
	got := capRCAConfidence(math_NaN(), 0.9, true, &sh)
	if got != 0 {
		t.Errorf("NaN güven %v döndü, beklenen 0", got)
	}
	if len(sh.Notes) == 0 {
		t.Error("NaN sessizce yutuldu — not düşülmeli")
	}
}

func math_NaN() float64 { var z float64; return z / z }

// SALDIRI 4: enum'dan geçen gerçek bir varlık yazıp, SERBEST METİNDE
// var olmayan bir servisi anlatmak.
func TestCheckRCAEntitiesScansFreeText(t *testing.T) {
	cat := testCatalog()
	var sh rcaShieldReport
	checkRCAEntities(cat, []string{
		"Kök neden odeme-db üzerinde bağlantı havuzu tükenmesi.",
		"oracle-rac-cluster node eviction sonrası havuz doldu.", // UYDURMA
		"oracle-rac-cluster node 2'yi yeniden başlat",           // aksiyonda da
	}, &sh)

	if len(sh.UnknownEntities) == 0 {
		t.Fatal("serbest metindeki uydurma servis adı yakalanmadı — operatör " +
			"ekranda gerçek görünen bir zincir ve somut bir restart aksiyonu görürdü")
	}
	found := false
	for _, u := range sh.UnknownEntities {
		if u == "oracle-rac-cluster" {
			found = true
		}
	}
	if !found {
		t.Errorf("beklenen uydurma ad yakalanmadı: %v", sh.UnknownEntities)
	}
}

func TestCheckRCAEntitiesAllowsKnownNames(t *testing.T) {
	cat := testCatalog()
	var sh rcaShieldReport
	checkRCAEntities(cat, []string{
		"odeme-db yavaşladı, kimlik-api etkilendi, odeme-api hata verdi.",
		// Teknik terimler servis sanılmamalı.
		"error-rate yükseldi, p99-latency arttı, root-cause belirlendi.",
	}, &sh)
	if len(sh.UnknownEntities) != 0 {
		t.Errorf("bilinen adlar/teknik terimler uydurma sayıldı: %v", sh.UnknownEntities)
	}
}

func TestBuildRCARivalOptions(t *testing.T) {
	cat := testCatalog()
	opts := buildRCARivalOptions(cat, "odeme-db", []string{"odeme-db", "kimlik-api", ""})

	// Baş şüpheli DIŞARIDA: kendini elemek anlamsız.
	for _, o := range opts {
		if strings.Contains(o, "odeme-db") {
			t.Errorf("baş şüpheli rakip listesine girmiş: %q", o)
		}
	}
	// Diğer aday içeride.
	hasCandidate := false
	for _, o := range opts {
		if strings.Contains(o, "kimlik-api") {
			hasCandidate = true
		}
	}
	if !hasCandidate {
		t.Errorf("diğer aday rakip listesinde yok: %v", opts)
	}
	// Sabit sınıflar her zaman var — hipotezde hiç aday olmasa bile
	// modelin seçebileceği gerçek bir alternatif kalmalı.
	if len(opts) < len(rcaRivalClasses) {
		t.Errorf("sabit rakip sınıfları eksik: %v", opts)
	}
}
