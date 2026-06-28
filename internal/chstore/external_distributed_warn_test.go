package chstore

import (
	"strings"
	"testing"
)

// v0.8.211 — on an external Distributed `spans` with cluster_name unset, the
// summary MVs read FROM the Distributed wrapper, their per-shard insert trigger
// never fires, and they stay EMPTY → reads return no/partial results. This used
// to be a silent failure (mysteriously empty dashboards). externalDistributedWarning
// is the pure builder of the operator-facing fix message (boot log + the basis
// of the /admin/stats SystemHealth flag); pin that it always names the env var
// and, when the cluster was discoverable, the exact value to set.
func TestExternalDistributedWarning(t *testing.T) {
	t.Run("known cluster names the exact value to set", func(t *testing.T) {
		msg := externalDistributedWarning("uptrace_all")
		if !strings.Contains(msg, "COREMETRY_CH_CLUSTER_NAME=uptrace_all") {
			t.Fatalf("expected the discovered cluster in the fix; got: %s", msg)
		}
	})
	t.Run("unparseable cluster still gives actionable guidance", func(t *testing.T) {
		msg := externalDistributedWarning("")
		if !strings.Contains(msg, "COREMETRY_CH_CLUSTER_NAME") {
			t.Fatalf("expected the env var in the fix even without a discovered name; got: %s", msg)
		}
		if strings.Contains(msg, "COREMETRY_CH_CLUSTER_NAME=") {
			t.Fatalf("must NOT emit a bare 'NAME=' with no value when the cluster is unknown; got: %s", msg)
		}
	})
	t.Run("always explains the empty-MV consequence", func(t *testing.T) {
		for _, c := range []string{"uptrace_all", ""} {
			if msg := externalDistributedWarning(c); !strings.Contains(msg, "EMPTY") {
				t.Fatalf("warning should state the MVs stay EMPTY; got: %s", msg)
			}
		}
	})
}

// v0.8.213 — boot now HARD-ERRORS on the external-Distributed-unset state
// (the root of the local-pass/prod-break class) instead of silently creating
// MVs that never populate, with COREMETRY_CH_ALLOW_UNSET_CLUSTER as the escape
// hatch. externalDistributedFatal is the pure gate: fatal ONLY when spans is an
// external Distributed table AND the operator hasn't opted into degraded mode —
// never for single-node or a Coremetry-owned cluster (both yield isExternal=false
// upstream, but pin the gate's own truth table so a future refactor can't make a
// healthy install fail to boot).
func TestExternalDistributedFatal(t *testing.T) {
	cases := []struct {
		name       string
		isExternal bool
		allowUnset bool
		want       bool
	}{
		{"external + not allowed => fatal", true, false, true},
		{"external + opted into degraded => boots", true, true, false},
		{"not external (single-node / owned cluster) => boots", false, false, false},
		{"not external + allow (irrelevant) => boots", false, true, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := externalDistributedFatal(c.isExternal, c.allowUnset); got != c.want {
				t.Fatalf("externalDistributedFatal(%v, %v) = %v, want %v",
					c.isExternal, c.allowUnset, got, c.want)
			}
		})
	}
}
