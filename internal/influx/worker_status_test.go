package influx

import (
	"context"
	"testing"
	"time"
)

type fakePub struct {
	key string
	raw []byte
	n   int
}

func (f *fakePub) PutSetting(_ context.Context, key string, value []byte) error {
	f.key, f.raw, f.n = key, value, f.n+1
	return nil
}

// v0.10.333 — paylaşılan işçi durumu: codec gidiş-dönüş (kaynak sırası
// deterministik), yayın anahtarı, nil store sessiz.
func TestWorkerStatusCodecAndPublish(t *testing.T) {
	at := time.UnixMilli(1_700_000_000_000)
	raw, err := EncodeWorkerStatus("pod-b", at, []SourceStatus{{SourceID: "s2", Name: "two", LastRows: 5}, {SourceID: "s1", Name: "one", LastError: "x"}})
	if err != nil {
		t.Fatal(err)
	}
	snap, ok := DecodeWorkerStatus(raw)
	if !ok || snap.Pod != "pod-b" || snap.UpdatedAt != at.UnixMilli() || len(snap.Sources) != 2 || snap.Sources[0].SourceID != "s1" || snap.Sources[1].LastRows != 5 {
		t.Errorf("roundtrip: %+v", snap)
	}
	if _, ok := DecodeWorkerStatus(nil); ok {
		t.Error("boş → ok=false")
	}
	if _, ok := DecodeWorkerStatus([]byte("{bad")); ok {
		t.Error("bozuk → ok=false")
	}
	w := NewWorker(New(), nil)
	w.publishStatus(context.Background(), at) // store yok → sessiz
	fp := &fakePub{}
	w.SetStatusStore(fp)
	w.mu.Lock()
	w.status["s1"] = SourceStatus{SourceID: "s1", Name: "one", LastPoints: 3}
	w.mu.Unlock()
	w.publishStatus(context.Background(), at)
	if fp.n != 1 || fp.key != WorkerStatusKey {
		t.Fatalf("yayın: n=%d key=%s", fp.n, fp.key)
	}
	got, ok := DecodeWorkerStatus(fp.raw)
	if !ok || len(got.Sources) != 1 || got.Sources[0].LastPoints != 3 {
		t.Errorf("yayınlanan: %+v", got)
	}
}
