package api

// v0.8.397 (AI audit A3) — guided chat mode regression tests. The
// deterministic intent router + the pure Turkish evidence renderers
// are the load-bearing halves of the small-model (qwen3.5-2b) guided
// path: WE decide what data the question needs, so a routing bug
// silently sends the operator to the wrong bundle. Pure functions
// only — no store, no LLM.

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/cilcenk/coremetry/internal/anomaly"
	"github.com/cilcenk/coremetry/internal/chstore"
	"github.com/cilcenk/coremetry/internal/logstore"
)

// The live service list every routing test matches entities against.
var guidedTestServices = []string{
	"checkout-service",
	"payment-service",
	"mobile-bff",
	"mobile-bff-uat",
	"ledger-service",
	"auth-service",
}

// The live environment list (v0.8.398) — mirrors what ListEnvironments
// returns on the demo install (count-ordered, busiest first).
var guidedTestEnvs = []string{"prod", "uat", "int", "prep"}

func TestRouteGuidedIntent(t *testing.T) {
	cases := []struct {
		name    string
		msg     string
		intent  guidedIntent
		service string
		env     string
	}{
		// (a) errors/problems now — Turkish + English.
		{"errors english (canned suggestion)", "Show me errors in the last hour", guidedProblems, "", ""},
		{"errors turkish", "son 1 saatte hatalar var mı", guidedProblems, "", ""},
		{"problems turkish", "şu an açık problemler neler", guidedProblems, "", ""},
		{"alerts english", "any alerts firing right now?", guidedProblems, "", ""},
		{"problems agglutinated", "sorunları göster", guidedProblems, "", ""},
		{"problems scoped to service", "payment-service için açık problem var mı", guidedProblems, "payment-service", ""},
		{"incident english", "is there an incident going on", guidedProblems, "", ""},

		// (b) service health — needs a live-list entity.
		{"health turkish smoke", "checkout servisi yavaş mı", guidedServiceHealth, "checkout-service", ""},
		{"health turkish sağlık", "payment-service sağlığı nasıl", guidedServiceHealth, "payment-service", ""},
		{"health english", "is payment-service healthy", guidedServiceHealth, "payment-service", ""},
		{"health suffixed name wins", "mobile-bff-uat sağlığı nasıl", guidedServiceHealth, "mobile-bff-uat", ""},
		{"health base name not shadowed", "mobile-bff yavaş mı", guidedServiceHealth, "mobile-bff", ""},
		{"health apostrophe suffix", "checkout-service'in durumu ne", guidedServiceHealth, "checkout-service", ""},
		{"errors on a service routes to health", "checkout servisinde hata var mı", guidedServiceHealth, "checkout-service", ""},
		{"why slow english", "why is ledger-service slow", guidedServiceHealth, "ledger-service", ""},

		// (c) slowest traces.
		{"slowest turkish", "en yavaş traceler hangileri", guidedSlowTraces, "", ""},
		{"slowest english scoped", "show me the slowest traces for checkout-service", guidedSlowTraces, "checkout-service", ""},
		{"slowest turkish scoped prefix", "checkout için en yavaş istekler", guidedSlowTraces, "checkout-service", ""},
		{"slow traces english", "slow traces in the last hour", guidedSlowTraces, "", ""},

		// (d) deploy impact.
		{"deploy turkish", "son deploy etkisi ne oldu", guidedDeployImpact, "", ""},
		{"deploy english scoped", "did the last deploy of payment-service regress latency", guidedDeployImpact, "payment-service", ""},
		{"rollout english", "any bad rollouts today", guidedDeployImpact, "", ""},
		{"sürüm turkish", "yeni sürüm sonrası durum nasıl", guidedDeployImpact, "", ""},

		// (e) log errors — needs BOTH a log token and an error token.
		{"log errors turkish", "log hataları neler", guidedLogErrors, "", ""},
		{"log errors turkish agglutinated", "checkout loglarında hata var mı", guidedLogErrors, "checkout-service", ""},
		{"log errors english", "log errors for mobile-bff", guidedLogErrors, "mobile-bff", ""},
		{"logs without error word is not log_errors", "checkout servisinin durumu nasıl", guidedServiceHealth, "checkout-service", ""},
		// "login" must NOT trip the token-bounded log signal.
		{"login is not log", "login hataları var mı", guidedProblems, "", ""},

		// (f) env extraction (v0.8.398) — TR + EN phrasings + bare names.
		{"env problems turkish ortamındaki", "uat ortamındaki problemler neler", guidedProblems, "", "uat"},
		{"env errors turkish ortamında", "uat ortamında hatalar var mı", guidedProblems, "", "uat"},
		{"env english environment phrase", "errors in the uat environment", guidedProblems, "", "uat"},
		{"env bare name", "uat hataları", guidedProblems, "", "uat"},
		{"env apostrophe locative", "prod'daki açık problemler", guidedProblems, "", "prod"},
		{"env slow traces scoped", "prod ortamında en yavaş traceler", guidedSlowTraces, "", "prod"},
		// The audit's suffixed-service + env-in-same-sentence case: the
		// standalone "uat" sets Env, the "uat" INSIDE mobile-bff-uat is
		// boundary-rejected — no double-count in either direction.
		{"suffixed service + env same sentence", "mobile-bff-uat servisi uat ortamında yavaş mı", guidedServiceHealth, "mobile-bff-uat", "uat"},
		{"env-suffixed service alone sets no env", "mobile-bff-uat yavaş mı", guidedServiceHealth, "mobile-bff-uat", ""},
		{"unknown env ignored", "staging ortamındaki hatalar", guidedProblems, "", ""},
		{"env with deploy is honest-envless later but still routed", "uat ortamında son deploy etkisi", guidedDeployImpact, "", "uat"},
		{"env with log errors routed", "uat ortamındaki log hataları", guidedLogErrors, "", "uat"},

		// No match → fall through to the free tool loop.
		{"greeting", "merhaba", guidedNone, "", ""},
		{"smalltalk with health word but no entity", "bugün hava nasıl", guidedNone, "", ""},
		{"unrelated question", "kafka consumer lag neden artar", guidedNone, "", ""},
		{"dashboard request", "bana bir dashboard oluştur", guidedNone, "", ""},
		{"empty", "", guidedNone, "", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := routeGuidedIntent(tc.msg, guidedTestServices, guidedTestEnvs)
			if got.Intent != tc.intent {
				t.Fatalf("intent: got %q want %q (msg %q)", got.Intent, tc.intent, tc.msg)
			}
			if got.Service != tc.service {
				t.Fatalf("service: got %q want %q (msg %q)", got.Service, tc.service, tc.msg)
			}
			if got.Env != tc.env {
				t.Fatalf("env: got %q want %q (msg %q)", got.Env, tc.env, tc.msg)
			}
		})
	}
}

func TestExtractServiceEntity(t *testing.T) {
	cases := []struct {
		name     string
		msg      string
		services []string
		envs     []string
		want     string
	}{
		{"exact bounded match", "is payment-service healthy", guidedTestServices, nil, "payment-service"},
		{"longest suffixed name wins", "mobile-bff-uat çok yavaş", guidedTestServices, nil, "mobile-bff-uat"},
		{"base name when suffixed sibling exists", "mobile-bff çok yavaş", guidedTestServices, nil, "mobile-bff"},
		{"apostrophe detaches turkish suffix", "checkout-service'in p99 değeri", guidedTestServices, nil, "checkout-service"},
		{"unique prefix fallback", "checkout servisi yavaş", guidedTestServices, nil, "checkout-service"},
		{"prefix must stop at separator", "check servisi yavaş", guidedTestServices, nil, ""},
		{"ambiguous prefix returns empty", "mobile servisleri yavaş",
			[]string{"mobile-bff-uat", "mobile-bff-prod"}, nil, ""},
		{"no bare-substring inside longer name", "bff yavaş", []string{"mobile-bff"}, nil, ""},
		{"stopword never matches", "service yavaş", []string{"service-a"}, nil, ""},
		{"empty list", "checkout yavaş", nil, nil, ""},
		{"turkish token skipped (ascii-only names)", "sağlık kontrolü", []string{"saglik-servisi"}, nil, ""},
		// v0.8.398 — a live env name is never a service-PREFIX candidate:
		// "uat ortamındaki hatalar" must not resolve to uat-gateway.
		{"env name never claims a service via prefix", "uat ortamındaki hatalar",
			[]string{"uat-gateway"}, []string{"uat"}, ""},
		// …but the literal full service name still wins the bounded pass.
		{"literal env-prefixed service still matches", "uat-gateway hataları",
			[]string{"uat-gateway"}, []string{"uat"}, "uat-gateway"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := extractServiceEntity(normalizeGuidedMsg(tc.msg), tc.services, tc.envs)
			if got != tc.want {
				t.Fatalf("got %q want %q (msg %q)", got, tc.want, tc.msg)
			}
		})
	}
}

// v0.8.398 (AI audit env-awareness slice) — env-entity extraction
// against the LIVE env list: bare names, TR/EN phrasings, boundary
// discipline against env-suffixed service names, longest-match.
func TestExtractEnvEntity(t *testing.T) {
	cases := []struct {
		name string
		msg  string
		envs []string
		want string
	}{
		{"bare name", "uat hataları neler", guidedTestEnvs, "uat"},
		{"turkish ortamında phrase", "uat ortamında hatalar var mı", guidedTestEnvs, "uat"},
		{"turkish ortamındaki phrase", "uat ortamındaki problemler", guidedTestEnvs, "uat"},
		{"turkish ortamı phrase", "uat ortamı sağlıklı mı", guidedTestEnvs, "uat"},
		{"english environment phrase", "errors in the uat environment", guidedTestEnvs, "uat"},
		{"english env prefix", "env uat problems", guidedTestEnvs, "uat"},
		{"apostrophe locative", "prod'daki hatalar", guidedTestEnvs, "prod"},
		// deploy_env-suffixed SERVICE names never leak an env — '-' is a
		// name char, so the inner "uat" fails the boundary check.
		{"suffix inside service name is not an env", "mobile-bff-uat yavaş mı", guidedTestEnvs, ""},
		{"standalone token beside suffixed service counts once",
			"mobile-bff-uat servisi uat ortamında yavaş mı", guidedTestEnvs, "uat"},
		{"longest env wins", "preprod ortamında hata var mı", []string{"prod", "preprod"}, "preprod"},
		{"prod never matches inside preprod", "preprod ortamında hata", []string{"prod"}, ""},
		{"unknown env ignored", "staging ortamındaki hatalar", guidedTestEnvs, ""},
		{"original casing returned", "uat ortamındaki hatalar", []string{"UAT"}, "UAT"},
		{"single-char env skipped", "a ortamındaki hatalar", []string{"a"}, ""},
		{"empty env list", "uat ortamındaki hatalar", nil, ""},
		{"empty message", "", guidedTestEnvs, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := extractEnvEntity(normalizeGuidedMsg(tc.msg), tc.envs)
			if got != tc.want {
				t.Fatalf("got %q want %q (msg %q)", got, tc.want, tc.msg)
			}
		})
	}
}

// v0.8.398 — bundle threading: the prefetch filters must carry the
// routed env into the store reads (ProblemFilter.Env service-scoped,
// TraceFilter.Env deploy_env conjunct). A regression that drops Env
// here silently un-scopes the guided answer.
func TestGuidedBundleFilterThreading(t *testing.T) {
	pf := guidedProblemFilter("payment-service", "uat", 50)
	if pf.Status != "open" || pf.Service != "payment-service" || pf.Env != "uat" || pf.Limit != 50 {
		t.Fatalf("problem filter wrong: %+v", pf)
	}
	if pf := guidedProblemFilter("", "", 10); pf.Env != "" || pf.Service != "" || pf.Limit != 10 {
		t.Fatalf("env-less problem filter wrong: %+v", pf)
	}
	from := time.Now().Add(-time.Hour)
	to := time.Now()
	tf := guidedTraceFilter("checkout-service", "prod", from, to)
	if tf.Service != "checkout-service" || tf.Env != "prod" ||
		tf.Sort != "duration" || tf.Order != "desc" || tf.Limit != 10 || tf.CountMode != "skip" {
		t.Fatalf("trace filter wrong: %+v", tf)
	}
	if !tf.From.Equal(from) || !tf.To.Equal(to) {
		t.Fatalf("trace filter window wrong: %+v", tf)
	}
	if tf := guidedTraceFilter("", "", from, to); tf.Env != "" {
		t.Fatalf("env-less trace filter carries env: %+v", tf)
	}
}

// v0.8.398 — the step-event args echo: env appended only when applied,
// well-formed JSON either way (the chat panel renders these chips).
func TestWithEnvArg(t *testing.T) {
	cases := []struct {
		name string
		args string
		env  string
		want string
	}{
		{"no env passes through", `{"status":"open"}`, "", `{"status":"open"}`},
		{"env appended to existing args", `{"status":"open"}`, "uat", `{"status":"open","env":"uat"}`},
		{"env on empty args", "", "uat", `{"env":"uat"}`},
		{"env on empty object", "{}", "prod", `{"env":"prod"}`},
		{"service+sort keeps shape", `{"service":"x","sort":"duration"}`, "int", `{"service":"x","sort":"duration","env":"int"}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := withEnvArg(tc.args, tc.env)
			if got != tc.want {
				t.Fatalf("withEnvArg(%q, %q) = %q, want %q", tc.args, tc.env, got, tc.want)
			}
			var parsed map[string]any
			if err := json.Unmarshal([]byte(got), &parsed); err != nil {
				t.Fatalf("withEnvArg output not valid JSON: %q (%v)", got, err)
			}
		})
	}
}

// v0.8.398 — the honest env-less note for bundles whose data path has
// no env dimension yet (logs + deploy markers, Phase 4 pending).
func TestGuidedEnvlessNoteTR(t *testing.T) {
	if got := guidedEnvlessNoteTR("log verisi", ""); got != "" {
		t.Fatalf("empty env must render no note, got %q", got)
	}
	got := guidedEnvlessNoteTR("log verisi", "uat")
	for _, want := range []string{"log verisi", `"uat"`, "UYGULANMADI", "tüm ortamların toplamı"} {
		if !strings.Contains(got, want) {
			t.Fatalf("envless note missing %q in %q", want, got)
		}
	}
	if got := guidedEnvlessNoteTR("deploy işaretçileri", "prod"); !strings.Contains(got, "deploy işaretçileri") || !strings.Contains(got, `"prod"`) {
		t.Fatalf("deploy envless note wrong: %q", got)
	}
}

// v0.8.398 — shared scope fragment for the evidence headers.
func TestGuidedScopeTR(t *testing.T) {
	cases := []struct {
		service, env, want string
	}{
		{"", "", ""},
		{"checkout-service", "", ", servis: checkout-service"},
		{"", "uat", ", ortam: uat"},
		{"checkout-service", "uat", ", servis: checkout-service, ortam: uat"},
	}
	for _, tc := range cases {
		if got := guidedScopeTR(tc.service, tc.env); got != tc.want {
			t.Fatalf("guidedScopeTR(%q, %q) = %q, want %q", tc.service, tc.env, got, tc.want)
		}
	}
}

// Every unit branch of the range template, per the Nh/Nd unit-mixing
// ship rule.
func TestGuidedRangeS(t *testing.T) {
	cases := []struct {
		msg  string
		want int64
	}{
		{"Show me errors in the last hour", 3600},
		{"son 1 saatte hatalar", 3600},
		{"son 2 saat", 7200},
		{"last 30 minutes", 1800},
		{"son 15 dk", 900},
		{"son 45 dakika", 2700},
		{"last 1 day", 86400},
		{"son 1 gün", 86400},
		{"bugün deploy oldu mu", 86400},
		{"son 5 dakika", 300},
		{"son 2 dakika (clamped up)", 300},
		{"son 3 gün (clamped down)", 86400},
		{"no window words", 1800},
	}
	for _, tc := range cases {
		if got := guidedRangeS(tc.msg); got != tc.want {
			t.Fatalf("guidedRangeS(%q) = %d, want %d", tc.msg, got, tc.want)
		}
	}
}

// Every unit branch of the age template (sn / dk / sa / sa+dk / gün /
// gün+sa), plus the negative clamp.
func TestFmtAgoTR(t *testing.T) {
	cases := []struct {
		sec  int64
		want string
	}{
		{-30, "0sn"},
		{45, "45sn"},
		{300, "5dk"},
		{3599, "59dk"},
		{7200, "2sa"},
		{5400, "1sa 30dk"},
		{86400, "1gün"},
		{2*86400 + 4*3600, "2gün 4sa"},
	}
	for _, tc := range cases {
		if got := fmtAgoTR(tc.sec); got != tc.want {
			t.Fatalf("fmtAgoTR(%d) = %q, want %q", tc.sec, got, tc.want)
		}
	}
}

func TestRenderProblemsEvidenceTR(t *testing.T) {
	now := time.Now()
	probs := []chstore.Problem{
		{
			ID: "p1", RuleName: "High error rate", Severity: "critical",
			Service: "payment-service", Value: 8.3, Threshold: 5,
			StartedAt: now.Add(-42 * time.Minute).UnixNano(),
			Priority:  "P1", PriorityReason: "critical + 2x eşik",
			RootCause: &chstore.RootCauseSummary{TopSuspect: "ledger-service", TopScore: 0.9, Confidence: 0.82},
		},
		{
			ID: "p2", RuleName: "p99 latency", Severity: "warning",
			Service: "checkout-service", Value: 900, Threshold: 500,
			StartedAt: now.Add(-2 * time.Hour).UnixNano(), Priority: "P2",
		},
	}
	out := renderProblemsEvidenceTR(probs, "", "", now)
	for _, want := range []string{
		"toplam 2 (kritik 1, warning 1, info 0)",
		"[P1] payment-service — High error rate",
		"42dk önce",
		"değer 8.30 / eşik 5.00",
		"kök-neden şüphelisi: ledger-service (güven 0.82)",
		"öncelik nedeni: critical + 2x eşik",
		"[P2] checkout-service — p99 latency",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("problems evidence missing %q in:\n%s", want, out)
		}
	}
	// The second row has no hypothesis — the root-cause fragment must
	// not leak into it.
	if strings.Count(out, "kök-neden şüphelisi") != 1 {
		t.Fatalf("root-cause fragment count wrong:\n%s", out)
	}
	if got := renderProblemsEvidenceTR(nil, "checkout-service", "", now); !strings.Contains(got, "Açık problem yok (servis: checkout-service)") {
		t.Fatalf("empty render = %q", got)
	}
	// v0.8.398 — env scope lands in the header (populated + empty).
	if got := renderProblemsEvidenceTR(probs, "", "uat", now); !strings.Contains(got, "Açık problemler (ortam: uat):") {
		t.Fatalf("env scope missing in header: %q", got)
	}
	if got := renderProblemsEvidenceTR(nil, "checkout-service", "uat", now); !strings.Contains(got, "Açık problem yok (servis: checkout-service, ortam: uat)") {
		t.Fatalf("env scope missing in empty render: %q", got)
	}
}

func TestRenderSlowTracesEvidenceTR(t *testing.T) {
	rows := []chstore.TraceRow{
		{TraceID: "abc123", RootName: "POST /api/cart", ServiceName: "checkout-service",
			DurationMs: 4521, SpanCount: 12, HasError: true},
		{TraceID: "def456", RootName: "GET /health", ServiceName: "checkout-service",
			DurationMs: 900, SpanCount: 3},
	}
	out := renderSlowTracesEvidenceTR(rows, "checkout-service", "", 1800)
	for _, want := range []string{
		"En yavaş trace'ler (son 30dk, servis: checkout-service",
		"4521ms — checkout-service / POST /api/cart (12 span, HATA) trace=abc123",
		"900ms — checkout-service / GET /health (3 span) trace=def456",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("slow-traces evidence missing %q in:\n%s", want, out)
		}
	}
	if got := renderSlowTracesEvidenceTR(nil, "", "", 3600); !strings.Contains(got, "trace bulunamadı") {
		t.Fatalf("empty render = %q", got)
	}
	// v0.8.398 — env scope lands in the header (populated + empty).
	if got := renderSlowTracesEvidenceTR(rows, "checkout-service", "uat", 1800); !strings.Contains(got, "(son 30dk, servis: checkout-service, ortam: uat") {
		t.Fatalf("env scope missing: %q", got)
	}
	if got := renderSlowTracesEvidenceTR(nil, "", "prod", 3600); !strings.Contains(got, "(son 1sa, ortam: prod)") {
		t.Fatalf("env-only scope missing in empty render: %q", got)
	}
}

func TestRenderDeployEvidenceTR(t *testing.T) {
	now := time.Now()
	refs := []guidedDeployRef{
		{Service: "payment-service", Version: "v1.4.0", TimeNs: now.Add(-23 * time.Minute).UnixNano()},
		{Service: "checkout-service", Version: "v2.1.3", TimeNs: now.Add(-3 * time.Hour).UnixNano()},
	}
	impacts := []*chstore.DeployImpact{
		{
			Service: "payment-service", Version: "v1.4.0",
			Before:      chstore.DeployImpactStats{P99Ms: 210, ErrorRate: 0.004, RPS: 40},
			After:       chstore.DeployImpactStats{P99Ms: 480, ErrorRate: 0.021, RPS: 38.2},
			P99DeltaPct: 128.6,
		},
		nil, // impact read failed / skipped — the line must still render
	}
	out := renderDeployEvidenceTR(refs, impacts, 6*time.Hour, now)
	for _, want := range []string{
		"Son deploylar (son 6sa):",
		"payment-service v1.4.0 (23dk önce)",
		"p99 210ms→480ms (%+128.6)",
		"error %0.40→%2.10",
		"rps 40.0→38.2",
		"checkout-service v2.1.3 (3sa önce)",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("deploy evidence missing %q in:\n%s", want, out)
		}
	}
	if strings.Count(out, "etki (±10dk)") != 1 {
		t.Fatalf("nil impact must not render an impact fragment:\n%s", out)
	}
	if got := renderDeployEvidenceTR(nil, nil, 6*time.Hour, now); !strings.Contains(got, "deploy görülmedi") {
		t.Fatalf("empty render = %q", got)
	}
}

func TestRenderLogErrorsEvidenceTR(t *testing.T) {
	series := []logstore.LogSeries{
		{Name: "INFO", Points: []logstore.LogPoint{{V: 84000}}},
		{Name: "ERROR", Points: []logstore.LogPoint{{V: 1000}, {V: 240}}},
		{Name: "WARN", Points: []logstore.LogPoint{{V: 3200}}},
	}
	pats := []anomaly.LogPatternAnomaly{
		{Pattern: "OOMKilled", CurrentCount: 12, BaselineCount: 1, Service: "payment-service", Kind: "spike"},
		{Pattern: "Deadlock", CurrentCount: 4, Service: "checkout-service", Kind: "new"},
	}
	out := renderLogErrorsEvidenceTR(series, pats, "payment-service", 1800)
	for _, want := range []string{
		"Log severity dağılımı (son 30dk, servis: payment-service)",
		"ERROR 1240",
		"(toplam 88440)",
		"OOMKilled ×12 (payment-service, spike, baseline 1)",
		"Deadlock ×4 (checkout-service, new)",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("log evidence missing %q in:\n%s", want, out)
		}
	}
	// Worst-first severity ordering: ERROR must precede WARN and INFO.
	if ei, wi := strings.Index(out, "ERROR"), strings.Index(out, "WARN"); ei > wi {
		t.Fatalf("severity order wrong (ERROR after WARN):\n%s", out)
	}
	empty := renderLogErrorsEvidenceTR(nil, nil, "", 3600)
	if !strings.Contains(empty, "bu pencerede log yok") || !strings.Contains(empty, "eşleşme yok") {
		t.Fatalf("empty render = %q", empty)
	}
}
