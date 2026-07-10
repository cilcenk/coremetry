package chstore

import (
	"context"
	"crypto/sha1"
	"encoding/hex"
	"fmt"
	"regexp"
	"strings"
	"time"
)

// ── Exception Inbox ─────────────────────────────────────────────────────────
//
// On top of the raw exception events in the spans table, we track each
// distinct (type, message, service) tuple as a stateful "group" — same
// idea as Sentry's issues or New Relic's Errors Inbox. State transitions:
//
//   new        → on first occurrence
//   acknowledged → admin or assignee marked "I'm on it"
//   resolved   → admin closed it; if it occurs again later it auto-flips
//                to "regressed" (still open, but flagged)
//   regressed  → reopened by a fresh occurrence after resolve
//   ignored    → don't surface in the inbox; raw events still persist

// Possible states. The frontend filters on these.
const (
	ExStateNew          = "new"
	ExStateAcknowledged = "acknowledged"
	ExStateResolved     = "resolved"
	ExStateRegressed    = "regressed"
	ExStateIgnored      = "ignored"
)

type ExceptionGroup struct {
	Fingerprint string `json:"fingerprint"`
	Type        string `json:"type"`
	Message     string `json:"message"`
	Service     string `json:"service"`
	State       string `json:"state"`
	Assignee    string `json:"assignee"`
	FirstSeen   int64  `json:"firstSeen"`   // unix ns
	LastSeen    int64  `json:"lastSeen"`    // unix ns
	ResolvedAt  *int64 `json:"resolvedAt,omitempty"`
	Occurrences uint64 `json:"occurrences"`
	Notes       string `json:"notes"`
}

// FingerprintException computes a stable identifier for "the same
// exception" across many occurrences. Strategy mirrors Sentry/Honeybadger:
//
//  1. If a stacktrace is available, hash the top 5 frame identifiers
//     (class.method, line numbers stripped) — code path is the most
//     stable signal even when messages contain dynamic IDs.
//  2. Otherwise, normalize the message (digits / hex / UUIDs replaced
//     with placeholders) so "order 12345 not found" and "order 67890
//     not found" collapse into one group.
//
// Service is always part of the hash — same exception in two services
// stays in two distinct inbox rows so different teams can triage them.
func FingerprintException(exType, exMessage, service, stacktrace string) string {
	h := sha1.New()
	h.Write([]byte(exType))
	h.Write([]byte("|"))
	h.Write([]byte(service))
	h.Write([]byte("|"))
	if frames := topFrames(stacktrace, 5); frames != "" {
		h.Write([]byte("stack:"))
		h.Write([]byte(frames))
	} else {
		h.Write([]byte("msg:"))
		h.Write([]byte(normalizeMessage(exMessage)))
	}
	return hex.EncodeToString(h.Sum(nil))[:16]
}

// Frame extractors — one regex per language style. The
// previous implementation was Java-only, which silently
// fell through to message-fingerprinting on Python / Go /
// Node / .NET / Ruby — fragmenting groups along the message
// dimension instead of the stable code-path dimension. Each
// regex captures a single "fully-qualified frame identifier"
// (no line number, no path prefix) so a refactor that
// moves the throw across files doesn't fragment the group.
var (
	// Java / Kotlin / Scala: "    at fully.qualified.Class.method(Source.java:42)"
	frameJavaRe = regexp.MustCompile(`(?m)^\s*at\s+([\w$.<>]+)\(`)
	// Python: '  File "/path/to/file.py", line 42, in func_name'
	framePythonRe = regexp.MustCompile(`(?m)^\s*File\s+"[^"]*?([^/\\"]+\.py)",\s*line\s+\d+,\s*in\s+(\S+)`)
	// Node.js / JavaScript: '    at Object.func (path/to/file.js:42:13)' or
	// '    at func (path/to/file.js:42:13)'.
	frameNodeRe = regexp.MustCompile(`(?m)^\s*at\s+([\w$.<>]+)\s*\(`)
	// Go: 'pkg.func(...) /path/to/file.go:42 +0x...' — the
	// canonical traceback shape for runtime.gopanic. Capture
	// the package-qualified function (everything up to the
	// last "(...)").
	frameGoRe = regexp.MustCompile(`(?m)^([\w./]+(?:\.[\w$.<>]+)+)\(`)
	// .NET: 'at Namespace.Class.Method() in C:\path\file.cs:line 42'.
	frameDotnetRe = regexp.MustCompile(`(?m)^\s*at\s+([\w.<>]+)`)
	// Ruby: '/path/to/file.rb:42:in `func''.
	frameRubyRe = regexp.MustCompile(`(?m)([^/\\:]+\.rb):\d+:in\s+[\x60'](\S+?)['\x60]`)
)

// frameworkPrefixes — frames at these prefixes are noise that
// every exception in the language has at the bottom of the
// stack (the runtime / framework boilerplate). Skipping them
// lets the top-N frames zoom in on application code so the
// fingerprint is "the bug" rather than "the runtime".
var frameworkPrefixes = []string{
	// Java / JVM
	"java.lang.Thread", "java.util.concurrent", "sun.", "jdk.",
	// Spring / Tomcat / common Java frameworks
	"org.springframework", "org.apache.catalina",
	// Go
	"runtime.", "net/http.", "reflect.",
	// Node
	"node:internal/", "internal/process/", "anonymous",
	// Python
	"<frozen importlib", "importlib._bootstrap", "site-packages",
	// .NET
	"System.Threading", "System.Web",
	// Ruby
	"<internal:",
}

func isFrameworkFrame(frame string) bool {
	for _, p := range frameworkPrefixes {
		if strings.Contains(frame, p) {
			return true
		}
	}
	return false
}

func topFrames(stacktrace string, n int) string {
	if stacktrace == "" {
		return ""
	}
	// Try each language's pattern in turn; first one that
	// matches wins. Patterns are ordered most-specific to
	// least so e.g. Python's "File ... line N, in func" doesn't
	// get consumed by the Node "at func (...)" matcher.
	type extractor struct {
		re   *regexp.Regexp
		// keep tells the caller which capture groups to
		// concatenate per match (different patterns capture
		// different anchors).
		keep []int
	}
	tries := []extractor{
		{framePythonRe, []int{1, 2}}, // file.py + func
		{frameRubyRe, []int{1, 2}},   // file.rb + func
		{frameJavaRe, []int{1}},
		{frameNodeRe, []int{1}},
		{frameGoRe, []int{1}},
		{frameDotnetRe, []int{1}},
	}
	for _, t := range tries {
		matches := t.re.FindAllStringSubmatch(stacktrace, -1)
		if len(matches) == 0 {
			continue
		}
		out := make([]string, 0, n)
		for _, m := range matches {
			parts := make([]string, 0, len(t.keep))
			for _, k := range t.keep {
				if k < len(m) {
					parts = append(parts, m[k])
				}
			}
			frame := strings.Join(parts, ":")
			if isFrameworkFrame(frame) {
				continue
			}
			out = append(out, frame)
			if len(out) >= n {
				break
			}
		}
		if len(out) > 0 {
			return strings.Join(out, "\n")
		}
	}
	return ""
}

// Replacements applied in order — UUIDs and hex tokens contain digits,
// so they MUST be substituted before the bare-digit pass.
var (
	uuidRe = regexp.MustCompile(`[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}`)
	hexRe  = regexp.MustCompile(`\b0x[0-9a-fA-F]+\b`)
	intRe  = regexp.MustCompile(`\b\d+\b`)
)

func normalizeMessage(s string) string {
	s = uuidRe.ReplaceAllString(s, "#uuid")
	s = hexRe.ReplaceAllString(s, "#hex")
	s = intRe.ReplaceAllString(s, "#")
	return s
}

// UpsertExceptionGroup is called from the GetExceptions read path so
// every distinct group is implicitly registered. State auto-flips:
//
//   - missing row             → state=new
//   - state=resolved + new occurrence later than resolved_at → state=regressed
//   - everything else         → state preserved, only counters updated
func (s *Store) UpsertExceptionGroup(ctx context.Context, g ExceptionGroup) error {
	existing, err := s.GetExceptionGroup(ctx, g.Fingerprint)
	if err != nil {
		return err
	}
	if existing != nil {
		// Preserve state/assignee/notes/first_seen; bump last_seen + count.
		g.State = existing.State
		g.Assignee = existing.Assignee
		g.Notes = existing.Notes
		g.FirstSeen = existing.FirstSeen
		// Regression detection — a resolved group reopens only if it KEEPS firing
		// past a grace window after the resolve. v0.8.99 (operator-reported:
		// "resolve doesn't stick"): a continuously-firing exception flipped
		// resolved→regressed within the 60s refresh, so a manual resolve never
		// held. The grace lets the operator mute an in-flight issue while they
		// fix it; a fingerprint still erroring well past the resolve genuinely
		// regressed.
		if shouldRegress(existing.State, existing.ResolvedAt, g.LastSeen, exResolveGrace) {
			g.State = ExStateRegressed
		} else if existing.State == ExStateIgnored {
			// Stay ignored — silence is the whole point.
			g.State = ExStateIgnored
		}
		// Use the larger occurrence count (avoid races resetting to lower)
		if existing.Occurrences > g.Occurrences {
			g.Occurrences = existing.Occurrences
		}
		g.ResolvedAt = existing.ResolvedAt
	} else {
		g.State = ExStateNew
		if g.FirstSeen == 0 {
			g.FirstSeen = g.LastSeen
		}
	}
	return s.writeExceptionGroup(ctx, g)
}

func (s *Store) writeExceptionGroup(ctx context.Context, g ExceptionGroup) error {
	batch, err := s.conn.PrepareBatch(ctx, `INSERT INTO exception_groups
		(fingerprint, ex_type, ex_message, service, state, assignee,
		 first_seen, last_seen, resolved_at, occurrences, notes)`)
	if err != nil {
		return fmt.Errorf("prepare exception_groups: %w", err)
	}
	var resolved *time.Time
	if g.ResolvedAt != nil {
		t := time.Unix(0, *g.ResolvedAt).UTC()
		resolved = &t
	}
	if err := batch.Append(
		g.Fingerprint, g.Type, g.Message, g.Service, g.State, g.Assignee,
		time.Unix(0, g.FirstSeen).UTC(),
		time.Unix(0, g.LastSeen).UTC(),
		resolved,
		g.Occurrences,
		g.Notes,
	); err != nil {
		return fmt.Errorf("append exception_group: %w", err)
	}
	return batch.Send()
}

func (s *Store) GetExceptionGroup(ctx context.Context, fingerprint string) (*ExceptionGroup, error) {
	row := s.conn.QueryRow(ctx, `
		SELECT fingerprint, ex_type, ex_message, service, state, assignee,
		       toUnixTimestamp64Nano(first_seen),
		       toUnixTimestamp64Nano(last_seen),
		       resolved_at,
		       occurrences, notes
		FROM exception_groups FINAL
		WHERE fingerprint = ? LIMIT 1`, fingerprint)
	var g ExceptionGroup
	var resolvedAt *time.Time
	if err := row.Scan(&g.Fingerprint, &g.Type, &g.Message, &g.Service, &g.State, &g.Assignee,
		&g.FirstSeen, &g.LastSeen, &resolvedAt, &g.Occurrences, &g.Notes); err != nil {
		if err.Error() == "sql: no rows in result set" {
			return nil, nil
		}
		return nil, err
	}
	if resolvedAt != nil {
		ns := resolvedAt.UnixNano()
		g.ResolvedAt = &ns
	}
	return &g, nil
}

type ExceptionGroupFilter struct {
	State    string // empty = all (except ignored)
	Service  string
	Assignee string
	// Services constrains the result to this set (service IN (…)). Used
	// by the owner/SRE team filter on the Problems inbox (v0.8.310): the
	// API resolves a team pick to its member services from the catalog
	// and sets this, so the filter bites BEFORE the limit/offset — the
	// only correct way to team-filter a server-paginated list (a Go-side
	// post-filter would only trim the current page and break the count).
	// Empty = no service-set constraint.
	Services []string
	// Search (v0.8.318) — case-insensitive substring over ex_type /
	// ex_message / service, applied server-side so it covers EVERY page
	// of the paginated inbox (the old client-side filter only searched
	// the loaded 50 rows).
	Search string
	// Sort / Dir (v0.8.318) — server-side ordering (whitelisted via
	// exceptionGroupsOrderBy). The inbox is LIMIT/OFFSET paginated, so a
	// client-side sort of one page silently lied ("top by occurrences"
	// was really "most-recent 50, reordered").
	Sort string
	Dir  string
	Limit  int
	Offset int
}

// buildExceptionGroupWhere builds the WHERE clause shared by the
// list + count queries. Pulled out so the count never drifts from
// the rows actually returned by ListExceptionGroups — without this
// helper, a small edit to one and not the other would silently
// produce a paginator whose "Page X of Y" is wrong.
func buildExceptionGroupWhere(f ExceptionGroupFilter) whereClause {
	var wc whereClause
	if f.State != "" {
		if f.State == "open" {
			// Convenience bucket: anything not closed-out
			wc.add("state IN ('new','acknowledged','regressed')")
		} else {
			wc.add("state = ?", f.State)
		}
	} else {
		// Default view excludes ignored — they're explicitly silenced.
		wc.add("state != ?", ExStateIgnored)
	}
	if f.Service != "" {
		wc.add("service = ?", f.Service)
	}
	if len(f.Services) > 0 {
		// Team filter → member-service set. Bound in list + count via the
		// same helper so "Page X of Y" can't drift from the rows returned.
		wc.add("service IN (?)", f.Services)
	}
	if f.Assignee != "" {
		wc.add("assignee = ?", f.Assignee)
	}
	if f.Search != "" {
		p := "%" + f.Search + "%"
		wc.add("(ex_type ILIKE ? OR ex_message ILIKE ? OR service ILIKE ?)", p, p, p)
	}
	return wc
}

// exceptionGroupsOrderBy maps the UI sort ids onto whitelisted ORDER BY
// clauses (v0.8.318) — caller input never reaches the SQL string — with a
// fingerprint tiebreak so equal-key rows stay stable across OFFSET pages.
// Unknown ids/dirs fall back to the historical last_seen DESC.
func exceptionGroupsOrderBy(sort, dir string) string {
	col, ok := map[string]string{
		// state sorts by severity rank (worst first when DESC), matching
		// the ordering the client used to compute — not lexically.
		"state":       "multiIf(state='new',5, state='regressed',4, state='acknowledged',3, state='resolved',2, 1)",
		"type":        "ex_type",
		"service":     "service",
		"occurrences": "occurrences",
		"firstSeen":   "first_seen",
		"lastSeen":    "last_seen",
		"assignee":    "assignee",
	}[sort]
	if !ok {
		return "ORDER BY last_seen DESC, fingerprint ASC"
	}
	d := "DESC"
	if dir == "asc" {
		d = "ASC"
	}
	return "ORDER BY " + col + " " + d + ", fingerprint ASC"
}

func (s *Store) ListExceptionGroups(ctx context.Context, f ExceptionGroupFilter) ([]ExceptionGroup, error) {
	if f.Limit == 0 {
		f.Limit = 50
	}
	if f.Limit > 500 {
		f.Limit = 500
	}
	if f.Offset < 0 {
		f.Offset = 0
	}
	wc := buildExceptionGroupWhere(f)
	args := append(wc.args, f.Limit, f.Offset)
	rows, err := s.conn.Query(ctx, `
		SELECT fingerprint, ex_type, ex_message, service, state, assignee,
		       toUnixTimestamp64Nano(first_seen),
		       toUnixTimestamp64Nano(last_seen),
		       resolved_at, occurrences, notes
		FROM exception_groups FINAL `+wc.sql()+`
		`+exceptionGroupsOrderBy(f.Sort, f.Dir)+`
		LIMIT ? OFFSET ?`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ExceptionGroup
	for rows.Next() {
		var g ExceptionGroup
		var resolvedAt *time.Time
		if err := rows.Scan(&g.Fingerprint, &g.Type, &g.Message, &g.Service, &g.State, &g.Assignee,
			&g.FirstSeen, &g.LastSeen, &resolvedAt, &g.Occurrences, &g.Notes); err != nil {
			return nil, err
		}
		if resolvedAt != nil {
			ns := resolvedAt.UnixNano()
			g.ResolvedAt = &ns
		}
		out = append(out, g)
	}
	return out, rows.Err()
}

// CountExceptionGroups returns the total number of rows that
// match f (Limit + Offset are ignored). Drives the paginator's
// "X of N" indicator on the inbox so the UI can offer a "last
// page" jump without having to fetch every group.
func (s *Store) CountExceptionGroups(ctx context.Context, f ExceptionGroupFilter) (int64, error) {
	wc := buildExceptionGroupWhere(f)
	row := s.conn.QueryRow(ctx, `
		SELECT count() FROM exception_groups FINAL `+wc.sql(), wc.args...)
	var n uint64
	if err := row.Scan(&n); err != nil {
		return 0, err
	}
	return int64(n), nil
}

// SetExceptionGroupState handles all explicit user-driven transitions.
// `resolved` stamps resolved_at; transitioning out of resolved clears it.
// exResolveGrace is how long a resolve holds before a still-firing fingerprint
// can regress. A short grace still absorbs the in-flight occurrences of an
// issue the operator just resolved (so a manual resolve isn't undone within the
// 60s refresh — the v0.8.99 fix), but past it a fingerprint that's genuinely
// still erroring should resurface promptly. v0.8.x: operator wanted regressed
// to land sooner — 15m → 5m (still > the 60s refresh, so resolve sticks).
const exResolveGrace = 5 * time.Minute

// DefaultExceptionStaleHorizon is how long a group can go with NO new
// occurrences before the background sweep auto-resolves it (main.go wires this
// into AutoResolveStaleExceptionGroups). v0.8.x — operator-reported: the old
// 14-day horizon made resolved transitions take far too long and contradicted
// the v0.6.24 "cleared by tomorrow" intent. 24h clears a genuinely-fixed
// exception by the next day (14× faster) while a still-active fingerprint keeps
// firing well inside the window so it never spuriously resolves. Lives here
// (not main.go) so it sits beside exResolveGrace and both windows are unit-
// testable. Reversible: shouldRegress re-opens a fingerprint that fires again.
const DefaultExceptionStaleHorizon = 24 * time.Hour

// shouldRegress decides whether a resolved group reopens (regresses) on a fresh
// occurrence. Pure — unit-tested (v0.8.99). Regression fires only for a resolved
// group whose newest occurrence is past resolved_at + grace; an in-grace
// occurrence keeps the operator's resolve.
func shouldRegress(state string, resolvedAtNs *int64, lastSeenNs int64, grace time.Duration) bool {
	if state != ExStateResolved || resolvedAtNs == nil {
		return false
	}
	return lastSeenNs > *resolvedAtNs+grace.Nanoseconds()
}

func (s *Store) SetExceptionGroupState(ctx context.Context, fingerprint, newState string) error {
	g, err := s.GetExceptionGroup(ctx, fingerprint)
	if err != nil {
		return err
	}
	if g == nil {
		return fmt.Errorf("group not found")
	}
	g.State = newState
	if newState == ExStateResolved {
		now := time.Now().UnixNano()
		g.ResolvedAt = &now
	} else if g.State != ExStateResolved {
		g.ResolvedAt = nil
	}
	return s.writeExceptionGroup(ctx, *g)
}

// AssignExceptionGroup sets or clears the assignee (empty → unassigned).
func (s *Store) AssignExceptionGroup(ctx context.Context, fingerprint, userID string) error {
	g, err := s.GetExceptionGroup(ctx, fingerprint)
	if err != nil {
		return err
	}
	if g == nil {
		return fmt.Errorf("group not found")
	}
	g.Assignee = userID
	return s.writeExceptionGroup(ctx, *g)
}

// shouldAutoResolveStale is the pure-function decision behind
// AutoResolveStaleExceptionGroups. Extracted for the v0.6.24
// regression test — touches no I/O, only the (state, last_seen,
// staleAfter, now) tuple. Re-regressing this would re-open the
// "Resolved tab stays empty forever" bug.
func shouldAutoResolveStale(state string, lastSeenNs int64, staleAfter time.Duration, now time.Time) bool {
	if staleAfter <= 0 {
		return false
	}
	switch state {
	case ExStateNew, ExStateAcknowledged, ExStateRegressed:
		// proceed
	default:
		return false
	}
	lastSeen := time.Unix(0, lastSeenNs)
	return now.Sub(lastSeen) >= staleAfter
}

// AutoResolveStaleExceptionGroups transitions any open/acknowledged
// group whose last occurrence is older than staleAfter into the
// `resolved` state. Operator-reported (v0.6.24): without this, the
// /problems "Resolved" tab stays empty forever on installs where
// operators forget to click Resolve manually. Sentry / Honeycomb /
// Datadog all default to this behaviour.
//
// Sets resolved_at to the row's existing last_seen so the audit
// trail reflects "last touched at" rather than "swept at" — keeps
// the timeline honest. UpsertExceptionGroup's regression detector
// will flip the row back to `regressed` if the exception starts
// firing again later.
//
// Returns the number of rows transitioned. Lock-gated at the caller
// (main.runExceptionRefresher) so multi-replica installs don't
// double-sweep.
func (s *Store) AutoResolveStaleExceptionGroups(ctx context.Context, staleAfter time.Duration) (int, error) {
	if staleAfter <= 0 {
		return 0, nil
	}
	cutoff := time.Now().Add(-staleAfter)
	// Read the candidates via FINAL so we work against the
	// currently-effective state per fingerprint (not a stale
	// pre-merge row). Bound the scan with a LIMIT — at typical
	// volumes there are a handful of stale groups per sweep, never
	// thousands; the LIMIT is a safety belt against an install
	// that's been ignored for a year.
	rows, err := s.conn.Query(ctx, `
		SELECT fingerprint, ex_type, ex_message, service, state, assignee,
		       toUnixTimestamp64Nano(first_seen),
		       toUnixTimestamp64Nano(last_seen),
		       resolved_at, occurrences, notes
		FROM exception_groups FINAL
		WHERE state IN ('new','acknowledged','regressed')
		  AND last_seen < ?
		LIMIT 1000
		SETTINGS max_execution_time = 10`, cutoff)
	if err != nil {
		return 0, fmt.Errorf("scan stale exception groups: %w", err)
	}
	var stale []ExceptionGroup
	for rows.Next() {
		var g ExceptionGroup
		var resolvedAt *time.Time
		if err := rows.Scan(&g.Fingerprint, &g.Type, &g.Message, &g.Service,
			&g.State, &g.Assignee, &g.FirstSeen, &g.LastSeen, &resolvedAt,
			&g.Occurrences, &g.Notes); err != nil {
			rows.Close()
			return 0, err
		}
		if resolvedAt != nil {
			ns := resolvedAt.UnixNano()
			g.ResolvedAt = &ns
		}
		stale = append(stale, g)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return 0, err
	}
	for i := range stale {
		// resolved_at = the group's own last_seen, not now() —
		// honest audit trail.
		ts := stale[i].LastSeen
		stale[i].State = ExStateResolved
		stale[i].ResolvedAt = &ts
		if err := s.writeExceptionGroup(ctx, stale[i]); err != nil {
			return 0, fmt.Errorf("upsert resolved group %s: %w", stale[i].Fingerprint, err)
		}
	}
	return len(stale), nil
}

// ExceptionSample is one observed occurrence of a group — used to fill
// the "show me 10 recent examples of this exception" inline expansion.
type ExceptionSample struct {
	TraceID    string `json:"traceId"`
	SpanID     string `json:"spanId"`
	Time       int64  `json:"time"`        // unix ns
	Message    string `json:"message"`     // per-sample exception message — varies within a group
	Stacktrace string `json:"stacktrace"`  // raw, may be empty
	SpanName   string `json:"spanName"`    // operation that errored
	StatusMsg  string `json:"statusMsg"`   // span status message
}

// GetExceptionGroupSamples returns up to `limit` recent occurrences of
// the group (by fingerprint), most-recent first. Because v2 fingerprints
// merge messages that differ only in dynamic IDs, we can't filter the
// candidate set by exact message — instead we scan recent spans matching
// (service, type), recompute the fingerprint per row in Go, and return
// the first `limit` that match.
func (s *Store) GetExceptionGroupSamples(ctx context.Context, fingerprint string, limit int) ([]ExceptionSample, error) {
	if limit <= 0 || limit > 100 {
		limit = 10
	}
	g, err := s.GetExceptionGroup(ctx, fingerprint)
	if err != nil {
		return nil, err
	}
	if g == nil {
		return nil, nil
	}
	// Pull a wide candidate window so even rare messages within the group
	// have a chance to surface, capped to bound memory.
	//
	// v0.8.454 — tarama artık grubun KENDİ yaşam penceresine bağlı
	// (first_seen-1h .. last_seen+1h): grup zaten span'lerden türedi,
	// dışındaki zamanda üyesi olamaz; slack saat-hizalama/geç-varış
	// payı. Önceden zaman sınırı hiç yoktu — problems drawer'ın her
	// açılışı tüm span tarihçesini duration-bağımsız tarıyordu
	// (1B+/gün ölçeğinde hard-constraint ihlali).
	const maxCandidates = 500
	winFrom := time.Unix(0, g.FirstSeen).Add(-time.Hour)
	winTo := time.Unix(0, g.LastSeen).Add(time.Hour)
	rows, err := s.conn.Query(ctx, `
		SELECT trace_id, span_id, toUnixTimestamp64Nano(time),
		       coalesce(JSON_VALUE(events, '$[0].attributes."exception.message"'), '')    AS message,
		       coalesce(JSON_VALUE(events, '$[0].attributes."exception.stacktrace"'), '') AS stacktrace,
		       name, status_msg
		FROM spans
		WHERE service_name = ?
		  AND time >= ? AND time <= ?
		  AND events LIKE '%"exception"%'
		  AND coalesce(JSON_VALUE(events, '$[0].attributes."exception.type"'), '<unknown>') = ?
		ORDER BY time DESC
		LIMIT ?
		SETTINGS max_execution_time = 10`, g.Service, winFrom, winTo, g.Type, maxCandidates)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]ExceptionSample, 0, limit)
	for rows.Next() {
		var sm ExceptionSample
		if err := rows.Scan(&sm.TraceID, &sm.SpanID, &sm.Time, &sm.Message, &sm.Stacktrace, &sm.SpanName, &sm.StatusMsg); err != nil {
			return nil, err
		}
		// Filter to samples that belong to this group — keeps message
		// variants together while excluding spans whose stacktrace puts
		// them in a different inbox row.
		if FingerprintException(g.Type, sm.Message, g.Service, sm.Stacktrace) != fingerprint {
			continue
		}
		out = append(out, sm)
		if len(out) >= limit {
			break
		}
	}
	return out, rows.Err()
}

// OccurrencePoint is one time-bucket of the "occurrences over time"
// histogram on the problem detail page — a real server-side COUNT, not
// a sample. Time is the bucket START in unix ns; Count is how many
// occurrences of the group landed in [Time, Time+step).
type OccurrencePoint struct {
	Time  int64  `json:"time"`  // unix ns, bucket start
	Count uint64 `json:"count"`
}

// occurrenceBucketCap bounds the gap-filled series so a pathological
// window can't balloon the response or the fill loop. bucketForWindow
// caps the step at 1h, so 5000 buckets covers ~208 days — far past the
// 24h stale horizon that auto-resolves idle groups.
const occurrenceBucketCap = 5000

// occurrencesQuery builds the occurrences-over-time SQL. Extracted so a
// unit test can pin the type-safe bucket shape (v0.8.312, operator-
// reported): the bucket MUST stay a bare toStartOfInterval(...) scanned
// as time.Time and converted with UnixNano() in Go — never wrapped in
// toUnixTimestamp64Nano. toStartOfInterval on a second-grain INTERVAL
// yields a DateTime (not DateTime64) on the external Distributed spans
// schema, and toUnixTimestamp64Nano only accepts DateTime64 → code 43 on
// prod. Bounds (service+time WHERE, LIMIT, max_execution_time) are the
// raw-spans hard constraint and must survive any future edit.
func occurrencesQuery(bucketCap int, shardSkip string) string {
	return `
		SELECT toStartOfInterval(time, INTERVAL ? SECOND) AS bucket,
		       count() AS c
		FROM spans
		WHERE service_name = ? AND time >= ? AND time <= ?
		  AND events LIKE '%"exception"%'
		  AND coalesce(JSON_VALUE(events, '$[0].attributes."exception.type"'), '<unknown>') = ?
		GROUP BY bucket
		ORDER BY bucket
		LIMIT ` + fmt.Sprint(bucketCap) + `
		SETTINGS max_execution_time = 10,
		         ` + shardSkip
}

// GetExceptionOccurrences returns a real, gap-filled occurrences-over-
// time series for the group (by fingerprint), spanning its whole
// [first_seen, last_seen] window. It replaces the old client-side
// bucketing of the 100 most-recent samples, which mis-rendered any busy
// group: the newest 100 samples cluster near last_seen, so all-but-one
// bucket read zero even for a steadily-firing problem (v0.8.309).
//
// The count is coarse-scoped to (service, exception.type) — the same
// candidate population GetExceptionGroupSamples draws from — so a group
// whose (service, type) hosts a single fingerprint (the common case)
// reads exactly; a rare (service, type) shared by sibling fingerprints
// reads slightly high. That's the honest, bounded trade for a temporal
// distribution SQL can compute without recomputing the Go-side
// fingerprint per row.
func (s *Store) GetExceptionOccurrences(ctx context.Context, fingerprint string) ([]OccurrencePoint, error) {
	g, err := s.GetExceptionGroup(ctx, fingerprint)
	if err != nil {
		return nil, err
	}
	if g == nil {
		return nil, nil
	}
	fromNs, toNs := g.FirstSeen, g.LastSeen
	if toNs <= fromNs {
		// Degenerate window (single occurrence / clock skew): widen by
		// one second so we still emit exactly one bucket instead of [].
		toNs = fromNs + int64(time.Second)
	}
	from := time.Unix(0, fromNs).UTC()
	to := time.Unix(0, toNs).UTC()
	step := bucketForWindow(int64(to.Sub(from).Seconds()))

	// Bounded raw-spans drill-down (single service + exception type +
	// time-bounded WHERE + LIMIT + max_execution_time). toStartOfInterval
	// on a sub-day INTERVAL is epoch-aligned and tz-independent, so the
	// Go-side fill below lands on the exact same bucket starts.
	//
	// Return the bucket as its native CH time type and convert to unix-ns
	// in Go via bucket.UnixNano() — mirrors GetSpanBreakdown. Do NOT wrap
	// it in toUnixTimestamp64Nano: toStartOfInterval on a second-grain
	// INTERVAL yields a DateTime (not DateTime64) on the external
	// Distributed schema, and toUnixTimestamp64Nano only accepts
	// DateTime64 → code 43 on prod (v0.8.312, operator-reported; local
	// monolithic CH yields DateTime64 so it never reproduced). Scanning
	// as time.Time is type-agnostic and correct on both schemas.
	rows, err := s.conn.Query(ctx,
		occurrencesQuery(occurrenceBucketCap, s.shardSkipSetting()),
		step, g.Service, from, to, g.Type)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	counts := make(map[int64]uint64)
	for rows.Next() {
		var bucket time.Time
		var c uint64
		if err := rows.Scan(&bucket, &c); err != nil {
			return nil, err
		}
		counts[bucket.UnixNano()] = c
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return fillOccurrenceBuckets(fromNs, toNs, step, counts), nil
}

// fillOccurrenceBuckets builds a dense, epoch-aligned series of
// `stepSec`-wide buckets spanning [fromNs, toNs]. `counts` maps a bucket
// start (unix ns, already epoch-aligned) to its observed count; buckets
// with no observations read 0 — so the chart shows real gaps instead of
// silently dropping empty intervals. Pure + table-tested (v0.8.309).
func fillOccurrenceBuckets(fromNs, toNs, stepSec int64, counts map[int64]uint64) []OccurrencePoint {
	if stepSec <= 0 || toNs < fromNs {
		return nil
	}
	stepNs := stepSec * int64(time.Second)
	// Floor-align the first bucket to the epoch, matching CH's
	// toStartOfInterval(time, INTERVAL stepSec SECOND).
	start := (fromNs / stepNs) * stepNs
	out := make([]OccurrencePoint, 0, (toNs-start)/stepNs+1)
	for t := start; t <= toNs; t += stepNs {
		out = append(out, OccurrencePoint{Time: t, Count: counts[t]})
		if len(out) >= occurrenceBucketCap {
			break
		}
	}
	return out
}

// RefreshExceptionGroups scans exception events newer than `since`, then
// applies the v2 fingerprint (stacktrace top-frames or normalized message)
// to merge what would otherwise show as several rows for the same logical
// bug — e.g. "order 12345 not found" + "order 67890 not found".
//
// Step 1 is a coarse SQL-side aggregation by (type, message, service) +
// the most-recent stacktrace per bucket; step 2 is a Go-side re-merge by
// the v2 fingerprint into a smaller set of canonical groups.
func (s *Store) RefreshExceptionGroups(ctx context.Context, since time.Time) (int, error) {
	rows, err := s.conn.Query(ctx, `
		WITH src AS (
		  SELECT
		    coalesce(JSON_VALUE(events, '$[0].attributes."exception.type"'),       '<unknown>') AS ex_type,
		    coalesce(JSON_VALUE(events, '$[0].attributes."exception.message"'),    '')          AS ex_msg,
		    coalesce(JSON_VALUE(events, '$[0].attributes."exception.stacktrace"'), '')          AS ex_stack,
		    service_name, time
		  FROM spans
		  WHERE time >= ? AND events LIKE '%"exception"%'
		)
		SELECT ex_type, ex_msg, service_name,
		       argMax(ex_stack, time) AS stacktrace,
		       count() AS cnt,
		       toUnixTimestamp64Nano(min(time)) AS first_seen,
		       toUnixTimestamp64Nano(max(time)) AS last_seen
		FROM src
		GROUP BY ex_type, ex_msg, service_name`, since)
	if err != nil {
		return 0, err
	}
	defer rows.Close()

	// Re-merge by v2 fingerprint. The SQL pre-aggregation already collapses
	// duplicate raw events; the Go pass merges across messages that differ
	// only in dynamic IDs / values so they share an inbox row.
	merged := map[string]*ExceptionGroup{}
	for rows.Next() {
		var exType, exMsg, svc, stack string
		var cnt uint64
		var firstSeen, lastSeen int64
		if err := rows.Scan(&exType, &exMsg, &svc, &stack, &cnt, &firstSeen, &lastSeen); err != nil {
			return 0, err
		}
		fp := FingerprintException(exType, exMsg, svc, stack)
		if g, ok := merged[fp]; ok {
			g.Occurrences += cnt
			if firstSeen < g.FirstSeen {
				g.FirstSeen = firstSeen
			}
			if lastSeen > g.LastSeen {
				// Latest sample wins for the displayed message — gives a
				// recent example rather than the first-ever one.
				g.LastSeen = lastSeen
				g.Message = exMsg
			}
		} else {
			merged[fp] = &ExceptionGroup{
				Fingerprint: fp,
				Type:        exType,
				Message:     exMsg,
				Service:     svc,
				FirstSeen:   firstSeen,
				LastSeen:    lastSeen,
				Occurrences: cnt,
			}
		}
	}
	if err := rows.Err(); err != nil {
		return 0, err
	}

	for _, g := range merged {
		if err := s.UpsertExceptionGroup(ctx, *g); err != nil {
			return 0, err
		}
	}
	return len(merged), nil
}
