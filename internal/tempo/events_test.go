package tempo

import (
	"encoding/json"
	"testing"
)

// v0.9.859 — Operator-reported: a trace resolved through the TEMPO FALLBACK
// showed no "Exceptions" section on a red DB-error span (unique constraint),
// while the same shape of span served from ClickHouse showed it.
//
// Root cause: otlpSpan had no `events` field at all, so parseOTLPTrace never
// read span events and chstore.SpanRow.Events stayed empty. The frontend
// builds the Exceptions section by filtering events named "exception", so the
// section simply never rendered. Nothing said "events unavailable" — the span
// looked like it had no exception, which is the expensive failure: the
// operator concludes the error had no stack.
//
// These tests pin the parse, both envelope shapes, both timestamp encodings,
// and — critically — that a payload WITHOUT events leaves Events nil, matching
// the ClickHouse path (which only fills the field when the stored column
// decodes). "Which store answered" must not be a visible behaviour difference.

// decodeEvents asserts on the WIRE shape rather than the Go value: SpanRow.Events
// is `interface{}`, and what the operator's browser receives is whatever it
// marshals to. Round-tripping through JSON is therefore the honest assertion —
// it catches a right-looking Go value that serialises wrong.
func decodeEvents(t *testing.T, v interface{}) []tempoSpanEvent {
	t.Helper()
	if v == nil {
		return nil
	}
	raw, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("Events does not marshal: %v", err)
	}
	var out []tempoSpanEvent
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("Events is not a SpanEvent array on the wire: %v (raw=%s)", err, raw)
	}
	return out
}

// exceptionPayload builds a one-span OTLP trace carrying one exception event.
// `envelope` selects the batches/resourceSpans wrapper; `timeLit` is the raw
// JSON literal for timeUnixNano so both the string and number encodings can
// be exercised.
func exceptionPayload(envelope, timeLit string) string {
	return `{"` + envelope + `":[{
	  "resource":{"attributes":[{"key":"service.name","value":{"stringValue":"orders"}}]},
	  "scopeSpans":[{"scope":{"name":"io.opentelemetry.jdbc"},"spans":[{
	    "traceId":"0af7651916cd43dd8448eb211c80319c",
	    "spanId":"b7ad6b7169203331",
	    "name":"INSERT orders",
	    "startTimeUnixNano":"1700000000000000000",
	    "endTimeUnixNano":"1700000000500000000",
	    "attributes":[{"key":"db.system","value":{"stringValue":"oracle"}}],
	    "events":[{
	      "timeUnixNano":` + timeLit + `,
	      "name":"exception",
	      "attributes":[
	        {"key":"exception.type","value":{"stringValue":"java.sql.SQLIntegrityConstraintViolationException"}},
	        {"key":"exception.message","value":{"stringValue":"ORA-00001: unique constraint (ORDERS.PK) violated"}},
	        {"key":"exception.stacktrace","value":{"stringValue":"at com.acme.OrderDao.insert(OrderDao.java:42)"}},
	        {"key":"exception.escaped","value":{"boolValue":true}}
	      ]
	    }]
	  }]}]
	}]}`
}

func TestParseOTLPTraceCarriesExceptionEvents(t *testing.T) {
	// (b) both envelope shapes + (c) both timeUnixNano encodings.
	cases := []struct {
		name     string
		envelope string
		timeLit  string
		wantNano uint64
	}{
		{"batches + string nano", "batches", `"1700000000400000000"`, 1700000000400000000},
		{"batches + numeric nano", "batches", `1700000000400000000`, 1700000000400000000},
		{"resourceSpans + string nano", "resourceSpans", `"1700000000400000000"`, 1700000000400000000},
		{"resourceSpans + numeric nano", "resourceSpans", `1700000000400000000`, 1700000000400000000},
		// A missing/garbage timestamp must not cost the operator the event
		// itself — the name and attributes are what render the section.
		{"unparseable nano keeps the event", "batches", `"not-a-number"`, 0},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			rows, err := parseOTLPTrace([]byte(exceptionPayload(c.envelope, c.timeLit)))
			if err != nil {
				t.Fatalf("parseOTLPTrace: %v", err)
			}
			if len(rows) != 1 {
				t.Fatalf("want 1 span, got %d", len(rows))
			}
			evs := decodeEvents(t, rows[0].Events)
			if len(evs) != 1 {
				t.Fatalf("want 1 event, got %d (Events=%q)", len(evs), rows[0].Events)
			}
			e := evs[0]
			// The name is load-bearing: the Exceptions section filters on it.
			if e.Name != "exception" {
				t.Errorf("event name = %q, want %q", e.Name, "exception")
			}
			if e.TimeNano != c.wantNano {
				t.Errorf("timeNano = %d, want %d", e.TimeNano, c.wantNano)
			}
			// (a) exception attributes carried VERBATIM — these are the
			// section's whole content.
			want := map[string]string{
				"exception.type":       "java.sql.SQLIntegrityConstraintViolationException",
				"exception.message":    "ORA-00001: unique constraint (ORDERS.PK) violated",
				"exception.stacktrace": "at com.acme.OrderDao.insert(OrderDao.java:42)",
				"exception.escaped":    "true",
			}
			if len(e.Attributes) != len(want) {
				t.Errorf("attribute count = %d, want %d (%v)", len(e.Attributes), len(want), e.Attributes)
			}
			for k, v := range want {
				if got := e.Attributes[k]; got != v {
					t.Errorf("attribute %q = %q, want %q", k, got, v)
				}
			}
		})
	}
}

// (d) The no-regression half: a response without an `events` field must leave
// Events nil, so an event-less Tempo span serialises exactly like an
// event-less ClickHouse span. Emitting "[]" here would be a new, different
// wire value for the same fact.
func TestParseOTLPTraceWithoutEventsLeavesEventsEmpty(t *testing.T) {
	payload := `{"batches":[{
	  "resource":{"attributes":[{"key":"service.name","value":{"stringValue":"orders"}}]},
	  "scopeSpans":[{"scope":{"name":"s"},"spans":[{
	    "traceId":"0af7651916cd43dd8448eb211c80319c",
	    "spanId":"b7ad6b7169203331",
	    "name":"GET /orders",
	    "startTimeUnixNano":"1700000000000000000",
	    "endTimeUnixNano":"1700000000500000000"
	  }]}]
	}]}`
	rows, err := parseOTLPTrace([]byte(payload))
	if err != nil {
		t.Fatalf("parseOTLPTrace: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("want 1 span, got %d", len(rows))
	}
	// nil, not "[]" and not "": the ClickHouse path leaves the field untouched
	// when the stored column is empty, so both sources serialise to null.
	if rows[0].Events != nil {
		t.Errorf("Events = %#v, want nil for an event-less span", rows[0].Events)
	}
}

// Multiple events on one span keep their order and their individual
// attribute sets — a span can carry an exception plus log-bridge records.
func TestParseOTLPTraceCarriesMultipleEvents(t *testing.T) {
	payload := `{"batches":[{
	  "resource":{"attributes":[{"key":"service.name","value":{"stringValue":"orders"}}]},
	  "scopeSpans":[{"scope":{"name":"s"},"spans":[{
	    "traceId":"0af7651916cd43dd8448eb211c80319c",
	    "spanId":"b7ad6b7169203331",
	    "name":"INSERT orders",
	    "startTimeUnixNano":"1700000000000000000",
	    "endTimeUnixNano":"1700000000500000000",
	    "events":[
	      {"timeUnixNano":"100","name":"retrying","attributes":[{"key":"attempt","value":{"intValue":"2"}}]},
	      {"timeUnixNano":"200","name":"exception","attributes":[{"key":"exception.type","value":{"stringValue":"SQLException"}}]}
	    ]
	  }]}]
	}]}`
	rows, err := parseOTLPTrace([]byte(payload))
	if err != nil {
		t.Fatalf("parseOTLPTrace: %v", err)
	}
	evs := decodeEvents(t, rows[0].Events)
	if len(evs) != 2 {
		t.Fatalf("want 2 events, got %d", len(evs))
	}
	if evs[0].Name != "retrying" || evs[1].Name != "exception" {
		t.Errorf("event order not preserved: %q, %q", evs[0].Name, evs[1].Name)
	}
	// OTLP-JSON encodes int64 as a string; otlpAttr.String() flattens it.
	if evs[0].Attributes["attempt"] != "2" {
		t.Errorf("intValue attribute = %q, want %q", evs[0].Attributes["attempt"], "2")
	}
	if evs[1].Attributes["exception.type"] != "SQLException" {
		t.Errorf("exception.type = %q", evs[1].Attributes["exception.type"])
	}
}

// The serialized shape must match internal/otlp/convert.go's spanEvent JSON
// tags exactly — a rename there or here silently breaks every reader that
// parses the column.
func TestEventsJSONShapeMatchesIngestPath(t *testing.T) {
	rows, err := parseOTLPTrace([]byte(exceptionPayload("batches", `"1700000000400000000"`)))
	if err != nil {
		t.Fatalf("parseOTLPTrace: %v", err)
	}
	raw, err := json.Marshal(rows[0].Events)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var generic []map[string]any
	if err := json.Unmarshal(raw, &generic); err != nil {
		t.Fatalf("decode: %v", err)
	}
	for _, key := range []string{"name", "timeNano", "attributes"} {
		if _, ok := generic[0][key]; !ok {
			t.Errorf("event JSON missing %q key — diverged from the OTLP ingest path", key)
		}
	}
	if _, ok := generic[0]["timeUnixNano"]; ok {
		t.Error("event JSON leaked the wire name timeUnixNano; the column shape uses timeNano")
	}
}
