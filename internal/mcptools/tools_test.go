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
	}
	for _, name := range envBlind {
		tool := toolByName(t, tools, name)
		if _, ok := schemaProps(t, tool)["env"]; ok {
			t.Fatalf("%s: gained an env property but its read has no env path — remove it or wire the read first", name)
		}
	}
}
