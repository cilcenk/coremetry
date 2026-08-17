package api

// v0.9.1150 — Settings → Metrics backend.
//
// mergeVMSettings holds the one rule that is invisible in review and
// expensive when wrong: an EMPTY token in the payload preserves the
// stored one. The Settings form never echoes the token back (it cannot —
// the GET snapshot masks it), so every save the operator makes after the
// first one submits token:"". If that blanked the stored value, auth
// would silently switch off on the next read and the failure would look
// like a VM permissions problem.

import (
	"strings"
	"testing"

	"github.com/cilcenk/coremetry/internal/vmetrics"
)

func TestMergeVMSettings(t *testing.T) {
	stored := vmetrics.Settings{
		Enabled: true, BaseURL: "http://old:8428",
		AuthType: "bearer", Token: "stored-token",
	}
	tests := []struct {
		name    string
		in      vmSettingsInput
		cur     vmetrics.Settings
		want    vmetrics.Settings
		wantBad string
	}{
		{
			// THE rule. A form save with no token typed must keep the token.
			name: "empty token preserves the stored one",
			in:   vmSettingsInput{Enabled: true, BaseURL: "http://new:8428", AuthType: "bearer"},
			cur:  stored,
			want: vmetrics.Settings{Enabled: true, BaseURL: "http://new:8428", AuthType: "bearer", Token: "stored-token"},
		},
		{
			name: "a new token replaces the stored one",
			in:   vmSettingsInput{Enabled: true, BaseURL: "http://new:8428", AuthType: "bearer", Token: "fresh"},
			cur:  stored,
			want: vmetrics.Settings{Enabled: true, BaseURL: "http://new:8428", AuthType: "bearer", Token: "fresh"},
		},
		{
			// Disabling must not require retyping the URL or the token —
			// the operator may want to flip it back on later.
			name: "disable keeps url and token",
			in:   vmSettingsInput{Enabled: false, BaseURL: "http://old:8428", AuthType: "bearer"},
			cur:  stored,
			want: vmetrics.Settings{Enabled: false, BaseURL: "http://old:8428", AuthType: "bearer", Token: "stored-token"},
		},
		{
			name: "url is trimmed",
			in:   vmSettingsInput{Enabled: true, BaseURL: "  http://vm:8428  ", AuthType: " none "},
			want: vmetrics.Settings{Enabled: true, BaseURL: "http://vm:8428", AuthType: "none"},
		},
		{
			name: "empty authType is allowed (means none)",
			in:   vmSettingsInput{Enabled: true, BaseURL: "http://vm:8428"},
			want: vmetrics.Settings{Enabled: true, BaseURL: "http://vm:8428"},
		},
		{
			name: "skip-tls flag carries",
			in:   vmSettingsInput{Enabled: true, BaseURL: "http://vm:8428", InsecureSkipVerify: true},
			want: vmetrics.Settings{Enabled: true, BaseURL: "http://vm:8428", InsecureSkipVerify: true},
		},
		{
			// Enabled with no URL would route every metric surface at a
			// backend that cannot answer, and there is no fallback.
			name:    "enabled without a url is rejected",
			in:      vmSettingsInput{Enabled: true},
			wantBad: "baseUrl required",
		},
		{
			name:    "enabled with a whitespace url is rejected",
			in:      vmSettingsInput{Enabled: true, BaseURL: "   "},
			wantBad: "baseUrl required",
		},
		{
			// Disabled + empty URL is legal: that is the factory state.
			name: "disabled without a url is fine",
			in:   vmSettingsInput{},
			want: vmetrics.Settings{},
		},
		{
			name:    "basic auth is rejected (VM path has no basic)",
			in:      vmSettingsInput{Enabled: true, BaseURL: "http://vm:8428", AuthType: "basic"},
			wantBad: "authType must be one of: none, bearer",
		},
		{
			name:    "unknown authType is rejected",
			in:      vmSettingsInput{Enabled: true, BaseURL: "http://vm:8428", AuthType: "oauth2"},
			wantBad: "authType must be one of: none, bearer",
		},

		// ── v0.9.1164: the two query-shaping knobs ──────────────────────────
		//
		// Unlike the token these have NO preserve-on-empty rule: 0 and false
		// are meaningful ("default floor", "guard on"), so they must land in
		// the config exactly as submitted. A preserve rule here would make the
		// guard impossible to switch back ON from the form.
		{
			name: "rate window floor carries",
			in:   vmSettingsInput{Enabled: true, BaseURL: "http://vm:8428", RateWindowFloorS: 60},
			cur:  stored,
			want: vmetrics.Settings{Enabled: true, BaseURL: "http://vm:8428", Token: "stored-token", RateWindowFloorS: 60},
		},
		{
			name: "zero floor is the unset sentinel, not a rejection",
			in:   vmSettingsInput{Enabled: true, BaseURL: "http://vm:8428", RateWindowFloorS: 0},
			want: vmetrics.Settings{Enabled: true, BaseURL: "http://vm:8428"},
		},
		{
			name: "a stored floor is REPLACED by zero, never preserved",
			in:   vmSettingsInput{Enabled: true, BaseURL: "http://vm:8428"},
			cur:  vmetrics.Settings{RateWindowFloorS: 600},
			want: vmetrics.Settings{Enabled: true, BaseURL: "http://vm:8428"},
		},
		{
			name: "the lower bound is accepted",
			in:   vmSettingsInput{Enabled: true, BaseURL: "http://vm:8428", RateWindowFloorS: vmetrics.MinRateWindowFloorSec},
			want: vmetrics.Settings{Enabled: true, BaseURL: "http://vm:8428", RateWindowFloorS: vmetrics.MinRateWindowFloorSec},
		},
		{
			name: "the upper bound is accepted",
			in:   vmSettingsInput{Enabled: true, BaseURL: "http://vm:8428", RateWindowFloorS: vmetrics.MaxRateWindowFloorSec},
			want: vmetrics.Settings{Enabled: true, BaseURL: "http://vm:8428", RateWindowFloorS: vmetrics.MaxRateWindowFloorSec},
		},
		{
			// REJECTED, not clamped. resolveRateWindowFloor would ignore an
			// out-of-bounds value and use the default, so accepting it here
			// would show the operator their number in Settings while the chart
			// used another — with nothing on screen connecting the two.
			name:    "a floor below the bound is rejected",
			in:      vmSettingsInput{Enabled: true, BaseURL: "http://vm:8428", RateWindowFloorS: 9},
			wantBad: "rateWindowFloorS must be 0",
		},
		{
			name:    "a floor above the bound is rejected",
			in:      vmSettingsInput{Enabled: true, BaseURL: "http://vm:8428", RateWindowFloorS: 3601},
			wantBad: "rateWindowFloorS must be 0",
		},
		{
			name:    "a negative floor is rejected",
			in:      vmSettingsInput{Enabled: true, BaseURL: "http://vm:8428", RateWindowFloorS: -60},
			wantBad: "rateWindowFloorS must be 0",
		},
		{
			name:    "the rejection names the bounds so the operator can fix it",
			in:      vmSettingsInput{Enabled: true, BaseURL: "http://vm:8428", RateWindowFloorS: 5},
			wantBad: "between 10 and 3600 seconds",
		},
		{
			name: "the guard can be lifted",
			in:   vmSettingsInput{Enabled: true, BaseURL: "http://vm:8428", AllowUnfilteredPercentiles: true},
			want: vmetrics.Settings{Enabled: true, BaseURL: "http://vm:8428", AllowUnfilteredPercentiles: true},
		},
		{
			// The direction that matters: a form that cannot switch the guard
			// back on is a one-way door.
			name: "and put back — false is submitted, not inherited",
			in:   vmSettingsInput{Enabled: true, BaseURL: "http://vm:8428"},
			cur:  vmetrics.Settings{AllowUnfilteredPercentiles: true},
			want: vmetrics.Settings{Enabled: true, BaseURL: "http://vm:8428"},
		},
		{
			name: "both knobs together, with the token preserved",
			in: vmSettingsInput{Enabled: true, BaseURL: "http://vm:8428", AuthType: "bearer",
				RateWindowFloorS: 30, AllowUnfilteredPercentiles: true},
			cur: stored,
			want: vmetrics.Settings{Enabled: true, BaseURL: "http://vm:8428", AuthType: "bearer",
				Token: "stored-token", RateWindowFloorS: 30, AllowUnfilteredPercentiles: true},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, bad := mergeVMSettings(tc.in, tc.cur)
			if tc.wantBad != "" {
				if !strings.Contains(bad, tc.wantBad) {
					t.Fatalf("bad = %q, want it to contain %q", bad, tc.wantBad)
				}
				return
			}
			if bad != "" {
				t.Fatalf("unexpected rejection: %s", bad)
			}
			if got != tc.want {
				t.Fatalf("got  %+v\nwant %+v", got, tc.want)
			}
		})
	}
}

// The audit details must never carry the token. Checked structurally
// against the Snapshot type (which has no token field) plus the handler's
// detail keys, because "we remembered not to log it" is not a property.
func TestVMSnapshotHasNoTokenField(t *testing.T) {
	svc := vmetrics.New()
	svc.Configure(vmetrics.Settings{
		Enabled: true, BaseURL: "http://vm:8428",
		AuthType: "bearer", Token: "secret-value",
	})
	snap := svc.Snapshot()
	if !snap.HasToken {
		t.Fatal("hasToken must be true")
	}
	// If someone adds a Token field to Snapshot, putVMSettings' audit
	// details (built from snap) would start carrying it.
	if strings.Contains(snapshotFields(snap), "secret-value") {
		t.Fatal("token reachable from the Snapshot the audit details are built from")
	}
}

func snapshotFields(s vmetrics.Snapshot) string {
	return strings.Join([]string{s.BaseURL, s.AuthType}, "|")
}
