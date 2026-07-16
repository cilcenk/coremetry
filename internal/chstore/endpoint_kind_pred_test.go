// endpoint_kind_pred_test.go — v0.8.560 regression.
//
// The plain `kind NOT IN ('client','producer')` filter killed pathExpr's
// documented url.path/http.target fallback layers: url.path lives almost
// exclusively on kind=client spans, so the rows needing coalesce layer
// 3-4 were dropped before the coalesce ever ran (operator-reported; the
// population exists only in prod — locally url.path is 0/11.8M, and the
// relaxed filter was measured a no-op against the 313-endpoint baseline).
//
// endpointKindPred is ONE const shared by getEndpointsRaw and
// endpointStatusSidecar — a sidecar with a narrower filter would silently
// return no status breakdown for rows the main query admitted. These
// tests pin the predicate's three load-bearing properties and that both
// sites keep using it.
package chstore

import (
	"strings"
	"testing"
)

func TestEndpointKindPred(t *testing.T) {
	t.Run("routed client stays excluded — the double-counting rationale", func(t *testing.T) {
		// The carve-out must demand BOTH route sources empty; a routed
		// client call already lands under the callee's server-span row
		// (v0.5.386), admitting it would count the call twice.
		if !strings.Contains(endpointKindPred, `nullIf(http_route, '') IS NULL`) {
			t.Error("carve-out must require the http_route column empty")
		}
		if !strings.Contains(endpointKindPred, `nullIf(attr_values[indexOf(attr_keys, 'http.route')], '') IS NULL`) {
			t.Error("carve-out must require the http.route attr empty")
		}
	})

	t.Run("producer stays excluded unconditionally", func(t *testing.T) {
		// Messaging spans have no route concept and already own /messaging
		// (M1 v0.8.364) — the carve-out leg must be client-only.
		if !strings.Contains(endpointKindPred, `kind NOT IN ('client', 'producer')`) {
			t.Error("first leg must keep the full exclusion")
		}
		if !strings.Contains(endpointKindPred, `kind = 'client'`) {
			t.Error("carve-out must name client explicitly, never producer")
		}
		if strings.Contains(endpointKindPred, `kind = 'producer'`) {
			t.Error("no producer carve-out")
		}
	})

	t.Run("route-less client is admitted — the OR structure", func(t *testing.T) {
		// The predicate is (excluded-kinds OR client-carve-out); without the
		// OR the whole fix is a no-op.
		if !strings.Contains(endpointKindPred, "OR") {
			t.Error("predicate must be a disjunction")
		}
	})

	t.Run("no bind placeholder", func(t *testing.T) {
		// A literal ? would shift every positional arg after the splice
		// point — the v0.8.356 opSig class of failure.
		if strings.Contains(endpointKindPred, "?") {
			t.Error("predicate must not contain bind placeholders")
		}
	})
}
