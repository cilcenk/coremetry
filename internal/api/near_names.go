package api

// near_names.go — v0.10.429 (CoSRE router boşlukları D1): "hangisini
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

// guidedServiceAskMax — sunulan servis çipi tavanı (guidedTeamAskMax ikizi).
const guidedServiceAskMax = 8

// nameTokens — küçük harf, [a-z0-9] dışı her şey ayırıcı; ≥2 karakter.
func nameTokens(s string) []string {
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

// levenshtein — küçük dizeler için (adlar ≤ 64 karakter); sınır aşımı
// çağıran tarafta (≤2 kabul).
func levenshtein(a, b string) int {
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

// nearNames — sorgu ifadesine en yakın canlı adlar (skor sırası, tavan max).
// Boş sonuç = katalogda hiçbir yakın ad yok (çağıran none'a düşer).
func nearNames(query string, live []string, max int) []string {
	q := strings.ToLower(strings.TrimSpace(query))
	// <3 karakter aday üretmez: "ap" → api-gateway önerisi gürültüdür
	// (matchLiveName'in ≥3 önek tabanıyla aynı gerekçe).
	if len(q) < 3 || max <= 0 {
		return nil
	}
	qt := nameTokens(q)
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
			nt := nameTokens(ln)
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
			if len(q) >= 4 && levenshtein(q, ln) <= 2 {
				if score < 50 {
					score = 50
				}
			} else if score == 0 {
				for _, t := range qt {
					if len(t) < 4 {
						continue
					}
					for _, x := range nt {
						if levenshtein(t, x) <= 1 {
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

// serviceCandidates — deterministik router için: mesajın ad-şekilli
// jetonları (≥3, stopword/env değil) canlı servis adlarının SINIRLI
// parçalarına ya da öneklerine oturuyorsa, en çok jeton kapsayan servisler.
// 1 aday = çözüldü sayılır (çağıran doğrudan yönlendirir), 2+ = sor.
// Sıfır jeton eşleşmesi → nil ("bugün hava nasıl" servis üretmez).
func serviceCandidates(msg string, services, envs []string, max int) []string {
	envTok := map[string]bool{}
	for _, e := range envs {
		envTok[strings.ToLower(e)] = true
	}
	var toks []string
	for _, t := range guidedTokens(msg) {
		if len(t) < 3 || guidedStopwords[t] || envTok[t] || !asciiNameToken(t) {
			continue
		}
		toks = append(toks, t)
	}
	if len(toks) == 0 {
		return nil
	}
	type cand struct {
		name string
		hits int
	}
	var cands []cand
	best := 0
	for _, svc := range services {
		segs := nameTokens(svc)
		hits := 0
		for _, t := range toks {
			for _, sg := range segs {
				if sg == t || strings.HasPrefix(sg, t) {
					hits++
					break
				}
			}
		}
		if hits == 0 {
			continue
		}
		if hits > best {
			best = hits
		}
		cands = append(cands, cand{svc, hits})
	}
	if best == 0 {
		return nil
	}
	var out []string
	for _, c := range cands {
		if c.hits == best {
			out = append(out, c.name)
		}
	}
	sort.Strings(out)
	if len(out) > max {
		out = out[:max]
	}
	return out
}
