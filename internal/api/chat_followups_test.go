package api

import (
	"strings"
	"testing"

	"github.com/cilcenk/coremetry/internal/mcptools"
)

// v0.10.479 (Faz 4, F4-2) — takip mutasyonları. Adlar SENTETİK.

func ctxWithLast(intent guidedIntent, svc string) ChatContext {
	r := guidedRoute{Intent: intent, Service: svc}
	return ChatContext{Service: svc, RangeS: 1800, LastRoute: &r, LastIntent: string(intent)}
}

func TestDetectContextMutation(t *testing.T) {
	c := ctxWithLast(guidedTraceSearch, "checkout-service")
	cases := []struct {
		q    string
		kind string
	}{
		{"son 1 saate genişlet", "window"},
		{"pencereyi 6 saate çıkar", "window"},
		{"24 saate bak", "window"},
		{"sadece hatalı olanlar", "errors"},
		{"yalnız hatalılar", "errors"},
		{"bunun pod'larını göster", "pods"},
		{"pod'ları", "pods"},
		{"aynı filtreyle loglara bak", "logs"},
		{"loglara bak", "logs"},
		// kip değil:
		{"son 1 saatte hata var mı", ""},
		{"checkout-service en yavaş trace'ler?", ""},
		{"bugün hava nasıl", ""},
	}
	for _, x := range cases {
		norm := normalizeGuidedMsg(x.q)
		explicit := extractServiceEntity(norm, feServices, feEnvs) != ""
		m, ok := detectContextMutation(norm, guidedTokens(norm), c, explicit)
		if (x.kind == "") != !ok || m.Kind != x.kind {
			t.Errorf("%q → %s/%v; want %q", x.q, m.Kind, ok, x.kind)
		}
	}
	if _, ok := detectContextMutation("son 1 saate genişlet", guidedTokens("son 1 saate genişlet"), ChatContext{}, false); ok {
		t.Error("LastRoute yoksa kip yok")
	}
}

func TestApplyContextMutation(t *testing.T) {
	c := ctxWithLast(guidedTraceSearch, "checkout-service")
	c.LastRoute.SearchText = "gw.example.com"
	r, rs, next, ok := applyContextMutation(c, contextMutation{Kind: "window", RangeS: 3600})
	if !ok || r.Intent != guidedTraceSearch || r.SearchText != "gw.example.com" || rs != 3600 || next.RangeS != 3600 || !next.RangeExplicit {
		t.Fatalf("pencere: %+v %d %+v %v", r, rs, next, ok)
	}
	r, _, next, ok = applyContextMutation(c, contextMutation{Kind: "errors"})
	if !ok || !r.TraceErrorsOnly || !next.ErrorsOnly {
		t.Fatalf("hatalı (arama): %+v %v", r, ok)
	}
	sl := ctxWithLast(guidedSlowTraces, "checkout-service")
	if r, _, _, ok := applyContextMutation(sl, contextMutation{Kind: "errors"}); !ok || r.Intent != guidedFamilyTraces || !r.TraceErrorsOnly || r.Service != "checkout-service" {
		t.Fatalf("hatalı (yavaş → aile hatalı): %+v %v", r, ok)
	}
	if r, _, _, ok := applyContextMutation(c, contextMutation{Kind: "pods"}); !ok || r.Intent != guidedPodHealth || r.Service != "checkout-service" {
		t.Fatalf("pod'lar (servis): %+v %v", r, ok)
	}
	nsC := ChatContext{Namespace: "shop", RangeS: 900, LastRoute: &guidedRoute{Intent: guidedNamespaceServices, FindQuery: "shop"}}
	if r, rs, _, ok := applyContextMutation(nsC, contextMutation{Kind: "pods"}); !ok || r.Intent != guidedNamespaceServices || !r.FindPods || r.FindQuery != "shop" || rs != 900 {
		t.Fatalf("pod'lar (namespace): %+v %d %v", r, rs, ok)
	}
	c.SearchText = "gw.example.com"
	if r, _, _, ok := applyContextMutation(c, contextMutation{Kind: "logs"}); !ok || r.Intent != guidedLogField || r.LogValue != "gw.example.com" || !r.LogContains {
		t.Fatalf("loglar (aranan değerle): %+v %v", r, ok)
	}
	c.SearchText = ""
	if r, _, _, ok := applyContextMutation(c, contextMutation{Kind: "logs"}); !ok || r.Intent != guidedLogErrors {
		t.Fatalf("loglar (hata logları): %+v %v", r, ok)
	}
	if _, _, _, ok := applyContextMutation(ChatContext{LastRoute: &guidedRoute{Intent: guidedProblems}}, contextMutation{Kind: "pods"}); ok {
		t.Error("servis/namespace yokken pod kipi anlamsız")
	}
}

func TestRenderNamespacePods(t *testing.T) {
	txt := renderNamespacePods("shop", []mcptools.PodRow{{Cluster: "eu-west", Pod: "api-1", Workload: "api", Node: "n1", Spans: 40, Errors: 1, LastSpan: "2026-09-06T12:00:00Z"}, {Cluster: "eu-west", Pod: "db-0", Workload: "db"}}, 1800)
	for _, want := range []string{"**shop** namespace'i · 2 pod", "| eu-west | api-1 | api | n1 | 40 | 1 |", "| eu-west | db-0 | db | — | 0 | 0 | — |", "telemetri göndermedi"} {
		if !strings.Contains(txt, want) {
			t.Errorf("%q yok:\n%s", want, txt)
		}
	}
	if !strings.Contains(renderNamespacePods("x", nil, 60), "pod yok") {
		t.Error("boş liste dürüst")
	}
}
