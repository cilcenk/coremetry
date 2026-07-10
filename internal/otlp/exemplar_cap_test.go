package otlp

import (
	"testing"
	"time"

	"github.com/cilcenk/coremetry/internal/chstore"
)

// exemplar_cap_test.go — v0.8.433 (exemplar audit Faz C). The per-
// series×minute ingest cap: budget enforcement, per-series
// independence, minute rollover reset, and the Ingester gate's counter
// + accept-but-discard semantics (a capped drop must never reject the
// parent batch — same contract as the trace gate, v0.8.328/345).
func TestExemplarRateLimiter(t *testing.T) {
	base := time.Unix(1_699_999_980, 0) // minute-aligned
	l := newExemplarRateLimiter(2)

	if !l.allow(1, base) || !l.allow(1, base.Add(5*time.Second)) {
		t.Fatalf("first two within the budget must pass")
	}
	if l.allow(1, base.Add(10*time.Second)) {
		t.Fatalf("third in the same minute must be denied")
	}
	if !l.allow(2, base.Add(10*time.Second)) {
		t.Fatalf("a different series has its own budget")
	}
	if !l.allow(1, base.Add(61*time.Second)) {
		t.Fatalf("minute rollover must reset the budget")
	}
	// Rollover dropped the old map wholesale — series 2 is fresh too.
	if !l.allow(2, base.Add(61*time.Second)) {
		t.Fatalf("rollover must reset every series")
	}
}

func TestIngesterExemplarCapGate(t *testing.T) {
	ing := &Ingester{exemplarsNoTraceOK: true} // isolate the cap gate
	ing.SetExemplarCap(1)
	ex := &chstore.ExemplarRow{Fingerprint: 42, TraceID: "abc"}

	if ok := ing.addExemplar(ex); !ok {
		t.Fatalf("first exemplar must be accepted")
	}
	if ok := ing.addExemplar(ex); !ok {
		t.Fatalf("capped drop must still return true (accept-but-discard)")
	}
	if got := ing.ExemplarsDroppedCapped(); got != 1 {
		t.Fatalf("droppedCapped = %d, want 1", got)
	}
	if got := ing.ExemplarsIngested(); got != 1 {
		t.Fatalf("ingested = %d, want 1", got)
	}

	// n <= 0 disarms.
	ing2 := &Ingester{exemplarsNoTraceOK: true}
	ing2.SetExemplarCap(0)
	for i := 0; i < 5; i++ {
		ing2.addExemplar(ex)
	}
	if got := ing2.ExemplarsDroppedCapped(); got != 0 {
		t.Fatalf("unlimited default must never cap (got %d)", got)
	}
}
