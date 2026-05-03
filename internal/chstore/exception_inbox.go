package chstore

import (
	"context"
	"crypto/sha1"
	"encoding/hex"
	"fmt"
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

// FingerprintExceptions: sha1(type|message|service). Stable across runs;
// an empty message still produces a unique fingerprint per type+service.
func FingerprintException(exType, exMessage, service string) string {
	h := sha1.New()
	h.Write([]byte(exType))
	h.Write([]byte("|"))
	h.Write([]byte(exMessage))
	h.Write([]byte("|"))
	h.Write([]byte(service))
	return hex.EncodeToString(h.Sum(nil))[:16]
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
		// Regression detection — only meaningful when the group was previously
		// closed and we're now seeing a newer occurrence.
		if existing.State == ExStateResolved && existing.ResolvedAt != nil && g.LastSeen > *existing.ResolvedAt {
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
	Limit    int
}

func (s *Store) ListExceptionGroups(ctx context.Context, f ExceptionGroupFilter) ([]ExceptionGroup, error) {
	if f.Limit == 0 {
		f.Limit = 200
	}
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
	if f.Assignee != "" {
		wc.add("assignee = ?", f.Assignee)
	}
	args := append(wc.args, f.Limit)
	rows, err := s.conn.Query(ctx, `
		SELECT fingerprint, ex_type, ex_message, service, state, assignee,
		       toUnixTimestamp64Nano(first_seen),
		       toUnixTimestamp64Nano(last_seen),
		       resolved_at, occurrences, notes
		FROM exception_groups FINAL `+wc.sql()+`
		ORDER BY last_seen DESC
		LIMIT ?`, args...)
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

// SetExceptionGroupState handles all explicit user-driven transitions.
// `resolved` stamps resolved_at; transitioning out of resolved clears it.
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

// ExceptionSample is one observed occurrence of a group — used to fill
// the "show me 10 recent examples of this exception" inline expansion.
type ExceptionSample struct {
	TraceID    string `json:"traceId"`
	SpanID     string `json:"spanId"`
	Time       int64  `json:"time"`        // unix ns
	Stacktrace string `json:"stacktrace"`  // raw, may be empty
	SpanName   string `json:"spanName"`    // operation that errored
	StatusMsg  string `json:"statusMsg"`   // span status message
}

// GetExceptionGroupSamples returns up to `limit` recent occurrences of
// the group (by fingerprint), most-recent first. Each sample is enough
// to deep-link into the trace + see the stacktrace inline.
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
	rows, err := s.conn.Query(ctx, `
		SELECT trace_id, span_id, toUnixTimestamp64Nano(time),
		       coalesce(JSON_VALUE(events, '$[0].attributes."exception.stacktrace"'), '') AS stacktrace,
		       name, status_msg
		FROM spans
		WHERE service_name = ?
		  AND events LIKE '%"exception"%'
		  AND coalesce(JSON_VALUE(events, '$[0].attributes."exception.type"'), '<unknown>') = ?
		  AND coalesce(JSON_VALUE(events, '$[0].attributes."exception.message"'), '')        = ?
		ORDER BY time DESC
		LIMIT ?`, g.Service, g.Type, g.Message, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ExceptionSample
	for rows.Next() {
		var sm ExceptionSample
		if err := rows.Scan(&sm.TraceID, &sm.SpanID, &sm.Time, &sm.Stacktrace, &sm.SpanName, &sm.StatusMsg); err != nil {
			return nil, err
		}
		out = append(out, sm)
	}
	return out, rows.Err()
}

// RefreshExceptionGroups scans exception events newer than `since` from the
// spans table, groups them by (type, message, service) — the most specific
// grouping, ensuring fingerprint stability — and upserts each into the
// inbox. Called from a background ticker so the inbox stays in sync
// without piggy-backing on user requests.
func (s *Store) RefreshExceptionGroups(ctx context.Context, since time.Time) (int, error) {
	rows, err := s.conn.Query(ctx, `
		WITH src AS (
		  SELECT
		    coalesce(JSON_VALUE(events, '$[0].attributes."exception.type"'),    '<unknown>') AS ex_type,
		    coalesce(JSON_VALUE(events, '$[0].attributes."exception.message"'), '')          AS ex_msg,
		    service_name, time
		  FROM spans
		  WHERE time >= ? AND events LIKE '%"exception"%'
		)
		SELECT ex_type, ex_msg, service_name,
		       count() AS cnt,
		       toUnixTimestamp64Nano(min(time)) AS first_seen,
		       toUnixTimestamp64Nano(max(time)) AS last_seen
		FROM src
		GROUP BY ex_type, ex_msg, service_name`, since)
	if err != nil {
		return 0, err
	}
	defer rows.Close()
	count := 0
	for rows.Next() {
		var exType, exMsg, svc string
		var cnt uint64
		var firstSeen, lastSeen int64
		if err := rows.Scan(&exType, &exMsg, &svc, &cnt, &firstSeen, &lastSeen); err != nil {
			return count, err
		}
		g := ExceptionGroup{
			Fingerprint: FingerprintException(exType, exMsg, svc),
			Type:        exType,
			Message:     exMsg,
			Service:     svc,
			FirstSeen:   firstSeen,
			LastSeen:    lastSeen,
			Occurrences: cnt,
		}
		if err := s.UpsertExceptionGroup(ctx, g); err != nil {
			return count, err
		}
		count++
	}
	return count, rows.Err()
}
