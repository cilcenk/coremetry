package notify

import "testing"

// problem_url_test.go — v0.8.492 regression. Bildirim derin linki
// /anomalies?problem= üretiyordu; /anomalies rotası v0.8.219 sonrası
// anomaly-STREAM sayfası olduğundan (?problem='i yok sayar) PublicURL
// ayarlı her kurulumda link yanlış sayfaya düşüyordu. Doğru hedef
// /problems?problem= (api.go statusPageProblemURL ile aynı şekil).
func TestProblemURL(t *testing.T) {
	cases := []struct {
		name, base, id, want string
	}{
		{"unset base → empty (caller omits the line)", "", "p1", ""},
		{"points at /problems, not /anomalies",
			"https://coremetry.bank.local", "p1",
			"https://coremetry.bank.local/problems?problem=p1"},
		{"id is appended verbatim",
			"http://localhost:8088", "prob-42",
			"http://localhost:8088/problems?problem=prob-42"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			n := New(nil)
			n.SetPublicURL(c.base)
			if got := n.problemURL(c.id); got != c.want {
				t.Fatalf("problemURL(%q) = %q, want %q", c.id, got, c.want)
			}
		})
	}
}
