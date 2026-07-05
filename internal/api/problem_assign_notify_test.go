package api

import "testing"

// v0.8.289 — operator asked for an email to the assignee when a Problem is
// assigned to a person. Contract of assigneeNotifyEmail (the pure gate before
// the SMTP send):
//   - notify ONLY when the new assignee is a person (contains '@'); a team
//     name auto-set from service_metadata (e.g. "payments") is not a person;
//   - notify ONLY on an actual change — re-saving the same assignee (any case)
//     must not re-send;
//   - empty/whitespace new assignee (an unassign) never notifies.
func TestAssigneeNotifyEmail(t *testing.T) {
	cases := []struct {
		name          string
		newA, oldA    string
		wantEmail     string
		wantSend      bool
	}{
		{"person newly assigned", "alice@corp.com", "", "alice@corp.com", true},
		{"person replaces a team", "bob@corp.com", "payments", "bob@corp.com", true},
		{"person replaces another person", "carol@corp.com", "bob@corp.com", "carol@corp.com", true},
		{"same person re-saved = no resend", "alice@corp.com", "alice@corp.com", "", false},
		{"same person different case = no resend", "Alice@Corp.com", "alice@corp.com", "", false},
		{"team assignment does not notify", "payments", "", "", false},
		{"unassign does not notify", "", "alice@corp.com", "", false},
		{"whitespace is trimmed to unassign", "   ", "alice@corp.com", "", false},
		{"leading/trailing space on a person is trimmed", "  dan@corp.com  ", "", "dan@corp.com", true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			email, send := assigneeNotifyEmail(c.newA, c.oldA)
			if email != c.wantEmail || send != c.wantSend {
				t.Fatalf("assigneeNotifyEmail(%q, %q) = (%q, %v), want (%q, %v)",
					c.newA, c.oldA, email, send, c.wantEmail, c.wantSend)
			}
		})
	}
}
