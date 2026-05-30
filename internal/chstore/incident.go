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
	ID         string  `json:"id"`
	Title      string  `json:"title"`
	Severity   string  `json:"severity"`     // info | warning | critical
	Status     string  `json:"status"`       // open | acknowledged | resolved
	Service    string  `json:"service,omitempty"`
	Summary    string  `json:"summary,omitempty"`
	Assignee   string  `json:"assignee,omitempty"`
	Postmortem string  `json:"postmortem,omitempty"`
	StartedAt  int64   `json:"startedAt"`
	AckAt      *int64  `json:"ackAt,omitempty"`
	ResolvedAt *int64  `json:"resolvedAt,omitempty"`
	UpdatedAt  int64   `json:"updatedAt"`
	// Clusters — same pattern as Problem.Clusters: enriched
	// at read time from recent span activity for the
	// incident's primary service. Empty when the service
	// isn't tagged with cluster attrs. Multi-cluster
	// incidents render with multiple chips.
	Clusters []string `json:"clusters,omitempty"`
}

type IncidentEvent struct {
	IncidentID string `json:"incidentId"`
	Time       int64  `json:"time"`         // unix ns
	Kind       string `json:"kind"`         // created | ack | resolved | note | problem_attached | problem_resolved
	Actor      string `json:"actor,omitempty"`
	Body       string `json:"body,omitempty"`
	RefID      string `json:"refId,omitempty"`
}

type IncidentFilter struct {
	Status   string
	Service  string
	Severity string
	Limit    int
}

func (s *Store) ListIncidents(ctx context.Context, f IncidentFilter) ([]Incident, error) {
	limit := f.Limit
	if limit <= 0 || limit > 1000 {
		limit = 200
	}
	var wc whereClause
	if f.Status != "" {
		wc.add("status = ?", f.Status)
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

func (s *Store) GetIncident(ctx context.Context, id string) (*Incident, error) {
	rows, err := s.ListIncidents(ctx, IncidentFilter{Limit: 1000})
	if err != nil {
		return nil, err
	}
	for _, i := range rows {
		if i.ID == id {
			return &i, nil
		}
	}
	return nil, nil
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
	rows, err := s.conn.Query(ctx, `
		SELECT i.id AS id,
		       toInt32(count())                                              AS n,
		       toInt32(countIf(p.status != 'resolved'))                      AS unresolved,
		       ifNull(max(toUnixTimestamp64Nano(p.resolved_at)), toInt64(0)) AS max_resolved
		FROM incidents i FINAL
		INNER JOIN incident_problems ip FINAL ON ip.incident_id = i.id
		INNER JOIN problems p FINAL ON p.id = ip.problem_id
		WHERE i.status = 'open'
		GROUP BY i.id
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
		if err := rows.Scan(&ro.ID, &n, &unresolved, &ro.MaxResolvedAt); err != nil {
			return nil, err
		}
		ro.ProblemCount = int(n)
		ro.Unresolved = int(unresolved)
		out = append(out, ro)
	}
	return out, rows.Err()
}
