package chstore

import (
	"regexp"
	"strings"
)

// v0.5.406 — path templating for topology TopLabels. Honeycomb /
// Datadog auto-template paths so concrete-id variants
// (/users/abc-123-uuid/orders, /users/def-456-uuid/orders)
// collapse into a single readable shape (/users/{id}/orders).
//
// We apply this AT READ TIME on the labels returned by topK in
// topology_edges_5m. The stored labels remain raw (so the MV
// stays simple); the read path post-processes them. Cost: ~5K
// edges × ~5 labels each × a handful of regex passes — single-
// digit milliseconds total, dominated by per-call regex setup
// that's amortised here by pre-compiling each pattern.
//
// After templating, multiple raw labels may collapse to the
// same templated form — dedupTemplatedLabels keeps the first
// occurrence (preserves topK ranking).
//
// The patterns are conservative: they catch the common ID
// shapes (UUIDs, numeric IDs, long hex digests) without
// touching meaningful path segments. A path like
// `/api/v2/users` keeps `v2` intact (no slash before digits-
// only suffix → no match); `/api/users/12345` becomes
// `/api/users/{id}`.

// pathUUIDRe matches the canonical 8-4-4-4-12 hex UUID form
// (case-insensitive on the hex characters). Named with the
// pathPrefix so it doesn't collide with exception_inbox.go's
// own uuidRe (different shape, different use).
var pathUUIDRe = regexp.MustCompile(`(?i)[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}`)

// numericSegmentRe matches a path segment that is all digits
// (preceded by `/`, optionally followed by `/` or end). Catches
// /users/12345, /orders/7. NOT /v2/foo (digit not the only
// content) and NOT /api2 (no leading slash).
var numericSegmentRe = regexp.MustCompile(`/[0-9]+(?:/|$)`)

// longHexRe matches a path segment of ≥16 hex chars — trace
// IDs (16), span IDs (16), MD5 (32), SHA-1 (40), SHA-256 (64).
// Required leading slash so middle-of-word matches don't fire.
var longHexRe = regexp.MustCompile(`(?i)/[0-9a-f]{16,}(?:/|$)`)

// b64IdRe matches /<base64url-32+>/ segments — JWT fragments,
// long opaque session ids. Conservative on length so real
// words like "session" don't match.
var b64IdRe = regexp.MustCompile(`/[A-Za-z0-9_-]{20,}(?:/|$)`)

// templatePath returns `p` with embedded IDs replaced by `{id}`.
// Order matters: UUIDs first (they contain hex, which would
// otherwise be caught by longHexRe at fragments). Then numeric.
// Then long hex. Then long base64url. Each replace is
// idempotent — applying templatePath twice yields the same
// output.
func templatePath(p string) string {
	if p == "" {
		return p
	}
	p = pathUUIDRe.ReplaceAllString(p, "{id}")
	p = numericSegmentRe.ReplaceAllStringFunc(p, func(m string) string {
		if strings.HasSuffix(m, "/") {
			return "/{id}/"
		}
		return "/{id}"
	})
	p = longHexRe.ReplaceAllStringFunc(p, func(m string) string {
		if strings.HasSuffix(m, "/") {
			return "/{id}/"
		}
		return "/{id}"
	})
	p = b64IdRe.ReplaceAllStringFunc(p, func(m string) string {
		if strings.HasSuffix(m, "/") {
			return "/{id}/"
		}
		return "/{id}"
	})
	return p
}

// templateLabel applies templating to a topology edge label
// (e.g. "GET /api/users/12345" → "GET /api/users/{id}"). Label
// format is "METHOD path"; templatePath operates on the path
// portion only. For non-HTTP labels (db/msg/internal), the
// whole string passes through templatePath since there's no
// method prefix.
func templateLabel(label string) string {
	if label == "" {
		return label
	}
	// HTTP labels: "METHOD path" — split on first space.
	if sp := strings.IndexByte(label, ' '); sp > 0 && sp < len(label)-1 {
		method := label[:sp]
		rest := label[sp+1:]
		// Only template if the second part looks like a path.
		if len(rest) > 0 && rest[0] == '/' {
			return method + " " + templatePath(rest)
		}
	}
	return templatePath(label)
}

// dedupTemplatedLabels applies templateLabel to each entry and
// returns a deduplicated slice preserving first-occurrence
// order. Operator's eye-scan benefits from the topK ranking
// staying stable across templating.
func dedupTemplatedLabels(labels []string) []string {
	if len(labels) == 0 {
		return labels
	}
	out := make([]string, 0, len(labels))
	seen := make(map[string]struct{}, len(labels))
	for _, l := range labels {
		t := templateLabel(l)
		if _, ok := seen[t]; ok {
			continue
		}
		seen[t] = struct{}{}
		out = append(out, t)
	}
	return out
}
