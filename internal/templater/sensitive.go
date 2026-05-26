package templater

import (
	"regexp"
	"strings"
)

// Sensitive-log handling.
//
// The Drain templater is supposed to fold many similar log
// lines into one stable template — but auth / audit / token-
// bearing lines defeat that purpose: every request carries a
// unique bearer / api-key / session-id, so each one either
// (a) creates its own near-unique template that pollutes the
// templates view with single-instance noise, or
// (b) gets aggressively masked into "<*>" placeholders that
// collapse semantically different lines together.
//
// Operator preference (v0.5.336): SKIP these lines from the
// templater entirely. They still appear in raw log search;
// they just don't surface in the Templates / Live Patterns
// panels where they're noise.
//
// Detection rules are conservative on purpose — false
// positives mean a legit operational log line goes missing
// from Templates, which is worse than a JWT-bearing line
// occasionally slipping through.

var (
	// JWT — three dot-separated base64url segments where the
	// first two start with the conventional "eyJ" header. The
	// regex matches the whole token; a body containing one
	// anywhere is enough to mark the line sensitive.
	rxJWT = regexp.MustCompile(`eyJ[A-Za-z0-9_-]{10,}\.eyJ[A-Za-z0-9_-]{10,}\.[A-Za-z0-9_-]{10,}`)

	// Bearer <token> in any case. Token portion is base64url-
	// ish and at least 20 chars so we don't trip on short
	// literals like "Bearer NONE" placeholder docs.
	rxBearer = regexp.MustCompile(`(?i)\bbearer\s+[A-Za-z0-9._\-+/=]{20,}`)

	// HTTP Authorization header in either log-line form.
	rxAuthHeader = regexp.MustCompile(`(?i)\bauthorization\s*[:=]\s*\S{8,}`)

	// X-API-Key / api_key / apikey + value (>=12 chars).
	rxAPIKey = regexp.MustCompile(`(?i)\b(?:x-api-key|api[-_ ]?key|apikey)\s*[:=]\s*[A-Za-z0-9._\-+/=]{12,}`)
)

// IsSensitiveLine returns true when the body almost certainly
// carries an auth credential or per-request opaque correlation
// ID. Such lines are excluded from templating in the puller.
func IsSensitiveLine(body string) bool {
	if body == "" {
		return false
	}
	// Quick rejects to avoid running 4 regexes on every log
	// line at billion-line scale. Cheap substring fast-paths:
	low := strings.ToLower(body)
	if !(strings.Contains(low, "bearer ") ||
		strings.Contains(low, "authorization") ||
		strings.Contains(low, "api-key") ||
		strings.Contains(low, "apikey") ||
		strings.Contains(low, "api_key") ||
		strings.Contains(body, "eyJ")) {
		return false
	}
	if rxJWT.MatchString(body) {
		return true
	}
	if rxBearer.MatchString(body) {
		return true
	}
	if rxAuthHeader.MatchString(body) {
		return true
	}
	if rxAPIKey.MatchString(body) {
		return true
	}
	return false
}

// LooksLikeOpaqueID returns true when a single token looks
// like a per-request opaque value rather than a real keyword.
// Surfaced via /api/logs/patterns post-filter to keep the
// "Live patterns" panel free of JWT fragments, UUIDs, trace
// IDs and base64 session ids that statistically score high but
// mean nothing to a human reader.
//
// Rules (post-filter — token already passed token-worth
// filters in the backend):
//
//   - JWT-shaped (starts with eyJ, base64url chars, ≥16 chars)
//   - UUID (8-4-4-4-12 hex with dashes; case-insensitive)
//   - Hex strings ≥16 chars (trace IDs are 32-hex, span IDs
//     16-hex, MD5 32-hex, SHA-1 40-hex, SHA-256 64-hex)
//   - All-digit strings ≥4 digits (sequence IDs, request
//     counters, port numbers, epoch timestamps). 3-digit cutoff
//     preserved so HTTP status codes (200, 404, 500) still
//     surface as meaningful patterns.
//   - Long base64url ≥24 chars with no English-letter run ≥5
//   - High-digit-ratio tokens (≥60% digits AND length ≥ 10)
//     — catches mixed alphanumeric IDs like "a1b2c3d4e5f6"
//   - Consonant-only tokens length ≥ 10 — random strings
//     emitted by hash truncation typically have ~no vowels
//
// v0.5.397 — operator-reported: panel still showed UUIDs +
// trace ids + numeric request ids despite v0.5.336's initial
// filter. The original rules only caught JWTs and pure-base64
// strings; the broader ID shapes leaked through.
func LooksLikeOpaqueID(tok string) bool {
	n := len(tok)
	// v0.5.465 — short-numeric check runs BEFORE the n < 10
	// early return. The other rules (JWT, UUID, hex digests,
	// base64) all need ≥10 chars to make sense, but 4-9 digit
	// all-numeric tokens are sequence IDs / request counters
	// that the operator wants filtered too.
	if n >= 4 && isAllDigits(tok) {
		return true
	}
	if n < 10 {
		return false
	}
	// JWT fragment.
	if n >= 16 && strings.HasPrefix(tok, "eyJ") && isBase64URLish(tok) {
		return true
	}
	// Canonical UUID — 8-4-4-4-12 hex with dashes. Operator-
	// reported pattern. Length is always 36; check both length
	// and shape to avoid false positives on "abcd-defg" style
	// route templates.
	if n == 36 && isUUID(tok) {
		return true
	}
	// Pure-hex strings ≥16 chars (trace IDs, span IDs, hash
	// digests). Allow optional dashes inside (some loggers
	// dash-segment digests).
	if n >= 16 && isHexish(tok) {
		return true
	}
	// (All-digit check above the n < 10 short-circuit handles
	// every length ≥ 4; this branch is now unreachable but
	// kept commented for legibility of the rule list.)
	// High-digit-ratio mixed tokens — random alphanumeric IDs
	// like "a1b2c3d4e5f6789" or "tx_88273482ab12". A real word
	// rarely has >50% digits; an ID often does.
	if n >= 10 && digitRatio(tok) >= 0.60 {
		return true
	}
	// Long base64url with no alphabetic-run hint.
	if n >= 24 && isBase64URLish(tok) && !hasLetterRun(tok, 5) {
		return true
	}
	// Consonant-only / no-vowels token ≥ 10 chars — typical of
	// truncated hashes ("zxptkrqln") and base32-encoded ids.
	if n >= 10 && !hasVowel(tok) && hasLetterRun(tok, 6) {
		return true
	}
	return false
}

// isUUID returns true for the canonical 8-4-4-4-12 hex form.
// Case-insensitive on the hex characters; dashes must be at
// positions 8, 13, 18, 23.
func isUUID(t string) bool {
	if len(t) != 36 {
		return false
	}
	for i, c := range t {
		switch i {
		case 8, 13, 18, 23:
			if c != '-' {
				return false
			}
		default:
			if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f') || (c >= 'A' && c <= 'F')) {
				return false
			}
		}
	}
	return true
}

// isHexish returns true when every char is a hex digit (with
// optional dashes — some loggers dash-segment trace IDs like
// "d4-b1-...").
func isHexish(t string) bool {
	hexCount := 0
	for i := 0; i < len(t); i++ {
		c := t[i]
		switch {
		case (c >= '0' && c <= '9') || (c >= 'a' && c <= 'f') || (c >= 'A' && c <= 'F'):
			hexCount++
		case c == '-':
			// allowed separator
		default:
			return false
		}
	}
	// Require at least 14 actual hex digits so a short "a-b"
	// route slug doesn't trip the check.
	return hexCount >= 14
}

func isAllDigits(t string) bool {
	for i := 0; i < len(t); i++ {
		if t[i] < '0' || t[i] > '9' {
			return false
		}
	}
	return true
}

func digitRatio(t string) float64 {
	if len(t) == 0 {
		return 0
	}
	d := 0
	for i := 0; i < len(t); i++ {
		if t[i] >= '0' && t[i] <= '9' {
			d++
		}
	}
	return float64(d) / float64(len(t))
}

func hasVowel(t string) bool {
	for i := 0; i < len(t); i++ {
		c := t[i] | 0x20 // lowercase
		if c == 'a' || c == 'e' || c == 'i' || c == 'o' || c == 'u' {
			return true
		}
	}
	return false
}

func isBase64URLish(t string) bool {
	for i := 0; i < len(t); i++ {
		c := t[i]
		base64 := (c >= 'A' && c <= 'Z') || (c >= 'a' && c <= 'z') ||
			(c >= '0' && c <= '9') || c == '-' || c == '_' || c == '.' ||
			c == '+' || c == '/' || c == '='
		if !base64 {
			return false
		}
	}
	return true
}

// hasLetterRun returns true when the token contains a contiguous
// run of letters of length ≥ n. Used to spare real English-
// looking compound tokens (e.g. "OrderProcessingException") from
// the opaque-id filter — a 5-letter run is rare in pure base64
// where digits/dashes/underscores break things up.
func hasLetterRun(t string, n int) bool {
	run := 0
	for i := 0; i < len(t); i++ {
		c := t[i]
		if (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') {
			run++
			if run >= n {
				return true
			}
		} else {
			run = 0
		}
	}
	return false
}
