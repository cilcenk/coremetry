package mcpclient

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"sync"
	"time"
)

// settings.go — dilim ② (v0.10.87): sunucu listesinin kalıcılığı.
//
// Şablon birebir devops istemcisi: system_settings'te TEK JSON blob
// (invariant 5 — yüzey başına yeni şema yok), dar settingsStore arayüzü
// (chstore'a import bağı yok), LoadPersisted/SavePersisted/Configure +
// 30s StartConfigRefresh (çok-pod yakınsaması; publishConfigReload
// sinyali daha hızlı yolu, poll emniyet ağı).
//
// Sır sözleşmesi: token Snapshot'ta ASLA yer almaz (hasToken sinyali),
// boş/sentinel girdi saklıyı korur — birleştirme api katmanının saf
// mergeMCPServers'ında.

// Settings — kalıcı biçim: sunucu listesi.
type Settings struct {
	Servers []ServerConfig `json:"servers,omitempty"`
}

// ServerSnapshot — tek sunucunun sır İÇERMEYEN görünümü.
type ServerSnapshot struct {
	Name               string   `json:"name"`
	Transport          string   `json:"transport"`
	URL                string   `json:"url,omitempty"`
	Command            string   `json:"command,omitempty"`
	Args               []string `json:"args,omitempty"`
	Enabled            bool     `json:"enabled"`
	HasToken           bool     `json:"hasToken"`
	AllowTools         []string `json:"allowTools,omitempty"`
	DenyTools          []string `json:"denyTools,omitempty"`
	InsecureSkipVerify bool     `json:"insecureSkipVerify,omitempty"`
}

// Snapshot — GET /api/settings/mcp-servers gövdesi: ayar + canlı sağlık.
type Snapshot struct {
	Servers []ServerSnapshot `json:"servers"`
	// Status — Registry'nin sunucu başına görünümü (katalog boyu, son
	// hata). Ayar değil, o ANIN gözlemi; kalıcı değildir.
	Status []EntryStatus `json:"status,omitempty"`
}

// TestResult — POST .../test cevabı. Başarısız bağlantı {ok:false} ile
// 200 döner: başarısız prova, operatörün sorusuna BAŞARILI bir cevaptır
// (devops test ucunun duruşu).
type TestResult struct {
	OK        bool   `json:"ok"`
	Tools     int    `json:"tools"`
	Truncated bool   `json:"truncated,omitempty"`
	Error     string `json:"error,omitempty"`
}

// Service — canlı yapılandırma + Registry sahibi.
type Service struct {
	mu  sync.RWMutex
	cfg Settings
	reg *Registry
}

func NewService() *Service { return &Service{reg: NewRegistry(nil)} }

// Registry — sohbet köprüsünün (dilim ③) okuyacağı canlı katalog.
func (s *Service) Registry() *Registry {
	if s == nil {
		return nil
	}
	return s.reg
}

// Configure — canlı yapılandırmayı değiştirir; Registry taşımalarını
// yalnız GERÇEKTEN değişen sunucular için yeniden kurar (configEqual).
func (s *Service) Configure(cfg Settings) {
	if s == nil {
		return
	}
	s.mu.Lock()
	s.cfg = cfg
	s.mu.Unlock()
	s.reg.Configure(cfg.Servers)
}

// CurrentSettings — kayıtlı hâlin kopyası (token DAHİL — merge bunun
// üstünde çalışır; dışarı yalnız Snapshot çıkar).
func (s *Service) CurrentSettings() Settings {
	if s == nil {
		return Settings{}
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := Settings{Servers: make([]ServerConfig, len(s.cfg.Servers))}
	copy(out.Servers, s.cfg.Servers)
	return out
}

// ToolRules — sunucunun (sanitize edilmiş adıyla) allow/deny listeleri.
// Köprü (dilim ③) her katalog kurulumunda çağırır; Registry'yle aynı
// kimlik (SanitizedName) — iki yazım aynı sunucuya iki kimlik olurdu.
func (s *Service) ToolRules(server string) (allow, deny []string) {
	if s == nil {
		return nil, nil
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, sv := range s.cfg.Servers {
		if SanitizedName(sv.Name) == server {
			return sv.AllowTools, sv.DenyTools
		}
	}
	return nil, nil
}

// Configured — en az bir ETKİN sunucu var mı (dilim ③'ün kapısı).
func (s *Service) Configured() bool {
	if s == nil {
		return false
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, sv := range s.cfg.Servers {
		if sv.Enabled {
			return true
		}
	}
	return false
}

// Snapshot — sır içermeyen görünüm + canlı sağlık.
func (s *Service) Snapshot() Snapshot {
	if s == nil {
		return Snapshot{Servers: []ServerSnapshot{}}
	}
	s.mu.RLock()
	servers := make([]ServerSnapshot, 0, len(s.cfg.Servers))
	for _, sv := range s.cfg.Servers {
		servers = append(servers, ServerSnapshot{
			Name: sv.Name, Transport: sv.Transport, URL: sv.URL,
			Command: sv.Command, Args: sv.Args, Enabled: sv.Enabled,
			HasToken: sv.Token != "", AllowTools: sv.AllowTools,
			DenyTools: sv.DenyTools, InsecureSkipVerify: sv.InsecureSkipVerify,
		})
	}
	s.mu.RUnlock()
	return Snapshot{Servers: servers, Status: s.reg.Status()}
}

// settingsStore — chstore'un bu paket için dar yüzü (devops şablonu).
type settingsStore interface {
	GetMCPClientSettingsRaw(ctx context.Context) ([]byte, error)
	PutMCPClientSettingsRaw(ctx context.Context, raw []byte) error
}

// LoadPersisted — system_settings'ten okur. Blob yoksa boş yapılandırma.
func (s *Service) LoadPersisted(ctx context.Context, store settingsStore) error {
	if s == nil || store == nil {
		return nil
	}
	raw, err := store.GetMCPClientSettingsRaw(ctx)
	if err != nil {
		return err
	}
	if len(raw) == 0 {
		return nil
	}
	var cfg Settings
	if err := json.Unmarshal(raw, &cfg); err != nil {
		return fmt.Errorf("mcpclient decode: %w", err)
	}
	s.Configure(cfg)
	return nil
}

// SavePersisted — yazar ve canlıyı değiştirir. Token birleştirmesi
// ÇAĞIRANDA (api.mergeMCPServers) — bu katman sır politikası bilmez.
func (s *Service) SavePersisted(ctx context.Context, store settingsStore, cfg Settings) error {
	if s == nil || store == nil {
		return nil
	}
	raw, err := json.Marshal(cfg)
	if err != nil {
		return err
	}
	if err := store.PutMCPClientSettingsRaw(ctx, raw); err != nil {
		return err
	}
	s.Configure(cfg)
	return nil
}

// StartConfigRefresh — 30s poll; peer pod yakınsamasının emniyet ağı
// (publishConfigReload hızlı yol). devops şablonu.
func (s *Service) StartConfigRefresh(ctx context.Context, store settingsStore, interval time.Duration) {
	if s == nil || store == nil {
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
			if err := s.LoadPersisted(ctx, store); err != nil {
				log.Printf("[mcpclient] config refresh: %v", err)
			}
		}
	}
}

// Test — TEK sunucu yapılandırmasını KAYDETMEDEN prova eder: bağlan,
// el sıkış, katalogu say, kapat. stdio'da bu bir alt süreç başlatıp
// indirmek demektir — prova da gerçek yolun kendisi.
func (s *Service) Test(ctx context.Context, cfg ServerConfig) TestResult {
	ctx, cancel := context.WithTimeout(ctx, callTimeout)
	defer cancel()
	tr, err := DialTransport(cfg)
	if err != nil {
		return TestResult{Error: err.Error()}
	}
	defer tr.Close()
	cl := NewClient(tr)
	tools, trunc, err := cl.ListTools(ctx)
	if err != nil {
		return TestResult{Error: err.Error()}
	}
	return TestResult{OK: true, Tools: len(tools), Truncated: trunc}
}

// Close — Registry taşımalarını indirir (test/kapanış temizliği).
func (s *Service) Close() {
	if s == nil {
		return
	}
	s.reg.Close()
}
