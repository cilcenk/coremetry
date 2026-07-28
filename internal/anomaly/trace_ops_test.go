package anomaly

import "testing"

// trace_ops_test.go — qualification thresholds for the per-operation error
// detector.
//
// v0.9.327 rewrote the table. Operator (prod): "Active anomalyler daha sıkı
// kuralları olsun, prodta çok daha az tetiklensin. Anomaly olmasa da şu an
// event oluşuyor."
//
// Measured before the change: the median firing event carried
// current_count = 3-4, minimum 3 — i.e. the floor itself was what fired. At
// prod volume three errors in five minutes is not an anomaly, and a "12×
// spike" over a baseline of a quarter-error is small-number arithmetic
// wearing a big multiplier.
//
// Three floors now, all in classifyTraceOps and all mirrored in the SQL
// coarse filter so the LIMIT ranks rows that can actually qualify:
//
//	traceOpMinErrs     = 10    absolute count
//	traceOpMinRatio    = 3.0   over the window-normalized baseline
//	traceOpMinErrShare = 0.01  errors as a share of CALLS  ← was missing entirely
//
// The share floor is the important one: the detector qualified on error COUNT
// alone and never looked at the denominator, so 3 errors out of 500,000 calls
// opened an event exactly like 3 errors out of 3. 1% is not invented here —
// it is the same floor anomaly.go's metricPolicies already applies to
// error_rate ("<%1 = birkaç-hata gürültüsü, açma").

func TestClassifyTraceOpsThresholds(t *testing.T) {
	const wr = 1.0 / 12.0 // 5-min current window over a 1-hour baseline
	cases := []struct {
		name     string
		in       traceOpBucket
		wantKind string // "" = kalifiye değil
		wantBase uint64
	}{
		// ── absolute count floor ─────────────────────────────────────────
		// The old floor was 3, and 3 is exactly what prod was firing on.
		{"eski taban artık yetmiyor: base=0 cur=3",
			traceOpBucket{Service: "s", Operation: "op", CurErrs: 3, CurCalls: 10}, "", 0},
		{"tabanın hemen altı: cur=9",
			traceOpBucket{Service: "s", Operation: "op", CurErrs: 9, CurCalls: 50}, "", 0},
		{"yeni taban: base=0 cur=10, 50 çağrının %20'si",
			traceOpBucket{Service: "s", Operation: "op", CurErrs: 10, CurCalls: 50}, "new_error", 0},

		// ── share floor: the rule that did not exist ─────────────────────
		// Same error count, three orders of magnitude of traffic between
		// them. Only the one that is a real share of the calls qualifies.
		{"500 bin çağrıda 40 hata (%0.008) — anomali değil",
			traceOpBucket{Service: "s", Operation: "op", CurErrs: 40, CurCalls: 500_000}, "", 0},
		{"4 bin çağrıda 40 hata (%1) — tam eşikte, kalifiye",
			traceOpBucket{Service: "s", Operation: "op", CurErrs: 40, CurCalls: 4_000}, "new_error", 0},
		{"400 çağrıda 40 hata (%10) — kalifiye",
			traceOpBucket{Service: "s", Operation: "op", CurErrs: 40, CurCalls: 400}, "new_error", 0},
		// An unknown denominator is not evidence of a high rate: refuse
		// rather than divide by zero into +Inf and qualify everything.
		{"payda yok (MV çağrı sayısı vermedi) — bölme, reddet",
			traceOpBucket{Service: "s", Operation: "op", CurErrs: 40, CurCalls: 0}, "", 0},

		// ── ratio floor over a real baseline ─────────────────────────────
		// base 120 over the hour → 10 per 5-min window.
		{"2.5× artık yetmiyor: base 120→10, cur 25",
			traceOpBucket{Service: "s", Operation: "op", CurErrs: 25, BaseErrs: 120, CurCalls: 200}, "", 10},
		{"3.1×: base 120→10, cur 31",
			traceOpBucket{Service: "s", Operation: "op", CurErrs: 31, BaseErrs: 120, CurCalls: 200}, "error_spike", 10},
		// A high ratio does NOT rescue a low share — both must hold.
		{"10× ama 200 binde 100 hata (%0.05) — kalifiye değil",
			traceOpBucket{Service: "s", Operation: "op", CurErrs: 100, BaseErrs: 120, CurCalls: 200_000}, "", 10},

		// ── sparse baseline branch ───────────────────────────────────────
		// base 5 over the hour normalizes to 0 per window; the branch
		// divides by the un-rounded value instead.
		{"seyrek baseline: base=5 cur=12 → 12/(5/12)=28.8, kalifiye",
			traceOpBucket{Service: "s", Operation: "op", CurErrs: 12, BaseErrs: 5, CurCalls: 100}, "error_spike", 0},
		{"seyrek baseline ama sayı tabanı altında: base=5 cur=3",
			traceOpBucket{Service: "s", Operation: "op", CurErrs: 3, BaseErrs: 5, CurCalls: 10}, "", 0},

		{"cur=0 hiç girmez",
			traceOpBucket{Service: "s", Operation: "op", CurErrs: 0, BaseErrs: 100, CurCalls: 1000}, "", 0},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := classifyTraceOps([]traceOpBucket{c.in}, wr)
			if c.wantKind == "" {
				if len(got) != 0 {
					t.Fatalf("kalifiye olmamalıydı: %+v", got)
				}
				return
			}
			if len(got) != 1 || got[0].Kind != c.wantKind {
				t.Fatalf("kind=%s bekleniyordu, got=%+v", c.wantKind, got)
			}
			if got[0].BaselineErrors != c.wantBase {
				t.Fatalf("BaselineErrors=%d bekleniyordu, got=%d", c.wantBase, got[0].BaselineErrors)
			}
			// The row must carry its own denominator so the operator reads
			// "40 of 400" instead of a bare count they have to go look up.
			if got[0].CurrentCalls != c.in.CurCalls {
				t.Fatalf("CurrentCalls=%d bekleniyordu, got=%d", c.in.CurCalls, got[0].CurrentCalls)
			}
			wantShare := float64(c.in.CurErrs) / float64(c.in.CurCalls)
			if got[0].ErrorShare != wantShare {
				t.Fatalf("ErrorShare=%v bekleniyordu, got=%v", wantShare, got[0].ErrorShare)
			}
		})
	}
}

// The measured prod shape must now be REJECTED. This is the regression that
// matters: it is the exact profile of what was firing (median cur 3-4 with a
// large-looking ratio), and it is what the operator asked to stop seeing.
func TestClassifyTraceOpsRejectsTheMeasuredNoiseProfile(t *testing.T) {
	const wr = 1.0 / 12.0
	noise := []traceOpBucket{
		{Service: "sanctions-screening-v2", Operation: "Screen", CurErrs: 3, BaseErrs: 0, CurCalls: 4_100},
		{Service: "sms-gateway", Operation: "Send", CurErrs: 4, BaseErrs: 1, CurCalls: 12_000},
		{Service: "web-bff", Operation: "Get", CurErrs: 3, BaseErrs: 2, CurCalls: 88_000},
	}
	if got := classifyTraceOps(noise, wr); len(got) != 0 {
		t.Fatalf("ölçülen gürültü profili hâlâ event açıyor: %+v", got)
	}
}

func TestClassifyTraceOpsOrderAndCap(t *testing.T) {
	const wr = 1.0 / 12.0
	rows := []traceOpBucket{
		{Service: "s", Operation: "spike-small", CurErrs: 40, BaseErrs: 120, CurCalls: 200},  // ratio 4
		{Service: "s", Operation: "new-1", CurErrs: 10, BaseErrs: 0, CurCalls: 100},          // ratio 10
		{Service: "s", Operation: "spike-big", CurErrs: 200, BaseErrs: 120, CurCalls: 1_000}, // ratio 20
		{Service: "s", Operation: "new-2", CurErrs: 90, BaseErrs: 0, CurCalls: 500},          // ratio 90
	}
	got := classifyTraceOps(rows, wr)
	// Kind FIRST (a brand-new error outranks any spike), then ratio — so
	// spike-big's 20× still sits below new-1's 10×. Non-obvious, hence pinned.
	wantOrder := []string{"new-2", "new-1", "spike-big", "spike-small"}
	if len(got) != 4 {
		t.Fatalf("4 sonuç bekleniyordu: %+v", got)
	}
	for i, w := range wantOrder {
		if got[i].Operation != w {
			t.Fatalf("sıra[%d]=%s bekleniyordu, got=%s", i, w, got[i].Operation)
		}
	}

	// 50 tavanı — eski SQL'in LIMIT 50'siyle aynı sözleşme.
	many := make([]traceOpBucket, 0, 80)
	for i := 0; i < 80; i++ {
		many = append(many, traceOpBucket{
			Service: "s", Operation: "op",
			CurErrs: uint64(traceOpMinErrs + i), CurCalls: 200,
		})
	}
	if got := classifyTraceOps(many, wr); len(got) != 50 {
		t.Fatalf("50 tavanı uygulanmalıydı, got=%d", len(got))
	}
}
