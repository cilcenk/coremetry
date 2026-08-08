// Package devops implements a read-only client for an on-prem
// Azure DevOps Server / TFS collection (v0.9.829).
//
// DELIBERATELY NARROW: this is the connection layer only —
// settings persistence, an authenticated HTTP client, and a
// "does it answer?" probe. There is no repo mapping, no stack-
// frame → source resolution and no Copilot integration yet;
// those land in a later slice once the operator's repo-naming
// pattern is known. Nothing in Coremetry consumes this package
// today, so wiring it up changes no existing behaviour.
//
// The secret contract is the tempo.Service one, verbatim: the
// PAT lives in Settings, never in Snapshot, never in an error
// string and never in an audit entry.
package devops

import (
	"context"
	"crypto/tls"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

// Flavor values. On-prem installs differ in which api-version
// they will answer: Azure DevOps Server (2019+) speaks 6.0, the
// older TFS line (2015-2018) tops out around 4.1 and 400s on a
// 6.0 request. "auto" probes rather than making the operator
// know which box they inherited.
const (
	FlavorAuto   = "auto"
	FlavorServer = "azure-devops-server"
	FlavorTFS    = "tfs"
)

// apiVersionFor maps a concrete flavor to the api-version query
// parameter. "auto" has no single answer — the caller expands it
// to a candidate list via candidateFlavors first.
func apiVersionFor(flavor string) string {
	switch flavor {
	case FlavorServer:
		return "6.0"
	case FlavorTFS:
		return "4.1"
	}
	return ""
}

// Settings is the persisted connection config, stored as a JSON
// blob under the system_settings key "devops_connection".
type Settings struct {
	// BaseURL is the server root, with or without the collection.
	// e.g. https://devops.example.local/tfs
	BaseURL string `json:"baseUrl"`
	// Collection is the TFS collection / Azure DevOps organisation
	// segment, e.g. "DefaultCollection". Empty is allowed for
	// installs that already carry it inside BaseURL.
	Collection string `json:"collection,omitempty"`
	// Project is optional. When set, TestConnection verifies the
	// project resolves as well as the collection.
	Project string `json:"project,omitempty"`
	// Username is optional. PAT auth conventionally sends an EMPTY
	// username with the PAT as the password; NTLM-era TFS installs
	// sometimes want a real account name alongside it.
	Username string `json:"username,omitempty"`
	// PAT is the personal access token — the secret. Never echoed
	// in Snapshot(), never interpolated into an error string, never
	// written to audit_log.
	PAT string `json:"pat,omitempty"`
	// Flavor — auto | azure-devops-server | tfs.
	Flavor string `json:"flavor,omitempty"`
	// InsecureSkipVerify relaxes TLS chain verification, for the
	// internal CA / self-signed certs common on on-prem servers.
	InsecureSkipVerify bool `json:"insecureSkipVerify,omitempty"`
	// RepoPrefixes / BranchOrder (v0.9.830) — the service→repo naming
	// convention. Empty = the bundled defaults (see repo_resolve.go).
	//
	// These live in the SAME blob rather than a new settings key: one
	// integration, one row (invariant 5's spirit — no new schema per
	// surface). They are not secrets and DO round-trip through the
	// snapshot, unlike the PAT — the secret contract is unchanged.
	RepoPrefixes []string `json:"repoPrefixes,omitempty"`
	BranchOrder  []string `json:"branchOrder,omitempty"`
}

// Snapshot is the public view returned by GET /api/settings/devops.
// Mirrors Settings with the PAT replaced by a HasPAT signal.
//
// DetectedFlavor / DetectedAPIVersion report what the last
// successful probe of THIS config actually spoke. They are
// in-memory only and deliberately NOT persisted: re-detecting at
// boot costs one request and can never go stale, whereas a
// written-back guess survives a server upgrade and starts lying.
type Snapshot struct {
	BaseURL            string `json:"baseUrl"`
	Collection         string `json:"collection,omitempty"`
	Project            string `json:"project,omitempty"`
	Username           string `json:"username,omitempty"`
	HasPAT             bool   `json:"hasPat"`
	Flavor             string `json:"flavor,omitempty"`
	InsecureSkipVerify bool   `json:"insecureSkipVerify,omitempty"`
	DetectedFlavor     string `json:"detectedFlavor,omitempty"`
	DetectedAPIVersion string `json:"detectedApiVersion,omitempty"`
	// RepoPrefixes / BranchOrder (v0.9.830) — echoed back RESOLVED, i.e.
	// the defaults appear when the operator saved nothing. The card
	// would otherwise render two empty boxes next to a resolver that is
	// quietly using "bsa-" and release→master, and the first question
	// out of a failed lookup would be "but what IS it stripping?".
	RepoPrefixes []string `json:"repoPrefixes,omitempty"`
	BranchOrder  []string `json:"branchOrder,omitempty"`
}

// TestResult is the POST /api/settings/devops/test response.
// Either OK with a project count, or a sanitised error string.
type TestResult struct {
	OK             bool   `json:"ok"`
	DetectedFlavor string `json:"detectedFlavor,omitempty"`
	APIVersion     string `json:"apiVersion,omitempty"`
	ProjectCount   int    `json:"projectCount"`
	// ProjectChecked is true when Settings.Project was set and the
	// project lookup ran, so the UI can say "collection + project OK"
	// rather than implying it verified something it didn't.
	ProjectChecked bool   `json:"projectChecked,omitempty"`
	Error          string `json:"error,omitempty"`
}

// Service holds the live config + a pooled HTTP client.
type Service struct {
	mu  sync.RWMutex
	cfg Settings
	cli *http.Client

	// Last successful probe of the live config. Advisory only.
	detFlavor  string
	detVersion string

	// code — repo-tree cache for the source-window fetcher
	// (v0.9.830). Its own mutex: a recursive listing takes seconds
	// and must not block Snapshot() / the settings page behind it.
	code codeCache
}

func New() *Service {
	return &Service{cli: newDevOpsHTTPClient(false)}
}

// settingsStore is the narrow chstore interface this package
// needs, mirroring tempo.settingsStore so the devops package
// never imports the concrete *chstore.Store.
type settingsStore interface {
	GetDevOpsSettingsRaw(ctx context.Context) ([]byte, error)
	PutDevOpsSettingsRaw(ctx context.Context, raw []byte) error
}

// LoadPersisted hydrates the in-memory config from system_settings.
// Missing blob = empty config (Configured reports false).
func (s *Service) LoadPersisted(ctx context.Context, store settingsStore) error {
	if s == nil || store == nil {
		return nil
	}
	raw, err := store.GetDevOpsSettingsRaw(ctx)
	if err != nil {
		return err
	}
	if len(raw) == 0 {
		return nil
	}
	var cfg Settings
	if err := json.Unmarshal(raw, &cfg); err != nil {
		return fmt.Errorf("devops decode: %w", err)
	}
	s.Configure(cfg)
	return nil
}

// StartConfigRefresh keeps peer pods converged on the shared blob
// (tempo/thanos template). interval ≤ 0 → 30s.
func (s *Service) StartConfigRefresh(ctx context.Context, store settingsStore, interval time.Duration) {
	if s == nil || store == nil {
		return
	}
	if interval <= 0 {
		interval = 30 * time.Second
	}
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			if err := s.LoadPersisted(ctx, store); err != nil {
				log.Printf("[devops] config refresh: %v", err)
			}
		}
	}
}

// SavePersisted writes the typed config to system_settings and
// swaps the live client. The handler merges the stored PAT in
// before calling this — see api.mergeDevOpsSettings.
func (s *Service) SavePersisted(ctx context.Context, store settingsStore, cfg Settings) error {
	if s == nil || store == nil {
		return nil
	}
	raw, err := json.Marshal(cfg)
	if err != nil {
		return err
	}
	if err := store.PutDevOpsSettingsRaw(ctx, raw); err != nil {
		return err
	}
	s.Configure(cfg)
	return nil
}

// Configure swaps the live config. Rebuilds the HTTP client when
// the TLS-verify flag flips so a connection pooled under the old
// certificate policy can't outlive the toggle. Any recorded
// detection is dropped — it described the PREVIOUS endpoint.
func (s *Service) Configure(cfg Settings) {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	prev := s.cfg
	s.cfg = cfg
	if s.cli == nil || prev.InsecureSkipVerify != cfg.InsecureSkipVerify {
		s.cli = newDevOpsHTTPClient(cfg.InsecureSkipVerify)
	}
	if prev.BaseURL != cfg.BaseURL || prev.Collection != cfg.Collection || prev.Flavor != cfg.Flavor {
		s.detFlavor, s.detVersion = "", ""
	}
}

// newDevOpsHTTPClient — 20s ceiling. On-prem TFS behind a slow
// corporate proxy answers a project list in 1-3s; 20s covers the
// cold-IIS-apppool case without letting a black-holed host hold
// an admin request open forever.
func newDevOpsHTTPClient(insecureSkipVerify bool) *http.Client {
	tr := &http.Transport{}
	if insecureSkipVerify {
		tr.TLSClientConfig = &tls.Config{InsecureSkipVerify: true}
	}
	return &http.Client{Timeout: 20 * time.Second, Transport: tr}
}

// Snapshot returns the public config view (no PAT).
func (s *Service) Snapshot() Snapshot {
	if s == nil {
		return Snapshot{}
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	rc := s.resolveConfigLocked()
	return Snapshot{
		BaseURL:            s.cfg.BaseURL,
		Collection:         s.cfg.Collection,
		Project:            s.cfg.Project,
		Username:           s.cfg.Username,
		HasPAT:             s.cfg.PAT != "",
		Flavor:             s.cfg.Flavor,
		InsecureSkipVerify: s.cfg.InsecureSkipVerify,
		DetectedFlavor:     s.detFlavor,
		DetectedAPIVersion: s.detVersion,
		RepoPrefixes:       rc.RepoPrefixes,
		BranchOrder:        rc.BranchOrder,
	}
}

// ResolveConfig returns the service→repo convention with defaults
// already folded in, so callers never have to know what the fallback
// is. Safe on a nil Service (returns the bundled defaults).
func (s *Service) ResolveConfig() ResolveConfig {
	if s == nil {
		return ResolveConfig{}.withDefaults()
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.resolveConfigLocked()
}

// resolveConfigLocked — caller holds at least the read lock.
func (s *Service) resolveConfigLocked() ResolveConfig {
	return ResolveConfig{
		RepoPrefixes: s.cfg.RepoPrefixes,
		BranchOrder:  s.cfg.BranchOrder,
	}.withDefaults()
}

// CurrentSettings returns the full config INCLUDING the PAT.
// Only for the settings handler's stored-secret merge — never
// call this from a path that writes its return value to the wire.
func (s *Service) CurrentSettings() Settings {
	if s == nil {
		return Settings{}
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.cfg
}

// Configured reports whether a server URL has been set.
func (s *Service) Configured() bool {
	if s == nil {
		return false
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	return strings.TrimSpace(s.cfg.BaseURL) != ""
}

// TestConnection probes the LIVE config.
func (s *Service) TestConnection(ctx context.Context) TestResult {
	if s == nil {
		return TestResult{Error: "devops client not available"}
	}
	return s.Test(ctx, s.CurrentSettings())
}

// Test probes a CANDIDATE config without saving or swapping
// anything — the Settings tab's "Test connection" button. On
// success against the live endpoint it records the detected
// flavor so the Snapshot can report it.
func (s *Service) Test(ctx context.Context, cfg Settings) TestResult {
	if strings.TrimSpace(cfg.BaseURL) == "" {
		return TestResult{Error: "server URL required"}
	}
	cli := s.clientFor(cfg.InsecureSkipVerify)
	cands := candidateFlavors(cfg)

	var firstErr error
	for _, flavor := range cands {
		ver := apiVersionFor(flavor)
		count, err := probeProjects(ctx, cli, cfg, ver)
		if err != nil {
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		res := TestResult{
			OK: true, DetectedFlavor: flavor, APIVersion: ver, ProjectCount: count,
		}
		if strings.TrimSpace(cfg.Project) != "" {
			res.ProjectChecked = true
			if err := probeProject(ctx, cli, cfg, ver); err != nil {
				return TestResult{
					DetectedFlavor: flavor, APIVersion: ver, ProjectCount: count,
					ProjectChecked: true, Error: sanitize(err.Error(), cfg),
				}
			}
		}
		s.recordDetection(cfg, flavor, ver)
		return res
	}
	// Every candidate failed. Report the FIRST attempt's error —
	// with "auto" that is the flavor the URL shape pointed at, so
	// the message describes the likely install rather than the
	// fallback we also tried.
	msg := "connection failed"
	if firstErr != nil {
		msg = sanitize(firstErr.Error(), cfg)
	}
	if len(cands) > 1 {
		msg += fmt.Sprintf(" (tried api-version %s and %s)",
			apiVersionFor(cands[0]), apiVersionFor(cands[1]))
	}
	return TestResult{Error: msg}
}

// clientFor returns the pooled live client when the candidate's
// TLS policy matches it, otherwise a throwaway with the right
// policy — a candidate probe must never borrow a connection that
// was established under a different verification setting.
func (s *Service) clientFor(insecure bool) *http.Client {
	if s == nil {
		return newDevOpsHTTPClient(insecure)
	}
	s.mu.RLock()
	cli, cur := s.cli, s.cfg.InsecureSkipVerify
	s.mu.RUnlock()
	if cli != nil && cur == insecure {
		return cli
	}
	return newDevOpsHTTPClient(insecure)
}

// recordDetection stores the probe outcome — but only when the
// candidate IS the live endpoint. Testing an unrelated host from
// the form must not make Snapshot claim a detection for the
// saved one.
func (s *Service) recordDetection(cfg Settings, flavor, ver string) {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.cfg.BaseURL != cfg.BaseURL || s.cfg.Collection != cfg.Collection {
		return
	}
	s.detFlavor, s.detVersion = flavor, ver
}

// candidateFlavors expands the configured flavor into the probe
// order. An explicit flavor is honoured as-is (one attempt, no
// silent fallback — the operator said what they run). "auto"
// tries the shape the URL suggests first, then the other one: a
// `/tfs/` path segment is the on-prem TFS convention, while a
// bare host is far more likely to be a modern Azure DevOps Server.
func candidateFlavors(cfg Settings) []string {
	switch cfg.Flavor {
	case FlavorServer, FlavorTFS:
		return []string{cfg.Flavor}
	}
	if hasTFSSegment(cfg.BaseURL) {
		return []string{FlavorTFS, FlavorServer}
	}
	return []string{FlavorServer, FlavorTFS}
}

// hasTFSSegment reports whether the URL path has a standalone
// "tfs" segment. Segment-wise, not substring: a host called
// tfs-archive.example.com or a collection named "TfsMigration"
// must not flip the guess.
func hasTFSSegment(raw string) bool {
	p := raw
	if u, err := url.Parse(strings.TrimSpace(raw)); err == nil && u.Path != "" {
		p = u.Path
	} else if i := strings.Index(p, "://"); i >= 0 {
		// Unparseable — drop the scheme+host by hand so the host
		// name can't be mistaken for a path segment.
		rest := p[i+3:]
		if j := strings.Index(rest, "/"); j >= 0 {
			p = rest[j:]
		} else {
			p = ""
		}
	}
	for _, seg := range strings.Split(p, "/") {
		if strings.EqualFold(seg, "tfs") {
			return true
		}
	}
	return false
}

// collectionURL builds `{base}/{collection}` with the collection
// path-escaped (names may contain spaces). An empty collection is
// legal — some installs put it inside BaseURL already.
func collectionURL(cfg Settings) string {
	base := strings.TrimRight(strings.TrimSpace(cfg.BaseURL), "/")
	coll := strings.Trim(strings.TrimSpace(cfg.Collection), "/")
	if coll == "" {
		return base
	}
	return base + "/" + url.PathEscape(coll)
}

// projectsResponse is the shape of _apis/projects. `count` is
// authoritative on both Azure DevOps Server and TFS.
type projectsResponse struct {
	Count int `json:"count"`
	Value []struct {
		Name string `json:"name"`
	} `json:"value"`
}

// probeProjects issues GET {base}/{collection}/_apis/projects.
// Returns the project count, or an error describing what the
// server said. Callers sanitise before surfacing.
func probeProjects(ctx context.Context, cli *http.Client, cfg Settings, apiVersion string) (int, error) {
	body, err := doGet(ctx, cli, collectionURL(cfg)+"/_apis/projects?api-version="+apiVersion, cfg)
	if err != nil {
		return 0, err
	}
	var pr projectsResponse
	if err := json.Unmarshal(body, &pr); err != nil {
		return 0, fmt.Errorf("unexpected response (not a project list)")
	}
	if pr.Count == 0 && len(pr.Value) > 0 {
		return len(pr.Value), nil
	}
	return pr.Count, nil
}

// probeProject verifies the optional project resolves inside the
// collection. Uses the dedicated lookup rather than scanning the
// list — _apis/projects pages at 100 by default, so a name-scan
// would report a false "not found" on a large collection.
func probeProject(ctx context.Context, cli *http.Client, cfg Settings, apiVersion string) error {
	proj := strings.Trim(strings.TrimSpace(cfg.Project), "/")
	u := collectionURL(cfg) + "/_apis/projects/" + url.PathEscape(proj) + "?api-version=" + apiVersion
	if _, err := doGet(ctx, cli, u, cfg); err != nil {
		return fmt.Errorf("project %q: %w", proj, err)
	}
	return nil
}

// bodyCap — 1MB. A project list on the largest realistic
// collection is a few hundred KB; anything past this is a server
// error page we have no reason to read in full.
const bodyCap = 1 << 20

// doGet performs the authenticated request and returns the body
// on 2xx, capped at bodyCap.
func doGet(ctx context.Context, cli *http.Client, rawURL string, cfg Settings) ([]byte, error) {
	return doGetCapped(ctx, cli, rawURL, cfg, bodyCap)
}

// doGetCapped is doGet with an explicit read ceiling. The recursive
// repo listing (v0.9.830) legitimately runs to several MB on a large
// monorepo, so it passes its own cap rather than raising the ceiling
// for every probe — a settings-page reachability check has no reason
// to read more than 1MB of a server error page.
//
// Auth is HTTP Basic: PAT in the PASSWORD field with an empty
// username, which is the documented Azure DevOps PAT form (v0.8.451
// uses the same shape for the RAG wiki crawler). When the operator
// supplied a username — NTLM-era TFS setups — it is sent alongside.
func doGetCapped(ctx context.Context, cli *http.Client, rawURL string, cfg Settings, cap int64) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "coremetry-devops/1.0")
	if cfg.PAT != "" || cfg.Username != "" {
		req.SetBasicAuth(cfg.Username, cfg.PAT)
	}
	resp, err := cli.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, cap))
	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		return nil, fmt.Errorf("http %d — check the PAT and its scopes (Code: Read)", resp.StatusCode)
	}
	if resp.StatusCode >= 300 {
		return nil, fmt.Errorf("http %d: %s", resp.StatusCode, firstLine(string(body)))
	}
	// Azure DevOps answers an unauthenticated request with 200/203
	// + an HTML sign-in page rather than a 401 when the endpoint
	// sits behind forms auth. Content-type is the tell; without
	// this check a sign-in page decodes as an empty project list
	// and the test reports a cheerful "0 projects".
	if ct := resp.Header.Get("Content-Type"); ct != "" && !strings.Contains(strings.ToLower(ct), "json") {
		return nil, fmt.Errorf("server returned %s instead of JSON — the endpoint is likely behind a sign-in page", firstLine(ct))
	}
	return body, nil
}

// firstLine trims a server error body to something a settings
// form can render — one line, 200 chars.
func firstLine(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexAny(s, "\r\n"); i >= 0 {
		s = s[:i]
	}
	if len(s) > 200 {
		s = s[:200] + "…"
	}
	return s
}

// sanitize strips credentials from anything on its way to an
// operator-visible string. Two vectors, both real:
//
//  1. userinfo in the URL — Go's *url.Error embeds the request
//     URL, so a base URL pasted as https://user:pat@host/... would
//     print the PAT verbatim on every dial failure.
//  2. the PAT itself, raw or base64 — a proxy that echoes the
//     Authorization header back in its error body.
//
// Belt and braces: this runs on EVERY error the test path
// surfaces, so a future error source can't reopen the hole.
func sanitize(msg string, cfg Settings) string {
	msg = stripUserinfo(msg)
	if cfg.PAT != "" {
		msg = strings.ReplaceAll(msg, cfg.PAT, "***")
		enc := base64.StdEncoding.EncodeToString([]byte(cfg.Username + ":" + cfg.PAT))
		msg = strings.ReplaceAll(msg, enc, "***")
	}
	return msg
}

// stripUserinfo rewrites "scheme://user:secret@host" to
// "scheme://host" anywhere in a string. Hand-rolled rather than
// regex-driven so it stays allocation-cheap on the happy path
// (no "://" → return unchanged).
func stripUserinfo(msg string) string {
	var b strings.Builder
	rest := msg
	for {
		i := strings.Index(rest, "://")
		if i < 0 {
			b.WriteString(rest)
			return b.String()
		}
		b.WriteString(rest[:i+3])
		rest = rest[i+3:]
		// The authority ends at the first /?# — an @ after that is
		// part of a path, not credentials.
		end := strings.IndexAny(rest, "/?# \t\"")
		if end < 0 {
			end = len(rest)
		}
		if at := strings.LastIndex(rest[:end], "@"); at >= 0 {
			rest = rest[at+1:]
		}
	}
}
