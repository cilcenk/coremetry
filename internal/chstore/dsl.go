package chstore

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// ParseDSL turns a multi-line query DSL into a slice of FilterExpr.
//
// Syntax — one condition per line, AND-joined:
//
//	# comments allowed
//	duration > 500ms
//	service.name = "frontend"
//	http.status_code >= 500
//	resource.deployment.environment = production
//	span.peer.service = "payment-service"
//	name ~ checkout                   # LIKE substring
//	status_code in [error]            # IN [a, b, c]
//	exception.type exists
//
// Operators: =, !=, >, >=, <, <=, ~ (LIKE), !~ (NOT LIKE), in / not in,
// exists / not exists.
//
// Values may be quoted ("foo") or bare; durations (`500ms`, `1.5s`, `2m`)
// are normalised to milliseconds when used with the synthetic `duration` key.
func ParseDSL(src string) ([]FilterExpr, error) {
	var out []FilterExpr
	for i, raw := range strings.Split(src, "\n") {
		line := strings.TrimSpace(raw)
		if line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, "//") {
			continue
		}
		f, err := parseDSLLine(line)
		if err != nil {
			return nil, fmt.Errorf("line %d: %w", i+1, err)
		}
		out = append(out, f)
	}
	return out, nil
}

// Tightened to also accept the multi-word "not in" / "not exists" forms.
var dslLineRe = regexp.MustCompile(
	`^([\w.\-:]+)\s+(not\s+in|not\s+exists|exists|in|!=|>=|<=|=|>|<|!~|~)(?:\s+(.*))?$`)

func parseDSLLine(line string) (FilterExpr, error) {
	m := dslLineRe.FindStringSubmatch(line)
	if m == nil {
		return FilterExpr{}, fmt.Errorf("invalid syntax: %q", line)
	}
	key := m[1]
	op := normalizeDSLOp(strings.ToLower(strings.TrimSpace(m[2])))
	raw := strings.TrimSpace(m[3])

	// Synthetic helpers: `duration` aliases the well-known column with
	// duration-aware value parsing (`500ms` → 500).
	if key == "duration" {
		key = "duration_ms"
		raw = parseDurationToMs(raw)
	}

	switch op {
	case "EXISTS", "NOT EXISTS":
		return FilterExpr{Key: key, Op: op}, nil

	case "IN", "NOT IN":
		if !strings.HasPrefix(raw, "[") || !strings.HasSuffix(raw, "]") {
			return FilterExpr{}, fmt.Errorf("`%s` requires bracketed list, e.g. [a, b]", op)
		}
		inner := strings.TrimSpace(raw[1 : len(raw)-1])
		if inner == "" {
			return FilterExpr{}, fmt.Errorf("`%s` list cannot be empty", op)
		}
		var vals []string
		for _, v := range splitOutsideQuotes(inner, ',') {
			vals = append(vals, unquote(strings.TrimSpace(v)))
		}
		return FilterExpr{Key: key, Op: op, Values: vals}, nil

	default:
		if raw == "" {
			return FilterExpr{}, fmt.Errorf("missing value for %q", key)
		}
		return FilterExpr{Key: key, Op: op, Values: []string{unquote(raw)}}, nil
	}
}

func normalizeDSLOp(op string) string {
	switch op {
	case "~":          return "LIKE"
	case "!~":         return "NOT LIKE"
	case "in":         return "IN"
	case "not in":     return "NOT IN"
	case "exists":     return "EXISTS"
	case "not exists": return "NOT EXISTS"
	}
	return strings.ToUpper(op)
}

func unquote(s string) string {
	if len(s) >= 2 && (s[0] == '"' || s[0] == '\'') && s[len(s)-1] == s[0] {
		return s[1 : len(s)-1]
	}
	return s
}

// parseDurationToMs accepts "500ms", "1.5s", "2m", or a plain number (treated
// as ms). Anything unparseable comes back unchanged so the caller can reject it.
func parseDurationToMs(s string) string {
	if s == "" { return s }
	if d, err := time.ParseDuration(s); err == nil {
		return strconv.FormatFloat(float64(d)/float64(time.Millisecond), 'f', -1, 64)
	}
	return s
}

// splitOutsideQuotes is like strings.Split but ignores the separator when
// inside double or single quotes — needed for IN lists with quoted values.
func splitOutsideQuotes(s string, sep byte) []string {
	var out []string
	var inQ byte
	last := 0
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case inQ != 0:
			if c == inQ { inQ = 0 }
		case c == '"' || c == '\'':
			inQ = c
		case c == sep:
			out = append(out, s[last:i])
			last = i + 1
		}
	}
	out = append(out, s[last:])
	return out
}
