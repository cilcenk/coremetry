package logstore

import "testing"

// v0.10.310 — Drain şablonları "<*>" yazar; PatternSearchQuery tek türetici
// olarak her iki yer tutucuyu da atmalı.
func TestPatternSearchQueryDrainWildcard(t *testing.T) {
	got := PatternSearchQuery("svc: parse error: pacs.<*> GrpHdr<*> contains invalid")
	want := `"svc: parse error: pacs" AND "GrpHdr" AND "contains invalid"`
	if got != want {
		t.Errorf("got %q\nwant %q", got, want)
	}
	if got := PatternSearchQuery("<*> <*>"); got != "" {
		t.Errorf("yalnız yer tutucu → boş; got %q", got)
	}
}
