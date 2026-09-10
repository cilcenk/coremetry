package chstore

import "testing"

// v0.10.584 — paylaşılan gövdenin kendi tablosu; Histogram bunu çağırır,
// CH olmadan Histogram test edilemez, o yüzden gövde burada pinli.
func TestLogTraceIDsConjunct(t *testing.T) {
	cases := []struct {
		name     string
		ids      []string
		wantExpr string
		wantArgs []any
	}{
		{"boş → hiç", nil, "", nil},
		{"yalnız boşluk atılır", []string{"  ", ""}, "", nil},
		{"normalize + sıra korunur", []string{" ABC ", "def"}, "trace_id IN (?,?)", []any{"abc", "def"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			expr, args := LogTraceIDsConjunct(tc.ids)
			if expr != tc.wantExpr {
				t.Fatalf("expr = %q, beklenen %q", expr, tc.wantExpr)
			}
			if len(args) != len(tc.wantArgs) {
				t.Fatalf("args = %v, beklenen %v", args, tc.wantArgs)
			}
			for i := range args {
				if args[i] != tc.wantArgs[i] {
					t.Fatalf("args[%d] = %v, beklenen %v", i, args[i], tc.wantArgs[i])
				}
			}
		})
	}
}
