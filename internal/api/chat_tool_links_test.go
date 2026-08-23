package api

// chat_tool_links_test.go — v0.9.1228 pinleri. Köprü haritası K4
// ölü-param denetiminden geçmiş hedefleri vaat eder; args HAM model
// çıktısıdır ve yalnız QueryEscape'li alan çekimiyle href'e girer.

import (
	"encoding/json"
	"testing"
	"time"
)

// linkTestNow — sabit "şimdi"; v0.9.1321'de toolCallLink pencere için
// bunu argüman aldı. 1_700_000_000_000 ms; 1h geriye = 1_699_996_400_000.
var linkTestNow = time.UnixMilli(1_700_000_000_000)

func TestToolCallLink(t *testing.T) {
	// v0.9.1321 — bu tablo BİLEREK eski gövdesiyle kalıyor: hiçbirinde
	// range_s yok, dolayısıyla hepsi penceresiz kalmalı. Değişmeyen hâl
	// ancak eski beklentilere karşı koşarak kanıtlanır; pencere
	// davranışının kendi tablosu aşağıda.
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
		l, ok := toolCallLink(c.tool, json.RawMessage(c.args), linkTestNow)
		if ok != c.wantOK || (ok && l.Href != c.wantHref) {
			t.Errorf("toolCallLink(%s, %s) = (%q, %v), beklenen (%q, %v)",
				c.tool, c.args, l.Href, ok, c.wantHref, c.wantOK)
		}
	}
}

// TestToolCallLinkCarriesWindow — v0.9.1321 (§3.1 K6). Köprü çipi,
// tool'un GERÇEKTEN sorguladığı pencereyi taşır; taşımayan hedeflerde
// (zaman ekseni olmayan sayfalar) hiçbir şey yazmaz.
func TestToolCallLinkCarriesWindow(t *testing.T) {
	const win = "range=custom:1699996400000-1700000000000" // now-1h .. now
	cases := []struct {
		name, tool, args, wantHref string
		now                        time.Time
	}{
		{"range_s pencereyi yazar", "search_traces", `{"service":"x","range_s":3600}`,
			"/traces?service=x&" + win, linkTestNow},
		{"paramsız hedefte ? ayracı", "get_db_health", `{"range_s":3600}`,
			"/databases?" + win, linkTestNow},
		{"range_s yoksa pencere YOK", "search_traces", `{"service":"x"}`,
			"/traces?service=x", linkTestNow},
		{"negatif range_s pencere değildir", "search_traces", `{"service":"x","range_s":-5}`,
			"/traces?service=x", linkTestNow},
		{"sıfır now pencere üretmez", "search_traces", `{"service":"x","range_s":3600}`,
			"/traces?service=x", time.Time{}},
		// K4 ölü-param: zaman ekseni OLMAYAN üç hedef. Bunlara range
		// yazmak, operatöre tutulmayacak bir söz vermek olurdu.
		{"/problems zaman eksenini okumaz", "list_problems", `{"service":"x","range_s":3600}`,
			"/problems?service=x", linkTestNow},
		{"/inbox zaman eksenini okumaz", "list_exception_groups", `{"range_s":3600}`,
			"/inbox?kind=exception", linkTestNow},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			l, ok := toolCallLink(c.tool, json.RawMessage(c.args), c.now)
			if !ok {
				t.Fatalf("köprü kurulmadı")
			}
			if l.Href != c.wantHref {
				t.Errorf("href = %q, beklenen %q", l.Href, c.wantHref)
			}
		})
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
