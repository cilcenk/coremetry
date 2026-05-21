// Package templater turns raw log lines into stable templates via
// the Drain-3 algorithm (online template extraction with a fixed-
// depth tree). Backend-agnostic: a puller goroutine in this
// package periodically samples logs from whichever backend is
// wired (CH or ES), feeds them to Drain, and upserts the
// resulting templates into chstore.LogTemplate so the operator
// can see "what shapes of log are firing right now".
//
// Tuned for Java-heavy production traffic where stack traces +
// MDC contexts dominate, so the masker preserves class names
// and logger paths while masking IDs and timestamps.
package templater

import (
	"regexp"
)

// maskRule is one regex + replacement pass over tokens. Order
// matters — the regexes are applied in this order so longer /
// more specific patterns (UUIDs, ISO timestamps) match before
// the catch-all numeric mask.
type maskRule struct {
	re   *regexp.Regexp
	repl string
}

var maskRules = []maskRule{
	// Defense-in-depth for auth payloads. The puller filters
	// IsSensitiveLine bodies out of templating altogether, but
	// if a sensitive line slipped through (different framing,
	// embedded JSON, etc.), strip the secret here so the
	// template doesn't carry it literally.
	// JWT — three base64url segments, eyJ-headered.
	{regexp.MustCompile(`eyJ[A-Za-z0-9_-]{10,}\.eyJ[A-Za-z0-9_-]{10,}\.[A-Za-z0-9_-]{10,}`), "<*>"},
	// Bearer <opaque>
	{regexp.MustCompile(`(?i)\bbearer\s+[A-Za-z0-9._\-+/=]{20,}`), "Bearer <*>"},
	// Authorization: <opaque>
	{regexp.MustCompile(`(?i)\bauthorization(\s*[:=]\s*)\S{8,}`), "Authorization$1<*>"},
	// X-API-Key / api_key / apikey = <opaque>
	{regexp.MustCompile(`(?i)\b(x-api-key|api[-_ ]?key|apikey)(\s*[:=]\s*)[A-Za-z0-9._\-+/=]{12,}`), "$1$2<*>"},

	// Trace IDs / span IDs — 16 or 32 hex chars (OTel canonical).
	{regexp.MustCompile(`\b[0-9a-fA-F]{32}\b`), "<*>"},
	{regexp.MustCompile(`\b[0-9a-fA-F]{16}\b`), "<*>"},
	// UUIDs
	{regexp.MustCompile(`\b[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}\b`), "<*>"},
	// ISO 8601 timestamps (with or without timezone)
	{regexp.MustCompile(`\b\d{4}-\d{2}-\d{2}[Tt ]\d{2}:\d{2}:\d{2}(?:\.\d+)?(?:Z|[+-]\d{2}:?\d{2})?\b`), "<*>"},
	// HH:MM:SS standalone timestamps
	{regexp.MustCompile(`\b\d{2}:\d{2}:\d{2}(?:\.\d+)?\b`), "<*>"},
	// IPv4
	{regexp.MustCompile(`\b\d{1,3}\.\d{1,3}\.\d{1,3}\.\d{1,3}(?::\d{1,5})?\b`), "<*>"},
	// URLs
	{regexp.MustCompile(`https?://\S+`), "<*>"},
	// Email addresses
	{regexp.MustCompile(`\b[\w.+-]+@[\w.-]+\.\w+\b`), "<*>"},
	// File paths (Unix + Windows). Aggressively replaces so
	// "/var/log/foo-12345.log" → "<*>" instead of fragmenting
	// the template along the path components.
	{regexp.MustCompile(`/[\w./_-]+`), "<*>"},
	{regexp.MustCompile(`[A-Za-z]:\\[\w\\._-]+`), "<*>"},
	// Java MDC / structured-context brackets (key=value, ...)
	// "[traceId=abc, userId=xyz]" — bracket content is too
	// variable to be a template token, mask the whole bracket.
	{regexp.MustCompile(`\[[\w\s=,;:.&-]+\]`), "[<*>]"},
	// Java thread names "[http-nio-8080-exec-7]" already covered
	// by the bracket rule above.
	// Bare numbers — last so it doesn't gobble pieces of the
	// above regexes. \b boundaries prevent matching "ORA-12345"
	// fragments (the dash is non-word so the digits boundary).
	{regexp.MustCompile(`\b\d+\b`), "<*>"},
}

// Mask returns the input line with every variable substring
// replaced by "<*>". Order-dependent regex set defined in
// maskRules — see comments there for ordering rationale.
func Mask(line string) string {
	out := line
	for _, r := range maskRules {
		out = r.re.ReplaceAllString(out, r.repl)
	}
	return out
}

// Tokenize splits on whitespace AFTER masking, preserving the
// "<*>" tokens as standalone units. Drain operates on token
// arrays; whitespace inside a value is already neutralised by
// the mask pass.
func Tokenize(line string) []string {
	masked := Mask(line)
	// Strings.Fields handles arbitrary whitespace runs cleanly.
	out := make([]string, 0, 16)
	tok := ""
	for _, c := range masked {
		if c == ' ' || c == '\t' {
			if tok != "" {
				out = append(out, tok)
				tok = ""
			}
			continue
		}
		tok += string(c)
	}
	if tok != "" {
		out = append(out, tok)
	}
	return out
}
