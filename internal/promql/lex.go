package promql

import (
	"fmt"
	"strings"
)

// lex.go — PromQL tokenizer. Whole-input scan → []token (small queries; a
// slice is simpler to drive a recursive-descent parser than a streaming lexer
// and trivial to unit-test). Keywords (and/or/unless/by/without/on/ignoring/
// group_left/group_right/offset/bool/start/end/inf/nan) lex as tIdentifier and
// are recognized by value at parse time — the standard context-sensitive
// approach (e.g. `on` is a keyword after a binary op but a legal metric name).

type tokenKind int

const (
	tEOF tokenKind = iota
	tError
	tIdentifier // metric names, label names, function/keyword words
	tNumber
	tString
	tDuration
	tLParen
	tRParen
	tLBrace
	tRBrace
	tLBracket
	tRBracket
	tComma
	tColon
	tAdd // +
	tSub // -
	tMul // *
	tDiv // /
	tMod // %
	tPow // ^
	tEQL // ==
	tNEQ // !=
	tLSS // <
	tLTE // <=
	tGTR // >
	tGTE // >=
	tEQLRegex // =~
	tNEQRegex // !~
	tAssign   // =
	tAt       // @
)

type token struct {
	kind tokenKind
	val  string
	pos  int // byte offset in the source, for error underlining
}

func isDigit(c byte) bool { return c >= '0' && c <= '9' }

// isAlpha — identifier START chars. Coremetry is OTel-native, so metric names
// and label names are DOTTED (http.server.duration, service.name); the dot is a
// continuation char (below), never a start char. ':' (Prometheus recording-rule
// names) is intentionally excluded — Coremetry has no recording rules, and
// keeping ':' out lets the subquery separator `[1h:1m]` lex cleanly.
func isAlpha(c byte) bool {
	return c == '_' || (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z')
}

// isAlphaNum — identifier CONTINUATION chars: letters, digits, '_', and '.' so
// dotted OTel metric/attribute names are a single token.
func isAlphaNum(c byte) bool { return isAlpha(c) || isDigit(c) || c == '.' }

// durationUnits in longest-first order so "ms" wins over "m".
var durationUnits = []string{"ms", "s", "m", "h", "d", "w", "y"}

// lex tokenizes the whole input. On a lexical error it returns the tokens
// scanned so far plus a non-nil error (with the byte position).
func lex(input string) ([]token, error) {
	var toks []token
	i, n := 0, len(input)
	emit := func(k tokenKind, val string, pos int) { toks = append(toks, token{k, val, pos}) }

	for i < n {
		c := input[i]
		switch {
		case c == ' ' || c == '\t' || c == '\n' || c == '\r':
			i++
		case c == '#': // comment to end of line
			for i < n && input[i] != '\n' {
				i++
			}
		case c == '(':
			emit(tLParen, "(", i)
			i++
		case c == ')':
			emit(tRParen, ")", i)
			i++
		case c == '{':
			emit(tLBrace, "{", i)
			i++
		case c == '}':
			emit(tRBrace, "}", i)
			i++
		case c == '[':
			emit(tLBracket, "[", i)
			i++
		case c == ']':
			emit(tRBracket, "]", i)
			i++
		case c == ',':
			emit(tComma, ",", i)
			i++
		case c == ':':
			emit(tColon, ":", i)
			i++
		case c == '@':
			emit(tAt, "@", i)
			i++
		case c == '+':
			emit(tAdd, "+", i)
			i++
		case c == '-':
			emit(tSub, "-", i)
			i++
		case c == '*':
			emit(tMul, "*", i)
			i++
		case c == '/':
			emit(tDiv, "/", i)
			i++
		case c == '%':
			emit(tMod, "%", i)
			i++
		case c == '^':
			emit(tPow, "^", i)
			i++
		case c == '=':
			if i+1 < n && input[i+1] == '=' {
				emit(tEQL, "==", i)
				i += 2
			} else if i+1 < n && input[i+1] == '~' {
				emit(tEQLRegex, "=~", i)
				i += 2
			} else {
				emit(tAssign, "=", i)
				i++
			}
		case c == '!':
			if i+1 < n && input[i+1] == '=' {
				emit(tNEQ, "!=", i)
				i += 2
			} else if i+1 < n && input[i+1] == '~' {
				emit(tNEQRegex, "!~", i)
				i += 2
			} else {
				return toks, fmt.Errorf("unexpected '!' at position %d (did you mean != or !~?)", i)
			}
		case c == '<':
			if i+1 < n && input[i+1] == '=' {
				emit(tLTE, "<=", i)
				i += 2
			} else {
				emit(tLSS, "<", i)
				i++
			}
		case c == '>':
			if i+1 < n && input[i+1] == '=' {
				emit(tGTE, ">=", i)
				i += 2
			} else {
				emit(tGTR, ">", i)
				i++
			}
		case c == '"' || c == '\'' || c == '`':
			s, ni, err := scanString(input, i)
			if err != nil {
				return toks, err
			}
			emit(tString, s, i)
			i = ni
		case isDigit(c) || (c == '.' && i+1 < n && isDigit(input[i+1])):
			// A number, unless it is a bare duration like 5m / 1h30m.
			if dur, ni, ok := scanDuration(input, i); ok {
				emit(tDuration, dur, i)
				i = ni
			} else {
				num, ni := scanNumber(input, i)
				emit(tNumber, num, i)
				i = ni
			}
		case isAlpha(c):
			start := i
			for i < n && isAlphaNum(input[i]) {
				i++
			}
			word := input[start:i]
			// Inf / NaN literals lex as numbers so the parser treats them as
			// scalar constants (case-insensitive, Prometheus-compatible).
			lw := strings.ToLower(word)
			if lw == "inf" || lw == "nan" || lw == "+inf" || lw == "-inf" {
				emit(tNumber, word, start)
			} else {
				emit(tIdentifier, word, start)
			}
		default:
			return toks, fmt.Errorf("unexpected character %q at position %d", string(c), i)
		}
	}
	emit(tEOF, "", i)
	return toks, nil
}

// scanDuration recognizes a PromQL duration starting at i: one or more
// <digits><unit> groups, e.g. "5m", "1h30m", "500ms". Returns ok=false when
// the run at i is not a valid duration (so the caller lexes a number instead).
func scanDuration(input string, i int) (string, int, bool) {
	n := len(input)
	start := i
	groups := 0
	for i < n && isDigit(input[i]) {
		// consume digits
		j := i
		for j < n && isDigit(input[j]) {
			j++
		}
		// must be followed by a unit
		unit := ""
		for _, u := range durationUnits {
			if strings.HasPrefix(input[j:], u) {
				// Guard: "ms" must not be read as "m" then a stray "s"; the
				// longest-first order handles that. Also the char AFTER the
				// unit must not continue the unit letters (e.g. "5made" ≠ dur).
				unit = u
				break
			}
		}
		if unit == "" {
			return "", start, false
		}
		i = j + len(unit)
		groups++
	}
	if groups == 0 || i == start {
		return "", start, false
	}
	// The token must end at a non-identifier boundary — "5m2" or "5motel" is
	// not a duration (avoids swallowing a metric name that starts with digits…
	// which PromQL forbids anyway, but be strict).
	if i < n && isAlphaNum(input[i]) {
		return "", start, false
	}
	return input[start:i], i, true
}

// scanNumber scans a numeric literal: decimal, float, exponent, or 0x hex.
func scanNumber(input string, i int) (string, int) {
	n := len(input)
	start := i
	if input[i] == '0' && i+1 < n && (input[i+1] == 'x' || input[i+1] == 'X') {
		i += 2
		for i < n && isHex(input[i]) {
			i++
		}
		return input[start:i], i
	}
	for i < n && isDigit(input[i]) {
		i++
	}
	if i < n && input[i] == '.' {
		i++
		for i < n && isDigit(input[i]) {
			i++
		}
	}
	if i < n && (input[i] == 'e' || input[i] == 'E') {
		i++
		if i < n && (input[i] == '+' || input[i] == '-') {
			i++
		}
		for i < n && isDigit(input[i]) {
			i++
		}
	}
	return input[start:i], i
}

func isHex(c byte) bool {
	return isDigit(c) || (c >= 'a' && c <= 'f') || (c >= 'A' && c <= 'F')
}

// scanString scans a quoted string. Double/single quotes honor backslash
// escapes; backticks are raw (no escapes), matching PromQL.
func scanString(input string, i int) (string, int, error) {
	n := len(input)
	quote := input[i]
	i++ // past opening quote
	var b strings.Builder
	if quote == '`' {
		for i < n && input[i] != '`' {
			b.WriteByte(input[i])
			i++
		}
		if i >= n {
			return "", i, fmt.Errorf("unterminated raw string")
		}
		return b.String(), i + 1, nil
	}
	for i < n {
		c := input[i]
		if c == quote {
			return b.String(), i + 1, nil
		}
		if c == '\\' && i+1 < n {
			i++
			switch input[i] {
			case 'n':
				b.WriteByte('\n')
			case 't':
				b.WriteByte('\t')
			case 'r':
				b.WriteByte('\r')
			case '\\':
				b.WriteByte('\\')
			case '"':
				b.WriteByte('"')
			case '\'':
				b.WriteByte('\'')
			default:
				b.WriteByte('\\')
				b.WriteByte(input[i])
			}
			i++
			continue
		}
		b.WriteByte(c)
		i++
	}
	return "", i, fmt.Errorf("unterminated string")
}
