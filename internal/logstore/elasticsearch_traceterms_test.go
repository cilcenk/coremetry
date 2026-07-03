package logstore

import (
	"encoding/json"
	"strings"
	"testing"
)

// v0.8.235 — operator-reported: trace-id log search returned 0 rows on
// the external ES cluster even though Kibana Discover's free-text
// search found them. Their pipeline stores the id under a field name
// outside our candidate set, and Kibana's discriminating clause is a
// bare multi_match (best_fields, lenient, all fields). This pins that
// catch-all clause into traceTermsAny for BOTH kinds — dropping it
// re-breaks every install whose trace field name we can't predict.
func TestTraceTermsAnyKibanaCatchAll(t *testing.T) {
	for _, kind := range []string{"trace", "span"} {
		q := traceTermsAny("configured.field", "message", "b07bf7a01f340611e014b90a91b66d6e", kind)
		raw, err := json.Marshal(q)
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		s := string(raw)
		for _, want := range []string{
			`"multi_match"`,
			`"type":"best_fields"`,
			`"lenient":true`,
			`"query":"b07bf7a01f340611e014b90a91b66d6e"`,
			// the cheap exact-field terms must stay first-class
			`"configured.field"`,
			`"` + kind + `.id"`,
			`"minimum_should_match":1`,
		} {
			if !strings.Contains(s, want) {
				t.Errorf("kind=%s missing %s in: %s", kind, want, s)
			}
		}
		// The multi_match must NOT carry a fields list — no list means
		// "all eligible fields", which is exactly the Kibana behaviour
		// that found the operator's rows.
		if strings.Contains(s, `"fields"`) {
			t.Errorf("kind=%s multi_match must not restrict fields: %s", kind, s)
		}
	}
}
