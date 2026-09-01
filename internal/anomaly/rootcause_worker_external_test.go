package anomaly

// v0.10.229 (Influx D4, audit E2) — synthesizer external anchor'ı atlar;
// atlamasaydı tam-satır UpsertHypothesis dış kanıtı silerdi. Çağrı yeri
// de pinli: saf yardımcı yeşilken döngüde çağrılmıyorsa ölü kalır
// (feedback-tested-but-unreachable).

import (
	"os"
	"strings"
	"testing"

	"github.com/cilcenk/coremetry/internal/chstore"
)

func TestSynthesizerSkipsExternalProblems(t *testing.T) {
	if !synthesizerSkipsProblem(chstore.Problem{Kind: chstore.ProblemKindExternal, Service: "ext:ggfail/OP1/E1"}) {
		t.Fatal("external anchor must be skipped")
	}
	for _, k := range []string{"", chstore.ProblemKindService, chstore.ProblemKindDB} {
		if synthesizerSkipsProblem(chstore.Problem{Kind: k, Service: "svc"}) {
			t.Fatalf("kind %q must still be synthesized", k)
		}
	}
	src, err := os.ReadFile("rootcause_worker.go")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(src), "if synthesizerSkipsProblem(p) {") {
		t.Fatal("synthesizerSkipsProblem is not wired into the problem loop")
	}
}
