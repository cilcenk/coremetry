package vmetrics

// v0.9.1150 — config-plumbing half of the VictoriaMetrics read backend.
//
// The properties pinned here are the ones whose failure is invisible in
// review: a token that leaks into a GET response body, and a "Configured"
// predicate that routes reads at a backend with no URL.

import (
	"context"
	"encoding/json"
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

// Configured is the ONE predicate the API's source selector reads. A
// half-filled form (enabled, no URL) must NOT route metric reads at VM —
// there is no fallback, so it would 502 every metric surface.
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
