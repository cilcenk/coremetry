package mcptools

// near_names.go — v0.10.464 (CoSRE sohbet paritesi D3): bulanık servis adı
// eşleştirici, api/near_names.go'dan (v0.10.429) TAŞINDI ki MCP tool'ları da
// kullanabilsin (import yönü api → mcptools; tersi yasak). "hangisini
// kastettin?" aday üreticisi. Eskiden eşleşmeyen ya da belirsiz (2+ önek)
// servis adı SESSİZCE ""/none oluyordu; operatör "login external" yazınca
// asistan hiçbir şey sormadan genel cevaba düşüyordu. Şimdi canlı
// katalogdan adaylar çıkar, cevap ÇİP olarak sorar (TeamOptions deseni).
//
// SAF, katalogla sınırlı (ad UYDURMAZ): tam eş > önek > alt-dize > jeton
// kapsaması ("login external" → "…-login-external-…") > düzenleme mesafesi
// (yazım hatası, ≤2). Deterministik sıra (skor, ad).

import (
	"sort"
	"strings"
	"unicode"
)

// NameTokens — küçük harf, [a-z0-9] dışı her şey ayırıcı; ≥2 karakter.
func NameTokens(s string) []string {
	s = strings.ToLower(s)
	var out []string
	var cur strings.Builder
	flush := func() {
		if cur.Len() >= 2 {
			out = append(out, cur.String())
		}
		cur.Reset()
	}
	for _, r := range s {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			cur.WriteRune(r)
			continue
		}
		if unicode.IsLetter(r) { // Türkçe harf: jetonun parçası ama ASCII değil
			cur.WriteRune(unicode.ToLower(r))
			continue
		}
		flush()
	}
	flush()
	return out
}

// Levenshtein — küçük dizeler için (adlar ≤ 64 karakter); sınır aşımı
// çağıran tarafta (≤2 kabul).
func Levenshtein(a, b string) int {
	ra, rb := []rune(a), []rune(b)
	if len(ra) == 0 {
		return len(rb)
	}
	if len(rb) == 0 {
		return len(ra)
	}
	prev := make([]int, len(rb)+1)
	cur := make([]int, len(rb)+1)
	for j := range prev {
		prev[j] = j
	}
	for i := 1; i <= len(ra); i++ {
		cur[0] = i
		for j := 1; j <= len(rb); j++ {
			cost := 1
			if ra[i-1] == rb[j-1] {
				cost = 0
			}
			cur[j] = min(min(prev[j]+1, cur[j-1]+1), prev[j-1]+cost)
		}
		prev, cur = cur, prev
	}
	return prev[len(rb)]
}

// NearNames — sorgu ifadesine en yakın canlı adlar (skor sırası, tavan max).
// Boş sonuç = katalogda hiçbir yakın ad yok (çağıran none'a düşer).
func NearNames(query string, live []string, max int) []string {
	q := strings.ToLower(strings.TrimSpace(query))
	// <3 karakter aday üretmez: "ap" → api-gateway önerisi gürültüdür
	// (matchLiveName'in ≥3 önek tabanıyla aynı gerekçe).
	if len(q) < 3 || max <= 0 {
		return nil
	}
	qt := NameTokens(q)
	type cand struct {
		name  string
		score int
	}
	var cands []cand
	for _, n := range live {
		ln := strings.ToLower(n)
		score := 0
		switch {
		case ln == q:
			score = 100
		case strings.HasPrefix(ln, q):
			score = 80
		case strings.Contains(ln, q):
			score = 70
		default:
			nt := NameTokens(ln)
			matched := 0
			for _, t := range qt {
				hit := false
				for _, x := range nt {
					if x == t || (len(t) >= 3 && strings.HasPrefix(x, t)) {
						hit = true
						break
					}
				}
				if hit {
					matched++
				}
			}
			switch {
			case len(qt) > 0 && matched == len(qt):
				score = 60 + matched
			case matched > 0 && matched*2 >= len(qt):
				score = 30 + matched
			}
			// Düzenleme mesafesi jeton kapsamasından BAĞIMSIZ hesaplanır ve
			// büyüğü alınır: "chekout-service" hem "service" jetonuyla yarım
			// kapsar (31) hem tüm adla 1 mesafede (50) — 50 kazanmalı, yoksa
			// payment-service'le aynı katmanda kalırdı.
			if len(q) >= 4 && Levenshtein(q, ln) <= 2 {
				if score < 50 {
					score = 50
				}
			} else if score == 0 {
				for _, t := range qt {
					if len(t) < 4 {
						continue
					}
					for _, x := range nt {
						if Levenshtein(t, x) <= 1 {
							score = 25
						}
					}
				}
			}
		}
		if score > 0 {
			cands = append(cands, cand{n, score})
		}
	}
	sort.Slice(cands, func(i, j int) bool {
		if cands[i].score != cands[j].score {
			return cands[i].score > cands[j].score
		}
		return cands[i].name < cands[j].name
	})
	// Katman kuralı: tam eş varsa yalnız o; güçlü aday (≥50: tam jeton
	// kapsaması, yazım hatası, önek/alt-dize) varsa zayıf kısmi kapsama
	// (< 50, ör. yalnız "service" jetonu) düşer — "checkout-service" için
	// payment-service önerilmez.
	if len(cands) > 0 {
		switch {
		case cands[0].score >= 100:
			cands = cands[:1]
		case cands[0].score >= 50:
			n := 0
			for n < len(cands) && cands[n].score >= 50 {
				n++
			}
			cands = cands[:n]
		}
	}
	if len(cands) > max {
		cands = cands[:max]
	}
	out := make([]string, 0, len(cands))
	for _, c := range cands {
		out = append(out, c.name)
	}
	return out
}

// ResolveServiceAmong — bir ifadeyi canlı katalogla çözer: tam/harfe
// duyarsız eş → exact; yoksa NearNames adayları (1 aday = çözüldü sayılır,
// 2+ = candidates, 0 = katalogda yakın ad yok). SAF.
func ResolveServiceAmong(phrase string, live []string, max int) (exact string, candidates []string) {
	p := strings.TrimSpace(phrase)
	if p == "" {
		return "", nil
	}
	for _, n := range live {
		if n == p || strings.EqualFold(n, p) {
			return n, nil
		}
	}
	opts := NearNames(p, live, max)
	switch len(opts) {
	case 0:
		return "", nil // katalogda yakın ad yok (JSON'da candidates hiç çıkmaz)
	case 1:
		return opts[0], nil
	}
	return "", opts
}
