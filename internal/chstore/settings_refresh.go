package chstore

// settings_refresh.go — v0.10.259 (perf profili §7 madde 3, P-set): ayar
// yenileyici tekilleştirme.
//
// Ölçüldü (lokal, 5 dk): 14 yenileyici × 30 s → system_settings üzerinde
// 224 FINAL okuma / 44.8 dk⁻¹, toplam 221.8 s CH süresi (~1 s/sorgu).
// Her servis kendi anahtarını ayrı bir `SELECT value … FINAL WHERE key=?`
// ile okuyordu; tablo tek ve küçük (≤ birkaç yüz satır).
//
// Tasarım: tek tik → tek sorgu (SettingsSnapshot: SELECT key, value FROM
// system_settings FINAL) → anlık görüntü ctx'e bağlanır → her servisin
// MEVCUT LoadPersisted(ctx, store) çağrısı aynen çalışır; Store.GetSetting
// ctx'te anlık görüntü varsa oradan cevaplar (CH'ye gitmez). Servis
// paketlerinin arayüzleri/kodları DEĞİŞMEZ (13 paket, Get<X>SettingsRaw →
// GetSetting). Anlık görüntü sorgusu düşerse yükleyiciler eski davranışla
// (kendi tekil okumalarıyla) koşar — bozulma yok, yalnız tasarruf kaybı.
// Yükleyici hatası diğerlerini durdurmaz (log). Boot'taki ilk LoadPersisted
// çağrıları main.go'da aynen kalır (yenileyici yalnız periyodik tazeleme).

import (
	"context"
	"log"
	"time"
)

type settingsSnapshotKey struct{}

// SettingsSnapshot — system_settings'in tek okumalık kopyası.
type SettingsSnapshot struct {
	values map[string][]byte
	taken  time.Time
}

// NewSettingsSnapshot — test/kompozisyon için (map kopyalanır).
func NewSettingsSnapshot(values map[string][]byte) *SettingsSnapshot {
	m := make(map[string][]byte, len(values))
	for k, v := range values {
		m[k] = v
	}
	return &SettingsSnapshot{values: m, taken: time.Now()}
}

func (s *SettingsSnapshot) get(key string) ([]byte, bool) {
	if s == nil {
		return nil, false
	}
	v, ok := s.values[key]
	return v, ok
}

// Len — anlık görüntüdeki anahtar sayısı.
func (s *SettingsSnapshot) Len() int {
	if s == nil {
		return 0
	}
	return len(s.values)
}

// WithSettingsSnapshot — ctx'e anlık görüntü; GetSetting önce buna bakar.
func WithSettingsSnapshot(ctx context.Context, snap *SettingsSnapshot) context.Context {
	if snap == nil {
		return ctx
	}
	return context.WithValue(ctx, settingsSnapshotKey{}, snap)
}

func settingsSnapshotFrom(ctx context.Context) *SettingsSnapshot {
	s, _ := ctx.Value(settingsSnapshotKey{}).(*SettingsSnapshot)
	return s
}

// SettingsSnapshot — tek FINAL okuma; state tablosu (in-order ana bağlantı).
func (s *Store) SettingsSnapshot(ctx context.Context) (*SettingsSnapshot, error) {
	rows, err := s.conn.Query(ctx, `
		SELECT key, value FROM system_settings FINAL
		ORDER BY key LIMIT 10000 SETTINGS max_execution_time = 10`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	m := map[string][]byte{}
	for rows.Next() {
		var k, v string
		if err := rows.Scan(&k, &v); err != nil {
			return nil, err
		}
		m[k] = []byte(v)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return &SettingsSnapshot{values: m, taken: time.Now()}, nil
}

// SettingsLoader — bir servisin periyodik tazelemesi (LoadPersisted sarmalı).
type SettingsLoader struct {
	Name string
	Load func(ctx context.Context) error
}

// SettingsRefresher — tek tik, tek okuma, N yükleyici.
type SettingsRefresher struct {
	store    *Store
	interval time.Duration
	loaders  []SettingsLoader
}

func NewSettingsRefresher(store *Store, interval time.Duration) *SettingsRefresher {
	if interval <= 0 {
		interval = 30 * time.Second
	}
	return &SettingsRefresher{store: store, interval: interval}
}

// Add — Start'tan ÖNCE çağrılır (kayıt sırası = koşma sırası).
func (r *SettingsRefresher) Add(name string, load func(ctx context.Context) error) {
	if r == nil || load == nil {
		return
	}
	r.loaders = append(r.loaders, SettingsLoader{Name: name, Load: load})
}

// Start — ctx bitene dek her interval'de RunOnce.
func (r *SettingsRefresher) Start(ctx context.Context) {
	if r == nil || len(r.loaders) == 0 {
		return
	}
	t := time.NewTicker(r.interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			r.RunOnce(ctx)
		}
	}
}

// RunOnce — bir tik: anlık görüntü (bir sorgu) + yükleyiciler. Anlık görüntü
// alınamazsa yükleyiciler düz ctx ile koşar (her biri kendi okumasını yapar).
func (r *SettingsRefresher) RunOnce(ctx context.Context) {
	lctx := ctx
	if r.store != nil {
		if snap, err := r.store.SettingsSnapshot(ctx); err != nil {
			log.Printf("[settings-refresh] anlık görüntü: %v — yükleyiciler tekil okumayla koşuyor", err)
		} else {
			lctx = WithSettingsSnapshot(ctx, snap)
		}
	}
	runSettingsLoaders(lctx, r.loaders)
}

// runSettingsLoaders — SAF (testli): sırayla, hata izole.
func runSettingsLoaders(ctx context.Context, loaders []SettingsLoader) (failed int) {
	for _, l := range loaders {
		if err := l.Load(ctx); err != nil {
			failed++
			log.Printf("[%s] config refresh: %v", l.Name, err)
		}
	}
	return failed
}
