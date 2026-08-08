// metric_exclusions_test.go — v0.9.797 pipeline köprüsünün SAF
// çekirdekleri. Canlı CH / Redis gerekmez.
//
// Yollar JENERİK (`/health/checkStartup`, `/api/orders`) —
// no_customer_identifiers_test'in koruduğu kural testlerde de geçerli.
package api

import (
	"strings"
	"testing"

	"github.com/cilcenk/coremetry/internal/chstore"
	"github.com/cilcenk/coremetry/internal/pipeline"
)

// ÇİFT MOTOR YASAK (v0.9.797 mimari kararı): dropAtIngest kendi drop
// kodunu yazmıyor, mevcut pipeline motoruna KÖPRÜLENİYOR. Bu test
// türetilen kuralın şeklini çiviliyor — şekil kayarsa ya yanlış
// datapoint düşer ya da hiçbiri düşmez, ikisi de sessiz.
func TestDeriveExclusionRuleShape(t *testing.T) {
	r := deriveExclusionRule(chstore.MetricExclusionRule{
		Metric: "http.server.duration", Pattern: "^/health", DropAtIngest: true,
	})
	if r.Kind != pipeline.KindDrop || r.Signal != pipeline.SignalMetrics || !r.Enabled {
		t.Errorf("kural şekli: kind=%q signal=%q enabled=%v", r.Kind, r.Signal, r.Enabled)
	}
	if !strings.HasPrefix(r.ID, exclusionRuleIDPrefix) {
		t.Errorf("kimlik öneki yok: %q — senkron bu kuralı SAHİPLENEMEZ", r.ID)
	}
	// when: route regex — okuma filtresiyle AYNI desen, AYNI operatör.
	if r.When.Key != chstore.MetricExclusionAttrKey || r.When.Op != pipeline.OpMatches || r.When.Value != "^/health" {
		t.Errorf("when koşulu: %+v", r.When)
	}
	// and: metrik eşitliği — bu koşul DÜŞERSE tek bir metrik için kurulan
	// dışlama BÜTÜN metriklerin o route'unu yazılmaz yapar.
	if len(r.And) != 1 || r.And[0].Key != "metric" || r.And[0].Op != pipeline.OpEq ||
		r.And[0].Value != "http.server.duration" {
		t.Fatalf("metrik koşulu eksik/yanlış: %+v", r.And)
	}
}

// '*' kuralında metrik koşulu OLMAMALI — kasıt zaten "her metrik".
func TestDeriveExclusionRuleWildcardHasNoMetricCondition(t *testing.T) {
	r := deriveExclusionRule(chstore.MetricExclusionRule{
		Metric: chstore.MetricExclusionWildcard, Pattern: "/probe", DropAtIngest: true,
	})
	if len(r.And) != 0 {
		t.Errorf("'*' kuralına metrik koşulu eklendi: %+v", r.And)
	}
}

// Kimlik DETERMİNİSTİK olmalı: aynı kural iki kez kaydedilince ikinci bir
// ikiz DEĞİL aynı ikiz güncellenir. Farklı kurallar farklı kimlik alır,
// yoksa biri diğerini sessizce ezer.
func TestDeriveExclusionRuleIDIsDeterministicAndDistinct(t *testing.T) {
	a := chstore.MetricExclusionRule{Metric: "m1", Pattern: "^/health", DropAtIngest: true}
	if deriveExclusionRule(a).ID != deriveExclusionRule(a).ID {
		t.Error("aynı kural iki farklı kimlik üretti — her kayıtta yeni ikiz birikir")
	}
	diffPattern := a
	diffPattern.Pattern = "^/probe"
	diffMetric := a
	diffMetric.Metric = "m2"
	ids := map[string]bool{
		deriveExclusionRule(a).ID:           true,
		deriveExclusionRule(diffPattern).ID: true,
		deriveExclusionRule(diffMetric).ID:  true,
	}
	if len(ids) != 3 {
		t.Errorf("üç ayrı kural %d kimlik üretti — biri diğerini ezer", len(ids))
	}
}

// İstenen küme YALNIZ işaretli kuralları içerir: okuma-yalnız bir
// kuralın ingest'e sızması veri kaybı olurdu.
func TestDesiredExclusionRulesOnlyDropFlagged(t *testing.T) {
	cfg := chstore.MetricExclusions{Rules: []chstore.MetricExclusionRule{
		{Metric: "m1", Pattern: "^/health", DropAtIngest: true},
		{Metric: "m1", Pattern: "^/metrics"}, // okuma-yalnız
		{Metric: "*", Pattern: "/probe", DropAtIngest: true},
	}}
	want := desiredExclusionRules(cfg)
	if len(want) != 2 {
		t.Fatalf("%d ikiz üretildi, beklenen 2 (yalnız işaretliler)", len(want))
	}
	for _, r := range want {
		if r.When.Value == "^/metrics" {
			t.Error("işaretsiz kural ingest'e sızdı")
		}
	}
}

// Senkron YALNIZ kendi ikizlerini siler; operatörün elle yazdığı pipeline
// kurallarına DOKUNMAZ. Bu kapı olmasaydı bir dışlama kaydı, ilgisiz bir
// drop kuralını sessizce silebilirdi.
func TestStaleExclusionRuleIDsNeverTouchesForeignRules(t *testing.T) {
	keep := deriveExclusionRule(chstore.MetricExclusionRule{Metric: "m1", Pattern: "^/health", DropAtIngest: true})
	gone := deriveExclusionRule(chstore.MetricExclusionRule{Metric: "m1", Pattern: "^/old", DropAtIngest: true})
	existing := []pipeline.Rule{
		keep,
		gone,
		{ID: "rule-abc", Name: "operatörün kuralı", Kind: pipeline.KindDrop, Signal: pipeline.SignalMetrics},
	}
	want := map[string]pipeline.Rule{keep.ID: keep}

	stale := staleExclusionRuleIDs(existing, want)
	if len(stale) != 1 || stale[0] != gone.ID {
		t.Fatalf("silinecekler = %v, beklenen yalnız %q", stale, gone.ID)
	}
	for _, id := range stale {
		if id == "rule-abc" {
			t.Error("operatörün elle yazdığı kural silinecekler listesinde")
		}
	}
}

// Köprü ile okuma filtresi AYNI kümeyi seçmeli. Ayrışırlarsa operatör
// "grafikte yok ama depoda var" (ya da tersi) yaşar — v0.9.797'nin
// tamamen kaçınmak için kurduğu durum.
func TestBridgePatternMatchesReadFilterSemantics(t *testing.T) {
	rule := chstore.MetricExclusionRule{
		Metric: "http.server.duration", Pattern: "/health", DropAtIngest: true,
	}
	compiled, err := chstore.CompileMetricExclusions(chstore.MetricExclusions{
		Rules: []chstore.MetricExclusionRule{rule},
	})
	if err != nil {
		t.Fatal(err)
	}
	derived := deriveExclusionRule(rule)
	// Aynı desen, ve okuma tarafı da ANKORSUZ (CH match() gibi).
	if derived.When.Value != rule.Pattern {
		t.Errorf("köprü deseni değiştirdi: %q → %q", rule.Pattern, derived.When.Value)
	}
	// Ankorsuzluk kanıtı: ön ek olmayan bir konumda da eşleşir.
	if !compiled.DropAtIngest("http.server.duration", "/v1/health/checkStartup") {
		t.Error("okuma tarafı ankorsuz eşleşmedi — köprüyle ayrışır")
	}
}
