// ghost_pods_test.go — v0.9.368 regression. A pod with no running
// container has no cAdvisor cpu/mem series, so it never entered byKey and
// the Pods tab showed "No pods matched" exactly when the service was down
// (all replicas crashlooping) — the tab claimed the workload didn't exist
// at the moment it mattered most. appendGhostPods mirrors applyDeployKSM:
// the KSM phase/restart maps already know those pods; they come back as
// zero-resource rows.
package thanos

import "testing"

func key(ns, pod string) string { return ns + "\x00" + pod }

func TestAppendGhostPods(t *testing.T) {
	emitted := map[string]bool{key("prod", "api-1"): true}
	base := []PodRow{{Cluster: "c1", Namespace: "prod", Pod: "api-1", CPUCores: 0.2}}

	phaseBy := map[string]string{
		key("prod", "api-1"):    "Running", // emitted normally — no ghost
		key("prod", "api-2"):    "Pending", // scheduling failure → ghost
		key("prod", "api-3"):    "Failed",  // crashloop, containers down → ghost
		key("prod", "idle-1"):   "Running", // healthy idle, scrape gap → stays excluded
		key("prod", "job-done"): "Succeeded",
	}
	restartBy := map[string]int{
		key("prod", "api-1"):   1,
		key("prod", "api-4"):   14, // Running-but-restarting (only in restart map) → ghost
		key("prod", "quiet-1"): 0,  // restart series exists, zero count, no phase → excluded
	}

	out := appendGhostPods(base, "c1", emitted, phaseBy, restartBy)

	got := map[string]PodRow{}
	for _, r := range out {
		got[r.Pod] = r
	}
	if len(out) != 4 {
		t.Fatalf("rows = %d (%v), want 4 (api-1 + 3 ghosts)", len(out), got)
	}
	ghosts := []struct {
		pod      string
		phase    string
		restarts int
	}{
		{"api-2", "Pending", 0},
		{"api-3", "Failed", 0},
		{"api-4", "", 14},
	}
	for _, g := range ghosts {
		r, ok := got[g.pod]
		if !ok || r.Phase != g.phase || r.Restarts != g.restarts || r.CPUCores != 0 {
			t.Errorf("%s: got %+v, want phase=%q restarts=%d zero-resource", g.pod, r, g.phase, g.restarts)
		}
	}
	// v0.9.371 — bilinmezlik ÜYELİKTEN: restart serisi olmayan hayaletler
	// unknown (UI '—'), seri getiren api-4 known (14 gerçek sayıdır).
	if !got["api-2"].RestartsUnknown || !got["api-3"].RestartsUnknown {
		t.Errorf("ghosts without a restart series must be RestartsUnknown")
	}
	if got["api-4"].RestartsUnknown {
		t.Errorf("api-4 has a restart series (14) — must not be RestartsUnknown")
	}
	for _, absent := range []string{"idle-1", "job-done", "quiet-1"} {
		if _, ok := got[absent]; ok {
			t.Errorf("%s should stay excluded (healthy/completed/no-signal)", absent)
		}
	}
	if r := got["api-1"]; r.CPUCores != 0.2 {
		t.Errorf("api-1 emitted row must pass through untouched, got %+v", r)
	}
}
