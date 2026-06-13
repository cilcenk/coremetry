package logstore

import "testing"

// resolveESAuth must pick EXACTLY ONE auth method for the ES client. Operator
// connects with an API key only; previously NewES passed Username/Password AND
// APIKey to the client at once, so an api-key install also carried basic-auth
// creds. API key takes precedence and must clear user/pass.
func TestResolveESAuth(t *testing.T) {
	tests := []struct {
		name                      string
		cfg                       ESConfig
		wantKey, wantUser, wantPw string
	}{
		{
			name:    "api key only",
			cfg:     ESConfig{APIKey: "ZW5jb2RlZA=="},
			wantKey: "ZW5jb2RlZA==",
		},
		{
			name:     "basic auth only",
			cfg:      ESConfig{Username: "coremetry", Password: "s3cret"},
			wantUser: "coremetry", wantPw: "s3cret",
		},
		{
			// The bug: both supplied. API key must win and user/pass must be
			// dropped so the request never sends both auth headers.
			name:    "both → api key wins, user/pass dropped",
			cfg:     ESConfig{APIKey: "ZW5jb2RlZA==", Username: "coremetry", Password: "s3cret"},
			wantKey: "ZW5jb2RlZA==",
		},
		{
			name: "no auth",
			cfg:  ESConfig{},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			key, user, pw := resolveESAuth(tc.cfg)
			if key != tc.wantKey || user != tc.wantUser || pw != tc.wantPw {
				t.Errorf("resolveESAuth(%+v) = (key=%q user=%q pw=%q), want (key=%q user=%q pw=%q)",
					tc.cfg, key, user, pw, tc.wantKey, tc.wantUser, tc.wantPw)
			}
		})
	}
}
