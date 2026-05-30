package chstore

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"time"
)

// SavedView is a per-user (or team-shared, OwnerID="") named
// query for one of the SPA's filterable pages (`traces`,
// `logs`, `anomalies`, `metrics`, …). The query state is the
// raw URL search string the SPA already encodes — applying a
// view = restoring that URL. No coupling between server and SPA
// schemas, no breakage when filters evolve.
type SavedView struct {
	ID          string `json:"id"`
	OwnerID     string `json:"ownerId"`     // user.id; "" = team-shared
	Name        string `json:"name"`
	Page        string `json:"page"`        // "traces" | "logs" | "anomalies" | "metrics" | …
	QueryString string `json:"queryString"` // ?key=val&… (no leading ?)
	Pinned      bool   `json:"pinned"`
	CreatedAt   int64  `json:"createdAt"`   // unix ns
}

// savedViewsInsertCols is the explicit INSERT column list for saved_views. It
// MUST stay 1:1 with the Append() arguments in UpsertSavedView and OMIT
// `version` (which defaults so it auto-increments per upsert). See the v0.7.36
// fix note in UpsertSavedView.
const savedViewsInsertCols = "id, owner_id, name, page, query_string, pinned, created_at"

// savedViewsInsertColCount is the number of columns/values UpsertSavedView
// binds — pinned by a test so adding a column to one side without the other
// can't silently re-break the insert.
const savedViewsInsertColCount = 7

func (s *Store) UpsertSavedView(ctx context.Context, v SavedView) error {
	if v.ID == "" {
		v.ID = newSavedViewID()
	}
	if v.CreatedAt == 0 {
		v.CreatedAt = time.Now().UnixNano()
	}
	// v0.7.36 — Operator-reported: "Save view → expected 8 arguments got 7"
	// (repro: trace-detail → Logs → Save view). PrepareBatch WITHOUT a column
	// list binds ALL 8 table columns (id, owner_id, name, page, query_string,
	// pinned, created_at, version), but Append passes 7 → count mismatch, every
	// save fails on a fresh schema. The explicit list omits `version` so it
	// defaults to toUnixTimestamp64Nano(now64(9)) — which is exactly what drives
	// the ReplacingMergeTree(version) dedup (each upsert gets a fresh, larger
	// version). Keep this list 1:1 with the Append() args below.
	batch, err := s.conn.PrepareBatch(ctx, "INSERT INTO saved_views ("+savedViewsInsertCols+")")
	if err != nil {
		return err
	}
	pinned := uint8(0)
	if v.Pinned {
		pinned = 1
	}
	if err := batch.Append(
		v.ID, v.OwnerID, v.Name, v.Page, v.QueryString,
		pinned,
		time.Unix(0, v.CreatedAt),
	); err != nil {
		return err
	}
	return batch.Send()
}

// DeleteSavedView soft-removes by inserting a tombstone row at
// a new version. Read paths skip rows whose name is empty after
// FINAL — operator's eye-grep can also see these are gone.
func (s *Store) DeleteSavedView(ctx context.Context, id string) error {
	cur, err := s.GetSavedView(ctx, id)
	if err != nil || cur == nil {
		return err
	}
	cur.Name = ""
	return s.UpsertSavedView(ctx, *cur)
}

func (s *Store) GetSavedView(ctx context.Context, id string) (*SavedView, error) {
	var v SavedView
	var pinned uint8
	var createdAt time.Time
	err := s.conn.QueryRow(ctx, `
		SELECT id, owner_id, name, page, query_string, pinned, created_at
		FROM saved_views FINAL WHERE id = ? LIMIT 1`, id).Scan(
		&v.ID, &v.OwnerID, &v.Name, &v.Page, &v.QueryString,
		&pinned, &createdAt,
	)
	if err != nil {
		if err.Error() == "sql: no rows in result set" {
			return nil, nil
		}
		return nil, err
	}
	v.Pinned = pinned == 1
	v.CreatedAt = createdAt.UnixNano()
	return &v, nil
}

// ListSavedViews returns the union of (a) the requesting user's
// own views and (b) team-shared views (OwnerID=""). Both buckets
// are filtered to the requested page so the topbar dropdown only
// shows relevant entries.
func (s *Store) ListSavedViews(ctx context.Context, ownerID, page string) ([]SavedView, error) {
	var wc whereClause
	wc.add("name != ''") // skip soft-deleted tombstones
	if page != "" {
		wc.add("page = ?", page)
	}
	if ownerID != "" {
		wc.add("(owner_id = ? OR owner_id = '')", ownerID)
	}
	rows, err := s.conn.Query(ctx, `
		SELECT id, owner_id, name, page, query_string, pinned, created_at
		FROM saved_views FINAL `+wc.sql()+`
		ORDER BY pinned DESC, created_at DESC
		LIMIT 200`, wc.args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []SavedView
	for rows.Next() {
		var v SavedView
		var pinned uint8
		var createdAt time.Time
		if err := rows.Scan(
			&v.ID, &v.OwnerID, &v.Name, &v.Page, &v.QueryString,
			&pinned, &createdAt,
		); err != nil {
			return nil, err
		}
		v.Pinned = pinned == 1
		v.CreatedAt = createdAt.UnixNano()
		out = append(out, v)
	}
	return out, rows.Err()
}

func newSavedViewID() string {
	b := make([]byte, 8)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}
