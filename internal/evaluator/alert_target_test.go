package evaluator

import (
	"os"
	"strings"
	"testing"

	"github.com/cilcenk/coremetry/internal/chstore"
)

// v0.10.331 — hedefli kural: özne türü, gerekçe metni, dispatch pini.
func TestTargetSubjectAndDescribe(t *testing.T) {
	tg := chstore.RuleTarget{Kind: chstore.RuleTargetDBStatement, DBSystem: "oracle", DBName: "CRD", StmtHash: "42", Sample: "SELECT * FROM x WHERE id = ?"}
	if s, k := targetSubject(tg, []string{"svc"}); k != chstore.ProblemKindDB || s == "" {
		t.Errorf("db öznesi: %s %s", s, k)
	}
	if s, k := targetSubject(chstore.RuleTarget{StmtHash: "42"}, []string{"svc"}); k != chstore.ProblemKindService || s != "svc" {
		t.Errorf("servis öznesi: %s %s", s, k)
	}
	if s, _ := targetSubject(chstore.RuleTarget{StmtHash: "42"}, nil); s != "db-statement:42" {
		t.Errorf("boş özne: %s", s)
	}
	r := chstore.AlertRule{Name: "Slow offer SQL", Metric: "db_stmt_p95_ms", Comparator: ">", Threshold: 1000, WindowSec: 600, Target: &tg}
	d := describeTargetProblem(r, chstore.StatementWindowStats{Count: 37, Services: []string{"a", "b"}}, 1420)
	for _, w := range []string{"Slow offer SQL", "p95 1.42 s", "> 1.00 s", "600s window", "37 executions", "callers: a, b", "SELECT * FROM x"} {
		if !strings.Contains(d, w) {
			t.Errorf("gerekçede %q yok: %s", w, d)
		}
	}
}

func TestEvaluateAllDispatchesTargetRules(t *testing.T) {
	src := readEvaluatorSource(t)
	if !strings.Contains(src, "if r.Target != nil {") || !strings.Contains(src, "e.evaluateTargetRule(ctx, r, openSnap)") {
		t.Error("evaluateAll hedefli kuralı ayrı dala göndermiyor")
	}
}

func readEvaluatorSource(t *testing.T) string {
	t.Helper()
	b, err := os.ReadFile("evaluator.go")
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}
