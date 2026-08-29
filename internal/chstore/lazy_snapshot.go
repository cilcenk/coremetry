package chstore

// lazy_snapshot.go — v0.10.158: tick/süpürme-yerel, İLK ihtiyaçta bir kez
// okunan açık-problem snapshot'ı.
//
// v0.10.156 (inceleme D3) monitor tick'ine ve promotion süpürmesine
// "tick başına tek snapshot" hoist etti — ama KOŞULSUZ: monitor tick'i
// 5 s'de bir, durum değişimi/keep-alive olmasa da tam `problems FINAL`
// taraması yapıyordu (lokal ölçüm 2026-08-29: snapshot taraması 47 →
// 93 / 10 dk; FindOpenProblem 24 → 0, ListProblems 75 → 14 — yani kazanç
// tick başına koşulsuz taramaya yenildi). Sözleşme (lazy_snapshot_test.go):
//   - Get çağrılmadan fetch ÇALIŞMAZ.
//   - Aynı örnekte tekrar Get'ler fetch'i bir kez daha ÇAĞIRMAZ (sonuç ve
//     hata örnek ömrünce sabit — bir tick'in okuyamaması CH'yi dövmez,
//     sonraki tick yeni örnekle dener).
//   - nil alıcı güvenli (nil snapshot, nil hata → ByKey nil).
// Tek goroutine kullanımı (tick-yerel); kilit yok.

import "context"

type LazySnapshot struct {
	fetch func(ctx context.Context) (*OpenProblems, error)
	done  bool
	snap  *OpenProblems
	err   error
}

// NewLazySnapshot — fetch'i erteler; test için enjekte edilebilir.
func NewLazySnapshot(fetch func(ctx context.Context) (*OpenProblems, error)) *LazySnapshot {
	return &LazySnapshot{fetch: fetch}
}

// LazyOpenSnapshot — OpenProblemsSnapshot (5 s memo) üzerine tembel sarmal.
func (s *Store) LazyOpenSnapshot() *LazySnapshot {
	return NewLazySnapshot(s.OpenProblemsSnapshot)
}

// Get — ilk çağrıda fetch, sonrakilerde aynı sonuç.
func (l *LazySnapshot) Get(ctx context.Context) (*OpenProblems, error) {
	if l == nil {
		return nil, nil
	}
	if !l.done {
		l.snap, l.err = l.fetch(ctx)
		l.done = true
	}
	return l.snap, l.err
}

// Fetched — fetch en az bir kez çalıştı mı (test/teşhis).
func (l *LazySnapshot) Fetched() bool { return l != nil && l.done }
