package influx

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// client_test.go — v0.10.222 (Influx D1, audit §2 client.go + K2).
//
// Sözleşme (InfluxDB 2.x /api/v2/query): POST {url}/api/v2/query?org=…,
// `Authorization: Token <t>`, JSON gövde {query, type:"flux", dialect:
// {annotations:[group,datatype,default], header:true}}, `Accept:
// application/csv`. 2xx → annotated CSV → kayıtlar; 2xx dışı → hata
// (Influx'un JSON {code,message} zarfı mesaja taşınır). Test(): her
// sorguya `|> limit(n: 20)` eklenir, satır sayısı + örnek döner, token
// çözülemezse ok=false + neden.

func fakeInflux(t *testing.T, wantToken string, csv string, status int) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/api/v2/query" {
			t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
		}
		if got := r.URL.Query().Get("org"); got != "bank" {
			t.Errorf("org query param = %q", got)
		}
		if got := r.Header.Get("Authorization"); got != "Token "+wantToken {
			w.WriteHeader(http.StatusUnauthorized)
			io.WriteString(w, `{"code":"unauthorized","message":"unauthorized access"}`)
			return
		}
		if ct := r.Header.Get("Content-Type"); ct != "application/json" {
			t.Errorf("content-type = %q", ct)
		}
		var body struct {
			Query   string `json:"query"`
			Type    string `json:"type"`
			Dialect struct {
				Annotations []string `json:"annotations"`
				Header      bool     `json:"header"`
			} `json:"dialect"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("body decode: %v", err)
		}
		if body.Type != "flux" || len(body.Dialect.Annotations) != 3 || !body.Dialect.Header {
			t.Errorf("dialect wrong: %+v", body)
		}
		if strings.Contains(body.Query, "{{") {
			t.Errorf("unfilled placeholder sent to Influx: %s", body.Query)
		}
		if status != 200 {
			w.WriteHeader(status)
			io.WriteString(w, `{"code":"invalid","message":"compilation failed: bad flux"}`)
			return
		}
		w.Header().Set("Content-Type", "text/csv")
		io.WriteString(w, csv)
	}))
}

func TestHTTPQueryAPI_Query(t *testing.T) {
	srv := fakeInflux(t, "tok", sampleCSV, 200)
	defer srv.Close()
	q := NewHTTPQueryAPI(srv.Client(), srv.URL, "bank", "tok")
	recs, err := q.Query(context.Background(), `from(bucket:"B") |> range(start:-2m)`)
	if err != nil || len(recs) != 3 {
		t.Fatalf("want 3 recs, got %d / %v", len(recs), err)
	}
}

func TestHTTPQueryAPI_ErrorEnvelope(t *testing.T) {
	srv := fakeInflux(t, "tok", "", 400)
	defer srv.Close()
	q := NewHTTPQueryAPI(srv.Client(), srv.URL, "bank", "tok")
	_, err := q.Query(context.Background(), `bad`)
	if err == nil || !strings.Contains(err.Error(), "compilation failed") || !strings.Contains(err.Error(), "400") {
		t.Fatalf("status + message must surface; got %v", err)
	}
	q2 := NewHTTPQueryAPI(srv.Client(), srv.URL, "bank", "wrong")
	_, err = q2.Query(context.Background(), `x`)
	if err == nil || !strings.Contains(err.Error(), "401") {
		t.Fatalf("401 must surface; got %v", err)
	}
}

func TestServiceTest_ProbesEachQuery(t *testing.T) {
	srv := fakeInflux(t, "tok", sampleCSV, 200)
	defer srv.Close()
	svc := New()
	svc.getenv = func(k string) string {
		if k == "COREMETRY_INFLUX_TOKEN_GG" {
			return "tok"
		}
		return ""
	}
	src := validSource()
	src.URL = srv.URL
	res := svc.Test(context.Background(), src)
	if !res.OK || !res.TokenResolved || len(res.Queries) != 1 {
		t.Fatalf("test result: %+v", res)
	}
	p := res.Queries[0]
	if p.Name != "tfail_adet" || p.Rows != 3 || len(p.Sample) == 0 || p.Sample[0]["OPERATIONCODE"] != "OP1" {
		t.Fatalf("probe: %+v", p)
	}
	if p.Error != "" {
		t.Fatalf("probe error: %s", p.Error)
	}

	// Çözülemeyen token: Influx'a HİÇ gidilmez, sebep söylenir.
	src.TokenRef = "env:NOPE"
	res = svc.Test(context.Background(), src)
	if res.OK || res.TokenResolved || !strings.Contains(res.Error, "NOPE") {
		t.Fatalf("unresolved token must fail fast: %+v", res)
	}
}

// v0.10.335 — sıfır satır teşhisi. Prod olayı: işçi koşuyor, hata yok,
// "0 satır → 0 nokta"; kart gecikme mi / ad uyuşmazlığı mı söyleyemiyordu.
// Test(): asıl sorgu boş dönerse aynı sorgu 24 saatlik pencerede bir kez
// daha koşar (limit 20) ve Hint sebebi ayırt eder.
func TestWidenRange(t *testing.T) {
	cases := []struct {
		in, want string
		ok       bool
	}{
		{`from(b) |> range(start: -2m) |> sum()`, `from(b) |> range(start: -24h) |> sum()`, true},
		{`range(start: -5mo)`, `range(start: -24h)`, true},
		{`range( start:  -30s, stop: now())`, `range(start: -24h, stop: now())`, true},
		{`range(start: -1h) |> x |> range(start: -2m)`, `range(start: -24h) |> x |> range(start: -2m)`, true}, // yalnız ilki
		{`range(start: 2026-09-01T00:00:00Z)`, `range(start: 2026-09-01T00:00:00Z)`, false},
		{`range(start: {{from}}, stop: {{to}})`, `range(start: {{from}}, stop: {{to}})`, false},
	}
	for _, c := range cases {
		got, ok := widenRange(c.in)
		if got != c.want || ok != c.ok {
			t.Errorf("widenRange(%q) = %q,%v want %q,%v", c.in, got, ok, c.want, c.ok)
		}
	}
}

func TestServiceTest_WideProbeOnEmpty(t *testing.T) {
	var queries []string
	wideCSV := sampleCSV
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Query string `json:"query"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		queries = append(queries, body.Query)
		w.Header().Set("Content-Type", "text/csv")
		if strings.Contains(body.Query, "range(start: -24h)") {
			io.WriteString(w, wideCSV)
			return
		}
		io.WriteString(w, "") // poll penceresi boş
	}))
	defer srv.Close()
	svc := New()
	svc.getenv = func(k string) string {
		if k == "COREMETRY_INFLUX_TOKEN_GG" {
			return "tok"
		}
		return ""
	}
	src := validSource()
	src.URL = srv.URL

	res := svc.Test(context.Background(), src)
	if !res.OK || len(res.Queries) != 1 {
		t.Fatalf("test result: %+v", res)
	}
	p := res.Queries[0]
	if p.Rows != 0 || p.Error != "" {
		t.Fatalf("asıl sorgu boş dönmeli, hata yok: %+v", p)
	}
	if p.WideWindow != "24h" || p.WideRows != 3 || p.WideNewest != "2026-09-01T10:02:00Z" || p.WideError != "" {
		t.Fatalf("geniş deneme: %+v", p)
	}
	if !strings.Contains(p.Hint, "gecikmeli") || !strings.Contains(p.Hint, "2026-09-01T10:02:00Z") {
		t.Fatalf("gecikme ipucu bekleniyordu: %q", p.Hint)
	}
	if len(p.Sample) == 0 || len(p.Columns) == 0 {
		t.Fatalf("geniş denemenin örneği operatöre gösterilmeli: %+v", p)
	}
	if len(queries) != 2 || !strings.Contains(queries[0], "range(start: -2m)") || !strings.HasSuffix(strings.TrimSpace(queries[1]), "|> limit(n: 20)") {
		t.Fatalf("iki sorgu bekleniyordu (asıl -2m, sonra -24h + limit): %q", queries)
	}

	// Geniş pencere de boş → ad uyuşmazlığı ipucu.
	wideCSV = ""
	queries = nil
	res = svc.Test(context.Background(), src)
	p = res.Queries[0]
	if !res.OK || p.WideWindow != "24h" || p.WideRows != 0 || !strings.Contains(p.Hint, "_measurement") {
		t.Fatalf("ad uyuşmazlığı ipucu bekleniyordu: %+v", p)
	}

	// Satır varsa geniş deneme HİÇ koşmaz.
	wideCSV = sampleCSV
	queries = nil
	src.Queries[0].Flux = `from(bucket: "b") |> range(start: -24h) |> sum()` // asıl sorgu zaten -24h → dolu döner
	res = svc.Test(context.Background(), src)
	p = res.Queries[0]
	if p.Rows != 3 || p.WideWindow != "" || p.Hint != "" || len(queries) != 1 {
		t.Fatalf("dolu sonuçta ikinci deneme olmamalı: %+v (%d sorgu)", p, len(queries))
	}

	// Göreli başlangıcı olmayan sorgu: deneme atlanır, ipucu yok.
	src.Queries[0].Flux = `from(bucket: "b") |> range(start: 2026-09-01T00:00:00Z) |> sum()`
	queries = nil
	res = svc.Test(context.Background(), src)
	p = res.Queries[0]
	if p.Rows != 0 || p.WideWindow != "" || p.Hint != "" || len(queries) != 1 {
		t.Fatalf("mutlak zamanlı sorguda deneme olmamalı (boş kalır, ipucu yok): %+v (%d sorgu)", p, len(queries))
	}
}
