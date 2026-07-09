package api

import (
	"strings"
	"testing"

	"github.com/cilcenk/coremetry/internal/logstore"
)

// v0.8.406 — trace-only filter (operator ask: "sadece trace'i olan
// loglar"). Hash-ALL-inputs (the v0.5.187 class): a hasTrace-filtered
// response must never cross-poison the unfiltered one inside the
// shared 15s/30s TTL, and toggling the filter must never share a
// live-tail poll group with the unfiltered stream.

func TestLogsSearchKey_CarriesHasTrace(t *testing.T) {
	key := func(ht bool) string {
		return logsSearchKey(logstore.Filter{Service: "mobile-bff", HasTrace: ht, Limit: 100}, "1", "2")
	}
	if key(true) == key(false) {
		t.Fatal("hasTrace on/off must produce distinct search keys")
	}
	if !strings.Contains(key(true), "ht=true") {
		t.Fatalf("key must carry the hasTrace value; got %q", key(true))
	}
	if key(true) != key(true) {
		t.Fatal("logsSearchKey must be deterministic")
	}
}

func TestLogsTimeseriesKey_CarriesHasTrace(t *testing.T) {
	key := func(ht bool) string {
		return logsTimeseriesKey(logstore.Filter{HasTrace: ht}, "1", "2", 30, "severity")
	}
	if key(true) == key(false) {
		t.Fatal("hasTrace on/off must produce distinct timeseries keys")
	}
	if !strings.Contains(key(true), "ht=true") {
		t.Fatalf("key must carry the hasTrace value; got %q", key(true))
	}
}

func TestTailFilterKey_CarriesHasTrace(t *testing.T) {
	base := logstore.Filter{Service: "mobile-bff", Search: "timeout"}
	on := base
	on.HasTrace = true
	if tailFilterKey(on) == tailFilterKey(base) {
		t.Fatal("hasTrace on/off must produce distinct tail poll groups")
	}
	if tailFilterKey(on) != tailFilterKey(on) {
		t.Fatal("tailFilterKey must be deterministic")
	}
}

// parseBoolParam — the /api/logs* hasTrace query-string reader.
func TestParseBoolParam(t *testing.T) {
	for in, want := range map[string]bool{
		"1": true, "true": true, "TRUE": true, "True": true,
		"": false, "0": false, "false": false, "yes": false, "2": false,
	} {
		if got := parseBoolParam(in); got != want {
			t.Errorf("parseBoolParam(%q) = %v, want %v", in, got, want)
		}
	}
}
