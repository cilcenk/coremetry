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
