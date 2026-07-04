package notify

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/cilcenk/coremetry/internal/chstore"
)

// v0.8.247 — notification-log target contract: destinations are stored
// at FULL fidelity (CLAUDE.md hard constraint: no redaction features).
// The ONE exception is webhook URLs — host-only, because the URL path
// is a live credential (the settings surfaces' "never echo back"
// discipline, not data redaction). These tests pin both directions so
// a future edit can't quietly re-introduce recipient masking OR start
// persisting webhook secrets.

func ch(kind, cfg string) chstore.NotificationChannel {
	return chstore.NotificationChannel{Type: kind, Name: "t", Config: json.RawMessage(cfg)}
}

func TestHostOnlyStripsCredentialPath(t *testing.T) {
	cases := []struct{ in, want string }{
		{"https://hooks.slack.com/services/T00/B00/SECRETXX", "hooks.slack.com"},
		{"http://internal-hook.bank.local:8443/pathsecret", "internal-hook.bank.local:8443"},
		{"hooks.slack.com", "hooks.slack.com"}, // bare host passes through
		{"", ""},
	}
	for _, c := range cases {
		if got := hostOnly(c.in); got != c.want {
			t.Errorf("hostOnly(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestChannelTargetFullFidelity(t *testing.T) {
	// Recipients round-trip COMPLETE — "j***@…" style masking is a
	// regression against the operator's no-redaction preference.
	if got := channelTarget(ch("email", `{"recipients":["john.doe@bank.com","ops@bank.com"]}`)); got != "john.doe@bank.com, ops@bank.com" {
		t.Errorf("email target = %q, want full recipient list", got)
	}
	if got := channelTarget(ch("whatsapp", `{"to":["+14155238886"]}`)); got != "+14155238886" {
		t.Errorf("whatsapp target = %q, want full number", got)
	}
	if got := channelTarget(ch("zoomchat", `{"channelId":"zoom-channel-jid-abc123"}`)); got != "zoom-channel-jid-abc123" {
		t.Errorf("zoom target = %q, want full channel id", got)
	}
	// Webhook family: host survives, secret path never does.
	got := channelTarget(ch("slack", `{"webhookUrl":"https://hooks.slack.com/services/T00/B00/SECRETXX"}`))
	if got != "hooks.slack.com" {
		t.Errorf("slack target = %q, want host only", got)
	}
	if strings.Contains(got, "SECRET") {
		t.Error("webhook secret leaked into the log target")
	}
}

func TestPreviewRuneSafeTruncation(t *testing.T) {
	if got := preview("  kısa metin  ", 200); got != "kısa metin" {
		t.Errorf("preview trim = %q", got)
	}
	long := strings.Repeat("ğ", 300) // multi-byte: rune-safe cut, no torn UTF-8
	if got := preview(long, 200); len([]rune(got)) != 200 || !strings.HasPrefix(long, got) {
		t.Errorf("preview truncation broke rune boundary: %d runes", len([]rune(got)))
	}
	if got := preview("x", 0); got != "" {
		t.Errorf("preview n=0 = %q, want empty", got)
	}
}
