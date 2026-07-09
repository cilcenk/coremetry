package api

import (
	"strings"
	"testing"

	"github.com/cilcenk/coremetry/internal/logstore"
)

// v0.8.400 — env-separation Phase 4: every /api/logs/* cache key (and
// the live-tail group key) carries the new env input. Hash-ALL-inputs
// (the v0.5.187 class): an env-filtered response must never
// cross-poison the unfiltered one inside the shared TTL, and two envs
// must never share a tail poll group.

func TestLogsSearchKey_CarriesEnv(t *testing.T) {
	key := func(env string) string {
		return logsSearchKey(logstore.Filter{Service: "mobile-bff", Env: env, Limit: 100}, "1", "2")
	}
	uat, prep, all := key("uat"), key("prep"), key("")
	if uat == prep || uat == all || prep == all {
		t.Fatalf("distinct envs must produce distinct keys: uat=%q prep=%q all=%q", uat, prep, all)
	}
	if !strings.Contains(uat, "env=uat") {
		t.Fatalf("key must carry the env value; got %q", uat)
	}
	if key("uat") != uat {
		t.Fatal("logsSearchKey must be deterministic")
	}
}

func TestLogsFieldStatsKey_CarriesEnv(t *testing.T) {
	key := func(env string) string {
		return logsFieldStatsKey("k8s.pod.name", logstore.Filter{Env: env}, "1", "2")
	}
	if key("uat") == key("") || key("uat") == key("prep") {
		t.Fatal("fieldstats key must differentiate envs")
	}
	if !strings.Contains(key("uat"), "env=uat") {
		t.Fatalf("key must carry the env value; got %q", key("uat"))
	}
}

func TestLogsTimeseriesKey_CarriesEnv(t *testing.T) {
	key := func(env string) string {
		return logsTimeseriesKey(logstore.Filter{Env: env}, "1", "2", 30, "severity")
	}
	if key("uat") == key("") || key("uat") == key("prep") {
		t.Fatal("timeseries key must differentiate envs")
	}
	if !strings.Contains(key("uat"), "env=uat") {
		t.Fatalf("key must carry the env value; got %q", key("uat"))
	}
}

func TestTailFilterKey_CarriesEnv(t *testing.T) {
	base := logstore.Filter{Service: "mobile-bff", Search: "timeout"}
	uat, prep, all := base, base, base
	uat.Env, prep.Env = "uat", "prep"
	kUat, kPrep, kAll := tailFilterKey(uat), tailFilterKey(prep), tailFilterKey(all)
	if kUat == kPrep || kUat == kAll || kPrep == kAll {
		t.Fatalf("distinct envs must produce distinct tail groups: uat=%q prep=%q all=%q", kUat, kPrep, kAll)
	}
	if tailFilterKey(uat) != kUat {
		t.Fatal("tailFilterKey must be deterministic")
	}
}
