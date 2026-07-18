package api

import (
	"context"
	"sync"

	"github.com/cilcenk/coremetry/internal/chstore"
)

// service_runtimes_cache.go — v0.9.46 (operator-reported: /services
// listesinde HİÇ rozet yok, detay sayfasının TECHNOLOGY bölümü dolu).
//
// Kök neden: batch /api/services-runtimes tek bir 1h span taramasına
// dayanıyordu; prod hacminde Array kolon taraması 10s bütçeyi aşınca
// endpoint hata dönüyor ve TÜM rozetler birden kayboluyordu. Tekil
// detay sorgusu (PK-pruned LIMIT 1) etkilenmediği için TECHNOLOGY
// çalışmaya devam ediyordu.
//
// Bu katman iki şey ekler:
//  1. MERGE — chstore artık 15 dakikalık (ucuz) pencere tarar; burada
//     önceki turların sonuçlarıyla birleştirilir. Runtime yalnız
//     deploy'da değişir: bir kez çözülen servis, o an boşta diye
//     rozetini KAYBETMEZ.
//  2. STALE-SERVE — CH taraması hata verirse (timeout dahil) son
//     birleşik harita servis edilir; rozetler toptan sönmez
//     (görünmez-düşer: hata görünmezleşir, veri kalır).
//
// Eşzamanlılık: harita copy-on-write değiştirilir ve çağırana
// paylaşılan snapshot dönülür — dönen haritaya YAZILMAZ (bugünkü
// Go 1.25 swissmap fatal sınıfına karşı: okuyucular eski snapshot'ı
// güvenle tutar, yazıcı yeni haritayı tek atomik atamayla koyar).
type serviceRuntimesCache struct {
	mu     sync.Mutex
	merged map[string]chstore.ServiceRuntime
}

// refresh merges fresh rows over the previous snapshot and returns
// the NEW combined map. Always builds a fresh map — never mutates
// the one previously handed to readers.
func (c *serviceRuntimesCache) refresh(fresh map[string]chstore.ServiceRuntime) map[string]chstore.ServiceRuntime {
	c.mu.Lock()
	defer c.mu.Unlock()
	next := make(map[string]chstore.ServiceRuntime, len(c.merged)+len(fresh))
	for k, v := range c.merged {
		next[k] = v
	}
	for k, v := range fresh {
		// Attribute'suz (boş) taze satır, önceki dolu çözümü EZMEZ —
		// argMaxIf'in boş dönmesi "şu 15 dakikada attribute'lu span
		// yok" demektir, "runtime değişti" değil.
		if v.Language == "" && v.RuntimeName == "" && v.SDKVersion == "" {
			if old, ok := next[k]; ok &&
				(old.Language != "" || old.RuntimeName != "" || old.SDKVersion != "") {
				continue
			}
		}
		next[k] = v
	}
	c.merged = next
	return next
}

// snapshot returns the last merged map (nil when never populated).
func (c *serviceRuntimesCache) snapshot() map[string]chstore.ServiceRuntime {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.merged
}

// serviceRuntimesMerged is the handler-facing read: fresh scan
// merged over history; on scan error the stale snapshot (when we
// have one) instead of a hard failure.
func (s *Server) serviceRuntimesMerged(ctx context.Context) (map[string]chstore.ServiceRuntime, error) {
	fresh, err := s.store.GetAllServiceRuntimes(ctx)
	if err != nil {
		if snap := s.svcRuntimes.snapshot(); snap != nil {
			return snap, nil
		}
		return nil, err
	}
	return s.svcRuntimes.refresh(fresh), nil
}
