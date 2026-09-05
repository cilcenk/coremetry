package chstore

import (
	"os"
	"strings"
	"testing"
)

// v0.10.439 (D4) — kenar katlama saf: parent servisi ≠ child servisi olan
// span'ler kenar, aynı servis/parentsız span değil; sorgu sınırlı.
func TestFoldTraceEdges(t *testing.T) {
	rows := []traceEdgeRow{
		{"t1", "s1", "", "checkout"},
		{"t1", "s2", "s1", "payment"},
		{"t1", "s3", "s2", "ledger"},
		{"t1", "s4", "s2", "ledger"},
		{"t1", "s5", "s1", "checkout"}, // aynı servis: kenar değil
		{"t2", "s1", "", "checkout"},
		{"t2", "s2", "s1", "payment"},
		{"t3", "s9", "zz", "orphan"}, // parent yok
	}
	out := foldTraceEdges(rows)
	if out["t1"][TraceEdge{"checkout", "payment"}] != 1 || out["t1"][TraceEdge{"payment", "ledger"}] != 2 || len(out["t1"]) != 2 {
		t.Fatalf("t1: %+v", out["t1"])
	}
	if out["t2"][TraceEdge{"checkout", "payment"}] != 1 || len(out["t3"]) != 0 {
		t.Fatalf("t2/t3: %+v %+v", out["t2"], out["t3"])
	}
	src, err := os.ReadFile("trace_edges.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"trace_id IN (%s)", "time >= ? AND time <= ?", "LIMIT 400000", "max_execution_time = 25", "traceFetchPad"} {
		if !strings.Contains(string(src), want) {
			t.Errorf("sorgu %q içermeli", want)
		}
	}
}
