package api

import (
	"testing"

	"github.com/cilcenk/coremetry/internal/chstore"
)

// v0.10.706 — /api/problems okuma-anı süzgeçleri (öncelik taşındı, kategori yeni).
func TestProblemsReadFilters(t *testing.T) {
	probs := []chstore.Problem{
		{ID: "a", Priority: "P1", Category: chstore.CategoryError},
		{ID: "b", Priority: "", Category: chstore.CategorySlowdown},
		{ID: "c", Priority: "P2", Category: chstore.CategoryCustom},
	}
	if got := filterProblemsByPriority(append([]chstore.Problem(nil), probs...), nil, nil); len(got) != 3 {
		t.Fatal("öncelik süzgeci yokken dokunmaz")
	}
	got := filterProblemsByPriority(append([]chstore.Problem(nil), probs...), []string{"P3"}, map[string]bool{"P3": true})
	if len(got) != 1 || got[0].ID != "b" {
		t.Fatalf("boş öncelik P3 sayılır: %+v", got)
	}
	cats, catMap := parseProblemCategories("")
	if len(cats) != len(chstore.ProblemCategories) || !catMap[chstore.CategoryError] {
		t.Fatal("boş param = tam sözlük")
	}
	if got := filterProblemsByCategory(append([]chstore.Problem(nil), probs...), cats, catMap); len(got) != 3 {
		t.Fatal("tam sözlük süzmez")
	}
	cats, catMap = parseProblemCategories("SLOWDOWN,bogus,CUSTOM")
	got = filterProblemsByCategory(append([]chstore.Problem(nil), probs...), cats, catMap)
	if len(got) != 2 || got[0].ID != "b" || got[1].ID != "c" {
		t.Fatalf("alt küme: %+v", got)
	}
}
