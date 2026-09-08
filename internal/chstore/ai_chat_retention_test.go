package chstore

import (
	"strings"
	"testing"
	"time"
)

// v0.10.561 — sohbet arşivi süpürücüsü: normalize (0 = kapalı, tavan), cutoff,
// SQL sınırları (page + version yüklemi, FINAL sayım, mutasyon).
func TestAIChatRetentionNormalizeAndCutoff(t *testing.T) {
	if NormalizeAIChatRetention(AIChatRetentionConfig{Days: -5}).Days != 0 {
		t.Fatal("negatif → 0 (kapalı)")
	}
	if NormalizeAIChatRetention(AIChatRetentionConfig{Days: 99999}).Days != AIChatRetentionMax {
		t.Fatal("tavan")
	}
	if DefaultAIChatRetention().Days != 90 {
		t.Fatal("varsayılan 90 (audit)")
	}
	now := time.Date(2026, 9, 8, 12, 0, 0, 0, time.UTC)
	if _, ok := AIChatSweepCutoff(now, 0); ok {
		t.Fatal("0 gün → süpürme yok")
	}
	c, ok := AIChatSweepCutoff(now, 90)
	if !ok || !c.Equal(now.Add(-90*24*time.Hour)) {
		t.Fatalf("cutoff: %v %v", c, ok)
	}
	SetAIChatRetention(AIChatRetentionConfig{Days: -1})
	if CurrentAIChatRetention().Days != 0 {
		t.Fatal("Set normalize eder")
	}
	SetAIChatRetention(DefaultAIChatRetention())
}

func TestAIChatSweepSQLBounds(t *testing.T) {
	for _, want := range []string{"FROM saved_views FINAL", "page = ? AND version < ?", "max_execution_time"} {
		if !strings.Contains(aiChatSweepCountSQL, want) {
			t.Errorf("sayım SQL %q taşımalı", want)
		}
	}
	if !strings.Contains(aiChatSweepDeleteSQL, "ALTER TABLE saved_views DELETE WHERE page = ? AND version < ?") {
		t.Fatalf("silme yüklemi: %s", aiChatSweepDeleteSQL)
	}
	if strings.Contains(aiChatSweepDeleteSQL, "'ai-chat'") || aiChatPage != "ai-chat" {
		t.Fatal("sayfa adı bağ argümanı; sabit ai-chat")
	}
}
