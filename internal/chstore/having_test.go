package chstore

// v0.8.453 (B2-c) — compileHaving whitelist derleyicisi. SQL'e yalnız
// kolon haritasından girilir; değerler daima bind parametresi.

import (
	"reflect"
	"testing"
)

func TestCompileHaving(t *testing.T) {
	cases := []struct {
		name    string
		in      []HavingExpr
		wantSQL string
		wantN   int
		wantErr bool
	}{
		{name: "boş → boş", in: nil, wantSQL: "", wantN: 0},
		{
			name:    "tek koşul",
			in:      []HavingExpr{{Metric: "errorRate", Op: ">", Value: 1}},
			wantSQL: " AND error_rate > ?", wantN: 1,
		},
		{
			name: "çoklu AND — operatör isteği: errorRate>1 VE p95>500",
			in: []HavingExpr{
				{Metric: "errorRate", Op: ">", Value: 1},
				{Metric: "p95", Op: ">", Value: 500},
			},
			wantSQL: " AND error_rate > ? AND p95_ms > ?", wantN: 2,
		},
		{
			name:    "tüm metrikler geçerli",
			in: []HavingExpr{
				{Metric: "count", Op: ">=", Value: 10},
				{Metric: "perMin", Op: "<", Value: 100},
				{Metric: "avg", Op: "<=", Value: 250},
				{Metric: "p50", Op: ">", Value: 1},
				{Metric: "p99", Op: ">", Value: 900},
				{Metric: "max", Op: ">", Value: 5000},
			},
			wantSQL: " AND trace_count >= ? AND per_min < ? AND avg_ms <= ? AND p50_ms > ? AND p99_ms > ? AND max_ms > ?",
			wantN:   6,
		},
		{name: "bilinmeyen metrik", in: []HavingExpr{{Metric: "trace_count; DROP TABLE spans", Op: ">", Value: 1}}, wantErr: true},
		{name: "bilinmeyen operatör", in: []HavingExpr{{Metric: "count", Op: "=1 OR 1", Value: 1}}, wantErr: true},
		{name: "koşul tavanı aşımı", in: make([]HavingExpr, maxHavingExprs+1), wantErr: true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			sql, args, err := compileHaving(c.in)
			if c.wantErr {
				if err == nil {
					t.Fatalf("hata beklenirdi, sql=%q", sql)
				}
				return
			}
			if err != nil {
				t.Fatalf("beklenmeyen hata: %v", err)
			}
			if sql != c.wantSQL {
				t.Fatalf("sql: %q != %q", sql, c.wantSQL)
			}
			if len(args) != c.wantN {
				t.Fatalf("args: %d != %d", len(args), c.wantN)
			}
			// Değerler sırayla bind edilir.
			want := make([]any, 0, len(c.in))
			for _, h := range c.in {
				want = append(want, h.Value)
			}
			if c.wantN > 0 && !reflect.DeepEqual(args, want) {
				t.Fatalf("args sırası: %v != %v", args, want)
			}
		})
	}
}
