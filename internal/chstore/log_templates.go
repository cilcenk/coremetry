package chstore

import (
	"context"
	"time"
)

// LogTemplate is one Drain-extracted log shape with its running
// observation stats. Persisted via ReplacingMergeTree(version)
// so repeated batch upserts from the templater puller fold
// into one row keyed by template_id.
type LogTemplate struct {
	ID            string   `json:"id"`
	Template      string   `json:"template"`
	FirstSeen     int64    `json:"firstSeen"` // unix ns
	LastSeen      int64    `json:"lastSeen"`
	TotalCount    uint64   `json:"totalCount"`
	Services      []string `json:"services"`
	ExceptionType string   `json:"exceptionType,omitempty"`
	Sample        string   `json:"sample"`
}

// UpsertLogTemplate writes (or refreshes) one template row.
// ReplacingMergeTree(version) picks the highest version on
// merge so the latest batch wins. Caller supplies template_id;
// we keep first_seen sticky (never decrease) by reading the
// existing row before writing, similar to how
// UpsertAnomalyEvent guards started_at.
func (s *Store) UpsertLogTemplate(ctx context.Context, t LogTemplate) error {
	// Sticky first_seen — read the existing row's first_seen so
	// a re-observation of an old template doesn't reset its
	// "since when" marker. ReplacingMergeTree wouldn't on its
	// own protect against this — we'd just keep whichever value
	// the latest version carried. The lookup is keyed on the
	// indexed column so it's cheap.
	var existingFirst time.Time
	_ = s.conn.QueryRow(ctx,
		`SELECT first_seen FROM log_templates FINAL WHERE id = ? LIMIT 1`,
		t.ID,
	).Scan(&existingFirst)
	first := time.Unix(0, t.FirstSeen).UTC()
	if !existingFirst.IsZero() && existingFirst.Before(first) {
		first = existingFirst
	}
	last := time.Unix(0, t.LastSeen).UTC()

	batch, err := s.conn.PrepareBatch(ctx,
		`INSERT INTO log_templates (id, template, first_seen, last_seen,
			total_count, services, exception_type, sample, version)`)
	if err != nil {
		return err
	}
	services := t.Services
	if services == nil {
		services = []string{}
	}
	if err := batch.Append(
		t.ID, t.Template, first, last, t.TotalCount,
		services, t.ExceptionType, t.Sample,
		uint64(time.Now().UnixNano()),
	); err != nil {
		return err
	}
	return batch.Send()
}

// ListLogTemplatesFilter narrows the read by recency and sort
// order. SinceNs filters last_seen ≥ X; Limit caps the response.
type ListLogTemplatesFilter struct {
	SinceNs int64
	SortBy  string // "first_seen" | "last_seen" | "count" (default "count")
	Limit   int
}

// ListLogTemplates returns the persisted templates ordered by
// the requested signal. The "spike" sort is computed in the
// API layer because it needs both the 1h and 24h counts which
// don't live on the row.
func (s *Store) ListLogTemplates(ctx context.Context, f ListLogTemplatesFilter) ([]LogTemplate, error) {
	if f.Limit <= 0 || f.Limit > 500 {
		f.Limit = 100
	}
	order := "total_count DESC"
	switch f.SortBy {
	case "first_seen":
		order = "first_seen DESC"
	case "last_seen":
		order = "last_seen DESC"
	}
	args := []any{}
	wc := "WHERE 1"
	if f.SinceNs > 0 {
		wc += " AND last_seen >= ?"
		args = append(args, time.Unix(0, f.SinceNs).UTC())
	}
	args = append(args, f.Limit)
	rows, err := s.conn.Query(ctx, `
		SELECT id, template,
		       toUnixTimestamp64Nano(first_seen),
		       toUnixTimestamp64Nano(last_seen),
		       total_count, services, exception_type, sample
		FROM log_templates FINAL
		`+wc+`
		ORDER BY `+order+`
		LIMIT ?
		SETTINGS max_execution_time = 5`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []LogTemplate{}
	for rows.Next() {
		var t LogTemplate
		if err := rows.Scan(&t.ID, &t.Template, &t.FirstSeen, &t.LastSeen,
			&t.TotalCount, &t.Services, &t.ExceptionType, &t.Sample); err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}
