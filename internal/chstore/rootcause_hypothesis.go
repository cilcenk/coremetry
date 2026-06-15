package chstore

import (
	"context"
	"encoding/json"
	"strings"
	"time"
)

// RootCauseHypothesis is the PERSISTED, pre-computed root-cause ranking for
// one anchor (an AnomalyEvent or a Problem). The worker synthesizes it on a
// leader-gated tick (correlator.Synthesize over the same bounded evidence the
// on-demand /rootcause fan-out gathers) and upserts it here, so /anomalies and
// /problems can render a "Root cause: <suspect> (NN%)" ribbon WITHOUT a per-row
// fetch (rc #2 of the anomaly → root-cause feature; rc #3 reads it).
//
// This is COMPUTED state, not user-saved state — so a dedicated table is the
// right call (invariant #5's saved_views catch-all is for OPERATOR-created
// state like presets/views; anomaly_events is the precedent for derived,
// continuously-refreshed state with its own access pattern). ReplacingMergeTree
// keyed on the anchor so the latest synthesis per anchor wins; the worker
// re-upserts as the picture changes, FINAL reads collapse to the newest row.
//
// Candidates is stored as a JSON String column (json.Marshal on write,
// Unmarshal on read) — deliberately NOT a nested/Array-of-Tuple schema. The
// shape is small, read whole, and never queried by sub-field, so a JSON blob
// keeps the schema flat and the ScoredCause shape (owned by the correlator)
// from leaking into a CH column layout that would have to track it.
type RootCauseHypothesis struct {
	AnchorKind   string        `json:"anchorKind"`   // "anomaly" | "problem"
	AnchorID     string        `json:"anchorId"`     // AnomalyEvent.ID or Problem.ID
	Service      string        `json:"service"`      // the anchor's service
	ComputedAt   int64         `json:"computedAt"`   // unix ns — when the worker synthesized this
	TopSuspect   string        `json:"topSuspect"`   // the #1 candidate's Service (empty = no clear cause)
	TopScore     float64       `json:"topScore"`     // the #1 candidate's blended score
	Confidence   float64       `json:"confidence"`   // 0..1 — honest low/zero when evidence is thin
	Candidates   []ScoredCause `json:"candidates"`   // full ranked list, best first (reused correlator shape)
	RecentDeploy *RecentDeploy `json:"recentDeploy,omitempty"` // the deploy that the fuser weighted, if any
	Version      uint64        `json:"version"`      // set by the table DEFAULT on insert; read back on FINAL
}

// ScoredCause mirrors correlator.ScoredCause so chstore (the lowest layer) does
// not import correlator. The correlator's Synthesize fills these and the worker
// copies the fields across — same names, same JSON tags, so the wire shape is
// identical whichever side constructs it. Service/Score/Hops/Path match
// correlator.ScoredCause exactly; Reason is the human-readable "why this rank"
// line the fuser attaches (e.g. "fresh deploy 4m before onset").
type ScoredCause struct {
	Service string   `json:"service"`
	Score   float64  `json:"score"`
	Hops    int      `json:"hops"`
	Path    []string `json:"path,omitempty"`
	Reason  string   `json:"reason,omitempty"`
}

// UpsertHypothesis records (or refreshes) the synthesized hypothesis for one
// anchor. ReplacingMergeTree(version) keeps the latest per (anchor_kind,
// anchor_id); the version column's DEFAULT stamps a monotonic ns timestamp so
// successive worker syntheses dedup to the newest. Candidates is marshalled to
// the json String column here. Explicit column list (the table also has a
// `version` DEFAULT) — same idiom as UpsertAnomalyEvent, so the DEFAULT does
// its job and we don't hand-craft a version value.
func (s *Store) UpsertHypothesis(ctx context.Context, h RootCauseHypothesis) error {
	cands, err := json.Marshal(h.Candidates)
	if err != nil {
		return err
	}
	deploy := ""
	if h.RecentDeploy != nil {
		b, err := json.Marshal(h.RecentDeploy)
		if err != nil {
			return err
		}
		deploy = string(b)
	}
	computedAt := h.ComputedAt
	if computedAt == 0 {
		computedAt = time.Now().UnixNano()
	}
	batch, err := s.conn.PrepareBatch(ctx, `INSERT INTO root_cause_hypotheses
		(anchor_kind, anchor_id, service, computed_at,
		 top_suspect, top_score, confidence, candidates, recent_deploy)`)
	if err != nil {
		return err
	}
	if err := batch.Append(
		h.AnchorKind, h.AnchorID, h.Service,
		time.Unix(0, computedAt),
		h.TopSuspect, h.TopScore, h.Confidence,
		string(cands), deploy,
	); err != nil {
		return err
	}
	return batch.Send()
}

// GetHypothesis reads the latest hypothesis for one anchor. FINAL collapses the
// ReplacingMergeTree versions to the newest row. Returns (nil, nil) on no-match
// so the API layer answers a clean empty-state instead of treating "not yet
// synthesized" as an error (same soft-not-found idiom as GetAnomalyEvent).
// Bounded by the (anchor_kind, anchor_id) equality on the ORDER BY key;
// root_cause_hypotheses is a small low-volume state table, not spans /
// metric_points, so no time-bound is needed.
func (s *Store) GetHypothesis(ctx context.Context, anchorKind, anchorID string) (*RootCauseHypothesis, error) {
	var (
		h          RootCauseHypothesis
		computedAt time.Time
		candsJSON  string
		deployJSON string
	)
	row := s.conn.QueryRow(ctx, `
		SELECT anchor_kind, anchor_id, service,
		       computed_at,
		       top_suspect, top_score, confidence,
		       candidates, recent_deploy, version
		FROM root_cause_hypotheses FINAL
		WHERE anchor_kind = ? AND anchor_id = ?
		LIMIT 1`,
		anchorKind, anchorID,
	)
	if err := row.Scan(
		&h.AnchorKind, &h.AnchorID, &h.Service,
		&computedAt,
		&h.TopSuspect, &h.TopScore, &h.Confidence,
		&candsJSON, &deployJSON, &h.Version,
	); err != nil {
		// clickhouse-go surfaces an empty result as this exact string (no
		// typed sentinel) — the same soft no-rows idiom GetAnomalyEvent uses.
		if err.Error() == "sql: no rows in result set" {
			return nil, nil
		}
		return nil, err
	}
	h.ComputedAt = computedAt.UnixNano()
	if candsJSON != "" {
		if err := json.Unmarshal([]byte(candsJSON), &h.Candidates); err != nil {
			return nil, err
		}
	}
	if deployJSON != "" {
		var d RecentDeploy
		if err := json.Unmarshal([]byte(deployJSON), &d); err != nil {
			return nil, err
		}
		h.RecentDeploy = &d
	}
	return &h, nil
}

// RootCauseSummary is the COMPACT slice of a hypothesis the /anomalies and
// /problems list rows carry so the in-page ribbon (rc #3) renders the
// "Root cause: <suspect> (NN%)" chip WITHOUT a per-row fetch of the full
// hypothesis. Only the three fields the collapsed ribbon needs — the expand
// fetches the full /rootcause fan-out on demand. Attached at read time by the
// list handlers (same posture as Problem.RecentDeploy / .Priority): never
// stored on the problems / anomaly_events rows, joined from
// root_cause_hypotheses on each read.
type RootCauseSummary struct {
	TopSuspect string  `json:"topSuspect"` // the #1 candidate's service ("" = no clear cause)
	TopScore   float64 `json:"topScore"`   // the #1 candidate's blended score
	Confidence float64 `json:"confidence"` // 0..1 — honest low/zero when evidence is thin
}

// hypothesesIDCap bounds the IN-list one batch read accepts. The list handlers
// already cap their row count (problems 100, anomaly events 200), but the read
// defends against an unbounded id slice independently — a single oversized IN()
// is the kind of accidental fan-out the CH bounds invariant guards against.
const hypothesesIDCap = 500

// boundHypothesisIDs is the PURE id-list guard GetHypotheses applies before
// building its `IN (?, …)` clause: drop empties, de-duplicate (a repeated id
// shouldn't pad the placeholder list), and cap at hypothesesIDCap so the IN-list
// can never fan out unbounded regardless of caller input. Order-preserving on
// first occurrence so the placeholder ↔ arg pairing the caller builds stays
// deterministic. Extracted + table-driven tested (rootcause_hypothesis_test.go)
// because the cap/dedup is exactly the kind of bound the CH-query invariant
// guards — a regression here re-opens an unbounded IN(). rc #3.
func boundHypothesisIDs(ids []string) []string {
	if len(ids) == 0 {
		return nil
	}
	seen := make(map[string]struct{}, len(ids))
	out := make([]string, 0, len(ids))
	for _, id := range ids {
		if id == "" {
			continue
		}
		if _, dup := seen[id]; dup {
			continue
		}
		seen[id] = struct{}{}
		out = append(out, id)
		if len(out) >= hypothesesIDCap {
			break
		}
	}
	return out
}

// GetHypotheses batch-reads the latest hypothesis for many anchors of ONE kind
// in a SINGLE FINAL query — `WHERE anchor_kind = ? AND anchor_id IN (?, ?, …)`.
// This is the N+1-free join the /anomalies + /problems list handlers use to
// attach a RootCauseSummary per row: one round-trip for the whole page instead
// of GetHypothesis per row. Returns a map keyed by anchor_id holding only the
// anchors that HAVE a synthesized hypothesis — callers omit the summary for the
// rest (the ribbon shows an honest "no clear cause yet" state).
//
// Plain `IN (?, …)` — NOT `GLOBAL IN`: root_cause_hypotheses is a local
// ReplacingMergeTree state table (like anomaly_events / problems), not a
// Distributed table, and the values are bound literals, not a subquery. GLOBAL
// IN only matters when the right-hand side is a subquery executed over a
// Distributed table. The id slice is de-duplicated + capped at hypothesesIDCap
// so the IN-list can't fan out unbounded. The (anchor_kind, anchor_id) ORDER BY
// key bounds the scan; the table is small + low-volume so no time-bound is
// needed (same rationale as GetHypothesis).
func (s *Store) GetHypotheses(ctx context.Context, anchorKind string, ids []string) (map[string]RootCauseHypothesis, error) {
	out := make(map[string]RootCauseHypothesis)
	if anchorKind == "" {
		return out, nil
	}
	bounded := boundHypothesisIDs(ids)
	if len(bounded) == 0 {
		return out, nil
	}
	args := make([]any, 0, len(bounded)+1)
	args = append(args, anchorKind)
	holders := make([]string, 0, len(bounded))
	for _, id := range bounded {
		holders = append(holders, "?")
		args = append(args, id)
	}

	rows, err := s.conn.Query(ctx, `
		SELECT anchor_kind, anchor_id, service,
		       computed_at,
		       top_suspect, top_score, confidence,
		       candidates, recent_deploy, version
		FROM root_cause_hypotheses FINAL
		WHERE anchor_kind = ? AND anchor_id IN (`+strings.Join(holders, ",")+`)`,
		args...,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	for rows.Next() {
		var (
			h          RootCauseHypothesis
			computedAt time.Time
			candsJSON  string
			deployJSON string
		)
		if err := rows.Scan(
			&h.AnchorKind, &h.AnchorID, &h.Service,
			&computedAt,
			&h.TopSuspect, &h.TopScore, &h.Confidence,
			&candsJSON, &deployJSON, &h.Version,
		); err != nil {
			return nil, err
		}
		h.ComputedAt = computedAt.UnixNano()
		if candsJSON != "" {
			if err := json.Unmarshal([]byte(candsJSON), &h.Candidates); err != nil {
				return nil, err
			}
		}
		if deployJSON != "" {
			var d RecentDeploy
			if err := json.Unmarshal([]byte(deployJSON), &d); err != nil {
				return nil, err
			}
			h.RecentDeploy = &d
		}
		out[h.AnchorID] = h
	}
	return out, rows.Err()
}

// summaryOf projects a hypothesis down to the compact ribbon summary. Returns
// nil when there is no clear suspect AND no confidence — an empty top_suspect
// with zero confidence is a synthesized "no clear cause" row, which the ribbon
// can derive from the absence of a summary just as well, so we omit it to keep
// the wire payload lean. A confidence > 0 (even with an empty suspect) is kept
// so the ribbon can honestly say "computing… / N signals" rather than nothing.
func summaryOf(h RootCauseHypothesis) *RootCauseSummary {
	if h.TopSuspect == "" && h.Confidence <= 0 {
		return nil
	}
	return &RootCauseSummary{
		TopSuspect: h.TopSuspect,
		TopScore:   h.TopScore,
		Confidence: h.Confidence,
	}
}

// EnrichProblemsWithRootCause attaches the persisted root-cause summary to each
// problem in ONE batch read — the N+1-free join the /problems list handler uses
// for the in-page ribbon. Collects the ids, fires a single GetHypotheses, and
// sets p.RootCause only for problems that have a hypothesis (the rest keep nil
// → honest "no clear cause yet" ribbon). Soft-fails to the unenriched slice on
// error so a transient blip on this advisory join never blanks the page (same
// posture as EnrichProblemsWithClusters).
func (s *Store) EnrichProblemsWithRootCause(ctx context.Context, problems []Problem) []Problem {
	if len(problems) == 0 {
		return problems
	}
	ids := make([]string, 0, len(problems))
	for i := range problems {
		ids = append(ids, problems[i].ID)
	}
	hyps, err := s.GetHypotheses(ctx, "problem", ids)
	if err != nil || len(hyps) == 0 {
		return problems
	}
	for i := range problems {
		if h, ok := hyps[problems[i].ID]; ok {
			problems[i].RootCause = summaryOf(h)
		}
	}
	return problems
}

// EnrichAnomaliesWithRootCause is the anomaly-anchored sibling — same single
// batch GetHypotheses("anomaly", ids) join for the /anomalies events list
// ribbon. Soft-fails to the unenriched slice on error.
func (s *Store) EnrichAnomaliesWithRootCause(ctx context.Context, events []AnomalyEvent) []AnomalyEvent {
	if len(events) == 0 {
		return events
	}
	ids := make([]string, 0, len(events))
	for i := range events {
		ids = append(ids, events[i].ID)
	}
	hyps, err := s.GetHypotheses(ctx, "anomaly", ids)
	if err != nil || len(hyps) == 0 {
		return events
	}
	for i := range events {
		if h, ok := hyps[events[i].ID]; ok {
			events[i].RootCause = summaryOf(h)
		}
	}
	return events
}
