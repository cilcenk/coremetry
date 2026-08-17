package promapi

// v0.9.1150 — VictoriaMetrics read backend, Faz 1.
//
// The pure decoder half of the shared Prometheus-API core. Every case
// here is a failure mode that has bitten a Prometheus-shaped client
// somewhere before:
//
//   - status:"error" arriving with HTTP 200 (an "empty result" lie),
//   - sample values as STRINGS, including NaN / ±Inf,
//   - `"data": null` (a legitimate "no labels" answer, not an error),
//   - a trailing slash on the operator's base URL.

import (
	"encoding/json"
	"net/url"
	"strings"
	"testing"
)

func TestDecodeSeries(t *testing.T) {
	tests := []struct {
		name    string
		body    string
		want    int
		wantErr string
	}{
		{
			name: "range matrix",
			body: `{"status":"success","data":{"resultType":"matrix","result":[
				{"metric":{"__name__":"up","job":"a"},"values":[[1700000000,"1"],[1700000060,"1"]]},
				{"metric":{"__name__":"up","job":"b"},"values":[[1700000000,"0"]]}]}}`,
			want: 2,
		},
		{
			name: "instant vector",
			body: `{"status":"success","data":{"resultType":"vector","result":[
				{"metric":{"__name__":"up"},"value":[1700000000,"1"]}]}}`,
			want: 1,
		},
		{
			name: "empty result is not an error",
			body: `{"status":"success","data":{"resultType":"matrix","result":[]}}`,
			want: 0,
		},
		{
			name: "null data is not an error",
			body: `{"status":"success","data":null}`,
			want: 0,
		},
		{
			// The core case: HTTP 200 + status:error. Reading this as
			// "no data" is how a bad query becomes a silent empty chart.
			name:    "status error surfaces errorType AND error",
			body:    `{"status":"error","errorType":"bad_data","error":"unknown label \"svc\""}`,
			wantErr: `bad_data: unknown label "svc"`,
		},
		{
			name:    "malformed json",
			body:    `{"status":"success",`,
			wantErr: "vm decode:",
		},
		{
			name:    "data shape mismatch",
			body:    `{"status":"success","data":["not","a","series","object"]}`,
			wantErr: "vm decode data:",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := DecodeSeries("vm", []byte(tc.body))
			if tc.wantErr != "" {
				if err == nil {
					t.Fatalf("want error containing %q, got nil (rows=%d)", tc.wantErr, len(got))
				}
				if !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("error %q does not contain %q", err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if len(got) != tc.want {
				t.Fatalf("rows = %d, want %d", len(got), tc.want)
			}
		})
	}
}

// A misbehaving backend must not be able to hand us unbounded rows even
// when the query-side shield fails.
func TestDecodeSeriesTruncatesAtMaxSeriesParsed(t *testing.T) {
	var b strings.Builder
	b.WriteString(`{"status":"success","data":{"resultType":"matrix","result":[`)
	for i := 0; i < MaxSeriesParsed+50; i++ {
		if i > 0 {
			b.WriteByte(',')
		}
		b.WriteString(`{"metric":{"i":"x"},"values":[[1,"1"]]}`)
	}
	b.WriteString(`]}}`)
	got, err := DecodeSeries("vm", []byte(b.String()))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) != MaxSeriesParsed {
		t.Fatalf("rows = %d, want %d", len(got), MaxSeriesParsed)
	}
}

func TestDecodeStrings(t *testing.T) {
	tests := []struct {
		name    string
		body    string
		want    []string
		wantErr string
	}{
		{
			name: "label values",
			body: `{"status":"success","data":["jvm.memory.used","http.server.duration"]}`,
			want: []string{"jvm.memory.used", "http.server.duration"},
		},
		{
			// A metric with no datapoint labels is a real answer. If this
			// errored, MetricAttrKeys would report a broken backend for a
			// perfectly healthy gauge.
			name: "null data means no labels",
			body: `{"status":"success","data":null}`,
			want: nil,
		},
		{
			name: "empty array",
			body: `{"status":"success","data":[]}`,
			want: []string{},
		},
		{
			name:    "status error",
			body:    `{"status":"error","errorType":"execution","error":"too many series"}`,
			wantErr: "execution: too many series",
		},
		{
			name:    "wrong data shape",
			body:    `{"status":"success","data":{"resultType":"matrix"}}`,
			wantErr: "vm decode data:",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := DecodeStrings("vm", []byte(tc.body))
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("want error containing %q, got %v", tc.wantErr, err)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if len(got) != len(tc.want) {
				t.Fatalf("got %v, want %v", got, tc.want)
			}
			for i := range got {
				if got[i] != tc.want[i] {
					t.Fatalf("got %v, want %v", got, tc.want)
				}
			}
		})
	}
}

func TestSample(t *testing.T) {
	tests := []struct {
		name   string
		raw    string
		wantTS float64
		wantV  float64
		wantOK bool
	}{
		{name: "integer value", raw: `[1700000000,"42"]`, wantTS: 1700000000, wantV: 42, wantOK: true},
		{name: "float value", raw: `[1700000000.5,"1.25"]`, wantTS: 1700000000.5, wantV: 1.25, wantOK: true},
		{name: "negative", raw: `[1,"-3.5"]`, wantTS: 1, wantV: -3.5, wantOK: true},
		{name: "scientific", raw: `[1,"1.5e3"]`, wantTS: 1, wantV: 1500, wantOK: true},
		{name: "zero is a real value", raw: `[1,"0"]`, wantTS: 1, wantV: 0, wantOK: true},
		// Non-finite: legal Prometheus output, must be DROPPED (never 0).
		{name: "NaN dropped", raw: `[1,"NaN"]`, wantOK: false},
		{name: "+Inf dropped", raw: `[1,"+Inf"]`, wantOK: false},
		{name: "Inf dropped", raw: `[1,"Inf"]`, wantOK: false},
		{name: "-Inf dropped", raw: `[1,"-Inf"]`, wantOK: false},
		// Shape failures.
		{name: "short arity", raw: `[1]`, wantOK: false},
		{name: "empty", raw: `[]`, wantOK: false},
		{name: "value as number not string", raw: `[1,42]`, wantOK: false},
		{name: "ts as string", raw: `["1","42"]`, wantOK: false},
		{name: "value not numeric", raw: `[1,"abc"]`, wantOK: false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var raw []json.RawMessage
			if err := json.Unmarshal([]byte(tc.raw), &raw); err != nil {
				t.Fatalf("fixture: %v", err)
			}
			ts, v, ok := Sample(raw)
			if ok != tc.wantOK {
				t.Fatalf("ok = %v, want %v (ts=%v v=%v)", ok, tc.wantOK, ts, v)
			}
			if !ok {
				return
			}
			if ts != tc.wantTS || v != tc.wantV {
				t.Fatalf("got (%v, %v), want (%v, %v)", ts, v, tc.wantTS, tc.wantV)
			}
		})
	}
}

func TestBuildURL(t *testing.T) {
	tests := []struct {
		name   string
		base   string
		path   string
		params url.Values
		want   string
	}{
		{
			name: "plain",
			base: "http://vm:8428", path: "/api/v1/labels",
			want: "http://vm:8428/api/v1/labels",
		},
		{
			// A double slash 404s behind some reverse proxies, and the
			// operator pastes a trailing slash roughly half the time.
			name: "trailing slash on base",
			base: "https://vm.example.com/", path: "/api/v1/labels",
			want: "https://vm.example.com/api/v1/labels",
		},
		{
			name: "many trailing slashes",
			base: "https://vm.example.com///", path: "/api/v1/query",
			want: "https://vm.example.com/api/v1/query",
		},
		{
			name: "sub-path base preserved",
			base: "https://gw.example.com/vmselect/0/prometheus", path: "/api/v1/query_range",
			want: "https://gw.example.com/vmselect/0/prometheus/api/v1/query_range",
		},
		{
			name: "params encoded",
			base: "http://vm:8428", path: "/api/v1/query",
			params: url.Values{"query": {`{__name__="a.b"}`}},
			want:   "http://vm:8428/api/v1/query?query=%7B__name__%3D%22a.b%22%7D",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := BuildURL(tc.base, tc.path, tc.params); got != tc.want {
				t.Fatalf("got %q, want %q", got, tc.want)
			}
		})
	}
}

func TestFirstN(t *testing.T) {
	if got := FirstN("short", 200); got != "short" {
		t.Fatalf("got %q", got)
	}
	// Multi-byte: a byte slice would land mid-rune and print U+FFFD.
	long := strings.Repeat("ü", 300)
	got := FirstN(long, 10)
	if !strings.HasSuffix(got, "…") {
		t.Fatalf("want ellipsis, got %q", got)
	}
	if r := []rune(strings.TrimSuffix(got, "…")); len(r) != 10 {
		t.Fatalf("want 10 runes, got %d", len(r))
	}
	if strings.ContainsRune(got, '�') {
		t.Fatalf("truncation split a rune: %q", got)
	}
}
