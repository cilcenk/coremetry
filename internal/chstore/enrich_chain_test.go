// v0.9.554 regresyon testi — okuma zenginleştirme zincirinin sözleşmesi.
//
// Sözleşme tek cümle: DEPLOY ÖNCE, ÖNCELİK SONRA.
//
// computePriority'nin kritik kolu RecentDeploy'a bakar; deploy adımı
// koşmazsa RecentDeploy nil kalır ve taze deploy sonrası kritik bir
// problem P1 yerine P2 görünür. v0.9.553'te sohbet yüzeyleri, v0.9.554'te
// MCP araçları tam bu yüzden Problems sayfasıyla çelişiyordu.
//
// Zincir bir *Store metodu olduğu için (CH bağlantısı ister) davranış
// testi yerine kaynak sözleşmesi sabitleniyor — repoda emsali var
// (snapshot_batch_test.go, aynı sebep).
package chstore

import (
	"os"
	"strings"
	"testing"
)

func TestEnrichForReadOrder(t *testing.T) {
	b, err := os.ReadFile("problem_telemetry.go")
	if err != nil {
		t.Fatalf("kaynak okunamadı: %v", err)
	}
	src := string(b)

	i := strings.Index(src, "func (s *Store) EnrichProblemsForRead(")
	if i < 0 {
		t.Fatal("EnrichProblemsForRead kaybolmuş — api ve mcptools ikisi de " +
			"buna bağlı, kaldırılırsa her biri kendi zincirini yazar ve ayrışırlar")
	}
	body := src[i:]
	if end := strings.Index(body, "\n}\n"); end > 0 {
		body = body[:end]
	}

	iDeploy := strings.Index(body, "EnrichProblemsWithDeploys(")
	iPrio := strings.Index(body, "EnrichProblemsWithPriority(")
	if iDeploy < 0 {
		t.Error("deploy adımı kaybolmuş — RecentDeploy nil kalır, " +
			"postDeploy dalı hiç ateşlemez, P1'ler P2 görünür")
	}
	if iPrio < 0 {
		t.Error("öncelik adımı kaybolmuş — Priority boş kalır ve omitempty " +
			"ile JSON'dan düşer; tüketici tier'ı uydurur")
	}
	if iDeploy >= 0 && iPrio >= 0 && iDeploy > iPrio {
		t.Error("SIRA TERS: öncelik deploy'dan önce hesaplanıyor — " +
			"RecentDeploy o an hâlâ nil olduğu için deploy adımının " +
			"hiçbir etkisi olmaz, sessizce yanlış tier üretilir")
	}
}

func TestFilterProblemsByPriority(t *testing.T) {
	rows := []Problem{
		{ID: "a", Priority: "P1"},
		{ID: "b", Priority: "P2"},
		{ID: "c", Priority: "P3"},
		{ID: "d", Priority: ""}, // bucket'sız → P3 sayılır
	}
	t.Run("boş filtre hepsini geçirir", func(t *testing.T) {
		if got := FilterProblemsByPriority(rows, nil); len(got) != 4 {
			t.Errorf("%d satır, beklenen 4", len(got))
		}
	})
	t.Run("P1", func(t *testing.T) {
		got := FilterProblemsByPriority(rows, []string{"P1"})
		if len(got) != 1 || got[0].ID != "a" {
			t.Errorf("beklenen [a], gelen %v", ids(got))
		}
	})
	t.Run("boş bucket P3 sayılır", func(t *testing.T) {
		got := FilterProblemsByPriority(rows, []string{"P3"})
		if len(got) != 2 {
			t.Errorf("beklenen 2 (c ve bucket'sız d), gelen %v", ids(got))
		}
	})
}

func TestSortProblemsByPriority(t *testing.T) {
	rows := []Problem{
		{ID: "eski-p1", Priority: "P1", StartedAt: 100},
		{ID: "p3", Priority: "P3", StartedAt: 900},
		{ID: "yeni-p1", Priority: "P1", StartedAt: 500},
		{ID: "bucketsiz", Priority: "", StartedAt: 800}, // P3 sayılır
		{ID: "p2", Priority: "P2", StartedAt: 200},
	}
	got := ids(SortProblemsByPriority(rows))
	want := []string{"yeni-p1", "eski-p1", "p2", "p3", "bucketsiz"}
	if len(got) != len(want) {
		t.Fatalf("uzunluk %d, beklenen %d", len(got), len(want))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("sıra %v, beklenen %v", got, want)
		}
	}
}

func ids(ps []Problem) []string {
	out := make([]string, len(ps))
	for i, p := range ps {
		out[i] = p.ID
	}
	return out
}
