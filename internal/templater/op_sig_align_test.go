package templater

import (
	"regexp"
	"testing"

	"github.com/cilcenk/coremetry/internal/chstore"
)

// v0.8.x — the Endpoints "Group by shape" toggle normalizes operation paths
// READ-TIME via chstore.opSigWrap (SQL regex), while the service Operations
// tab groups by the INGEST-TIME op_group column (NormalizeOperation here).
// Shipped separately, they diverged: opSigWrap used :uuid + missed hex ids,
// NormalizeOperation used :id for every id type — so the same span could show
// a different shape on the two pages. opSigWrap was aligned to this
// normalizer's `:id` convention (+ a long-hex rule). This locks that the two
// collapse a path to the SAME shape for the common id types so a future edit
// to either side can't silently re-introduce the divergence. (Lives in
// templater, not chstore, because chstore can't import templater — puller.go
// already imports chstore — so the test imports the EXPORTED opSig consts.)
//
// opSigWrap is a SQL string; ClickHouse RE2 == Go regexp, so applying the
// SAME pattern consts here is faithful to what ClickHouse runs.
func applyOpSig(path string) string {
	path = regexp.MustCompile(chstore.OpSigReUUID).ReplaceAllString(path, ":id")
	path = regexp.MustCompile(chstore.OpSigReHex).ReplaceAllString(path, "/:id")
	path = regexp.MustCompile(chstore.OpSigReNum).ReplaceAllString(path, "/:id")
	return path
}

func TestOpSig_OpGroup_SameShape(t *testing.T) {
	cases := []struct {
		path string
		want string // the normalized PATH shape (op_group adds the method prefix)
	}{
		{"/orders/8421", "/orders/:id"},
		{"/orders/8421/items/99", "/orders/:id/items/:id"},
		{"/users/550e8400-e29b-41d4-a716-446655440000", "/users/:id"}, // UUID
		{"/items/0123456789abcdef0", "/items/:id"},                    // 17-char hex id
		{"/api/v2/health", "/api/v2/health"},                          // no id — unchanged both sides
		{"/order-8421/x", "/order-8421/x"},                            // mid-segment digits NOT collapsed (both sides)
	}
	for _, c := range cases {
		// Read-time path (Endpoints group-by-shape).
		if got := applyOpSig(c.path); got != c.want {
			t.Errorf("opSigWrap(%q) = %q, want %q", c.path, got, c.want)
		}
		// Ingest-time path (op_group column / Operations tab) must produce the
		// SAME path shape — only the deliberate method prefix differs.
		gotOp := NormalizeOperation("GET "+c.path, "server", "", "", "", "")
		wantOp := "GET " + c.want
		if gotOp != wantOp {
			t.Errorf("op_sig/op_group DIVERGENCE on %q: NormalizeOperation = %q, want %q",
				c.path, gotOp, wantOp)
		}
	}
}
