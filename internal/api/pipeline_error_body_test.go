package api

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/cilcenk/coremetry/internal/pipeline"
)

// v0.9.803 regression — the pipeline admin handlers hand-built their error
// bodies by string concatenation:
//
//	http.Error(w, `{"error":"`+err.Error()+`"}`, http.StatusBadRequest)
//
// Every message carrying a double quote therefore shipped INVALID JSON. That
// is the common case, not the exotic one: pipeline's validateCondition
// formats the offending regex with %q ("invalid RE2 pattern %q: %w"), so a
// bad pattern — the single most likely 400 on this form, and the one
// v0.9.802 deliberately routed to a message under the pattern field —
// produced a body the SPA could not parse. humanize() JSON.parses and falls
// back to the raw text, so the operator saw the broken blob verbatim.
//
// Table drives every quote-hostile shape through writeJSONError and asserts
// the body round-trips: parseable JSON, error field byte-identical to the
// message that went in.
func TestWriteJSONErrorIsValidJSON(t *testing.T) {
	cases := []struct {
		name string
		msg  string
	}{
		{"plain", "id required"},
		// The v0.9.803 bug case verbatim: %q-quoted RE2 pattern.
		{"quoted pattern", `invalid RE2 pattern "[": error parsing regexp: missing closing ]: ` + "`[`"},
		{"quoted rule kind", `unknown rule kind "explode"`},
		{"backslash", `invalid RE2 pattern "\\": trailing backslash at end of expression`},
		{"embedded newline", "invalid JSON: unexpected end\nof input"},
		{"embedded tab and quote", "and[0]: predicate key required\t\"metric\""},
		{"unicode", `invalid RE2 pattern "^/sağlık": ok`},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			writeJSONError(rec, 400, c.msg)

			if rec.Code != 400 {
				t.Fatalf("code = %d, want 400", rec.Code)
			}
			if ct := rec.Header().Get("Content-Type"); ct != "application/json" {
				t.Errorf("Content-Type = %q, want application/json", ct)
			}
			var got map[string]string
			if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
				t.Fatalf("body is not valid JSON: %v\nbody = %s", err, rec.Body.String())
			}
			if got["error"] != c.msg {
				t.Errorf("error = %q, want %q", got["error"], c.msg)
			}
		})
	}
}

// pipelineTestStore satisfies pipeline's unexported `store` interface — its
// method set is exported, so a type outside the package can implement it.
type pipelineTestStore struct{ raw []byte }

func (p *pipelineTestStore) GetPipelineRulesRaw(context.Context) ([]byte, error) {
	return p.raw, nil
}
func (p *pipelineTestStore) PutPipelineRulesRaw(_ context.Context, raw []byte) error {
	p.raw = raw
	return nil
}

// End-to-end half of the same regression: take the REAL error the engine
// returns for a bad pattern (so the test can't drift from validateCondition's
// wording) and prove it survives writeJSONError as parseable JSON. The old
// concatenation path is asserted broken on the same bytes, which is what
// makes this a regression test rather than a tautology.
func TestPipelineUpsertBadPatternErrorSerialises(t *testing.T) {
	eng := pipeline.New()
	_, err := eng.Upsert(context.Background(), &pipelineTestStore{}, pipeline.Rule{
		Name:    "bad regex",
		Kind:    pipeline.KindDrop,
		Signal:  pipeline.SignalMetrics,
		Enabled: true,
		When:    pipeline.Condition{Key: "http.route", Op: pipeline.OpMatches, Value: "["},
	})
	if err == nil {
		t.Fatal("expected an error for pattern \"[\"")
	}
	if !strings.Contains(err.Error(), `"`) {
		t.Fatalf("precondition: engine error should carry %%q quotes, got %q", err.Error())
	}

	// The pre-v0.9.803 body shape — must NOT parse (proves the bug was real).
	legacy := `{"error":"` + err.Error() + `"}`
	if json.Valid([]byte(legacy)) {
		t.Errorf("legacy concatenated body unexpectedly parsed: %s", legacy)
	}

	rec := httptest.NewRecorder()
	writeJSONError(rec, 400, err.Error())
	var got map[string]string
	if e := json.Unmarshal(rec.Body.Bytes(), &got); e != nil {
		t.Fatalf("writeJSONError body is not valid JSON: %v\nbody = %s", e, rec.Body.String())
	}
	if got["error"] != err.Error() {
		t.Errorf("error = %q, want %q", got["error"], err.Error())
	}
}
