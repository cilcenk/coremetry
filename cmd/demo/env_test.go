package main

import "testing"

// v0.8.383 — env-separation Phase 0c: the demo emits
// deployment.environment.name per service INSTANCE (pod), assigned by
// envForPod. These tests pin the properties the feature test-bed
// depends on:
//   - deterministic: same (service, pod) → same env, always
//     (resource-level, never per-span);
//   - coverage: every registered service's pod pool spans ≥2 of
//     demoEnvs (the operator's "same mobile-bff in int/uat/prep"
//     case) and all three envs exist somewhere in the mesh;
//   - stability: env follows the pod's INDEX, so a pod-generation
//     roll (name suffix) never reshuffles a service's env set;
//   - fallback pods (ad-hoc services) still map deterministically.
func TestEnvForPod_Deterministic(t *testing.T) {
	for name, s := range services {
		for _, pod := range s.Pods {
			a := envForPod(s, pod)
			b := envForPod(s, pod)
			if a != b {
				t.Fatalf("%s/%s: env not deterministic (%q vs %q)", name, pod, a, b)
			}
			valid := false
			for _, e := range demoEnvs {
				if a == e {
					valid = true
				}
			}
			if !valid {
				t.Fatalf("%s/%s: env %q not in demoEnvs %v", name, pod, a, demoEnvs)
			}
		}
	}
}

func TestEnvForPod_EveryServiceSpansMultipleEnvs(t *testing.T) {
	global := map[string]bool{}
	for name, s := range services {
		envs := map[string]bool{}
		for _, pod := range s.Pods {
			e := envForPod(s, pod)
			envs[e] = true
			global[e] = true
		}
		// Index-round-robin guarantees a k-pod pool covers
		// min(k, 3) distinct envs; every pool is ≥2 pods
		// (sms/email/audit were bumped in v0.8.383 for this).
		want := len(s.Pods)
		if want > len(demoEnvs) {
			want = len(demoEnvs)
		}
		if len(envs) != want {
			t.Errorf("%s: %d pods cover %d envs, want %d (%v)", name, len(s.Pods), len(envs), want, envs)
		}
		if len(envs) < 2 {
			t.Errorf("%s: spans %d env(s), want >= 2 — mirror the operator's multi-env case", name, len(envs))
		}
	}
	for _, e := range demoEnvs {
		if !global[e] {
			t.Errorf("env %q never assigned across the mesh — /api/environments would miss it", e)
		}
	}
}

func TestEnvForPod_StableAcrossGenerationRolls(t *testing.T) {
	// rollPodGeneration suffixes every pod name; the env must follow
	// the INDEX so each service's env set survives a demo restart.
	s := Service{Name: "transfer-service", Pods: []string{"xfer-prod-1", "xfer-prod-2"}}
	before := []string{envForPod(s, s.Pods[0]), envForPod(s, s.Pods[1])}
	rolled := Service{Name: s.Name, Pods: []string{s.Pods[0] + "-r4242", s.Pods[1] + "-r4242"}}
	after := []string{envForPod(rolled, rolled.Pods[0]), envForPod(rolled, rolled.Pods[1])}
	for i := range before {
		if before[i] != after[i] {
			t.Fatalf("pod %d: env changed across a generation roll (%q → %q)", i, before[i], after[i])
		}
	}
}

func TestEnvForPod_UnknownPodFallsBackDeterministically(t *testing.T) {
	// sendLog/sendTraces synthesise Service{Pods:[svc+"-1"]} for
	// unregistered services; envForPod must not panic and must be
	// stable for such pods too.
	s := Service{Name: "adhoc-service", Pods: []string{"adhoc-service-1"}}
	a := envForPod(s, "some-other-pod")
	b := envForPod(s, "some-other-pod")
	if a != b {
		t.Fatalf("fallback env not deterministic (%q vs %q)", a, b)
	}
}
