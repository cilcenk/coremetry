package api

import (
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// ── redactSecrets / mergeSecrets ─────────────────────────────
//
// These two functions guard the round-trip of notification-channel
// secrets: redactSecrets strips them on read, mergeSecrets preserves
// them on partial-update. A regression here either (a) leaks
// secrets via the channel-list endpoint or (b) silently wipes them
// when an operator edits a non-secret field. Both have shipped
// before (pre-v0.4.78); these tests lock the contract.

func TestRedactSecretsStripsZoomClientSecret(t *testing.T) {
	in := json.RawMessage(`{"accountId":"a","clientId":"c","clientSecret":"shhh","channelId":"x"}`)
	out := redactSecrets("zoomchat", in)
	if strings.Contains(string(out), "shhh") {
		t.Fatalf("zoom clientSecret leaked: %s", out)
	}
	if !strings.Contains(string(out), `"accountId":"a"`) {
		t.Fatalf("non-secret fields must survive: %s", out)
	}
}

func TestRedactSecretsStripsWhatsAppAuthToken(t *testing.T) {
	in := json.RawMessage(`{"accountSid":"AC1","authToken":"shhh","to":"+1"}`)
	out := redactSecrets("whatsapp", in)
	if strings.Contains(string(out), "shhh") {
		t.Fatalf("whatsapp authToken leaked: %s", out)
	}
}

func TestRedactSecretsGenericCatchAll(t *testing.T) {
	// Generic safety net — anything with password/secret/token/apikey
	// in the field name must be stripped, regardless of channel type.
	in := json.RawMessage(`{"url":"https://x","webhookSecret":"shhh","apiKey":"sk","password":"p","accessToken":"t","normal":"keep"}`)
	out := redactSecrets("slack", in) // type unknown to the type-specific switch
	for _, banned := range []string{"shhh", "sk", "\"p\"", "\"t\""} {
		if strings.Contains(string(out), banned) {
			t.Errorf("generic redactor missed secret %q in: %s", banned, out)
		}
	}
	if !strings.Contains(string(out), `"normal":"keep"`) {
		t.Errorf("non-secret field stripped: %s", out)
	}
}

func TestRedactSecretsPassesThroughEmpty(t *testing.T) {
	if got := redactSecrets("zoomchat", nil); got != nil {
		t.Errorf("nil input must pass through, got %v", got)
	}
	if got := redactSecrets("zoomchat", json.RawMessage("")); len(got) != 0 {
		t.Errorf("empty input must pass through, got %v", got)
	}
}

func TestRedactSecretsInvalidJSONPassesThrough(t *testing.T) {
	// Don't silently transform garbage — the caller's storage layer
	// will surface its own error.
	in := json.RawMessage(`not json`)
	out := redactSecrets("zoomchat", in)
	if string(out) != "not json" {
		t.Errorf("invalid JSON must pass through unchanged, got %s", out)
	}
}

func TestMergeSecretsPreservesOnBlankEdit(t *testing.T) {
	// The "leave-blank-to-keep" UX: operator edits the channel name
	// without re-typing the secret. The incoming body has clientSecret=""
	// (because redactSecrets stripped it earlier and the form didn't
	// repopulate it). mergeSecrets must carry the previous value.
	existing := json.RawMessage(`{"accountId":"a","clientId":"c","clientSecret":"prev"}`)
	incoming := json.RawMessage(`{"accountId":"a","clientId":"new-c"}`)
	out := mergeSecrets("zoomchat", existing, incoming)
	if !strings.Contains(string(out), `"clientSecret":"prev"`) {
		t.Fatalf("clientSecret was wiped on partial update: %s", out)
	}
	if !strings.Contains(string(out), `"clientId":"new-c"`) {
		t.Fatalf("non-secret edit must apply: %s", out)
	}
}

func TestMergeSecretsRespectsExplicitRotate(t *testing.T) {
	// If the operator did type a new value, that wins — the carry-over
	// must not stomp a deliberate rotation.
	existing := json.RawMessage(`{"clientSecret":"prev"}`)
	incoming := json.RawMessage(`{"clientSecret":"rotated"}`)
	out := mergeSecrets("zoomchat", existing, incoming)
	if !strings.Contains(string(out), `"clientSecret":"rotated"`) {
		t.Fatalf("deliberate rotation must survive merge: %s", out)
	}
	if strings.Contains(string(out), `"prev"`) {
		t.Fatalf("merge resurrected old secret: %s", out)
	}
}

func TestMergeSecretsEmptyExistingNoOp(t *testing.T) {
	// Cold-create path: no existing record. Incoming is returned as-is.
	out := mergeSecrets("zoomchat", nil, json.RawMessage(`{"x":1}`))
	if string(out) != `{"x":1}` {
		t.Errorf("empty existing must passthrough incoming, got %s", out)
	}
}

// ── parseFromTo / parseTime / parseDuration / parseInt ───────

func TestParseTimeUnixNanos(t *testing.T) {
	want := int64(1_700_000_000_123_456_789)
	got := parseTime("1700000000123456789")
	if got.UnixNano() != want {
		t.Errorf("UnixNano: got %d want %d", got.UnixNano(), want)
	}
}

func TestParseTimeEmptyIsZero(t *testing.T) {
	if !parseTime("").IsZero() {
		t.Error("empty string must yield zero time so callers can detect 'not set'")
	}
	if !parseTime("not-a-number").IsZero() {
		t.Error("garbage must yield zero time, not panic")
	}
}

func TestParseDurationFallback(t *testing.T) {
	if got := parseDuration("", time.Minute); got != time.Minute {
		t.Errorf("empty: got %v want 1m", got)
	}
	if got := parseDuration("garbage", time.Minute); got != time.Minute {
		t.Errorf("garbage: got %v want 1m", got)
	}
	if got := parseDuration("90s", time.Minute); got != 90*time.Second {
		t.Errorf("valid: got %v want 90s", got)
	}
}

func TestParseIntFallback(t *testing.T) {
	if got := parseInt("", 7); got != 7 {
		t.Errorf("empty: got %d want 7", got)
	}
	if got := parseInt("garbage", 7); got != 7 {
		t.Errorf("garbage: got %d want 7", got)
	}
	if got := parseInt("42", 7); got != 42 {
		t.Errorf("valid: got %d want 42", got)
	}
}

func TestParseFromToDefaultsToNowMinusWindow(t *testing.T) {
	req := httptest.NewRequest("GET", "/x", nil)
	from, to := parseFromTo(req, 5*time.Minute)
	if to.IsZero() {
		t.Fatal("to must default to now when unset")
	}
	gap := to.Sub(from)
	if gap < 4*time.Minute || gap > 6*time.Minute {
		t.Errorf("default window: got %v want ~5m", gap)
	}
}

func TestParseFromToRespectsExplicitParams(t *testing.T) {
	from := time.Now().Add(-1 * time.Hour)
	to := time.Now()
	req := httptest.NewRequest("GET",
		"/x?from="+itoa(from.UnixNano())+"&to="+itoa(to.UnixNano()), nil)
	gotFrom, gotTo := parseFromTo(req, 5*time.Minute)
	if gotFrom.UnixNano() != from.UnixNano() {
		t.Errorf("from drift: got %d want %d", gotFrom.UnixNano(), from.UnixNano())
	}
	if gotTo.UnixNano() != to.UnixNano() {
		t.Errorf("to drift: got %d want %d", gotTo.UnixNano(), to.UnixNano())
	}
}

// ── sanitizePassword ─────────────────────────────────────────
//
// Locks the contract that paste-mangle (trailing whitespace,
// invisible soft-hyphens injected when copying from rendered
// HTML) does not silently fail bcrypt/LDAP compare. Visible
// homoglyphs (en/em-dash) are deliberately preserved — the i18n
// hint on the login screen owns that user-facing recovery path.

func TestSanitizePasswordTrimsLeadingTrailingSpace(t *testing.T) {
	if got := sanitizePassword("  hunter2\t\n"); got != "hunter2" {
		t.Errorf("trim: got %q want %q", got, "hunter2")
	}
}

func TestSanitizePasswordStripsSoftHyphen(t *testing.T) {
	// "se­cret" — rendered as "secret" but bcrypt-compares as different.
	if got := sanitizePassword("se­cret"); got != "secret" {
		t.Errorf("soft-hyphen: got %q want %q", got, "secret")
	}
}

func TestSanitizePasswordPreservesEnDash(t *testing.T) {
	// en-dash is visible; could be a real character. Frontend hint
	// handles user confusion. Do not silently rewrite.
	in := "foo–bar"
	if got := sanitizePassword(in); got != in {
		t.Errorf("en-dash must survive: got %q want %q", got, in)
	}
}

func TestSanitizePasswordPreservesInternalSpace(t *testing.T) {
	in := "two words"
	if got := sanitizePassword(in); got != in {
		t.Errorf("internal space must survive: got %q want %q", got, in)
	}
}

func itoa(n int64) string {
	const digits = "0123456789"
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	buf := make([]byte, 0, 20)
	for n > 0 {
		buf = append([]byte{digits[n%10]}, buf...)
		n /= 10
	}
	if neg {
		return "-" + string(buf)
	}
	return string(buf)
}
