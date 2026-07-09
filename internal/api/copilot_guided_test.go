package api

// v0.8.397 (AI audit A3) — guided chat mode regression tests. The
// deterministic intent router + the pure Turkish evidence renderers
// are the load-bearing halves of the small-model (qwen3.5-2b) guided
// path: WE decide what data the question needs, so a routing bug
// silently sends the operator to the wrong bundle. Pure functions
// only — no store, no LLM.

import (
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

func TestRouteGuidedIntent(t *testing.T) {
	cases := []struct {
		name    string
		msg     string
		intent  guidedIntent
		service string
	}{
		// (a) errors/problems now — Turkish + English.
		{"errors english (canned suggestion)", "Show me errors in the last hour", guidedProblems, ""},
		{"errors turkish", "son 1 saatte hatalar var mı", guidedProblems, ""},
		{"problems turkish", "şu an açık problemler neler", guidedProblems, ""},
		{"alerts english", "any alerts firing right now?", guidedProblems, ""},
		{"problems agglutinated", "sorunları göster", guidedProblems, ""},
		{"problems scoped to service", "payment-service için açık problem var mı", guidedProblems, "payment-service"},
		{"incident english", "is there an incident going on", guidedProblems, ""},

		// (b) service health — needs a live-list entity.
		{"health turkish smoke", "checkout servisi yavaş mı", guidedServiceHealth, "checkout-service"},
		{"health turkish sağlık", "payment-service sağlığı nasıl", guidedServiceHealth, "payment-service"},
		{"health english", "is payment-service healthy", guidedServiceHealth, "payment-service"},
		{"health suffixed name wins", "mobile-bff-uat sağlığı nasıl", guidedServiceHealth, "mobile-bff-uat"},
		{"health base name not shadowed", "mobile-bff yavaş mı", guidedServiceHealth, "mobile-bff"},
		{"health apostrophe suffix", "checkout-service'in durumu ne", guidedServiceHealth, "checkout-service"},
		{"errors on a service routes to health", "checkout servisinde hata var mı", guidedServiceHealth, "checkout-service"},
		{"why slow english", "why is ledger-service slow", guidedServiceHealth, "ledger-service"},

		// (c) slowest traces.
		{"slowest turkish", "en yavaş traceler hangileri", guidedSlowTraces, ""},
		{"slowest english scoped", "show me the slowest traces for checkout-service", guidedSlowTraces, "checkout-service"},
		{"slowest turkish scoped prefix", "checkout için en yavaş istekler", guidedSlowTraces, "checkout-service"},
		{"slow traces english", "slow traces in the last hour", guidedSlowTraces, ""},

		// (d) deploy impact.
		{"deploy turkish", "son deploy etkisi ne oldu", guidedDeployImpact, ""},
		{"deploy english scoped", "did the last deploy of payment-service regress latency", guidedDeployImpact, "payment-service"},
		{"rollout english", "any bad rollouts today", guidedDeployImpact, ""},
		{"sürüm turkish", "yeni sürüm sonrası durum nasıl", guidedDeployImpact, ""},

		// (e) log errors — needs BOTH a log token and an error token.
		{"log errors turkish", "log hataları neler", guidedLogErrors, ""},
		{"log errors turkish agglutinated", "checkout loglarında hata var mı", guidedLogErrors, "checkout-service"},
		{"log errors english", "log errors for mobile-bff", guidedLogErrors, "mobile-bff"},
		{"logs without error word is not log_errors", "checkout servisinin durumu nasıl", guidedServiceHealth, "checkout-service"},
		// "login" must NOT trip the token-bounded log signal.
		{"login is not log", "login hataları var mı", guidedProblems, ""},

		// No match → fall through to the free tool loop.
		{"greeting", "merhaba", guidedNone, ""},
		{"smalltalk with health word but no entity", "bugün hava nasıl", guidedNone, ""},
		{"unrelated question", "kafka consumer lag neden artar", guidedNone, ""},
		{"dashboard request", "bana bir dashboard oluştur", guidedNone, ""},
		{"empty", "", guidedNone, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := routeGuidedIntent(tc.msg, guidedTestServices)
			if got.Intent != tc.intent {
				t.Fatalf("intent: got %q want %q (msg %q)", got.Intent, tc.intent, tc.msg)
			}
			if got.Service != tc.service {
				t.Fatalf("service: got %q want %q (msg %q)", got.Service, tc.service, tc.msg)
			}
		})
	}
}

func TestExtractServiceEntity(t *testing.T) {
	cases := []struct {
		name     string
		msg      string
		services []string
		want     string
	}{
		{"exact bounded match", "is payment-service healthy", guidedTestServices, "payment-service"},
		{"longest suffixed name wins", "mobile-bff-uat çok yavaş", guidedTestServices, "mobile-bff-uat"},
		{"base name when suffixed sibling exists", "mobile-bff çok yavaş", guidedTestServices, "mobile-bff"},
		{"apostrophe detaches turkish suffix", "checkout-service'in p99 değeri", guidedTestServices, "checkout-service"},
		{"unique prefix fallback", "checkout servisi yavaş", guidedTestServices, "checkout-service"},
		{"prefix must stop at separator", "check servisi yavaş", guidedTestServices, ""},
		{"ambiguous prefix returns empty", "mobile servisleri yavaş",
			[]string{"mobile-bff-uat", "mobile-bff-prod"}, ""},
		{"no bare-substring inside longer name", "bff yavaş", []string{"mobile-bff"}, ""},
		{"stopword never matches", "service yavaş", []string{"service-a"}, ""},
		{"empty list", "checkout yavaş", nil, ""},
		{"turkish token skipped (ascii-only names)", "sağlık kontrolü", []string{"saglik-servisi"}, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := extractServiceEntity(normalizeGuidedMsg(tc.msg), tc.services)
			if got != tc.want {
				t.Fatalf("got %q want %q (msg %q)", got, tc.want, tc.msg)
			}
		})
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
	out := renderProblemsEvidenceTR(probs, "", now)
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
	if got := renderProblemsEvidenceTR(nil, "checkout-service", now); !strings.Contains(got, "Açık problem yok (servis: checkout-service)") {
		t.Fatalf("empty render = %q", got)
	}
}

func TestRenderSlowTracesEvidenceTR(t *testing.T) {
	rows := []chstore.TraceRow{
		{TraceID: "abc123", RootName: "POST /api/cart", ServiceName: "checkout-service",
			DurationMs: 4521, SpanCount: 12, HasError: true},
		{TraceID: "def456", RootName: "GET /health", ServiceName: "checkout-service",
			DurationMs: 900, SpanCount: 3},
	}
	out := renderSlowTracesEvidenceTR(rows, "checkout-service", 1800)
	for _, want := range []string{
		"En yavaş trace'ler (son 30dk, servis: checkout-service",
		"4521ms — checkout-service / POST /api/cart (12 span, HATA) trace=abc123",
		"900ms — checkout-service / GET /health (3 span) trace=def456",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("slow-traces evidence missing %q in:\n%s", want, out)
		}
	}
	if got := renderSlowTracesEvidenceTR(nil, "", 3600); !strings.Contains(got, "trace bulunamadı") {
		t.Fatalf("empty render = %q", got)
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
