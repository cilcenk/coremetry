package chstore

import (
	"context"
	"time"
)

// RedisMetrics is the receiver-flavoured drill-down for one
// Redis instance — reads from OpenTelemetry redis receiver
// (`redis.*`).
//
// Operator-relevant signals:
//   - Clients (connected / blocked) → connection saturation
//   - Memory used / max + fragmentation → eviction risk
//   - Keyspace hits/misses → cache effectiveness
//   - Ops/sec by category (commands, instantaneous_ops_per_sec)
//   - Evicted/expired key rates → cache turnover signal
//   - Replication lag (replica_offset distance from master)
//   - Persistence: changes_since_last_save → durability risk
//   - Role (master/replica) → topology awareness
type RedisMetrics struct {
	Instance       string         `json:"instance"`
	Status         string         `json:"status"`
	Role           string         `json:"role"` // master / replica / unknown
	WindowSeconds  float64        `json:"windowSeconds"`
	UptimeSec      float64        `json:"uptimeSec"`
	Clients        RedisClients   `json:"clients"`
	Memory         RedisMemory    `json:"memory"`
	CommandsPS     float64        `json:"commandsPerSec"`
	NetInputBPS    float64        `json:"netInputBytesPerSec"`
	NetOutputBPS   float64        `json:"netOutputBytesPerSec"`
	KeyspaceHitsPS float64        `json:"keyspaceHitsPerSec"`
	KeyspaceMissPS float64        `json:"keyspaceMissesPerSec"`
	HitRatePct     float64        `json:"hitRatePct"`
	EvictedPS      float64        `json:"keysEvictedPerSec"`
	ExpiredPS      float64        `json:"keysExpiredPerSec"`
	ReplLagBytes   float64        `json:"replicationLagBytes"`
	ChangesSince   float64        `json:"changesSinceLastSave"`
	SlowlogEntries float64        `json:"slowlogEntries"`
	ConnRefusedPS  float64        `json:"connectionsRejectedPerSec"`
	Keyspaces      []RedisDB      `json:"keyspaces"`
}

// RedisClients — total + blocked. Blocked clients are stuck on
// BLPOP / BRPOP / XREAD etc. — operationally useful to spot
// long queues vs healthy waiting consumers.
type RedisClients struct {
	Connected     float64 `json:"connected"`
	Blocked       float64 `json:"blocked"`
	MaxInputBuf   float64 `json:"maxInputBufferBytes"`
	MaxOutputBuf  float64 `json:"maxOutputBufferBytes"`
}

// RedisMemory — used vs max gives saturation %. Fragmentation
// ratio over 1.5 hints at memory waste; over 5 means restart-
// to-recover territory. RSS is what Linux sees; used is what
// Redis allocated.
type RedisMemory struct {
	UsedBytes         float64 `json:"usedBytes"`
	RSSBytes          float64 `json:"rssBytes"`
	PeakBytes         float64 `json:"peakBytes"`
	MaxBytes          float64 `json:"maxBytes"`
	FragmentationRatio float64 `json:"fragmentationRatio"`
	LuaBytes          float64 `json:"luaBytes"`
	UsagePct          float64 `json:"usagePct"`
}

// RedisDB — per-keyspace (db0 / db1 / …) key counts and expire
// counts. Redis stores up to 16 logical databases per
// instance; this lets the operator see which one is doing the
// work.
type RedisDB struct {
	Name    string  `json:"name"` // "db0" etc.
	Keys    float64 `json:"keys"`
	Expires float64 `json:"expires"`
}

func (s *Store) GetRedisMetrics(
	ctx context.Context, instance string, from, to time.Time,
) (*RedisMetrics, error) {
	if from.IsZero() {
		from = time.Now().Add(-1 * time.Hour)
	}
	if to.IsZero() {
		to = time.Now()
	}
	windowSec := to.Sub(from).Seconds()
	if windowSec <= 0 {
		windowSec = 60
	}
	out := &RedisMetrics{
		Instance: instance, Status: "down", Role: "unknown",
		WindowSeconds: windowSec,
		Keyspaces:     []RedisDB{},
	}
	hasInstanceFilter := instance != "" && instance != "unknown"

	if gauges, err := s.queryRedisGauges(ctx, from, to, instance, hasInstanceFilter); err == nil && len(gauges) > 0 {
		out.UptimeSec = gauges["redis.uptime"]
		out.Clients.Connected = gauges["redis.clients.connected"]
		out.Clients.Blocked = gauges["redis.clients.blocked"]
		out.Clients.MaxInputBuf = gauges["redis.clients.max_input_buffer"]
		out.Clients.MaxOutputBuf = gauges["redis.clients.max_output_buffer"]
		out.Memory.UsedBytes = gauges["redis.memory.used"]
		out.Memory.RSSBytes = gauges["redis.memory.rss"]
		out.Memory.PeakBytes = gauges["redis.memory.peak"]
		out.Memory.MaxBytes = gauges["redis.memory.max"]
		out.Memory.FragmentationRatio = gauges["redis.memory.fragmentation_ratio"]
		out.Memory.LuaBytes = gauges["redis.memory.lua"]
		out.ReplLagBytes = gauges["redis.replication.replica_offset"]
		out.ChangesSince = gauges["redis.rdb.changes_since_last_save"]
		out.SlowlogEntries = gauges["redis.slowlog.length"]
		out.Status = "up"
	}
	if rates, err := s.queryRedisRates(ctx, from, to, instance, hasInstanceFilter, windowSec); err == nil {
		out.CommandsPS = rates["redis.commands"]
		if out.CommandsPS == 0 {
			out.CommandsPS = rates["redis.commands.processed"]
		}
		out.NetInputBPS = rates["redis.net.input"]
		out.NetOutputBPS = rates["redis.net.output"]
		out.KeyspaceHitsPS = rates["redis.keyspace.hits"]
		out.KeyspaceMissPS = rates["redis.keyspace.misses"]
		out.EvictedPS = rates["redis.keys.evicted"]
		out.ExpiredPS = rates["redis.keys.expired"]
		out.ConnRefusedPS = rates["redis.connections.rejected"]
	}

	// Derived hit rate %.
	total := out.KeyspaceHitsPS + out.KeyspaceMissPS
	if total > 0 {
		out.HitRatePct = (out.KeyspaceHitsPS / total) * 100
	}
	// Derived memory usage % when maxmemory is configured.
	if out.Memory.MaxBytes > 0 {
		out.Memory.UsagePct = (out.Memory.UsedBytes / out.Memory.MaxBytes) * 100
	}

	// Role: redis receiver emits redis.role as a string-valued
	// gauge sometimes; otherwise look for a dedicated metric.
	if r, err := s.queryRedisRole(ctx, from, to, instance, hasInstanceFilter); err == nil && r != "" {
		out.Role = r
	}

	// Per-keyspace key counts.
	if ks, err := s.queryRedisKeyspaces(ctx, from, to, instance, hasInstanceFilter); err == nil {
		out.Keyspaces = ks
	}

	return out, nil
}

func (s *Store) queryRedisGauges(
	ctx context.Context, from, to time.Time, instance string, withInstance bool,
) (map[string]float64, error) {
	q := `
		SELECT metric, argMax(value, time) AS v
		FROM metric_points
		WHERE time >= ? AND time <= ?
		  AND startsWith(metric, 'redis.')
		` + redisInstanceClause(withInstance) + `
		GROUP BY metric` + dbInstanceQuerySettings
	args := []any{from, to}
	if withInstance {
		args = append(args, instance, instance)
	}
	return scanMetricMap(ctx, s, q, args)
}

func (s *Store) queryRedisRates(
	ctx context.Context, from, to time.Time, instance string, withInstance bool, windowSec float64,
) (map[string]float64, error) {
	q := `
		SELECT metric, (max(value) - min(value)) / ? AS rate
		FROM metric_points
		WHERE time >= ? AND time <= ?
		  AND startsWith(metric, 'redis.')
		` + redisInstanceClause(withInstance) + `
		GROUP BY metric` + dbInstanceQuerySettings
	args := []any{windowSec, from, to}
	if withInstance {
		args = append(args, instance, instance)
	}
	return scanMetricMap(ctx, s, q, args)
}

func (s *Store) queryRedisRole(
	ctx context.Context, from, to time.Time, instance string, withInstance bool,
) (string, error) {
	// Some Redis exporters attach role as an attribute on every
	// metric; pull the most-recent one as the source of truth.
	q := `
		SELECT argMax(attr_values[indexOf(attr_keys, 'role')], time) AS role
		FROM metric_points
		WHERE time >= ? AND time <= ?
		  AND startsWith(metric, 'redis.')
		  AND has(attr_keys, 'role')
		` + redisInstanceClause(withInstance) + dbInstanceQuerySettings
	args := []any{from, to}
	if withInstance {
		args = append(args, instance, instance)
	}
	row := s.conn.QueryRow(ctx, q, args...)
	var r string
	if err := row.Scan(&r); err != nil {
		return "", err
	}
	return r, nil
}

func (s *Store) queryRedisKeyspaces(
	ctx context.Context, from, to time.Time, instance string, withInstance bool,
) ([]RedisDB, error) {
	// redis.db.keys / redis.db.expires are dimensioned by the
	// `db` attribute ("db0", "db1", …).
	q := `
		SELECT attr_values[indexOf(attr_keys, 'db')] AS db,
		       metric,
		       argMax(value, time) AS v
		FROM metric_points
		WHERE time >= ? AND time <= ?
		  AND metric IN ('redis.db.keys', 'redis.db.expires', 'redis.keys', 'redis.keys.expires')
		  AND has(attr_keys, 'db')
		` + redisInstanceClause(withInstance) + `
		GROUP BY db, metric` + dbInstanceQuerySettings
	args := []any{from, to}
	if withInstance {
		args = append(args, instance, instance)
	}
	rows, err := s.conn.Query(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	byName := map[string]*RedisDB{}
	for rows.Next() {
		var db, metric string
		var v float64
		if err := rows.Scan(&db, &metric, &v); err != nil {
			continue
		}
		if db == "" {
			continue
		}
		entry, ok := byName[db]
		if !ok {
			entry = &RedisDB{Name: db}
			byName[db] = entry
		}
		switch metric {
		case "redis.db.keys", "redis.keys":
			entry.Keys = v
		case "redis.db.expires", "redis.keys.expires":
			entry.Expires = v
		}
	}
	out := make([]RedisDB, 0, len(byName))
	for _, e := range byName {
		out = append(out, *e)
	}
	return out, nil
}

func redisInstanceClause(withInstance bool) string {
	if !withInstance {
		return ""
	}
	return `AND (
		attr_values[indexOf(attr_keys, 'redis.instance.name')] = ?
		OR attr_values[indexOf(attr_keys, 'server.address')] = ?
	)`
}
