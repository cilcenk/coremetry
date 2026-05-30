package api

import (
	"context"
	"encoding/json"
	"fmt"
	"hash/fnv"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/cilcenk/coremetry/internal/logstore"
)

// Live log tailer (v0.7.14) — pod-local SSE fan-out that replaces the /logs
// 2s browser poll. Each operator tailing logs used to drive ~30 logstore
// Search calls/min (the ES query load the scale-audit flagged at billion-doc
// scale). Instead, ONE tailer per api pod runs a single bounded Search per
// distinct (service, cluster, severity) filter every tick and pushes new rows
// to every SSE subscriber of that filter. Query load is O(distinct filters),
// not O(operators).
//
// Backend-agnostic — the tail goes through logstore.Store.Search, so it works
// on the Elasticsearch backend (the common deployment) as well as ClickHouse.
// Two ES-aware choices: rows are deduped by CONTENT HASH (ES doesn't populate
// LogRecord.ID), and the surfaced latency is bounded by the index
// refresh_interval — a 3s tick can't show a doc ES hasn't made searchable yet.

const (
	logTailInterval = 3 * time.Second
	logTailLimit    = 500 // rows per group per tick — bounded scan
	logSubBuffer    = 64  // per-subscriber channel depth before drop
)

// logFilter is the subset of a log query that scopes the backend Search. The
// free-text `search` is deliberately NOT here — it's applied per-subscriber
// in-memory so two operators that differ only by search term still share one
// backend query.
type logFilter struct {
	service  string
	cluster  string
	severity uint8
}

func (f logFilter) key() string {
	return f.service + "\x1f" + f.cluster + "\x1f" + strconv.Itoa(int(f.severity))
}

// logSub is one connected /logs live-tail operator.
type logSub struct {
	search string // case-insensitive substring on the body; "" = all
	ch     chan *logstore.LogRecord
}

// logGroup batches every subscriber sharing a logFilter behind one cursor.
type logGroup struct {
	filter   logFilter
	subs     map[*logSub]struct{}
	cursorNs int64               // max log timestamp emitted so far
	boundary map[uint64]struct{} // content hashes at exactly cursorNs (same-ns dedup)
}

type logTailer struct {
	store  logstore.Store
	mu     sync.Mutex
	groups map[string]*logGroup
}

func newLogTailer(store logstore.Store) *logTailer {
	return &logTailer{store: store, groups: map[string]*logGroup{}}
}

// logRowHash is a backend-agnostic identity for a row — ES doesn't populate
// LogRecord.ID, so we key dedup on (timestamp, service, body).
func logRowHash(r *logstore.LogRecord) uint64 {
	h := fnv.New64a()
	var ts [8]byte
	for i := 0; i < 8; i++ {
		ts[i] = byte(r.Timestamp >> (uint(i) * 8))
	}
	h.Write(ts[:])
	h.Write([]byte(r.ServiceName))
	h.Write([]byte{0})
	h.Write([]byte(r.Body))
	return h.Sum64()
}

// subscribe registers sub under its filter, creating the group (seeded at
// "now" so only NEW logs are tailed) if it's the first subscriber. The
// returned func deregisters; it does NOT close sub.ch (an in-flight tick may
// still hold the snapshot — sends are non-blocking, so the channel is simply
// GC'd once unreferenced; mirrors sse.Broker.Subscribe).
func (t *logTailer) subscribe(f logFilter, sub *logSub) func() {
	k := f.key()
	t.mu.Lock()
	g := t.groups[k]
	if g == nil {
		g = &logGroup{
			filter:   f,
			subs:     map[*logSub]struct{}{},
			cursorNs: time.Now().UnixNano(),
			boundary: map[uint64]struct{}{},
		}
		t.groups[k] = g
	}
	g.subs[sub] = struct{}{}
	t.mu.Unlock()
	return func() {
		t.mu.Lock()
		if g := t.groups[k]; g != nil {
			delete(g.subs, sub)
			if len(g.subs) == 0 {
				delete(t.groups, k)
			}
		}
		t.mu.Unlock()
	}
}

func (t *logTailer) run(ctx context.Context) {
	tk := time.NewTicker(logTailInterval)
	defer tk.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-tk.C:
			t.tick(ctx)
		}
	}
}

func (t *logTailer) tick(ctx context.Context) {
	t.mu.Lock()
	groups := make([]*logGroup, 0, len(t.groups))
	for _, g := range t.groups {
		groups = append(groups, g)
	}
	t.mu.Unlock()
	for _, g := range groups {
		t.refresh(ctx, g)
	}
}

func (t *logTailer) refresh(ctx context.Context, g *logGroup) {
	t.mu.Lock()
	if len(g.subs) == 0 {
		t.mu.Unlock()
		return
	}
	cursor, boundary, f := g.cursorNs, g.boundary, g.filter
	t.mu.Unlock()

	qctx, cancel := context.WithTimeout(ctx, logTailInterval)
	defer cancel()
	page, err := t.store.Search(qctx, logstore.Filter{
		Service:     f.service,
		Cluster:     f.cluster,
		SeverityMin: f.severity,
		From:        time.Unix(0, cursor),
		To:          time.Now(),
		Limit:       logTailLimit,
	})
	if err != nil || page == nil {
		return // transient; next tick retries from the same cursor
	}

	fresh := selectFreshLogs(page.Logs, cursor, boundary)
	if len(fresh) == 0 {
		return
	}
	sort.Slice(fresh, func(i, j int) bool { return fresh[i].Timestamp < fresh[j].Timestamp })
	newCursor, newBoundary := advanceLogCursor(fresh, cursor, boundary)

	t.mu.Lock()
	g.cursorNs, g.boundary = newCursor, newBoundary
	subs := make([]*logSub, 0, len(g.subs))
	for sub := range g.subs {
		subs = append(subs, sub)
	}
	t.mu.Unlock()

	for _, sub := range subs {
		for _, r := range fresh {
			if sub.search != "" && !strings.Contains(strings.ToLower(r.Body), sub.search) {
				continue
			}
			select {
			case sub.ch <- r:
			default: // slow consumer — drop rather than block the tick
			}
		}
	}
}

// selectFreshLogs returns the rows not yet emitted: strictly newer than the
// cursor, or at exactly the cursor ns but not in the boundary dedup set. Pure
// so the tail cursor logic is unit-testable (v0.7.14).
func selectFreshLogs(rows []*logstore.LogRecord, cursor int64, boundary map[uint64]struct{}) []*logstore.LogRecord {
	fresh := make([]*logstore.LogRecord, 0, len(rows))
	for _, r := range rows {
		if r.Timestamp < cursor {
			continue
		}
		if r.Timestamp == cursor {
			if _, seen := boundary[logRowHash(r)]; seen {
				continue
			}
		}
		fresh = append(fresh, r)
	}
	return fresh
}

// advanceLogCursor computes the next (cursor, boundary) after emitting fresh.
// boundary tracks the content hashes at the max timestamp so same-ns rows that
// arrive on a later tick aren't re-sent.
func advanceLogCursor(fresh []*logstore.LogRecord, cursor int64, boundary map[uint64]struct{}) (int64, map[uint64]struct{}) {
	newCursor := cursor
	for _, r := range fresh {
		if r.Timestamp > newCursor {
			newCursor = r.Timestamp
		}
	}
	next := map[uint64]struct{}{}
	if newCursor == cursor {
		for h := range boundary {
			next[h] = struct{}{}
		}
	}
	for _, r := range fresh {
		if r.Timestamp == newCursor {
			next[logRowHash(r)] = struct{}{}
		}
	}
	return newCursor, next
}

// handleLogStream is GET /api/logs/stream — an SSE live tail. Auth is already
// enforced by the mux middleware (logs are viewer-readable). Mirrors the
// sse.Handler transport (text/event-stream + 15s heartbeat + disconnect on
// ctx.Done) but with per-subscriber filtering and no Redis bridge (logs are
// high-volume + pod-local).
func (s *Server) handleLogStream(w http.ResponseWriter, r *http.Request) {
	if s.logTail == nil {
		http.Error(w, "log tailer unavailable", http.StatusServiceUnavailable)
		return
	}
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}
	q := r.URL.Query()
	sev, _ := strconv.Atoi(q.Get("severity"))
	sub := &logSub{
		search: strings.ToLower(strings.TrimSpace(q.Get("search"))),
		ch:     make(chan *logstore.LogRecord, logSubBuffer),
	}
	unsub := s.logTail.subscribe(logFilter{
		service:  q.Get("service"),
		cluster:  q.Get("cluster"),
		severity: uint8(sev),
	}, sub)
	defer unsub()

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache, no-transform")
	w.Header().Set("X-Accel-Buffering", "no")
	w.Header().Set("Connection", "keep-alive")
	fmt.Fprint(w, ": ok\n\n")
	flusher.Flush()

	heartbeat := time.NewTicker(15 * time.Second)
	defer heartbeat.Stop()
	ctx := r.Context()
	for {
		select {
		case <-ctx.Done():
			return
		case <-heartbeat.C:
			fmt.Fprint(w, ": ping\n\n")
			flusher.Flush()
		case row := <-sub.ch:
			if data, err := json.Marshal(row); err == nil {
				fmt.Fprintf(w, "event: log\ndata: %s\n\n", data)
				flusher.Flush()
			}
		}
	}
}
