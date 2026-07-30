package chstore

import (
	"os"
	"regexp"
	"strings"
	"testing"
)

// v0.9.445 (prod hacim denetimi yan bulgusu) — UpsertProblemAISummary'nin
// kolon listesinde pod yoktu: AI özeti yazan her problem, bütün-satır
// replace'te pod bağlamını kaybediyordu (v0.9.403 pod kolonu eklendiğinde
// bu yazma yolu güncellenmemişti). Pin: problems tablosuna yazan HER
// explicit kolon listesi pod'u taşır ve Append argüman sayısı kolon
// sayısıyla eşleşir — bir sonraki kolon eklemesinde aynı sınıf tekrar
// yakalansın.
func TestProblemWritePathsCarryPod(t *testing.T) {
	b, err := os.ReadFile("problem.go")
	if err != nil {
		t.Fatal(err)
	}
	src := string(b)
	lists := regexp.MustCompile(`INSERT INTO problems\s*\n?\s*\(([^)]+)\)`).FindAllStringSubmatch(src, -1)
	if len(lists) < 2 {
		t.Fatalf("expected ≥2 explicit problems column lists, found %d", len(lists))
	}
	for i, m := range lists {
		cols := m[1]
		if !strings.Contains(cols, "pod") {
			t.Errorf("problems INSERT #%d omits pod — whole-row replace wipes the pod context:\n%s", i+1, cols)
		}
	}
}
