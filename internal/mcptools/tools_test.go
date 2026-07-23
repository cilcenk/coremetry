package mcptools

// tools_test.go — v0.8.398 (AI audit env-awareness slice). The optional
// `env` arg on the env-capable tools must be ADDITIVE: every
// pre-existing schema property survives, env is never required, and the
// args structs mirror the schema field-for-field (a mismatch silently
// zero-values the filter — the /mcp-tools skill's step-7 rule). Tools
// whose underlying reads have no env path stay env-LESS on purpose
// (documented skip, not an oversight) — a property appearing there
// would promise a filter the read can't apply.

import (
	"strings"
	"testing"

	"github.com/cilcenk/coremetry/internal/mcp"
)

func toolByName(t *testing.T, tools []mcp.Tool, name string) mcp.Tool {
	t.Helper()
	for _, tool := range tools {
		if tool.Name == name {
			return tool
		}
	}
	t.Fatalf("tool %q not in ToolList", name)
	return mcp.Tool{}
}

func schemaProps(t *testing.T, tool mcp.Tool) map[string]any {
	t.Helper()
	props, ok := tool.InputSchema["properties"].(map[string]any)
	if !ok {
		t.Fatalf("tool %q: InputSchema.properties missing or wrong type", tool.Name)
	}
	return props
}

func TestEnvArgAdditive(t *testing.T) {
	// Deps{} is safe: ToolList only builds closures, no store access.
	tools := ToolList(Deps{})

	// The env-capable set (v0.8.398) → the pre-existing properties each
	// schema must KEEP (additive contract).
	withEnv := map[string][]string{
		"list_problems":      {"status", "service", "severity", "priority", "limit"},
		"list_services":      {"name_contains", "range_s", "limit"},
		"get_service_health": {"service", "range_s"},
	}
	for name, keep := range withEnv {
		t.Run(name, func(t *testing.T) {
			tool := toolByName(t, tools, name)
			props := schemaProps(t, tool)
			env, ok := props["env"].(map[string]any)
			if !ok {
				t.Fatalf("%s: env property missing", name)
			}
			if env["type"] != "string" {
				t.Fatalf("%s: env type = %v, want string", name, env["type"])
			}
			desc, _ := env["description"].(string)
			if desc == "" {
				t.Fatalf("%s: env property has no description (the LLM will guess)", name)
			}
			// The description must teach the value vocabulary (int/uat/prep
			// style) so a small model doesn't invent 'production-eu-1'.
			if !strings.Contains(desc, "uat") {
				t.Fatalf("%s: env description must mention int/uat/prep style values, got %q", name, desc)
			}
			for _, p := range keep {
				if _, ok := props[p]; !ok {
					t.Fatalf("%s: pre-existing property %q dropped — env must be additive", name, p)
				}
			}
			// env must stay OPTIONAL: never in required.
			if req, ok := tool.InputSchema["required"].([]string); ok {
				for _, r := range req {
					if r == "env" {
						t.Fatalf("%s: env must not be required", name)
					}
				}
			}
		})
	}

	// get_service_health keeps its original required contract exactly.
	sh := toolByName(t, tools, "get_service_health")
	req, ok := sh.InputSchema["required"].([]string)
	if !ok || len(req) != 1 || req[0] != "service" {
		t.Fatalf("get_service_health required = %v, want [service]", sh.InputSchema["required"])
	}

	// The deliberate env-less set: their reads carry no env path yet
	// (logs/anomalies/metrics — env-separation Phase 4 pending) or are
	// id-anchored point lookups where env is meaningless.
	envBlind := []string{
		"list_anomalies", "search_logs", "get_trace", "query_metric",
		"get_logs_for_trace", "get_exemplar_traces", "get_linked_traces",
		"get_metrics_for_span",
		// CoSRE Faz-2 — render_chart's spanMetricBatch read (frontend)
		// has no env conjunct; an env property here would promise a
		// filter the chart can't apply.
		"render_chart",
	}
	for _, name := range envBlind {
		tool := toolByName(t, tools, name)
		if _, ok := schemaProps(t, tool)["env"]; ok {
			t.Fatalf("%s: gained an env property but its read has no env path — remove it or wire the read first", name)
		}
	}
}

// v0.9.160 — get_problem_root_cause tool: problem_id ZORUNLU (boş id her
// anchor'ı tarar) + string + açıklama root-cause hipotezini vaat etmeli.
// ToolList hem MCP hem in-app copilot'u besler → kontrat ikisini de bağlar.
func TestGetProblemRootCauseTool(t *testing.T) {
	tool := toolByName(t, ToolList(Deps{}), "get_problem_root_cause")
	props := schemaProps(t, tool)
	pid, ok := props["problem_id"].(map[string]any)
	if !ok {
		t.Fatal("get_problem_root_cause: problem_id property missing")
	}
	if pid["type"] != "string" {
		t.Fatalf("problem_id type = %v, want string", pid["type"])
	}
	req, _ := tool.InputSchema["required"].([]any)
	found := false
	for _, r := range req {
		if r == "problem_id" {
			found = true
		}
	}
	if !found {
		t.Fatal("get_problem_root_cause: problem_id must be in required[]")
	}
	d := strings.ToLower(tool.Description)
	if !strings.Contains(d, "root-cause") && !strings.Contains(d, "root cause") {
		t.Fatal("description must explain it returns the root-cause hypothesis")
	}
}

// CoSRE Faz-2 — render_chart: the model PICKS the chart, the server
// (copilot_chat.go) emits the deterministic ```chart``` block. The
// schema below is the LLM's whole contract; the metric enum MUST stay
// 1:1 with CosreChart's AGG_META keys (frontend) — an unknown agg
// silently falls back to 'rate' there.
func TestRenderChartTool(t *testing.T) {
	tool := toolByName(t, ToolList(Deps{}), "render_chart")
	props := schemaProps(t, tool)

	// service: string + the ONLY required property.
	svc, ok := props["service"].(map[string]any)
	if !ok {
		t.Fatal("render_chart: service property missing")
	}
	if svc["type"] != "string" {
		t.Fatalf("service type = %v, want string", svc["type"])
	}
	req, ok := tool.InputSchema["required"].([]string)
	if !ok || len(req) != 1 || req[0] != "service" {
		t.Fatalf("required = %v, want [service]", tool.InputSchema["required"])
	}

	// metric enum ↔ CosreChart AGG_META keys, exactly, in order.
	met, ok := props["metric"].(map[string]any)
	if !ok {
		t.Fatal("render_chart: metric property missing")
	}
	enum, ok := met["enum"].([]string)
	if !ok {
		t.Fatalf("metric enum missing or wrong type: %v", met["enum"])
	}
	want := []string{"rate", "error_rate", "p50", "p95", "p99"}
	if len(enum) != len(want) {
		t.Fatalf("metric enum = %v, want %v", enum, want)
	}
	for i := range want {
		if enum[i] != want[i] {
			t.Fatalf("metric enum[%d] = %q, want %q (must mirror CosreChart AGG_META)", i, enum[i], want[i])
		}
	}

	// range_s carries the 7d cap so the LLM can't fan a 90d scan.
	rs, ok := props["range_s"].(map[string]any)
	if !ok {
		t.Fatal("render_chart: range_s property missing")
	}
	if rs["maximum"] != 604800 {
		t.Fatalf("range_s maximum = %v, want 604800", rs["maximum"])
	}

	// Every property needs a description — an un-described property
	// gets guessed wrong by the LLM (/mcp-tools skill rule).
	for name, p := range props {
		pm, _ := p.(map[string]any)
		if desc, _ := pm["description"].(string); desc == "" {
			t.Fatalf("property %q has no description", name)
		}
	}
}

// normalizeRenderChart is the pure validation core of render_chart:
// metric default + whitelist, range_s default + 7d clamp.
func TestNormalizeRenderChart(t *testing.T) {
	cases := []struct {
		name       string
		in         renderChartArgs
		wantMetric string
		wantRange  int
		wantErr    bool
	}{
		{"defaults", renderChartArgs{Service: "checkout"}, "error_rate", 1800, false},
		{"explicit p99", renderChartArgs{Service: "checkout", Metric: "p99", RangeS: 3600}, "p99", 3600, false},
		{"rate passes", renderChartArgs{Service: "checkout", Metric: "rate"}, "rate", 1800, false},
		{"p50 passes", renderChartArgs{Service: "checkout", Metric: "p50"}, "p50", 1800, false},
		{"p95 passes", renderChartArgs{Service: "checkout", Metric: "p95"}, "p95", 1800, false},
		{"unknown metric rejected", renderChartArgs{Service: "checkout", Metric: "p999"}, "", 0, true},
		{"avg is not chartable", renderChartArgs{Service: "checkout", Metric: "avg"}, "", 0, true},
		{"range clamped to 7d", renderChartArgs{Service: "checkout", RangeS: 99999999}, "error_rate", 7 * 86400, false},
		{"negative range → default", renderChartArgs{Service: "checkout", RangeS: -5}, "error_rate", 1800, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, errMsg := normalizeRenderChart(tc.in)
			if tc.wantErr {
				if errMsg == "" {
					t.Fatalf("want error for metric %q, got none (normalized to %q)", tc.in.Metric, got.Metric)
				}
				return
			}
			if errMsg != "" {
				t.Fatalf("unexpected error: %s", errMsg)
			}
			if got.Metric != tc.wantMetric {
				t.Fatalf("metric = %q, want %q", got.Metric, tc.wantMetric)
			}
			if got.RangeS != tc.wantRange {
				t.Fatalf("rangeS = %d, want %d", got.RangeS, tc.wantRange)
			}
		})
	}
}
