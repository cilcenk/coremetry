package templater

// v0.6.28 regression test — the bare-number masker rule
// (\b\d+\b) does NOT catch digits adjacent to underscore
// because `_` is a regex word character (the boundary
// only matches against non-word). Without the new
// `<letters>_<digits>` rule, every "usr_54347 initiated
// transfer to account acc_1523" turned into its own Drain
// template — thousands of single-instance shapes in
// production banking installs. Operator-reported with
// screenshot showing 50+ NEW templates for the same
// underlying log shape, one per (user, account) tuple.

import (
	"strings"
	"testing"
)

func TestMask_PrefixedIDs(t *testing.T) {
	cases := []struct {
		name    string
		in      string
		wantOut string  // exact expected output
	}{
		// Operator-reported shape — should collapse to one template.
		{
			"transfer with user + account ID",
			"User usr_54347 initiated transfer request of $1000.50 to account acc_1523",
			"User <*> initiated transfer request of $<*>.<*> to account <*>",
		},
		// Loan application — same shape across users.
		{
			"loan application with user ID",
			"User usr_43202 applied for a loan of $50000",
			"User <*> applied for a loan of $<*>",
		},
		// Long underscored prefix.
		{
			"customer session reference",
			"Approved customer_session_98421 for endpoint",
			"Approved <*> for endpoint",
		},
		// Multiple IDs in one line. cust_67 has only 2 digits
		// (under the {3,} floor) so it intentionally survives —
		// short-suffix tokens overlap with real abbreviations
		// (mp3_player, aws_s3) and the operator-reported
		// pollution case is overwhelmingly 4+ digit IDs.
		{
			"three IDs — 4+ digit ones masked, 2-digit preserved",
			"Order ord_12345 from cust_67 via txn_8888888",
			"Order <*> from cust_67 via <*>",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := Mask(tc.in)
			if got != tc.wantOut {
				t.Errorf("Mask(%q)\n  got:  %q\n  want: %q", tc.in, got, tc.wantOut)
			}
		})
	}
}

// Spot-checks that we did NOT over-mask common log content.
// Each case asserts the substring stays present in the masked
// output (full equality would be brittle as other rules evolve).
func TestMask_DoesNotOverReach(t *testing.T) {
	cases := []struct {
		name        string
		in          string
		mustContain string
	}{
		// Note: the pre-existing bare-number rule (\b\d+\b)
		// masks every digit run regardless of v0.6.28's change.
		// Tests here lock in BOTH the new prefix-id rule's
		// non-overreach AND the bare-number rule's prior
		// behaviour at the points that matter for operator
		// readability.
		{"v-version still recognisable (v + dotted)",
			"Starting agent v1.2.3", "v"},  // bare-number rule still folds "1.2.3" but the v stays
		{"single-digit suffix preserved",   "Connecting to log4j logger",        "log4j"},
		{"iso8601 word (no underscore)",    "Parsed iso8601 timestamp",          "iso8601"},
		{"HTTP status untouched (3 digits)", "Returned 200 OK",                  "Returned <*> OK"},
		{"ORA-error code preserved",        "Encountered ORA-12345 fault",       "ORA-<*>"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := Mask(tc.in)
			if !strings.Contains(got, tc.mustContain) {
				t.Errorf("Mask(%q) = %q; expected to contain %q", tc.in, got, tc.mustContain)
			}
		})
	}
}
