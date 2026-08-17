package api

// v0.9.1150 — every publishConfigReload topic must have a listener.
//
// This trap has been sprung twice. v0.9.237: putThanosSettings had
// published "thanos" since it shipped, but reloadConfigOnSignal had no
// case for it — the message fell into default: and was dropped, so peer
// pods converged on their 30s poll instead of the sub-50ms the publish
// was written for. v0.9.829's devops case carries a comment about
// deliberately adding the case in the SAME release for that reason.
//
// A publish with no listener reads as working right up until someone
// measures it, which is exactly the failure a test should own rather than
// a comment. It matters most for the victoria-metrics topic: that setting
// decides WHICH STORE answers, so a dropped signal means peer pods serve
// metrics from a different backend for up to 30 seconds.

import (
	"os"
	"regexp"
	"sort"
	"strings"
	"testing"
)

func TestEveryConfigReloadTopicHasAListener(t *testing.T) {
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	pub := regexp.MustCompile(`publishConfigReload\([^,]+,\s*"([^"]+)"\)`)

	topics := map[string]string{} // topic → publishing file
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		raw, err := os.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
		for _, m := range pub.FindAllStringSubmatch(stripGoComments(string(raw)), -1) {
			topics[m[1]] = name
		}
	}
	// Scope floor: if the scan ever finds nothing it would pass while
	// proving nothing (the settingsLoadGate lesson).
	if len(topics) < 8 {
		t.Fatalf("only %d publishConfigReload topics found — the scan lost its scope", len(topics))
	}

	raw, err := os.ReadFile("cache.go")
	if err != nil {
		t.Fatal(err)
	}
	listener := stripGoComments(string(raw))
	i := strings.Index(listener, "func (s *Server) reloadConfigOnSignal")
	if i < 0 {
		t.Fatal("reloadConfigOnSignal not found in cache.go")
	}
	body := listener[i:]

	var missing []string
	for topic := range topics {
		// `case "x":` and `case "x", "y":` both count.
		if !regexp.MustCompile(`case[^:\n]*"` + regexp.QuoteMeta(topic) + `"`).MatchString(body) {
			missing = append(missing, topic+" (published by "+topics[topic]+")")
		}
	}
	sort.Strings(missing)
	if len(missing) > 0 {
		t.Errorf("publishConfigReload topics with no case in reloadConfigOnSignal: %v — "+
			"the signal falls into default: and peer pods wait out their 30s poll instead "+
			"(v0.9.237)", missing)
	}
}
