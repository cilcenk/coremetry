package sse

import (
	"context"
	"encoding/json"
	"testing"
	"time"
)

// broker_test.go — v0.10.200 sözleşmesi (rollouts Faz 3, audit §12):
// PublishLocal köprüyü GEÇMEZ (N pod × N tail = N× teslim olurdu), Publish
// geçer, kendi yankısı düşer, dolu abone kanalı sayaçla düşer.

// fakeBridge — iki broker'ı bağlayan kanal hub'ı (Redis pub/sub ikamesi).
type fakeBridge struct {
	hub  chan []byte
	subs []chan []byte
}

func newFakeBridge() *fakeBridge { return &fakeBridge{hub: make(chan []byte, 64)} }

func (f *fakeBridge) Publish(_ context.Context, _ string, msg []byte) error {
	for _, s := range f.subs {
		s <- append([]byte(nil), msg...)
	}
	return nil
}

func (f *fakeBridge) Subscribe(_ context.Context, _ string) (<-chan []byte, error) {
	ch := make(chan []byte, 64)
	f.subs = append(f.subs, ch)
	return ch, nil
}

func recvKinds(ch chan Event, wait time.Duration) []string {
	var out []string
	deadline := time.After(wait)
	for {
		select {
		case ev := <-ch:
			out = append(out, ev.Kind)
		case <-deadline:
			return out
		}
	}
}

func TestPublishLocalDoesNotCrossBridge(t *testing.T) {
	br := newFakeBridge()
	a, b := NewBroker(), NewBroker()
	a.SetBridge(br)
	b.SetBridge(br)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	a.StartBridge(ctx)
	b.StartBridge(ctx)

	chA := make(chan Event, 8)
	chB := make(chan Event, 8)
	defer a.Subscribe(chA)()
	defer b.Subscribe(chB)()
	if a.Subscribers() != 1 || b.Subscribers() != 1 {
		t.Fatalf("abone sayısı: %d %d", a.Subscribers(), b.Subscribers())
	}

	a.PublishLocal(KindRollout, map[string]any{"n": 3})
	if got := recvKinds(chA, 100*time.Millisecond); len(got) != 1 || got[0] != KindRollout {
		t.Fatalf("yerel abone olayı almalı: %v", got)
	}
	if got := recvKinds(chB, 150*time.Millisecond); len(got) != 0 {
		t.Fatalf("PublishLocal köprüyü GEÇMEMELİ (N pod tail = N× teslim): %v", got)
	}

	// Publish köprüyü geçer; üretici pod kendi yankısını düşürür (çift teslim yok)
	a.Publish("problem.open", map[string]string{"id": "p1"})
	if got := recvKinds(chB, 300*time.Millisecond); len(got) != 1 || got[0] != "problem.open" {
		t.Fatalf("Publish köprüden karşı pod'a ulaşmalı: %v", got)
	}
	if got := recvKinds(chA, 150*time.Millisecond); len(got) != 1 {
		t.Fatalf("üretici pod TEK teslim almalı (yankı düşer): %v", got)
	}
}

func TestDroppedCounterOnFullSubscriber(t *testing.T) {
	b := NewBroker()
	ch := make(chan Event) // tamponsuz + okuyucu yok → her olay düşer
	defer b.Subscribe(ch)()
	before := b.Dropped()
	for i := 0; i < 5; i++ {
		b.PublishLocal(KindRollout, map[string]any{"n": i})
	}
	if got := b.Dropped() - before; got != 5 {
		t.Fatalf("düşen olay sayılmalı: %d", got)
	}
}

func TestPublishLocalPayloadShape(t *testing.T) {
	b := NewBroker()
	ch := make(chan Event, 1)
	defer b.Subscribe(ch)()
	b.PublishLocal(KindRollout, map[string]any{"n": 7})
	ev := <-ch
	var p struct {
		N int `json:"n"`
	}
	if err := json.Unmarshal(ev.Payload, &p); err != nil || p.N != 7 || ev.Kind != "rollout" {
		t.Fatalf("zarf: %+v %v", ev, err)
	}
}
