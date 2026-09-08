package chstore

// ai_chat_retention.go — v0.10.561 (CoSRE Faz 5c). Sohbet arşivi
// saved_views(page='ai-chat') sınırsız birikiyordu (audit §6 risk). Süpürücü:
// son yazımı (version = yazım anı ns) cutoff'tan eski ai-chat satırlarını
// siler — tombstone (name='') satırları dahil. DDL YOK: ALTER … DELETE
// mutasyonu, state tablosu emsali (dashboards/monitors/alert_rules).
//
// Ayar system_settings `ai_chat_retention` JSON blobu (problem_priority
// şablonu): days 0 = süpürme KAPALI; varsayılan 90 (audit); tavan 3650.
// Worker'da leader kilidiyle saatte bir (StartRetentionEnforcer kalıbı);
// her koşu ÖNCE sayar, sonra siler ve sayıyı loglar (sessiz silme yok).

import (
	"context"
	"encoding/json"
	"log"
	"sync/atomic"
	"time"

	"github.com/cilcenk/coremetry/internal/cache"
)

const (
	aiChatRetentionKey     = "ai_chat_retention"
	AIChatRetentionDefault = 90
	AIChatRetentionMax     = 3650
	aiChatSweepLockKey     = "coremetry:lock:ai-chat-sweep"
	aiChatPage             = "ai-chat"
)

type AIChatRetentionConfig struct {
	// Days — son yazımdan bu kadar gün sonra sohbet silinir; 0 = kapalı.
	Days int `json:"days"`
}

func DefaultAIChatRetention() AIChatRetentionConfig {
	return AIChatRetentionConfig{Days: AIChatRetentionDefault}
}

// NormalizeAIChatRetention — negatif → 0 (kapalı), tavan 3650.
func NormalizeAIChatRetention(c AIChatRetentionConfig) AIChatRetentionConfig {
	if c.Days < 0 {
		c.Days = 0
	}
	if c.Days > AIChatRetentionMax {
		c.Days = AIChatRetentionMax
	}
	return c
}

var aiChatRetentionCfg atomic.Pointer[AIChatRetentionConfig]

func CurrentAIChatRetention() AIChatRetentionConfig {
	if c := aiChatRetentionCfg.Load(); c != nil {
		return *c
	}
	return DefaultAIChatRetention()
}

func SetAIChatRetention(c AIChatRetentionConfig) {
	n := NormalizeAIChatRetention(c)
	aiChatRetentionCfg.Store(&n)
}

func (s *Store) GetAIChatRetention(ctx context.Context) AIChatRetentionConfig {
	raw, err := s.GetSetting(ctx, aiChatRetentionKey)
	if err != nil || len(raw) == 0 {
		return DefaultAIChatRetention()
	}
	c := DefaultAIChatRetention()
	if err := json.Unmarshal(raw, &c); err != nil {
		return DefaultAIChatRetention()
	}
	return NormalizeAIChatRetention(c)
}

func (s *Store) SaveAIChatRetention(ctx context.Context, c AIChatRetentionConfig) error {
	raw, err := json.Marshal(NormalizeAIChatRetention(c))
	if err != nil {
		return err
	}
	return s.PutSetting(ctx, aiChatRetentionKey, raw)
}

// AIChatSweepCutoff — SAF: days ≤ 0 → (zero, false) = süpürme yok.
func AIChatSweepCutoff(now time.Time, days int) (time.Time, bool) {
	if days <= 0 {
		return time.Time{}, false
	}
	return now.Add(-time.Duration(days) * 24 * time.Hour), true
}

const aiChatSweepCountSQL = `
		SELECT count()
		FROM saved_views FINAL
		WHERE page = ? AND version < ?
		SETTINGS max_execution_time = 10`

const aiChatSweepDeleteSQL = `ALTER TABLE saved_views DELETE WHERE page = ? AND version < ?`

// SweepAIChatConversations — cutoff'tan eski ai-chat satırlarını sayar ve
// siler; silinen (sayılan) satır sayısını döner. 0 ise mutasyon GÖNDERİLMEZ.
func (s *Store) SweepAIChatConversations(ctx context.Context, cutoff time.Time) (uint64, error) {
	cutNs := uint64(cutoff.UnixNano())
	var n uint64
	if err := s.conn.QueryRow(ctx, aiChatSweepCountSQL, aiChatPage, cutNs).Scan(&n); err != nil {
		return 0, err
	}
	if n == 0 {
		return 0, nil
	}
	if err := s.conn.Exec(ctx, aiChatSweepDeleteSQL, aiChatPage, cutNs); err != nil {
		return 0, err
	}
	return n, nil
}

// StartAIChatSweeper — worker'da saatte bir, leader kilidiyle (retention
// enforcer kalıbı). days her tikte ayardan okunur (admin PUT anında etkili).
func (s *Store) StartAIChatSweeper(ctx context.Context, interval time.Duration, lock cache.Lock) {
	if interval <= 0 {
		interval = time.Hour
	}
	leader := cache.NewLeaderHolder(lock, aiChatSweepLockKey, cache.LeaderTTL(interval))
	leader.Start(ctx)
	runOnce := func() {
		if !leader.IsLeader() {
			return
		}
		cfg := CurrentAIChatRetention()
		cutoff, ok := AIChatSweepCutoff(time.Now(), cfg.Days)
		if !ok {
			return
		}
		n, err := s.SweepAIChatConversations(ctx, cutoff)
		switch {
		case err != nil:
			log.Printf("[ai-chat-sweep] %v", err)
		case n > 0:
			log.Printf("[ai-chat-sweep] %d sohbet satırı silindi (son yazım < %s, %d gün)", n, cutoff.UTC().Format(time.RFC3339), cfg.Days)
		}
	}
	runOnce()
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			runOnce()
		}
	}
}
