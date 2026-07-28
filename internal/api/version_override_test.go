package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// version_override_test.go — v0.9.339.
//
// Operator (prod): the login screen shows the version locally but not in
// prod, and they want it settable from the deployment env so correcting it
// does not need an image rebuild.
//
// COREMETRY_VERSION existed and was REMOVED in v0.5.394 for a real reason: a
// stale env value (compose .env, k8s manifest) silently became "the version"
// and nothing anywhere could contradict it, so the login page and
// /api/version reported the override rather than the image.
//
// Bringing it back safely means the override may decide what is DISPLAYED but
// may not erase what the image IS. The endpoint now ships both plus an
// explicit disagreement flag, so a wrong override is visible instead of
// authoritative.
func TestVersionEndpointReportsOverride(t *testing.T) {
	cases := []struct {
		name             string
		version, build   string
		wantVersion      string
		wantBuild        string
		wantOverridden   bool
	}{
		{"no override", "v0.9.339", "v0.9.339", "v0.9.339", "v0.9.339", false},
		// The v0.5.394 scenario: a stale env pointing at an old release. The
		// display honours it — but the payload still carries the truth.
		{"stale override", "v0.9.100", "v0.9.339", "v0.9.100", "v0.9.339", true},
		// Nothing set anywhere: "dev" for both, and NOT flagged as an
		// override — that would cry wolf on every dev build.
		{"unset", "", "", "dev", "dev", false},
		// Build unknown but a display version set (an initContainer wrote
		// /app/VERSION but the ldflag was forgotten): buildVersion falls back
		// to the display value rather than claiming a contradiction.
		{"build unknown", "v0.9.339", "", "v0.9.339", "v0.9.339", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := &Server{version: tc.version, buildVersion: tc.build}
			rr := httptest.NewRecorder()
			s.getVersion(rr, httptest.NewRequest(http.MethodGet, "/api/version", nil))
			if rr.Code != http.StatusOK {
				t.Fatalf("status %d", rr.Code)
			}
			var got struct {
				Version      string `json:"version"`
				BuildVersion string `json:"buildVersion"`
				Overridden   bool   `json:"overridden"`
			}
			if err := json.NewDecoder(rr.Body).Decode(&got); err != nil {
				t.Fatalf("decode: %v", err)
			}
			if got.Version != tc.wantVersion {
				t.Errorf("version = %q, want %q", got.Version, tc.wantVersion)
			}
			if got.BuildVersion != tc.wantBuild {
				t.Errorf("buildVersion = %q, want %q", got.BuildVersion, tc.wantBuild)
			}
			if got.Overridden != tc.wantOverridden {
				t.Errorf("overridden = %v, want %v", got.Overridden, tc.wantOverridden)
			}
		})
	}
}

// `version` must keep its exact meaning: the login page reads that field and
// nothing else, and it renders before any session exists. A rename would make
// the login screen go blank — which is the very symptom being fixed.
func TestVersionFieldStaysBackwardCompatible(t *testing.T) {
	s := &Server{version: "v1.0.0", buildVersion: "v1.0.0"}
	rr := httptest.NewRecorder()
	s.getVersion(rr, httptest.NewRequest(http.MethodGet, "/api/version", nil))
	var raw map[string]any
	if err := json.NewDecoder(rr.Body).Decode(&raw); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if raw["version"] != "v1.0.0" {
		t.Errorf("the `version` field must stay the displayed tag, got %v", raw["version"])
	}
}
