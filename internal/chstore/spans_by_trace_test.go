package chstore

// spans_by_trace_test.go — v0.10.229 (Influx D4): foldTraceSummaries.

import (
	"testing"
	"time"
)

func TestFoldTraceSummaries(t *testing.T) {
	t0 := time.Date(2026, 9, 2, 10, 0, 0, 0, time.UTC)
	rows := []traceSpanRow{
		// trace A: kök gateway (200ms), child db hatalı (150ms, en yavaş child), child cache
		{TraceID: "a", SpanID: "a1", ParentID: "", Service: "gateway", Name: "POST /pay", Status: "ok", Time: t0, DurationNs: 200e6},
		{TraceID: "a", SpanID: "a2", ParentID: "a1", Service: "db", Name: "SELECT", Status: "error", Time: t0.Add(10 * time.Millisecond), DurationNs: 150e6},
		{TraceID: "a", SpanID: "a3", ParentID: "a1", Service: "cache", Name: "GET", Status: "ok", Time: t0.Add(5 * time.Millisecond), DurationNs: 1e6},
		// trace B (daha yeni): kökü yok (parent trace dışında) → ilk span kök; süre min..max
		{TraceID: "b", SpanID: "b1", ParentID: "zz", Service: "worker", Name: "consume", Status: "error", Time: t0.Add(time.Minute), DurationNs: 50e6},
		{TraceID: "b", SpanID: "b2", ParentID: "b1", Service: "worker", Name: "handle", Status: "error", Time: t0.Add(time.Minute + 20*time.Millisecond), DurationNs: 60e6},
	}
	got := foldTraceSummaries(rows)
	if len(got) != 2 || got[0].TraceID != "b" || got[1].TraceID != "a" {
		t.Fatalf("newest first: %+v", got)
	}
	a := got[1]
	if a.RootService != "gateway" || a.RootOp != "POST /pay" || a.DurationNs != 200e6 || a.Spans != 3 || a.ErrorSpans != 1 {
		t.Fatalf("trace a summary: %+v", a)
	}
	if a.ErrorService != "db" || a.ErrorOp != "SELECT" || a.SlowestService != "gateway" {
		t.Fatalf("trace a error/slowest: %+v", a)
	}
	b := got[0]
	if b.RootService != "worker" || b.ErrorSpans != 2 || b.DurationNs != 50e6 {
		t.Fatalf("trace b: %+v", b)
	}
	if foldTraceSummaries(nil) != nil {
		t.Fatal("empty → nil")
	}
}
