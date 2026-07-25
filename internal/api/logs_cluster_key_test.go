package api

import (
	"strings"
	"testing"

	"github.com/cilcenk/coremetry/internal/logstore"
)

// v0.9.216 — the toolbar's cluster select narrowed the /logs TABLE but not
// the histogram above it or the severity chips beside it: getLogsTimeseries
// never copied Cluster into its filter. Fixing that alone would have been
// worse than the bug — logsTimeseriesKey didn't hash Cluster either, so a
// cluster-scoped histogram and an unscoped one would have shared a cache
// entry inside the 30s TTL (the v0.5.187 cross-poisoning class). These
// tests pin the key half of that contract.

func TestLogsTimeseriesKey_CarriesCluster(t *testing.T) {
	key := func(cluster string) string {
		return logsTimeseriesKey(logstore.Filter{Service: "mobile-bff", Cluster: cluster}, "1", "2", 30, "severity")
	}
	if key("prod-eu") == key("") {
		t.Fatal("cluster-scoped key must differ from the unscoped one")
	}
	if key("prod-eu") == key("prod-us") {
		t.Fatal("two clusters must not share a key")
	}
	if !strings.Contains(key("prod-eu"), "clu=prod-eu") {
		t.Fatalf("key must carry the cluster value; got %q", key("prod-eu"))
	}
}

// Every other input must keep differentiating once cluster is in the mix —
// a key that collapses on some OTHER field is the same bug wearing a hat.
func TestLogsTimeseriesKey_StillSeparatesEveryInput(t *testing.T) {
	base := logstore.Filter{
		Service: "mobile-bff", Cluster: "prod-eu", Env: "uat",
		SeverityMin: 17, TraceID: "abc", HasTrace: true, Search: "timeout",
	}
	baseKey := logsTimeseriesKey(base, "1", "2", 30, "severity")

	mutations := map[string]logstore.Filter{}
	m := base
	m.Service = "web-bff"
	mutations["service"] = m
	m = base
	m.Cluster = "prod-us"
	mutations["cluster"] = m
	m = base
	m.Env = "prep"
	mutations["env"] = m
	m = base
	m.SeverityMin = 13
	mutations["severity"] = m
	m = base
	m.TraceID = "def"
	mutations["traceID"] = m
	m = base
	m.HasTrace = false
	mutations["hasTrace"] = m
	m = base
	m.Search = "refused"
	mutations["search"] = m

	for name, f := range mutations {
		if got := logsTimeseriesKey(f, "1", "2", 30, "severity"); got == baseKey {
			t.Fatalf("%s must change the key, got the same: %q", name, got)
		}
	}

	// Non-Filter inputs travel as separate args — they must separate too.
	if logsTimeseriesKey(base, "9", "2", 30, "severity") == baseKey {
		t.Fatal("from must change the key")
	}
	if logsTimeseriesKey(base, "1", "9", 30, "severity") == baseKey {
		t.Fatal("to must change the key")
	}
	if logsTimeseriesKey(base, "1", "2", 60, "severity") == baseKey {
		t.Fatal("bucketSec must change the key")
	}
	if logsTimeseriesKey(base, "1", "2", 30, "") == baseKey {
		t.Fatal("groupBy must change the key")
	}
}
