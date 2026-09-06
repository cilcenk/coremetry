package mcptools

import (
	"context"
	"encoding/json"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/cilcenk/coremetry/internal/chstore"
)

// v0.10.474 (Faz 3, F3-3) — trace_stats saf parçaları + arg kapıları.

func TestTraceStatsRowsAndLink(t *testing.T) {
	rows := traceStatsRows([]chstore.AggregateRow{{GroupKey: "POST /pay", GroupExtra: "checkout", TraceCount: 120, PerMin: 4, ErrorCount: 6, ErrorRate: 5, P95Ms: 420}})
	if len(rows) != 1 || rows[0].Group != "POST /pay" || rows[0].Extra != "checkout" || rows[0].Traces != 120 || rows[0].ErrorRate != 5 || rows[0].P95Ms != 420 {
		t.Fatalf("satır: %+v", rows)
	}
	from := time.Date(2026, 9, 6, 12, 0, 0, 0, time.UTC)
	h := aggregateDeepLink(chstore.AggregateFilter{GroupBy: "attr", GroupAttr: "http.route", Service: "checkout", HasError: true, Filters: []chstore.FilterExpr{{Key: "server.address", Op: "=", Values: []string{"gw"}}}}, from, from.Add(time.Hour))
	u, _ := url.Parse(h)
	q := u.Query()
	if u.Path != "/traces" || q.Get("view") != "aggregate" || q.Get("groupBy") != "attr" || q.Get("groupAttr") != "http.route" || q.Get("service") != "checkout" || q.Get("hasError") != "true" || !strings.Contains(q.Get("filters"), "server.address") || !strings.HasPrefix(q.Get("range"), "custom:") {
		t.Errorf("deep link: %s", h)
	}
}

func TestTraceStatsArgGates(t *testing.T) {
	tool := traceStatsTool(Deps{})
	for _, bad := range []string{`{"group_by":"pod"}`, `{"group_by":"attr"}`, `{"filters":[{"key":"http.route","value":"/pay"}]}`, `{"filters":[{"key":"x","op":"fuzzy","value":"y"}],"service":"s"}`} {
		if _, err := tool.Handler(context.Background(), json.RawMessage(bad)); err == nil {
			t.Errorf("%s → hata beklenir (Store'a dokunmadan)", bad)
		}
	}
	if !traceStatsSort["p95"] || traceStatsSort["p999"] || !traceStatsGroupBy["status"] {
		t.Error("izin listeleri")
	}
}
