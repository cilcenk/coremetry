package api

// near_names.go — v0.10.429 (CoSRE router boşlukları D1) aday üreticisi;
// v0.10.464 (D3) bulanık çekirdek (NameTokens/Levenshtein/NearNames)
// mcptools/near_names.go'ya TAŞINDI (tool'lar da kullanıyor), burada ince
// sarmalayıcılar + router'a özgü serviceCandidates kaldı.

import (
	"sort"
	"strings"

	"github.com/cilcenk/coremetry/internal/mcptools"
)

// guidedServiceAskMax — sunulan servis çipi tavanı (guidedTeamAskMax ikizi).
const guidedServiceAskMax = 8

func nameTokens(s string) []string { return mcptools.NameTokens(s) }
func levenshtein(a, b string) int  { return mcptools.Levenshtein(a, b) }
func nearNames(query string, live []string, max int) []string {
	return mcptools.NearNames(query, live, max)
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
