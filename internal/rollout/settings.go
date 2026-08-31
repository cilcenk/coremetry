package rollout

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"math"
	"strings"
	"sync"
	"time"

	"github.com/cilcenk/coremetry/internal/settingsdur"
)

// settings.go — ROLLOUTS özellik bayrağı + vidalar (system_settings["rollouts"]).
// v0.10.199 (Faz 2). Şablon internal/entity/settings.go: tek JSON blob, boot
// LoadPersisted, çok-pod 30 s poll, admin PUT → SavePersisted + canlı swap
// (uçlar v0.10.200). Varsayılan KAPALI: reconciler koşmaz; uçlar 404.
// Eşik gerekçeleri docs/audits/rollouts-audit.md §11; kelepçeler BAĞLI
// (inceleme: histerezis × kova ≥ 10 dk, kova saat böleni — CH ızgarası).

const SettingsKey = "rollouts"

type Settings struct {
	Enabled    bool   `json:"enabled"`
	Interval   string `json:"interval,omitempty"`   // reconciler tiki (60s)
	Bucket     string `json:"bucket,omitempty"`     // karar kovası (5m; düşük trafikte 10m) — 1m/5m/10m/15m/30m
	Threshold  int64  `json:"threshold,omitempty"`  // kovada aktif sayılmak için span (10)
	Hysteresis int    `json:"hysteresis,omitempty"` // GİRİŞ: ardışık kova (2)
	// ExitHysteresis — ÇIKIŞ: "çekildi" için ardışık inaktif kova (6 = 30 dk):
	// gece dalması / pod restart olay değildir (inceleme 2. tur).
	ExitHysteresis int    `json:"exitHysteresis,omitempty"`
	OverlapMax     string `json:"overlapMax,omitempty"` // çok-revizyonlu notu eşiği (30m)
	Lookback       string `json:"lookback,omitempty"`   // etkinlik penceresi (6h)
	// WeakSignal — "yeni revizyon son kovada eşik altında kaldı" notu (audit §11 vidası; nil = açık).
	WeakSignal *bool `json:"weakSignal,omitempty"`
	// StalledMin — Faz 5 (KSM): ready < desired bu süreden uzun → stalled (10m). Bugün yalnız saklanır/kelepçelenir.
	StalledMin string `json:"stalledMin,omitempty"`
	UpdatedAt  int64  `json:"updatedAt,omitempty"`
}

type Resolved struct {
	Enabled        bool
	Interval       time.Duration
	Bucket         time.Duration
	Threshold      int64
	Hysteresis     int
	ExitHysteresis int
	OverlapMax     time.Duration
	Lookback       time.Duration
	WeakSignal     bool
	StalledMin     time.Duration
}

func DefaultSettings() Settings {
	return Settings{Enabled: false, Interval: "60s", Bucket: "5m", Threshold: 10, Hysteresis: 2, ExitHysteresis: 6, OverlapMax: "30m", Lookback: "6h", StalledMin: "10m"}
}

// bucketAllowed — saat bölenleri: Go AlignBucket ile CH toStartOfInterval aynı
// ızgarada kalsın (7 dk gibi bir kova iki tarafta farklı hizalanır).
var bucketAllowed = []time.Duration{time.Minute, 5 * time.Minute, 10 * time.Minute, 15 * time.Minute, 30 * time.Minute}

// snapBucket — izinli listede en büyük ≤ d (d < 1m → 1m).
func snapBucket(d time.Duration) time.Duration {
	best := bucketAllowed[0]
	for _, a := range bucketAllowed {
		if a <= d {
			best = a
		}
	}
	return best
}

// Resolved — kelepçeler: interval 30s..15m, bucket {1,5,10,15,30}m, threshold
// 1..1e6, hysteresis 2..12 VE hysteresis×bucket ≥ 10 dk (1 dk kovada 2 kova =
// 2 dk'lık karar çok kırılgan), exitHysteresis hysteresis..36 VE ×bucket ≥ 30 dk,
// overlapMax 5m..6h ve ≤ lookback/2, lookback 1h..48h ve ≥ 4×exitHysteresis×bucket
// ve lookback/bucket ≤ 576 kova, stalledMin 2m..2h.
func (s Settings) Resolved() Resolved {
	d := DefaultSettings()
	r := Resolved{
		Enabled:        s.Enabled,
		Interval:       settingsdur.Clamp(settingsdur.Parse(s.Interval, settingsdur.Parse(d.Interval, time.Minute)), 30*time.Second, 15*time.Minute),
		Bucket:         snapBucket(settingsdur.Parse(s.Bucket, settingsdur.Parse(d.Bucket, 5*time.Minute))),
		Threshold:      s.Threshold,
		Hysteresis:     s.Hysteresis,
		ExitHysteresis: s.ExitHysteresis,
		OverlapMax:     settingsdur.Clamp(settingsdur.Parse(s.OverlapMax, settingsdur.Parse(d.OverlapMax, 30*time.Minute)), 5*time.Minute, 6*time.Hour),
		Lookback:       settingsdur.Clamp(settingsdur.Parse(s.Lookback, settingsdur.Parse(d.Lookback, 6*time.Hour)), time.Hour, 48*time.Hour),
		WeakSignal:     s.WeakSignal == nil || *s.WeakSignal,
		StalledMin:     settingsdur.Clamp(settingsdur.Parse(s.StalledMin, settingsdur.Parse(d.StalledMin, 10*time.Minute)), 2*time.Minute, 2*time.Hour),
	}
	if r.Threshold <= 0 {
		r.Threshold = d.Threshold
	}
	if r.Threshold > 1_000_000 {
		r.Threshold = 1_000_000
	}
	if r.Hysteresis < 2 {
		r.Hysteresis = d.Hysteresis
	}
	if r.Hysteresis > 12 {
		r.Hysteresis = 12
	}
	if minH := int(math.Ceil(float64(10*time.Minute) / float64(r.Bucket))); r.Hysteresis < minH {
		r.Hysteresis = minH
	}
	if r.ExitHysteresis <= 0 {
		r.ExitHysteresis = d.ExitHysteresis
	}
	if r.ExitHysteresis < r.Hysteresis {
		r.ExitHysteresis = r.Hysteresis
	}
	if r.ExitHysteresis > 36 {
		r.ExitHysteresis = 36
	}
	if minEH := int(math.Ceil(float64(30*time.Minute) / float64(r.Bucket))); r.ExitHysteresis < minEH {
		r.ExitHysteresis = minEH
	}
	// lookback ≥ 4·EH·B ama tavan 48 sa: tavana sığmıyorsa EH düşer (pencere büyümez)
	if maxEH := int(48 * time.Hour / (4 * r.Bucket)); r.ExitHysteresis > maxEH {
		r.ExitHysteresis = maxEH
	}
	if minLB := 4 * time.Duration(r.ExitHysteresis) * r.Bucket; r.Lookback < minLB {
		r.Lookback = minLB
	}
	// seri başına kova sayısı ≤ 576 (48 sa / 5 dk): 1 dk kovayla 48 sa = 2880
	// kova × iş yükü × revizyon etkinlik tavanını (500k) geçerli ayarla aşardı
	if n := int(r.Lookback / r.Bucket); n > 576 {
		r.Lookback = 576 * r.Bucket
	}
	if r.OverlapMax >= r.Lookback/2 {
		r.OverlapMax = r.Lookback / 2
	}
	return r
}

// Config — saf çekirdeğin girdisi (reconcile.go).
func (r Resolved) Config() Config {
	return Config{Bucket: r.Bucket, Threshold: r.Threshold, Hysteresis: r.Hysteresis, ExitHysteresis: r.ExitHysteresis, OverlapMax: r.OverlapMax, WeakSignal: r.WeakSignal, StalledMin: r.StalledMin}
}

// ValidateSettings — PUT kapısı: anlaşılmaz girdi 400 olsun (kelepçe yine
// okumada — Resolved; operatör girdiğini geri görür, uygulananı resolved'da).
func ValidateSettings(s Settings) error {
	for name, v := range map[string]string{"interval": s.Interval, "bucket": s.Bucket, "overlapMax": s.OverlapMax, "lookback": s.Lookback, "stalledMin": s.StalledMin} {
		if strings.TrimSpace(v) == "" {
			continue
		}
		if settingsdur.Parse(v, 0) == 0 {
			return fmt.Errorf("%s anlaşılamadı: %q (örn. \"30s\", \"5m\", \"6h\", \"2d\")", name, v)
		}
	}
	if s.Threshold < 0 {
		return fmt.Errorf("threshold negatif olamaz: %d", s.Threshold)
	}
	if s.Hysteresis < 0 || s.ExitHysteresis < 0 {
		return fmt.Errorf("histerezis negatif olamaz")
	}
	return nil
}

type settingsStore interface {
	GetRolloutSettingsRaw(ctx context.Context) ([]byte, error)
	PutRolloutSettingsRaw(ctx context.Context, raw []byte) error
}

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

// applyLoaded — SAF: kalıcı blob canlı ayardan YENİ ya da eşitse alınır
// (eski pod'un bayat blobu daha yeni admin PUT'unu ezmesin).
func applyLoaded(cur, loaded Settings) Settings {
	if loaded.UpdatedAt >= cur.UpdatedAt {
		return loaded
	}
	return cur
}

func (s *SettingsService) LoadPersisted(ctx context.Context, store settingsStore) error {
	if s == nil || store == nil {
		return nil
	}
	raw, err := store.GetRolloutSettingsRaw(ctx)
	if err != nil {
		return err
	}
	if len(raw) == 0 {
		return nil
	}
	var cfg Settings
	if err := json.Unmarshal(raw, &cfg); err != nil {
		return fmt.Errorf("rollout settings decode: %w", err)
	}
	s.mu.Lock()
	s.cfg = applyLoaded(s.cfg, cfg)
	s.mu.Unlock()
	return nil
}

func (s *SettingsService) SavePersisted(ctx context.Context, store settingsStore, cfg Settings) error {
	cfg.UpdatedAt = time.Now().UnixNano()
	raw, err := json.Marshal(cfg)
	if err != nil {
		return err
	}
	if err := store.PutRolloutSettingsRaw(ctx, raw); err != nil {
		return err
	}
	s.Configure(cfg)
	return nil
}

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
				log.Printf("[rollout] ayar tazeleme: %v", err)
			}
		}
	}
}
