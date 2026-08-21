package api

// chat_tool_links_test.go — v0.9.1228 pinleri. Köprü haritası K4
// ölü-param denetiminden geçmiş hedefleri vaat eder; args HAM model
// çıktısıdır ve yalnız QueryEscape'li alan çekimiyle href'e girer.

import (
	"encoding/json"
	"testing"
)

func TestToolCallLink(t *testing.T) {
	cases := []struct {
		tool, args, wantHref string
		wantOK               bool
	}{
		{"get_service_health", `{"service":"bsa-pay"}`, "/service?name=bsa-pay", true},
		{"get_service_health", `{}`, "", false}, // servissiz overview linki anlamsız
		{"get_operation_health", `{"service":"bsa-pay","sort":"p99"}`, "/endpoints?service=bsa-pay", true},
		{"search_traces", `{"service":"a b"}`, "/traces?service=a+b", true}, // escape
		{"search_traces", `{}`, "/traces", true},
		{"search_logs", `{"service":"x","query":"error AND payment"}`, "/logs?service=x&q=error+AND+payment", true},
		{"search_logs", `{"query":"oom"}`, "/logs?q=oom", true},
		{"list_problems", `{"service":"x"}`, "/problems?service=x", true},
		{"list_exception_groups", `{}`, "/inbox?kind=exception", true},
		{"list_exception_groups", `{"service":"x"}`, "/inbox?kind=exception&service=x", true},
		{"get_topology", `{}`, "/service-map", true},
		{"get_db_health", `{}`, "/databases", true},
		{"render_chart", `{}`, "", false},  // grafik cevabın içinde — köprü yok
		{"list_services", `{}`, "", false}, // hedef sayfa aramanın kendisi değil
		{"get_service_health", `not-json`, "", false}, // bozuk args → servis boş → köprü yok
	}
	for _, c := range cases {
		l, ok := toolCallLink(c.tool, json.RawMessage(c.args))
		if ok != c.wantOK || (ok && l.Href != c.wantHref) {
			t.Errorf("toolCallLink(%s, %s) = (%q, %v), beklenen (%q, %v)",
				c.tool, c.args, l.Href, ok, c.wantHref, c.wantOK)
		}
	}
}

func TestMergeToolLinks(t *testing.T) {
	var acc []guidedAnswerLink
	for _, h := range []string{"/a", "/b", "/a", "/c", "/d", "/e"} {
		acc = mergeToolLinks(acc, guidedAnswerLink{Label: h, Href: h})
	}
	if len(acc) != 4 { // tekil + tavan 4
		t.Fatalf("4 link beklenirdi (tekil+tavan), %d geldi: %+v", len(acc), acc)
	}
	if acc[0].Href != "/a" || acc[3].Href != "/d" {
		t.Errorf("ilk-çağrılan-önce sıra bozuldu: %+v", acc)
	}
}
