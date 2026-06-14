package chstore

import "testing"

// v0.8.122 — regression guard for /api/service-map zero error attribution.
//
// Operator-reported: on the /service-map preview every node rendered
// errorRate=0 and every edge errorCount=0 over a 24h window while the
// underlying services ran 1-6% errors, so the red-edge / amber-node
// health rendering was dead. Root cause: getServiceMapAt classified a
// span as an error with
//
//	statusCode == "STATUS_CODE_ERROR" || statusCode == "ERROR" || statusCode == "Error"
//
// but the ingest path (internal/otlp/convert.go) maps the OTLP
// STATUS_CODE_ERROR enum to the lowercase token "error" BEFORE the CH
// write, and every other chstore query compares status_code = 'error'.
// The uppercase OTLP enum names never appear in the column, so the
// predicate matched 0 of 255 real error spans in the sampled traces
// (live-CH ground truth) and all error counters stayed 0.
//
// The fix routes the classification through the pure spanStatusIsError
// helper; this table pins the lowercase token (and case variants) as
// the error tokens and the OTLP enum names + non-error tokens as
// non-errors, so the regression can't silently return.
func TestSpanStatusIsError(t *testing.T) {
	cases := []struct {
		name       string
		statusCode string
		want       bool
	}{
		// The token the ingest path actually writes — the only true
		// error value. This is the case the original bug missed.
		{"lowercase error (ingest value)", "error", true},
		// Case-insensitive on the real token, so a future ingest
		// casing tweak doesn't silently re-break attribution.
		{"uppercase ERROR", "ERROR", true},
		{"title-case Error", "Error", true},
		{"mixed-case eRRoR", "eRRoR", true},

		// The OTLP enum name is NOT what lands in the column. The old
		// predicate compared against these and matched nothing.
		{"OTLP enum name never stored", "STATUS_CODE_ERROR", false},

		// Non-error tokens the column actually carries.
		{"ok", "ok", false},
		{"unset", "unset", false},
		{"empty", "", false},
		// "OK" prefix must not accidentally substring-match "error".
		{"OK upper", "OK", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := spanStatusIsError(c.statusCode); got != c.want {
				t.Fatalf("spanStatusIsError(%q) = %v, want %v", c.statusCode, got, c.want)
			}
		})
	}
}
