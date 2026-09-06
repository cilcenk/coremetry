package logstore

import (
	"errors"
	"os"
	"strings"
	"testing"
)

// v0.10.500 — log arama denetimi A2 🔴: terms agg'lar `.keyword`e çakılıydı;
// yönetilen/ECS mapping'de (düz keyword, alt-alan yok) servis kırılımı boş,
// severity yığını OTHER. Yazım artık field_caps'ten, o yoksa (prod apikey
// yalnız doküman okur) probe'dan; bilinemezse bugünkü `.keyword`.
func TestTermsAggSpellingFromCaps(t *testing.T) {
	cases := []struct {
		name string
		caps map[string]traceFieldCap
		want string
	}{
		{"managed mapping: bare keyword, no subfield", map[string]traceFieldCap{"service.name": cap1("keyword")}, "service.name"},
		{"dynamic mapping: text + .keyword", map[string]traceFieldCap{"service.name": cap1("text"), "service.name.keyword": cap1("keyword")}, "service.name.keyword"},
		{"text only → no keyword path", map[string]traceFieldCap{"service.name": cap1("text")}, ""},
		{"absent from mapping", map[string]traceFieldCap{}, ""},
	}
	for _, c := range cases {
		if got := termsAggSpellingFromCaps("service.name", c.caps); got != c.want {
			t.Errorf("%s: got %q want %q", c.name, got, c.want)
		}
	}
}

func TestTermsAggSpellingFromProbe(t *testing.T) {
	e400 := errors.New("400 fielddata is disabled on text fields")
	cases := []struct {
		name    string
		kw      int
		bareErr error
		bare    int
		want    string
	}{
		{"keyword has buckets → today's shape", 3, nil, 0, "svc.keyword"},
		{"keyword empty, bare has buckets → managed mapping", 0, nil, 7, "svc"},
		{"keyword empty, bare is text (400) → keep .keyword", 0, e400, 0, "svc.keyword"},
		{"both empty → unknown, keep .keyword", 0, nil, 0, "svc.keyword"},
	}
	for _, c := range cases {
		if got := termsAggSpellingFromProbe("svc", c.kw, c.bareErr, c.bare); got != c.want {
			t.Errorf("%s: got %q want %q", c.name, got, c.want)
		}
	}
}

func TestTermsProbeBodyIsBounded(t *testing.T) {
	b := termsProbeBody("@timestamp", "svc.keyword")
	if b["size"] != 0 || b["track_total_hits"] != false || b["timeout"] != "3s" {
		t.Fatalf("probe must carry the v0.8.3 guards: %v", b)
	}
	terms := b["aggs"].(map[string]any)["p"].(map[string]any)["terms"].(map[string]any)
	if terms["field"] != "svc.keyword" || terms["size"] != 1 {
		t.Fatalf("probe terms agg wrong: %v", terms)
	}
}

// v0.10.500 (A4) — desen örneklemesi yalnız gövde/zaman/severity okur.
func TestLeanSourceFields(t *testing.T) {
	s := &ESStore{}
	s.fields.Body, s.fields.Timestamp, s.fields.SeverityTx, s.fields.SeverityNo = "message", "@timestamp", "log.level", "severity_number"
	got := strings.Join(s.leanSourceFields(), ",")
	want := "message,@timestamp,log.level,severity_number,level,severity_text,severity"
	if got != want {
		t.Fatalf("lean _source = %s, want %s", got, want)
	}
}

// Kaynak pinleri: çözümleyici gerçekten histogram + desen sayımında
// kullanılıyor, patternCountBody artık `.keyword` eklemiyor, Search
// LeanSource'u `_source`a bağlıyor, GroupBySignatureN bayrağı kuruyor.
func TestTermsAggFieldWired_v0_10_500(t *testing.T) {
	es, err := os.ReadFile("elasticsearch.go")
	if err != nil {
		t.Fatal(err)
	}
	src := string(es)
	for _, want := range []string{
		"return s.termsAggField(ctx, s.fields.Service)",
		"s.termsAggField(ctx, s.fields.Service),",
		`searchBody["_source"] = s.leanSourceFields()`,
		"out = append(out, c, c+\".keyword\")",
	} {
		if !strings.Contains(src, want) {
			t.Fatalf("elasticsearch.go missing %q", want)
		}
	}
	if strings.Contains(src, `"field": svcField + ".keyword"`) {
		t.Fatal("patternCountBody must take the resolved spelling, not append .keyword")
	}
	pt, err := os.ReadFile("patterns.go")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(pt), "f.LeanSource = true") {
		t.Fatal("GroupBySignatureN must request the lean _source")
	}
}
