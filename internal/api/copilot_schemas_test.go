package api

import (
	"encoding/json"
	"testing"
)

// v0.9.527 — şema enum'ları sunucunun doğruladığı kümeden TÜRETİLMELİ.
//
// Neden bu testin var olması gerekiyor: elle yazılmış bir enum kopyası
// SESSİZCE ayrışır. `allowedOps`'a yeni bir operatör eklenir, şema eski
// kalır — model artık o operatörü ÜRETEMEZ. Çıktı hâlâ geçerli JSON,
// handler hâlâ mutlu, hiçbir hata yok; sadece yüzey fakirleşmiş olur.
// Sessiz kalitesizlik, tıpkı v0.9.526'nın kapattığı sınıf gibi.

// enumOf, şemanın verilen yolundaki enum listesini çıkarır.
func enumOf(t *testing.T, schema map[string]any, path ...string) []string {
	t.Helper()
	cur := schema
	for i, p := range path {
		props, ok := cur["properties"].(map[string]any)
		if !ok {
			t.Fatalf("%v: %q adımında properties yok", path[:i+1], p)
		}
		next, ok := props[p].(map[string]any)
		if !ok {
			t.Fatalf("%v: %q alanı yok", path[:i+1], p)
		}
		cur = next
	}
	raw, ok := cur["enum"].([]string)
	if !ok {
		t.Fatalf("%v: enum yok (got %T)", path, cur["enum"])
	}
	return raw
}

// Operatör enum'u dizi içinde yaşıyor; onu TestNLToQueryFilterOpEnum
// ayrıca doğruluyor. Burada düz yoldaki enum: aralık ön-ayarı.
func TestNLToQueryEnumsDeriveFromServerValidation(t *testing.T) {
	sc := nlToQuerySchema()

	presets := enumOf(t, sc, "range", "preset")
	if len(presets) != len(allowedPresets) {
		t.Fatalf("preset enum'u %d değer taşıyor, allowedPresets %d — türetme kopmuş", len(presets), len(allowedPresets))
	}
	for _, p := range presets {
		if !allowedPresets[p] {
			t.Errorf("şema %q ön-ayarını sunuyor ama sunucu 1h'a çeviriyor", p)
		}
	}
}

// enumOf "filters" için dizi içine inemez; filtre enum'unu ayrıca alalım.
func TestNLToQueryFilterOpEnum(t *testing.T) {
	sc := nlToQuerySchema()
	filters := sc["properties"].(map[string]any)["filters"].(map[string]any)
	item := filters["items"].(map[string]any)
	op := item["properties"].(map[string]any)["op"].(map[string]any)
	got, ok := op["enum"].([]string)
	if !ok || len(got) == 0 {
		t.Fatal("filtre op alanı enum taşımalı — merdivenin bu yüzeydeki asıl kazancı bu")
	}
	for _, v := range got {
		if !allowedOps[v] {
			t.Errorf("%q sunucunun kabul ettiği küme dışında", v)
		}
	}
	// Alan adları Go tipiyle (nlToQueryFilter) aynı olmalı, yoksa şema
	// modele YANLIŞ anahtar adı dayatır ve çözümleme boş yapı üretir.
	props := item["properties"].(map[string]any)
	for _, k := range []string{"k", "op", "v"} {
		if _, ok := props[k]; !ok {
			t.Errorf("filtre şemasında %q alanı yok — nlToQueryFilter ile ayrışmış", k)
		}
	}
}

// OpenAI `strict` şartı: her object'te additionalProperties:false ve TÜM
// anahtarlar required. Uymayan şema strict-uygulayan bir uçta 400 alır,
// merdiven json_object'e düşer ve enum kazancı sessizce kaybolur.
func TestSchemasAreStrictValid(t *testing.T) {
	for name, sc := range map[string]map[string]any{
		"nlToQuery":       nlToQuerySchema(),
		"chOptimize":      chOptimizeSchema(),
		"serviceAnalysis": serviceAnalysisSchema(),
	} {
		t.Run(name, func(t *testing.T) { assertStrict(t, sc, name) })
	}
}

func assertStrict(t *testing.T, node map[string]any, path string) {
	t.Helper()
	switch node["type"] {
	case "object":
		if node["additionalProperties"] != false {
			t.Errorf("%s: additionalProperties:false yok", path)
		}
		props, _ := node["properties"].(map[string]any)
		req, _ := node["required"].([]string)
		if len(req) != len(props) {
			t.Errorf("%s: required %d alan sayıyor, properties %d — strict TÜMÜNÜ ister", path, len(req), len(props))
		}
		for k, v := range props {
			if child, ok := v.(map[string]any); ok {
				assertStrict(t, child, path+"."+k)
			}
		}
	case "array":
		if item, ok := node["items"].(map[string]any); ok {
			assertStrict(t, item, path+"[]")
		}
	}
}

// Şema JSON'a serileşebilmeli — istek gövdesine gömülüyor. Serileşemeyen
// bir değer (fonksiyon, kanal) sessizce boş gövde üretirdi.
func TestSchemasMarshal(t *testing.T) {
	for name, sc := range map[string]map[string]any{
		"nlToQuery":       nlToQuerySchema(),
		"chOptimize":      chOptimizeSchema(),
		"serviceAnalysis": serviceAnalysisSchema(),
	} {
		b, err := json.Marshal(sc)
		if err != nil {
			t.Errorf("%s serileşmedi: %v", name, err)
		}
		if len(b) < 40 {
			t.Errorf("%s şüpheli derecede küçük: %s", name, b)
		}
	}
}

// sortedKeys deterministik olmalı: Go map yineleme sırası rastgele, ve
// sıralanmazsa aynı şema her çağrıda farklı serileşir.
func TestSortedKeysDeterministic(t *testing.T) {
	first := sortedKeys(allowedPresets)
	for i := 0; i < 20; i++ {
		got := sortedKeys(allowedPresets)
		for j := range got {
			if got[j] != first[j] {
				t.Fatalf("sortedKeys deterministik değil: %v vs %v", first, got)
			}
		}
	}
}

// serviceAnalysis'in guven enum'u prompt'un dayattığı kümeyle aynı
// olmalı (copilot_aianalyze.go:47). Ayrışırsa model şemanın izin verdiği
// ama prompt'un tanımadığı bir değer üretir.
func TestServiceAnalysisGuvenMatchesPrompt(t *testing.T) {
	for _, v := range serviceAnalysisGuven {
		if !contains(serviceAnalysisPrompt, `"`+v+`"`) {
			t.Errorf("guven değeri %q prompt'ta geçmiyor — şema ile prompt ayrışmış", v)
		}
	}
}

func contains(haystack, needle string) bool {
	return len(needle) > 0 && len(haystack) >= len(needle) &&
		func() bool {
			for i := 0; i+len(needle) <= len(haystack); i++ {
				if haystack[i:i+len(needle)] == needle {
					return true
				}
			}
			return false
		}()
}
