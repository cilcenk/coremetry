package chstore

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"time"
)

// Synthetic monitoring — active probes + passive heartbeat checks.
//
// Monitor types share the one schema (additive columns per v0.8.283):
//   - http      — runner periodically GETs the URL, records latency +
//                 status_code. Down when the response code doesn't
//                 match `expected_status` or the request times out.
//   - tcp       — runner dials `target` (host:port) with the monitor
//                 timeout; up = connect succeeds, records dial latency.
//   - ssl-cert  — runner TLS-handshakes `target` (host[:443]), reads the
//                 leaf cert NotAfter. Down when expired OR days-remaining
//                 < cert_warn_days. Records days-remaining in the result
//                 `detail` column so the UI can show "37d left".
//   - keyword   — runner GETs `url` (reusing the http prober's client /
//                 TLS posture) and asserts the (size-capped) response body
//                 contains `keyword`. `keyword_invert` flips it to a
//                 must-NOT-contain assertion.
//   - heartbeat — passive: external job posts to
//                 /api/heartbeats/{token} on its own cadence. The
//                 runner inspects the gap since the last beat and
//                 marks the monitor down when it exceeds the
//                 monitor's interval_sec (treated as "expected
//                 cadence"). Useful for cron jobs that don't run a
//                 server.
//
// State changes (up→down / down→up) open and resolve a Problem via the
// shared problems table, so the existing notification stack delivers
// alerts without any monitor-specific plumbing.

type Monitor struct {
	ID             string `json:"id"`
	Name           string `json:"name"`
	Type           string `json:"type"`            // http | tcp | ssl-cert | keyword | heartbeat
	URL            string `json:"url,omitempty"`   // http + keyword
	Method         string `json:"method,omitempty"`
	ExpectedStatus uint16 `json:"expectedStatus,omitempty"`
	TimeoutSec     uint16 `json:"timeoutSec,omitempty"`
	IntervalSec    uint32 `json:"intervalSec"`     // active probe interval OR heartbeat grace window
	Enabled        bool   `json:"enabled"`
	HeartbeatToken string `json:"heartbeatToken,omitempty"`
	Target         string `json:"target,omitempty"`        // tcp + ssl-cert (host:port)
	CertWarnDays   uint16 `json:"certWarnDays,omitempty"`  // ssl-cert warn threshold (days)
	Keyword        string `json:"keyword,omitempty"`       // keyword type
	KeywordInvert  bool   `json:"keywordInvert,omitempty"` // keyword type: must-NOT-contain
	CreatedAt      int64  `json:"createdAt"`
}

type MonitorResult struct {
	MonitorID string `json:"monitorId"`
	Time      int64  `json:"time"`        // unix ns
	Status    string `json:"status"`      // up | down | degraded
	LatencyMs int64  `json:"latencyMs"`
	HTTPCode  uint16 `json:"httpCode,omitempty"`
	Message   string `json:"message,omitempty"`
	Detail    int64  `json:"detail"` // type-specific number (ssl-cert: days remaining; 0/negative are meaningful)
}

func (s *Store) ListMonitors(ctx context.Context) ([]Monitor, error) {
	rows, err := s.conn.Query(ctx, `
		SELECT id, name, type, url, method, expected_status, timeout_sec,
		       interval_sec, enabled, heartbeat_token,
		       target, cert_warn_days, keyword, keyword_invert,
		       toUnixTimestamp64Nano(created_at)
		FROM monitors FINAL
		ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Monitor{}
	for rows.Next() {
		var m Monitor
		var enabled, kwInvert uint8
		if err := rows.Scan(&m.ID, &m.Name, &m.Type, &m.URL, &m.Method,
			&m.ExpectedStatus, &m.TimeoutSec, &m.IntervalSec, &enabled,
			&m.HeartbeatToken, &m.Target, &m.CertWarnDays, &m.Keyword,
			&kwInvert, &m.CreatedAt); err != nil {
			return nil, err
		}
		m.Enabled = enabled == 1
		m.KeywordInvert = kwInvert == 1
		out = append(out, m)
	}
	return out, rows.Err()
}

func (s *Store) GetMonitor(ctx context.Context, id string) (*Monitor, error) {
	row := s.conn.QueryRow(ctx, `
		SELECT id, name, type, url, method, expected_status, timeout_sec,
		       interval_sec, enabled, heartbeat_token,
		       target, cert_warn_days, keyword, keyword_invert,
		       toUnixTimestamp64Nano(created_at)
		FROM monitors FINAL WHERE id = ? LIMIT 1`, id)
	var m Monitor
	var enabled, kwInvert uint8
	if err := row.Scan(&m.ID, &m.Name, &m.Type, &m.URL, &m.Method,
		&m.ExpectedStatus, &m.TimeoutSec, &m.IntervalSec, &enabled,
		&m.HeartbeatToken, &m.Target, &m.CertWarnDays, &m.Keyword,
		&kwInvert, &m.CreatedAt); err != nil {
		if err.Error() == "sql: no rows in result set" {
			return nil, nil
		}
		return nil, err
	}
	m.Enabled = enabled == 1
	m.KeywordInvert = kwInvert == 1
	return &m, nil
}

func (s *Store) GetMonitorByToken(ctx context.Context, token string) (*Monitor, error) {
	if token == "" {
		return nil, nil
	}
	row := s.conn.QueryRow(ctx,
		`SELECT id FROM monitors FINAL WHERE heartbeat_token = ? AND type = 'heartbeat' LIMIT 1`, token)
	var id string
	if err := row.Scan(&id); err != nil {
		if err.Error() == "sql: no rows in result set" {
			return nil, nil
		}
		return nil, err
	}
	return s.GetMonitor(ctx, id)
}

// UpsertMonitor takes a pointer so that auto-generated IDs / tokens
// (filled in for new monitors) flow back to the caller.
func (s *Store) UpsertMonitor(ctx context.Context, m *Monitor) error {
	if m.ID == "" {
		m.ID = randHex(8)
	}
	if m.Type == "heartbeat" && m.HeartbeatToken == "" {
		m.HeartbeatToken = randHex(16)
	}
	if m.Method == "" {
		m.Method = "GET"
	}
	if m.ExpectedStatus == 0 {
		m.ExpectedStatus = 200
	}
	if m.TimeoutSec == 0 {
		m.TimeoutSec = 5
	}
	if m.IntervalSec == 0 {
		m.IntervalSec = 60
	}
	if m.CertWarnDays == 0 {
		m.CertWarnDays = 14
	}
	enabled := uint8(0)
	if m.Enabled {
		enabled = 1
	}
	kwInvert := uint8(0)
	if m.KeywordInvert {
		kwInvert = 1
	}
	created := time.Now().UTC()
	if m.CreatedAt > 0 {
		created = time.Unix(0, m.CreatedAt).UTC()
	}
	// ReplacingMergeTree whole-row replace — every column is carried
	// forward on every upsert; the API hands us the full edited monitor.
	batch, err := s.conn.PrepareBatch(ctx, `INSERT INTO monitors
		(id, name, type, url, method, expected_status, timeout_sec,
		 interval_sec, enabled, heartbeat_token, target, cert_warn_days,
		 keyword, keyword_invert, created_at, version)`)
	if err != nil {
		return err
	}
	if err := batch.Append(m.ID, m.Name, m.Type, m.URL, m.Method,
		m.ExpectedStatus, m.TimeoutSec, m.IntervalSec, enabled,
		m.HeartbeatToken, m.Target, m.CertWarnDays, m.Keyword, kwInvert,
		created, uint64(time.Now().UnixNano())); err != nil {
		return err
	}
	return batch.Send()
}

func (s *Store) DeleteMonitor(ctx context.Context, id string) error {
	return s.conn.Exec(ctx, `ALTER TABLE monitors DELETE WHERE id = ?`, id)
}

func (s *Store) InsertMonitorResult(ctx context.Context, r MonitorResult) error {
	batch, err := s.conn.PrepareBatch(ctx,
		`INSERT INTO monitor_results (monitor_id, time, status, latency_ms, http_code, message, detail)`)
	if err != nil {
		return err
	}
	t := time.Unix(0, r.Time).UTC()
	if r.Time == 0 {
		t = time.Now().UTC()
	}
	if err := batch.Append(r.MonitorID, t, r.Status, r.LatencyMs, r.HTTPCode, r.Message, r.Detail); err != nil {
		return err
	}
	return batch.Send()
}

// LastMonitorStatus returns the most recent result for each monitor —
// drives the dashboard cells.
func (s *Store) LastMonitorStatus(ctx context.Context) (map[string]MonitorResult, error) {
	rows, err := s.conn.Query(ctx, `
		SELECT monitor_id,
		       toUnixTimestamp64Nano(argMax(time, time))    AS t,
		       argMax(status, time)                          AS status,
		       argMax(latency_ms, time)                      AS lat,
		       argMax(http_code, time)                       AS code,
		       argMax(message, time)                         AS msg,
		       argMax(detail, time)                          AS detail
		FROM monitor_results
		WHERE time > now() - INTERVAL 7 DAY
		GROUP BY monitor_id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]MonitorResult{}
	for rows.Next() {
		var r MonitorResult
		if err := rows.Scan(&r.MonitorID, &r.Time, &r.Status, &r.LatencyMs, &r.HTTPCode, &r.Message, &r.Detail); err != nil {
			return nil, err
		}
		out[r.MonitorID] = r
	}
	return out, rows.Err()
}

// MonitorStats is the rollup the /monitors page renders next to each
// card — uptime percentage over the last 1h and 24h, plus average
// latency over the same windows. Computed in a single CH query so a
// big fleet of monitors doesn't fan out into N round-trips.
type MonitorStats struct {
	Uptime1h        float64 `json:"uptime1h"`        // 0..100
	Uptime24h       float64 `json:"uptime24h"`       // 0..100
	AvgLatencyMs1h  int64   `json:"avgLatencyMs1h"`
	AvgLatencyMs24h int64   `json:"avgLatencyMs24h"`
	Probes24h       int64   `json:"probes24h"`       // sample size for 24h numbers
}

// MonitorStatsAll returns the rollup for every monitor that has at
// least one probe result in the last 24h, keyed by monitor ID. Map
// is keyed sparsely — monitors with no recent results don't appear,
// so callers should treat a missing key as "no data yet" rather than
// "down".
func (s *Store) MonitorStatsAll(ctx context.Context) (map[string]MonitorStats, error) {
	// `degraded` counts as "up" for the uptime percentage — it's a
	// soft signal (slow but reachable), not a real outage. Consumers
	// who want to be strict can read raw timeline rows and recompute.
	rows, err := s.conn.Query(ctx, `
		SELECT
			monitor_id,
			countIf(time >= now() - INTERVAL 1 HOUR)                                                AS p1,
			countIf(time >= now() - INTERVAL 1 HOUR AND status IN ('up','degraded'))                AS u1,
			countIf(time >= now() - INTERVAL 24 HOUR)                                               AS p24,
			countIf(time >= now() - INTERVAL 24 HOUR AND status IN ('up','degraded'))               AS u24,
			toInt64(round(avgIf(latency_ms, time >= now() - INTERVAL 1 HOUR  AND latency_ms > 0)))  AS lat1,
			toInt64(round(avgIf(latency_ms, time >= now() - INTERVAL 24 HOUR AND latency_ms > 0)))  AS lat24
		FROM monitor_results
		WHERE time >= now() - INTERVAL 24 HOUR
		GROUP BY monitor_id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]MonitorStats{}
	for rows.Next() {
		var (
			id                              string
			p1, u1, p24, u24, lat1, lat24   int64
		)
		if err := rows.Scan(&id, &p1, &u1, &p24, &u24, &lat1, &lat24); err != nil {
			return nil, err
		}
		st := MonitorStats{
			AvgLatencyMs1h:  lat1,
			AvgLatencyMs24h: lat24,
			Probes24h:       p24,
		}
		if p1 > 0 {
			st.Uptime1h = float64(u1) / float64(p1) * 100
		}
		if p24 > 0 {
			st.Uptime24h = float64(u24) / float64(p24) * 100
		}
		out[id] = st
	}
	return out, rows.Err()
}

// MonitorTimeline returns the result history for one monitor (newest
// first), capped at `limit` rows. Drives the per-monitor status timeline.
func (s *Store) MonitorTimeline(ctx context.Context, monitorID string, limit int) ([]MonitorResult, error) {
	if limit <= 0 || limit > 5000 {
		limit = 500
	}
	rows, err := s.conn.Query(ctx, `
		SELECT monitor_id, toUnixTimestamp64Nano(time), status, latency_ms, http_code, message, detail
		FROM monitor_results
		WHERE monitor_id = ?
		ORDER BY time DESC
		LIMIT ?`, monitorID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []MonitorResult{}
	for rows.Next() {
		var r MonitorResult
		if err := rows.Scan(&r.MonitorID, &r.Time, &r.Status, &r.LatencyMs, &r.HTTPCode, &r.Message, &r.Detail); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

func randHex(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// silence unused import warning if hex never used elsewhere
var _ = fmt.Sprintf
