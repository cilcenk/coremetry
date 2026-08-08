package devops

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// v0.9.829 — Azure DevOps / TFS bağlantı katmanı.
//
// Every server in this file is an httptest.Server on 127.0.0.1.
// No customer host, collection or project name appears anywhere in
// the repo — the fixtures are deliberately generic ("ProjA").

// ── flavor → api-version ────────────────────────────────────────

func TestAPIVersionFor(t *testing.T) {
	cases := []struct {
		flavor string
		want   string
	}{
		{FlavorServer, "6.0"},
		{FlavorTFS, "4.1"},
		{FlavorAuto, ""}, // auto has no single version — expanded first
		{"", ""},
		{"nonsense", ""},
	}
	for _, c := range cases {
		if got := apiVersionFor(c.flavor); got != c.want {
			t.Errorf("apiVersionFor(%q) = %q, want %q", c.flavor, got, c.want)
		}
	}
}

// ── auto-detect: probe ORDER from the URL shape ─────────────────

func TestCandidateFlavors(t *testing.T) {
	cases := []struct {
		name string
		cfg  Settings
		want []string
	}{
		{
			name: "explicit server — one attempt, no fallback",
			cfg:  Settings{BaseURL: "https://dev.example.local/tfs", Flavor: FlavorServer},
			want: []string{FlavorServer},
		},
		{
			name: "explicit tfs — one attempt, no fallback",
			cfg:  Settings{BaseURL: "https://dev.example.local", Flavor: FlavorTFS},
			want: []string{FlavorTFS},
		},
		{
			name: "auto + /tfs/ segment → tfs first",
			cfg:  Settings{BaseURL: "https://dev.example.local/tfs/", Flavor: FlavorAuto},
			want: []string{FlavorTFS, FlavorServer},
		},
		{
			name: "auto + /tfs segment, no trailing slash → tfs first",
			cfg:  Settings{BaseURL: "https://dev.example.local/tfs", Flavor: FlavorAuto},
			want: []string{FlavorTFS, FlavorServer},
		},
		{
			name: "auto + plain URL → server first",
			cfg:  Settings{BaseURL: "https://dev.example.local", Flavor: FlavorAuto},
			want: []string{FlavorServer, FlavorTFS},
		},
		{
			name: "empty flavor behaves as auto",
			cfg:  Settings{BaseURL: "https://dev.example.local/tfs"},
			want: []string{FlavorTFS, FlavorServer},
		},
		{
			// Substring matches must NOT flip the guess — this is the
			// bug a naive strings.Contains(url, "tfs") would ship.
			name: "auto + host containing tfs → server first",
			cfg:  Settings{BaseURL: "https://tfs-archive.example.local", Flavor: FlavorAuto},
			want: []string{FlavorServer, FlavorTFS},
		},
		{
			name: "auto + path segment merely PREFIXED tfs → server first",
			cfg:  Settings{BaseURL: "https://dev.example.local/tfsmigration", Flavor: FlavorAuto},
			want: []string{FlavorServer, FlavorTFS},
		},
		{
			name: "auto + uppercase TFS segment → tfs first",
			cfg:  Settings{BaseURL: "https://dev.example.local/TFS/DefaultCollection", Flavor: FlavorAuto},
			want: []string{FlavorTFS, FlavorServer},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := candidateFlavors(c.cfg)
			if strings.Join(got, ",") != strings.Join(c.want, ",") {
				t.Errorf("candidateFlavors(%q, %q) = %v, want %v",
					c.cfg.BaseURL, c.cfg.Flavor, got, c.want)
			}
		})
	}
}

// ── TestConnection over a generic httptest server ───────────────

// projectsHandler serves _apis/projects for the api-versions in
// `accept`, and 400s everything else — the way a real TFS box
// rejects an api-version it doesn't implement. Records the
// api-versions it was asked for.
func projectsHandler(t *testing.T, accept map[string]bool, seen *[]string) http.Handler {
	t.Helper()
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ver := r.URL.Query().Get("api-version")
		*seen = append(*seen, ver)
		if !accept[ver] {
			w.WriteHeader(http.StatusBadRequest)
			fmt.Fprintf(w, "unsupported api-version %s", ver)
			return
		}
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		switch {
		case strings.HasSuffix(r.URL.Path, "/_apis/projects"):
			fmt.Fprint(w, `{"count":2,"value":[{"name":"ProjA"},{"name":"ProjB"}]}`)
		case strings.Contains(r.URL.Path, "/_apis/projects/ProjA"):
			fmt.Fprint(w, `{"id":"1","name":"ProjA"}`)
		default:
			w.WriteHeader(http.StatusNotFound)
			fmt.Fprint(w, `{"message":"project does not exist"}`)
		}
	})
}

func TestTestConnection_SuccessPaths(t *testing.T) {
	cases := []struct {
		name        string
		accept      map[string]bool
		flavor      string
		wantFlavor  string
		wantVersion string
		wantProbes  []string
	}{
		{
			name:        "explicit server speaks 6.0",
			accept:      map[string]bool{"6.0": true},
			flavor:      FlavorServer,
			wantFlavor:  FlavorServer,
			wantVersion: "6.0",
			wantProbes:  []string{"6.0"},
		},
		{
			name:        "explicit tfs speaks 4.1",
			accept:      map[string]bool{"4.1": true},
			flavor:      FlavorTFS,
			wantFlavor:  FlavorTFS,
			wantVersion: "4.1",
			wantProbes:  []string{"4.1"},
		},
		{
			name:        "auto on a plain URL lands on 6.0 first try",
			accept:      map[string]bool{"6.0": true},
			flavor:      FlavorAuto,
			wantFlavor:  FlavorServer,
			wantVersion: "6.0",
			wantProbes:  []string{"6.0"},
		},
		{
			// The ping-fallback path: URL shape says Azure DevOps
			// Server, the box only answers 4.1, so the second probe
			// wins and we report what actually answered.
			name:        "auto falls back to 4.1 when 6.0 is refused",
			accept:      map[string]bool{"4.1": true},
			flavor:      FlavorAuto,
			wantFlavor:  FlavorTFS,
			wantVersion: "4.1",
			wantProbes:  []string{"6.0", "4.1"},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var seen []string
			srv := httptest.NewServer(projectsHandler(t, c.accept, &seen))
			defer srv.Close()

			s := New()
			res := s.Test(context.Background(), Settings{
				BaseURL: srv.URL, Collection: "DefaultCollection",
				PAT: "s3cr3t-pat", Flavor: c.flavor,
			})
			if !res.OK {
				t.Fatalf("Test() not ok: %s", res.Error)
			}
			if res.DetectedFlavor != c.wantFlavor || res.APIVersion != c.wantVersion {
				t.Errorf("detected %s/%s, want %s/%s",
					res.DetectedFlavor, res.APIVersion, c.wantFlavor, c.wantVersion)
			}
			if res.ProjectCount != 2 {
				t.Errorf("projectCount = %d, want 2", res.ProjectCount)
			}
			if strings.Join(seen, ",") != strings.Join(c.wantProbes, ",") {
				t.Errorf("probed api-versions %v, want %v", seen, c.wantProbes)
			}
		})
	}
}

// An explicit flavor must NOT silently fall back — the operator
// said what they run, and a "works anyway" answer would hide a
// misconfiguration that the later repo-mapping slice depends on.
func TestTestConnection_ExplicitFlavorDoesNotFallBack(t *testing.T) {
	var seen []string
	srv := httptest.NewServer(projectsHandler(t, map[string]bool{"4.1": true}, &seen))
	defer srv.Close()

	s := New()
	res := s.Test(context.Background(), Settings{
		BaseURL: srv.URL, Collection: "DefaultCollection", Flavor: FlavorServer,
	})
	if res.OK {
		t.Fatal("explicit azure-devops-server should not succeed against a 4.1-only server")
	}
	if strings.Join(seen, ",") != "6.0" {
		t.Errorf("probed %v, want only 6.0 (no fallback)", seen)
	}
	if strings.Contains(res.Error, "tried api-version") {
		t.Errorf("explicit flavor must not report a two-version sweep: %q", res.Error)
	}
}

func TestTestConnection_ProjectVerification(t *testing.T) {
	var seen []string
	srv := httptest.NewServer(projectsHandler(t, map[string]bool{"6.0": true}, &seen))
	defer srv.Close()
	s := New()

	ok := s.Test(context.Background(), Settings{
		BaseURL: srv.URL, Collection: "DefaultCollection",
		Project: "ProjA", Flavor: FlavorServer,
	})
	if !ok.OK || !ok.ProjectChecked {
		t.Fatalf("known project should verify: ok=%v checked=%v err=%s",
			ok.OK, ok.ProjectChecked, ok.Error)
	}

	bad := s.Test(context.Background(), Settings{
		BaseURL: srv.URL, Collection: "DefaultCollection",
		Project: "NoSuchProject", Flavor: FlavorServer,
	})
	if bad.OK {
		t.Fatal("unknown project should fail the test")
	}
	if !bad.ProjectChecked {
		t.Error("ProjectChecked should stay true on the project-lookup failure")
	}
	// The collection probe succeeded — say so, so the operator can
	// tell a credential problem from a project typo.
	if bad.ProjectCount != 2 {
		t.Errorf("projectCount = %d, want the collection count 2", bad.ProjectCount)
	}
	if !strings.Contains(bad.Error, "NoSuchProject") {
		t.Errorf("error should name the project: %q", bad.Error)
	}
}

// A sign-in page served with 200 must not decode as "0 projects".
func TestTestConnection_HTMLSignInPageIsNotSuccess(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		fmt.Fprint(w, `<html><body>Sign in to continue</body></html>`)
	}))
	defer srv.Close()

	res := New().Test(context.Background(), Settings{
		BaseURL: srv.URL, Collection: "DefaultCollection", Flavor: FlavorServer,
	})
	if res.OK {
		t.Fatal("an HTML sign-in page must not report ok")
	}
	if !strings.Contains(res.Error, "sign-in page") {
		t.Errorf("error should explain the sign-in page: %q", res.Error)
	}
}

// ── auth shape: PAT in the PASSWORD field ───────────────────────

func TestBasicAuthShape(t *testing.T) {
	cases := []struct {
		name     string
		username string
		pat      string
		wantUser string
	}{
		// The documented Azure DevOps PAT form (v0.8.451 RAG crawler
		// uses the same shape): empty username, PAT as the password.
		{"pat only — empty username", "", "s3cr3t-pat", ""},
		{"ntlm-era tfs — username alongside", "svc_coremetry", "s3cr3t-pat", "svc_coremetry"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var gotUser, gotPass string
			var gotOK bool
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				gotUser, gotPass, gotOK = r.BasicAuth()
				w.Header().Set("Content-Type", "application/json")
				fmt.Fprint(w, `{"count":1,"value":[{"name":"ProjA"}]}`)
			}))
			defer srv.Close()

			res := New().Test(context.Background(), Settings{
				BaseURL: srv.URL, Collection: "DefaultCollection",
				Username: c.username, PAT: c.pat, Flavor: FlavorServer,
			})
			if !res.OK {
				t.Fatalf("Test() not ok: %s", res.Error)
			}
			if !gotOK {
				t.Fatal("no Basic Authorization header was sent")
			}
			if gotUser != c.wantUser {
				t.Errorf("basic-auth username = %q, want %q", gotUser, c.wantUser)
			}
			if gotPass != c.pat {
				t.Errorf("PAT must ride in the password field, got %q", gotPass)
			}
		})
	}
}

// No Authorization header at all when nothing is configured —
// an anonymous on-prem collection stays usable.
func TestNoAuthHeaderWhenUnconfigured(t *testing.T) {
	var sawAuth bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sawAuth = r.Header.Get("Authorization") != ""
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"count":0,"value":[]}`)
	}))
	defer srv.Close()

	res := New().Test(context.Background(), Settings{
		BaseURL: srv.URL, Collection: "DefaultCollection", Flavor: FlavorServer,
	})
	if !res.OK {
		t.Fatalf("Test() not ok: %s", res.Error)
	}
	if sawAuth {
		t.Error("no credentials configured — no Authorization header should be sent")
	}
}

// ── the PIN: the PAT never reaches an operator-visible string ───

const patPin = "SUPERSECRETPAT1234567890"

func TestPATNeverLeaksIntoErrors(t *testing.T) {
	patB64 := base64.StdEncoding.EncodeToString([]byte(":" + patPin))

	cases := []struct {
		name string
		cfg  Settings
		srv  func() *httptest.Server
	}{
		{
			name: "401 from the server",
			cfg:  Settings{Collection: "DefaultCollection", PAT: patPin, Flavor: FlavorServer},
			srv: func() *httptest.Server {
				return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					w.WriteHeader(http.StatusUnauthorized)
				}))
			},
		},
		{
			// A proxy that echoes the request headers into its error
			// body — the reason sanitize() scrubs the base64 form too.
			name: "proxy echoes the Authorization header back",
			cfg:  Settings{Collection: "DefaultCollection", PAT: patPin, Flavor: FlavorServer},
			srv: func() *httptest.Server {
				return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					w.WriteHeader(http.StatusBadGateway)
					fmt.Fprintf(w, "upstream refused: %s", r.Header.Get("Authorization"))
				}))
			},
		},
		{
			name: "project lookup fails",
			cfg: Settings{
				Collection: "DefaultCollection", Project: "NoSuchProject",
				PAT: patPin, Flavor: FlavorServer,
			},
			srv: func() *httptest.Server {
				var seen []string
				return httptest.NewServer(projectsHandler(t, map[string]bool{"6.0": true}, &seen))
			},
		},
		{
			name: "auto sweep where both versions fail",
			cfg:  Settings{Collection: "DefaultCollection", PAT: patPin, Flavor: FlavorAuto},
			srv: func() *httptest.Server {
				return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					w.WriteHeader(http.StatusInternalServerError)
					fmt.Fprintf(w, "boom %s", r.Header.Get("Authorization"))
				}))
			},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			srv := c.srv()
			defer srv.Close()
			cfg := c.cfg
			cfg.BaseURL = srv.URL

			res := New().Test(context.Background(), cfg)
			if res.OK {
				t.Fatal("expected a failure so there is an error string to inspect")
			}
			if res.Error == "" {
				t.Fatal("failure with an empty error string")
			}
			if strings.Contains(res.Error, patPin) {
				t.Fatalf("PAT leaked into the error: %q", res.Error)
			}
			if strings.Contains(res.Error, patB64) {
				t.Fatalf("base64 PAT leaked into the error: %q", res.Error)
			}
		})
	}
}

// The dial-failure path is where a PAT pasted into the URL as
// userinfo would surface: Go's *url.Error prints the whole request
// URL. Unreachable host, so no server here.
func TestPATInURLUserinfoNeverLeaks(t *testing.T) {
	// 127.0.0.1:1 — connection refused immediately, no DNS, no wait.
	res := New().Test(context.Background(), Settings{
		BaseURL:    "http://admin:" + patPin + "@127.0.0.1:1",
		Collection: "DefaultCollection", Flavor: FlavorServer,
	})
	if res.OK {
		t.Fatal("expected a dial failure")
	}
	if strings.Contains(res.Error, patPin) {
		t.Fatalf("URL-embedded PAT leaked into the error: %q", res.Error)
	}
	if strings.Contains(res.Error, "admin:") {
		t.Fatalf("URL userinfo leaked into the error: %q", res.Error)
	}
}

func TestStripUserinfo(t *testing.T) {
	cases := []struct{ in, want string }{
		{"Get \"https://u:p@host/x\": dial error", "Get \"https://host/x\": dial error"},
		{"https://host/path", "https://host/path"},
		// An @ in the PATH is not credentials.
		{"https://host/a@b", "https://host/a@b"},
		{"no url here", "no url here"},
		{"https://u:p@host", "https://host"},
		{"https://u:p@host?q=1", "https://host?q=1"},
		// Two URLs in one message — both must be scrubbed.
		{"a https://u:p@h1/x b https://v:q@h2/y", "a https://h1/x b https://h2/y"},
	}
	for _, c := range cases {
		if got := stripUserinfo(c.in); got != c.want {
			t.Errorf("stripUserinfo(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

// ── Snapshot never carries the PAT ──────────────────────────────

func TestSnapshotMasksPAT(t *testing.T) {
	s := New()
	s.Configure(Settings{
		BaseURL: "https://dev.example.local/tfs", Collection: "DefaultCollection",
		Username: "svc", PAT: patPin, Flavor: FlavorAuto,
	})
	snap := s.Snapshot()
	if !snap.HasPAT {
		t.Error("hasPat should be true when a PAT is stored")
	}
	// Marshal it the way the handler does — the wire form is what
	// matters, a struct field is only half the contract.
	blob := mustJSON(t, snap)
	if strings.Contains(blob, patPin) {
		t.Fatalf("PAT reached the GET response body: %s", blob)
	}
	if !strings.Contains(blob, `"hasPat":true`) {
		t.Errorf("snapshot should expose hasPat: %s", blob)
	}

	s.Configure(Settings{BaseURL: "https://dev.example.local/tfs"})
	if s.Snapshot().HasPAT {
		t.Error("hasPat should be false once the PAT is gone")
	}
}

// Detection is reported, never persisted — a written-back guess
// survives a server upgrade and starts lying.
func TestDetectionIsReportedNotPersisted(t *testing.T) {
	var seen []string
	srv := httptest.NewServer(projectsHandler(t, map[string]bool{"4.1": true}, &seen))
	defer srv.Close()

	s := New()
	cfg := Settings{BaseURL: srv.URL, Collection: "DefaultCollection", Flavor: FlavorAuto}
	s.Configure(cfg)

	if snap := s.Snapshot(); snap.DetectedFlavor != "" {
		t.Errorf("no detection before a probe, got %q", snap.DetectedFlavor)
	}
	if res := s.TestConnection(context.Background()); !res.OK {
		t.Fatalf("probe failed: %s", res.Error)
	}
	snap := s.Snapshot()
	if snap.DetectedFlavor != FlavorTFS || snap.DetectedAPIVersion != "4.1" {
		t.Errorf("detected %s/%s, want tfs/4.1", snap.DetectedFlavor, snap.DetectedAPIVersion)
	}
	// The SAVED flavor is untouched — still "auto".
	if snap.Flavor != FlavorAuto {
		t.Errorf("stored flavor changed to %q — detection must not write back", snap.Flavor)
	}
	if s.CurrentSettings().Flavor != FlavorAuto {
		t.Error("detection wrote itself into Settings")
	}

	// Repointing at a different server drops the stale detection.
	s.Configure(Settings{BaseURL: "https://other.example.local", Collection: "DefaultCollection"})
	if got := s.Snapshot().DetectedFlavor; got != "" {
		t.Errorf("detection survived a URL change: %q", got)
	}
}

// Probing an endpoint that is NOT the saved one must not make the
// snapshot claim a detection for the saved server.
func TestDetectionNotRecordedForForeignCandidate(t *testing.T) {
	var seen []string
	srv := httptest.NewServer(projectsHandler(t, map[string]bool{"6.0": true}, &seen))
	defer srv.Close()

	s := New()
	s.Configure(Settings{BaseURL: "https://saved.example.local", Collection: "DefaultCollection"})

	res := s.Test(context.Background(), Settings{
		BaseURL: srv.URL, Collection: "DefaultCollection", Flavor: FlavorServer,
	})
	if !res.OK {
		t.Fatalf("probe failed: %s", res.Error)
	}
	if got := s.Snapshot().DetectedFlavor; got != "" {
		t.Errorf("a foreign candidate recorded a detection for the saved config: %q", got)
	}
}

// ── URL assembly ────────────────────────────────────────────────

func TestCollectionURL(t *testing.T) {
	cases := []struct {
		name string
		cfg  Settings
		want string
	}{
		{"plain", Settings{BaseURL: "https://d.example.local", Collection: "DefaultCollection"},
			"https://d.example.local/DefaultCollection"},
		{"trailing slash on base", Settings{BaseURL: "https://d.example.local/tfs/", Collection: "DefaultCollection"},
			"https://d.example.local/tfs/DefaultCollection"},
		{"collection folded into base", Settings{BaseURL: "https://d.example.local/tfs/DefaultCollection"},
			"https://d.example.local/tfs/DefaultCollection"},
		{"space in collection name", Settings{BaseURL: "https://d.example.local", Collection: "My Collection"},
			"https://d.example.local/My%20Collection"},
		{"slashes trimmed off collection", Settings{BaseURL: "https://d.example.local", Collection: "/DefaultCollection/"},
			"https://d.example.local/DefaultCollection"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := collectionURL(c.cfg); got != c.want {
				t.Errorf("collectionURL = %q, want %q", got, c.want)
			}
		})
	}
}

func TestConfigured(t *testing.T) {
	var nilSvc *Service
	if nilSvc.Configured() {
		t.Error("nil service must not report configured")
	}
	s := New()
	if s.Configured() {
		t.Error("fresh service must not report configured")
	}
	s.Configure(Settings{BaseURL: "   "})
	if s.Configured() {
		t.Error("whitespace-only URL must not report configured")
	}
	s.Configure(Settings{BaseURL: "https://d.example.local"})
	if !s.Configured() {
		t.Error("URL set — should report configured")
	}
}

func mustJSON(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return string(b)
}
