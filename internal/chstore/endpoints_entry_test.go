package chstore

import (
	"strings"
	"testing"
)

// v0.9.313 (brief N1) — the /endpoints table filtered `http_route != ''`
// from day one, so gRPC server and Kafka consumer entry points were not
// merely unlisted: they were INVISIBLE, and no footnote said so. An
// operator reading the table came away with "these are all my entry
// points" — an incomplete truth, the same silent class as the drawer's
// scope (v0.9.306), and against the standing entry-span principle that
// a service's inbound surface is server AND consumer.
//
// The MV needed nothing: its ORDER BY already carries `name` and
// `kind`; they simply never reached the projection.

func TestEntryKindDefaultsToHTTP(t *testing.T) {
	// The zero value must be today's table, byte for byte — every
	// existing caller constructs EndpointsQuery without this field.
	var q EndpointsQuery
	if q.Entry == EntryRPC {
		t.Fatal("the zero value must not select the RPC surface")
	}
	if q.Entry != "" && q.Entry != EntryHTTP {
		t.Fatalf("unexpected zero value %q", q.Entry)
	}
}

// Each surface must key on its OWN dimension and filter to its own
// kinds. Crossing them would list HTTP routes under an RPC heading or
// silently drop consumers.
func TestEntrySurfacesAreDisjoint(t *testing.T) {
	src := mustReadSource(t, "endpoints.go")

	if !strings.Contains(src, `entryWhere = " AND kind IN ('server', 'consumer') AND http_route = ''"`) {
		t.Error("the RPC surface must select inbound spans WITHOUT a route — that is precisely the set the HTTP tab hides")
	}
	if !strings.Contains(src, `AND kind NOT IN ('client', 'producer') AND http_route != ''`) {
		t.Error("the HTTP surface must stay exactly as it was")
	}
	// The shape toggle has to follow the dimension, or the RPC tab
	// drowns in id-bearing span names ("GET /orders/8421").
	if !strings.Contains(src, "opSigWrap(dimCol)") {
		t.Error("group-by-shape must collapse the ACTIVE dimension, not always http_route")
	}
}

// The RPC tab has no raw-path equivalent. Running the HTTP raw query
// anyway would render an empty table under a tab labelled "RPC &
// Messaging" — i.e. "you have no gRPC entry points" when the truth is
// "this combination cannot be answered". A stated refusal costs one
// sentence; a silent empty costs a wrong conclusion.
func TestRPCEntryRefusesTheRawPath(t *testing.T) {
	if errEndpointsRPCRaw == nil {
		t.Fatal("the refusal must be a real error, not a nil check")
	}
	msg := errEndpointsRPCRaw.Error()
	for _, want := range []string{"cluster", "env", "MV"} {
		if !strings.Contains(msg, want) {
			t.Errorf("the refusal must name what caused it and what to do; %q missing from %q", want, msg)
		}
	}
	if strings.Contains(strings.ToLower(msg), "no entry points") ||
		strings.Contains(strings.ToLower(msg), "not found") {
		t.Fatalf("the message must not read as an empty result: %q", msg)
	}

	src := mustReadSource(t, "endpoints.go")
	i := strings.Index(src, "if q.forcesRaw() {")
	if i < 0 {
		t.Fatal("raw dispatch not found — the scan has drifted")
	}
	j := strings.Index(src[i:], "return s.getEndpointsRaw(ctx, q)")
	if j < 0 {
		t.Fatal("raw call not found")
	}
	// The refusal must come BEFORE the raw call, or it never fires.
	if k := strings.Index(src[i:i+j], "errEndpointsRPCRaw"); k < 0 {
		t.Fatal("the RPC refusal must be checked before falling through to the HTTP raw query")
	}
}

// v0.9.324 — the search box on the RPC tab could not return a row.
//
// EntryRPC pins `http_route = ''` — that IS its definition of "non-HTTP
// inbound" — while the search conjunct was hardcoded to filter
// positionCaseInsensitive(http_route, ?) > 0. False for every row the tab can
// produce. The operator sees their gRPC and Kafka entry points, types a term
// to narrow, gets an empty table, and reads it as "there are none": the
// silent-empty class the N1 slice was written to remove, re-introduced by the
// slice itself.
//
// The invariant is simple enough to pin on the source: the column searched
// must be the column displayed, whichever surface is active.
func TestSearchFiltersTheDisplayedDimension(t *testing.T) {
	src := mustReadSource(t, "endpoints.go")

	if strings.Contains(src, `where += " AND positionCaseInsensitive(http_route, ?) > 0"`) {
		t.Error("the MV search conjunct is hardcoded to http_route — on the RPC tab that column is pinned to '', so search can never match")
	}
	if !strings.Contains(src, `where += " AND positionCaseInsensitive(" + dimCol + ", ?) > 0"`) {
		t.Error("the MV search conjunct must filter dimCol — the same column the tab projects and displays")
	}

	// dimCol has to actually differ per surface, or the assertion above is
	// vacuous.
	if !strings.Contains(src, `dimCol := "http_route"`) || !strings.Contains(src, `dimCol = "name"`) {
		t.Error("dimCol must be http_route on the HTTP surface and name on the RPC surface")
	}
}

// v0.9.325 — the HTTP status sidecar must not run on the RPC surface.
//
// It fills the 2xx/3xx/4xx/5xx pills and the Method chip from http_status /
// http_method. gRPC and Kafka entry points have neither. And it keys its
// IN-list on `http_route` while RPC rows carry the span NAME in Path, so the
// predicate matched nothing: a real scan of the external Distributed spans
// table on every page load, to fill columns that cannot apply.
//
// Bounded-and-empty is still work. On prod that is a per-page-load fan-out
// bought for a guaranteed zero rows.
func TestStatusSidecarSkippedOnRPCSurface(t *testing.T) {
	src := mustReadSource(t, "endpoints.go")

	if !strings.Contains(src, `if !q.SkipStatus && len(out) > 0 && q.Entry != EntryRPC {`) {
		t.Error("the HTTP status sidecar must be skipped on the RPC surface — its columns cannot apply and its route IN-list cannot match")
	}
	// The sidecar itself stays http_route-keyed on purpose; pin that so a
	// future edit doesn't "fix" it into running against RPC rows.
	if !strings.Contains(src, `pathProj := "http_route"`) {
		t.Error("the sidecar is HTTP-keyed by design; if this changes, revisit the RPC skip above")
	}
}
