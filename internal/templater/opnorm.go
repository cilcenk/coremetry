package templater

import (
	"regexp"
	"strings"
)

// Operation-shape normalizer for the op_group column (group_id
// release A). Computed PURELY per-span at ingest in
// internal/otlp/convert.go and stored as a LowCardinality(String)
// column. Forward-only — old spans read op_group=''.
//
// Why a separate pure normalizer instead of the Drain tree:
// internal/templater/drain.go's match path takes a mutex on every
// call (the cluster forest is shared, stateful state). At
// billion-spans/day that lock is a per-span ingest bottleneck. This
// function is allocation-light, lock-free, and deterministic — the
// same span name always folds to the same group regardless of what
// other spans came before it. No shared state, no learning.
//
// Output is a HUMAN-READABLE normalized name (e.g. "GET /users/:id"),
// NOT a hash — the operator reads it directly in the UI (release C).
//
// NEVER write a customer's literal name/value into the group. The
// whole point is to collapse the per-request varying bits (ids,
// literals) into stable placeholders so 10k+ raw operation names
// fold to a few hundred shapes.

const opGroupMaxLen = 200

// HTTP method tokens we recognise at the START of a raw span name
// like "GET /users/123". Span names emitted by HTTP instrumentation
// commonly take this "METHOD path" shape when http.route is absent.
var httpMethods = map[string]bool{
	"GET": true, "POST": true, "PUT": true, "DELETE": true,
	"PATCH": true, "HEAD": true, "OPTIONS": true, "TRACE": true,
	"CONNECT": true,
}

var (
	// SQL string literals: single- or double-quoted. Non-greedy so
	// adjacent literals don't merge. Handles the common '' / ""
	// escape by treating each quoted run independently — good enough
	// for shape normalisation (we're not parsing SQL).
	rxSQLSingleQuote = regexp.MustCompile(`'(?:[^']|'')*'`)
	rxSQLDoubleQuote = regexp.MustCompile(`"(?:[^"]|"")*"`)

	// Standalone numeric literals (word-bounded). 678, 3.14, 0x1f
	// handled loosely: a digit run optionally with a single dot.
	// \b keeps us off identifiers like "v2" or "col1".
	rxSQLNumber = regexp.MustCompile(`\b\d+(?:\.\d+)?\b`)

	// IN (?, ?, ?, …) → IN (?). Runs AFTER literal/number
	// replacement so the list is already all "?".
	rxSQLInList = regexp.MustCompile(`(?i)\bIN\s*\(\s*\?(?:\s*,\s*\?)*\s*\)`)

	// Collapse runs of whitespace (incl. newlines/tabs from
	// multi-line SQL) to a single space.
	rxWhitespace = regexp.MustCompile(`\s+`)
)

// NormalizeOperation folds a raw span into a stable, human-readable
// operation-shape group. Source priority: http.route >
// DB-statement-stripped > generic name-segment normalization.
//
//	name       — raw span name (operation name)
//	kind       — OTel span kind string (e.g. "server","client"); reserved
//	             for future shape decisions, currently unused.
//	httpMethod — http.method attr ("" for client spans without one)
//	httpRoute  — http.route attr (already templated by instrumentation,
//	             e.g. "/users/{id}")
//	dbSystem   — db.system attr ("" when not a DB span)
//	dbStatement— db.statement attr (raw SQL/command)
//
// Returns "" when nothing applies (no group).
func NormalizeOperation(name, kind, httpMethod, httpRoute, dbSystem, dbStatement string) string {
	_ = kind // reserved; shape decisions are attr-driven today.

	// (a) HTTP — route present: instrumentation already templated it.
	if httpRoute != "" {
		return cap200(strings.TrimSpace(httpMethod + " " + httpRoute))
	}

	// (a') HTTP — no route, but the raw name leads with a method
	// token. Normalize the path segments ourselves.
	if m, rest, ok := splitMethodPath(name); ok {
		return cap200(m + " " + normalizePath(rest))
	}

	// (b) DB — shape the statement.
	if dbSystem != "" {
		if dbStatement != "" {
			return cap200(normalizeDBStatement(dbSystem, dbStatement))
		}
		// No statement to shape — fall through to the generic name
		// path so a DB span still gets SOME group.
	}

	// (c) Generic fallback — normalize the raw name in place.
	if name != "" {
		return cap200(normalizeGenericName(name))
	}

	// (d) Nothing applies.
	return ""
}

// splitMethodPath returns (method, path, true) when name starts with
// a recognised HTTP method token followed by a space. Otherwise
// ("","",false).
func splitMethodPath(name string) (string, string, bool) {
	sp := strings.IndexByte(name, ' ')
	if sp <= 0 {
		return "", "", false
	}
	method := name[:sp]
	if !httpMethods[method] {
		return "", "", false
	}
	return method, name[sp+1:], true
}

// normalizePath splits a URL path on "/" and replaces each segment
// with ":id" when it is all-digits (any length — a number in a URL
// path is ~always an id) or LooksLikeOpaqueID. Rejoins on "/".
// A trailing "?query=string" is collapsed away.
func normalizePath(path string) string {
	// Drop a trailing query string entirely — query params are
	// per-request noise, never part of the operation shape.
	if q := strings.IndexByte(path, '?'); q >= 0 {
		path = path[:q]
	}
	segs := strings.Split(path, "/")
	for i, seg := range segs {
		if seg == "" {
			continue // preserves leading/trailing/double slashes
		}
		if isAllDigits(seg) || LooksLikeOpaqueID(seg) {
			segs[i] = ":id"
		}
	}
	return strings.Join(segs, "/")
}

// normalizeDBStatement reduces a raw SQL/command string to a stable
// shape: string literals → ?, numeric literals → ?, IN (?, ?, …) →
// IN (?), whitespace collapsed, trimmed. Prefixed with the db system
// for readability ("postgresql: SELECT …"). Capped by the caller.
func normalizeDBStatement(dbSystem, stmt string) string {
	s := rxSQLSingleQuote.ReplaceAllString(stmt, "?")
	s = rxSQLDoubleQuote.ReplaceAllString(s, "?")
	s = rxSQLNumber.ReplaceAllString(s, "?")
	s = rxSQLInList.ReplaceAllString(s, "IN (?)")
	s = rxWhitespace.ReplaceAllString(s, " ")
	s = strings.TrimSpace(s)
	if s == "" {
		return dbSystem
	}
	return dbSystem + ": " + s
}

// normalizeGenericName normalizes a non-HTTP, non-DB operation name.
// Splits on "/" and whitespace, replaces opaque-id-looking tokens
// with ":id", and rejoins preserving the original separators. We
// walk the string char by char to keep the exact run of separators
// (so "/api/v1/users/abc-uuid" stays slash-shaped, and
// "process order 12345" stays space-shaped).
func normalizeGenericName(name string) string {
	var b strings.Builder
	b.Grow(len(name))
	i := 0
	n := len(name)
	for i < n {
		c := name[i]
		if c == '/' || c == ' ' || c == '\t' {
			b.WriteByte(c)
			i++
			continue
		}
		// Consume one token up to the next separator.
		j := i
		for j < n && name[j] != '/' && name[j] != ' ' && name[j] != '\t' {
			j++
		}
		tok := name[i:j]
		if isAllDigits(tok) || LooksLikeOpaqueID(tok) {
			b.WriteString(":id")
		} else {
			b.WriteString(tok)
		}
		i = j
	}
	return b.String()
}

func cap200(s string) string {
	if len(s) > opGroupMaxLen {
		return s[:opGroupMaxLen]
	}
	return s
}
