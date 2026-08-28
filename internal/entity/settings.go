package entity

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"strconv"
	"strings"
	"sync"
	"time"
)

// settings.go — ÖZELLİK BAYRAĞI + VİDALAR (system_settings["entity_layer"]).
//
// Şablon internal/vmetrics/client.go: tek JSON blob, boot LoadPersisted,
// çok-pod 30 s poll, admin PUT → SavePersisted + canlı swap. Varsayılan
// KAPALI: syncer koşmaz, uçlar 404 {disabled:true}, UI sekmeleri gizli;
// MV/kolonlar bayraktan bağımsız (veri katmanı migration'la geri alınır).
//
// Süreler operatör dizesi ("60s", "10m", "2d") — Go süre sözdizimi + "d"
// (gün) eki; her alan kelepçeli (settings_test.go), yanlış vida ne
// Thanos'u ne CH'yi boğar.

const SettingsKey = "entity_layer"

// Settings — kayıtlı blob.
type Settings struct {
	Enabled          bool   `json:"enabled"`
	SyncInterval     string `json:"syncInterval,omitempty"`
	PodGap           string `json:"podGap,omitempty"`
	StaleAfter       string `json:"staleAfter,omitempty"`
	ParallelClusters int    `json:"parallelClusters,omitempty"`
	// UpdatedAt — v0.10.129: yazım damgası (UnixNano). LoadPersisted daha
	// eski bir blobu bellekteki yeninin üstüne yazmaz — PUT'un kendi reload
	// sinyali replike olmamış eski satırı okuyup değeri geri alıyordu.
	UpdatedAt int64 `json:"updatedAt,omitempty"`
	// BackfillUntil — v0.10.141 (otomatik eşleme brief'i): bu ana (ms) kadar
	// span geçişi 24 saatlik pencereyle koşar — bir span cluster değeri bir
	// kayda ATANDIĞINDA geriye dönük pod/servis entity'leri üretilsin.
	// Rol-güvenli: blob üzerinden yayılır, lider Tick'i okur.
	BackfillUntil int64 `json:"backfillUntil,omitempty"`
	// BackfillValue — geriye dönük geçiş YALNIZ bu span cluster değeri için
	// (inceleme: küresel 24 s pencere her cluster'ın ölü pod'larını canlı
	// olarak yeniden açıyordu). Boş = backfill yok.
	BackfillValue string `json:"backfillValue,omitempty"`
}

// Resolved — kelepçelenmiş, çözülmüş vidalar.
type Resolved struct {
	Enabled          bool
	SyncInterval     time.Duration
	PodGap           time.Duration
	StaleAfter       time.Duration
	ParallelClusters int
	BackfillUntil    time.Time // sıfır = yok
	BackfillValue    string
}

func DefaultSettings() Settings {
	return Settings{Enabled: false, SyncInterval: "60s", PodGap: "10m", StaleAfter: "24h", ParallelClusters: 4}
}

// parseDur — Go süresi + "d" (gün). Bozuk/boş → def.
func parseDur(s string, def time.Duration) time.Duration {
	s = strings.TrimSpace(s)
	if s == "" {
		return def
	}
	if strings.HasSuffix(s, "d") {
		if n, err := strconv.ParseFloat(strings.TrimSuffix(s, "d"), 64); err == nil && n > 0 {
			return time.Duration(n * float64(24*time.Hour))
		}
		return def
	}
	d, err := time.ParseDuration(s)
	if err != nil || d <= 0 {
		return def
	}
	return d
}

func clampDur(d, lo, hi time.Duration) time.Duration {
	if d < lo {
		return lo
	}
	if d > hi {
		return hi
	}
	return d
}

// Resolved — kelepçeler: syncInterval 15s..1h, podGap 1m..24h, staleAfter
// 1h..30d, parallelClusters 1..16.
func (s Settings) Resolved() Resolved {
	d := DefaultSettings()
	r := Resolved{
		Enabled:          s.Enabled,
		SyncInterval:     clampDur(parseDur(s.SyncInterval, parseDur(d.SyncInterval, time.Minute)), 15*time.Second, time.Hour),
		PodGap:           clampDur(parseDur(s.PodGap, parseDur(d.PodGap, 10*time.Minute)), time.Minute, 24*time.Hour),
		StaleAfter:       clampDur(parseDur(s.StaleAfter, parseDur(d.StaleAfter, 24*time.Hour)), time.Hour, 30*24*time.Hour),
		ParallelClusters: s.ParallelClusters,
		BackfillUntil:    backfillUntil(s.BackfillUntil),
		BackfillValue:    strings.TrimSpace(s.BackfillValue),
	}
	if r.ParallelClusters == 0 {
		r.ParallelClusters = d.ParallelClusters // ayarlanmadı
	} else if r.ParallelClusters < 0 {
		r.ParallelClusters = 1
	}
	if r.ParallelClusters > 16 {
		r.ParallelClusters = 16
	}
	return r
}

// settingsStore — chstore'un ham blob kapısı (thanos.go emsali).
type settingsStore interface {
	GetEntitySettingsRaw(ctx context.Context) ([]byte, error)
	PutEntitySettingsRaw(ctx context.Context, raw []byte) error
}

// SettingsService — bellekteki ayar + kalıcılık.
type SettingsService struct {
	mu  sync.RWMutex
	cfg Settings
}

func NewSettingsService() *SettingsService { return &SettingsService{cfg: DefaultSettings()} }

func (s *SettingsService) Current() Settings {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.cfg
}

func (s *SettingsService) Resolved() Resolved { return s.Current().Resolved() }

func (s *SettingsService) Configure(cfg Settings) {
	s.mu.Lock()
	s.cfg = cfg
	s.mu.Unlock()
}

// LoadPersisted — boot: blob yoksa varsayılan (kapalı).
func (s *SettingsService) LoadPersisted(ctx context.Context, store settingsStore) error {
	if s == nil || store == nil {
		return nil
	}
	raw, err := store.GetEntitySettingsRaw(ctx)
	if err != nil {
		return err
	}
	if len(raw) == 0 {
		return nil
	}
	var cfg Settings
	if err := json.Unmarshal(raw, &cfg); err != nil {
		return fmt.Errorf("entity settings decode: %w", err)
	}
	s.applyLoaded(cfg)
	return nil
}

// applyLoaded — yüklenen blob bellektekinden ESKİ ise (damga küçük) atlar.
func (s *SettingsService) applyLoaded(cfg Settings) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if cfg.UpdatedAt < s.cfg.UpdatedAt {
		return false
	}
	s.cfg = cfg
	return true
}

// SavePersisted — admin PUT: tam blob + canlı swap.
func (s *SettingsService) SavePersisted(ctx context.Context, store settingsStore, cfg Settings) error {
	cfg.UpdatedAt = time.Now().UnixNano()
	raw, err := json.Marshal(cfg)
	if err != nil {
		return err
	}
	if err := store.PutEntitySettingsRaw(ctx, raw); err != nil {
		return err
	}
	s.Configure(cfg)
	return nil
}

// StartConfigRefresh — çok-pod blob eşitlemesi (30 s).
func (s *SettingsService) StartConfigRefresh(ctx context.Context, store settingsStore, interval time.Duration) {
	if s == nil || store == nil {
		return
	}
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			if err := s.LoadPersisted(ctx, store); err != nil {
				log.Printf("[entity] ayar tazeleme: %v", err)
			}
		}
	}
}

func backfillUntil(ms int64) time.Time {
	if ms <= 0 {
		return time.Time{}
	}
	return time.UnixMilli(ms)
}

// BackfillLookback — Tick'in span geçişi penceresi: atama sonrası kısa bir
// süre (BackfillUntil) 24 saat, sonra normal seenLookback.
const BackfillLookback = 24 * time.Hour
