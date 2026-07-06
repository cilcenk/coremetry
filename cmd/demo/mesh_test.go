package main

import (
	"testing"

	tracepb "go.opentelemetry.io/proto/otlp/trace/v1"
)

// Table-driven mesh generator contract (v0.8.326). buildMeshTraceRoll walks a
// chainSpec and promises:
//   - a root hop emits exactly ONE span (SERVER or CONSUMER); a nested
//     http/grpc hop emits a CLIENT+SERVER pair; db-*/redis/ext/kafka-* hops
//     emit exactly one span each — so the span count of a spec is computable
//     from the table alone;
//   - every span's parent exists in the same trace, and there is exactly one
//     parentless root;
//   - kafka-pub children (async consumer continuations) are parented under the
//     PRODUCER span and start AFTER it ends, like scenarioTransferEvent;
//   - parallel (par) siblings share their start offset;
//   - no span has a zero duration — every hop's latency flows through dur();
//   - a failed hop keeps its own span(s) with status ERROR and SKIPS its
//     children (downstream skip), while its ancestors stay OK (degraded).
//
// Every service a chain names must exist in the `services` map, or pickPod
// fabricates a pod and the resource attributes silently degrade — the same
// silent-degrade trap the keyed realism maps have.

// neverFail / alwaysFail let the tests pin the failure branch without
// depending on rollFail's randomness (the injected-roll seam).
func neverFail(int) bool  { return false }
func alwaysFail(int) bool { return true }

// wantSpans mirrors the span-count contract above: 1 for a root hop, 2 for a
// nested http/grpc hop, 1 for everything else, plus children.
func wantSpans(h *chainHop, root bool) int {
	n := 1
	if !root && (h.proto == "http" || h.proto == "grpc") {
		n = 2
	}
	for i := range h.kids {
		n += wantSpans(&h.kids[i], false)
	}
	return n
}

func chainByName(t *testing.T, name string) *chainSpec {
	t.Helper()
	for i := range meshChains {
		if meshChains[i].name == name {
			return &meshChains[i]
		}
	}
	t.Fatalf("chain %q not registered in meshChains", name)
	return nil
}

func TestMeshChainShapes(t *testing.T) {
	// 8 slice-1 chains + 8 slice-2 chains (EOD batch, CDC, TPP AISP/PISP,
	// webhook fan-out, nightly archive, partner portal, platform maintenance).
	if len(meshChains) < 16 {
		t.Fatalf("meshChains has %d specs, want at least 16", len(meshChains))
	}
	for i := range meshChains {
		spec := &meshChains[i]
		t.Run(spec.name, func(t *testing.T) {
			tr := buildMeshTraceRoll(spec, neverFail)
			if tr == nil {
				t.Fatal("buildMeshTraceRoll returned nil trace")
			}
			if want := wantSpans(&spec.root, true); len(tr.spans) != want {
				t.Fatalf("span count = %d, want %d", len(tr.spans), want)
			}
			byID := map[string]bool{}
			roots := 0
			for _, si := range tr.spans {
				if _, ok := services[si.service]; !ok {
					t.Errorf("span %q emitted by unregistered service %q", si.span.Name, si.service)
				}
				if si.span.EndTimeUnixNano <= si.span.StartTimeUnixNano {
					t.Errorf("span %q has zero/negative duration", si.span.Name)
				}
				byID[string(si.span.SpanId)] = true
				if len(si.span.ParentSpanId) == 0 {
					roots++
				}
			}
			if roots != 1 {
				t.Errorf("trace has %d parentless roots, want exactly 1", roots)
			}
			for _, si := range tr.spans {
				if p := si.span.ParentSpanId; len(p) > 0 && !byID[string(p)] {
					t.Errorf("span %q parent %x not in trace", si.span.Name, p)
				}
			}
		})
	}
}

// TestMeshKafkaContinuation pins the async continuation shape: the fast-payment
// chain publishes payment.fast.settled and TWO independent consumer groups
// (payment-status-tracker + reconciliation-service) pick it up, each parented
// under the producer span and starting only after the publish completes.
func TestMeshKafkaContinuation(t *testing.T) {
	spec := chainByName(t, "MeshFastPayment")
	tr := buildMeshTraceRoll(spec, neverFail)

	var prod *tracepb.Span
	var consumers []*tracepb.Span
	for _, si := range tr.spans {
		switch si.span.Kind {
		case tracepb.Span_SPAN_KIND_PRODUCER:
			prod = si.span
		case tracepb.Span_SPAN_KIND_CONSUMER:
			consumers = append(consumers, si.span)
		}
	}
	if prod == nil {
		t.Fatal("no PRODUCER span in MeshFastPayment")
	}
	if len(consumers) != 2 {
		t.Fatalf("got %d CONSUMER spans, want 2 (status-tracker + reconciliation)", len(consumers))
	}
	for _, c := range consumers {
		if string(c.ParentSpanId) != string(prod.SpanId) {
			t.Errorf("consumer %q not parented under the producer span", c.Name)
		}
		if c.StartTimeUnixNano < prod.EndTimeUnixNano {
			t.Errorf("consumer %q starts before the publish finished (sync, not async)", c.Name)
		}
	}
}

// TestMeshParallelFanOut pins that par siblings genuinely fan out: the
// pricing-engine hop resolves its Redis rate cache and the forex-service rate
// call concurrently, so both CLIENT spans share a start timestamp.
func TestMeshParallelFanOut(t *testing.T) {
	spec := chainByName(t, "MeshProductQuote")
	tr := buildMeshTraceRoll(spec, neverFail)

	var redis, forex *tracepb.Span
	for _, si := range tr.spans {
		switch si.span.Name {
		case "redis.GET rates:{ccy}":
			redis = si.span
		case "forex-service/GetRates":
			forex = si.span
		}
	}
	if redis == nil || forex == nil {
		t.Fatalf("fan-out spans missing: redis=%v forex=%v", redis != nil, forex != nil)
	}
	if redis.StartTimeUnixNano != forex.StartTimeUnixNano {
		t.Errorf("parallel siblings start at %d vs %d, want identical offsets",
			redis.StartTimeUnixNano, forex.StartTimeUnixNano)
	}
	if string(redis.ParentSpanId) != string(forex.ParentSpanId) {
		t.Error("parallel siblings must share the same parent span")
	}
}

// TestMeshFailureSkipsDownstream uses a minimal local spec so the count is
// hand-verifiable: root(1) + grpc pair(2) + redis(1) = 4 when nothing fails;
// when the grpc hop fails both its spans go ERROR, the redis child is skipped
// (3 spans) and the root stays OK — degraded, not cascaded.
func TestMeshFailureSkipsDownstream(t *testing.T) {
	spec := &chainSpec{name: "test", root: chainHop{
		svc: "web-bff", proto: "http", op: "GET /t", minMs: 10, maxMs: 20,
		kids: []chainHop{
			{svc: "session-gateway", proto: "grpc", op: "Establish", minMs: 5, maxMs: 10,
				failPct: 50, errMsg: "boom",
				kids: []chainHop{
					{svc: "session-gateway", proto: "redis", op: "GET", table: "k", minMs: 1, maxMs: 3},
				}},
		},
	}}

	ok := buildMeshTraceRoll(spec, neverFail)
	if len(ok.spans) != 4 {
		t.Fatalf("healthy build: %d spans, want 4", len(ok.spans))
	}
	for _, si := range ok.spans {
		if si.span.Status.Code != tracepb.Status_STATUS_CODE_OK {
			t.Errorf("healthy build: span %q not OK", si.span.Name)
		}
	}

	bad := buildMeshTraceRoll(spec, alwaysFail)
	if len(bad.spans) != 3 {
		t.Fatalf("failing build: %d spans, want 3 (redis child skipped)", len(bad.spans))
	}
	errored := 0
	for _, si := range bad.spans {
		if len(si.span.ParentSpanId) == 0 {
			if si.span.Status.Code != tracepb.Status_STATUS_CODE_OK {
				t.Errorf("root must stay OK when a downstream hop fails (degraded, not cascaded)")
			}
			continue
		}
		if si.span.Status.Code == tracepb.Status_STATUS_CODE_ERROR {
			if si.span.Status.Message != "boom" {
				t.Errorf("error span %q message = %q, want %q", si.span.Name, si.span.Status.Message, "boom")
			}
			errored++
		}
	}
	if errored != 2 {
		t.Errorf("failing build: %d ERROR spans, want 2 (client + server of the failed hop)", errored)
	}
}

// TestMeshServiceCoverage guards the silent-degrade traps: every mesh service
// must be reachable from at least one chain, registered in `services`, carry an
// explicit team assignment, and run a 2-4 pod fleet. The total service count
// pins that no mesh name silently OVERWROTE a base/bank_extra service via the
// init() map merge.
func TestMeshServiceCoverage(t *testing.T) {
	inChain := map[string]bool{}
	var walk func(h *chainHop)
	walk = func(h *chainHop) {
		inChain[h.svc] = true
		for i := range h.kids {
			walk(&h.kids[i])
		}
	}
	for i := range meshChains {
		walk(&meshChains[i].root)
	}

	// 30 slice-1 + 25 slice-2 (batch/ETL, open-banking, platform-ops).
	if len(meshServices) != 55 {
		t.Fatalf("meshServices has %d entries, want 55", len(meshServices))
	}
	for _, s := range meshServices {
		if !inChain[s.Name] {
			t.Errorf("%s: not referenced by any chainSpec", s.Name)
		}
		if _, ok := services[s.Name]; !ok {
			t.Errorf("%s: not registered in the services map", s.Name)
		}
		if _, ok := meshTeams[s.Name]; !ok {
			t.Errorf("%s: no meshTeams entry — teamsFor would fall into a substring bucket", s.Name)
		}
		if len(s.Pods) < 2 || len(s.Pods) > 4 {
			t.Errorf("%s: %d pods, want 2-4", s.Name, len(s.Pods))
		}
	}
	// 20 base + 25 bank_extra + 55 mesh: any name collision would merge two
	// entries and drop the total below 100.
	if len(services) < 100 {
		t.Errorf("services map has %d entries, want >= 100 — a mesh name collided with an existing service", len(services))
	}
}

// TestMeshWebhookParallelFanout pins the slice-2 headline shape: the webhook
// dispatcher delivers to THREE partner endpoints in parallel (plus a
// concurrent idempotency check), so all four CLIENT spans share the consumer
// span as parent and start at the same offset.
func TestMeshWebhookParallelFanout(t *testing.T) {
	spec := chainByName(t, "MeshWebhookFanout")
	tr := buildMeshTraceRoll(spec, neverFail)

	var root *tracepb.Span
	var kids []*tracepb.Span
	for _, si := range tr.spans {
		if si.span.Kind == tracepb.Span_SPAN_KIND_CONSUMER {
			root = si.span
		} else {
			kids = append(kids, si.span)
		}
	}
	if root == nil {
		t.Fatal("no CONSUMER root span in MeshWebhookFanout")
	}
	if len(kids) != 4 {
		t.Fatalf("got %d fan-out spans, want 4 (3 partner deliveries + idempotency check)", len(kids))
	}
	for _, k := range kids {
		if string(k.ParentSpanId) != string(root.SpanId) {
			t.Errorf("fan-out span %q not parented under the consumer root", k.Name)
		}
		if k.StartTimeUnixNano != kids[0].StartTimeUnixNano {
			t.Errorf("fan-out span %q starts at %d, want %d (parallel siblings share offsets)",
				k.Name, k.StartTimeUnixNano, kids[0].StartTimeUnixNano)
		}
	}
}
