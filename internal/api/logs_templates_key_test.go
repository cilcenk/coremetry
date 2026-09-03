package api

// v0.10.310 — /api/logs/templates: servis anahtara girer; satır JSON'u
// gömülü alanları düzleştirip query'yi ekler (Şablonlar sekmesi sözleşmesi).

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/cilcenk/coremetry/internal/chstore"
)

func TestLogsTemplatesKey(t *testing.T) {
	a := logsTemplatesKey("last_seen", 6*time.Hour, 200, "")
	b := logsTemplatesKey("last_seen", 6*time.Hour, 200, "api")
	if a == b {
		t.Fatalf("servisli ve servissiz anahtar aynı: %s", a)
	}
	for _, w := range []string{"sort=last_seen", "since=6h0m0s", "limit=200", "svc=api"} {
		if !strings.Contains(b, w) {
			t.Errorf("%q yok: %s", w, b)
		}
	}
}

func TestLogTemplateRowJSON(t *testing.T) {
	raw, err := json.Marshal(logTemplateRow{
		LogTemplate: chstore.LogTemplate{ID: "x", Template: "a <*> b", TotalCount: 3, Services: []string{"api"}, Sample: "a 1 b"},
		Query:       `"a" AND "b"`,
	})
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{"id", "template", "totalCount", "services", "sample", "query", "firstSeen", "lastSeen"} {
		if _, ok := m[k]; !ok {
			t.Errorf("%q düz alan olarak yok: %s", k, raw)
		}
	}
	if _, nested := m["LogTemplate"]; nested {
		t.Errorf("gömülü struct düzleşmemiş: %s", raw)
	}
}
