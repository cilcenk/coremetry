package api

import "sort"

// Katı-JSON yüzeylerinin çıktı şemaları (v0.9.527).
//
// `json_object` yalnız "geçerli JSON üret" der. Alan eksik gelebilir, tip
// kayabilir, enum dışı değer gelebilir — bu yüzden her yüzey çözümlemeden
// SONRA elle temizlik yapıyor. `json_schema` çözümlemeyi şemaya kilitler
// ve asıl kazanç ENUM: modelin üretebileceği değer kümesi daralır.
//
// İKİ KURAL:
//
//  1. Enum'lar sunucunun ZATEN doğruladığı kümeden TÜRETİLİR, elle
//     yazılmaz. Elle yazılan bir kopya sessizce ayrışır: `allowedOps`'a
//     yeni bir operatör eklenir, şema eski kalır, model o operatörü
//     üretemez — kimse fark etmez çünkü çıktı hâlâ geçerlidir, sadece
//     fakirdir. `schema_test.go` bu türetmeyi pinler.
//
//  2. Sunucu-tarafı doğrulama KALDIRILMAZ. Şema desteği yoklamayla
//     kapanmış, basamak düşmüş ya da uç eski olabilir; üç durumda da
//     yanıt şemasız gelir. Şema kaliteyi yükseltir, doğrulamanın yerini
//     almaz.
//
// Şemalar OpenAI `strict` kurallarına uygun: her object'te
// `additionalProperties: false` ve tüm anahtarlar `required`.

// sortedKeys — map'ten deterministik enum listesi. Go map yineleme sırası
// rastgele; sıralamazsak aynı şema her çağrıda farklı serileşir ve
// istek gövdesi gereksiz yere değişir.
func sortedKeys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func strProp() map[string]any { return map[string]any{"type": "string"} }

func strArrayProp() map[string]any {
	return map[string]any{"type": "array", "items": map[string]any{"type": "string"}}
}

// objSchema — strict-uyumlu object: tüm anahtarlar required, ek alan yok.
func objSchema(props map[string]any) map[string]any {
	req := make([]string, 0, len(props))
	for k := range props {
		req = append(req, k)
	}
	sort.Strings(req)
	return map[string]any{
		"type":                 "object",
		"properties":           props,
		"required":             req,
		"additionalProperties": false,
	}
}

// nlToQuerySchema — doğal dil → filtre. Merdivenin en çok kazandırdığı
// yüzey: çıktı doğrudan MAKİNE tarafından tüketiliyor (filtre listesi +
// aralık), düzyazı değil. `op` ve `preset` enum'a bağlanınca handler'ın
// "bilinmeyen operatörü düşür" / "bilinmeyen ön-ayarı 1h'a çevir"
// dalları modelin ulaşamayacağı hale gelir.
func nlToQuerySchema() map[string]any {
	return objSchema(map[string]any{
		"filters": map[string]any{
			"type": "array",
			"items": objSchema(map[string]any{
				"k":  strProp(),
				"op": map[string]any{"type": "string", "enum": sortedKeys(allowedOps)},
				"v":  strArrayProp(),
			}),
		},
		"range": objSchema(map[string]any{
			"preset": map[string]any{"type": "string", "enum": sortedKeys(allowedPresets)},
		}),
		"explain": strProp(),
	})
}

// chOptimizeSchema — CH sorgu iyileştirme. `Raw`/`Warning` KASITLI olarak
// yok: onlar sunucunun çözümleme başarısızlığında doldurduğu alanlar,
// modelin üreteceği alanlar değil. Şemaya koymak modele "bunları da
// doldur" demek olurdu.
func chOptimizeSchema() map[string]any {
	return objSchema(map[string]any{
		"optimized":   strProp(),
		"explanation": strProp(),
	})
}

// serviceAnalysisGuven — prompt'un dayattığı güven kümesi
// (copilot_aianalyze.go:47). Diyakritiksiz: prompt öyle yazıyor ve
// çözümleme tarafı da öyle bekliyor.
var serviceAnalysisGuven = []string{"yuksek", "orta", "dusuk"}

// serviceAnalysisSchema — servis analizi. Buradaki kazanç sınırlı ve
// bunu açıkça söylemek gerekiyor: şema ŞEKLİ garantiler, İÇERİĞİ değil.
// `postCheckServiceAnalysis` (uydurulmuş servis adı avı) yerinde KALIR —
// o kontrol düzyazının İÇİNDEKİ tokenları tarıyor, ve hiçbir JSON şeması
// bir string'in içindeki servis adının gerçek olup olmadığını kısıtlayamaz.
func serviceAnalysisSchema() map[string]any {
	return objSchema(map[string]any{
		"ozet":        strProp(),
		"olasi_neden": strProp(),
		"kanit":       strArrayProp(),
		"oneriler":    strArrayProp(),
		"guven":       map[string]any{"type": "string", "enum": serviceAnalysisGuven},
	})
}

// numProp — sayısal alan (v0.9.559). Şemalarda ilk kez RCA verdict'inin
// `model_confidence` alanıyla gerekti.
//
// Aralık ŞEMADA kısıtlanmıyor (minimum/maximum), bilerek: json_schema
// desteği uçtan uca garanti değil (merdiven düşebilir) ve aralık
// kontrolü zaten sunucuda var — capRCAConfidence NaN/Inf ve aralık
// dışını kıstırıyor. Şemaya güvenip sunucu kontrolünü kaldırmak, bu
// dosyanın başındaki 2. kuralın ihlali olurdu.
func numProp() map[string]any { return map[string]any{"type": "number"} }

// enumProp — verilen kümeden seçim. Boş küme geçersizdir (strict şema
// boş enum kabul etmez), o yüzden çağıran boşluğu kendi ele alır.
func enumProp(vals []string) map[string]any {
	return map[string]any{"type": "string", "enum": vals}
}

// rcaVerdictSchema — RCA verdict'i (v0.9.559).
//
// Tasarım: docs/cosre-verdict-design.md §3.
//
// İki enum SUNUCUDAN gelir ve asıl kazanç orada:
//
//   - entities: root_cause.entity yalnız GERÇEK bir varlık olabilir.
//   - rivals:   rakip hipotezi model YAZMAZ, listeden SEÇER. Serbest
//     bırakmak, hiç değerlendirilmemiş bir rakibi sahte bir gerekçeyle
//     "elenmiş" göstermeye izin veriyordu ve elemecilik tavanı böylece
//     tek satırla atlanıyordu.
//
// `deciding_rules` alanı KASITLI olarak YOK: R1-R7 kodda kural olarak
// yaşamıyor, modele sordurmak sıfır kalkanı olan bir DENETİM İZİ
// TAKLİDİ üretirdi. Taklit, yokluktan kötüdür.
//
// `impact` da YOK: sayılar ClickHouse'tan gelir (rca_impact.go), modele
// hesaplatılmaz — yalnız yorumlatılır.
func rcaVerdictSchema(entities, rivals []string) map[string]any {
	// Strict şema boş enum kabul etmez. Varlık listesi boşsa (hipotez
	// çok zayıf) alan serbest string kalır ve K3 taraması yakalar —
	// kalkan zinciri tek bir halkaya bağlı değil.
	entityProp := strProp()
	if len(entities) > 0 {
		entityProp = enumProp(entities)
	}
	rivalProp := strProp()
	if len(rivals) > 0 {
		rivalProp = enumProp(rivals)
	}

	return objSchema(map[string]any{
		"verdict": enumProp([]string{
			"root_cause_identified", "probable_cause", "insufficient_evidence",
		}),
		"title":   strProp(),
		"summary": strProp(),
		"root_cause": objSchema(map[string]any{
			"entity":          entityProp,
			"failure_mode":    strProp(),
			"trigger":         strProp(),
			"latent_weakness": strProp(),
			"evidence":        strArrayProp(),
		}),
		"causal_chain": map[string]any{
			"type": "array",
			"items": objSchema(map[string]any{
				"entity":   strProp(),
				"effect":   strProp(),
				"evidence": strArrayProp(),
			}),
		},
		"rejected_hypotheses": map[string]any{
			"type": "array",
			"items": objSchema(map[string]any{
				"hypothesis": rivalProp,
				"refuted_by": strArrayProp(),
				"reason":     strProp(),
			}),
		},
		"model_confidence": numProp(),
		"missing_evidence": strArrayProp(),
		"remediation": map[string]any{
			"type": "array",
			"items": objSchema(map[string]any{
				"kind":   enumProp([]string{"mitigate", "fix"}),
				"action": strProp(),
				"target": strProp(),
				"risk":   enumProp([]string{"low", "medium", "high"}),
			}),
		},
	})
}
