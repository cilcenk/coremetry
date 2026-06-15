package chstore

import (
	"context"
	"encoding/json"
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
