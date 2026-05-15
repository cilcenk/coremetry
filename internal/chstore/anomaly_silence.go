package chstore

import (
	"context"
	"time"
)

// AnomalySilence mutes a single (kind, pattern, service)
// fingerprint until UntilAt. Silenced anomalies still get
// recorded in anomaly_events (for the history table) but are
// suppressed in the live sections of /anomalies and skip
// notification fan-out.
//
// `Fingerprint` matches AnomalyEvent.ID — same hash recipe so
// no joins are needed at query time.
type AnomalySilence struct {
	ID          string `json:"id"`
	Fingerprint string `json:"fingerprint"`     // matches AnomalyEvent.ID
	Kind        string `json:"kind"`            // log_pattern | trace_op
	Pattern     string `json:"pattern"`
	Service     string `json:"service"`
	CreatedBy   string `json:"createdBy"`       // user email
	CreatedAt   int64  `json:"createdAt"`       // unix ns
	UntilAt     int64  `json:"untilAt"`         // unix ns; <=0 = no expiry
	Reason      string `json:"reason"`
	// Active is filled at query time: true while now < until_at.
	Active bool `json:"active"`
}

func (s *Store) UpsertAnomalySilence(ctx context.Context, sil AnomalySilence) error {
	batch, err := s.conn.PrepareBatch(ctx, "INSERT INTO anomaly_silences")
	if err != nil {
		return err
	}
	if err := batch.Append(
		sil.ID, sil.Fingerprint, sil.Kind, sil.Pattern, sil.Service,
		sil.CreatedBy,
		time.Unix(0, sil.CreatedAt),
		time.Unix(0, sil.UntilAt),
		sil.Reason,
	); err != nil {
		return err
	}
	return batch.Send()
}

// DeleteAnomalySilence soft-deletes by setting until_at to now.
// ReplacingMergeTree keeps the latest version; queries filtering
// by `until_at > now()` exclude it cleanly without an actual
// DELETE (which mutations are slow on partitioned tables).
func (s *Store) DeleteAnomalySilence(ctx context.Context, id string) error {
	cur, err := s.GetAnomalySilence(ctx, id)
	if err != nil || cur == nil {
		return err
	}
	cur.UntilAt = time.Now().UnixNano() - 1
	return s.UpsertAnomalySilence(ctx, *cur)
}

// DeleteAnomalySilences soft-deletes a batch in one call. Already-
// expired or non-existent ids are silently skipped (idempotent so
// the UI can blast every visible id without checking which are
// still live). Returns the number that were actually flipped.
func (s *Store) DeleteAnomalySilences(ctx context.Context, ids []string) (int, error) {
	if len(ids) == 0 {
		return 0, nil
	}
	now := time.Now().UnixNano() - 1
	count := 0
	for _, id := range ids {
		cur, err := s.GetAnomalySilence(ctx, id)
		if err != nil {
			return count, err
		}
		if cur == nil || cur.UntilAt <= now {
			continue
		}
		cur.UntilAt = now
		if err := s.UpsertAnomalySilence(ctx, *cur); err != nil {
			return count, err
		}
		count++
	}
	return count, nil
}

func (s *Store) GetAnomalySilence(ctx context.Context, id string) (*AnomalySilence, error) {
	var s2 AnomalySilence
	var createdAt, untilAt time.Time
	err := s.conn.QueryRow(ctx, `
		SELECT id, fingerprint, kind, pattern, service, created_by,
		       created_at, until_at, reason
		FROM anomaly_silences FINAL WHERE id = ? LIMIT 1`, id).Scan(
		&s2.ID, &s2.Fingerprint, &s2.Kind, &s2.Pattern, &s2.Service, &s2.CreatedBy,
		&createdAt, &untilAt, &s2.Reason,
	)
	if err != nil {
		if err.Error() == "sql: no rows in result set" {
			return nil, nil
		}
		return nil, err
	}
	s2.CreatedAt = createdAt.UnixNano()
	s2.UntilAt = untilAt.UnixNano()
	s2.Active = time.Now().Before(untilAt)
	return &s2, nil
}

// ListActiveSilences returns silences whose until_at is still in
// the future. Used by the anomaly read path + recorder
// notification fan-out to suppress muted entries.
func (s *Store) ListActiveSilences(ctx context.Context) ([]AnomalySilence, error) {
	rows, err := s.conn.Query(ctx, `
		SELECT id, fingerprint, kind, pattern, service, created_by,
		       created_at, until_at, reason
		FROM anomaly_silences FINAL
		WHERE until_at > now64()
		ORDER BY created_at DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []AnomalySilence
	for rows.Next() {
		var s2 AnomalySilence
		var createdAt, untilAt time.Time
		if err := rows.Scan(
			&s2.ID, &s2.Fingerprint, &s2.Kind, &s2.Pattern, &s2.Service, &s2.CreatedBy,
			&createdAt, &untilAt, &s2.Reason,
		); err != nil {
			return nil, err
		}
		s2.CreatedAt = createdAt.UnixNano()
		s2.UntilAt = untilAt.UnixNano()
		s2.Active = true
		out = append(out, s2)
	}
	return out, rows.Err()
}

// ActiveSilencedFingerprints returns a set of fingerprints
// currently muted. Hot path on /anomalies; tiny result set
// (rarely more than a few dozen). Caller can compare in O(1).
func (s *Store) ActiveSilencedFingerprints(ctx context.Context) (map[string]bool, error) {
	rows, err := s.conn.Query(ctx, `
		SELECT fingerprint FROM anomaly_silences FINAL
		WHERE until_at > now64()`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]bool{}
	for rows.Next() {
		var fp string
		if err := rows.Scan(&fp); err != nil {
			return nil, err
		}
		out[fp] = true
	}
	return out, rows.Err()
}
