// Package promapi is the shared Prometheus HTTP-API client core
// (v0.9.1150) — envelope decoding + a bounded GET, with nothing in it
// that knows about clusters, metrics pages, or Coremetry's read model.
//
// Why a package and not a helper in internal/vmetrics: the SAME wire
// contract now has two consumers with different jobs — internal/thanos
// (multi-cluster pod/host metrics for /clusters) and internal/vmetrics
// (the metric read backend, Faz 1). The decoding rules that matter are
// the non-obvious ones, and each was a bug somewhere first:
//
//   - status != "success" carries errorType/error INSTEAD of data, so a
//     200 with a broken query is an ERROR, not an empty result. Treating
//     it as empty is the "no data" lie.
//   - sample values arrive as JSON STRINGS ("1.5", "NaN", "+Inf"), never
//     numbers. A struct with float64 silently fails to unmarshal.
//   - non-finite values are legal Prometheus output (staleness markers,
//     0/0 in a recording rule). They must be DROPPED, not coerced to 0 —
//     v0.9.1149 taught us a %f-formatted +Inf travels all the way into a
//     prompt.
//
// DELIBERATELY NOT a refactor of internal/thanos. thanos' doQuery stays
// exactly where it is: it is load-bearing on a surface with no test
// harness for a live Querier, and the spec for this release scoped the
// blast radius to the new reader. This package is the copy-the-pattern
// half of "copy the pattern, share the core" — new callers use it, the
// old one is left alone until something else forces it to move.
package promapi

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Series is one result row of /api/v1/query (Value filled, instant
// vector) or /api/v1/query_range (Values filled, range matrix).
// json.RawMessage because element 0 is a number and element 1 is a
// string — see Sample.
type Series struct {
	Metric map[string]string   `json:"metric"`
	Value  []json.RawMessage   `json:"value"`
	Values [][]json.RawMessage `json:"values"`
}

// envelope is the outer shape every /api/v1/* response shares. Data is
// held raw because the shape underneath differs per endpoint (series
// object for query/query_range, plain string array for labels /
// label/<k>/values).
type envelope struct {
	Status    string          `json:"status"`
	ErrorType string          `json:"errorType"`
	Error     string          `json:"error"`
	Data      json.RawMessage `json:"data"`
}

type seriesData struct {
	ResultType string   `json:"resultType"`
	Result     []Series `json:"result"`
}

// MaxSeriesParsed is the defensive backstop behind whatever cardinality
// shield the query itself carries (topk / a bounded selector): even a
// misbehaving backend can't hand us more rows than this. Mirrors
// thanos.maxSeriesParsed.
const MaxSeriesParsed = 1000

// MaxBodyBytes — 8MB read ceiling. A bounded vector is a few hundred KB;
// anything past this means the shield upstream failed and we would
// rather fail than allocate.
const MaxBodyBytes = 8 << 20

// decodeEnvelope is the shared status check. Extracted so both decoders
// produce byte-identical error text for the same failure.
func decodeEnvelope(label string, body []byte) (json.RawMessage, error) {
	var env envelope
	if err := json.Unmarshal(body, &env); err != nil {
		return nil, fmt.Errorf("%s decode: %w", label, err)
	}
	if env.Status != "success" {
		// Both halves in the message: errorType alone ("bad_data") does
		// not tell the operator WHICH label was wrong.
		return nil, fmt.Errorf("%s: %s: %s", label, env.ErrorType, env.Error)
	}
	return env.Data, nil
}

// DecodeSeries parses a query / query_range body into series. Pure —
// table-tested in promapi_test.go.
func DecodeSeries(label string, body []byte) ([]Series, error) {
	data, err := decodeEnvelope(label, body)
	if err != nil {
		return nil, err
	}
	if len(data) == 0 {
		return nil, nil
	}
	var sd seriesData
	if err := json.Unmarshal(data, &sd); err != nil {
		return nil, fmt.Errorf("%s decode data: %w", label, err)
	}
	if len(sd.Result) > MaxSeriesParsed {
		sd.Result = sd.Result[:MaxSeriesParsed]
	}
	return sd.Result, nil
}

// DecodeStrings parses a labels / label-values body — data is a plain
// string array. `"data": null` decodes to an empty slice, not an error:
// a metric with no labels is a legitimate answer.
func DecodeStrings(label string, body []byte) ([]string, error) {
	data, err := decodeEnvelope(label, body)
	if err != nil {
		return nil, err
	}
	if len(data) == 0 {
		return nil, nil
	}
	var out []string
	if err := json.Unmarshal(data, &out); err != nil {
		return nil, fmt.Errorf("%s decode data: %w", label, err)
	}
	return out, nil
}

// Sample decodes one [timestamp, "value"] pair.
//
// ok=false means the pair is UNUSABLE and the caller must skip the
// point: malformed JSON, wrong arity, or a non-finite value. Prometheus
// emits NaN / +Inf / -Inf as ordinary strings and they are not
// exceptional — a counter divided by a zero interval, a staleness
// marker. Coercing them to 0 draws a line to the floor that the
// operator reads as a real measurement, and a formatted ±Inf leaks into
// every downstream string (v0.9.1149). Dropping leaves a gap, which is
// what a gap is.
func Sample(raw []json.RawMessage) (tsSec float64, val float64, ok bool) {
	if len(raw) < 2 {
		return 0, 0, false
	}
	if err := json.Unmarshal(raw[0], &tsSec); err != nil {
		return 0, 0, false
	}
	var s string
	if err := json.Unmarshal(raw[1], &s); err != nil {
		return 0, 0, false
	}
	v, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return 0, 0, false
	}
	// ParseFloat ACCEPTS "NaN", "+Inf", "Inf", "-Inf" — the guard is the
	// finite check, not the parse error.
	if math.IsNaN(v) || math.IsInf(v, 0) {
		return 0, 0, false
	}
	return tsSec, v, true
}

// ── HTTP transport ──────────────────────────────────────────────

// verifyingClient is the default TLS-verifying client. 15s hard timeout
// (thanos precedent): a wedged backend must never pin a serveCached
// singleflight slot indefinitely — callers layer a tighter per-request
// ctx deadline on top.
var verifyingClient = &http.Client{Timeout: 15 * time.Second}

var (
	insecureOnce   sync.Once
	insecureClient *http.Client
)

// clientFor picks the verifying or the lazily-built insecure client. TLS
// variance across callers is a single bool, so two shared clients cover
// every backend; a client per caller would just fragment connection
// pools for no benefit.
func clientFor(skipTLS bool) *http.Client {
	if !skipTLS {
		return verifyingClient
	}
	insecureOnce.Do(func() {
		insecureClient = &http.Client{
			Timeout: 15 * time.Second,
			Transport: &http.Transport{
				TLSClientConfig: &tls.Config{InsecureSkipVerify: true},
			},
		}
	})
	return insecureClient
}

// Request is one Prometheus-API GET. Label names the backend in every
// error message ("victoriametrics") — an error that reads "connection
// refused" without saying WHOSE connection sends the operator hunting.
type Request struct {
	Label    string
	BaseURL  string
	Path     string
	Params   url.Values
	AuthType string // "" / "none" / "bearer"
	Token    string
	SkipTLS  bool
}

// BuildURL joins base + path + encoded params. Pure — the trailing-slash
// case is table-tested; "https://vm/" + "/api/v1/labels" must not
// produce a double slash, because some reverse proxies 404 on it.
func BuildURL(baseURL, path string, params url.Values) string {
	u := strings.TrimRight(baseURL, "/") + path
	if len(params) > 0 {
		u += "?" + params.Encode()
	}
	return u
}

// Do performs the GET and returns the raw body. HTTP >= 300 is an error
// carrying the first 200 bytes of the body — a 401 from VM says which
// auth it wanted, and hiding that costs an operator an hour.
func Do(ctx context.Context, req Request) ([]byte, error) {
	label := req.Label
	if label == "" {
		label = "prometheus-api"
	}
	if strings.TrimSpace(req.BaseURL) == "" {
		return nil, fmt.Errorf("%s: base URL not configured", label)
	}
	u := BuildURL(req.BaseURL, req.Path, req.Params)
	hr, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	hr.Header.Set("Accept", "application/json")
	if req.AuthType == "bearer" && req.Token != "" {
		hr.Header.Set("Authorization", "Bearer "+req.Token)
	}
	resp, err := clientFor(req.SkipTLS).Do(hr)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", label, err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, MaxBodyBytes))
	if resp.StatusCode >= 300 {
		return nil, fmt.Errorf("%s: HTTP %d: %s", label, resp.StatusCode,
			strings.TrimSpace(FirstN(string(body), 200)))
	}
	return body, nil
}

// QuerySeries = Do + DecodeSeries.
func QuerySeries(ctx context.Context, req Request) ([]Series, error) {
	body, err := Do(ctx, req)
	if err != nil {
		return nil, err
	}
	return DecodeSeries(labelOr(req.Label), body)
}

// QueryStrings = Do + DecodeStrings.
func QueryStrings(ctx context.Context, req Request) ([]string, error) {
	body, err := Do(ctx, req)
	if err != nil {
		return nil, err
	}
	return DecodeStrings(labelOr(req.Label), body)
}

func labelOr(l string) string {
	if l == "" {
		return "prometheus-api"
	}
	return l
}

// FirstN truncates for error messages. Rune-safe: slicing bytes mid-rune
// puts U+FFFD in an operator-visible string (the v0.9.842 truncate trap).
func FirstN(s string, n int) string {
	if len(s) <= n {
		return s
	}
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…"
}
