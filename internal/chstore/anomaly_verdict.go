package chstore

// anomaly_verdict.go — v0.10.181: operatörün anomali olayına kararı
// («anomali» / «değil»). Tablo store.go `anomaly_verdicts` (RMT(version),
// ORDER BY event_id, FINAL). Susturmadan (anomaly_silence.go) AYRI: karar
// kayıt + görünümdür, akışı kapatmaz. Saf parçalar (ValidVerdict, verdictInPlaceholders)
// anomaly_verdict_test.go'da tablo-testli.

import (
	"context"
	"strings"
	"time"
)

const (
	VerdictAnomaly    = "anomaly"
	VerdictNotAnomaly = "not_anomaly"
)

// ValidVerdict — yalnız iki değer; boş/başka → false.
func ValidVerdict(v string) bool { return v == VerdictAnomaly || v == VerdictNotAnomaly }

type AnomalyVerdict struct {
	EventID     string `json:"eventId"`
	Fingerprint string `json:"fingerprint"`
	Kind        string `json:"kind"`
	Pattern     string `json:"pattern"`
	Service     string `json:"service"`
	Verdict     string `json:"verdict"`
	Note        string `json:"note,omitempty"`
	CreatedBy   string `json:"createdBy"`
	CreatedAt   int64  `json:"createdAt"` // unix ns
}

func (s *Store) UpsertAnomalyVerdict(ctx context.Context, v AnomalyVerdict) error {
	batch, err := s.conn.PrepareBatch(ctx, "INSERT INTO anomaly_verdicts (event_id, fingerprint, kind, pattern, service, verdict, note, created_by, created_at)")
	if err != nil {
		return err
	}
	if err := batch.Append(v.EventID, v.Fingerprint, v.Kind, v.Pattern, v.Service, v.Verdict, v.Note, v.CreatedBy, time.Unix(0, v.CreatedAt)); err != nil {
		return err
	}
	return batch.Send()
}

// verdictInPlaceholders — n adet `?` yer tutucu: "?,?,?" (n<=0 → "" — çağıran boş listeyi
// zaten eler). Dizgi birleştirmeyle değil bind ile: kimlikler kullanıcı verisi.
func verdictInPlaceholders(n int) string {
	if n <= 0 {
		return ""
	}
	return strings.TrimSuffix(strings.Repeat("?,", n), ",")
}

// ListAnomalyVerdicts — verilen olay kimlikleri için son karar (FINAL).
// Tavan 500 kimlik (events ucu 200 döner; çağıran keser).
func (s *Store) ListAnomalyVerdicts(ctx context.Context, ids []string) (map[string]AnomalyVerdict, error) {
	out := map[string]AnomalyVerdict{}
	if len(ids) == 0 {
		return out, nil
	}
	if len(ids) > 500 {
		ids = ids[:500]
	}
	args := make([]any, 0, len(ids))
	for _, id := range ids {
		args = append(args, id)
	}
	rows, err := s.conn.Query(ctx, `
		SELECT event_id, fingerprint, kind, pattern, service, verdict, note, created_by, created_at
		FROM anomaly_verdicts FINAL
		WHERE event_id IN (`+verdictInPlaceholders(len(ids))+`)
		LIMIT 500
		SETTINGS max_execution_time = 5`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var v AnomalyVerdict
		var at time.Time
		if err := rows.Scan(&v.EventID, &v.Fingerprint, &v.Kind, &v.Pattern, &v.Service, &v.Verdict, &v.Note, &v.CreatedBy, &at); err != nil {
			return nil, err
		}
		v.CreatedAt = at.UnixNano()
		out[v.EventID] = v
	}
	return out, rows.Err()
}

// EnrichAnomaliesWithVerdicts — events ucu (okuma zamanı); depo hatası
// sessizce atlanır (karar ikincil bağlam, liste kararsız da servis edilir).
func (s *Store) EnrichAnomaliesWithVerdicts(ctx context.Context, events []AnomalyEvent) []AnomalyEvent {
	if len(events) == 0 {
		return events
	}
	ids := make([]string, 0, len(events))
	for i := range events {
		ids = append(ids, events[i].ID)
	}
	vs, err := s.ListAnomalyVerdicts(ctx, ids)
	if err != nil {
		return events
	}
	for i := range events {
		if v, ok := vs[events[i].ID]; ok {
			events[i].Verdict, events[i].VerdictBy, events[i].VerdictAt = v.Verdict, v.CreatedBy, v.CreatedAt
		}
	}
	return events
}
