package api

// v0.8.350 (HA 🟡6) regression tests — the /logs read paths extend the
// v0.8.331 trace-pivot degrade contract: a slow/unreachable log backend
// (dial refused, transport timeout, deadline) returns HTTP 200
// {degraded:true, reason} + empty payload instead of a 5xx, while a
// GENUINE query error (ES 400, bad field, …) still fails loudly. One
// representative handler per payload shape:
//   - getLogs main search   → object with degraded flag (LogsResponse)
//   - getLogsFieldStats     → object with degraded flag (LogFieldStats)
//   - getLogsTimeseries     → bare ARRAY wire shape → degrades to []

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http/httptest"
	"testing"

	"github.com/cilcenk/coremetry/internal/logstore"
)

// degradeLogStore is a scripted logstore.Store: the embedded interface
// panics on anything the handler under test shouldn't touch.
type degradeLogStore struct {
	logstore.Store
	err error
}

func (s *degradeLogStore) Search(context.Context, logstore.Filter) (*logstore.Page, error) {
	return nil, s.err
}
func (s *degradeLogStore) FieldStats(context.Context, logstore.Filter, string, int) (*logstore.FieldStatsResult, error) {
	return nil, s.err
}
func (s *degradeLogStore) Histogram(context.Context, logstore.Filter, int, string) ([]logstore.LogSeries, error) {
	return nil, s.err
}
func (s *degradeLogStore) Backend() string { return "test" }

// dialRefused is the wire shape both real backends surface when the
// node is gone — *net.OpError, which isBackendSlow classifies as slow.
func dialRefused() error {
	return &net.OpError{Op: "dial", Net: "tcp", Err: errors.New("connect: connection refused")}
}

func degradeTestServer(err error) *Server {
	return &Server{
		logs:  &degradeLogStore{err: err},
		cache: &fakeCache{},
		l1:    newL1Cache(8),
		stats: newCacheStats(),
	}
}

func TestGetLogs_MainSearchDegradesOn200(t *testing.T) {
	s := degradeTestServer(dialRefused())
	w := httptest.NewRecorder()
	s.getLogs(w, httptest.NewRequest("GET", "/api/logs?service=checkout", nil))

	if w.Code != 200 {
		t.Fatalf("status = %d, want 200 (degrade contract) — body: %s", w.Code, w.Body.String())
	}
	var body struct {
		Total    int                  `json:"total"`
		Logs     []logstore.LogRecord `json:"logs"`
		Degraded bool                 `json:"degraded"`
		Reason   string               `json:"reason"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if !body.Degraded || body.Reason == "" {
		t.Errorf("degraded=%v reason=%q, want degraded:true with a reason", body.Degraded, body.Reason)
	}
	if body.Total != 0 || len(body.Logs) != 0 {
		t.Errorf("payload not empty: total=%d logs=%d", body.Total, len(body.Logs))
	}
}

func TestGetLogs_GenuineQueryErrorStays5xx(t *testing.T) {
	// A query error must NOT be masked as degraded — it's a bug to
	// surface, and caching a degraded payload for it would hide it.
	s := degradeTestServer(errors.New("parsing_exception: unknown field [svc]"))
	w := httptest.NewRecorder()
	s.getLogs(w, httptest.NewRequest("GET", "/api/logs?service=checkout", nil))
	if w.Code == 200 {
		t.Fatalf("status = 200 — genuine query error was swallowed into the degrade path: %s", w.Body.String())
	}
}

func TestGetLogsFieldStats_DegradesOn200(t *testing.T) {
	s := degradeTestServer(dialRefused())
	w := httptest.NewRecorder()
	s.getLogsFieldStats(w, httptest.NewRequest("GET", "/api/logs/fieldstats?field=service.name", nil))

	if w.Code != 200 {
		t.Fatalf("status = %d, want 200 — body: %s", w.Code, w.Body.String())
	}
	var body struct {
		Field    string `json:"field"`
		Degraded bool   `json:"degraded"`
		Values   []any  `json:"values"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if !body.Degraded || body.Field != "service.name" || len(body.Values) != 0 {
		t.Errorf("degraded=%v field=%q values=%d, want degraded:true, echoed field, empty values",
			body.Degraded, body.Field, len(body.Values))
	}
}

func TestGetLogsTimeseries_DegradesToEmptyArray(t *testing.T) {
	// The timeseries wire shape is a bare ARRAY (clients map over it),
	// so the degrade payload is [] — 200 + empty chart, never a 5xx.
	s := degradeTestServer(dialRefused())
	w := httptest.NewRecorder()
	s.getLogsTimeseries(w, httptest.NewRequest("GET", "/api/logs/timeseries?bucketSec=30", nil))

	if w.Code != 200 {
		t.Fatalf("status = %d, want 200 — body: %s", w.Code, w.Body.String())
	}
	var series []logstore.LogSeries
	if err := json.Unmarshal(w.Body.Bytes(), &series); err != nil {
		t.Fatalf("degraded timeseries is not an array (%v): %s", err, w.Body.String())
	}
	if len(series) != 0 {
		t.Errorf("series = %d, want empty", len(series))
	}
}
