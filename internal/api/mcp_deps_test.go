package api

// v0.9.1150 — mcpDeps must carry the metric read ROUTER.
//
// mcptools falls back to Deps.Store when Metrics is nil, and that
// fallback is ClickHouse. So forgetting the field here does not break
// anything visibly: the MCP tools keep answering, from the WRONG store,
// while every HTTP metric surface reads VictoriaMetrics. The model would
// then list a metric name from one backend and query it in the other,
// find an empty series, and report that the metric has no data.
//
// This is the "fail-open silently un-applies a fix" class
// (v0.9.984): the safe-looking default is the one that hides the bug.
// mcp_deps.go is the single Deps constructor precisely so ONE test can
// hold the line.

import (
	"os"
	"regexp"
	"strings"
	"testing"

	"github.com/cilcenk/coremetry/internal/vmetrics"
)

func TestMCPDepsCarriesMetricRouter(t *testing.T) {
	s := &Server{}
	if d := s.mcpDeps(); d.Metrics == nil {
		t.Fatal("mcpDeps().Metrics is nil — MCP metric tools would silently read ClickHouse " +
			"while the HTTP surfaces read the operator's configured backend")
	}
	// And the router it carries must be the SELECTED one, not a hardcoded
	// ClickHouse adapter.
	s.vmetrics = vmetrics.New()
	s.vmetrics.Configure(vmetrics.Settings{Enabled: true, BaseURL: "http://vm:8428"})
	d := s.mcpDeps()
	src, ok := d.Metrics.(metricSource)
	if !ok {
		t.Fatalf("Metrics is not a metricSource: %T", d.Metrics)
	}
	if got := src.Name(); got != metricSourceVM {
		t.Fatalf("Metrics backend = %q, want %q — mcpDeps is not using s.metricSource()", got, metricSourceVM)
	}
}

// mcpDeps is the ONLY place a mcptools.Deps may be constructed inside
// internal/api. A second construction site is how the Metrics field goes
// missing on one path (the reason the constructor was centralised in
// v0.9.1147, now load-bearing for correctness rather than tidiness).
func TestOnlyOneMCPDepsConstructionSite(t *testing.T) {
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	lit := regexp.MustCompile(`mcptools\.Deps\{`)
	var sites []string
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		raw, err := os.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
		body := stripGoComments(string(raw))
		for range lit.FindAllString(body, -1) {
			sites = append(sites, name)
		}
	}
	if len(sites) != 1 || sites[0] != "mcp_deps.go" {
		t.Fatalf("mcptools.Deps{...} literals found in %v — construct it only in mcp_deps.go "+
			"so the Metrics router can never be omitted on one path", sites)
	}
}
