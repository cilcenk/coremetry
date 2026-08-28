package chstore

import (
	"context"
	"fmt"
	"time"

	"github.com/cilcenk/coremetry/internal/entity"
)

// entity_store.go — K8s ENTITY KATMANI CH kapısı (v0.10.129, design §2/§3).
//
// entity.Store'un chstore uygulaması + system_settings["entity_layer"]
// blob kapısı. Yazım: tam-satır INSERT (ReplacingMergeTree(version) —
// invariant #4: her alan taşınır), async_insert dokunulmaz (PrepareBatch
// senkron batch; satır sayısı tick başına küçük). Okuma: FINAL, cluster_id
// + valid_to = 0 + last_seen zaman sınırı + LIMIT (ev kuralı: her yeni CH
// sorgusu zaman filtresi + limit).

const entitySettingsKey = entity.SettingsKey

func (s *Store) GetEntitySettingsRaw(ctx context.Context) ([]byte, error) {
	return s.GetSetting(ctx, entitySettingsKey)
}

func (s *Store) PutEntitySettingsRaw(ctx context.Context, raw []byte) error {
	return s.PutSetting(ctx, entitySettingsKey, raw)
}

// entityOpenLookback — açık ömür okumasının zaman sınırı: staleAfter'ın
// en büyüğü 30 g; 60 g dışında last_seen'i olan "açık" satır fiilen ölü,
// diff onu görmez ve yeniden görülürse yeni ömür açılır (doğru davranış).
const entityOpenLookback = 60 * 24 * time.Hour

// EntityOpenLifetimes — cluster'ın açık ömürleri (varlık + ilişki),
// entity.Store sözleşmesi. İlişki anahtarı "rel|<tip>|<parent>|<child>".
func (s *Store) EntityOpenLifetimes(ctx context.Context, cid string) (map[string]entity.Lifetime, error) {
	out := map[string]entity.Lifetime{}
	since := time.Now().Add(-entityOpenLookback)
	rows, err := s.conn.Query(ctx, `
		SELECT entity_id, uid, valid_from, last_seen
		FROM entities FINAL
		WHERE cluster_id = ? AND valid_to = toDateTime(0) AND last_seen >= ?
		LIMIT 500000
		SETTINGS max_execution_time = 15`, cid, since)
	if err != nil {
		return nil, fmt.Errorf("entity open lifetimes: %w", err)
	}
	for rows.Next() {
		var id, uid string
		var from, last time.Time
		if err := rows.Scan(&id, &uid, &from, &last); err != nil {
			rows.Close()
			return nil, err
		}
		out[id] = entity.Lifetime{ID: id, UID: uid, ValidFrom: from, LastSeen: last}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	rrows, err := s.conn.Query(ctx, `
		SELECT rel_type, parent_id, child_id, valid_from, last_seen
		FROM entity_relations FINAL
		WHERE cluster_id = ? AND valid_to = toDateTime(0) AND last_seen >= ?
		LIMIT 1000000
		SETTINGS max_execution_time = 15`, cid, since)
	if err != nil {
		return nil, fmt.Errorf("entity open relations: %w", err)
	}
	defer rrows.Close()
	for rrows.Next() {
		var typ, parent, child string
		var from, last time.Time
		if err := rrows.Scan(&typ, &parent, &child, &from, &last); err != nil {
			return nil, err
		}
		key := "rel|" + typ + "|" + parent + "|" + child
		out[key] = entity.Lifetime{ID: key, ValidFrom: from, LastSeen: last}
	}
	return out, rrows.Err()
}

// EntityApply — tam-satır yazım (varlıklar + ilişkiler).
func (s *Store) EntityApply(ctx context.Context, cid string, rows []entity.EntityRow, rels []entity.RelationRow) error {
	if len(rows) > 0 {
		b, err := s.conn.PrepareBatch(ctx, `INSERT INTO entities
			(entity_type, cluster_id, entity_id, valid_from, valid_to, namespace, name, uid, parent_id,
			 label_keys, label_values, source, first_seen, last_seen, stale)`)
		if err != nil {
			return fmt.Errorf("entities batch: %w", err)
		}
		for _, r := range rows {
			stale := uint8(0)
			if r.Stale {
				stale = 1
			}
			if err := b.Append(r.Type, cid, r.ID, r.ValidFrom, zeroOr(r.ValidTo), r.Namespace, r.Name, r.UID, r.ParentID,
				r.LabelKeys, r.LabelValues, r.Source, r.FirstSeen, r.LastSeen, stale); err != nil {
				return err
			}
		}
		if err := b.Send(); err != nil {
			return fmt.Errorf("entities send: %w", err)
		}
	}
	if len(rels) > 0 {
		b, err := s.conn.PrepareBatch(ctx, `INSERT INTO entity_relations
			(rel_type, cluster_id, parent_id, child_id, valid_from, valid_to, last_seen, source)`)
		if err != nil {
			return fmt.Errorf("entity_relations batch: %w", err)
		}
		for _, r := range rels {
			if err := b.Append(r.Type, cid, r.ParentID, r.ChildID, r.ValidFrom, zeroOr(r.ValidTo), r.LastSeen, r.Source); err != nil {
				return err
			}
		}
		if err := b.Send(); err != nil {
			return fmt.Errorf("entity_relations send: %w", err)
		}
	}
	return nil
}

// zeroOr — Go sıfır zamanı → CH DateTime 0 (1970-01-01), "açık" işareti.
func zeroOr(t time.Time) time.Time {
	if t.IsZero() {
		return time.Unix(0, 0).UTC()
	}
	return t
}

// EntityRecordRun — sync_runs satırı.
func (s *Store) EntityRecordRun(ctx context.Context, run entity.Run) error {
	b, err := s.conn.PrepareBatch(ctx, `INSERT INTO entity_sync_runs
		(cluster_id, started_at, finished_at, status, entities_written, relations_written, closed,
		 unmapped_keys, unmapped_counts, thanos_ms, ch_ms, error)`)
	if err != nil {
		return err
	}
	keys, counts := run.UnmappedKeys, run.UnmappedCounts
	if keys == nil {
		keys = []string{}
	}
	if counts == nil {
		counts = []uint32{}
	}
	fin := run.FinishedAt
	if fin.IsZero() {
		fin = time.Now()
	}
	if err := b.Append(run.ClusterID, run.StartedAt, fin, run.Status, uint32(run.EntitiesWritten), uint32(run.RelationsWritten),
		uint32(run.Closed), keys, counts, uint32(run.ThanosMs), uint32(run.CHMs), run.Error); err != nil {
		return err
	}
	return b.Send()
}

// EntitySyncRuns — admin görünümü: cluster başına SON koşu + son N koşu.
func (s *Store) EntitySyncRuns(ctx context.Context, since time.Time, limit int) ([]entity.Run, error) {
	if limit <= 0 || limit > 1000 {
		limit = 200
	}
	rows, err := s.conn.Query(ctx, `
		SELECT cluster_id, started_at, finished_at, status, entities_written, relations_written, closed,
		       unmapped_keys, unmapped_counts, thanos_ms, ch_ms, error
		FROM entity_sync_runs FINAL
		WHERE started_at >= ?
		ORDER BY started_at DESC
		LIMIT ?
		SETTINGS max_execution_time = 10`, since, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []entity.Run{}
	for rows.Next() {
		var r entity.Run
		var ew, rw, cl, tm, cm uint32
		if err := rows.Scan(&r.ClusterID, &r.StartedAt, &r.FinishedAt, &r.Status, &ew, &rw, &cl,
			&r.UnmappedKeys, &r.UnmappedCounts, &tm, &cm, &r.Error); err != nil {
			return nil, err
		}
		r.EntitiesWritten, r.RelationsWritten, r.Closed, r.ThanosMs, r.CHMs = int(ew), int(rw), int(cl), int(tm), int(cm)
		out = append(out, r)
	}
	return out, rows.Err()
}

// EntitySeenRecent — span geçişi: entity_seen_5m'in son dilimi (zaman
// sınırlı + LIMIT; kova bazlı, pencere ≤ 1 saat → prod'da ≤ 12 kova ×
// aktif (pod, servis)).
func (s *Store) EntitySeenRecent(ctx context.Context, since time.Time) ([]entity.SeenRow, error) {
	rows, err := s.conn.Query(ctx, `
		SELECT cluster, k8s_namespace, k8s_pod, anyLast(k8s_node) AS node, service_name,
		       toInt64(countMerge(span_count_state)) AS spans, maxMerge(last_seen_state) AS last_seen
		FROM entity_seen_5m
		WHERE time_bucket >= toStartOfFiveMinute(?) AND time_bucket <= now()
		GROUP BY cluster, k8s_namespace, k8s_pod, service_name
		LIMIT 200000
		SETTINGS max_execution_time = 10`, since)
	if err != nil {
		return nil, fmt.Errorf("entity_seen recent: %w", err)
	}
	defer rows.Close()
	out := []entity.SeenRow{}
	for rows.Next() {
		var r entity.SeenRow
		var spans int64
		if err := rows.Scan(&r.ClusterValue, &r.Namespace, &r.Pod, &r.Node, &r.Service, &spans, &r.LastSeen); err != nil {
			return nil, err
		}
		r.Spans = int(spans)
		out = append(out, r)
	}
	return out, rows.Err()
}
