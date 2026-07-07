package chstore

import (
	"context"
	"fmt"
	"log"
	"strconv"
	"strings"
	"time"

	"github.com/cilcenk/coremetry/internal/cache"
)

// Proactive retention enforcement — v0.5.320. Operator-reported:
// retention TTL on the schema is respected by ClickHouse only on
// background merges, and `merge_with_ttl_timeout` defaults to 4
// hours. At high ingest pressure a partition that meets the TTL
// can sit on disk for a full TTL period before CH gets around to
// dropping it. ALTER TABLE MODIFY TTL alone is fire-and-forget;
// it does not actively reclaim old partitions.
//
// EnforceRetention walks every retention-managed table, queries
// system.parts for the oldest day-bound partition, and DROP'ies
// partitions older than the configured horizon. DROP PARTITION is
// a metadata-only operation — instant, no merge required, frees
// disk immediately.
//
// Source of truth for retention days: same system_settings keys
// as RetentionSpec (retention.spans, retention.logs, etc.). Falls
// back to the table's CREATE-time TTL when no override is set,
// derived from the schema config the Store was constructed with.

type retentionTable struct {
	tableName    string
	settingsKey  string // system_settings key for the override
	defaultDays  int
}

// EnforceRetention drops every active partition older than the
// configured retention horizon on each retention-managed table.
// Idempotent: re-running on a clean state is a no-op. Logs every
// drop with size so the operator can audit storage reclaim from
// the server log.
func (s *Store) EnforceRetention(ctx context.Context) error {
	tables := []retentionTable{
		{"spans",         "retention.spans",    s.ret.SpansDays},
		{"logs",          "retention.logs",     s.ret.LogsDays},
		{"metric_points", "retention.metrics",  s.ret.MetricsDays},
		{"profiles",      "retention.profiles", s.ret.SpansDays}, // profiles share spans default
		// v0.8.328 — exemplars ride the SPANS horizon (key AND default): an
		// exemplar outliving its trace is a dead link. Mirrors the
		// SetRetention plan entry.
		{"exemplars",     "retention.spans",    s.ret.SpansDays},
	}
	overrides, _ := s.GetRetention(ctx)
	overrideMap := map[string]string{
		"retention.spans":    overrides.Spans,
		"retention.logs":     overrides.Logs,
		"retention.metrics":  overrides.Metrics,
		"retention.profiles": overrides.Profiles,
	}
	for _, t := range tables {
		days := t.defaultDays
		if v, ok := overrideMap[t.settingsKey]; ok && v != "" {
			if d, err := parseRetentionDays(v); err == nil {
				days = d
			}
		}
		if days <= 0 {
			continue // retention disabled / unconfigured
		}
		if err := s.dropOldPartitions(ctx, t.tableName, days); err != nil {
			log.Printf("[retention] %s: %v", t.tableName, err)
			continue
		}
	}
	return nil
}

// dropOldPartitions queries system.parts for partition names with
// max_time before the cutoff, then issues
// ALTER TABLE … DROP PARTITION for each. Day-partitioned tables
// have one partition per day; the partition name is the YYYYMMDD
// string CH returns from toYYYYMMDD(time). Bytes-on-disk is
// logged so the operator can see reclaimed storage in the server
// log.
func (s *Store) dropOldPartitions(ctx context.Context, table string, days int) error {
	cutoff := time.Now().AddDate(0, 0, -days)
	rows, err := s.conn.Query(ctx, `
		SELECT partition,
		       sum(rows)             AS row_count,
		       sum(bytes_on_disk)    AS bytes,
		       toUnixTimestamp64Nano(toDateTime64(max(max_time), 9)) AS max_t_ns
		FROM system.parts
		WHERE database = currentDatabase()
		  AND table = ?
		  AND active = 1
		GROUP BY partition
		HAVING max_t_ns < ?
		ORDER BY partition ASC
		SETTINGS max_execution_time = 5`, table, cutoff.UnixNano())
	if err != nil {
		return fmt.Errorf("scan parts: %w", err)
	}
	type candidate struct {
		name      string
		rowCount  uint64
		bytes     uint64
		maxTimeNs int64
	}
	var drops []candidate
	for rows.Next() {
		var c candidate
		if err := rows.Scan(&c.name, &c.rowCount, &c.bytes, &c.maxTimeNs); err != nil {
			rows.Close()
			return err
		}
		drops = append(drops, c)
	}
	rows.Close()
	if len(drops) == 0 {
		return nil
	}
	var totalBytes uint64
	var totalRows uint64
	for _, c := range drops {
		// CH partition names are unquoted integers (e.g.
		// 20260520) — safe to inline. DROP PARTITION is a
		// metadata op; doesn't require a merge to reclaim disk.
		stmt := fmt.Sprintf("ALTER TABLE %s DROP PARTITION %s", table, c.name)
		if err := s.execDDL(ctx, stmt); err != nil {
			log.Printf("[retention] DROP PARTITION %s/%s: %v", table, c.name, err)
			continue
		}
		totalBytes += c.bytes
		totalRows += c.rowCount
		log.Printf("[retention] dropped %s partition=%s rows=%d bytes=%d",
			table, c.name, c.rowCount, c.bytes)
	}
	if len(drops) > 0 {
		log.Printf("[retention] %s: dropped %d partitions older than %dd — reclaimed %d rows / %s",
			table, len(drops), days, totalRows, humanBytes(totalBytes))
	}
	return nil
}

// retentionLockKey is the Redis key for the distributed lock
// that gates retention enforcement. Single key across the
// cluster — whichever replica wins the SETNX runs DROP
// PARTITION, the rest skip. Pre-v0.5.341 all replicas ran the
// enforcement concurrently; CH serialised the DDL but the
// duplicate work + log noise + brief CH metadata-lock fight
// added up at scale.
const retentionLockKey = "coremetry:lock:retention-enforce"

// StartRetentionEnforcer runs EnforceRetention immediately, then
// every `interval` until ctx cancellation. Default interval is
// 1 hour when ≤ 0. Goroutine-friendly — caller is expected to
// invoke as `go s.StartRetentionEnforcer(ctx, 0, lock)`.
//
// Singleton-by-Redis-lock: each tick acquires retentionLockKey
// with a short 5-minute TTL (the enforcement itself runs in
// seconds; 5 min is generous headroom for a slow CH metadata
// op). Lock released after the tick. If a holder crashes
// mid-tick, the lease expires within 5 min and the next
// replica picks up the work — no manual recovery needed.
func (s *Store) StartRetentionEnforcer(ctx context.Context, interval time.Duration, lock cache.Lock) {
	if interval <= 0 {
		interval = time.Hour
	}
	// v0.5.429 — long-lived LeaderHolder. Hourly tick × old
	// 3*interval rule would lease for 3h (way too long for
	// failover); LeaderTTL clamps to 10min cap, refreshes at
	// ~3min. One pod owns the sweep, others sit idle.
	leader := cache.NewLeaderHolder(lock, retentionLockKey, cache.LeaderTTL(interval))
	leader.Start(ctx)
	runOnce := func() {
		if !leader.IsLeader() {
			return
		}
		if err := s.EnforceRetention(ctx); err != nil {
			log.Printf("[retention] sweep: %v", err)
		}
	}
	runOnce() // immediate first sweep so a fresh deploy reclaims disk within seconds, not an hour
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			runOnce()
		}
	}
}

// parseRetentionDays is a smaller cousin of parseRetention that
// returns the day-count only. "30d" → 30; "48h" → 2; otherwise
// error. Hours rounded up to one day so we don't drop a part
// whose age sits between 23h and 47h.
func parseRetentionDays(s string) (int, error) {
	s = strings.TrimSpace(strings.ToLower(s))
	m := retentionRe.FindStringSubmatch(s)
	if m == nil {
		return 0, fmt.Errorf("expected <n>h or <n>d, got %q", s)
	}
	n, _ := strconv.Atoi(m[1])
	if m[2] == "h" {
		// Round hours up to whole days. Below-1d retentions are
		// a power-user case; the enforcer drops at day granularity
		// because that's what the partition layout exposes.
		days := (n + 23) / 24
		if days == 0 {
			days = 1
		}
		return days, nil
	}
	return n, nil
}

// humanBytes — short human-readable size string for log lines.
// 1024 base; truncates to the first non-zero decimal so "1.2 GB"
// reads more naturally than "1234567890 B".
func humanBytes(b uint64) string {
	const k = 1024
	if b < k {
		return fmt.Sprintf("%d B", b)
	}
	div, exp := uint64(k), 0
	for n := b / k; n >= k; n /= k {
		div *= k
		exp++
	}
	return fmt.Sprintf("%.1f %ciB",
		float64(b)/float64(div), "KMGTPE"[exp])
}
