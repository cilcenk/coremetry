package api

// problems_read_filters.go — v0.10.706. /api/problems'ın OKUMA-ANI süzgeçleri:
// öncelik (v0.5.210, api.go'dan buraya bayt-bayt taşındı) ve kategori
// (Dynatrace paritesi #5). İkisi de zenginleştirmeden SONRA çalışır çünkü
// Problem.Priority / Problem.Category CH kolonu değil, saf türetimdir ve
// SQL'e inemez. api.go BÜYÜMEZ kuralı: mantık burada, api.go tek satır.

import (
	"github.com/cilcenk/coremetry/internal/chstore"
)

// parseProblemCategories — ?category=csv → (liste, küme). Boş/bilinmeyen =
// tam sözlük (süzgeç yok) — normalizeInboxSet sözleşmesi.
func parseProblemCategories(raw string) ([]string, map[string]bool) {
	cats := normalizeInboxSet(raw, chstore.ProblemCategories)
	m := make(map[string]bool, len(cats))
	for _, c := range cats {
		m[c] = true
	}
	return cats, m
}

// filterProblemsByPriority — Priority chip filter, applied AFTER enrich
// because Problem.Priority is populated by EnrichProblemsWithPriority, not
// stored on the CH row. Default "P3" matches the frontend's fallback for
// un-bucketed rows so the chip behaviour is consistent across read and render.
func filterProblemsByPriority(probs []chstore.Problem, prios []string, prioMap map[string]bool) []chstore.Problem {
	if len(prios) == 0 {
		return probs
	}
	keep := make([]chstore.Problem, 0, len(probs))
	for _, p := range probs {
		bucket := p.Priority
		if bucket == "" {
			bucket = "P3"
		}
		if prioMap[bucket] {
			keep = append(keep, p)
		}
	}
	return keep
}

// filterProblemsByCategory — tam sözlük = süzgeç yok; alt küme = yalnız o
// kategoriler (Category boşsa satır düşer: zenginleştirme koşmadıysa bu
// yol zaten çalışmaz).
func filterProblemsByCategory(probs []chstore.Problem, cats []string, catMap map[string]bool) []chstore.Problem {
	if len(cats) >= len(chstore.ProblemCategories) {
		return probs
	}
	keep := make([]chstore.Problem, 0, len(probs))
	for _, p := range probs {
		if catMap[p.Category] {
			keep = append(keep, p)
		}
	}
	return keep
}
