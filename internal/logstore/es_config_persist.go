package logstore

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log"
	"sync"
	"time"
)

// v0.8.232 — UI-managed logstore config (operator-requested: "I want
// to configure Elastic from the UI so I can SEE the error when it's
// wrong"). Mirrors the tempo.Service LoadPersisted / SavePersisted /
// Configure template: env/YAML seeds the boot config, a persisted
// system_settings blob overlays it (UI wins), and an admin PUT applies
// the new backend live via Switchable.Swap — no pod restart.

// ESSettings is the full persisted shape, secrets included. Never
// serialize this to a client — Snapshot() is the wire view.
type ESSettings struct {
	// Backend selects the live read backend: "clickhouse" (default) or
	// "elasticsearch". The UI toggle maps here.
	Backend            string     `json:"backend"`
	Addresses          []string   `json:"addresses,omitempty"`
	Username           string     `json:"username,omitempty"`
	Password           string     `json:"password,omitempty"`
	APIKey             string     `json:"apiKey,omitempty"`
	InsecureSkipVerify bool       `json:"insecureSkipVerify,omitempty"`
	Index              string     `json:"index,omitempty"`
	IndexTemplate      string     `json:"indexTemplate,omitempty"`
	Fields             ESFieldMap `json:"fields"`
}

// ESSettingsSnapshot is the secret-free GET view. HasPassword /
// HasAPIKey drive the "stored" indicator; empty input on PUT preserves
// the stored value (rotate by pasting a new one).
type ESSettingsSnapshot struct {
	Backend            string     `json:"backend"`
	Addresses          []string   `json:"addresses"`
	Username           string     `json:"username"`
	HasPassword        bool       `json:"hasPassword"`
	HasAPIKey          bool       `json:"hasApiKey"`
	InsecureSkipVerify bool       `json:"insecureSkipVerify"`
	Index              string     `json:"index"`
	IndexTemplate      string     `json:"indexTemplate"`
	Fields             ESFieldMap `json:"fields"`
	// Source is "env" until the first UI save is loaded, then "ui" —
	// tells the operator whether the form reflects env/YAML bootstrap
	// or a persisted override.
	Source string `json:"source"`
}

// ESSettingsStore is the narrow chstore surface the manager persists
// through (prevents an import cycle; matches tempo.settingsStore).
type ESSettingsStore interface {
	GetLogstoreESSettingsRaw(ctx context.Context) ([]byte, error)
	PutLogstoreESSettingsRaw(ctx context.Context, raw []byte) error
}

// ESManager owns the UI-managed logstore config: current effective
// settings, persistence, and applying changes to the shared
// Switchable. One instance per process, wired in main().
type ESManager struct {
	sw *Switchable
	// chFallback is the always-available ClickHouse-backed store the
	// Switchable swaps to when Backend != "elasticsearch".
	chFallback Store
	// resolver feeds ESStore.NamespaceResolver on every rebuilt ES
	// store (the {namespace} index-template placeholder, v0.8.231).
	resolver func(ctx context.Context, service string) string

	mu      sync.RWMutex
	cfg     ESSettings
	source  string // "env" | "ui"
	lastRaw []byte // last applied persisted blob; skip re-apply when unchanged
}

// NewESManager seeds the manager with the env/YAML-derived effective
// settings so the Settings tab shows real config before any UI save,
// and empty-secret-preserves works against env credentials too.
func NewESManager(sw *Switchable, chFallback Store, resolver func(ctx context.Context, service string) string, boot ESSettings) *ESManager {
	if boot.Backend == "" {
		boot.Backend = "clickhouse"
	}
	return &ESManager{sw: sw, chFallback: chFallback, resolver: resolver, cfg: boot, source: "env"}
}

// Snapshot returns the secret-free config view for the GET handler.
func (m *ESManager) Snapshot() ESSettingsSnapshot {
	m.mu.RLock()
	defer m.mu.RUnlock()
	addrs := m.cfg.Addresses
	if addrs == nil {
		addrs = []string{}
	}
	return ESSettingsSnapshot{
		Backend:            m.cfg.Backend,
		Addresses:          addrs,
		Username:           m.cfg.Username,
		HasPassword:        m.cfg.Password != "",
		HasAPIKey:          m.cfg.APIKey != "",
		InsecureSkipVerify: m.cfg.InsecureSkipVerify,
		Index:              m.cfg.Index,
		IndexTemplate:      m.cfg.IndexTemplate,
		Fields:             m.cfg.Fields,
		Source:             m.source,
	}
}

// CurrentSettings returns the full config including secrets — only for
// the PUT handler's empty-secret merge (tempo.CurrentSettings
// contract). Never echo the return value over the wire.
func (m *ESManager) CurrentSettings() ESSettings {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.cfg
}

// build constructs (and pings — NewES connects eagerly) the store a
// config describes, without swapping it in. Shared by apply and Test.
func (m *ESManager) build(cfg ESSettings) (Store, error) {
	switch cfg.Backend {
	case "", "clickhouse":
		return m.chFallback, nil
	case "elasticsearch":
		if len(cfg.Addresses) == 0 {
			return nil, fmt.Errorf("elasticsearch backend needs at least one address")
		}
		es, err := NewES(ESConfig{
			Addresses:          cfg.Addresses,
			Username:           cfg.Username,
			Password:           cfg.Password,
			APIKey:             cfg.APIKey,
			InsecureSkipVerify: cfg.InsecureSkipVerify,
			Index:              cfg.Index,
			IndexTemplate:      cfg.IndexTemplate,
			Fields:             cfg.Fields,
		})
		if err != nil {
			return nil, err
		}
		es.NamespaceResolver = m.resolver
		return es, nil
	default:
		return nil, fmt.Errorf("unknown logs backend %q (want clickhouse|elasticsearch)", cfg.Backend)
	}
}

// Test builds a candidate store from cfg and reports the connection
// error without touching the live backend. This is the Settings tab's
// "Test" button — the exact place a bad address / credential / index
// pattern surfaces to the operator instead of a silent empty /logs.
func (m *ESManager) Test(ctx context.Context, cfg ESSettings) error {
	st, err := m.build(cfg)
	if err != nil {
		return err
	}
	return st.Ping(ctx)
}

// apply swaps the live backend to what cfg describes. On error the
// previous store keeps serving — a bad save must never take /logs down.
func (m *ESManager) apply(cfg ESSettings) error {
	st, err := m.build(cfg)
	if err != nil {
		return err
	}
	m.sw.Swap(st)
	return nil
}

// SavePersisted validates + applies cfg to the live Switchable, then
// persists it. Apply-first: a config that can't connect is rejected
// with the real error (the operator asked to SEE failures) and nothing
// is written — the stored blob always describes a config that worked
// at save time.
func (m *ESManager) SavePersisted(ctx context.Context, store ESSettingsStore, cfg ESSettings) error {
	if m == nil || store == nil {
		return nil
	}
	if err := m.apply(cfg); err != nil {
		return err
	}
	raw, err := json.Marshal(cfg)
	if err != nil {
		return err
	}
	if err := store.PutLogstoreESSettingsRaw(ctx, raw); err != nil {
		return err
	}
	m.mu.Lock()
	m.cfg, m.source, m.lastRaw = cfg, "ui", raw
	m.mu.Unlock()
	return nil
}

// LoadPersisted hydrates from the saved blob: at boot (overlaying the
// env seed — UI wins), on the peer-pod config-reload signal, and on
// the 30s StartConfigRefresh tick. No blob = env config stays. An
// unchanged blob is a no-op — rebuilding the ES client (+ its eager
// ping) every 30s would churn connections for nothing.
func (m *ESManager) LoadPersisted(ctx context.Context, store ESSettingsStore) error {
	if m == nil || store == nil {
		return nil
	}
	raw, err := store.GetLogstoreESSettingsRaw(ctx)
	if err != nil {
		return err
	}
	if len(raw) == 0 {
		return nil
	}
	m.mu.RLock()
	unchanged := bytes.Equal(raw, m.lastRaw)
	m.mu.RUnlock()
	if unchanged {
		return nil
	}
	var cfg ESSettings
	if err := json.Unmarshal(raw, &cfg); err != nil {
		return fmt.Errorf("logstore settings decode: %w", err)
	}
	if err := m.apply(cfg); err != nil {
		return err
	}
	m.mu.Lock()
	m.cfg, m.source, m.lastRaw = cfg, "ui", raw
	m.mu.Unlock()
	return nil
}

// StartConfigRefresh keeps multi-pod deployments converged on the
// shared persisted blob (worker/ingest pods have no PUT handler or
// reload signal — they pick up an api-pod save within `interval`).
// Mirrors tempo.StartConfigRefresh; interval ≤ 0 → 30s.
func (m *ESManager) StartConfigRefresh(ctx context.Context, store ESSettingsStore, interval time.Duration) {
	if m == nil || store == nil {
		return
	}
	if interval <= 0 {
		interval = 30 * time.Second
	}
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			if err := m.LoadPersisted(ctx, store); err != nil {
				log.Printf("[logstore-es] config refresh: %v", err)
			}
		}
	}
}
