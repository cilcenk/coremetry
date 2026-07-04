package api

import (
	"context"
	"testing"
	"time"

	"github.com/cilcenk/coremetry/internal/logstore"
)

// v0.8.236 — shared live-tail broadcaster contracts:
//   - tailFilterKey hashes ALL filter inputs (v0.5.187 rule): any
//     single field difference → different group; identical filters →
//     same group (that's the whole point of sharing).
//   - refcount lifecycle: last detach removes the group (a leaked
//     group = a poll goroutine querying the backend forever).
//   - overflow: a full mailbox never blocks broadcast; the slow
//     subscriber's next delivered event carries gap=true.

type stubLogStore struct{}

func (stubLogStore) Search(context.Context, logstore.Filter) (*logstore.Page, error) {
	return &logstore.Page{}, nil
}
func (stubLogStore) CountPatterns(context.Context, []logstore.PatternSpec, time.Time, time.Time, time.Time) ([]logstore.PatternStats, error) {
	return nil, nil
}
func (stubLogStore) Histogram(context.Context, logstore.Filter, int, string) ([]logstore.LogSeries, error) {
	return nil, nil
}
func (stubLogStore) EQLSearch(context.Context, logstore.EQLQuery) ([]logstore.EQLSequence, error) {
	return nil, nil
}
func (stubLogStore) Indices(context.Context) ([]logstore.IndexInfo, error) { return nil, nil }
func (stubLogStore) FieldValues(context.Context, string, string, int) ([]string, error) {
	return nil, nil
}
func (stubLogStore) FieldStats(context.Context, logstore.Filter, string, int) (*logstore.FieldStatsResult, error) {
	return nil, nil
}
func (stubLogStore) Backend() string            { return "stub" }
func (stubLogStore) Ping(context.Context) error { return nil }

func TestTailFilterKeyHashesAllInputs(t *testing.T) {
	base := logstore.Filter{Service: "svc", Cluster: "cl", Search: "err", SeverityMin: 9, TraceID: "t1", SpanID: "s1"}
	variants := []logstore.Filter{
		{Service: "svc2", Cluster: "cl", Search: "err", SeverityMin: 9, TraceID: "t1", SpanID: "s1"},
		{Service: "svc", Cluster: "cl2", Search: "err", SeverityMin: 9, TraceID: "t1", SpanID: "s1"},
		{Service: "svc", Cluster: "cl", Search: "err2", SeverityMin: 9, TraceID: "t1", SpanID: "s1"},
		{Service: "svc", Cluster: "cl", Search: "err", SeverityMin: 13, TraceID: "t1", SpanID: "s1"},
		{Service: "svc", Cluster: "cl", Search: "err", SeverityMin: 9, TraceID: "t2", SpanID: "s1"},
		{Service: "svc", Cluster: "cl", Search: "err", SeverityMin: 9, TraceID: "t1", SpanID: "s2"},
	}
	key := tailFilterKey(base)
	if key != tailFilterKey(base) {
		t.Fatal("key must be deterministic")
	}
	for i, v := range variants {
		if tailFilterKey(v) == key {
			t.Errorf("variant %d must not share the base group (field not hashed)", i)
		}
	}
	// Field-boundary safety: ("ab","c") vs ("a","bc") must differ.
	a := tailFilterKey(logstore.Filter{Service: "ab", Cluster: "c"})
	b := tailFilterKey(logstore.Filter{Service: "a", Cluster: "bc"})
	if a == b {
		t.Error("field separator missing — concatenation collision")
	}
	// SinceNs is a per-client cursor, NOT part of the group identity.
	c := base
	c.SinceNs = 12345
	if tailFilterKey(c) != key {
		t.Error("SinceNs must not split the group")
	}
}

func TestTailBrokerRefcountLifecycle(t *testing.T) {
	b := newTailBroker(stubLogStore{})
	f := logstore.Filter{Service: "payments"}

	_, _, d1 := b.subscribe(f)
	_, _, d2 := b.subscribe(f)
	b.mu.Lock()
	n := len(b.groups)
	b.mu.Unlock()
	if n != 1 {
		t.Fatalf("same filter must share one group, got %d", n)
	}

	// Different filter → its own group.
	_, _, d3 := b.subscribe(logstore.Filter{Service: "orders"})
	b.mu.Lock()
	n = len(b.groups)
	b.mu.Unlock()
	if n != 2 {
		t.Fatalf("distinct filter must get its own group, got %d", n)
	}

	d1()
	d1() // double-detach must be safe (sync.Once)
	b.mu.Lock()
	n = len(b.groups)
	b.mu.Unlock()
	if n != 2 {
		t.Fatalf("group must survive while a subscriber remains, got %d", n)
	}

	d2()
	d3()
	b.mu.Lock()
	n = len(b.groups)
	b.mu.Unlock()
	if n != 0 {
		t.Fatalf("last detach must remove the group, got %d", n)
	}
}

func TestTailBroadcastOverflowGap(t *testing.T) {
	g := &tailGroup{subs: map[*tailSub]struct{}{}}
	sub := &tailSub{ch: make(chan tailEvent, 1)}
	g.subs[sub] = struct{}{}

	ev := tailEvent{logs: []*logstore.LogRecord{{ID: 1}}}
	g.broadcast(ev) // fills the size-1 mailbox
	g.broadcast(ev) // full → dropped for this sub, overflowed set

	<-sub.ch // drain the first event
	g.broadcast(tailEvent{logs: []*logstore.LogRecord{{ID: 2}}})
	got := <-sub.ch
	if !got.gap {
		t.Fatal("event after an overflow-drop must carry gap=true (no silent loss)")
	}
	// And once delivered, the flag resets.
	g.broadcast(tailEvent{logs: []*logstore.LogRecord{{ID: 3}}})
	if got := <-sub.ch; got.gap {
		t.Fatal("gap flag must clear after a successful delivery")
	}
}
