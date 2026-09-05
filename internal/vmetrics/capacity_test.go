package vmetrics

// v0.10.366 — VM dilim 3b-1: DB capacity reads through VictoriaMetrics.
// The fake server routes on the rendered metric spelling and answers
// with labelled matrices; the assertions are the ClickHouse reader's
// contract (instance coalesce order, positive-limit gate, empty subkey
// dropped, per-second rate over the window with reset clamp, trend
// keyed by CapacityTrendKey ascending and shifted to bucket start).

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/cilcenk/coremetry/internal/chstore"
)

func TestCapacityInstanceCoalesceOrder(t *testing.T) {
	cases := []struct {
		tuple []string
		inst  string
		sub   string
	}{
		{[]string{"ora-1", "", "svc", "USERS"}, "ora-1", "USERS"},
		{[]string{"", "ora-exp", "svc", "USERS"}, "ora-exp", "USERS"},
		{[]string{"", "", "svc"}, "svc", ""},
		{[]string{"", "", ""}, "", ""},
		{nil, "", ""},
	}
	for _, c := range cases {
		if got := capacityInstanceFromTuple(c.tuple); got != c.inst {
			t.Fatalf("%v → instance %q, want %q", c.tuple, got, c.inst)
		}
		if got := capacitySubkeyFromTuple(c.tuple); got != c.sub {
			t.Fatalf("%v → subkey %q, want %q", c.tuple, got, c.sub)
		}
	}
	if g := capacityGroupBy("tablespace_name"); len(g) != 4 || g[0] != "instance" || g[2] != "service.name" || g[3] != "tablespace_name" {
		t.Fatalf("group-by = %v", g)
	}
}

// capacityFake — one server, answers by metric spelling in the query.
func capacityFake(t *testing.T, answers map[string]string) (*Service, *[]string) {
	t.Helper()
	var queries []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query().Get("query")
		queries = append(queries, q)
		for needle, body := range answers {
			if strings.Contains(q, needle) {
				_, _ = w.Write([]byte(body))
				return
			}
		}
		_, _ = w.Write([]byte(`{"status":"success","data":{"resultType":"matrix","result":[]}}`))
	}))
	t.Cleanup(srv.Close)
	s := New()
	s.Configure(Settings{BaseURL: srv.URL})
	return s, &queries
}

func matrix(rows ...string) string {
	return `{"status":"success","data":{"resultType":"matrix","result":[` + strings.Join(rows, ",") + `]}}`
}

func TestUsageLimitJoinsAndGatesOnPositiveLimit(t *testing.T) {
	s, queries := capacityFake(t, map[string]string{
		"sessions_usage": matrix(
			`{"metric":{"instance":"ora-1","service_name":"oracle-recv"},"values":[[1700000000,"40"],[1700000600,"50"]]}`,
			`{"metric":{"service_name":"ora-2-svc"},"values":[[1700000600,"7"]]}`,
			`{"metric":{"instance":"ora-3"},"values":[[1700000600,"9"]]}`,
		),
		"sessions_limit": matrix(
			`{"metric":{"instance":"ora-1","service_name":"oracle-recv"},"values":[[1700000600,"100"]]}`,
			`{"metric":{"service_name":"ora-2-svc"},"values":[[1700000600,"0"]]}`,
		),
	})
	out, err := s.UsageLimit(context.Background(), "oracledb.sessions.usage", "oracledb.sessions.limit")
	if err != nil {
		t.Fatal(err)
	}
	if len(out) != 1 || out[0].Instance != "ora-1" || out[0].Usage != 50 || out[0].Limit != 100 || out[0].Subkey != "" {
		t.Fatalf("samples = %+v (want ora-1 usage 50 (LAST bucket) limit 100; ora-2 limit 0 and ora-3 no limit dropped)", out)
	}
	if len(*queries) != 2 {
		t.Fatalf("two range queries expected, got %d: %v", len(*queries), *queries)
	}
	for _, q := range *queries {
		if !strings.Contains(q, "instance") || !strings.Contains(q, "service_name") {
			t.Fatalf("query must group by the instance candidates: %s", q)
		}
	}
}

func TestDimensionedUsageLimitDropsEmptySubkey(t *testing.T) {
	s, queries := capacityFake(t, map[string]string{
		"tablespace_size_usage": matrix(
			`{"metric":{"instance":"ora-1","tablespace_name":"USERS"},"values":[[1700000600,"800"]]}`,
			`{"metric":{"instance":"ora-1","tablespace_name":"SYSTEM"},"values":[[1700000600,"300"]]}`,
			`{"metric":{"instance":"ora-1"},"values":[[1700000600,"5"]]}`,
		),
		"tablespace_size_limit": matrix(
			`{"metric":{"instance":"ora-1","tablespace_name":"USERS"},"values":[[1700000600,"1000"]]}`,
			`{"metric":{"instance":"ora-1","tablespace_name":"SYSTEM"},"values":[[1700000600,"1000"]]}`,
			`{"metric":{"instance":"ora-1"},"values":[[1700000600,"10"]]}`,
		),
	})
	out, err := s.DimensionedUsageLimit(context.Background(), "oracledb.tablespace_size.usage", "oracledb.tablespace_size.limit", "tablespace_name")
	if err != nil {
		t.Fatal(err)
	}
	if len(out) != 2 || out[0].Subkey != "SYSTEM" || out[1].Subkey != "USERS" || out[1].Usage != 800 || out[1].Limit != 1000 {
		t.Fatalf("samples = %+v (want SYSTEM, USERS; undimensioned row dropped)", out)
	}
	if !strings.Contains((*queries)[0], "tablespace_name") {
		t.Fatalf("dimension key must be in the group-by: %s", (*queries)[0])
	}
}

func TestRateGaugeIsIncreaseOverWindowClampedAtZero(t *testing.T) {
	s, queries := capacityFake(t, map[string]string{
		"keys_evicted": matrix(
			`{"metric":{"instance":"redis-1"},"values":[[1700000600,"1200"]]}`,
			`{"metric":{"instance":"redis-2"},"values":[[1700000600,"-5"]]}`,
		),
	})
	out, err := s.RateGauge(context.Background(), "redis.keys.evicted")
	if err != nil {
		t.Fatal(err)
	}
	if len(out) != 2 || out[0].Instance != "redis-1" || out[0].Usage != 2 || out[1].Usage != 0 || out[0].Limit != 0 {
		t.Fatalf("samples = %+v (want 1200/600 s = 2/s, negative → 0, no limit)", out)
	}
	if !strings.Contains((*queries)[0], "increase(") {
		t.Fatalf("rate gauge must use increase(): %s", (*queries)[0])
	}
}

func TestUsageTrendKeysAndShiftsToBucketStart(t *testing.T) {
	s, queries := capacityFake(t, map[string]string{
		"tablespace_size_usage": matrix(
			`{"metric":{"instance":"ora-1","tablespace_name":"USERS"},"values":[[1700000900,"820"],[1700000600,"810"]]}`,
			`{"metric":{"service_name":"ora-2-svc"},"values":[[1700000600,"5"]]}`,
		),
	})
	tr, err := s.UsageTrend(context.Background(), "oracledb.tablespace_size.usage", "tablespace_name", 6*time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	pts := tr[chstore.CapacityTrendKey("ora-1", "USERS")]
	if len(pts) != 2 || pts[0].TSec != 1700000600-300 || pts[0].Usage != 810 || pts[1].TSec != 1700000900-300 {
		t.Fatalf("trend = %+v (want ascending, END-stamped VM buckets shifted back 300 s)", pts)
	}
	if _, ok := tr[chstore.CapacityTrendKey("ora-2-svc", "")]; !ok {
		t.Fatalf("service_name-only instance must key as CapacityTrendKey(inst, \"\"): %v", tr)
	}
	// Unfiltered avg is refused by the bucket-family guard; the trend
	// must render as a plain max-by bucket, never trip the guard.
	if q := (*queries)[0]; !strings.HasPrefix(q, "max") || strings.Contains(q, "_sum") {
		t.Fatalf("trend query must be a plain max bucket: %s", q)
	}
}

func TestCapacityReaderContract(t *testing.T) {
	var _ chstore.CapacityReader = New()
}
