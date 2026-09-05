package anomaly

// v0.10.374 — VM dilim 3c: the saturation evidence reads JVM heap / GC
// through chstore.RuntimePodReader (VictoriaMetrics when configured),
// never *chstore.Store directly. Source pin — the "tested but
// unreachable" class: a VM reader that exists and is never called.

import (
	"os"
	"strings"
	"testing"

	"github.com/cilcenk/coremetry/internal/chstore"
)

func TestDeepEvidenceReadsJVMPodsThroughReader(t *testing.T) {
	b, err := os.ReadFile("investigation.go")
	if err != nil {
		t.Fatal(err)
	}
	src := string(b)
	for _, want := range []string{"pods := runtimePodsOrStore(rt, store)", "pods.JVMHeapPodUsage(ctx, from, to)", "pods.JVMGCPodPause(ctx, from, to)"} {
		if !strings.Contains(src, want) {
			t.Fatalf("investigation.go must contain %q", want)
		}
	}
	for _, stale := range []string{"store.JVMHeapPodUsage(", "store.JVMGCPodPause("} {
		if strings.Contains(src, stale) {
			t.Fatalf("saturation evidence still pinned to ClickHouse via %q", stale)
		}
	}
	w, err := os.ReadFile("rootcause_worker.go")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(w), "gatherDeepEvidence(ctx, s.store, s.runtimePods, p, plan") {
		t.Fatal("root-cause worker must pass its runtime pod reader")
	}
	// nil reader → store, so an un-wired caller still reads ClickHouse.
	var st *chstore.Store
	if got := runtimePodsOrStore(nil, st); got != chstore.RuntimePodReader(st) {
		t.Fatal("nil reader must fall back to the store")
	}
}
