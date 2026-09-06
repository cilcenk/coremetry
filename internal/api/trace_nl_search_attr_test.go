package api

import (
	"strings"
	"testing"

	"github.com/cilcenk/coremetry/internal/chstore"
	"github.com/cilcenk/coremetry/internal/mcptools"
)

// v0.10.476 (Faz 3, F3-5) — attribute-farkındalı trace araması saf parçaları.

func TestSearchKeyPick(t *testing.T) {
	m := []mcptools.AttrValueMatch{
		{Key: "url.full", Match: "substring"},
		{Key: "server.address", Match: "exact"},
		{Key: "http.host", Match: "exact"},
		{Key: "http.route", Match: "exact", Column: "http_route"},
		{Key: "net.peer.name", Match: "exact"},
	}
	keys := searchKeyPick(m)
	if len(keys) != 3 || keys[0] != "http.route" || keys[1] != "server.address" || keys[2] != "http.host" {
		t.Fatalf("anahtar seçimi (kolon önce, ≤3, alt-dize yok): %v", keys)
	}
	if searchKeyPick(nil) != nil {
		t.Error("boş → nil")
	}
}

func TestMergeTraceRows(t *testing.T) {
	a := []chstore.TraceRow{{TraceID: "t1", DurationMs: 100}, {TraceID: "t2", DurationMs: 900}}
	b := []chstore.TraceRow{{TraceID: "t2", DurationMs: 900}, {TraceID: "t3", DurationMs: 500}}
	out := mergeTraceRows([][]chstore.TraceRow{a, b}, 2)
	if len(out) != 2 || out[0].TraceID != "t2" || out[1].TraceID != "t3" {
		t.Fatalf("birleşim/sıra/limit: %+v", out)
	}
}

func TestTraceSearchLinkUsesResolvedKey(t *testing.T) {
	r := guidedRoute{Intent: guidedTraceSearch, Service: "checkout-service", SearchText: "gw.example.com", SearchKeys: []string{"server.address"}}
	links := guidedAnswerLinks(r, noLinkWindow())
	if len(links) == 0 || !strings.Contains(links[0].Href, "filters=") || !strings.Contains(links[0].Href, "server.address") || strings.Contains(links[0].Href, "search=") {
		t.Fatalf("anahtar çözüldüyse süzgeç linki: %+v", links)
	}
	plain := guidedAnswerLinks(guidedRoute{Intent: guidedTraceSearch, Service: "checkout-service", SearchText: "gw.example.com"}, noLinkWindow())
	if len(plain) == 0 || !strings.Contains(plain[0].Href, "search=") {
		t.Fatalf("anahtarsız eski link: %+v", plain)
	}
	ev := renderTraceSearchEvidenceTR(nil, r, 1800)
	if !strings.Contains(ev, "server.address anahtarında (tam eşitlik)") {
		t.Errorf("kanıt başlığı anahtarı söylemeli:\n%s", ev)
	}
}
