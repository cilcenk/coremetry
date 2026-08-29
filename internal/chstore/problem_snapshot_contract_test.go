package chstore

// v0.10.156 — arka plan işleri `problems FINAL`'ı tick'te BİR kez okur
// (OpenProblemsSnapshot memo). Kaynak taraması: arka plan paketlerinde
// ListProblems/FindOpenProblem çağrısı kalmadı (MCP + API + monitor'ün
// tekil doğruluk isteyen yerleri dışında), her problems INSERT'i memo'yu
// düşürüyor, snapshot memo'dan geçiyor.

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

func readRepo(t *testing.T, rel string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("..", "..", rel))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestOpenProblemsSnapshot_MemoWiring(t *testing.T) {
	src := readRepo(t, "internal/chstore/problem.go")
	if !strings.Contains(src, "return s.openSnap.Get(ctx, s.openProblemsSnapshotUncached)") {
		t.Fatal("OpenProblemsSnapshot must go through the 5s memo")
	}
	if n := strings.Count(src, "s.invalidateOpenSnapshot()"); n != 2 {
		t.Fatalf("expected 2 invalidation points (UpsertProblem, UpsertProblemAISummary), got %d", n)
	}
	if n := strings.Count(src, "INSERT INTO problems"); n != 2 {
		t.Fatalf("problem writers changed (%d INSERTs) — add invalidateOpenSnapshot to the new one", n)
	}
}

func TestBackgroundJobsUseTheSnapshot(t *testing.T) {
	// Arka plan paketleri: kural/tick başına FINAL taraması yok.
	for _, f := range []string{
		"internal/evaluator/evaluator.go", "internal/evaluator/watcher_eval.go", "internal/evaluator/slo_burn.go",
		"internal/anomaly/fusion.go", "internal/anomaly/rootcause_worker.go", "internal/anomaly/problem_explainer.go",
		"internal/anomaly/exception_storm.go", "internal/monitor/runner.go",
	} {
		src := readRepo(t, f)
		if regexp.MustCompile(`\.FindOpenProblem\(`).MatchString(src) && f != "internal/evaluator/evaluator.go" {
			t.Errorf("%s still calls FindOpenProblem (per-rule FINAL scan) — use OpenProblemsSnapshot().ByKey", f)
		}
		if regexp.MustCompile(`\.ListProblems\(ctx, chstore\.ProblemFilter\{\s*Status:\s*"open"`).MatchString(src) {
			t.Errorf("%s still lists open problems per tick — use OpenProblemsSnapshot().Filter", f)
		}
	}
	// evaluator.go: tek kalan FindOpenProblem, snapshot yokken (openSnap nil) yedek dal.
	ev := readRepo(t, "internal/evaluator/evaluator.go")
	if n := strings.Count(ev, ".FindOpenProblem("); n != 1 {
		t.Errorf("evaluator.go: expected exactly 1 FindOpenProblem fallback, got %d", n)
	}
}

func TestOpenProblemsFilter(t *testing.T) {
	o := &OpenProblems{}
	mk := func(id, status, sev string, at int64) *Problem {
		return &Problem{ID: id, Status: status, Severity: sev, StartedAt: at}
	}
	o.all = []*Problem{mk("a", "open", "critical", 10), mk("b", "acknowledged", "critical", 30), mk("c", "open", "warning", 20), mk("d", "open", "critical", 40)}
	got := o.Filter("open", "critical", 0)
	if len(got) != 2 || got[0].ID != "d" || got[1].ID != "a" {
		t.Fatalf("open+critical desc: %+v", got)
	}
	if got := o.Filter("", "", 3); len(got) != 3 || got[0].ID != "d" || got[2].ID != "c" {
		t.Fatalf("all, limit 3, desc: %+v", got)
	}
	if got := o.Filter("open", "", 500); len(got) != 3 {
		t.Fatalf("open only: %d", len(got))
	}
	var nilSnap *OpenProblems
	if nilSnap.Filter("open", "", 5) != nil {
		t.Fatal("nil snapshot → nil")
	}
	// Kopya: çağıranın değişikliği snapshot'a sızmaz.
	got[0].Severity = "info"
	if o.all[3].Severity != "critical" {
		t.Fatal("Filter must return copies")
	}
}

// v0.10.156 inceleme D1 — ByKey/ByID KOPYA döner: memo'lu snapshot goroutine'ler
// arasında paylaşılır; çağıranın MarkResolved/Value yazımı paylaşılan satırı
// (ve eşzamanlı Filter kopyalarını) değiştirmemeli.
func TestOpenProblemsLookupsReturnCopies(t *testing.T) {
	p := &Problem{ID: "p1", RuleID: "r", Service: "svc", Status: "open", Value: 1}
	o := &OpenProblems{byKey: map[string]*Problem{OpenProblemKey("r", "svc"): p}, byID: map[string]*Problem{"p1": p}, all: []*Problem{p}}
	k := o.ByKey("r", "svc")
	k.Status, k.Value = "resolved", 99
	if p.Status != "open" || p.Value != 1 {
		t.Fatalf("ByKey leaked the shared row: %+v", *p)
	}
	b := o.ByID("p1")
	b.Description = "mutated"
	if p.Description != "" {
		t.Fatalf("ByID leaked the shared row: %+v", *p)
	}
	if o.ByKey("r", "other") != nil || o.ByID("nope") != nil {
		t.Fatal("miss must stay nil")
	}
	if got := o.Filter("open", "", 0); len(got) != 1 || got[0].Status != "open" {
		t.Fatalf("Filter must see the untouched row: %+v", got)
	}
}
