package ldap

import "strings"

// team_candidates.go — v0.8.523 (operatör isteği: "auto-discover et ya
// da UI'dan seçmeme izin ver"). Inspect edilen kullanıcının attribute
// değerlerine bilinen bileşik-desen kütüphanesini uygular; boş olmayan
// ve birbirinden farklı sonuç veren adayları döndürür. Operatör UI'da
// ÇIKAN DEĞERİ görüp tıklar — regex bilgisi gerekmez; seçim
// TeamAttribute+TeamRegex olarak kaydedilir. Çıkan team değeri sonra
// katalog sy-team/ug-team eşleşmesi ve e-posta yönlendirmesinde
// kullanılacağı için adaylar TRİMLİ/temiz üretilir.
type TeamCandidate struct {
	// Pattern — TeamRegex'e yazılacak desen; "" = ham değer (regex yok).
	Pattern string `json:"pattern"`
	// Extracted — deseni BU kullanıcının değerine uygulayınca çıkan.
	Extracted string `json:"extracted"`
	// Label — insan-okur kısa açıklama (UI tooltip'i).
	Label string `json:"label"`
}

// teamPatternLib — sırayla denenen desenler. En yapısal (bileşik
// displayName şekilleri) önce; jenerik ayraçlar sonra. Desenler
// applyTeamRegex ile birebir aynı semantikte çalışır (ilk yakalama
// grubu; eşleşmezse boş).
var teamPatternLib = []struct{ pattern, label string }{
	{"", "tam değer"},
	// "Ad Soyad (Bölüm) - ÜNVAN-EKİP" / "… * ÜNVAN-EKİP" — ekip adı
	// tire İÇEREBİLİR (SY-… gibi): ünvan ilk tireye kadar atlanır,
	// kalanın tamamı ekiptir.
	{`\)\s*[-*]\s*[^-]+-(.+)$`, "parantezden ve ünvandan sonrası"},
	{`\)\s*[-*]\s*(.+)$`, "parantezden sonrası (ünvan dahil)"},
	{`\(([^)]+)\)`, "parantez içi"},
	{`-([^-]+)$`, "son tireden sonrası"},
	{`\*\s*([^*]+)$`, "yıldızdan sonrası"},
	{`/([^/]+)$`, "son bölü işaretinden sonrası"},
	{`,\s*([^,]+)$`, "son virgülden sonrası"},
}

// TeamCandidates generates the click-to-pick extraction candidates for
// one attribute value. Pure — tablo-testli.
func TeamCandidates(value string) []TeamCandidate {
	value = strings.TrimSpace(value)
	if value == "" {
		return nil
	}
	seen := map[string]bool{}
	out := []TeamCandidate{}
	for _, p := range teamPatternLib {
		ext := value
		if p.pattern != "" {
			ext = applyTeamRegex(value, p.pattern)
		}
		ext = strings.TrimSpace(ext)
		if ext == "" || seen[ext] {
			continue
		}
		seen[ext] = true
		out = append(out, TeamCandidate{Pattern: p.pattern, Extracted: ext, Label: p.label})
	}
	return out
}

// candidateEligible — Inspect yanıtında hangi attribute'lara aday
// üretileceğini sınırlar: tek-değerli-vari, makul uzunlukta, ikili
// (binary) olmayan alanlar. memberOf gibi çok-değerli listeler ve
// foto/sertifika blob'ları elenmiş olur.
func candidateEligible(vals []string) bool {
	if len(vals) == 0 || len(vals) > 3 {
		return false
	}
	v := vals[0]
	return v != "" && len(v) <= 300 && !strings.HasPrefix(v, "[")
}
