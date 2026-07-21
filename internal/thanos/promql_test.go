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

// v0.8.580 — request ekseni: podRequestQuery podLimitQuery'nin
// birebir kardeşi kalmalı (aynı gruplandırma + kalkanlar), yalnız
// metrik adı değişir.
func TestPodRequestQuery(t *testing.T) {
	q := podRequestQuery("cpu", "^app-")
	for _, sub := range []string{
		`kube_pod_container_resource_requests{resource="cpu",pod!="",namespace=~"^app-"}`,
		`sum by (namespace, pod)`,
	} {
		if !strings.Contains(q, sub) {
			t.Fatalf("query %q missing %q", q, sub)
		}
	}
	if strings.Contains(q, "resource_limits") {
		t.Fatal("request query must not touch the limits metric")
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

// v0.9.50 (design handoff §8) — deployment-kapsamlı trend builder:
// pod regex'i "<deploy>-.*" önekli, deploy adı regex-meta + label
// kaçışlı; byPod topk'siz sum by (pod) (v0.9.3 adım-kayması notu).
func TestDeployTrendQuery(t *testing.T) {
	cases := []struct {
		name string
		q    string
		want []string
	}{
		{"cpu total", deployTrendQuery("payments", "api-gw", "cpu", false),
			[]string{`sum(rate(container_cpu_usage_seconds_total{`, `namespace="payments"`, `pod=~"api-gw-.*"`}},
		{"mem byPod", deployTrendQuery("payments", "api-gw", "mem", true),
			[]string{`sum by (pod) (container_memory_working_set_bytes{`, `pod=~"api-gw-.*"`}},
		{"regex meta kaçışı", deployTrendQuery("ns", "svc.v2", "cpu", false),
			[]string{`pod=~"svc\\.v2-.*"`}},
	}
	for _, c := range cases {
		for _, w := range c.want {
			if !strings.Contains(c.q, w) {
				t.Errorf("%s: %q içinde %q yok", c.name, c.q, w)
			}
		}
	}
	for _, q := range []string{
		deployTrendQuery("ns", "d", "cpu", true),
		deployTrendQuery("ns", "d", "mem", true),
	} {
		if strings.Contains(q, "topk") {
			t.Errorf("byPod sorgusu topk içermemeli (adım-kayması): %q", q)
		}
	}
}

// TestJMXTrendQuery (v0.9.140, auto-discovery v0.9.144) — Service→Infra
// JBoss/JVM JMX sorgu şekli. Selector kube-prometheus/cAdvisor düzlemi
// (operatör 2026-07-21): container=~".*",namespace,pod=~"<deploy>-.*",
// group by pod. Metrik adı HAM (keşfedilmiş); sayaç (_total/_sum) rate.
func TestJMXTrendQuery(t *testing.T) {
	cases := []struct {
		name string
		q    string
		want []string
	}{
		{"gauge byPod", jmxTrendQuery("prod", "app", "jvm_memory_bytes_used", true),
			[]string{`sum by (pod) (jvm_memory_bytes_used{`, `container=~".*"`, `namespace="prod"`, `pod=~"app-.*"`}},
		{"gauge total", jmxTrendQuery("prod", "app", "jvm_threads_current", false),
			[]string{`sum(jvm_threads_current{`, `pod=~"app-.*"`}},
		{"counter _sum rate", jmxTrendQuery("prod", "app", "jvm_gc_collection_seconds_sum", true),
			[]string{`rate(jvm_gc_collection_seconds_sum{`, `[5m])`, `sum by (pod)`}},
		{"counter _total rate", jmxTrendQuery("prod", "app", "jvm_classes_loaded_total", true),
			[]string{`rate(jvm_classes_loaded_total{`, `[5m])`}},
		{"jboss datasource gauge", jmxTrendQuery("prod", "app", "jboss_pool_in_use_count", true),
			[]string{`sum by (pod) (jboss_pool_in_use_count{`}},
		{"regex meta kaçışı", jmxTrendQuery("ns", "svc.v2", "jvm_threads_current", true),
			[]string{`pod=~"svc\\.v2-.*"`}},
	}
	for _, c := range cases {
		for _, w := range c.want {
			if !strings.Contains(c.q, w) {
				t.Errorf("%s: %q içinde %q yok", c.name, c.q, w)
			}
		}
	}
	// jvm_gc..._count GAUGE kalmalı (jboss "_count" gauge'dur, rate DEĞİL).
	if q := jmxTrendQuery("ns", "d", "jboss_pool_in_use_count", true); strings.Contains(q, "rate(") {
		t.Errorf("_count gauge olmalı, rate'lenmemeli: %q", q)
	}
	// Discovery sorgusu __name__ filtresi + selector taşır.
	if d := jmxDiscoveryQuery("prod", "app"); !strings.Contains(d, `count by (__name__)`) ||
		!strings.Contains(d, `__name__=~"(jvm|jboss)_.*"`) || !strings.Contains(d, `pod=~"app-.*"`) {
		t.Errorf("jmxDiscoveryQuery yanlış: %q", d)
	}
	// ValidJMXMetric: yalnız jvm_/jboss_ + [a-z0-9_]; enjeksiyon reddedilir.
	for _, ok := range []string{"jvm_memory_bytes_used", "jboss_pool_in_use_count"} {
		if !ValidJMXMetric(ok) {
			t.Errorf("ValidJMXMetric(%q) true olmalı", ok)
		}
	}
	for _, bad := range []string{"", "cpu", "container_cpu_usage", "jvm_x} or vector(1)", "jvm-dash", "JVM_UPPER", "jvm_x{a=1}"} {
		if ValidJMXMetric(bad) {
			t.Errorf("ValidJMXMetric(%q) false olmalı (enjeksiyon/kapı)", bad)
		}
	}
}
