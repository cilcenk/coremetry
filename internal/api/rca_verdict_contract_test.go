// v0.9.589 — RCA verdict'inde ŞEMA ↔ AYRIŞTIRICI ↔ PROMPT sözleşmesi.
//
// Mevcut verdict testlerinin hepsi model çıktısını TAKLİT ediyor: elle
// yazılmış bir rcaModelVerdict kuruluyor ve kalkanlardan geçiriliyor.
// Kalkanlar böyle iyi test ediliyor ama bir katman tamamen dışarıda
// kalıyor — modele NE İSTEDİĞİMİZ ile ondan NE OKUDUĞUMUZ arasındaki
// bağ:
//
//	şema      → modele "şu alanları, şu enum'larla üret" der
//	struct    → gelen JSON'dan şu alanları okur
//	prompt    → modele enum'ları KELİMESİ KELİMESİNE öğretir
//
// Üçü ayrı dosyada ve hiçbir test onları karşılaştırmıyordu. Sessiz
// bozulma biçimleri:
//
//   - şemaya alan eklenir, struct'a eklenmez → model üretir, biz
//     okumayız; alan sessizce kaybolur
//   - struct'a alan eklenir, şemaya eklenmez → şema strict
//     (additionalProperties:false), model o alanı ASLA üretemez;
//     alan sonsuza dek boş kalır ve kimse fark etmez
//   - şemadaki enum yeniden adlandırılır, prompt eski adı öğretmeye
//     devam eder → model prompt'a uyar, şema reddeder, her verdict
//     düşüş yoluna gider ve dışarıdan "model zayıf" gibi görünür
//
// Üçü de derlenir, mevcut testlerden geçer ve YALNIZ canlı modelle
// ortaya çıkar. Bu dosya onları derleme zamanına yakın bir yere çeker.
package api

import (
	"encoding/json"
	"reflect"
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/cilcenk/coremetry/internal/copilot"
)

// schemaProps — bir objSchema'nın alan adları.
func schemaProps(t *testing.T, s map[string]any) []string {
	t.Helper()
	props, ok := s["properties"].(map[string]any)
	if !ok {
		t.Fatalf("şema properties taşımıyor: %v", s)
	}
	out := make([]string, 0, len(props))
	for k := range props {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// jsonTags — bir struct'ın okuduğu JSON alan adları.
func jsonTags(t *testing.T, v any) []string {
	t.Helper()
	rt := reflect.TypeOf(v)
	out := make([]string, 0, rt.NumField())
	for i := 0; i < rt.NumField(); i++ {
		tag := rt.Field(i).Tag.Get("json")
		if tag == "" || tag == "-" {
			continue
		}
		out = append(out, strings.Split(tag, ",")[0])
	}
	sort.Strings(out)
	return out
}

// TestVerdictSchemaMatchesParser — şema ile ayrıştırıcı alan alan aynı.
//
// Şema strict (additionalProperties:false, required=hepsi), yani iki
// taraf TAM eşleşmek zorunda: şemada olup struct'ta olmayan alan
// okunmaz, struct'ta olup şemada olmayan alan hiç gelmez.
func TestVerdictSchemaMatchesParser(t *testing.T) {
	root := rcaVerdictSchema([]string{"a-svc"}, []string{"r1"})
	props := root["properties"].(map[string]any)

	cases := []struct {
		name   string
		schema map[string]any
		target any
	}{
		{"verdict kökü", root, rcaModelVerdict{}},
		{"root_cause", props["root_cause"].(map[string]any), rcaModelRootCause{}},
		{"causal_chain[]", props["causal_chain"].(map[string]any)["items"].(map[string]any), rcaModelChainStep{}},
		{"rejected_hypotheses[]", props["rejected_hypotheses"].(map[string]any)["items"].(map[string]any), rcaModelRejected{}},
		{"remediation[]", props["remediation"].(map[string]any)["items"].(map[string]any), rcaModelRemediation{}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, want := jsonTags(t, c.target), schemaProps(t, c.schema)
			if !reflect.DeepEqual(got, want) {
				t.Errorf("şema ↔ ayrıştırıcı ayrışmış.\n  şema  : %v\n  struct: %v\n\n"+
					"Şema strict: additionalProperties=false ve required=hepsi. "+
					"Şemada olup struct'ta olmayan alan sessizce OKUNMAZ; struct'ta "+
					"olup şemada olmayan alan model tarafından ASLA üretilemez ve "+
					"sonsuza dek boş kalır. İkisi de derlenir ve yalnız canlı "+
					"modelle ortaya çıkar.", want, got)
			}
		})
	}
}

// mentionsToken — metin bu değeri TAM SÖZCÜK olarak içeriyor mu?
//
// strings.Contains YETMEZ ve bu testi yazarken bizzat kanıtlandı:
// şemadaki `probable_cause` değerini `probable_causeX` yapıp prompt'u
// bozdum, Contains yine eşleşti çünkü eski ad yenisinin ÖNEKİ. Tam da
// yakalaması gereken drift'i kaçırıyordu — yeniden adlandırmaların
// çoğu ek alarak yapılır.
func mentionsToken(text, token string) bool {
	return regexp.MustCompile(`\b` + regexp.QuoteMeta(token) + `\b`).MatchString(text)
}

// TestVerdictEnumsAreTaughtByThePrompt — prompt ile şema aynı kelimeleri
// kullanmalı.
//
// Şema modeli KISITLAR, prompt ona NE DEMEK olduklarını öğretir. Şemada
// bir enum yeniden adlandırılıp prompt güncellenmezse model prompt'a
// uyar, şema onu reddeder ve HER verdict düşüş yoluna gider — dışarıdan
// "model zayıf" gibi görünür, oysa iki dosya çelişiyordur.
func TestVerdictEnumsAreTaughtByThePrompt(t *testing.T) {
	prompt := copilot.SystemPromptRCAVerdict()
	root := rcaVerdictSchema(nil, nil)
	props := root["properties"].(map[string]any)

	verdicts := props["verdict"].(map[string]any)["enum"].([]string)
	if len(verdicts) == 0 {
		t.Fatal("verdict enum'u boş — şema okunamadı, test bayatladı")
	}
	for _, v := range verdicts {
		if !mentionsToken(prompt, v) {
			t.Errorf("şemadaki verdict değeri %q prompt'ta TAM SÖZCÜK olarak "+
				"geçmiyor — model bu kararı ne zaman vereceğini öğrenemez", v)
		}
	}

	// Ayrıştırıcının kabul kapısı da aynı üçlüyü tanımalı: enum kapısı
	// şemadan DAR olursa geçerli bir cevap düşüş yoluna gider, GENİŞ
	// olursa v0.9.577'nin boş-rozet hatası geri gelir.
	for _, v := range verdicts {
		if !rcaVerdictEnumOK(v) {
			t.Errorf("şema %q üretilmesine izin veriyor ama rcaVerdictEnumOK "+
				"reddediyor — geçerli cevap düşüş yoluna giderdi", v)
		}
	}
	for _, bad := range []string{"", "confirmed", "unknown", "ROOT_CAUSE_IDENTIFIED"} {
		if rcaVerdictEnumOK(bad) {
			t.Errorf("rcaVerdictEnumOK şema dışı %q değerini kabul etti — "+
				"v0.9.577'nin boş-rozet hatası geri gelir", bad)
		}
	}
}

// TestVerdictParsesASchemaShapedResponse — uçtan uca ayrıştırma.
//
// Şemaya BİREBİR uyan bir cevap (yani modelin üretmesine izin
// verdiğimiz şeyin ta kendisi) gerçek ayrıştırma yolundan geçirilir ve
// HİÇBİR alanın düşmediği doğrulanır. Elle kurulmuş bir struct'la
// yapılan mevcut testler bu yolu hiç çalıştırmıyor.
func TestVerdictParsesASchemaShapedResponse(t *testing.T) {
	raw := `{
	  "verdict": "root_cause_identified",
	  "title": "Oracle bağlantı havuzu tükendi",
	  "summary": "Paylaşılan havuz doldu; çağıran servisler sırayla zaman aşımına düştü.",
	  "root_cause": {
	    "entity": "oracle-prod",
	    "failure_mode": "bağlantı havuzu tükenmesi",
	    "trigger": "toplu iş penceresi",
	    "latent_weakness": "havuz üst sınırı çağıran sayısına göre düşük",
	    "evidence": ["E1", "E3"]
	  },
	  "causal_chain": [
	    {"entity": "oracle-prod", "effect": "bağlantı bekleme süresi arttı", "evidence": ["E1"]},
	    {"entity": "payment-svc", "effect": "istek zaman aşımı", "evidence": ["E3"]}
	  ],
	  "rejected_hypotheses": [
	    {"hypothesis": "payment-svc dağıtımı", "refuted_by": ["N2"], "reason": "pencerede dağıtım yok"}
	  ],
	  "model_confidence": 0.82,
	  "missing_evidence": ["havuz metriği"],
	  "remediation": [
	    {"kind": "mitigate", "action": "havuz üst sınırını geçici artır", "target": "oracle-prod", "risk": "low"}
	  ]
	}`

	var mv rcaModelVerdict
	if err := json.Unmarshal([]byte(strings.TrimSpace(raw)), &mv); err != nil {
		t.Fatalf("şemaya uyan cevap ayrıştırılamadı: %v", err)
	}
	if !rcaVerdictEnumOK(mv.Verdict) {
		t.Fatalf("enum kapısı şemaya uyan cevabı reddetti: %q", mv.Verdict)
	}

	// Her alan ayrı kontrol: biri sessizce düşerse hangisi olduğu belli
	// olsun. Tek bir DeepEqual "bir şey bozuk" derdi, hangisi demezdi.
	checks := []struct {
		field string
		empty bool
	}{
		{"title", mv.Title == ""},
		{"summary", mv.Summary == ""},
		{"root_cause.entity", mv.RootCause.Entity == ""},
		{"root_cause.failure_mode", mv.RootCause.FailureMode == ""},
		{"root_cause.trigger", mv.RootCause.Trigger == ""},
		{"root_cause.latent_weakness", mv.RootCause.LatentWeakness == ""},
		{"root_cause.evidence", len(mv.RootCause.Evidence) == 0},
		{"causal_chain", len(mv.CausalChain) != 2},
		{"rejected_hypotheses", len(mv.RejectedHypotheses) != 1},
		{"rejected_hypotheses[0].refuted_by", len(mv.RejectedHypotheses[0].RefutedBy) == 0},
		{"model_confidence", mv.ModelConfidence == 0},
		{"missing_evidence", len(mv.MissingEvidence) == 0},
		{"remediation", len(mv.Remediation) != 1},
		{"remediation[0].kind", mv.Remediation[0].Kind == ""},
		{"remediation[0].risk", mv.Remediation[0].Risk == ""},
		{"remediation[0].target", mv.Remediation[0].Target == ""},
	}
	for _, c := range checks {
		if c.empty {
			t.Errorf("%s ayrıştırma sonrası BOŞ — model bu alanı ürettiği hâlde "+
				"okunmuyor; panelde sessizce kaybolur", c.field)
		}
	}
}

// TestSalvageHandlesFencedJSON — onarım turu gerçek hatayı çözüyor mu?
//
// En sık model hatası JSON'un kendisi değil, etrafındaki gürültü:
// ```json çitleri ve önsöz cümlesi. Onarım turu bunun için var; taklit
// testler hiç çalıştırmıyordu.
func TestSalvageHandlesFencedJSON(t *testing.T) {
	body := `{"verdict":"probable_cause","title":"t","summary":"s"}`
	cases := map[string]string{
		"kod çiti":           "```json\n" + body + "\n```",
		"çitsiz dil etiketi": "```\n" + body + "\n```",
		"önsöz cümlesi":      "Elbette, işte değerlendirme:\n" + body,
		"sonsöz":             body + "\n\nUmarım yardımcı olur.",
	}
	for name, raw := range cases {
		t.Run(name, func(t *testing.T) {
			fixed, ok := salvageJSONObject(raw)
			if !ok {
				t.Fatalf("onarım turu %s halini kurtaramadı", name)
			}
			var mv rcaModelVerdict
			if err := json.Unmarshal([]byte(fixed), &mv); err != nil {
				t.Fatalf("onarılan metin ayrıştırılamadı: %v", err)
			}
			if !rcaVerdictEnumOK(mv.Verdict) {
				t.Errorf("onarım sonrası enum kapısı reddetti: %q — bu hâlde "+
					"model doğru cevap vermiş olsa bile düşüş yoluna giderdi", mv.Verdict)
			}
		})
	}
}
