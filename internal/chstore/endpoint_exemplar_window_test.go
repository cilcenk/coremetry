// endpoint_exemplar_window_test.go — v0.8.535 regression.
//
// EndpointExemplars read spanmetrics_1m with the RAW q.From. The MV keys
// on time_bucket (the minute FLOOR), so `time_bucket >= q.From` with a
// sub-minute From dropped the whole bucket the window opens inside — the
// slowest trace of that first partial minute was invisible and the
// drawer's "slowest →" resolved to the runner-up. parseFromTo never
// snaps (the minute bucket is CACHE-KEY only), so every real request
// carried a sub-minute From.
//
// Live 2-shard cluster, to=now, 30 probes per window width:
//
//	~5m window : wrong trace_id 7/30 before → 1/30 after
//	~15m, ~1h  : 0/30 before and after (loss scales inversely with width)
//
// These tests pin BOTH halves of the contract: From is floored to the MV
// grain, and To is deliberately NOT touched (ceiling it would admit a
// bucket wholly past the window).
package chstore

import (
	"testing"
	"time"
)

func TestEndpointExemplarArgs_FromFlooredToMVGrain(t *testing.T) {
	// Every case shares one expected floor so a broken Truncate shows up
	// as a value diff, not a case-by-case rewrite.
	want := time.Date(2026, 7, 15, 9, 5, 0, 0, time.UTC)

	cases := []struct {
		name string
		from time.Time
	}{
		{"exactly on the minute", time.Date(2026, 7, 15, 9, 5, 0, 0, time.UTC)},
		{"one nanosecond past", time.Date(2026, 7, 15, 9, 5, 0, 1, time.UTC)},
		{"mid-minute :30", time.Date(2026, 7, 15, 9, 5, 30, 0, time.UTC)},
		{"last nanosecond", time.Date(2026, 7, 15, 9, 5, 59, 999999999, time.UTC)},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, args := endpointExemplarArgs(EndpointDetailQuery{
				Service: "api-gateway", Path: "/api/v1/cards/authorize",
				From: c.from, To: c.from.Add(5 * time.Minute),
			})
			got, ok := args[0].(time.Time)
			if !ok {
				t.Fatalf("args[0] is %T, want time.Time", args[0])
			}
			if !got.Equal(want) {
				t.Errorf("From = %v, want %v (floored to the spanmetrics_1m grain)", got, want)
			}
		})
	}
}

func TestEndpointExemplarArgs_ToLeftRaw(t *testing.T) {
	// Ceiling To would pull in a bucket lying wholly past the window;
	// flooring it changes nothing. Raw is the deliberate posture.
	to := time.Date(2026, 7, 15, 9, 10, 42, 500000000, time.UTC)
	_, args := endpointExemplarArgs(EndpointDetailQuery{
		Service: "api-gateway", Path: "/x",
		From: time.Date(2026, 7, 15, 9, 5, 30, 0, time.UTC), To: to,
	})
	got, ok := args[1].(time.Time)
	if !ok {
		t.Fatalf("args[1] is %T, want time.Time", args[1])
	}
	if !got.Equal(to) {
		t.Errorf("To = %v, want %v untouched", got, to)
	}
}

func TestEndpointExemplarArgs_Order(t *testing.T) {
	// The SQL binds ? in this order: from, to, service, [opSig×3], path.
	// A silent reorder swaps service with path and the read returns
	// nothing — the failure mode the exemplar ingest hit in v0.8.435.
	from := time.Date(2026, 7, 15, 9, 5, 30, 0, time.UTC)
	to := from.Add(time.Hour)

	t.Run("raw route", func(t *testing.T) {
		proj, args := endpointExemplarArgs(EndpointDetailQuery{
			Service: "svc-a", Path: "/orders", From: from, To: to,
		})
		if proj != "http_route" {
			t.Errorf("pathProj = %q, want plain http_route", proj)
		}
		if len(args) != 4 {
			t.Fatalf("len(args) = %d, want 4", len(args))
		}
		if args[2] != "svc-a" {
			t.Errorf("args[2] = %v, want the service", args[2])
		}
		if args[3] != "/orders" {
			t.Errorf("args[3] = %v, want the path", args[3])
		}
	})

	t.Run("signature mode inserts opSig args between service and path", func(t *testing.T) {
		proj, args := endpointExemplarArgs(EndpointDetailQuery{
			Service: "svc-a", Path: "/orders/:id", BySignature: true, From: from, To: to,
		})
		if proj == "http_route" {
			t.Error("pathProj must be the opSigWrap expansion in signature mode")
		}
		sig := opSigArgs()
		if len(args) != 4+len(sig) {
			t.Fatalf("len(args) = %d, want %d", len(args), 4+len(sig))
		}
		if args[2] != "svc-a" {
			t.Errorf("args[2] = %v, want the service", args[2])
		}
		for i, w := range sig {
			if args[3+i] != w {
				t.Errorf("args[%d] = %v, want opSigArgs()[%d] = %v", 3+i, args[3+i], i, w)
			}
		}
		if args[len(args)-1] != "/orders/:id" {
			t.Errorf("last arg = %v, want the path", args[len(args)-1])
		}
	})
}
