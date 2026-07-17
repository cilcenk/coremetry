package thanos

import (
	"encoding/json"
	"strings"
	"testing"
)

// v0.8.575 — PromQL builder + sample-decode contracts for the
// /clusters surface (audit §4). Table-driven per CLAUDE.md #11.

func TestPodQueriesCarryCardinalityShields(t *testing.T) {
	cases := []struct {
		name     string
		query    string
		wantSubs []string
	}{
		{"cpu with ns filter", podCPUQuery("^app-"), []string{
			`topk(500,`, `sum by (namespace, pod)`,
			`rate(container_cpu_usage_seconds_total{container!="",pod!="",namespace=~"^app-"}[5m])`,
		}},
		{"cpu without ns filter", podCPUQuery(""), []string{
			`topk(500,`, `container_cpu_usage_seconds_total{container!="",pod!=""}`,
		}},
		{"mem with ns filter", podMemQuery("payments"), []string{
			`topk(500,`, `container_memory_working_set_bytes{container!="",pod!="",namespace=~"payments"}`,
		}},
		{"cpu limits", podLimitQuery("cpu", ""), []string{
			`kube_pod_container_resource_limits{resource="cpu",pod!=""}`,
		}},
		{"memory limits with ns", podLimitQuery("memory", "^x$"), []string{
			`resource="memory"`, `namespace=~"^x$"`,
		}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			for _, sub := range c.wantSubs {
				if !strings.Contains(c.query, sub) {
					t.Fatalf("query %q missing %q", c.query, sub)
				}
			}
		})
	}
}

// Quote/backslash injection in a namespace filter or pod name must
// not be able to break out of the label-matcher string literal.
func TestEscapeLabelValue(t *testing.T) {
	cases := []struct{ in, want string }{
		{`plain`, `plain`},
		{`a"b`, `a\"b`},
		{`a\b`, `a\\b`},
		{`a\"b`, `a\\\"b`},
	}
	for _, c := range cases {
		if got := escapeLabelValue(c.in); got != c.want {
			t.Fatalf("escapeLabelValue(%q) = %q, want %q", c.in, got, c.want)
		}
	}
	q := singlePodCPUQuery("ns", `pod"}[5m])) or vector(1`)
	if strings.Contains(q, `pod"}[5m])) or vector(1"`) {
		t.Fatalf("injection survived unescaped: %s", q)
	}
}

func TestSinglePodQueriesPinBothLabels(t *testing.T) {
	q := singlePodMemQuery("payments", "api-7d9f-x2")
	for _, sub := range []string{`namespace="payments"`, `pod="api-7d9f-x2"`} {
		if !strings.Contains(q, sub) {
			t.Fatalf("query %q missing %q", q, sub)
		}
	}
	if strings.Contains(q, "topk") {
		t.Fatal("single-pod query must not carry topk")
	}
}

func rawPair(t *testing.T, js string) []json.RawMessage {
	t.Helper()
	var pair []json.RawMessage
	if err := json.Unmarshal([]byte(js), &pair); err != nil {
		t.Fatalf("fixture: %v", err)
	}
	return pair
}

func TestSamplePairDecoding(t *testing.T) {
	cases := []struct {
		name   string
		js     string
		wantV  float64
		wantTS int64
		wantOK bool
	}{
		{"normal", `[1784271068.123, "0.25"]`, 0.25, 1784271068, true},
		{"integer ts", `[1784271068, "1073741824"]`, 1 << 30, 1784271068, true},
		{"NaN dropped", `[1784271068, "NaN"]`, 0, 0, false},
		{"+Inf dropped", `[1784271068, "+Inf"]`, 0, 0, false},
		{"non-numeric dropped", `[1784271068, "abc"]`, 0, 0, false},
		{"short pair dropped", `[1784271068]`, 0, 0, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			v, ts, ok := samplePair(rawPair(t, c.js))
			if ok != c.wantOK || v != c.wantV || ts != c.wantTS {
				t.Fatalf("samplePair(%s) = (%v,%v,%v), want (%v,%v,%v)",
					c.js, v, ts, ok, c.wantV, c.wantTS, c.wantOK)
			}
		})
	}
}
