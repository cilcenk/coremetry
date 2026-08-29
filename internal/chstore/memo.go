package chstore

// memo.go — v0.10.156: süreç-içi, kısa TTL'li tek-uçuş memo.
//
// İlk tüketici OpenProblemsSnapshot: arka plan işleri (evaluator escalation,
// fusion, rootcause-synth, problem-explainer, watcher/SLO/monitor dedup
// aramaları) her tick'te ayrı ayrı aynı 7k satırlık `problems FINAL`
// taramasını yapıyordu — 15/dk, 2.0 CPU-s/dk (%12 app CH CPU, lokal ölçüm
// 2026-08-29). Tick ≥10 s; 5 s'lik memo tick-içi tekrarları tek taramaya
// indirir, yazımda düşürülür (Invalidate) — bayatlık yalnız başka pod'un
// yazımı için ve en çok TTL kadar.
//
// Sözleşme (memo_test.go pinler):
//   - TTL içinde ikinci Get fn'i ÇAĞIRMAZ, aynı değeri döner.
//   - TTL geçince fn yeniden çağrılır.
//   - Invalidate sonraki Get'i fn'e gönderir.
//   - fn hatası CACHE'LENMEZ (bir sonraki Get yeniden dener).
//   - Eşzamanlı Get'ler tek uçuşta birleşir (mutex altında fn; bekleyenler
//     aynı değeri alır).
//   - Invalidate KİLİTLENMEZ (v0.10.156 inceleme D2): yazım yolu (operatör
//     ack/assignee PATCH → UpsertProblem) 20 s'lik bir arka plan taramasının
//     arkasında beklemez. Nesil sayacı: fn sırasında Invalidate gelirse
//     sonuç çağırana döner ama SAKLANMAZ — "uçuştaki tarama yazımdan sonra
//     servis edilmez" özelliği korunur.

import (
	"context"
	"sync"
	"sync/atomic"
	"time"
)

type ttlMemo[T any] struct {
	mu     sync.Mutex
	ttl    time.Duration
	now    func() time.Time // test için; nil = time.Now
	gen    atomic.Uint64    // Invalidate her çağrıda artırır (kilitsiz)
	val    T
	at     time.Time
	valGen uint64 // val'in üretimine BAŞLANDIĞI andaki gen
	have   bool
}

func newTTLMemo[T any](ttl time.Duration) *ttlMemo[T] {
	return &ttlMemo[T]{ttl: ttl}
}

func (m *ttlMemo[T]) clock() time.Time {
	if m.now != nil {
		return m.now()
	}
	return time.Now()
}

// Get — taze değer varsa onu döner; yoksa fn'i (mutex altında, tek uçuş)
// çağırır ve başarılıysa saklar.
func (m *ttlMemo[T]) Get(ctx context.Context, fn func(ctx context.Context) (T, error)) (T, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.have && m.valGen == m.gen.Load() && m.clock().Sub(m.at) < m.ttl {
		return m.val, nil
	}
	g := m.gen.Load() // fn'den ÖNCE: fn sırasında gelen Invalidate g'yi eskitir
	v, err := fn(ctx)
	if err != nil {
		var zero T
		return zero, err
	}
	m.val, m.at, m.valGen, m.have = v, m.clock(), g, true
	return v, nil
}

// Invalidate — bir sonraki Get fn'e gider (yazım sonrası). Kilitsiz:
// uçuştaki bir Get'i beklemez.
func (m *ttlMemo[T]) Invalidate() {
	m.gen.Add(1)
}
