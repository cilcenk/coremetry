// Package watcher tests — ES Watcher birebir-JSON import, Faz-1
// backend core (v0.9.x). Table-driven over the three public
// operations: Parse (schema fidelity + unknown-field tolerance),
// Validate (field-by-field Supported/Partial/Unsupported report),
// ToRule (executable chstore.AlertRule projection).
package watcher

import (
	"encoding/json"
	"strings"
	"testing"
)

// typicalWatch is the canonical log-threshold watcher an operator
// PUTs to _watcher/watch: interval schedule + search input +
// hits.total compare + email/webhook actions + throttle.
const typicalWatch = `{
  "trigger":   {"schedule": {"interval": "5m"}},
  "input":     {"search": {"request": {"indices": ["app-*"], "body": {"query": {"bool": {"must": [{"query_string": {"query": "level:ERROR"}}], "filter": [{"range": {"@timestamp": {"gte": "now-5m"}}}]}}}}}},
  "condition": {"compare": {"ctx.payload.hits.total": {"gte": 100}}},
  "actions": {
    "notify_email": {"email":   {"to": "oncall@example.com", "subject": "error surge"}},
    "notify_hook":  {"webhook": {"host": "hooks.example.com", "port": 443, "path": "/alert"}}
  },
  "throttle_period": "10m",
  "metadata": {"name": "app error surge"}
}`

func TestParseTypicalWatch(t *testing.T) {
	w, err := Parse([]byte(typicalWatch))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if w.Trigger == nil || w.Trigger.Schedule == nil || w.Trigger.Schedule.Interval != "5m" {
		t.Fatalf("trigger.schedule.interval not parsed: %+v", w.Trigger)
	}
	if w.Input == nil || w.Input.Search == nil {
		t.Fatalf("input.search not parsed: %+v", w.Input)
	}
	if got := w.Input.Search.Request.Indices; len(got) != 1 || got[0] != "app-*" {
		t.Fatalf("indices = %v, want [app-*]", got)
	}
	if len(w.Input.Search.Request.Body) == 0 || !strings.Contains(string(w.Input.Search.Request.Body), "query_string") {
		t.Fatalf("search body not carried verbatim: %s", w.Input.Search.Request.Body)
	}
	if w.Condition == nil || len(w.Condition.Compare) != 1 {
		t.Fatalf("condition.compare not parsed: %+v", w.Condition)
	}
	if len(w.Actions) != 2 {
		t.Fatalf("actions = %d, want 2", len(w.Actions))
	}
	if w.Actions["notify_email"].Email == nil {
		t.Fatalf("email action payload lost")
	}
	if w.Actions["notify_hook"].Webhook == nil {
		t.Fatalf("webhook action payload lost")
	}
	if w.ThrottlePeriod != "10m" {
		t.Fatalf("throttle_period = %q, want 10m", w.ThrottlePeriod)
	}
	if len(w.Unknown) != 0 {
		t.Fatalf("unexpected unknown fields: %v", w.Unknown)
	}
}

func TestParseErrors(t *testing.T) {
	tests := []struct {
		name string
		raw  string
	}{
		{"broken json", `{"trigger": {`},
		{"empty", ``},
		{"non-object", `[1,2,3]`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := Parse([]byte(tt.raw)); err == nil {
				t.Fatalf("Parse(%q): want error, got nil", tt.raw)
			}
		})
	}
}

func TestParseUnknownTopLevelFields(t *testing.T) {
	raw := `{"trigger": {"schedule": {"interval": "1m"}}, "input": {"search": {"request": {"body": {}}}}, "x_custom": {"a": 1}}`
	w, err := Parse([]byte(raw))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if len(w.Unknown) != 1 || w.Unknown[0] != "x_custom" {
		t.Fatalf("Unknown = %v, want [x_custom]", w.Unknown)
	}
	// Unknown fields surface on the report but never fail the import —
	// the raw JSON round-trips them.
	rep := Validate(w)
	if !hasFinding(rep, "x_custom", Partial) {
		t.Fatalf("expected Partial finding for x_custom, got %+v", rep.Findings)
	}
}

func TestParseESDuration(t *testing.T) {
	tests := []struct {
		in      string
		wantSec int64
		wantErr bool
	}{
		{"30s", 30, false},
		{"5m", 300, false},
		{"2h", 7200, false},
		{"1d", 86400, false}, // ES-only unit — time.ParseDuration rejects it
		{"90s", 90, false},
		{"1h30m", 5400, false},
		{"500ms", 0, false}, // sub-second truncates to 0
		{"", 0, true},
		{"bogus", 0, true},
		{"-5m", 0, true}, // negative durations are nonsense for schedules
	}
	for _, tt := range tests {
		t.Run(tt.in, func(t *testing.T) {
			d, err := parseESDuration(tt.in)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("parseESDuration(%q): want error", tt.in)
				}
				return
			}
			if err != nil {
				t.Fatalf("parseESDuration(%q): %v", tt.in, err)
			}
			if got := int64(d.Seconds()); got != tt.wantSec {
				t.Fatalf("parseESDuration(%q) = %ds, want %ds", tt.in, got, tt.wantSec)
			}
		})
	}
}

func TestIntervalSec(t *testing.T) {
	tests := []struct {
		name string
		raw  string
		want uint32
	}{
		{"5m", `{"trigger": {"schedule": {"interval": "5m"}}}`, 300},
		{"sub-minute clamps to 60", `{"trigger": {"schedule": {"interval": "10s"}}}`, 60},
		{"numeric seconds", `{"trigger": {"schedule": {"interval": 120}}}`, 120},
		{"no trigger", `{"input": {"search": {"request": {"body": {}}}}}`, 0},
		// v0.9.x cron→interval: fixed-rate Quartz cron maps to seconds.
		{"fixed-rate cron maps", `{"trigger": {"schedule": {"cron": "0 0/5 * * * ?"}}}`, 300},
		{"calendar cron stays unmapped", `{"trigger": {"schedule": {"cron": "0 0 2 * * ?"}}}`, 0},
		{"unparseable interval", `{"trigger": {"schedule": {"interval": "whenever"}}}`, 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			w, err := Parse([]byte(tt.raw))
			if err != nil {
				t.Fatalf("Parse: %v", err)
			}
			if got := w.IntervalSec(); got != tt.want {
				t.Fatalf("IntervalSec() = %d, want %d", got, tt.want)
			}
		})
	}
}

// hasFinding reports whether the report carries a finding whose Field
// contains the given substring at the given status.
func hasFinding(rep Report, fieldSub string, st Support) bool {
	for _, f := range rep.Findings {
		if strings.Contains(f.Field, fieldSub) && f.Status == st {
			return true
		}
	}
	return false
}

func TestValidateConditions(t *testing.T) {
	base := `{"trigger": {"schedule": {"interval": "5m"}}, "input": {"search": {"request": {"indices": ["app-*"], "body": {}}}}, "condition": %s}`
	tests := []struct {
		name         string
		condition    string
		wantDisabled bool
		wantField    string
		wantStatus   Support
	}{
		{"compare gte", `{"compare": {"ctx.payload.hits.total": {"gte": 100}}}`, false, "condition.compare", Supported},
		{"compare hits.total.value path", `{"compare": {"ctx.payload.hits.total.value": {"gt": 5}}}`, false, "condition.compare", Supported},
		{"compare eq unsupported", `{"compare": {"ctx.payload.hits.total": {"eq": 100}}}`, true, "condition.compare", Unsupported},
		{"compare non-total path", `{"compare": {"ctx.payload.aggregations.err.value": {"gte": 1}}}`, true, "condition.compare", Unsupported},
		{"compare multiple ops", `{"compare": {"ctx.payload.hits.total": {"gte": 1, "lte": 9}}}`, true, "condition.compare", Unsupported},
		{"compare mustache value", `{"compare": {"ctx.payload.hits.total": {"gte": "{{ctx.metadata.limit}}"}}}`, true, "condition.compare", Unsupported},
		{"script disables", `{"script": {"source": "ctx.payload.hits.total > 10"}}`, true, "condition.script", Unsupported},
		{"array_compare disables", `{"array_compare": {"ctx.payload.aggregations.x.buckets": {"path": "doc_count", "gte": {"value": 25}}}}`, true, "condition.array_compare", Unsupported},
		{"always supported", `{"always": {}}`, false, "condition.always", Supported},
		{"never imports disabled", `{"never": {}}`, true, "condition.never", Supported},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			w, err := Parse([]byte(strings.ReplaceAll(base, "%s", tt.condition)))
			if err != nil {
				t.Fatalf("Parse: %v", err)
			}
			rep := Validate(w)
			if rep.Disabled != tt.wantDisabled {
				t.Fatalf("Disabled = %v, want %v (reason %q)", rep.Disabled, tt.wantDisabled, rep.DisabledReason)
			}
			if !hasFinding(rep, tt.wantField, tt.wantStatus) {
				t.Fatalf("want %s finding on %s, got %+v", tt.wantStatus, tt.wantField, rep.Findings)
			}
		})
	}
}

func TestValidateNoConditionMeansAlways(t *testing.T) {
	w, err := Parse([]byte(`{"trigger": {"schedule": {"interval": "1m"}}, "input": {"search": {"request": {"body": {}}}}}`))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	rep := Validate(w)
	if rep.Disabled {
		t.Fatalf("no condition (= ES always) must not disable: %q", rep.DisabledReason)
	}
	if !hasFinding(rep, "condition", Supported) {
		t.Fatalf("want Supported condition finding, got %+v", rep.Findings)
	}
}

func TestValidateInputs(t *testing.T) {
	base := `{"trigger": {"schedule": {"interval": "5m"}}, "condition": {"always": {}}, "input": %s}`
	tests := []struct {
		name         string
		input        string
		wantDisabled bool
		wantField    string
		wantStatus   Support
	}{
		{"search supported", `{"search": {"request": {"indices": ["app-*"], "body": {"query": {"match_all": {}}}}}}`, false, "input.search", Supported},
		{"http unsupported", `{"http": {"request": {"host": "example.com", "port": 9200, "path": "/_cluster/health"}}}`, true, "input.http", Unsupported},
		{"simple unsupported", `{"simple": {"str": "val"}}`, true, "input.simple", Unsupported},
		{"chain unsupported", `{"chain": {"inputs": []}}`, true, "input.chain", Unsupported},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			w, err := Parse([]byte(strings.ReplaceAll(base, "%s", tt.input)))
			if err != nil {
				t.Fatalf("Parse: %v", err)
			}
			rep := Validate(w)
			if rep.Disabled != tt.wantDisabled {
				t.Fatalf("Disabled = %v, want %v (reason %q)", rep.Disabled, tt.wantDisabled, rep.DisabledReason)
			}
			if !hasFinding(rep, tt.wantField, tt.wantStatus) {
				t.Fatalf("want %s finding on %s, got %+v", tt.wantStatus, tt.wantField, rep.Findings)
			}
		})
	}
}

func TestValidateMissingInputDisables(t *testing.T) {
	w, err := Parse([]byte(`{"trigger": {"schedule": {"interval": "5m"}}, "condition": {"always": {}}}`))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	rep := Validate(w)
	if !rep.Disabled {
		t.Fatal("watch without input.search must import disabled")
	}
}

func TestValidateActions(t *testing.T) {
	base := `{"trigger": {"schedule": {"interval": "5m"}}, "input": {"search": {"request": {"body": {}}}}, "condition": {"always": {}}, "actions": %s}`
	tests := []struct {
		name       string
		actions    string
		wantField  string
		wantStatus Support
	}{
		{"email", `{"a": {"email": {"to": "x@y.z"}}}`, "actions.a.email", Supported},
		{"webhook", `{"a": {"webhook": {"host": "h"}}}`, "actions.a.webhook", Supported},
		{"slack", `{"a": {"slack": {"message": {"text": "hi"}}}}`, "actions.a.slack", Supported},
		{"logging", `{"a": {"logging": {"text": "fired"}}}`, "actions.a.logging", Supported},
		{"index write-back", `{"a": {"index": {"index": "alerts"}}}`, "actions.a.index", Unsupported},
		{"unknown kind", `{"a": {"pagerduty": {"description": "x"}}}`, "actions.a.pagerduty", Unsupported},
		{"per-action throttle", `{"a": {"throttle_period": "1m", "email": {"to": "x@y.z"}}}`, "actions.a.throttle_period", Partial},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			w, err := Parse([]byte(strings.ReplaceAll(base, "%s", tt.actions)))
			if err != nil {
				t.Fatalf("Parse: %v", err)
			}
			rep := Validate(w)
			if rep.Disabled {
				t.Fatalf("actions must never disable the rule: %q", rep.DisabledReason)
			}
			if !hasFinding(rep, tt.wantField, tt.wantStatus) {
				t.Fatalf("want %s finding on %s, got %+v", tt.wantStatus, tt.wantField, rep.Findings)
			}
		})
	}
}

func TestValidateCronUnsupported(t *testing.T) {
	w, err := Parse([]byte(`{"trigger": {"schedule": {"cron": "0 0 2 * * ?"}}, "input": {"search": {"request": {"body": {}}}}, "condition": {"always": {}}}`))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	rep := Validate(w)
	if !hasFinding(rep, "trigger.schedule.cron", Unsupported) {
		t.Fatalf("want Unsupported cron finding, got %+v", rep.Findings)
	}
	if rep.Disabled {
		t.Fatal("cron alone must not disable (continuous evaluation approximates it)")
	}
}

// cron→interval (v0.9.x, review improvement a): the fixed-rate Quartz
// subset real prod watches use maps losslessly onto IntervalSec;
// calendar-shaped expressions stay unmapped (0).
func TestCronIntervalSec(t *testing.T) {
	tests := []struct {
		expr string
		want uint32
	}{
		{"0 0/10 * * * ?", 600},  // every 10 minutes (the prod fixture)
		{"0 */5 * * * ?", 300},   // */N spelling of the same rate
		{"0 0/1 * * * ?", 60},    // every minute
		{"0 0 * * * ?", 3600},    // hourly
		{"0 0 0/2 * * ?", 7200},  // every 2 hours
		{"0 0 */6 * * ?", 21600}, // every 6 hours
		{"0 0/10 * * * ? *", 600}, // 7-field form with wildcard year
		// calendar shapes — NOT fixed-rate:
		{"0 0 2 * * ?", 0},           // daily at 02:00
		{"0 15 10 ? * MON-FRI", 0},   // weekday-gated
		{"0 0/10 2 * * ?", 0},        // every 10m ONLY during hour 2
		{"30 0/10 * * * ?", 0},       // nonzero seconds offset
		{"0 0/10 * 15 * ?", 0},       // day-of-month gated
		{"0 0/10 * * JAN ?", 0},      // month gated
		{"0 0/10 * * * ? 2026", 0},   // constrained year
		{"0 5/10 * * * ?", 0},        // offset start — not a plain rate
		{"0 0/0 * * * ?", 0},         // degenerate step
		{"* * * * * ?", 0},           // every second — nonsense as a rule cadence
		{"0 0/10 * * *", 0},          // 5 fields is not Quartz
		{"", 0},
	}
	for _, tt := range tests {
		t.Run(tt.expr, func(t *testing.T) {
			if got := cronIntervalSec([]string{tt.expr}); got != tt.want {
				t.Fatalf("cronIntervalSec(%q) = %d, want %d", tt.expr, got, tt.want)
			}
		})
	}
	if got := cronIntervalSec([]string{"0 0/10 * * * ?", "0 0/5 * * * ?"}); got != 0 {
		t.Fatalf("multiple cron expressions cannot map to one rate, got %d", got)
	}
	if got := cronIntervalSec(nil); got != 0 {
		t.Fatalf("nil cron list = %d, want 0", got)
	}
}

func TestValidateCronFixedRateSupported(t *testing.T) {
	w, err := Parse([]byte(`{"trigger": {"schedule": {"cron": "0 0/10 * * * ?"}}, "input": {"search": {"request": {"body": {"query": {"match_all": {}}}}}}, "condition": {"always": {}}}`))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	rep := Validate(w)
	if !hasFinding(rep, "trigger.schedule.cron", Supported) {
		t.Fatalf("fixed-rate cron must report Supported, got %+v", rep.Findings)
	}
	for _, f := range rep.Findings {
		if f.Field == "trigger.schedule.cron" && !strings.Contains(f.Reason, "interval") {
			t.Fatalf("cron finding must say it mapped to an interval: %q", f.Reason)
		}
	}
	if rep.Disabled {
		t.Fatalf("mapped cron must not disable: %q", rep.DisabledReason)
	}
}

// Review F10 (v0.9.x): a body-less input.search (template-driven or
// just missing) has nothing executable — Supported+enabled used to
// let it count ALL docs over the injected 24h match-all window.
func TestValidateBodylessSearchDisables(t *testing.T) {
	tests := []struct {
		name      string
		raw       string
		wantField string
	}{
		{"no body at all",
			`{"trigger": {"schedule": {"interval": "5m"}}, "input": {"search": {"request": {"indices": ["app-*"]}}}, "condition": {"always": {}}}`,
			"input.search.request.body"},
		{"template-only search",
			`{"trigger": {"schedule": {"interval": "5m"}}, "input": {"search": {"request": {"indices": ["app-*"], "template": {"id": "my-tpl"}}}}, "condition": {"always": {}}}`,
			"input.search.request.template"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			raw := []byte(tt.raw)
			w, err := Parse(raw)
			if err != nil {
				t.Fatalf("Parse: %v", err)
			}
			r, rep, err := ToRule(w, raw)
			if err != nil {
				t.Fatalf("ToRule: %v", err)
			}
			if r.Enabled || !rep.Disabled {
				t.Fatalf("body-less search must import disabled (report %+v)", rep)
			}
			if !hasFinding(rep, tt.wantField, Unsupported) {
				t.Fatalf("want Unsupported %s finding, got %+v", tt.wantField, rep.Findings)
			}
		})
	}
}

// Review F12 (v0.9.x): mustache in the SEARCH BODY is never rendered —
// ES would 400 on the literal placeholder every run, so the honest
// verdict is Unsupported + disabled. Mustache in ACTIONS stays
// Supported (actions never execute).
func TestValidateBodyMustacheDisables(t *testing.T) {
	raw := []byte(`{
		"trigger": {"schedule": {"interval": "5m"}},
		"input": {"search": {"request": {"indices": ["app-*"], "body": {"query": {"range": {"@timestamp": {"gte": "{{ctx.trigger.scheduled_time}}||-5m"}}}}}}},
		"condition": {"compare": {"ctx.payload.hits.total": {"gte": 10}}},
		"actions": {"mail": {"email": {"to": "x@y.z", "subject": "{{ctx.payload.hits.total}} hits"}}}
	}`)
	w, err := Parse(raw)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	r, rep, err := ToRule(w, raw)
	if err != nil {
		t.Fatalf("ToRule: %v", err)
	}
	if r.Enabled || !rep.Disabled {
		t.Fatalf("mustache body must import disabled (report %+v)", rep)
	}
	if !hasFinding(rep, "input.search.request.body", Unsupported) {
		t.Fatalf("want Unsupported body finding, got %+v", rep.Findings)
	}
	if !strings.Contains(rep.DisabledReason, "mustache") {
		t.Fatalf("reason must name mustache: %q", rep.DisabledReason)
	}
	if !hasFinding(rep, "actions.mail.email", Supported) {
		t.Fatalf("action mustache must stay Supported, got %+v", rep.Findings)
	}
}

// Ignored-by-design request fields (v0.9.x, review improvement c):
// search_type / rest_total_hits_as_int don't affect execution —
// Coremetry issues the search itself — so they report Supported with
// an "ignored by design" note instead of a scary Partial.
func TestValidateIgnoredRequestFields(t *testing.T) {
	raw := []byte(`{
		"trigger": {"schedule": {"interval": "5m"}},
		"input": {"search": {"request": {"search_type": "query_then_fetch", "rest_total_hits_as_int": true, "indices": ["app-*"], "body": {"query": {"match_all": {}}}}}},
		"condition": {"always": {}}
	}`)
	w, err := Parse(raw)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	rep := Validate(w)
	for _, field := range []string{"input.search.request.search_type", "input.search.request.rest_total_hits_as_int"} {
		if !hasFinding(rep, field, Supported) {
			t.Fatalf("want Supported ignored-by-design finding for %s, got %+v", field, rep.Findings)
		}
		for _, f := range rep.Findings {
			if f.Field == field && !strings.Contains(f.Reason, "ignored by design") {
				t.Fatalf("%s reason must say ignored by design: %q", field, f.Reason)
			}
		}
	}
	if rep.Disabled {
		t.Fatalf("ignored request fields must not disable: %q", rep.DisabledReason)
	}
}

// NormalizeDevTools (v0.9.x, review improvement b): Kibana DevTools
// """triple-quoted""" strings become escaped JSON strings; input
// without """ passes through byte-identical (verbatim contract).
func TestNormalizeDevTools(t *testing.T) {
	t.Run("no triple quotes → byte-identical", func(t *testing.T) {
		in := []byte(typicalWatch)
		if out := NormalizeDevTools(in); string(out) != string(in) {
			t.Fatal("input without triple quotes must pass through unchanged")
		}
	})
	t.Run("quotes, newline and mustache inside the block", func(t *testing.T) {
		in := []byte("{\"actions\":{\"a\":{\"email\":{\"body\":{\"text\":\"\"\"{{ctx.payload.hits.total}} \"quoted\" logs\nsecond line\"\"\"}}}}}")
		out := NormalizeDevTools(in)
		var m map[string]any
		if err := json.Unmarshal(out, &m); err != nil {
			t.Fatalf("normalized output is not valid JSON: %v\n%s", err, out)
		}
		text := m["actions"].(map[string]any)["a"].(map[string]any)["email"].(map[string]any)["body"].(map[string]any)["text"].(string)
		want := "{{ctx.payload.hits.total}} \"quoted\" logs\nsecond line"
		if text != want {
			t.Fatalf("content = %q, want %q", text, want)
		}
	})
	t.Run("triple quotes inside a normal string are untouched", func(t *testing.T) {
		in := []byte(`{"a": "x \" y", "b": 1}`)
		if out := NormalizeDevTools(in); string(out) != string(in) {
			t.Fatalf("escaped-quote string mangled: %s", out)
		}
	})
	t.Run("unterminated block left for Parse to reject", func(t *testing.T) {
		in := []byte(`{"a": """never closed}`)
		out := NormalizeDevTools(in)
		if _, err := Parse(out); err == nil {
			t.Fatal("unterminated triple quote must still fail Parse")
		}
	})
}

func TestValidateTransformUnsupported(t *testing.T) {
	raw := `{"trigger": {"schedule": {"interval": "5m"}}, "input": {"search": {"request": {"body": {}}}}, "condition": {"always": {}}, "transform": {"script": "return ctx.payload"}}`
	w, err := Parse([]byte(raw))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	rep := Validate(w)
	if !hasFinding(rep, "transform", Unsupported) {
		t.Fatalf("want Unsupported transform finding, got %+v", rep.Findings)
	}
	if rep.Disabled {
		t.Fatal("transform must not disable (it only shapes action payloads)")
	}
}

func TestToRuleTypical(t *testing.T) {
	raw := []byte(typicalWatch)
	w, err := Parse(raw)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	r, rep, err := ToRule(w, raw)
	if err != nil {
		t.Fatalf("ToRule: %v", err)
	}
	if rep.Disabled {
		t.Fatalf("typical watch must import enabled: %q", rep.DisabledReason)
	}
	if r.Name != "app error surge" {
		t.Fatalf("Name = %q, want metadata.name", r.Name)
	}
	if r.Metric != "watcher" {
		t.Fatalf("Metric = %q, want watcher", r.Metric)
	}
	if r.Comparator != ">=" || r.Threshold != 100 {
		t.Fatalf("comparator/threshold = %q/%v, want >=/100", r.Comparator, r.Threshold)
	}
	if r.WindowSec != 300 {
		t.Fatalf("WindowSec = %d, want 300 (5m interval)", r.WindowSec)
	}
	if r.CooldownSec != 600 {
		t.Fatalf("CooldownSec = %d, want 600 (10m throttle)", r.CooldownSec)
	}
	if !r.Enabled {
		t.Fatal("Enabled = false, want true")
	}
	if r.Service != "" {
		t.Fatalf("Service = %q, want empty", r.Service)
	}
	if r.WatcherJSON != string(raw) {
		t.Fatal("WatcherJSON must round-trip the exact raw bytes")
	}
	if r.Severity != "warning" {
		t.Fatalf("Severity = %q, want warning default", r.Severity)
	}
}

func TestToRuleComparatorMapping(t *testing.T) {
	base := `{"trigger": {"schedule": {"interval": "1m"}}, "input": {"search": {"request": {"body": {}}}}, "condition": {"compare": {"ctx.payload.hits.total": {%s}}}}`
	tests := []struct {
		op        string
		wantCmp   string
		wantThr   float64
	}{
		{`"gte": 100`, ">=", 100},
		{`"gt": 10`, ">", 10},
		{`"lte": 5`, "<=", 5},
		{`"lt": 1`, "<", 1},
	}
	for _, tt := range tests {
		t.Run(tt.op, func(t *testing.T) {
			raw := []byte(strings.ReplaceAll(base, "%s", tt.op))
			w, err := Parse(raw)
			if err != nil {
				t.Fatalf("Parse: %v", err)
			}
			r, _, err := ToRule(w, raw)
			if err != nil {
				t.Fatalf("ToRule: %v", err)
			}
			if r.Comparator != tt.wantCmp || r.Threshold != tt.wantThr {
				t.Fatalf("got %q/%v, want %q/%v", r.Comparator, r.Threshold, tt.wantCmp, tt.wantThr)
			}
		})
	}
}

func TestToRuleDisabledVariants(t *testing.T) {
	tests := []struct {
		name string
		raw  string
	}{
		{"script condition", `{"trigger": {"schedule": {"interval": "5m"}}, "input": {"search": {"request": {"body": {}}}}, "condition": {"script": {"source": "return true"}}}`},
		{"never condition", `{"trigger": {"schedule": {"interval": "5m"}}, "input": {"search": {"request": {"body": {}}}}, "condition": {"never": {}}}`},
		{"http input", `{"trigger": {"schedule": {"interval": "5m"}}, "input": {"http": {"request": {"host": "h", "port": 1}}}, "condition": {"always": {}}}`},
		{"eq compare", `{"trigger": {"schedule": {"interval": "5m"}}, "input": {"search": {"request": {"body": {}}}}, "condition": {"compare": {"ctx.payload.hits.total": {"eq": 3}}}}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			raw := []byte(tt.raw)
			w, err := Parse(raw)
			if err != nil {
				t.Fatalf("Parse: %v", err)
			}
			r, rep, err := ToRule(w, raw)
			if err != nil {
				t.Fatalf("ToRule: %v", err)
			}
			if r.Enabled {
				t.Fatalf("Enabled = true, want disabled (report: %+v)", rep)
			}
			if !rep.Disabled || rep.DisabledReason == "" {
				t.Fatalf("report must carry Disabled + reason, got %+v", rep)
			}
		})
	}
}

func TestToRuleAlwaysMapsToThresholdZero(t *testing.T) {
	raw := []byte(`{"trigger": {"schedule": {"interval": "1m"}}, "input": {"search": {"request": {"body": {}}}}, "condition": {"always": {}}}`)
	w, err := Parse(raw)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	r, _, err := ToRule(w, raw)
	if err != nil {
		t.Fatalf("ToRule: %v", err)
	}
	if r.Comparator != ">=" || r.Threshold != 0 {
		t.Fatalf("always must map to >= 0, got %q/%v", r.Comparator, r.Threshold)
	}
	if !r.Enabled {
		t.Fatal("always watch must import enabled")
	}
}

func TestToRuleDefaults(t *testing.T) {
	// No schedule, no throttle, no metadata: window defaults to 5m,
	// cooldown 0, name empty (import caller assigns the watch id).
	raw := []byte(`{"input": {"search": {"request": {"body": {}}}}, "condition": {"compare": {"ctx.payload.hits.total": {"gte": 1}}}}`)
	w, err := Parse(raw)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	r, _, err := ToRule(w, raw)
	if err != nil {
		t.Fatalf("ToRule: %v", err)
	}
	if r.WindowSec != 300 {
		t.Fatalf("WindowSec = %d, want 300 default", r.WindowSec)
	}
	if r.CooldownSec != 0 {
		t.Fatalf("CooldownSec = %d, want 0", r.CooldownSec)
	}
	if r.Name != "" {
		t.Fatalf("Name = %q, want empty (caller assigns)", r.Name)
	}
}

func TestToRuleThrottleMillis(t *testing.T) {
	raw := []byte(`{"trigger": {"schedule": {"interval": "1m"}}, "input": {"search": {"request": {"body": {}}}}, "condition": {"always": {}}, "throttle_period_in_millis": 120000}`)
	w, err := Parse(raw)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	r, _, err := ToRule(w, raw)
	if err != nil {
		t.Fatalf("ToRule: %v", err)
	}
	if r.CooldownSec != 120 {
		t.Fatalf("CooldownSec = %d, want 120 (millis form)", r.CooldownSec)
	}
}

func TestToRuleNilWatch(t *testing.T) {
	if _, _, err := ToRule(nil, []byte(`{}`)); err == nil {
		t.Fatal("ToRule(nil): want error")
	}
}

func TestParseIndicesSingleString(t *testing.T) {
	// Some exported watches carry "indices": "app-*" as a bare string.
	w, err := Parse([]byte(`{"input": {"search": {"request": {"indices": "app-*", "body": {}}}}}`))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if got := w.Input.Search.Request.Indices; len(got) != 1 || got[0] != "app-*" {
		t.Fatalf("indices = %v, want [app-*]", got)
	}
}

// ── Anonymized real-prod fixture (v0.9.x) ───────────────────────────────────

// prodShapeWatch is the ANONYMIZED real-prod watcher shape the Faz-1
// importer must swallow end-to-end: Quartz fixed-rate cron, wildcard
// indices, search_type + rest_total_hits_as_int request flags, a
// filter-context range (positive — no 24h injection, pinned in
// logstore's TestInjectRawSearchGuardsProdShapeBody), a nested
// bool.should, a plain hits.total gte compare, and mustache ONLY in
// the (never-executed) email action. No customer trace — do not
// substitute real names.
const prodShapeWatch = `{"trigger":{"schedule":{"cron":"0 0/10 * * * ?"}},"input":{"search":{"request":{"search_type":"query_then_fetch","indices":["app*-digital"],"rest_total_hits_as_int":true,"body":{"query":{"bool":{"must":[],"filter":[{"range":{"@timestamp":{"gte":"now-10m"}}},{"bool":{"should":[{"match_phrase":{"message":"EventAlreadyExistError"}}],"minimum_should_match":1}},{"match_phrase":{"kubernetes.container_name":"digital-core-prod"}}]}}}}}},"condition":{"compare":{"ctx.payload.hits.total":{"gte":5}}},"actions":{"email_team":{"email":{"profile":"standard","priority":"high","to":["alerts@example.com"],"subject":"[ALERT] digital-core error spike","body":{"text":"{{ctx.payload.hits.total}} error logs. MESSAGE: {{ctx.payload.hits.hits.0._source.message}}"}}}}}`

func TestProdShapeWatchEndToEnd(t *testing.T) {
	raw := []byte(prodShapeWatch)
	w, err := Parse(raw)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}

	// cron → 600s interval (Supported, not the old Unsupported+5m).
	if got := w.IntervalSec(); got != 600 {
		t.Fatalf("IntervalSec = %d, want 600 (cron 0 0/10 * * * ?)", got)
	}

	r, rep, err := ToRule(w, raw)
	if err != nil {
		t.Fatalf("ToRule: %v", err)
	}
	if rep.Disabled || !r.Enabled {
		t.Fatalf("prod-shape watch must import ENABLED (no mustache in the body); report: %+v", rep)
	}
	if rep.HasUnsupported() {
		t.Fatalf("prod-shape watch must map without Unsupported findings: %+v", rep.Findings)
	}
	if r.WindowSec != 600 {
		t.Fatalf("WindowSec = %d, want 600", r.WindowSec)
	}
	if r.Comparator != ">=" || r.Threshold != 5 {
		t.Fatalf("condition = %s %v, want >= 5", r.Comparator, r.Threshold)
	}
	if r.WatcherJSON != prodShapeWatch {
		t.Fatal("WatcherJSON must round-trip the exact raw bytes")
	}
	if got := w.Input.Search.Request.Indices; len(got) != 1 || got[0] != "app*-digital" {
		t.Fatalf("indices = %v, want [app*-digital]", got)
	}

	// Field-by-field expectations.
	wantSupported := []string{
		"trigger.schedule.cron",                        // cron→interval eşlendi
		"condition.compare",                            // gte 5 on hits.total
		"input.search",                                 // body verbatim, no mustache
		"input.search.request.search_type",             // ignored by design
		"input.search.request.rest_total_hits_as_int",  // ignored by design
		"actions.email_team.email",                     // mustache OK — never executed
	}
	for _, field := range wantSupported {
		if !hasFinding(rep, field, Supported) {
			t.Fatalf("want Supported finding for %s, got %+v", field, rep.Findings)
		}
	}
	for _, f := range rep.Findings {
		switch f.Field {
		case "actions.email_team.email":
			if !strings.Contains(f.Reason, "mustache") {
				t.Fatalf("email action reason must reassure about mustache: %q", f.Reason)
			}
		case "input.search.request.search_type", "input.search.request.rest_total_hits_as_int":
			if !strings.Contains(f.Reason, "ignored by design") {
				t.Fatalf("%s must be ignored-by-design, got %q", f.Field, f.Reason)
			}
		}
	}
}

// The same fixture as a Kibana DevTools paste: the email text arrives
// triple-quoted (with embedded quotes + newline + mustache), which is
// not valid JSON until NormalizeDevTools converts it.
func TestProdShapeWatchDevToolsVariant(t *testing.T) {
	devtools := strings.Replace(prodShapeWatch,
		`"text":"{{ctx.payload.hits.total}} error logs. MESSAGE: {{ctx.payload.hits.hits.0._source.message}}"`,
		"\"text\":\"\"\"{{ctx.payload.hits.total}} \"error\" logs.\nMESSAGE: {{ctx.payload.hits.hits.0._source.message}}\"\"\"", 1)
	if devtools == prodShapeWatch {
		t.Fatal("fixture rewrite failed")
	}
	if _, err := Parse([]byte(devtools)); err == nil {
		t.Fatal("sanity: the raw DevTools variant must NOT be valid JSON")
	}
	norm := NormalizeDevTools([]byte(devtools))
	w, err := Parse(norm)
	if err != nil {
		t.Fatalf("Parse(normalized): %v", err)
	}
	r, rep, err := ToRule(w, norm)
	if err != nil {
		t.Fatalf("ToRule: %v", err)
	}
	if rep.Disabled || !r.Enabled {
		t.Fatalf("DevTools variant must import enabled, report: %+v", rep)
	}
	if got := w.IntervalSec(); got != 600 {
		t.Fatalf("IntervalSec = %d, want 600", got)
	}
	if w.Actions["email_team"].Email == nil {
		t.Fatal("email action lost through normalization")
	}
	// The stored definition is the NORMALIZED form (valid JSON — the
	// evaluator re-parses it every run).
	if r.WatcherJSON != string(norm) {
		t.Fatal("WatcherJSON must store the normalized bytes")
	}
	var chk map[string]any
	if err := json.Unmarshal([]byte(r.WatcherJSON), &chk); err != nil {
		t.Fatalf("stored definition must be valid JSON: %v", err)
	}
}
