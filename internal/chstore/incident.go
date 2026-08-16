package chstore

import (
	"context"
	"fmt"
	"strings"
	"time"
)

// Incident is the high-level "something is going wrong" container — a
// declared event the oncall acknowledges, drives to resolution, and
// optionally writes a postmortem against. Multiple Problems / monitor
// flips auto-attach to one Incident when they share the same service +
// severity in a short window, so the oncall has a single page to drive
// the response from.

type Incident struct {
	ID         string `json:"id"`
	Title      string `json:"title"`
	Severity   string `json:"severity"` // info | warning | critical
	Status     string `json:"status"`   // open | acknowledged | resolved
	Service    string `json:"service,omitempty"`
	Summary    string `json:"summary,omitempty"`
	Assignee   string `json:"assignee,omitempty"`
	Postmortem string `json:"postmortem,omitempty"`
	StartedAt  int64  `json:"startedAt"`
	AckAt      *int64 `json:"ackAt,omitempty"`
	ResolvedAt *int64 `json:"resolvedAt,omitempty"`
	UpdatedAt  int64  `json:"updatedAt"`
	// Clusters — same pattern as Problem.Clusters: enriched
	// at read time from recent span activity for the
	// incident's primary service. Empty when the service
	// isn't tagged with cluster attrs. Multi-cluster
	// incidents render with multiple chips.
	Clusters []string `json:"clusters,omitempty"`
}

type IncidentEvent struct {
	IncidentID string `json:"incidentId"`
	Time       int64  `json:"time"` // unix ns
	Kind       string `json:"kind"` // created | ack | resolved | note | problem_attached | problem_resolved
	Actor      string `json:"actor,omitempty"`
	Body       string `json:"body,omitempty"`
	RefID      string `json:"refId,omitempty"`
}

type IncidentFilter struct {
	// ID narrows to exactly one incident (v0.9.332). Exists so GetIncident
	// can ask the database for the row it wants instead of paging the newest
	// N and scanning in Go — see the comment there.
	ID     string
	Status string
	// Services (v0.9.353) — strict service allowlist, in SQL. Same contract
	// as the anomaly filter: nil = no constraint, EMPTY = match nothing.
	// STRICT IN: under a team filter a service-less (global) incident does
	// not belong to any team — matching the Go pass it backs. (The env
	// narrow's `service='' OR …` escape is a DIFFERENT contract: env keeps
	// global rows, team does not.)
	Services []string
	// NotStatuses — statuses to EXCLUDE, applied in SQL so the LIMIT bites on
	// the rows that will be shown. See ProblemFilter.NotStatuses: on an
	// install whose incident history is 99% resolved (local: 994 of the
	// newest 1000), ORDER BY started_at DESC LIMIT 300 returned almost
	// nothing open and the inbox list disagreed with its own badge — 2 vs 29.
	NotStatuses []string
	Service     string
	Severity    string
	Limit       int
}

func (s *Store) ListIncidents(ctx context.Context, f IncidentFilter) ([]Incident, error) {
	limit := f.Limit
	if limit <= 0 || limit > 1000 {
		limit = 200
	}
	var wc whereClause
	if f.ID != "" {
		wc.add("id = ?", f.ID)
	}
	if f.Status != "" {
		wc.add("status = ?", f.Status)
	}
	if len(f.NotStatuses) > 0 {
		wc.add("status NOT IN ("+chPlaceholders(len(f.NotStatuses))+")", toAnySlice(f.NotStatuses)...)
	}
	if f.Services != nil {
		if len(f.Services) == 0 {
			wc.add("1 = 0")
		} else {
			wc.add("service IN ("+chPlaceholders(len(f.Services))+")", toAnySlice(f.Services)...)
		}
	}
	if f.Service != "" {
		wc.add("service = ?", f.Service)
	}
	if f.Severity != "" {
		wc.add("severity = ?", f.Severity)
	}
	rows, err := s.conn.Query(ctx, `
		SELECT id, title, severity, status, service, summary, assignee, postmortem,
		       toUnixTimestamp64Nano(started_at),
		       toUnixTimestamp64Nano(ack_at),
		       toUnixTimestamp64Nano(resolved_at),
		       toUnixTimestamp64Nano(updated_at)
		FROM incidents FINAL `+wc.sql()+`
		ORDER BY started_at DESC
		LIMIT ?`, append(wc.args, limit)...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Incident{}
	for rows.Next() {
		var i Incident
		var ack, resolved *int64
		if err := rows.Scan(&i.ID, &i.Title, &i.Severity, &i.Status, &i.Service,
			&i.Summary, &i.Assignee, &i.Postmortem,
			&i.StartedAt, &ack, &resolved, &i.UpdatedAt); err != nil {
			return nil, err
		}
		if ack != nil && *ack > 0 {
			i.AckAt = ack
		}
		if resolved != nil && *resolved > 0 {
			i.ResolvedAt = resolved
		}
		out = append(out, i)
	}
	return out, rows.Err()
}

// CountIncidentsByStatus (v0.9.456, dürüstlük A4) — /incidents sayfa
// başlığındaki open/ack/resolved sayıları SQL'den: eskiden en-yeni-200
// penceresinden sayılıyordu; 200'ün dışında kalan eski açık incident
// hem listeden hem sayımdan kayboluyor, /inbox rozetiyle çelişiyordu
// (inbox'ın v0.9.321'de kapattığı "2 vs 29" sınıfının sayfa kalıntısı).
// Service daraltması listeyle aynı conjunct; status daraltması BİLEREK
// yok — sayılar sekme chip'leridir, tüm durumları kapsar.
func (s *Store) CountIncidentsByStatus(ctx context.Context, service string) (map[string]uint64, error) {
	var wc whereClause
	if service != "" {
		wc.add("service = ?", service)
	}
	rows, err := s.conn.Query(ctx, `
		SELECT status, count()
		FROM incidents FINAL `+wc.sql()+`
		GROUP BY status
		SETTINGS max_execution_time = 5`, wc.args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]uint64{}
	for rows.Next() {
		var st string
		var n uint64
		if err := rows.Scan(&st, &n); err != nil {
			return nil, err
		}
		out[st] = n
	}
	return out, rows.Err()
}

// CountIncidentsNotInStatuses returns how many incidents are NOT in the given
// statuses — the /inbox badge's fourth term (v0.9.321).
//
// v0.9.322: exclusion rather than an allow-list, so it classifies every
// status exactly as the list's Go keeper does. See ProblemFilter.NotStatuses.
//
// The badge has to count the same rows the list shows or the two disagree,
// which is the bug this codebase keeps re-fixing (v0.9.219 did it for env).
// COUNT-only on a small state table with FINAL, run in parallel with the
// other three terms.
//
// envServices follows the shared contract: nil = unscoped, empty = the env
// resolved to no services (only service-less rows count), otherwise
// membership. Incidents CAN be service-less — an incident declared across
// several services has no single owner — so unlike exception groups the
// empty case is a real query, not a short-circuit to zero.
func (s *Store) CountIncidentsNotInStatuses(ctx context.Context, exclude []string, envServices []string) (uint64, error) {
	args := toAnySlice(exclude)
	statusSQL := "1"
	if len(exclude) > 0 {
		statusSQL = "status NOT IN (" + chPlaceholders(len(exclude)) + ")"
	}
	envSQL := ""
	if envServices != nil {
		if len(envServices) == 0 {
			envSQL = " AND service = ''"
		} else {
			envSQL = " AND (service = '' OR service IN (?))"
			args = append(args, envServices)
		}
	}
	var n uint64
	row := s.conn.QueryRow(ctx,
		`SELECT count() FROM incidents FINAL WHERE `+statusSQL+envSQL+
			` SETTINGS max_execution_time = 5`, args...)
	if err := row.Scan(&n); err != nil {
		return 0, err
	}
	return n, nil
}

// GetIncident returns one incident by id, or (nil, nil) when it genuinely
// does not exist.
//
// v0.9.332 — this used to fetch the newest 1000 incidents and linear-scan
// them in Go. Any incident outside that window was INVISIBLE and the function
// returned (nil, nil) — indistinguishable from "deleted". Locally there are
// 7,539 incidents, so ~87% of them could not be fetched at all; prod is
// deeper still.
//
// Everything that reads one incident was affected, and all of it failed
// quietly: the auto-resolve cascade skipped old incidents (`inc == nil →
// continue`), which is why the orphan fix in this same release closed them
// one at a time instead of all at once; Acknowledge / Resolve / Update
// answered 404 on anything older; the detail page opened empty.
//
// It now asks the database for the row it wants. Same scan code as the list
// (one filter field) rather than a second hand-rolled query that could drift.
func (s *Store) GetIncident(ctx context.Context, id string) (*Incident, error) {
	if id == "" {
		return nil, nil
	}
	rows, err := s.ListIncidents(ctx, IncidentFilter{ID: id, Limit: 1})
	if err != nil {
		return nil, err
	}
	if len(rows) == 0 {
		return nil, nil
	}
	return &rows[0], nil
}

// UpsertIncident takes a pointer so auto-generated IDs flow back to the
// caller (matches the monitor/dashboard pattern).
func (s *Store) UpsertIncident(ctx context.Context, i *Incident) error {
	if i.ID == "" {
		i.ID = randHex(8)
	}
	if i.Status == "" {
		i.Status = "open"
	}
	if i.Severity == "" {
		i.Severity = "warning"
	}
	now := time.Now()
	startedAt := time.Unix(0, i.StartedAt).UTC()
	if i.StartedAt == 0 {
		startedAt = now.UTC()
		i.StartedAt = startedAt.UnixNano()
	}
	var ack, resolved *time.Time
	if i.AckAt != nil {
		t := time.Unix(0, *i.AckAt).UTC()
		ack = &t
	}
	if i.ResolvedAt != nil {
		t := time.Unix(0, *i.ResolvedAt).UTC()
		resolved = &t
	}
	batch, err := s.conn.PrepareBatch(ctx, `INSERT INTO incidents
		(id, title, severity, status, service, summary, assignee, postmortem,
		 started_at, ack_at, resolved_at, updated_at, version)`)
	if err != nil {
		return err
	}
	if err := batch.Append(i.ID, i.Title, i.Severity, i.Status, i.Service,
		i.Summary, i.Assignee, i.Postmortem,
		startedAt, ack, resolved,
		now.UTC(), uint64(now.UnixNano())); err != nil {
		return err
	}
	i.UpdatedAt = now.UnixNano()
	return batch.Send()
}

func (s *Store) AppendIncidentEvent(ctx context.Context, e IncidentEvent) error {
	batch, err := s.conn.PrepareBatch(ctx,
		`INSERT INTO incident_events (incident_id, time, kind, actor, body, ref_id)`)
	if err != nil {
		return err
	}
	t := time.Unix(0, e.Time).UTC()
	if e.Time == 0 {
		t = time.Now().UTC()
	}
	if err := batch.Append(e.IncidentID, t, e.Kind, e.Actor, e.Body, e.RefID); err != nil {
		return err
	}
	return batch.Send()
}

func (s *Store) IncidentTimeline(ctx context.Context, incidentID string) ([]IncidentEvent, error) {
	rows, err := s.conn.Query(ctx, `
		SELECT incident_id, toUnixTimestamp64Nano(time), kind, actor, body, ref_id
		FROM incident_events
		WHERE incident_id = ?
		ORDER BY time ASC`, incidentID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []IncidentEvent{}
	for rows.Next() {
		var e IncidentEvent
		if err := rows.Scan(&e.IncidentID, &e.Time, &e.Kind, &e.Actor, &e.Body, &e.RefID); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// NeighborProvider is a 1-hop adjacency lookup the correlator
// passes in. Optional — when nil, only same-service grouping
// applies (legacy behaviour).
type NeighborProvider interface {
	Neighbors(service string) []string
}

// SetNeighborProvider registers the topology-aware lookup the
// auto-attach path uses for rule 3 (1-hop neighbour grouping).
// Call once at startup with the correlator.Correlator instance;
// nil disables topological clustering, leaving same-service
// grouping intact. The store keeps its own reference so the
// existing AttachProblemToIncident call sites (evaluator,
// anomaly, monitor) don't need to thread the provider through
// their constructors.
func (s *Store) SetNeighborProvider(np NeighborProvider) {
	s.neighborProvider = np
}

// AttachProblemToIncident either finds an existing OPEN incident
// matching this problem's service or a topological neighbour
// within `groupingWindow`, OR creates a new incident headed by
// this problem. Idempotent.
//
// Grouping rules (in priority order):
//
//  1. Already attached? Return that incident.
//  2. Open incident with matching service in the window — even
//     if the severities differ. Critical p99 and warning
//     error_rate on the same service are one incident, not two.
//  3. Open incident on a 1-hop topological neighbour of this
//     problem's service (caller or callee in sampled traces) in
//     the window. Catches cascading failures: an upstream
//     saturation alert and a downstream timeout alert end up
//     under one incident the oncall drives end-to-end.
//  4. Else: create a new incident.
//
// Topology (3) requires a NeighborProvider — pass nil to fall
// back to the rule-2 same-service-only behaviour.
func (s *Store) AttachProblemToIncident(ctx context.Context, p Problem) (*Incident, error) {
	return s.AttachProblemToIncidentWith(ctx, p, s.neighborProvider)
}

// AttachProblemToIncidentWith is the topology-aware variant — the
// correlator is wired here so non-correlator call sites stay
// untouched. The base AttachProblemToIncident calls this with
// nil for the provider.
func (s *Store) AttachProblemToIncidentWith(ctx context.Context, p Problem, np NeighborProvider) (*Incident, error) {
	const groupingWindow = 30 * time.Minute

	// Already attached?
	row := s.conn.QueryRow(ctx,
		`SELECT incident_id FROM incident_problems FINAL WHERE problem_id = ? LIMIT 1`, p.ID)
	var existingID string
	if err := row.Scan(&existingID); err == nil && existingID != "" {
		return s.GetIncident(ctx, existingID)
	}

	// Rule 2: same service, ignore severity. Multiple severity
	// levels on the same service share an incident.
	cutoff := time.Now().Add(-groupingWindow).UTC()
	matchRow := s.conn.QueryRow(ctx, `
		SELECT id FROM incidents FINAL
		WHERE status != 'resolved'
		  AND service  = ?
		  AND started_at >= ?
		ORDER BY started_at DESC
		LIMIT 1`, p.Service, cutoff)
	var matched string
	_ = matchRow.Scan(&matched)

	// topologicallyMatched marks rule-3 wins so the timeline event
	// records "clustered via service topology" instead of the
	// generic same-service note.
	var topologicallyMatched bool

	// Rule 3: 1-hop topological neighbour. Only consult when no
	// same-service incident matched, since same-service is
	// strictly more specific.
	if matched == "" && np != nil {
		neighbours := np.Neighbors(p.Service)
		if len(neighbours) > 0 {
			holders := make([]string, len(neighbours))
			args := make([]any, 0, len(neighbours)+1)
			for i, n := range neighbours {
				holders[i] = "?"
				args = append(args, n)
			}
			args = append(args, cutoff)
			ngRow := s.conn.QueryRow(ctx, `
				SELECT id FROM incidents FINAL
				WHERE status != 'resolved'
				  AND service IN (`+strings.Join(holders, ",")+`)
				  AND started_at >= ?
				ORDER BY started_at DESC
				LIMIT 1`, args...)
			_ = ngRow.Scan(&matched)
			if matched != "" {
				topologicallyMatched = true
			}
		}
	}

	var inc Incident
	if matched != "" {
		got, err := s.GetIncident(ctx, matched)
		if err != nil || got == nil {
			matched = ""
		} else {
			inc = *got
		}
	}
	if matched == "" {
		// Create new incident headed by this problem.
		title := p.RuleName
		if p.Service != "" {
			title = p.Service + " — " + p.RuleName
		}
		inc = Incident{
			Title:     title,
			Severity:  p.Severity,
			Status:    "open",
			Service:   p.Service,
			Summary:   p.Description,
			StartedAt: p.StartedAt,
		}
		if err := s.UpsertIncident(ctx, &inc); err != nil {
			return nil, fmt.Errorf("create incident: %w", err)
		}
		_ = s.AppendIncidentEvent(ctx, IncidentEvent{
			IncidentID: inc.ID, Kind: "created", Actor: "system",
			Body: "Auto-created from " + p.RuleName,
		})
	}

	// Bind problem → incident.
	batch, err := s.conn.PrepareBatch(ctx,
		`INSERT INTO incident_problems (problem_id, incident_id, attached_at, version)`)
	if err != nil {
		return &inc, err
	}
	now := time.Now().UTC()
	if err := batch.Append(p.ID, inc.ID, now, uint64(now.UnixNano())); err != nil {
		return &inc, err
	}
	if err := batch.Send(); err != nil {
		return &inc, err
	}

	body := p.RuleName
	if topologicallyMatched {
		// Tag the timeline so the operator sees this incident
		// absorbed a problem from a *neighbour* service, not the
		// same one. Useful when triaging a cascading outage:
		// "incident on api-gateway, but the third event was a
		// payment-service alert clustered in via topology."
		body = p.RuleName + " [clustered via service topology — " + p.Service + "]"
	}
	_ = s.AppendIncidentEvent(ctx, IncidentEvent{
		IncidentID: inc.ID,
		Kind:       "problem_attached",
		Actor:      "system",
		Body:       body,
		RefID:      p.ID,
	})
	return &inc, nil
}

// IncidentProblems lists all problem ids attached to an incident.
func (s *Store) IncidentProblems(ctx context.Context, incidentID string) ([]string, error) {
	rows, err := s.conn.Query(ctx,
		`SELECT problem_id FROM incident_problems FINAL WHERE incident_id = ?`, incidentID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []string{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
}

// OpenIncidentRollup summarises one open incident's attached-problem state for
// the cascade-resolution sweep (v0.7.33).
type OpenIncidentRollup struct {
	ID            string
	ProblemCount  int
	Unresolved    int
	MaxResolvedAt int64 // unix ns of the latest attached-problem resolution; 0 if none
	// StartedAt (v0.9.332) lets the cascade tell a freshly-created incident
	// whose problem has not attached YET from one that has been sitting
	// unattached for days. Without it, resolving on problemCount == 0 would
	// race every creation.
	StartedAt int64
}

// OpenIncidentRollups returns, for every OPEN incident, the number of attached
// problems, how many are NOT yet resolved, and the latest problem-resolution
// time. The evaluator's cascade sweep uses this to auto-resolve incidents whose
// problems have ALL cleared — operator-reported: problems auto-resolve but
// incidents stayed open forever (CH ground truth: 214 problems resolved / 0
// open, yet 57 incidents open / 0 resolved). One aggregate query, not N
// per-incident lookups, keeps the 1-minute sweep cheap; the state tables are
// small and the joins are LIMIT- + time-bounded.
func (s *Store) OpenIncidentRollups(ctx context.Context) ([]OpenIncidentRollup, error) {
	// v0.9.332 — LEFT JOIN, and acknowledged incidents included.
	//
	// Both joins were INNER, so an incident with no attached problem — or one
	// whose attached problems had aged out of `problems` — produced NO ROW
	// here at all. The cascade never saw it, so it could never resolve it,
	// so it stayed open forever. Measured locally: 26 of 32 open incidents
	// were in exactly that state, the oldest four days old. That is the
	// mechanism behind the operator's "çok fazla incident var, yanıltıcı
	// oluyor" — they accumulate and nothing can ever close them.
	//
	// `status = 'open'` also excluded ACKNOWLEDGED incidents: someone picking
	// one up made it immortal too. Acknowledging says "I'm looking", not
	// "never close this"; the set now matches inboxKeepsIncident and the
	// badge (anything not resolved).
	rows, err := s.conn.Query(ctx, `
		SELECT i.id AS id,
		       toInt64(toUnixTimestamp64Nano(i.started_at))                  AS started,
		       toInt32(countIf(p.id != ''))                                  AS n,
		       toInt32(countIf(p.id != '' AND p.status != 'resolved'))        AS unresolved,
		       ifNull(max(toUnixTimestamp64Nano(p.resolved_at)), toInt64(0)) AS max_resolved
		FROM incidents i FINAL
		LEFT JOIN incident_problems ip FINAL ON ip.incident_id = i.id
		LEFT JOIN problems p FINAL ON p.id = ip.problem_id
		WHERE i.status != 'resolved'
		GROUP BY i.id, i.started_at
		LIMIT 5000
		SETTINGS max_execution_time = 15, distributed_product_mode = 'global'`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []OpenIncidentRollup
	for rows.Next() {
		var ro OpenIncidentRollup
		var n, unresolved int32
		if err := rows.Scan(&ro.ID, &ro.StartedAt, &n, &unresolved, &ro.MaxResolvedAt); err != nil {
			return nil, err
		}
		ro.ProblemCount = int(n)
		ro.Unresolved = int(unresolved)
		out = append(out, ro)
	}
	return out, rows.Err()
}
