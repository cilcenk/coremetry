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
// "Live patterns" panel free of JWT fragments and base64
// session ids that statistically score high but mean nothing
// to a human reader.
//
// Rules (post-filter — token already passed token-worth
// filters in the backend):
//   - JWT-shaped (starts with eyJ, base64url chars, ≥30 chars)
//   - Long base64url ≥24 chars with no English-letter run ≥3
//     (very low chance of being a real word)
func LooksLikeOpaqueID(tok string) bool {
	if len(tok) < 16 {
		return false
	}
	// JWT fragment — three eyJ-prefixed base64 segments OR a
	// bare eyJ... payload that lost its other parts to the
	// tokenizer.
	if strings.HasPrefix(tok, "eyJ") && isBase64URLish(tok) {
		return true
	}
	// Long base64url with no alphabetic-run hint — token
	// looks like an opaque correlation id (session id, file
	// hash, signature segment). Be conservative on length so
	// real long words ("CommunicationsException" etc.) don't
	// get dropped.
	if len(tok) >= 24 && isBase64URLish(tok) && !hasLetterRun(tok, 5) {
		return true
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
