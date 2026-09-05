// Package promptfmt — v0.10.404 (CoSRE denetimi P2): prompt'a giren HAM
// telemetri metninin biçim kaçışı.
//
// Log gövdesi, stacktrace ve JSON'a gömülü attribute değerleri kod
// çitinin (```) içine yazılıyor; json.Marshal backtick'i KAÇIRMAZ. İçinde
// ``` geçen bir değer çiti erken kapatır ve sonrası "veri" değil talimat
// düzlemi olur — enjeksiyon yüzeyi. Bu redaksiyon DEĞİL (operatör
// tercihi: tam sadakat): karakterler korunur, yalnız üçlü backtick
// tipografik eşdeğeriyle (ˋ U+02CB) değiştirilir; model okur, çit
// bozulmaz.
package promptfmt

import "strings"

// FenceSafe — ``` → ˋˋˋ; başka hiçbir baytı değiştirmez.
func FenceSafe(s string) string {
	if !strings.Contains(s, "```") {
		return s
	}
	return strings.ReplaceAll(s, "```", "ˋˋˋ")
}
