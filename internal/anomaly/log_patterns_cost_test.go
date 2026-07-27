package anomaly

import (
	"regexp"
	"strings"
	"testing"
)

// v0.9.316 — the operator's own Grafana board ("airX - Genel - OCP
// Watcher Errors Metrics") is the list of shapes they actually watch.
// Adding them came with an explicit constraint: "elastice çok sorgu
// yükü oluşturma sakın."
//
// That constraint is arithmetic, not a feeling. The recorder ticks
// every minute (config default AnomalyRecordInterval = 1m) and
// CountPatterns batches ONE _msearch sub-query per pattern, so a
// pattern is 1,440 ES sub-queries per day, forever. Pasting the board's
// 24 rows in would have taken 19 patterns to 43 — a 2.3x standing
// increase on a cluster the operator already asked us to go easy on.
//
// This test is the budget. It fails when the list grows past what was
// justified, which is the only way a per-minute cost stays a decision
// instead of a drift.
const patternBudget = 26

func TestPatternCountStaysInBudget(t *testing.T) {
	if n := len(patterns); n > patternBudget {
		t.Fatalf("%d patterns × 1440 ticks = %d ES sub-queries/day, over the %d budget.\n"+
			"Before raising it: can the new shape be folded into an existing regex "+
			"(same sub-query, zero extra cost), or is it a low-volume tail that a "+
			"per-minute detector should not carry at all?",
			n, n*1440, patternBudget)
	}
}

// Every pattern MUST carry tokens. Tokens drive the tokenbf_v1 prefilter
// on the CH path — without them a naked match() scans every granule at
// billion-logs/day — and they build the drill-down link on both paths.
// A tokenless pattern is the single most expensive thing that can be
// added to this file.
func TestEveryPatternHasTokens(t *testing.T) {
	for _, p := range patterns {
		if len(p.Tokens) == 0 {
			t.Errorf("%q has no tokens — its regex would scan every granule", p.Name)
		}
		for _, tok := range p.Tokens {
			if tok != strings.ToLower(tok) {
				t.Errorf("%q token %q is not lowercase; the prefilter compares lowercased", p.Name, tok)
			}
		}
	}
}

// Every regex must compile, and every token must be a substring the
// body genuinely contains when the regex matches — otherwise the
// prefilter prunes granules that DO match and the pattern silently
// under-counts, which is worse than not having it.
func TestPatternRegexesCompile(t *testing.T) {
	for _, p := range patterns {
		if _, err := regexp.Compile(p.Regex); err != nil {
			t.Errorf("%q: regex does not compile: %v", p.Name, err)
		}
	}
}

// The shapes from the operator's board that we claimed to cover must
// actually match. This is the difference between "we added a pattern"
// and "the pattern catches the thing it was added for".
func TestOperatorBoardShapesAreMatched(t *testing.T) {
	// Real message fragments, taken from the board's own series names.
	cases := []struct{ sample, wantPattern string }{
		{"ORA-03113: database connection closed by peer", "Oracle errors (ORA-)"},
		{"java.lang.NullPointerException: Cannot invoke", "Null pointer"},
		{"com.akbank.bsa.core.exception.ExternalSystemException: Request not allowed for URI :", "External system rejected"},
		{"javax.naming.NameNotFoundException", "JNDI / lookup failure"},
		{"Service endpoint not found", "JNDI / lookup failure"},
		{"Queue connection definition not found with name", "JNDI / lookup failure"},
		{"Service quota warning", "Service quota"},
		{"Service quota error", "Service quota"},
		{"Connection refused", "Connection refused"},
		{"java.lang.NoClassDefFoundError: Could not initialize class", "Class init / load failure"},
		{"java.lang.ExceptionInInitializerError: Exception org.springframework.beans.factory", "Class init / load failure"},
		{"Caused by: java.lang.ClassNotFoundException", "Class init / load failure"},
		{"SqlException", "SQL exception"},
		// These two were NOT matching before v0.9.316 — the whole point
		// of widening the existing regexes instead of adding patterns.
		{"java.util.concurrent.TimeoutException", "Read / write timeout"},
		{"Unable to get managed connection", "JDBC pool exhausted"},
		{"IJ000655: No managed connections available within configured blocking timeout", "JDBC pool exhausted"},
		{"MQJCA1011: Failed to allocate a JMS connection", "JDBC pool exhausted"},
	}

	byName := map[string]logPattern{}
	for _, p := range patterns {
		byName[p.Name] = p
	}
	for _, c := range cases {
		p, ok := byName[c.wantPattern]
		if !ok {
			t.Errorf("pattern %q does not exist", c.wantPattern)
			continue
		}
		re, err := regexp.Compile(p.Regex)
		if err != nil {
			continue // reported by TestPatternRegexesCompile
		}
		if !re.MatchString(c.sample) {
			t.Errorf("%q does NOT match %q — the pattern was added for exactly this shape",
				c.wantPattern, c.sample)
		}
		// And the token prefilter must not prune it away: at least one
		// token has to appear in the lowercased body, or the pattern
		// under-counts silently.
		lower := strings.ToLower(c.sample)
		hit := false
		for _, tok := range p.Tokens {
			if strings.Contains(lower, tok) {
				hit = true
				break
			}
		}
		if !hit {
			t.Errorf("%q: no token of %v appears in %q — the prefilter would prune a granule that MATCHES",
				c.wantPattern, p.Tokens, c.sample)
		}
	}
}
