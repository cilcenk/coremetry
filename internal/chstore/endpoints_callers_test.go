package chstore

import "testing"

// endpoints_callers_test.go — v0.9.839. Table-driven cover of
// foldEndpointCallers, the pure half of the /endpoint page's "Who calls
// this" panel.
//
// What it defends. The fold is where three DIFFERENT "no caller shown"
// answers get separated, and collapsing any pair of them would be a
// silent lie of exactly the kind this codebase keeps re-learning:
//
//   • DirectEntries — the route has no parent span: entered from
//     outside the traced system. A real, informative answer.
//   • Unresolved — there IS a parent id but no parent span in the
//     window (uninstrumented caller, sampled away, aged out). "We
//     cannot see it", not "there is none".
//   • a caller row — we saw the parent and know its service.
//
// It also pins the ranking choice (share of time, not call count), the
// error-rate denominator (the caller's own calls, not the sample), and
// that TotalMs counts EVERY sampled entry span — including the direct
// and unresolved ones, because they really did consume that time.

func TestFoldEndpointCallers(t *testing.T) {
	tests := []struct {
		name      string
		samples   []endpointCallerSample
		parentSvc map[string]string
		limit     int
		want      func(t *testing.T, got *EndpointCallers)
	}{
		{
			name: "ranks by time share, not call count",
			samples: []endpointCallerSample{
				{ParentID: "a1", DurMs: 5}, {ParentID: "a1", DurMs: 5},
				{ParentID: "a1", DurMs: 5}, {ParentID: "a1", DurMs: 5},
				{ParentID: "b1", DurMs: 200},
			},
			parentSvc: map[string]string{"a1": "chatty", "b1": "heavy"},
			limit:     10,
			want: func(t *testing.T, got *EndpointCallers) {
				if len(got.Callers) != 2 {
					t.Fatalf("callers = %d, want 2", len(got.Callers))
				}
				if got.Callers[0].Service != "heavy" {
					t.Errorf("top caller = %q, want heavy (200ms beats 4×5ms)",
						got.Callers[0].Service)
				}
				if got.Callers[0].Calls != 1 || got.Callers[1].Calls != 4 {
					t.Errorf("calls = %d/%d, want 1/4",
						got.Callers[0].Calls, got.Callers[1].Calls)
				}
				if pct := got.Callers[0].SharePct; pct < 90 || pct > 91 {
					t.Errorf("heavy share = %.2f%%, want ~90.9%%", pct)
				}
			},
		},
		{
			name: "error rate is per-caller, not per-sample",
			samples: []endpointCallerSample{
				{ParentID: "a1", DurMs: 10, IsErr: true},
				{ParentID: "a1", DurMs: 10, IsErr: true},
				{ParentID: "a1", DurMs: 10},
				{ParentID: "a1", DurMs: 10},
				{ParentID: "b1", DurMs: 10},
			},
			parentSvc: map[string]string{"a1": "flaky", "b1": "clean"},
			limit:     10,
			want: func(t *testing.T, got *EndpointCallers) {
				byName := map[string]EndpointCaller{}
				for _, c := range got.Callers {
					byName[c.Service] = c
				}
				if r := byName["flaky"].ErrorRate; r != 50 {
					t.Errorf("flaky error rate = %.1f, want 50 (2 of ITS 4, not 2 of 5)", r)
				}
				if r := byName["clean"].ErrorRate; r != 0 {
					t.Errorf("clean error rate = %.1f, want 0", r)
				}
			},
		},
		{
			name: "parentless entries are direct, never a caller",
			samples: []endpointCallerSample{
				{ParentID: "", DurMs: 10},
				{ParentID: emptySpanID, DurMs: 10},
				{ParentID: "a1", DurMs: 10},
			},
			parentSvc: map[string]string{"a1": "gateway"},
			limit:     10,
			want: func(t *testing.T, got *EndpointCallers) {
				if got.DirectEntries != 2 {
					t.Errorf("directEntries = %d, want 2 (both '' and the all-zero id)",
						got.DirectEntries)
				}
				if got.Unresolved != 0 {
					t.Errorf("unresolved = %d, want 0 — a parentless span is not unresolved",
						got.Unresolved)
				}
				if len(got.Callers) != 1 || got.Callers[0].Service != "gateway" {
					t.Errorf("callers = %+v, want only gateway", got.Callers)
				}
			},
		},
		{
			name: "unknown parent is unresolved, never dropped silently",
			samples: []endpointCallerSample{
				{ParentID: "ghost", DurMs: 10},
				{ParentID: "ghost2", DurMs: 10},
				{ParentID: "a1", DurMs: 10},
			},
			parentSvc: map[string]string{"a1": "gateway"},
			limit:     10,
			want: func(t *testing.T, got *EndpointCallers) {
				if got.Unresolved != 2 {
					t.Errorf("unresolved = %d, want 2", got.Unresolved)
				}
				if got.DirectEntries != 0 {
					t.Errorf("directEntries = %d, want 0 — an unseen parent is not 'no parent'",
						got.DirectEntries)
				}
			},
		},
		{
			name: "totalMs counts every sampled span, caller or not",
			samples: []endpointCallerSample{
				{ParentID: "", DurMs: 100},
				{ParentID: "ghost", DurMs: 100},
				{ParentID: "a1", DurMs: 100},
			},
			parentSvc: map[string]string{"a1": "gateway"},
			limit:     10,
			want: func(t *testing.T, got *EndpointCallers) {
				if got.TotalMs != 300 {
					t.Errorf("totalMs = %.0f, want 300", got.TotalMs)
				}
				// The one visible caller therefore owns a THIRD of the
				// window's time, and the panel can say so honestly.
				if pct := got.Callers[0].SharePct; pct < 33 || pct > 34 {
					t.Errorf("gateway share = %.2f%%, want ~33.3%%", pct)
				}
			},
		},
		{
			name: "limit truncates the tail, keeps the top",
			samples: []endpointCallerSample{
				{ParentID: "a", DurMs: 30}, {ParentID: "b", DurMs: 20},
				{ParentID: "c", DurMs: 10},
			},
			parentSvc: map[string]string{"a": "big", "b": "mid", "c": "small"},
			limit:     2,
			want: func(t *testing.T, got *EndpointCallers) {
				if len(got.Callers) != 2 {
					t.Fatalf("callers = %d, want 2", len(got.Callers))
				}
				if got.Callers[0].Service != "big" || got.Callers[1].Service != "mid" {
					t.Errorf("kept %q/%q, want big/mid",
						got.Callers[0].Service, got.Callers[1].Service)
				}
			},
		},
		{
			name:      "empty sample yields an empty, non-nil list",
			samples:   nil,
			parentSvc: map[string]string{},
			limit:     10,
			want: func(t *testing.T, got *EndpointCallers) {
				if got.Callers == nil {
					t.Error("callers is nil — JSON would emit null, the UI's empty state expects []")
				}
				if len(got.Callers) != 0 || got.TotalMs != 0 {
					t.Errorf("got %+v, want zero-valued", got)
				}
			},
		},
		{
			name: "equal shares sort by name, deterministically",
			samples: []endpointCallerSample{
				{ParentID: "z", DurMs: 10}, {ParentID: "a", DurMs: 10},
			},
			parentSvc: map[string]string{"z": "zeta", "a": "alpha"},
			limit:     10,
			want: func(t *testing.T, got *EndpointCallers) {
				if got.Callers[0].Service != "alpha" {
					t.Errorf("tie broke to %q, want alpha", got.Callers[0].Service)
				}
			},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			out := &EndpointCallers{Callers: []EndpointCaller{}}
			got := foldEndpointCallers(tc.samples, tc.parentSvc, tc.limit, out)
			tc.want(t, got)
		})
	}
}

// P95 is over the CALLER's own sampled durations — a caller's tail must
// not be diluted by another caller's fast calls.
func TestFoldEndpointCallersP95IsPerCaller(t *testing.T) {
	samples := []endpointCallerSample{}
	for i := 0; i < 99; i++ {
		samples = append(samples, endpointCallerSample{ParentID: "fast", DurMs: 1})
	}
	samples = append(samples,
		endpointCallerSample{ParentID: "slow", DurMs: 500},
		endpointCallerSample{ParentID: "slow", DurMs: 500},
	)
	out := foldEndpointCallers(samples, map[string]string{
		"fast": "fast-svc", "slow": "slow-svc",
	}, 10, &EndpointCallers{Callers: []EndpointCaller{}})
	for _, c := range out.Callers {
		if c.Service == "slow-svc" && c.P95Ms != 500 {
			t.Errorf("slow-svc p95 = %.0f, want 500", c.P95Ms)
		}
		if c.Service == "fast-svc" && c.P95Ms != 1 {
			t.Errorf("fast-svc p95 = %.0f, want 1", c.P95Ms)
		}
	}
}
