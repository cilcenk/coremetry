package api

// ES Watcher Faz-1 AŞAMA B — import handler's pure stages.
// buildWatcherImport (request body → projected rule + report) and
// planWatcherImport (dryRun / name-conflict / import decision) carry
// the whole contract except the store round-trip, so they get the
// table-driven coverage; the watcher package owns Parse/Validate/
// ToRule semantics in its own tests.

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/cilcenk/coremetry/internal/chstore"
	"github.com/cilcenk/coremetry/internal/watcher"
)

// fullWatch is a completely supported watch: 10m interval schedule,
// search input, hits.total gte compare, 5m throttle.
const fullWatch = `{
	"trigger":  {"schedule": {"interval": "10m"}},
	"input":    {"search": {"request": {"indices": ["app-*"], "body": {"query": {"match_all": {}}}}}},
	"condition": {"compare": {"ctx.payload.hits.total": {"gte": 100}}},
	"throttle_period": "5m",
	"metadata": {"name": "meta-name"}
}`

// scriptWatch has a Painless condition — no engine, imports disabled.
const scriptWatch = `{
	"trigger":  {"schedule": {"interval": "5m"}},
	"input":    {"search": {"request": {"indices": ["logs-*"], "body": {}}}},
	"condition": {"script": {"source": "return true"}}
}`

func TestBuildWatcherImport(t *testing.T) {
	cases := []struct {
		name    string
		req     watcherImportRequest
		wantErr string // substring; "" = success expected
		check   func(t *testing.T, rule chstore.AlertRule, rep watcherImportReport)
	}{
		{
			name: "fully supported watch projects comparator/threshold/window/cooldown",
			req:  watcherImportRequest{Name: "prod errors", Watch: json.RawMessage(fullWatch)},
			check: func(t *testing.T, rule chstore.AlertRule, rep watcherImportReport) {
				if rule.Metric != "watcher" {
					t.Fatalf("Metric = %q, want watcher", rule.Metric)
				}
				if rule.Comparator != ">=" || rule.Threshold != 100 {
					t.Fatalf("condition = %s %v, want >= 100", rule.Comparator, rule.Threshold)
				}
				if rule.WindowSec != 600 {
					t.Fatalf("WindowSec = %d, want 600 (10m interval)", rule.WindowSec)
				}
				if rule.CooldownSec != 300 {
					t.Fatalf("CooldownSec = %d, want 300 (5m throttle)", rule.CooldownSec)
				}
				if !rule.Enabled || !rep.Enabled {
					t.Fatalf("rule/report enabled = %v/%v, want true/true", rule.Enabled, rep.Enabled)
				}
				if rule.WatcherJSON != fullWatch {
					t.Fatalf("WatcherJSON not stored verbatim")
				}
				if rep.Rule.WindowSec != 600 || rep.Rule.Comparator != ">=" || rep.Rule.Threshold != 100 || rep.Rule.CooldownSec != 300 {
					t.Fatalf("preview mismatch: %+v", rep.Rule)
				}
			},
		},
		{
			name: "request name wins over metadata.name",
			req:  watcherImportRequest{Name: "explicit", Watch: json.RawMessage(fullWatch)},
			check: func(t *testing.T, rule chstore.AlertRule, rep watcherImportReport) {
				if rule.Name != "explicit" || rep.Rule.Name != "explicit" {
					t.Fatalf("name = %q/%q, want explicit", rule.Name, rep.Rule.Name)
				}
			},
		},
		{
			name: "metadata.name is the fallback when the request name is blank",
			req:  watcherImportRequest{Name: "  ", Watch: json.RawMessage(fullWatch)},
			check: func(t *testing.T, rule chstore.AlertRule, _ watcherImportReport) {
				if rule.Name != "meta-name" {
					t.Fatalf("name = %q, want meta-name", rule.Name)
				}
			},
		},
		{
			name: "script condition imports disabled with a reason and an unsupported finding",
			req:  watcherImportRequest{Name: "scripted", Watch: json.RawMessage(scriptWatch)},
			check: func(t *testing.T, rule chstore.AlertRule, rep watcherImportReport) {
				if rule.Enabled || rep.Enabled {
					t.Fatalf("script watch must import disabled")
				}
				if rep.DisabledReason == "" {
					t.Fatalf("disabled report must carry a reason")
				}
				var sawUnsupported bool
				for _, f := range rep.Findings {
					if f.Status == watcher.Unsupported {
						sawUnsupported = true
					}
				}
				if !sawUnsupported {
					t.Fatalf("findings must flag the script condition unsupported: %+v", rep.Findings)
				}
			},
		},
		{
			name:    "no name anywhere is a 400",
			req:     watcherImportRequest{Watch: json.RawMessage(`{"input":{"search":{"request":{"indices":["a"],"body":{}}}}}`)},
			wantErr: "name required",
		},
		{
			name:    "empty watch is a 400",
			req:     watcherImportRequest{Name: "x", Watch: json.RawMessage("  ")},
			wantErr: "empty definition",
		},
		{
			name:    "malformed watch JSON is a 400",
			req:     watcherImportRequest{Name: "x", Watch: json.RawMessage(`{"trigger":`)},
			wantErr: "watcher JSON",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rule, rep, err := buildWatcherImport(tc.req)
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("err = %v, want substring %q", err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if rep.Findings == nil {
				t.Fatalf("findings must never be nil (JSON [] contract)")
			}
			tc.check(t, rule, rep)
		})
	}
}

func TestPlanWatcherImport(t *testing.T) {
	existing := []chstore.AlertRule{
		{Name: "prod errors"},
		{Name: "  padded  "},
	}
	cases := []struct {
		name   string
		dryRun bool
		rule   string
		want   watcherImportPlan
	}{
		{"dryRun never touches the store even on a colliding name", true, "prod errors", watcherPlanReportOnly},
		{"same name conflicts — Faz-1 has no overwrite", false, "prod errors", watcherPlanConflict},
		{"whitespace-padded existing name still conflicts", false, "padded", watcherPlanConflict},
		{"fresh name imports", false, "brand new", watcherPlanImport},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := planWatcherImport(tc.dryRun, tc.rule, existing); got != tc.want {
				t.Fatalf("plan = %d, want %d", got, tc.want)
			}
		})
	}
	t.Run("empty rule set imports", func(t *testing.T) {
		if got := planWatcherImport(false, "anything", nil); got != watcherPlanImport {
			t.Fatalf("plan = %d, want import", got)
		}
	})
}

// Review F5 (v0.9.x): the frontend sends the operator's literal paste
// as `watchText`; it wins over `watch`, is stored byte-verbatim (incl.
// >2^53 integers a browser parse→stringify would round), and Kibana
// DevTools """triple-quoted""" strings normalize into valid JSON
// before Parse — the NORMALIZED form is what gets stored.
func TestBuildWatcherImportWatchText(t *testing.T) {
	t.Run("watchText wins over watch and stores verbatim", func(t *testing.T) {
		rule, _, err := buildWatcherImport(watcherImportRequest{
			Name:      "x",
			Watch:     json.RawMessage(`{"input":{"search":{"request":{"indices":["ignored-*"],"body":{"query":{"match_all":{}}}}}}}`),
			WatchText: fullWatch,
		})
		if err != nil {
			t.Fatalf("buildWatcherImport: %v", err)
		}
		if rule.WatcherJSON != fullWatch {
			t.Fatalf("WatcherJSON must be the literal watchText, got %q", rule.WatcherJSON)
		}
	})
	t.Run("big integers survive byte-exact", func(t *testing.T) {
		src := `{"trigger":{"schedule":{"interval":"5m"}},"input":{"search":{"request":{"indices":["app-*"],"body":{"query":{"bool":{"must":[{"term":{"txn.id":9007199254740993}}],"filter":[{"range":{"ts":{"gte":1753000000123456789}}}]}}}}}},"condition":{"compare":{"ctx.payload.hits.total":{"gte":1}}}}`
		rule, _, err := buildWatcherImport(watcherImportRequest{Name: "big", WatchText: src})
		if err != nil {
			t.Fatalf("buildWatcherImport: %v", err)
		}
		for _, lit := range []string{"9007199254740993", "1753000000123456789"} {
			if !strings.Contains(rule.WatcherJSON, lit) {
				t.Fatalf("literal %s corrupted in stored definition", lit)
			}
		}
	})
	t.Run("DevTools triple-quoted paste normalizes and imports", func(t *testing.T) {
		src := "{\"trigger\":{\"schedule\":{\"interval\":\"5m\"}},\"input\":{\"search\":{\"request\":{\"indices\":[\"app-*\"],\"body\":{\"query\":{\"match_all\":{}}}}}},\"condition\":{\"always\":{}},\"actions\":{\"a\":{\"email\":{\"to\":\"x@y.z\",\"body\":{\"text\":\"\"\"{{ctx.payload.hits.total}} \"hits\"\nline two\"\"\"}}}}}"
		rule, rep, err := buildWatcherImport(watcherImportRequest{Name: "devtools", WatchText: src})
		if err != nil {
			t.Fatalf("buildWatcherImport: %v", err)
		}
		if !rep.Enabled || !rule.Enabled {
			t.Fatalf("DevTools paste must import enabled: %+v", rep)
		}
		// Stored form must be valid JSON (the evaluator re-parses it
		// every run) — i.e. the normalized bytes, not the raw paste.
		var m map[string]any
		if err := json.Unmarshal([]byte(rule.WatcherJSON), &m); err != nil {
			t.Fatalf("stored definition is not valid JSON: %v", err)
		}
		if strings.Contains(rule.WatcherJSON, `"""`) {
			t.Fatal("triple quotes must not survive into the stored definition")
		}
	})
	t.Run("whitespace-only watchText falls back to watch", func(t *testing.T) {
		rule, _, err := buildWatcherImport(watcherImportRequest{
			Name: "x", Watch: json.RawMessage(fullWatch), WatchText: "  \n ",
		})
		if err != nil {
			t.Fatalf("buildWatcherImport: %v", err)
		}
		if rule.WatcherJSON != fullWatch {
			t.Fatalf("fallback to watch failed, got %q", rule.WatcherJSON)
		}
	})
}
