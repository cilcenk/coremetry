package rollout

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

// reconciler_test.go — v0.10.199 çalışma zamanı sözleşmesi (reconciler.go başlığı).

type fakeStore struct {
	mu       sync.Mutex
	acts     []ActivityRow
	cut      string
	actErr   error
	prev     []Rollout
	prevErr  error
	seen     []FirstSeenRow
	seenErr  error
	upserts  [][]Rollout
	upErr    error
	runs     []Run
	runCtxOK []bool // RecordRun çağrısında ctx canlı mıydı
}

func (f *fakeStore) RolloutActivity(ctx context.Context, since time.Time, bucket time.Duration) ([]ActivityRow, string, error) {
	return f.acts, f.cut, f.actErr
}
func (f *fakeStore) RolloutRecentRows(ctx context.Context, since time.Time) ([]Rollout, error) {
	return f.prev, f.prevErr
}
func (f *fakeStore) RolloutFirstSeen(ctx context.Context, since time.Time) ([]FirstSeenRow, error) {
	return f.seen, f.seenErr
}
func (f *fakeStore) RolloutUpsert(ctx context.Context, rows []Rollout) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.upserts = append(f.upserts, rows)
	return f.upErr
}
func (f *fakeStore) RolloutRecordRun(ctx context.Context, run Run) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.runs = append(f.runs, run)
	f.runCtxOK = append(f.runCtxOK, ctx.Err() == nil)
	return nil
}

type fakeClusters []ClusterRef

func (c fakeClusters) Clusters() []ClusterRef { return c }

func enabled() Resolved {
	r := DefaultSettings().Resolved()
	r.Enabled = true
	return r
}

func newTestRec(st *fakeStore, res Resolved) *Reconciler {
	return New(st, fakeClusters{{ID: "c1", Name: "prod", SpanClusterValue: "prod-eu"}}, func() Resolved { return res })
}

func TestTickDeadline(t *testing.T) {
	for in, want := range map[time.Duration]time.Duration{30 * time.Second: 2*time.Minute + 30*time.Second, 10 * time.Second: 2 * time.Minute, 15 * time.Minute: 75 * time.Minute} {
		if got := tickDeadline(in); got != want {
			t.Errorf("%v → %v, want %v", in, got, want)
		}
	}
}

func TestMapClusters(t *testing.T) {
	refs := []ClusterRef{{ID: "a", Name: "A", SpanClusterValue: "prod-a"}, {ID: "b", Name: "B", SpanClusterValues: []string{"b1", "b2"}}, {ID: "n", Name: "by-name"}, {ID: "", Name: "disabled"}}
	rows := []ActivityRow{{ClusterValue: "prod-a", Workload: "w"}, {ClusterValue: "b2", Workload: "w"}, {ClusterValue: "by-name", Workload: "w"}, {ClusterValue: "zzz", Workload: "w"}, {ClusterValue: "zzz", Workload: "w2"}}
	out, un := MapClusters(rows, refs)
	if len(out) != 3 || out[0].ClusterID != "a" || out[1].ClusterID != "b" || out[2].ClusterID != "n" {
		t.Fatalf("eşleme: %+v", out)
	}
	if un["zzz"] != 2 || len(un) != 1 {
		t.Fatalf("eşlenmeyenler sayılmalı: %v", un)
	}
}

func TestTick_SkipsWhenDisabledOrNotLeader(t *testing.T) {
	st := &fakeStore{}
	res := enabled()
	res.Enabled = false
	rec := newTestRec(st, res)
	if rec.Tick(context.Background()) || len(st.runs) != 0 {
		t.Fatal("kapalıyken tik koşmamalı, koşu satırı yazmamalı")
	}
	rec = newTestRec(st, enabled())
	rec.SetLeaderCheck(func() bool { return false })
	if rec.Tick(context.Background()) || len(st.runs) != 0 {
		t.Fatal("lider değilken tik koşmamalı, koşu satırı yazmamalı")
	}
}

func TestTick_WritesRunRowEvenWhenTickCtxExpired(t *testing.T) {
	st := &fakeStore{actErr: context.DeadlineExceeded}
	rec := newTestRec(st, enabled())
	rec.Tick(context.Background())
	if len(st.runs) != 1 || st.runs[0].Status != RunFailed || !st.runCtxOK[0] {
		t.Fatalf("başarısız tik koşu satırını CANLI ctx ile yazmalı: %+v ok=%v", st.runs, st.runCtxOK)
	}
}

func TestTick_CancelledParentIsSkippedNotFailed(t *testing.T) {
	st := &fakeStore{actErr: context.Canceled}
	rec := newTestRec(st, enabled())
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	rec.tick(ctx, ctx, enabled())
	if len(st.runs) != 1 || st.runs[0].Status != RunSkipped {
		t.Fatalf("kapanışta kesilen tik skipped: %+v", st.runs)
	}
	// canlı parent + CH hatası → failed
	st = &fakeStore{actErr: errors.New("boom")}
	rec = newTestRec(st, enabled())
	rec.tick(context.Background(), context.Background(), enabled())
	if st.runs[0].Status != RunFailed || st.runs[0].SpanMs < 0 {
		t.Fatalf("CH hatası failed: %+v", st.runs)
	}
}

func TestTick_PartialOnUnmappedAndCut(t *testing.T) {
	b0 := time.Date(2026, 8, 30, 10, 0, 0, 0, time.UTC)
	st := &fakeStore{cut: "prod-eu/ns/w9", acts: []ActivityRow{{ClusterValue: "unknown-cluster", Namespace: "ns", Workload: "w", Revision: "w-1", Bucket: b0, Spans: 100}}}
	rec := newTestRec(st, enabled())
	rec.Tick(context.Background())
	run := st.runs[0]
	if run.Status != RunPartial || run.Error == "" || run.Clusters != 1 {
		t.Fatalf("eşlenmemiş + kesik → partial ve ilan: %+v", run)
	}
	for _, need := range []string{"unknown-cluster", "prod-eu/ns/w9"} {
		if !containsNote(run.Error, need) {
			t.Fatalf("koşu notu %q içermeli: %q", need, run.Error)
		}
	}
}

func TestTick_LeadershipRecheckBeforeUpsert(t *testing.T) {
	b := func(i int) time.Time {
		return time.Date(2026, 8, 30, 10, 0, 0, 0, time.UTC).Add(time.Duration(i) * 5 * time.Minute)
	}
	var acts []ActivityRow
	for i := 0; i <= 3; i++ {
		acts = append(acts, ActivityRow{ClusterValue: "prod-eu", Namespace: "ns", Workload: "w", Revision: "w-a", Bucket: b(i), Spans: 100})
	}
	for i := 3; i <= 12; i++ {
		acts = append(acts, ActivityRow{ClusterValue: "prod-eu", Namespace: "ns", Workload: "w", Revision: "w-b", Bucket: b(i), Spans: 100})
	}
	// pencere b(-70)'ten başlar (6 sa lookback): B'nin öncesinde ≥ 6 kovalık gözlenmiş yokluk var
	st := &fakeStore{acts: acts}
	rec := newTestRec(st, enabled())
	rec.now = func() time.Time { return b(13) }
	calls := 0
	rec.SetLeaderCheck(func() bool { calls++; return calls == 1 }) // ilk kontrol lider, ikincisi değil
	rec.Tick(context.Background())
	if len(st.upserts) != 0 || st.runs[0].Status != RunPartial || !containsNote(st.runs[0].Error, "liderlik") {
		t.Fatalf("liderlik kaybı yazımı atlamalı ve ilan etmeli: up=%d run=%+v", len(st.upserts), st.runs[0])
	}
	// lider kalırsa yazar
	st = &fakeStore{acts: acts}
	rec = newTestRec(st, enabled())
	rec.now = func() time.Time { return b(13) }
	rec.Tick(context.Background())
	if len(st.upserts) != 1 || st.runs[0].RolloutsWritten != len(st.upserts[0]) || st.runs[0].Status != RunOK {
		t.Fatalf("lider yazmalı: up=%d run=%+v", len(st.upserts), st.runs[0])
	}
	for _, r := range st.upserts[0] {
		if r.DetectedBy != "spans" {
			t.Fatalf("detected_by doldurulmalı: %+v", r)
		}
	}
}

func TestTick_UpsertErrorWritesFailedRun(t *testing.T) {
	b := func(i int) time.Time {
		return time.Date(2026, 8, 30, 10, 0, 0, 0, time.UTC).Add(time.Duration(i) * 5 * time.Minute)
	}
	var acts []ActivityRow
	for i := 0; i <= 20; i++ {
		acts = append(acts, ActivityRow{ClusterValue: "prod-eu", Namespace: "ns", Workload: "w", Revision: "w-a", Bucket: b(i), Spans: 100})
	}
	for i := 21; i <= 30; i++ {
		acts = append(acts, ActivityRow{ClusterValue: "prod-eu", Namespace: "ns", Workload: "w", Revision: "w-b", Bucket: b(i), Spans: 100})
	}
	st := &fakeStore{acts: acts, upErr: errors.New("ch write")}
	rec := newTestRec(st, enabled())
	rec.now = func() time.Time { return b(31) }
	rec.Tick(context.Background())
	if len(st.upserts) != 1 || len(st.runs) != 1 || st.runs[0].Status != RunFailed || st.runs[0].RolloutsWritten != 0 || !st.runCtxOK[0] {
		t.Fatalf("upsert hatası → failed koşu satırı (canlı ctx), yazılan 0: %+v", st.runs)
	}
	if !containsNote(st.runs[0].Error, "upsert") {
		t.Fatalf("hata notu: %q", st.runs[0].Error)
	}
	if _, ok := rec.LastRun(); !ok {
		t.Fatal("LastRun kopya dönmeli")
	}
}

func TestMapFirstSeen(t *testing.T) {
	refs := []ClusterRef{{ID: "a", Name: "A", SpanClusterValue: "prod-a"}}
	t1, t2 := time.Unix(100, 0), time.Unix(50, 0)
	m, ds := MapFirstSeen([]FirstSeenRow{{"prod-a", "ns", "w", "r1", t1}, {"prod-a", "ns", "w", "r1", t2}, {"zzz", "ns", "w", "r9", t1}, {"prod-a", "ns", "", "r1", t1}}, refs)
	if len(m) != 1 || !m[Key{"a", "ns", "w"}]["r1"].Equal(t2) || !ds["a"].Equal(t2) || len(ds) != 1 {
		t.Fatalf("en eski kova, eşlenmeyen düşer, küme veri başlangıcı: %+v %+v", m, ds)
	}
}

func TestTick_ReadErrorsFailRun(t *testing.T) {
	for name, st := range map[string]*fakeStore{"first-seen": {seenErr: errors.New("fs")}, "rows": {prevErr: errors.New("rows")}} {
		rec := newTestRec(st, enabled())
		rec.Tick(context.Background())
		if len(st.runs) != 1 || st.runs[0].Status != RunFailed || !containsNote(st.runs[0].Error, name) || len(st.upserts) != 0 {
			t.Fatalf("%s hatası → failed, yazım yok: %+v", name, st.runs)
		}
	}
}

func TestTick_OverlapIsSkipped(t *testing.T) {
	st := &fakeStore{}
	rec := newTestRec(st, enabled())
	rec.inFlight.Store(true)
	if rec.Tick(context.Background()) {
		t.Fatal("örtüşen tik atlanmalı")
	}
}

func TestRunJSON(t *testing.T) {
	r := Run{StartedAt: time.UnixMilli(1000), Host: "h", Status: RunOK, Clusters: 2}
	b, err := r.MarshalJSON()
	if err != nil || !containsNote(string(b), `"startedAt":1000`) || !containsNote(string(b), `"finishedAt":0`) || containsNote(string(b), `"error"`) {
		t.Fatalf("JSON: %s %v", b, err)
	}
}
