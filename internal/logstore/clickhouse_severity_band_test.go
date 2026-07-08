package logstore

import (
	"strings"
	"testing"
)

// v0.8.377 — Operator-reported: the log severity histogram (/logs +
// service Logs tab, GET /api/logs/timeseries?groupBy=severity) showed
// wrong band counts. On the CH backend the old groupExpr emitted raw
// severity_text verbatim PLUS toString(severity_num) numeric strings;
// the frontend prefix-matched text only, so SDKs emitting only
// severity_number rendered their ERRORS in the DEBUG band.
//
// chSeverityBandExpr now bands server-side into the canonical
// ERROR/WARN/INFO/DEBUG/TRACE/OTHER names. These shape assertions pin
// the expr: text prefixes (case-insensitive via upper()), text
// precedence over the numeric fallback, and the exact OTel
// severity_number ranges — in lockstep with the ES filters-agg path
// (severityBands) and the frontend severityBandOf.

func TestCHSeverityBandExpr_Shape(t *testing.T) {
	expr := chSeverityBandExpr

	// Case-insensitivity: every text probe goes through upper().
	if strings.Contains(expr, "startsWith(severity_text") {
		t.Errorf("text prefix probe bypasses upper() — banding would be case-sensitive:\n%s", expr)
	}

	// Text prefix → band pairs (mirrors ES wantBandPrefixes; upper-cased
	// because the CH expr compares against upper(severity_text)).
	textConds := []struct{ prefix, band string }{
		{"'FATAL'", "'ERROR'"},
		{"'ERR'", "'ERROR'"}, // ERR catches err / error / error:
		{"'WARN'", "'WARN'"},
		{"'INFO'", "'INFO'"},
		{"'DEBUG'", "'DEBUG'"},
		{"'TRACE'", "'TRACE'"},
	}
	for _, tc := range textConds {
		probe := "startsWith(upper(severity_text), " + tc.prefix + ")"
		i := strings.Index(expr, probe)
		if i < 0 {
			t.Errorf("missing text probe %s", probe)
			continue
		}
		// The band literal must follow its condition before the next comma
		// pair; cheap check: band appears after the probe.
		if j := strings.Index(expr[i:], tc.band); j < 0 {
			t.Errorf("probe %s does not map to band %s", probe, tc.band)
		}
	}

	// Numeric OTel ranges (text-empty fallback).
	numConds := []string{
		"severity_num BETWEEN 17 AND 24, 'ERROR'",
		"severity_num BETWEEN 13 AND 16, 'WARN'",
		"severity_num BETWEEN 9 AND 12, 'INFO'",
		"severity_num BETWEEN 5 AND 8, 'DEBUG'",
		"severity_num BETWEEN 1 AND 4, 'TRACE'",
	}
	for _, c := range numConds {
		if !strings.Contains(expr, c) {
			t.Errorf("missing numeric band condition %q", c)
		}
	}

	// Text PRECEDENCE: any non-empty unrecognised text must hit OTHER
	// BEFORE the numeric fallback can reclassify it — i.e. the
	// severity_text != '' guard sits before the first severity_num probe.
	guard := strings.Index(expr, "severity_text != '', 'OTHER'")
	firstNum := strings.Index(expr, "severity_num BETWEEN")
	if guard < 0 {
		t.Fatalf("missing text-nonempty → OTHER guard:\n%s", expr)
	}
	if firstNum < 0 {
		t.Fatalf("missing numeric fallback:\n%s", expr)
	}
	if guard > firstNum {
		t.Errorf("text-nonempty guard at %d comes AFTER first numeric probe at %d — numeric ranges would reclassify unrecognised text", guard, firstNum)
	}

	// Default arm: 0 / >24 / unset severity lands in OTHER.
	if !strings.HasSuffix(strings.TrimSpace(expr), "'OTHER')") {
		t.Errorf("expr default arm is not 'OTHER':\n%s", expr)
	}

	// It is a single multiIf (odd arg count = pairs + default) — a
	// syntactically broken expr would take the whole histogram down.
	if !strings.HasPrefix(strings.TrimSpace(expr), "multiIf(") {
		t.Errorf("expr is not a multiIf:\n%s", expr)
	}
}
