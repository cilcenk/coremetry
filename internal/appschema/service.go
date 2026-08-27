package appschema

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"sync"
	"time"
)

// Service — bellekteki katalog + system_settings kalıcılığı (v0.10.115).
// devops.Service deseni: LoadPersisted boot'ta, SavePersisted admin
// PUT'ta, StartConfigRefresh çok-pod yakınsaması için (5 dk — blob MB
// olabilir, 30 sn'lik devops kadansı burada israf olurdu).
type Service struct {
	mu  sync.RWMutex
	cat Catalog
}

// settingsStore — chstore'un dar arayüzü; paket *chstore.Store'u
// import etmez (tempo/devops ile aynı).
type settingsStore interface {
	GetSchemaCatalogRaw(ctx context.Context) ([]byte, error)
	PutSchemaCatalogRaw(ctx context.Context, raw []byte) error
}

func NewService() *Service { return &Service{} }

// Current — kataloğun kopyası değil, paylaşılan harita (salt-okunur
// kullanım); çağıran YAZMAZ.
func (s *Service) Current() Catalog {
	if s == nil {
		return Catalog{}
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.cat
}

// Summary — GET yanıtı; nil Service'te boş özet (SnapshotSQL yine dolu ki
// ekran sorguları gösterebilsin).
func (s *Service) Summary() Summary {
	if s == nil {
		return Catalog{}.Summarize()
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.cat.Summarize()
}

// Set — bellekteki kataloğu değiştirir (test ve LoadPersisted).
func (s *Service) Set(cat Catalog) {
	if s == nil {
		return
	}
	s.mu.Lock()
	s.cat = cat
	s.mu.Unlock()
}

// LoadPersisted — system_settings'ten yükler; kayıt yoksa sessiz.
func (s *Service) LoadPersisted(ctx context.Context, store settingsStore) error {
	raw, err := store.GetSchemaCatalogRaw(ctx)
	if err != nil {
		return err
	}
	if len(raw) == 0 {
		s.Set(Catalog{})
		return nil
	}
	var cat Catalog
	if err := json.Unmarshal(raw, &cat); err != nil {
		return fmt.Errorf("schema catalog decode: %w", err)
	}
	s.Set(cat)
	return nil
}

// SavePersisted — kataloğu yazar ve belleğe alır. Boş katalog = temizle
// (blob "{}" — GetSetting'in nil dönüşüyle aynı sonuç).
func (s *Service) SavePersisted(ctx context.Context, store settingsStore, cat Catalog) error {
	raw, err := json.Marshal(cat)
	if err != nil {
		return err
	}
	if err := store.PutSchemaCatalogRaw(ctx, raw); err != nil {
		return err
	}
	s.Set(cat)
	return nil
}

// StartConfigRefresh — çok-pod yakınsaması: her `every`de blobu yeniden
// okur (bir pod'daki import diğerlerinde görünsün). ctx bitince döner.
func (s *Service) StartConfigRefresh(ctx context.Context, store settingsStore, every time.Duration) {
	if s == nil || store == nil || every <= 0 {
		return
	}
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			if err := s.LoadPersisted(ctx, store); err != nil && ctx.Err() == nil {
				log.Printf("[appschema] refresh: %v", err)
			}
		}
	}
}
