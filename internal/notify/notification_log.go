package notify

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/url"
	"strings"
	"time"

	"github.com/cilcenk/coremetry/internal/chstore"
)

// Notification history (v0.8.247). Every channel dispatch routes
// through Notifier.sendOne, which — after the send — records one
// append-only row via recordNotification. The operator reads the
// trail on /events ("Notifications sent"): did the page go out, to
// whom, and did it succeed?
//
// Destinations are stored at FULL fidelity (operator preference — no
// redaction features, CLAUDE.md hard constraint): complete recipient
// emails, phone numbers, channel ids. The ONE exception is webhook
// URLs, kept host-only — the path of a Slack/Teams/webhook URL is a
// LIVE CREDENTIAL, and credentials never round-trip to readers (the
// same "never echo back" discipline every settings surface follows).
// That's secret hygiene, not data redaction.

// recordNotification writes one send outcome to the notification_log.
// Log-and-continue on error — a persistence failure must never block
// or re-fire the notification (which already happened by this point).
// Runs on a detached, short-deadline context so a cancelled
// evaluator-tick ctx doesn't drop the audit row.
func (n *Notifier) recordNotification(ctx context.Context, c chstore.NotificationChannel, p chstore.Problem, relKind, relID string, sendErr error) {
	entry := chstore.NotificationLog{
		ChannelKind: c.Type,
		ChannelName: c.Name,
		Target:      channelTarget(c),
		Subject:     notificationSubject(p),
		BodyPreview: preview(p.Description, 200),
		RelatedKind: relKind,
		RelatedID:   relID,
		OK:          sendErr == nil,
	}
	if sendErr != nil {
		entry.Error = preview(sendErr.Error(), 500)
	}
	logCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	if err := n.store.InsertNotificationLog(logCtx, entry); err != nil {
		log.Printf("[notify] record notification log (%s/%s): %v", c.Type, c.Name, err)
	}
}

// notificationSubject mirrors the subject line the channels render so
// the log row reads the same as what the recipient saw.
func notificationSubject(p chstore.Problem) string {
	return fmt.Sprintf("[%s] %s — %s", strings.ToUpper(p.Severity), p.Service, p.RuleName)
}

// channelTarget extracts the human-facing destination for a channel —
// full fidelity except webhook URLs (host-only; the path is a live
// credential, see the header comment). Best-effort: an undecodable
// config yields "" rather than an error (the send already
// succeeded/failed independently).
func channelTarget(c chstore.NotificationChannel) string {
	switch c.Type {
	case "email":
		var ec EmailChannelConfig
		_ = json.Unmarshal(c.Config, &ec)
		return strings.Join(ec.Recipients, ", ")
	case "slack", "mattermost":
		var sc SlackChannelConfig
		_ = json.Unmarshal(c.Config, &sc)
		return hostOnly(sc.WebhookURL)
	case "teams":
		var tc TeamsChannelConfig
		_ = json.Unmarshal(c.Config, &tc)
		return hostOnly(tc.WebhookURL)
	case "zoomchat":
		var zc ZoomChatChannelConfig
		_ = json.Unmarshal(c.Config, &zc)
		if zc.ChannelID != "" {
			return zc.ChannelID
		}
		return zc.ToContact
	case "webhook":
		var wc WebhookChannelConfig
		_ = json.Unmarshal(c.Config, &wc)
		return hostOnly(wc.URL)
	case "whatsapp":
		var wc WhatsAppChannelConfig
		_ = json.Unmarshal(c.Config, &wc)
		return strings.Join(wc.To, ", ")
	}
	return ""
}

// hostOnly reduces a webhook URL to its host. Pure, table-tested:
//
//	"https://hooks.slack.com/services/T00/B00/SECRET" → "hooks.slack.com"
//	non-URL strings pass through unchanged (a bare host is already safe).
func hostOnly(t string) string {
	t = strings.TrimSpace(t)
	if strings.HasPrefix(t, "http://") || strings.HasPrefix(t, "https://") {
		if u, err := url.Parse(t); err == nil && u.Host != "" {
			return u.Host
		}
		return "***"
	}
	return t
}

// preview returns the first n characters (rune-safe) of s, trimmed.
func preview(s string, n int) string {
	if n <= 0 {
		return ""
	}
	s = strings.TrimSpace(s)
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n])
}
