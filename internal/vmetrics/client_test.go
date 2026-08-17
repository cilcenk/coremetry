package vmetrics

// v0.9.1150 — config-plumbing half of the VictoriaMetrics read backend.
//
// The properties pinned here are the ones whose failure is invisible in
// review: a token that leaks into a GET response body, and a "Configured"
// predicate that routes reads at a backend with no URL.

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/cilcenk/coremetry/internal/chstore"
)

type fakeStore struct {
	raw    []byte
	getErr error
	putErr error
	puts   int
	gets   int
}

func (f *fakeStore) GetVMetricsSettingsRaw(context.Context) ([]byte, error) {
	f.gets++
	return f.raw, f.getErr
}

func (f *fakeStore) PutVMetricsSettingsRaw(_ context.Context, raw []byte) error {
	if f.putErr != nil {
		return f.putErr
	}
	f.puts++
	f.raw = raw
	return nil
}

// Snapshot is the GET body. The token must never appear in it — HasToken
// is the only secret-adjacent bit the UI gets.
func TestSnapshotMasksToken(t *testing.T) {
	s := New()
	s.Configure(Settings{
		Enabled: true, BaseURL: "http://vm:8428",
		AuthType: "bearer", Token: "super-secret-token",
		InsecureSkipVerify: true,
	})
	snap := s.Snapshot()
	if !snap.HasToken {
		t.Fatal("hasToken must be true when a token is stored")
	}
	// Structural, not string-matching: the Snapshot type has no token
	// FIELD, so this is really a guard against someone adding one.
	blob := snapshotJSON(t, snap)
	if strings.Contains(blob, "super-secret-token") {
		t.Fatalf("token leaked into the snapshot: %s", blob)
	}
	if !strings.Contains(blob, `"hasToken":true`) {
		t.Fatalf("snapshot missing hasToken: %s", blob)
	}
	// And the reverse: no token stored → hasToken false, not "".
	s.Configure(Settings{Enabled: true, BaseURL: "http://vm:8428", AuthType: "none"})
	if s.Snapshot().HasToken {
		t.Fatal("hasToken must be false with no stored token")
	}
}

func snapshotJSON(t *testing.T, snap Snapshot) string {
	t.Helper()
	b, err := json.Marshal(snap)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return string(b)
}

// Configured is the predicate the API's source selector reads for a
// request that expresses no preference — "VM is the DEFAULT backend". A
// half-filled form (enabled, no URL) must NOT route metric reads at VM —
// there is no fallback, so it would 502 every metric surface.
//
// v0.9.1151 — it is no longer the ONLY predicate: Available() answers the
// per-request `?metricsrc=vm` trial gate (base URL, Enabled irrelevant).
// See TestAvailableIsTheTrialGate in trial_test.go for the split.
func TestConfigured(t *testing.T) {
	tests := []struct {
		name string
		cfg  Settings
		want bool
	}{
		{name: "enabled with url", cfg: Settings{Enabled: true, BaseURL: "http://vm:8428"}, want: true},
		{name: "disabled with url", cfg: Settings{Enabled: false, BaseURL: "http://vm:8428"}, want: false},
		{name: "enabled without url", cfg: Settings{Enabled: true}, want: false},
		{name: "enabled with blank url", cfg: Settings{Enabled: true, BaseURL: "   "}, want: false},
		{name: "zero value", cfg: Settings{}, want: false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			s := New()
			s.Configure(tc.cfg)
			if got := s.Configured(); got != tc.want {
				t.Fatalf("Configured() = %v, want %v", got, tc.want)
			}
		})
	}
	// nil receiver: main() always wires the service, but a partial init
	// must degrade to "ClickHouse", not panic.
	var nilSvc *Service
	if nilSvc.Configured() {
		t.Fatal("nil service must not report configured")
	}
	if nilSvc.Snapshot().Enabled {
		t.Fatal("nil service snapshot must be empty")
	}
}

func TestLoadPersistedRoundTrip(t *testing.T) {
	st := &fakeStore{}
	s := New()
	cfg := Settings{Enabled: true, BaseURL: "http://vm:8428", AuthType: "bearer", Token: "tok"}
	if err := s.SavePersisted(context.Background(), st, cfg); err != nil {
		t.Fatalf("save: %v", err)
	}
	if st.puts != 1 {
		t.Fatalf("puts = %d", st.puts)
	}
	// A fresh service hydrating from the same blob must see the token —
	// otherwise a peer pod would authenticate anonymously after a refresh.
	other := New()
	if err := other.LoadPersisted(context.Background(), st); err != nil {
		t.Fatalf("load: %v", err)
	}
	if got := other.CurrentSettings(); got != cfg {
		t.Fatalf("round-trip lost data: %+v", got)
	}
	if !other.Configured() {
		t.Fatal("hydrated service should be configured")
	}
}

func TestLoadPersistedEmptyBlobIsNotAnError(t *testing.T) {
	s := New()
	if err := s.LoadPersisted(context.Background(), &fakeStore{}); err != nil {
		t.Fatalf("empty blob should hydrate to the zero config, got %v", err)
	}
	if s.Configured() {
		t.Fatal("empty blob must leave the backend unconfigured")
	}
}

func TestLoadPersistedBadJSONErrors(t *testing.T) {
	s := New()
	err := s.LoadPersisted(context.Background(), &fakeStore{raw: []byte(`{"enabled":`)})
	if err == nil {
		t.Fatal("want a decode error")
	}
	// A corrupt blob must not half-apply.
	if s.Configured() {
		t.Fatal("a failed decode must leave the previous config in place")
	}
}

func TestNilStoreIsSafe(t *testing.T) {
	s := New()
	if err := s.LoadPersisted(context.Background(), nil); err != nil {
		t.Fatalf("nil store: %v", err)
	}
	if err := s.SavePersisted(context.Background(), nil, Settings{}); err != nil {
		t.Fatalf("nil store: %v", err)
	}
	// Must return immediately rather than tick forever.
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	s.StartConfigRefresh(ctx, nil, time.Millisecond)
}

// ready() is the "no silent fallback" gate: a read that arrives after the
// operator disabled VM must fail, not quietly answer from somewhere else.
func TestReadyRefusesUnconfigured(t *testing.T) {
	s := New()
	if _, err := s.ready(); err == nil {
		t.Fatal("want an error when unconfigured")
	}
	s.Configure(Settings{Enabled: true, BaseURL: "http://vm:8428"})
	if _, err := s.ready(); err != nil {
		t.Fatalf("configured service: %v", err)
	}
	// And every public read surfaces that error rather than empty data.
	s.Configure(Settings{})
	ctx := context.Background()
	if _, _, err := s.ListMetricNames(ctx, "", "", 10, 0); err == nil {
		t.Fatal("ListMetricNames must error when unconfigured")
	}
	if _, err := s.QueryMetric(ctx, chstore.MetricQueryFilter{Name: "m"}); err == nil {
		t.Fatal("QueryMetric must error when unconfigured")
	}
	if _, err := s.MetricLabelValues(ctx, "m", "pod", time.Hour); err == nil {
		t.Fatal("MetricLabelValues must error when unconfigured")
	}
	if _, err := s.MetricAttrKeys(ctx, "m", "", time.Hour); err == nil {
		t.Fatal("MetricAttrKeys must error when unconfigured")
	}
}

// ── Label discovery scope (v0.9.1159) ──────────────────────────────────────
//
// WIRE-level, not pure. discoveryNameCandidates is already table-tested in
// names_test.go; what these pin is the BINDING, which is the half that was
// broken: the candidate list can be perfect and the filter picker still empty
// if MetricLabelValues keeps building its own `__name__=` selector. This is
// the "pure test ≠ wiring" lesson applied to a Go seam.
//
// An empty picker is a worse failure than an empty chart, because it does not
// read as "wrong name" — it reads as "this metric has no attributes".
func TestMetricLabelValuesScopesTheCandidateAlternation(t *testing.T) {
	var gotMatch, gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotMatch = r.URL.Query().Get("match[]")
		_, _ = w.Write([]byte(`{"status":"success","data":["api-1"]}`))
	}))
	defer srv.Close()

	s := New()
	s.Configure(Settings{BaseURL: srv.URL})
	if _, err := s.MetricLabelValues(context.Background(),
		"http.server.request.duration", "pod", time.Hour); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if gotPath != "/api/v1/label/pod/values" {
		t.Fatalf("path = %q", gotPath)
	}
	assertDiscoveryScope(t, gotMatch)
}

func TestMetricAttrKeysScopesTheCandidateAlternation(t *testing.T) {
	var gotMatch string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMatch = r.URL.Query().Get("match[]")
		_, _ = w.Write([]byte(`{"status":"success","data":["pod","__name__"]}`))
	}))
	defer srv.Close()

	s := New()
	s.Configure(Settings{BaseURL: srv.URL})
	keys, err := s.MetricAttrKeys(context.Background(),
		"http.server.request.duration", "cart", time.Hour)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	assertDiscoveryScope(t, gotMatch)
	// The service matcher still rides alongside the name alternation — the
	// candidate change replaces ONE matcher, not the selector.
	if !strings.Contains(gotMatch, `service_name="cart"`) {
		t.Fatalf("service scope lost from match[]: %s", gotMatch)
	}
	// __name__ is still dropped from the ANSWER: it is not an attribute key.
	for _, k := range keys {
		if k == "__name__" {
			t.Fatal("__name__ offered as an attribute key")
		}
	}
}

// The four properties, hand-written so a revert to `__name__="<name>"` fails
// on the first two rather than passing on a technicality.
func assertDiscoveryScope(t *testing.T, match string) {
	t.Helper()
	if !strings.HasPrefix(match, `{__name__=~"^(`) {
		t.Fatalf("match[] is not a candidate alternation: %s", match)
	}
	// The spelling the operator's live install actually holds. `_count` stands
	// in for the whole histogram family.
	if !strings.Contains(match, "http_server_request_duration_seconds_count") {
		t.Fatalf("match[] misses the OTel-Prometheus histogram spelling: %s", match)
	}
	// And the verbatim one, regex-escaped, still leads.
	if !strings.Contains(match, `http\\.server\\.request\\.duration`) {
		t.Fatalf("match[] lost the verbatim spelling (or its escaping): %s", match)
	}
	// `le` lives on the bucket series. Scoping discovery there would offer the
	// histogram's own internal dimension as one of the operator's attributes.
	if strings.Contains(match, "_bucket") {
		t.Fatalf("discovery scoped the bucket series — le becomes a filter key: %s", match)
	}
}

func TestTestRejectsEmptyURL(t *testing.T) {
	s := New()
	if err := s.Test(context.Background(), Settings{}); err == nil {
		t.Fatal("Test must reject an empty base URL before dialling")
	}
}

func TestPromTime(t *testing.T) {
	// Unix seconds with millisecond precision — VM accepts a fractional
	// unix timestamp; an RFC3339 string with a trailing Z is the shape
	// that has bitten the CH bind path (chDateTime64Arg).
	got := promTime(time.Unix(1700000000, 500*int64(time.Millisecond)))
	if got != "1700000000.500" {
		t.Fatalf("promTime = %q", got)
	}
}
