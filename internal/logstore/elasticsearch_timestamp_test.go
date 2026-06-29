package logstore

import (
	"encoding/json"
	"testing"
	"time"
)

// v0.8.163 — operator-reported: on an external ES the Logs LIST rendered
// blank while the bar histogram showed data. Root cause: the histogram is
// aggregated server-side by ES (format-agnostic via the field's `date`
// mapping), but mapHit parsed the timestamp CLIENT-side with a strict
// RFC3339Nano-only parse. A pipeline that ships epoch_millis (decodes to
// float64) or a log4j-style "2006-01-02 15:04:05" string failed the parse,
// so Timestamp fell to 0, the rows landed at 1970, and the time-windowed
// list dropped them — even though the same docs matched the same
// buildQuery range filter the histogram used.
//
// parseLogTimestampNs must normalise every shape a real shipper emits.
func TestParseLogTimestampNs_ShipperShapes(t *testing.T) {
	// Anchor: 2026-06-15T10:11:19Z.
	want := time.Date(2026, 6, 15, 10, 11, 19, 0, time.UTC).UnixNano()
	sec := want / 1_000_000_000
	ms := want / 1_000_000
	us := want / 1_000
	ns := want

	cases := []struct {
		name string
		in   any
		want int64
	}{
		{"rfc3339", "2026-06-15T10:11:19Z", want},
		{"rfc3339nano", "2026-06-15T10:11:19.000000000Z", want},
		{"log4j space form", "2026-06-15 10:11:19", want},
		// v0.8.229 — logback / log4j2 default `yyyy-MM-dd HH:mm:ss,SSS`
		// (comma millis). The common Java banking-stack shape; was the
		// residual gap behind "histogram bars but empty log list".
		{"logback comma-millis", "2026-06-15 10:11:19,000", want},
		{"logback comma-millis nonzero", "2026-06-15 10:11:19,123", want + 123*int64(time.Millisecond)},
		{"space form numeric offset", "2026-06-15 13:11:19+03:00", want},
		{"epoch millis float64 (ES default)", float64(ms), want},
		{"epoch millis int64", int64(ms), want},
		{"epoch millis numeric string", itoa(ms), want},
		{"garbage string", "not-a-timestamp", 0},
		{"epoch seconds float64", float64(sec), want},
		{"epoch seconds string", itoa(sec), want},
		{"epoch micros", us, want},
		{"epoch nanos", ns, want},
		{"json.Number millis", json.Number(itoa(ms)), want},
		{"empty string", "", 0},
		{"zero", int64(0), 0},
		{"nil", nil, 0},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := parseLogTimestampNs(c.in)
			if got != c.want {
				t.Fatalf("parseLogTimestampNs(%v) = %d, want %d", c.in, got, c.want)
			}
		})
	}
}

// epochToNs magnitude detection must keep realistic seconds/millis/micros/
// nanos in their own band — the boundary that separates them is what makes
// the unit-guess unambiguous for any timestamp this decade.
func TestEpochToNs_MagnitudeBands(t *testing.T) {
	want := time.Date(2026, 6, 15, 10, 11, 19, 0, time.UTC).UnixNano()
	for _, n := range []int64{want / 1_000_000_000, want / 1_000_000, want / 1_000, want} {
		if got := epochToNs(n); got != want {
			t.Fatalf("epochToNs(%d) = %d, want %d", n, got, want)
		}
	}
	if epochToNs(0) != 0 || epochToNs(-5) != 0 {
		t.Fatal("non-positive epoch must map to 0, not a spurious 1970 timestamp")
	}
}

// v0.8.163 — the ES CountPatterns sample-timestamp extraction (which
// stamps AnomalyEvent.LastSeen for every recorded log-pattern anomaly)
// must use parseLogTimestampNs on the RAW _source value, not the old
// strict `src[...].(string)` + RFC3339Nano parse. On an epoch_millis
// index ES decodes the value to float64, so the string assertion failed
// and LastSeen pinned to 1970. This guards the float64 path the old code
// dropped.
func TestCountPatternsSampleTimestamp_RobustOnFloat64(t *testing.T) {
	want := time.Date(2026, 6, 15, 10, 11, 19, 0, time.UTC).UnixNano()
	// As ES decodes an epoch_millis _source field: a JSON number → float64.
	src := map[string]any{"@timestamp": float64(want / 1_000_000)}
	if _, ok := src["@timestamp"].(string); ok {
		t.Fatal("precondition: epoch_millis must decode to float64, not string")
	}
	if got := parseLogTimestampNs(src["@timestamp"]); got != want {
		t.Fatalf("CountPatterns float64 epoch_millis sample → %d, want %d "+
			"(regressed to a strict string parse?)", got, want)
	}
}

// v0.8.229 — operator-reported (read-only api-key on app-*): the /logs
// histogram rendered but the log LIST was empty. Root cause class: the list
// parsed each hit's _source @timestamp string CLIENT-side, so a non-RFC shape
// (logback comma-millis, custom format) zeroed the row → dropped from the
// time-windowed list, while the server-side date_histogram (format-agnostic
// via the field's `date` mapping) still showed bars. The fix asks ES for the
// timestamp pre-parsed to epoch_millis via docvalue_fields — the SAME engine
// the histogram uses — and mapHit prefers it over the raw _source value.
//
// This guards: (1) docValueTimestampNs extracts epoch_millis from the
// docvalue array shape, and (2) mapHit prefers the docvalue timestamp even
// when the _source string is unparseable — so the row is no longer zeroed.
func TestDocValueTimestampNs_And_MapHitPrecedence(t *testing.T) {
	want := time.Date(2026, 6, 15, 10, 11, 19, 0, time.UTC).UnixNano()
	ms := want / 1_000_000

	t.Run("docvalue array epoch_millis", func(t *testing.T) {
		dv := map[string]any{"@timestamp": []any{itoa(ms)}}
		if got := docValueTimestampNs(dv, "@timestamp"); got != want {
			t.Fatalf("docValueTimestampNs = %d, want %d", got, want)
		}
		// Absent field / nil map → 0 so mapHit falls back to _source.
		if got := docValueTimestampNs(dv, "missing"); got != 0 {
			t.Fatalf("missing docvalue field should be 0, got %d", got)
		}
		if got := docValueTimestampNs(nil, "@timestamp"); got != 0 {
			t.Fatalf("nil docvalue map should be 0, got %d", got)
		}
	})

	t.Run("mapHit prefers docvalue over an unparseable _source string", func(t *testing.T) {
		cfg := ESConfig{}
		cfg.defaults()
		s := &ESStore{fields: cfg.Fields, cfg: cfg}
		// _source carries a shape that (pre-tsLayouts-widening) would not
		// parse; docvalue carries the canonical epoch_millis ES derived from
		// the date mapping. The row MUST take the docvalue time, not 0.
		src := map[string]any{"@timestamp": "definitely not a date"}
		dv := map[string]any{"@timestamp": []any{itoa(ms)}}
		rec := s.mapHit("doc-1", src, dv)
		if rec.Timestamp != want {
			t.Fatalf("mapHit Timestamp = %d, want %d (docvalue must win over a bad _source string)", rec.Timestamp, want)
		}
	})

	t.Run("mapHit still works with no docvalue (falls back to _source)", func(t *testing.T) {
		cfg := ESConfig{}
		cfg.defaults()
		s := &ESStore{fields: cfg.Fields, cfg: cfg}
		src := map[string]any{"@timestamp": "2026-06-15 10:11:19,000"} // logback comma-millis
		rec := s.mapHit("doc-2", src, nil)
		if rec.Timestamp != want {
			t.Fatalf("mapHit no-docvalue fallback = %d, want %d", rec.Timestamp, want)
		}
	})
}

func itoa(n int64) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var b [20]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		b[i] = '-'
	}
	return string(b[i:])
}
