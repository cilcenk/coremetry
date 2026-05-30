package chstore

import (
	"context"
	"encoding/json"
	"strings"
	"time"
)

// Runbook is an operator-authored, executable operational procedure
// (v0.7.0 — see docs/runbooks-agent-design.md). Modelled on OneUptime's
// Runbook{steps[]}: an ordered list of steps stepped through during an
// incident, each run tracked as a RunbookExecution (separate table).
//
// Storage note: runbooks live in a DEDICATED ReplacingMergeTree(version)
// table — NOT saved_views. saved_views (invariant #5) is the catch-all
// for per-user VIEW/preset state (dashboards, topology views, alert
// presets). A Runbook is a first-class SHARED operational entity with its
// own lifecycle, executions that reference it, and audit coverage — the
// same class as alert_rules / problems (invariant #4), which each own a
// table.
type Runbook struct {
	ID          string        `json:"id"`
	Title       string        `json:"title"`
	Description string        `json:"description,omitempty"` // markdown — the "knowledge"
	Steps       []RunbookStep `json:"steps"`
	Enabled     bool          `json:"enabled"`
	Labels      []string      `json:"labels,omitempty"`
	CreatedBy   string        `json:"createdBy,omitempty"` // creator email
	CreatedAt   int64         `json:"createdAt"`           // unix ns
	UpdatedAt   int64         `json:"updatedAt"`           // unix ns
	// NotifyOnComplete — when set, an execution of this runbook reaching a
	// terminal state (completed/failed) fans out to the configured
	// notification channels + a runbook.complete SSE event. (v0.7.7)
	NotifyOnComplete bool `json:"notifyOnComplete"`
	// NotifyChannels — which notification channel TYPES the completion
	// notification fires to (email / slack / teams / zoomchat / webhook /
	// whatsapp). Empty = email only (the default + back-compat for runbooks
	// created before the selector). Only consulted when NotifyOnComplete is
	// set. (v0.7.22)
	NotifyChannels []string `json:"notifyChannels,omitempty"`
}

// RunbookStep is one step in a runbook. kind decides where it runs:
//
//	manual     — pauses the run until a responder ticks it off (no agent)
//	query      — runs a Coremetry CH/Explore query inline (server-side)
//	http       — outbound HTTP call (PagerDuty/Slack/webhook) — coremetry-agent
//	javascript — sandboxed JS (goja) — coremetry-agent
//	bash       — shell command — coremetry-agent
//
// Only the payload group matching kind is populated.
type RunbookStep struct {
	ID           string            `json:"id"`
	Order        int               `json:"order"`
	Kind         string            `json:"kind"`
	Title        string            `json:"title"`
	Instructions string            `json:"instructions,omitempty"` // markdown
	Expected     string            `json:"expected,omitempty"`     // expected outcome
	Query        string            `json:"query,omitempty"`        // kind=query
	URL          string            `json:"url,omitempty"`          // kind=http
	Method       string            `json:"method,omitempty"`       // kind=http
	Headers      map[string]string `json:"headers,omitempty"`      // kind=http
	Body         string            `json:"body,omitempty"`         // kind=http
	TimeoutMs    int               `json:"timeoutMs,omitempty"`    // kind=http|bash
	Script       string            `json:"script,omitempty"`       // kind=javascript
	Command      string            `json:"command,omitempty"`      // kind=bash
}

// Runbook step kinds. Exported so the API layer + agent validate against
// the same set without re-declaring the strings.
const (
	RunbookStepManual     = "manual"
	RunbookStepQuery      = "query"
	RunbookStepHTTP       = "http"
	RunbookStepJavaScript = "javascript"
	RunbookStepBash       = "bash"
)

// ValidRunbookStepKind reports whether kind is a known step kind.
// Pure — unit-tested in runbooks_test.go (v0.7.0).
func ValidRunbookStepKind(kind string) bool {
	switch kind {
	case RunbookStepManual, RunbookStepQuery, RunbookStepHTTP,
		RunbookStepJavaScript, RunbookStepBash:
		return true
	}
	return false
}

// normalizeRunbookSteps renumbers Order to match slice position — the
// array IS the source of truth for ordering (the editor reorders by
// moving array elements, OneUptime-style) — and trims step titles. Pure
// — unit-tested in runbooks_test.go (v0.7.0).
func normalizeRunbookSteps(steps []RunbookStep) []RunbookStep {
	out := make([]RunbookStep, len(steps))
	for i, st := range steps {
		st.Order = i
		st.Title = strings.TrimSpace(st.Title)
		out[i] = st
	}
	return out
}

func parseRunbookSteps(j string) []RunbookStep {
	if j == "" {
		return nil
	}
	var steps []RunbookStep
	if err := json.Unmarshal([]byte(j), &steps); err != nil {
		return nil
	}
	return steps
}

// ── Runbook CRUD (mirrors alert_rules: explicit-column upsert, FINAL
//    reads, hard ALTER…DELETE) ─────────────────────────────────────────

func (s *Store) ListRunbooks(ctx context.Context) ([]Runbook, error) {
	rows, err := s.conn.Query(ctx, `
		SELECT id, title, description, steps_json, enabled, labels, created_by,
		       notify_on_complete, notify_channels,
		       toUnixTimestamp64Nano(created_at), toUnixTimestamp64Nano(updated_at)
		FROM runbooks FINAL
		ORDER BY title
		LIMIT 1000 SETTINGS max_execution_time = 10`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Runbook
	for rows.Next() {
		var rb Runbook
		var enabled, notifyOnComplete uint8
		var stepsJSON string
		if err := rows.Scan(&rb.ID, &rb.Title, &rb.Description, &stepsJSON,
			&enabled, &rb.Labels, &rb.CreatedBy, &notifyOnComplete, &rb.NotifyChannels, &rb.CreatedAt, &rb.UpdatedAt); err != nil {
			return nil, err
		}
		rb.Enabled = enabled == 1
		rb.NotifyOnComplete = notifyOnComplete == 1
		rb.Steps = parseRunbookSteps(stepsJSON)
		out = append(out, rb)
	}
	return out, rows.Err()
}

func (s *Store) GetRunbook(ctx context.Context, id string) (*Runbook, error) {
	var rb Runbook
	var enabled, notifyOnComplete uint8
	var stepsJSON string
	err := s.conn.QueryRow(ctx, `
		SELECT id, title, description, steps_json, enabled, labels, created_by,
		       notify_on_complete, notify_channels,
		       toUnixTimestamp64Nano(created_at), toUnixTimestamp64Nano(updated_at)
		FROM runbooks FINAL WHERE id = ? LIMIT 1`, id).
		Scan(&rb.ID, &rb.Title, &rb.Description, &stepsJSON, &enabled,
			&rb.Labels, &rb.CreatedBy, &notifyOnComplete, &rb.NotifyChannels, &rb.CreatedAt, &rb.UpdatedAt)
	if err != nil {
		return nil, err
	}
	rb.Enabled = enabled == 1
	rb.NotifyOnComplete = notifyOnComplete == 1
	rb.Steps = parseRunbookSteps(stepsJSON)
	return &rb, nil
}

func (s *Store) UpsertRunbook(ctx context.Context, rb Runbook) error {
	stepsJSON, err := json.Marshal(normalizeRunbookSteps(rb.Steps))
	if err != nil {
		return err
	}
	enabled := uint8(0)
	if rb.Enabled {
		enabled = 1
	}
	notifyOnComplete := uint8(0)
	if rb.NotifyOnComplete {
		notifyOnComplete = 1
	}
	createdAt := time.Now().UTC()
	if rb.CreatedAt > 0 {
		createdAt = time.Unix(0, rb.CreatedAt).UTC()
	}
	updatedAt := time.Now().UTC()
	if rb.UpdatedAt > 0 {
		updatedAt = time.Unix(0, rb.UpdatedAt).UTC()
	}
	labels := rb.Labels
	if labels == nil {
		labels = []string{}
	}
	notifyChannels := rb.NotifyChannels
	if notifyChannels == nil {
		notifyChannels = []string{}
	}
	// Explicit column list — mirrors UpsertAlertRule so a later ADD COLUMN
	// migration can't shift the positional arg shape on upgraded installs.
	batch, err := s.conn.PrepareBatch(ctx, `INSERT INTO runbooks
		(id, title, description, steps_json, enabled, labels, created_by,
		 notify_on_complete, notify_channels, created_at, updated_at, version)`)
	if err != nil {
		return err
	}
	if err := batch.Append(rb.ID, rb.Title, rb.Description, string(stepsJSON),
		enabled, labels, rb.CreatedBy, notifyOnComplete, notifyChannels, createdAt, updatedAt,
		uint64(time.Now().UnixNano())); err != nil {
		return err
	}
	return batch.Send()
}

// DeleteRunbook hard-removes the row (ALTER…DELETE), matching alert_rules:
// operators hitting Delete expect the runbook to leave the list, not
// linger as a disabled tombstone.
func (s *Store) DeleteRunbook(ctx context.Context, id string) error {
	return s.conn.Exec(ctx, `ALTER TABLE runbooks DELETE WHERE id = ?`, id)
}

func (s *Store) SetRunbookEnabled(ctx context.Context, id string, enabled bool) error {
	rb, err := s.GetRunbook(ctx, id)
	if err != nil {
		return err
	}
	rb.Enabled = enabled
	rb.UpdatedAt = time.Now().UnixNano()
	return s.UpsertRunbook(ctx, *rb)
}
