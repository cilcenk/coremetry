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
