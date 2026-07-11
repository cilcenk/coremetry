package anomaly

import "testing"

// trace_ops_test.go — v0.8.504 regression. Detektör raw-spans çift
// taramasından operation_summary_5m MV okumasına geçerken eşik/kind
// mantığı SQL'den classifyTraceOps'a ayrıldı; bu tablo eski SQL'in
// davranışını birebir sabitler (windowRatio = 5m/1h = 1/12 tipik).
func TestClassifyTraceOps(t *testing.T) {
	const wr = 1.0 / 12.0
	cases := []struct {
		name     string
		in       traceOpBucket
		wantKind string // "" = kalifiye değil
		wantBase uint64
	}{
		{"yeni hata: base=0 cur>=3", traceOpBucket{"s", "op", 3, 0}, "new_error", 0},
		{"yeni hata eşik altı: base=0 cur=2", traceOpBucket{"s", "op", 2, 0}, "", 0},
		{"spike: base 120→pencerede 10, cur 25 (2.5×)", traceOpBucket{"s", "op", 25, 120}, "error_spike", 10},
		{"spike eşik altı: base 120→10, cur 19", traceOpBucket{"s", "op", 19, 120}, "", 10},
		{"seyrek baseline: base=5 (pencerede 0'a yuvarlanır) cur=1 → 1/(5/12)=2.4 kalifiye", traceOpBucket{"s", "op", 1, 5}, "error_spike", 0},
		{"seyrek baseline eşik altı: base=30 (pencerede 2.5→2) cur=4 → 4/2=2 kalifiye", traceOpBucket{"s", "op", 4, 30}, "error_spike", 2},
		{"cur=0 hiç girmez", traceOpBucket{"s", "op", 0, 100}, "", 0},
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
		})
	}
}

func TestClassifyTraceOpsOrderAndCap(t *testing.T) {
	const wr = 1.0 / 12.0
	rows := []traceOpBucket{
		{"s", "spike-small", 25, 120},  // spike ratio 2.5
		{"s", "new-1", 3, 0},           // new_error ratio 3
		{"s", "spike-big", 100, 120},   // spike ratio 10
		{"s", "new-2", 9, 0},           // new_error ratio 9
	}
	got := classifyTraceOps(rows, wr)
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
		many = append(many, traceOpBucket{"s", "op", uint64(3 + i), 0})
	}
	if got := classifyTraceOps(many, wr); len(got) != 50 {
		t.Fatalf("50 tavanı uygulanmalıydı, got=%d", len(got))
	}
}
