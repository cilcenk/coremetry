package chstore

import (
	"strings"
	"testing"
)

// v0.8.356 — regression: /api/endpoints always returned top-N by
// calls and the frontend client-sorted that page, so "top by p95"
// was really "top-N-by-calls, reordered" — a slow-but-mid-traffic
// endpoint below the calls cutoff never surfaced. Sort moves
// server-side (ORDER BY on the merged aggregates, BEFORE the
// LIMIT) via the same whitelist-mapper pattern as
// exceptionGroupsOrderBy (v0.8.318).
//
// Contract of endpointsOrderBy: whitelist mapping of the UI sort
// ids onto ORDER BY clauses — caller input NEVER reaches the SQL
// string — with a deterministic (service_name, path) tiebreak so
// equal-value rows keep a stable order across refetches. Unknown
// ids/dirs fall back to the historical calls DESC. The aliases it
// emits exist in BOTH the MV read and the raw cluster-filter
// fallback, so one mapper serves both paths.
func TestEndpointsOrderBy(t *testing.T) {
	const tiebreak = ", service_name ASC, path ASC"
	const fallback = "ORDER BY calls DESC" + tiebreak
	cases := []struct {
		name string
		sort string
		dir  string
		want string
	}{
		{"default", "", "", fallback},
		{"calls desc", "calls", "desc", "ORDER BY calls DESC" + tiebreak},
		{"calls asc", "calls", "asc", "ORDER BY calls ASC" + tiebreak},
		{"errors desc", "errors", "desc", "ORDER BY errors DESC" + tiebreak},
		{"errorRate desc", "errorRate", "desc", "ORDER BY error_rate DESC" + tiebreak},
		{"avgMs asc", "avgMs", "asc", "ORDER BY avg_ms ASC" + tiebreak},
		{"p50Ms desc", "p50Ms", "desc", "ORDER BY p50_ms DESC" + tiebreak},
		{"p95Ms desc", "p95Ms", "desc", "ORDER BY p95_ms DESC" + tiebreak},
		{"p99Ms asc", "p99Ms", "asc", "ORDER BY p99_ms ASC" + tiebreak},
		// reqPerMin = calls / constant window divisor — identical
		// permutation to calls, so the mapper reuses the calls column.
		{"reqPerMin desc maps to calls", "reqPerMin", "desc", "ORDER BY calls DESC" + tiebreak},
		// Composite impact matches the frontend impactOf() formula.
		{"impact desc", "impact", "desc",
			"ORDER BY calls * p99_ms * (1 + error_rate / 100.0) DESC" + tiebreak},
		{"service asc", "service", "asc", "ORDER BY service_name ASC" + tiebreak},
		{"path asc", "path", "asc", "ORDER BY path ASC" + tiebreak},
		// Injection probes — hostile input must fall back, never
		// concatenate.
		{"injection via sort", "calls; DROP TABLE spans--", "desc", fallback},
		{"injection via sort quotes", "calls' OR '1'='1", "asc", fallback},
		{"unknown id falls back", "nope", "asc", fallback},
		// dir is a strict two-value switch: anything not exactly "asc"
		// (including injection payloads) becomes DESC.
		{"injection via dir", "calls", "asc; DROP TABLE spans", "ORDER BY calls DESC" + tiebreak},
		{"unknown dir falls back to desc", "p95Ms", "sideways", "ORDER BY p95_ms DESC" + tiebreak},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := endpointsOrderBy(c.sort, c.dir)
			if got != c.want {
				t.Fatalf("endpointsOrderBy(%q,%q) = %q, want %q", c.sort, c.dir, got, c.want)
			}
			// Belt-and-braces: no caller byte may survive into the
			// clause beyond the whitelisted expressions.
			if strings.Contains(got, "DROP") || strings.Contains(got, ";") {
				t.Fatalf("caller input leaked into ORDER BY: %q", got)
			}
		})
	}
}

// v0.8.356 — regression: the "Group by shape" toggle 500'd with
// "unsupported query parameter type" on EVERY request. Root cause:
// opSigWrap inlined the ID-collapsing regexes into the SQL text, and
// clickhouse-go v2 switches a statement into server-side parameter
// mode whenever the query matches `{.+:.+}` (query_parameters.go) —
// the `[0-9a-fA-F]{8}` quantifier braces plus the ':id' literal
// satisfied it, so plain positional args were rejected wholesale.
// Fix: the patterns bind as ? args (opSigArgs); the SQL fragment must
// stay brace-free forever.
func TestOpSigWrapBindSafety(t *testing.T) {
	sql := opSigWrap("http_route")
	if strings.ContainsAny(sql, "{}") {
		t.Fatalf("opSigWrap output contains braces — clickhouse-go will flip the statement into server-side parameter mode and every positional arg fails: %q", sql)
	}
	if got := strings.Count(sql, "?"); got != len(opSigArgs()) {
		t.Fatalf("opSigWrap has %d placeholders but opSigArgs supplies %d values — call sites splice these positionally", got, len(opSigArgs()))
	}
	// Placeholder order contract: UUID first, then long-hex, then
	// numeric — the same rule ordering the alignment test pins against
	// templater.NormalizeOperation.
	args := opSigArgs()
	if args[0] != OpSigReUUID || args[1] != OpSigReHex || args[2] != OpSigReNum {
		t.Fatalf("opSigArgs order changed — must stay UUID, hex, num: %v", args)
	}
}
