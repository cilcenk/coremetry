package chstore

import (
	"context"
	"strings"
	"time"
)

// MaintenanceWindow is an operator-declared time range during
// which alert notifications are suppressed. Problems still
// open + auto-resolve as usual (so the timeline survives the
// review afterwards), but the live fan-out to Slack / email
// / Zoom / etc. skips while the window is active.
//
// Match-mode on Service:
//   - "*"             → global silence (everything)
//   - "<exact name>"  → single-service silence
//   - "name*"         → prefix match (e.g. "payment*"
//     covers payment-api, payment-worker, …)
//
// Severity filter:
//   - "*"             → all severities
//   - "info" / "warning" / "critical" → only that severity
//
// Disabled = soft delete for audit trail. ListMaintenanceWindows
// hides disabled rows by default; the admin UI offers a
// "show disabled" toggle for a forensic review.
type MaintenanceWindow struct {
	ID        string `json:"id"`
	Service   string `json:"service"`
	Severity  string `json:"severity"`
	StartAt   int64  `json:"startAt"`
	EndAt     int64  `json:"endAt"`
	Reason    string `json:"reason"`
	CreatedBy string `json:"createdBy"`
	CreatedAt int64  `json:"createdAt"`
	Disabled  bool   `json:"disabled"`
}

// ListMaintenanceWindows returns every active or future
// window, sorted earliest-end first so the about-to-expire
// row is on top (typical operator question: "how much longer
// is this silenced?"). Past + disabled rows are hidden.
func (s *Store) ListMaintenanceWindows(ctx context.Context, includeDisabled bool) ([]MaintenanceWindow, error) {
	where := "WHERE disabled = 0 AND end_at >= now()"
	if includeDisabled {
		// Surface everything in the last 30 days for the
		// forensic-review view.
		where = "WHERE created_at >= now() - INTERVAL 30 DAY"
	}
	rows, err := s.conn.Query(ctx, `
		SELECT id, service, severity,
		       toUnixTimestamp64Nano(start_at),
		       toUnixTimestamp64Nano(end_at),
		       reason, created_by,
		       toUnixTimestamp64Nano(created_at),
		       disabled
		FROM maintenance_windows FINAL
		`+where+`
		ORDER BY end_at ASC
		LIMIT 500`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []MaintenanceWindow
	for rows.Next() {
		var w MaintenanceWindow
		var dis uint8
		if err := rows.Scan(&w.ID, &w.Service, &w.Severity,
			&w.StartAt, &w.EndAt, &w.Reason, &w.CreatedBy,
			&w.CreatedAt, &dis); err != nil {
			return nil, err
		}
		w.Disabled = dis != 0
		out = append(out, w)
	}
	return out, rows.Err()
}

// UpsertMaintenanceWindow writes / updates a window. ID set
// by caller (new rows generated upstream); ReplacingMergeTree
// keyed by id so re-upsert replaces.
func (s *Store) UpsertMaintenanceWindow(ctx context.Context, w MaintenanceWindow) error {
	b, err := s.conn.PrepareBatch(ctx, "INSERT INTO maintenance_windows (id, service, severity, start_at, end_at, reason, created_by, disabled)")
	if err != nil {
		return err
	}
	var dis uint8
	if w.Disabled {
		dis = 1
	}
	sev := w.Severity
	if sev == "" {
		sev = "*"
	}
	if err := b.Append(
		w.ID, w.Service, sev,
		time.Unix(0, w.StartAt).UTC(),
		time.Unix(0, w.EndAt).UTC(),
		w.Reason, w.CreatedBy, dis,
	); err != nil {
		return err
	}
	return b.Send()
}

// DeleteMaintenanceWindow soft-deletes by upserting with
// disabled=1. Preserves the row for audit trail.
func (s *Store) DeleteMaintenanceWindow(ctx context.Context, id string) error {
	// Read-modify-write to keep the rest of the fields intact.
	rows, err := s.ListMaintenanceWindows(ctx, true)
	if err != nil {
		return err
	}
	for _, w := range rows {
		if w.ID == id {
			w.Disabled = true
			return s.UpsertMaintenanceWindow(ctx, w)
		}
	}
	return nil
}

// IsMaintenanceActive reports whether any non-disabled window
// matches (service, severity, now). Used by the evaluator +
// anomaly detector to skip notification fan-out without
// blocking the problem-state machine (problems still open +
// resolve as usual; only the notify is suppressed).
//
// The list is small (typically <100 entries even on busy
// stacks) so we scan in Go rather than a per-call CH query.
// Caller is expected to refresh the list periodically — the
// evaluator does this once per tick.
func IsMaintenanceActive(windows []MaintenanceWindow, service, severity string, t time.Time) bool {
	tns := t.UnixNano()
	for _, w := range windows {
		if w.Disabled {
			continue
		}
		if tns < w.StartAt || tns > w.EndAt {
			continue
		}
		if !matchSeverity(w.Severity, severity) {
			continue
		}
		if !matchService(w.Service, service) {
			continue
		}
		return true
	}
	return false
}

func matchSeverity(pattern, actual string) bool {
	if pattern == "" || pattern == "*" {
		return true
	}
	return strings.EqualFold(pattern, actual)
}

func matchService(pattern, actual string) bool {
	if pattern == "" || pattern == "*" {
		return true
	}
	// Suffix-* wildcard: "payment*" matches "payment-api".
	if strings.HasSuffix(pattern, "*") {
		prefix := strings.TrimSuffix(pattern, "*")
		return strings.HasPrefix(actual, prefix)
	}
	return strings.EqualFold(pattern, actual)
}
