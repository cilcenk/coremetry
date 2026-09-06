package mcptools

import (
	"context"
	"encoding/json"
	"net/url"
	"strings"
	"testing"

	"github.com/cilcenk/coremetry/internal/chstore"
)

// v0.10.475 (Faz 3, F3-4) — deep-link sözleşmesi (audit §2.4) pinleri.

func parseHref(t *testing.T, href string) (string, url.Values) {
	t.Helper()
	u, err := url.Parse(href)
	if err != nil || !strings.HasPrefix(href, "/") {
		t.Fatalf("href uygulama-köklü değil: %s %v", href, err)
	}
	return u.Path, u.Query()
}

func TestBuildLinkPages(t *testing.T) {
	rng := "custom:1788696000000-1788697800000"
	f := []chstore.FilterExpr{{Key: "http.route", Op: "=", Values: []string{"/pay"}}}
	h, err := buildLink(buildLinkArgs{Page: "traces", Service: "checkout", ErrorsOnly: true, Sort: "duration", Env: "prod", GroupBy: "attr", GroupAttr: "http.route"}, "c-1", f, rng)
	if err != nil {
		t.Fatal(err)
	}
	p, q := parseHref(t, h)
	if p != "/traces" || q.Get("service") != "checkout" || q.Get("hasError") != "true" || q.Get("sort") != "duration" || q.Get("order") != "desc" || q.Get("env") != "prod" || q.Get("cluster") != "c-1" || q.Get("view") != "aggregate" || q.Get("groupBy") != "attr" || q.Get("groupAttr") != "http.route" || q.Get("range") != rng || !strings.Contains(q.Get("filters"), `"k":"http.route"`) {
		t.Errorf("traces: %s", h)
	}
	h, _ = buildLink(buildLinkArgs{Page: "trace", TraceID: "0af7651916cd43dd8448eb211c80319c", SpanID: "b7ad6b7169203331", Tab: "logs"}, "", nil, rng)
	if p, q := parseHref(t, h); p != "/trace" || q.Get("id") == "" || q.Get("span") == "" || q.Get("tab") != "logs" || q.Get("range") != rng {
		t.Errorf("trace: %s", h)
	}
	h, _ = buildLink(buildLinkArgs{Page: "logs", Query: `message:"timeout"`, Service: "checkout", ErrorsOnly: true, TraceID: "abc"}, "c-1", nil, rng)
	if p, q := parseHref(t, h); p != "/logs" || q.Get("q") == "" || q.Get("severity") != "17" || q.Get("cluster") != "c-1" || q.Get("traceId") != "abc" {
		t.Errorf("logs: %s", h)
	}
	h, _ = buildLink(buildLinkArgs{Page: "service", Service: "checkout", Tab: "pods"}, "", nil, rng)
	if p, q := parseHref(t, h); p != "/service" || q.Get("name") != "checkout" || q.Get("service") != "" || q.Get("tab") != "pods" {
		t.Errorf("service (name kanonik): %s", h)
	}
	h, _ = buildLink(buildLinkArgs{Page: "pod", Namespace: "shop", Pod: "api-1", Service: "shop-api"}, "c-1", nil, rng)
	if p, q := parseHref(t, h); p != "/pod" || q.Get("cluster") != "c-1" || q.Get("namespace") != "shop" || q.Get("pod") != "api-1" {
		t.Errorf("pod: %s", h)
	}
	h, _ = buildLink(buildLinkArgs{Page: "clusters", Namespace: "shop", Tab: "pods"}, "c-1", nil, rng)
	if p, q := parseHref(t, h); p != "/clusters" || q.Get("ns") != "c-1|shop" || q.Get("section") != "pods" {
		t.Errorf("clusters: %s", h)
	}
	h, _ = buildLink(buildLinkArgs{Page: "problems", Service: "checkout"}, "", nil, rng)
	if p, q := parseHref(t, h); p != "/problems" || q.Get("range") != "" {
		t.Errorf("problems pencere okumaz: %s", h)
	}
	for _, bad := range []buildLinkArgs{{Page: "nope"}, {Page: "trace"}, {Page: "service"}, {Page: "pod"}, {Page: "entity"}} {
		if _, err := buildLink(bad, "", nil, rng); err == nil {
			t.Errorf("%+v hata beklenir", bad)
		}
	}
}

func TestBuildLinkToolArgs(t *testing.T) {
	tool := buildLinkTool(Deps{Clusters: func() []ClusterRef { return ecClusters }})
	out, err := tool.Handler(context.Background(), json.RawMessage(`{"page":"traces","service":"checkout","cluster":"eu-west","namespace":"shop","preset":"1h","filters":[{"key":"http.route","op":"prefix","value":"/pay"}]}`))
	if err != nil {
		t.Fatal(err)
	}
	m := out.(map[string]any)
	_, q := parseHref(t, m["href"].(string))
	if q.Get("cluster") != "c-1" || q.Get("range") != "1h" || !strings.Contains(q.Get("filters"), "k8s.namespace.name") || !strings.Contains(q.Get("filters"), `"op":"=~"`) {
		t.Errorf("tool href: %s", m["href"])
	}
	if _, err := tool.Handler(context.Background(), json.RawMessage(`{"page":"traces","preset":"90m"}`)); err == nil {
		t.Error("bilinmeyen preset hata")
	}
	if _, err := tool.Handler(context.Background(), json.RawMessage(`{}`)); err == nil {
		t.Error("page zorunlu")
	}
}
