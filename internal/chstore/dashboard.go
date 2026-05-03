package chstore

import (
	"context"
	"encoding/json"
	"fmt"
	"time"
)

// Dashboard is a saved set of panels. The Panels payload is opaque to the
// backend — it's whatever JSON the UI dropped in. Keeps schema migrations
// off the critical path when we add new panel types.
type Dashboard struct {
	ID          string          `json:"id"`
	Name        string          `json:"name"`
	Description string          `json:"description"`
	Panels      json.RawMessage `json:"panels"`        // [] of {id,type,title,width,config}
	CreatedAt   int64           `json:"createdAt"`     // unix ns
	UpdatedAt   int64           `json:"updatedAt"`     // unix ns
}

func (s *Store) ListDashboards(ctx context.Context) ([]Dashboard, error) {
	// List view is small — skip the panels payload to keep it light.
	rows, err := s.conn.Query(ctx, `
		SELECT id, name, description,
		       toUnixTimestamp64Nano(created_at),
		       toUnixTimestamp64Nano(updated_at)
		FROM dashboards FINAL
		ORDER BY updated_at DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Dashboard
	for rows.Next() {
		var d Dashboard
		if err := rows.Scan(&d.ID, &d.Name, &d.Description, &d.CreatedAt, &d.UpdatedAt); err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

func (s *Store) GetDashboard(ctx context.Context, id string) (*Dashboard, error) {
	row := s.conn.QueryRow(ctx, `
		SELECT id, name, description, panels,
		       toUnixTimestamp64Nano(created_at),
		       toUnixTimestamp64Nano(updated_at)
		FROM dashboards FINAL
		WHERE id = ?
		LIMIT 1`, id)
	var d Dashboard
	var panels string
	if err := row.Scan(&d.ID, &d.Name, &d.Description, &panels, &d.CreatedAt, &d.UpdatedAt); err != nil {
		if err.Error() == "sql: no rows in result set" {
			return nil, nil
		}
		return nil, err
	}
	if panels == "" {
		panels = "[]"
	}
	d.Panels = json.RawMessage(panels)
	return &d, nil
}

// UpsertDashboard inserts a new version row — ReplacingMergeTree merges
// on `id` keeping the row with the highest version (set to current ns).
func (s *Store) UpsertDashboard(ctx context.Context, d Dashboard) error {
	if d.ID == "" {
		return fmt.Errorf("dashboard id required")
	}
	if len(d.Panels) == 0 {
		d.Panels = json.RawMessage("[]")
	}
	now := time.Now().UnixNano()
	if d.CreatedAt == 0 {
		d.CreatedAt = now
	}
	d.UpdatedAt = now

	batch, err := s.conn.PrepareBatch(ctx, "INSERT INTO dashboards (id, name, description, panels, created_at, updated_at, version)")
	if err != nil {
		return fmt.Errorf("prepare dashboards: %w", err)
	}
	if err := batch.Append(
		d.ID, d.Name, d.Description, string(d.Panels),
		time.Unix(0, d.CreatedAt).UTC(),
		time.Unix(0, d.UpdatedAt).UTC(),
		uint64(now),
	); err != nil {
		return fmt.Errorf("append dashboard: %w", err)
	}
	return batch.Send()
}

// DeleteDashboard issues a tombstone via OPTIMIZE-friendly ALTER DELETE.
// We don't have a "deleted" flag here (unlike users) because dashboards
// are owner-managed and full removal is the expected behaviour.
func (s *Store) DeleteDashboard(ctx context.Context, id string) error {
	return s.conn.Exec(ctx, `ALTER TABLE dashboards DELETE WHERE id = ?`, id)
}
