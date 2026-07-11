package chstore

import (
	"context"
	"encoding/json"
	"time"
)

// Announcement — v0.8.486 (operatör isteği): admin'in Settings'ten
// girdiği, tüm kullanıcılara sayfa üstünde gösterilen duyuru şeridi
// ("sorularınız için: …@… · wiki: http://…"). Kaldırılan What-changed
// şeridinin (v0.8.481) yerine operatör-kontrollü içerik. Branding
// deseninin birebir kardeşi: system_settings altında "announcement"
// anahtarında tek JSON blob.
//
// UpdatedAtNs revizyon damgasıdır: istemci kapatma tercihini bu damgayla
// saklar — aynı duyuru bir daha çıkmaz, metin güncellenince yeniden
// çıkar (GitLab broadcast-message davranışı).
type Announcement struct {
	Enabled   bool   `json:"enabled"`
	Text      string `json:"text,omitempty"`
	LinkURL   string `json:"linkUrl,omitempty"`
	LinkLabel string `json:"linkLabel,omitempty"`
	// Tone: "info" (nötr) | "warn" (amber, önemli duyuru).
	Tone        string `json:"tone,omitempty"`
	UpdatedAtNs int64  `json:"updatedAtNs,omitempty"`
}

const announcementKey = "announcement"

// GetAnnouncement returns the saved banner (zero-value when unset).
func (s *Store) GetAnnouncement(ctx context.Context) (Announcement, error) {
	var a Announcement
	raw, err := s.GetSetting(ctx, announcementKey)
	if err != nil {
		return a, err
	}
	if len(raw) == 0 {
		return a, nil
	}
	if err := json.Unmarshal(raw, &a); err != nil {
		return a, err
	}
	return a, nil
}

// SaveAnnouncement persists the banner and stamps the revision —
// the stamp is what re-surfaces the banner for users who dismissed
// an older text.
func (s *Store) SaveAnnouncement(ctx context.Context, a Announcement) (Announcement, error) {
	a.UpdatedAtNs = time.Now().UnixNano()
	raw, err := json.Marshal(a)
	if err != nil {
		return a, err
	}
	return a, s.PutSetting(ctx, announcementKey, raw)
}
