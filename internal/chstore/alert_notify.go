package chstore

// alert_notify.go — v0.10.519 (operatör 2026-09-06: "spesifik alarmı tek
// bir ekibe ya da uygulama geliştirme ekibi + SY takımı şeklinde
// gönderebilme"; spec onayı 2026-09-07 "kural bazında ekip yaz").
//
// AlertRule.Notify (alert_rules.notify_json): kuralın açtığı problemin
// ekip-maili alıcıları. Bugüne dek alıcı yalnız servis kataloğunun
// sahip (ug) + SRE (sy) takımıydı (v0.8.429). Mode:
//   - "add"  (varsayılan) → sahip + SRE'ye EK olarak bu ekipler;
//   - "only"              → YALNIZ bu ekipler.
// Adresler team_contacts blobundan çözülür (notify/routing.go). Slack /
// webhook kanalları MatchRules düzeninde kalır — bu dilim e-posta hattı.
//
// Kolon target_json emsalini izler: küme kipinde ertelenmiş DDL, iki-boot
// sözleşmesi, kolon yokken Notify'lı kayıt 409, okuma '' ile sürer.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

const (
	RuleNotifyModeAdd  = "add"
	RuleNotifyModeOnly = "only"
	RuleNotifyMaxTeams = 10
)

// RuleNotify — kuralın bildirim hedefi. Teams katalog/team_contacts ekip
// adları (büyük/küçük harfsiz eşleşir, v0.8.330 dersi).
type RuleNotify struct {
	Teams []string `json:"teams"`
	Mode  string   `json:"mode,omitempty"` // add | only ("" = add)
}

// ErrRuleNotifyColumnMissing — iki-boot sözleşmesi (target_json emsali).
var ErrRuleNotifyColumnMissing = errors.New("alert_rules.notify_json column not yet available — the deferred DDL has not landed yet; retry in a minute (a restart is not required once it lands)")

// NormalizeRuleNotify — SAF: adları kırpar, boşları ve (harfsiz) kopyaları
// düşürür, kipi varsayılana çeker. Ekip kalmazsa nil (hedef yok).
func NormalizeRuleNotify(n *RuleNotify) *RuleNotify {
	if n == nil {
		return nil
	}
	seen := make(map[string]bool, len(n.Teams))
	var teams []string
	for _, t := range n.Teams {
		t = strings.TrimSpace(t)
		if t == "" {
			continue
		}
		k := strings.ToLower(t)
		if seen[k] {
			continue
		}
		seen[k] = true
		teams = append(teams, t)
	}
	if len(teams) == 0 {
		return nil
	}
	mode := strings.ToLower(strings.TrimSpace(n.Mode))
	if mode == "" {
		mode = RuleNotifyModeAdd
	}
	return &RuleNotify{Teams: teams, Mode: mode}
}

// ValidateRuleNotify — API 400 kaynağı (normalize edilmiş değer üzerinde).
func ValidateRuleNotify(n *RuleNotify) error {
	if n == nil {
		return nil
	}
	if len(n.Teams) > RuleNotifyMaxTeams {
		return fmt.Errorf("notify: en fazla %d ekip", RuleNotifyMaxTeams)
	}
	for _, t := range n.Teams {
		if len(t) > 120 {
			return fmt.Errorf("notify: ekip adı çok uzun: %q", t[:20]+"…")
		}
	}
	if n.Mode != RuleNotifyModeAdd && n.Mode != RuleNotifyModeOnly {
		return fmt.Errorf("notify: mode %q tanınmıyor (add | only)", n.Mode)
	}
	return nil
}

func encodeRuleNotify(n *RuleNotify) string {
	n = NormalizeRuleNotify(n)
	if n == nil {
		return ""
	}
	b, err := json.Marshal(n)
	if err != nil {
		return ""
	}
	return string(b)
}

func decodeRuleNotify(raw string) *RuleNotify {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil
	}
	var n RuleNotify
	if err := json.Unmarshal([]byte(raw), &n); err != nil {
		return nil
	}
	return NormalizeRuleNotify(&n)
}

// alertRuleNotifySelect — kolon henüz yoksa SELECT sabit ” okur.
func (s *Store) alertRuleNotifySelect() string {
	if s.hasAlertRuleNotifyCol.Load() {
		return "notify_json"
	}
	return "''"
}

// probeAlertRuleNotifyCol — boot'ta ve (kolon yokken) Notify'lı kayıtta.
func (s *Store) probeAlertRuleNotifyCol(ctx context.Context) bool {
	rows, err := s.conn.Query(ctx, `SELECT notify_json FROM alert_rules LIMIT 1 SETTINGS max_execution_time = 3`)
	maybeCloseRows(rows, err)
	ok := err == nil
	s.hasAlertRuleNotifyCol.Store(ok)
	return ok
}
