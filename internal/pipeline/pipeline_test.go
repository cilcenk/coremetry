package pipeline

import (
	"testing"

	"github.com/cilcenk/coremetry/internal/chstore"
)

// v0.8.282 — pipeline engine extended to LOGS + METRICS (roadmap A5).
//
// Before this release the ingest policy engine (drop / enrich / sample)
// applied to SPANS only; logs + metrics flowed straight to the store
// regardless of any rule whose Signal was "logs" / "metrics" (the
// Settings → Pipeline signal selector even disabled those options with
// a "coming soon" note). These tests pin the two new pure decision
// seams — matchLog / matchMetric — plus the shared enrich writer and
// the end-to-end Accept{Log,Metric} drop/enrich decisions, so a future
// refactor can't silently regress a signal type the way the span-only
// MVP did.
//
// Seams under test (all allocation-light, run once per record on the
// 1B-spans / 10B-logs-per-day hot path):
//   - matchLog(Condition, *Log)          — well-known + attr/resource
//   - matchMetric(Condition, *MetricPoint)
//   - applyEnrichAttrs(&keys,&vals,set)  — override-in-place + append
//   - Engine.AcceptLog / Engine.AcceptMetric — drop + enrich + sample

func TestMatchLog(t *testing.T) {
	l := &chstore.Log{
		TraceID:      "abc123",
		SpanID:       "def456",
		ServiceName:  "checkout",
		HostName:     "node-7",
		SeverityNum:  17,
		SeverityText: "ERROR",
		Body:         "connection refused to upstream",
		ScopeName:    "otelcol",
		AttrKeys:     []string{"http.status_code", "log.source"},
		AttrValues:   []string{"500", "stdout"},
		ResKeys:      []string{"k8s.namespace", "deployment.environment"},
		ResValues:    []string{"prod", "production"},
	}
	tests := []struct {
		name string
		cond Condition
		want bool
	}{
		// well-known fields
		{"service eq hit", Condition{"service.name", OpEq, "checkout"}, true},
		{"service eq miss", Condition{"service.name", OpEq, "frontend"}, false},
		{"severity_text eq", Condition{"severity_text", OpEq, "ERROR"}, true},
		{"severity_number eq", Condition{"severity_number", OpEq, "17"}, true},
		{"severity_number miss", Condition{"severity_number", OpEq, "9"}, false},
		{"body contains", Condition{"body", OpContains, "connection refused"}, true},
		{"body contains miss", Condition{"body", OpContains, "timeout"}, false},
		{"host eq", Condition{"host.name", OpEq, "node-7"}, true},
		{"trace_id eq", Condition{"trace_id", OpEq, "abc123"}, true},
		{"span_id eq", Condition{"span_id", OpEq, "def456"}, true},
		{"scope eq", Condition{"scope.name", OpEq, "otelcol"}, true},
		// operators
		{"neq true", Condition{"service.name", OpNeq, "frontend"}, true},
		{"neq false", Condition{"service.name", OpNeq, "checkout"}, false},
		{"startsWith", Condition{"service.name", OpStartsWith, "check"}, true},
		{"endsWith", Condition{"service.name", OpEndsWith, "out"}, true},
		// span attributes via attr. prefix
		{"attr eq", Condition{"attr.http.status_code", OpEq, "500"}, true},
		{"attr miss key", Condition{"attr.nonexistent", OpEq, ""}, true}, // absent key -> "" ; == "" true
		{"attr neq", Condition{"attr.log.source", OpNeq, "stderr"}, true},
		// resource attributes via resource. prefix
		{"resource eq", Condition{"resource.k8s.namespace", OpEq, "prod"}, true},
		{"resource miss", Condition{"resource.k8s.namespace", OpEq, "staging"}, false},
		// unprefixed unknown falls back to span attributes
		{"unprefixed attr", Condition{"http.status_code", OpEq, "500"}, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := matchLog(tc.cond, l); got != tc.want {
				t.Errorf("matchLog(%+v) = %v, want %v", tc.cond, got, tc.want)
			}
		})
	}
}

func TestMatchMetric(t *testing.T) {
	m := &chstore.MetricPoint{
		Metric:      "http.server.duration",
		Instrument:  "histogram",
		Unit:        "ms",
		ServiceName: "checkout",
		HostName:    "node-7",
		AttrKeys:    []string{"http.route", "http.method"},
		AttrValues:  []string{"/api/pay", "POST"},
		ResKeys:     []string{"k8s.namespace", "team"},
		ResValues:   []string{"prod", "payments"},
	}
	tests := []struct {
		name string
		cond Condition
		want bool
	}{
		{"metric eq", Condition{"metric", OpEq, "http.server.duration"}, true},
		{"name alias eq", Condition{"name", OpEq, "http.server.duration"}, true},
		{"metric.name alias eq", Condition{"metric.name", OpEq, "http.server.duration"}, true},
		{"metric contains", Condition{"metric", OpContains, "server.duration"}, true},
		{"metric startsWith", Condition{"metric", OpStartsWith, "http."}, true},
		{"instrument eq", Condition{"instrument", OpEq, "histogram"}, true},
		{"type alias eq", Condition{"type", OpEq, "histogram"}, true},
		{"unit eq", Condition{"unit", OpEq, "ms"}, true},
		{"service eq", Condition{"service.name", OpEq, "checkout"}, true},
		{"host eq", Condition{"host.name", OpEq, "node-7"}, true},
		{"metric neq", Condition{"metric", OpNeq, "cpu.usage"}, true},
		{"attr eq", Condition{"attr.http.route", OpEq, "/api/pay"}, true},
		{"attr miss", Condition{"attr.http.route", OpEq, "/other"}, false},
		{"resource eq", Condition{"resource.team", OpEq, "payments"}, true},
		{"unprefixed attr", Condition{"http.method", OpEq, "POST"}, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := matchMetric(tc.cond, m); got != tc.want {
				t.Errorf("matchMetric(%+v) = %v, want %v", tc.cond, got, tc.want)
			}
		})
	}
}

func TestApplyEnrichAttrs(t *testing.T) {
	// append when key absent
	keys := []string{"existing"}
	vals := []string{"v0"}
	applyEnrichAttrs(&keys, &vals, map[string]string{"team": "payments"})
	if got := lookupAttr(keys, vals, "team"); got != "payments" {
		t.Fatalf("append: team = %q, want payments", got)
	}
	if lookupAttr(keys, vals, "existing") != "v0" {
		t.Fatalf("append clobbered existing key")
	}
	// override in place (no new key appended)
	beforeLen := len(keys)
	applyEnrichAttrs(&keys, &vals, map[string]string{"team": "checkout"})
	if got := lookupAttr(keys, vals, "team"); got != "checkout" {
		t.Fatalf("override: team = %q, want checkout", got)
	}
	if len(keys) != beforeLen {
		t.Fatalf("override appended a duplicate key: len %d -> %d", beforeLen, len(keys))
	}
}

func TestAcceptLog(t *testing.T) {
	dropErrors := Rule{
		ID: "r1", Name: "drop debug logs", Kind: KindDrop, Signal: SignalLogs,
		Enabled: true, When: Condition{"severity_text", OpEq, "DEBUG"},
	}
	enrichProd := Rule{
		ID: "r2", Name: "tag prod region", Kind: KindEnrich, Signal: SignalLogs,
		Enabled: true, When: Condition{"service.name", OpEq, "checkout"},
		SetAttributes: map[string]string{"region": "us-east-1"},
	}
	// span-signal rule must NOT affect logs
	dropSpanScoped := Rule{
		ID: "r3", Name: "span drop", Kind: KindDrop, Signal: SignalSpans,
		Enabled: true, When: Condition{"service.name", OpEq, "checkout"},
	}

	t.Run("drop matching log", func(t *testing.T) {
		e := &Engine{rules: []Rule{dropErrors}}
		l := &chstore.Log{SeverityText: "DEBUG", ServiceName: "checkout"}
		if e.AcceptLog(l) {
			t.Error("expected DEBUG log to be dropped")
		}
	})
	t.Run("keep non-matching log", func(t *testing.T) {
		e := &Engine{rules: []Rule{dropErrors}}
		l := &chstore.Log{SeverityText: "INFO", ServiceName: "checkout"}
		if !e.AcceptLog(l) {
			t.Error("expected INFO log to be kept")
		}
	})
	t.Run("span rule does not drop log", func(t *testing.T) {
		e := &Engine{rules: []Rule{dropSpanScoped}}
		l := &chstore.Log{ServiceName: "checkout"}
		if !e.AcceptLog(l) {
			t.Error("span-signal rule must not apply to logs")
		}
	})
	t.Run("enrich adds resource attr and keeps", func(t *testing.T) {
		e := &Engine{rules: []Rule{enrichProd}}
		l := &chstore.Log{ServiceName: "checkout"}
		if !e.AcceptLog(l) {
			t.Fatal("enrich rule must keep the log")
		}
		if got := lookupAttr(l.ResKeys, l.ResValues, "region"); got != "us-east-1" {
			t.Errorf("enrich: region = %q, want us-east-1", got)
		}
	})
	t.Run("sample rate 0 drops", func(t *testing.T) {
		e := &Engine{rules: []Rule{{
			ID: "s0", Name: "s0", Kind: KindSample, Signal: SignalLogs,
			Enabled: true, Rate: 0, When: Condition{"service.name", OpEq, "checkout"},
		}}}
		l := &chstore.Log{ServiceName: "checkout"}
		if e.AcceptLog(l) {
			t.Error("sample rate 0 must drop")
		}
	})
	t.Run("sample rate 1 keeps", func(t *testing.T) {
		e := &Engine{rules: []Rule{{
			ID: "s1", Name: "s1", Kind: KindSample, Signal: SignalLogs,
			Enabled: true, Rate: 1, When: Condition{"service.name", OpEq, "checkout"},
		}}}
		l := &chstore.Log{ServiceName: "checkout"}
		if !e.AcceptLog(l) {
			t.Error("sample rate 1 must keep")
		}
	})
	t.Run("disabled rule ignored", func(t *testing.T) {
		r := dropErrors
		r.Enabled = false
		e := &Engine{rules: []Rule{r}}
		l := &chstore.Log{SeverityText: "DEBUG"}
		if !e.AcceptLog(l) {
			t.Error("disabled rule must not drop")
		}
	})
	t.Run("nil engine and empty rules keep", func(t *testing.T) {
		if !(&Engine{}).AcceptLog(&chstore.Log{}) {
			t.Error("empty rule set must keep")
		}
	})
}

func TestAcceptMetric(t *testing.T) {
	dropDebugGauge := Rule{
		ID: "m1", Name: "drop debug gauge", Kind: KindDrop, Signal: SignalMetrics,
		Enabled: true, When: Condition{"metric", OpStartsWith, "debug."},
	}
	enrichTeam := Rule{
		ID: "m2", Name: "tag team", Kind: KindEnrich, Signal: SignalMetrics,
		Enabled: true, When: Condition{"service.name", OpEq, "checkout"},
		SetAttributes: map[string]string{"team": "payments"},
	}

	t.Run("drop matching metric", func(t *testing.T) {
		e := &Engine{rules: []Rule{dropDebugGauge}}
		m := &chstore.MetricPoint{Metric: "debug.heap.objects"}
		if e.AcceptMetric(m) {
			t.Error("expected debug.* metric to be dropped")
		}
	})
	t.Run("keep non-matching metric", func(t *testing.T) {
		e := &Engine{rules: []Rule{dropDebugGauge}}
		m := &chstore.MetricPoint{Metric: "http.server.duration"}
		if !e.AcceptMetric(m) {
			t.Error("expected http.* metric to be kept")
		}
	})
	t.Run("logs rule does not drop metric", func(t *testing.T) {
		r := dropDebugGauge
		r.Signal = SignalLogs
		e := &Engine{rules: []Rule{r}}
		m := &chstore.MetricPoint{Metric: "debug.heap.objects"}
		if !e.AcceptMetric(m) {
			t.Error("logs-signal rule must not apply to metrics")
		}
	})
	t.Run("enrich adds resource attr", func(t *testing.T) {
		e := &Engine{rules: []Rule{enrichTeam}}
		m := &chstore.MetricPoint{Metric: "http.server.duration", ServiceName: "checkout"}
		if !e.AcceptMetric(m) {
			t.Fatal("enrich rule must keep the metric")
		}
		if got := lookupAttr(m.ResKeys, m.ResValues, "team"); got != "payments" {
			t.Errorf("enrich: team = %q, want payments", got)
		}
	})
}
