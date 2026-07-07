package logstore

// logstore_pivot_test.go — pins the v0.8.330 pivot read semantics:
//   - LogsForTrace / LogsForSpan build EXACTLY the Filter the backends
//     already honour (TraceID / SpanID / From / To / capped Limit) — the
//     "implemented once over Search" contract, so CH and ES parity is
//     structural, not per-backend code.
//   - SearchWithTimeout maps slow/unreachable (deadline, transport timeout,
//     dial failure) to ErrBackendSlow, and ONLY those — a genuine query
//     error must pass through so it surfaces as the bug it is.

import (
	"context"
	"errors"
	"net"
	"reflect"
	"testing"
	"time"
)

// stubStore is a mocked Store: Search is captured/scripted, everything else
// panics via the embedded nil interface if touched (these reads must only
// ever go through Search).
type stubStore struct {
	Store             // embed the interface: unused methods panic if called
	backend string
	got     Filter
	calls   int
	page    *Page
	err     error
	delay   time.Duration // > 0: honour ctx cancellation while "querying"
}

func (s *stubStore) Search(ctx context.Context, f Filter) (*Page, error) {
	s.calls++
	s.got = f
	if s.delay > 0 {
		select {
		case <-ctx.Done():
			return nil, ctx.Err() // what both real backends surface on deadline
		case <-time.After(s.delay):
		}
	}
	return s.page, s.err
}

func (s *stubStore) Backend() string { return s.backend }

func TestLogsForTrace_BuildsFilter(t *testing.T) {
	st := &stubStore{page: &Page{Logs: []*LogRecord{}}}
	from := time.Unix(100, 0)
	to := time.Unix(200, 0)
	if _, err := LogsForTrace(context.Background(), st, "abc123", from, to, 42); err != nil {
		t.Fatalf("LogsForTrace: %v", err)
	}
	f := st.got
	if f.TraceID != "abc123" {
		t.Errorf("TraceID = %q, want abc123", f.TraceID)
	}
	if f.SpanID != "" {
		t.Errorf("SpanID = %q, want empty (trace-level read)", f.SpanID)
	}
	if !f.From.Equal(from) || !f.To.Equal(to) {
		t.Errorf("window = [%v, %v], want [%v, %v]", f.From, f.To, from, to)
	}
	if f.Limit != 42 {
		t.Errorf("Limit = %d, want 42", f.Limit)
	}
	// Zero window passes through untouched — trace_id must stay the sole
	// filter on both backends (ES ignores the window with TraceID set; CH
	// AND-applies it, so a synthetic default here would DROP older logs).
	if _, err := LogsForTrace(context.Background(), st, "abc123", time.Time{}, time.Time{}, 0); err != nil {
		t.Fatalf("LogsForTrace zero-window: %v", err)
	}
	if !st.got.From.IsZero() || !st.got.To.IsZero() {
		t.Errorf("zero window was defaulted: [%v, %v]", st.got.From, st.got.To)
	}
	if st.got.Limit != 500 {
		t.Errorf("default Limit = %d, want 500 (the drawer cap)", st.got.Limit)
	}
}

func TestLogsForSpan_BuildsFilter(t *testing.T) {
	st := &stubStore{page: &Page{}}
	if _, err := LogsForSpan(context.Background(), st, "trace1", "span1", time.Unix(1, 0), time.Unix(2, 0), 9999); err != nil {
		t.Fatalf("LogsForSpan: %v", err)
	}
	if st.got.TraceID != "trace1" || st.got.SpanID != "span1" {
		t.Errorf("filter = trace %q span %q, want trace1/span1", st.got.TraceID, st.got.SpanID)
	}
	if st.got.Limit != 500 {
		t.Errorf("Limit = %d, want cap 500 (caller asked 9999)", st.got.Limit)
	}
}

func TestLogsForTraceSpan_RequireIDs(t *testing.T) {
	st := &stubStore{page: &Page{}}
	if _, err := LogsForTrace(context.Background(), st, "", time.Time{}, time.Time{}, 0); err == nil {
		t.Error("LogsForTrace accepted an empty trace id")
	}
	if _, err := LogsForSpan(context.Background(), st, "t", "", time.Time{}, time.Time{}, 0); err == nil {
		t.Error("LogsForSpan accepted an empty span id")
	}
	if st.calls != 0 {
		t.Errorf("validation failures still hit the backend (%d calls)", st.calls)
	}
}

func TestSearchWithTimeout_DeadlineMapsToErrBackendSlow(t *testing.T) {
	// (a) Our own timeout fires mid-query: the stub honours ctx like the real
	// backends do and returns ctx.Err().
	slow := &stubStore{delay: 200 * time.Millisecond, page: &Page{}}
	_, err := SearchWithTimeout(context.Background(), slow, Filter{TraceID: "t"}, 10*time.Millisecond)
	if !errors.Is(err, ErrBackendSlow) {
		t.Errorf("mid-query deadline: err = %v, want ErrBackendSlow", err)
	}

	// (b) The backend surfaces DeadlineExceeded itself (already wrapped by
	// its driver) — the default-timeout path used by LogsForTrace.
	deadlined := &stubStore{err: context.DeadlineExceeded}
	_, err = LogsForTrace(context.Background(), deadlined, "t", time.Time{}, time.Time{}, 0)
	if !errors.Is(err, ErrBackendSlow) {
		t.Errorf("backend deadline: err = %v, want ErrBackendSlow", err)
	}
}

func TestSearchWithTimeout_ConnRefusedMapsToErrBackendSlow(t *testing.T) {
	// The go-elasticsearch transport surfaces a refused dial as *net.OpError
	// (usually inside *url.Error — errors.As unwraps either way).
	refused := &stubStore{err: &net.OpError{
		Op: "dial", Net: "tcp",
		Err: errors.New("connect: connection refused"),
	}}
	_, err := SearchWithTimeout(context.Background(), refused, Filter{TraceID: "t"}, time.Second)
	if !errors.Is(err, ErrBackendSlow) {
		t.Errorf("dial refused: err = %v, want ErrBackendSlow", err)
	}
}

func TestSearchWithTimeout_QueryErrorPassesThrough(t *testing.T) {
	// A genuine query failure is a bug to surface, not a condition to
	// degrade on — it must NOT be swallowed into ErrBackendSlow.
	queryErr := errors.New("elasticsearch: 400 parse_exception")
	bad := &stubStore{err: queryErr}
	_, err := SearchWithTimeout(context.Background(), bad, Filter{TraceID: "t"}, time.Second)
	if errors.Is(err, ErrBackendSlow) {
		t.Errorf("query error was masked as ErrBackendSlow: %v", err)
	}
	if !errors.Is(err, queryErr) {
		t.Errorf("query error not passed through: %v", err)
	}
}

func TestSearchWithTimeout_ClientCancelStaysCanceled(t *testing.T) {
	// The CLIENT walking away (parent ctx canceled) is not a slow backend —
	// mapping it to ErrBackendSlow would cache degraded payloads for healthy
	// backends every time an operator closes a tab mid-flight.
	parent, cancel := context.WithCancel(context.Background())
	cancel()
	gone := &stubStore{err: context.Canceled}
	_, err := SearchWithTimeout(parent, gone, Filter{TraceID: "t"}, time.Second)
	if errors.Is(err, ErrBackendSlow) {
		t.Errorf("client cancellation was misclassified as a slow backend: %v", err)
	}
}

func TestLogsForTrace_ParityAcrossBackends(t *testing.T) {
	// CH+ES parity is BY CONSTRUCTION — one shared code path over Search.
	// This pins it: two distinct Store impls must receive the identical
	// Filter for the same pivot call.
	ch := &stubStore{backend: "clickhouse", page: &Page{}}
	es := &stubStore{backend: "elasticsearch", page: &Page{}}
	from, to := time.Unix(10, 0), time.Unix(20, 0)
	for _, st := range []*stubStore{ch, es} {
		if _, err := LogsForTrace(context.Background(), st, "deadbeef", from, to, 100); err != nil {
			t.Fatalf("%s: %v", st.backend, err)
		}
	}
	if !reflect.DeepEqual(ch.got, es.got) {
		t.Errorf("backend filter divergence:\n  ch: %+v\n  es: %+v", ch.got, es.got)
	}
}
