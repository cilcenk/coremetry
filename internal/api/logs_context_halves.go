package api

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/cilcenk/coremetry/internal/logstore"
)

// searchContextHalves — v0.10.414 (log arama denetimi A7): /logs/context'in
// iki yarı penceresi GERÇEKTEN paralel (yorum yıllardır öyle diyordu,
// kod ardışıktı).
//
// errgroup DEĞİL: WithContext ilk hatada kardeşi iptal eder ve
// logstore.MapBackendSlow iptal edilmiş üst bağlamda ErrBackendSlow
// üretmeyi bilinçli reddeder (TestSearchWithTimeout_ClientCancelStaysCanceled)
// → yavaş backend bugünkü 200 {degraded} yerine 5xx olurdu. İki bağımsız
// hata yuvası; ErrBackendSlow öncelikli (bir yarı yavaşsa modal "yavaş"
// der), aksi hâlde ilk gerçek sorgu hatası.
//
// Bedel (v0.8.532 dersi — backend'e errgroup girmez): eşzamanlı ayak izi
// yarı başına 1→2; ama SkipTotal ile CH'de toplam 4 ardışık sorgu → 2
// paralel. Tıkla-tetiklenen, 15 sn önbellekli uç — poll değil.
func searchContextHalves(ctx context.Context, st logstore.Store, beforeF, afterF logstore.Filter, budget time.Duration) (before, after *logstore.Page, err error) {
	var wg sync.WaitGroup
	var errB, errA error
	wg.Add(2)
	go func() {
		defer wg.Done()
		before, errB = logstore.SearchWithTimeout(ctx, st, beforeF, budget)
	}()
	go func() {
		defer wg.Done()
		after, errA = logstore.SearchWithTimeout(ctx, st, afterF, budget)
	}()
	wg.Wait()
	for _, e := range []error{errB, errA} {
		if errors.Is(e, logstore.ErrBackendSlow) {
			return nil, nil, e
		}
	}
	if errB != nil {
		return nil, nil, errB
	}
	if errA != nil {
		return nil, nil, errA
	}
	return before, after, nil
}
