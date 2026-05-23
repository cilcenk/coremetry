package api

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"time"
)

// CHHealth — v0.5.319. Datadog-style ClickHouse dashboard payload.
// Gives the operator a single-page view of CH-side perf health:
// slow queries, merge queue depth, part overflow risk, async insert
// pressure. Powers the new /admin/clickhouse page.
//
// Read directly from system.* tables which CH provides as built-in
// observability surface. Each panel is fail-isolated — a CH version
// without one of the views (e.g. older OS edition without
// system.async_inserts) leaves the slot at its zero value rather
// than 500-ing the whole page.
type CHHealth struct {
	// v0.5.388 — topology banner. First field on the payload so
	// the operator's eye lands on the cluster mode + node count
	// before drilling into the perf panels. Powered by:
	//   - cfg.ClusterName (operator-set env var) → "configured"
	//     side
	//   - system.clusters lookup (CH self-report) → "live" side
	//   - system.tables engine filter → which tables wear the
	//     Distributed wrapper vs plain MergeTree
	// All three are needed because a misconfigured cluster name
	// looks identical to standalone from the driver — only
	// system.clusters confirms ZK actually wired the node up.
	Topology       CHTopology       `json:"topology"`
	SlowQueries    []CHSlowQuery    `json:"slowQueries"`
	Merges         []CHMerge        `json:"merges"`
	PartHotspots   []CHPartHotspot  `json:"partHotspots"`
	Replication    []CHReplicationLag `json:"replicationLag,omitempty"`
	// v0.5.346 — in-flight async_insert batches. Each row =
	// one currently-buffered INSERT awaiting flush. Lets the
	// operator see whether the tuned async_insert_busy_timeout
	// is doing useful coalescence or sitting idle.
	AsyncInserts   []CHAsyncInsert  `json:"asyncInserts,omitempty"`
	Generated      int64            `json:"generatedAt"` // unix ns of snapshot
}

// CHTopology — what cluster does Coremetry think it's talking to,
// and does the live CH agree.
type CHTopology struct {
	// Mode is "cluster" when an ON CLUSTER name is configured AND
	// system.clusters confirms it exists; "standalone" otherwise.
	// A misconfigured cluster name (operator set the env var but
	// the CH server has no matching <remote_servers> block) shows
	// up as "standalone" with a non-empty ConfiguredCluster — the
	// banner flags this mismatch.
	Mode               string         `json:"mode"`
	ConfiguredCluster  string         `json:"configuredCluster,omitempty"`
	Database           string         `json:"database"`
	ConnectedHosts     []string       `json:"connectedHosts,omitempty"`
	// Nodes — one entry per (shard, replica) registered in
	// system.clusters for the configured cluster. Empty in
	// standalone mode.
	Nodes              []CHClusterNode `json:"nodes,omitempty"`
	// Table engine breakdown — drives the "distributed table?"
	// answer. DistributedTables > 0 = the install is running the
	// full cluster pattern (Distributed wrapper + _local
	// Replicated tables). LocalReplicated = Replicated*MergeTree
	// count. Plain = MergeTree / ReplacingMergeTree without
	// replication. Used to confirm migrations actually built the
	// cluster pattern they were supposed to.
	DistributedTables  int            `json:"distributedTables"`
	LocalReplicated    int            `json:"localReplicated"`
	PlainMergeTree     int            `json:"plainMergeTree"`
	// ZK / Keeper presence — system.zookeeper exists only when
	// CH is configured with a Keeper endpoint. ReplicatedMergeTree
	// needs this; absence on a cluster mode install is a config
	// bug that should surface here, not via a CREATE TABLE failure
	// six hours into ingest.
	ZooKeeperConnected bool           `json:"zookeeperConnected"`
}

type CHClusterNode struct {
	Cluster       string `json:"cluster"`
	ShardNum      uint32 `json:"shardNum"`
	ReplicaNum    uint32 `json:"replicaNum"`
	HostName      string `json:"hostName"`
	HostAddress   string `json:"hostAddress,omitempty"`
	Port          uint16 `json:"port"`
	IsLocal       bool   `json:"isLocal"`
}

type CHSlowQuery struct {
	Query        string  `json:"query"`
	ElapsedMs    float64 `json:"elapsedMs"`
	MemoryMB     float64 `json:"memoryMb"`
	ReadRows     uint64  `json:"readRows"`
	ResultRows   uint64  `json:"resultRows"`
	EventTimeNs  int64   `json:"eventTimeNs"`
	User         string  `json:"user"`
}

type CHMerge struct {
	Database    string  `json:"database"`
	Table       string  `json:"table"`
	ElapsedSec  float64 `json:"elapsedSec"`
	ProgressPct float64 `json:"progressPct"`
	RowsRead    uint64  `json:"rowsRead"`
	MergedSize  uint64  `json:"mergedSizeBytes"`
}

type CHPartHotspot struct {
	Database  string `json:"database"`
	Table     string `json:"table"`
	Parts     uint64 `json:"parts"`     // active parts count
	RowsTotal uint64 `json:"rowsTotal"`
	BytesTotal uint64 `json:"bytesTotal"`
}

type CHReplicationLag struct {
	Database          string `json:"database"`
	Table             string `json:"table"`
	QueueSize         uint32 `json:"queueSize"`
	AbsoluteDelay     uint64 `json:"absoluteDelaySec"`
}

// CHAsyncInsert mirrors one row of system.asynchronous_inserts
// — a buffered INSERT awaiting the server-side coalescing
// flush. ageMs > 0 = how long it's been sitting; bytes/rows
// reflect what's queued so far. The whole list usually has
// a few rows during steady ingest, climbs visibly under
// burst (good sign — coalescence working) and falls back as
// the busy_timeout fires.
type CHAsyncInsert struct {
	Database string `json:"database"`
	Table    string `json:"table"`
	TotalBytes uint64 `json:"totalBytes"`
	EntriesCount uint64 `json:"entriesCount"`
	FirstUpdateMsAgo uint64 `json:"firstUpdateMsAgo"`
}

// getClickHouseHealth — admin-only. Cached 5s (CH self-stats are
// cheap; 5s amortises across a tab full of operators).
func (s *Server) getClickHouseHealth(w http.ResponseWriter, r *http.Request) {
	s.serveCached(w, r, "ch-health", 5*time.Second, func() (any, error) {
		ctx, cancel := context.WithTimeout(r.Context(), 8*time.Second)
		defer cancel()
		out := CHHealth{Generated: time.Now().UnixNano()}

		// ── Topology banner (cluster / standalone) ─────────────
		// Configured side from the running process, live side
		// from the CH server's own self-reports.
		out.Topology.ConfiguredCluster = s.store.ClusterName()
		out.Topology.Database = s.store.DatabaseName()
		out.Topology.ConnectedHosts = s.store.ConnectedHosts()

		// system.clusters lookup — confirms the configured cluster
		// name actually exists on the server. Empty result with
		// a non-empty ConfiguredCluster = misconfig (env var set,
		// but remote_servers.xml has no matching block).
		if name := out.Topology.ConfiguredCluster; name != "" {
			if rows, err := s.store.Conn().Query(ctx, `
				SELECT cluster, shard_num, replica_num,
				       host_name, host_address, port, is_local
				FROM system.clusters
				WHERE cluster = ?
				ORDER BY shard_num, replica_num
				SETTINGS max_execution_time = 3`, name); err == nil {
				for rows.Next() {
					var n CHClusterNode
					var isLocal uint8
					if err := rows.Scan(&n.Cluster, &n.ShardNum, &n.ReplicaNum,
						&n.HostName, &n.HostAddress, &n.Port, &isLocal); err != nil {
						continue
					}
					n.IsLocal = isLocal == 1
					out.Topology.Nodes = append(out.Topology.Nodes, n)
				}
				rows.Close()
			}
		}

		// Mode resolution: cluster-confirmed = ON CLUSTER name was
		// set AND system.clusters returned ≥ 1 node. Otherwise we
		// treat it as standalone — even if the env var was set —
		// because that's the operational truth from CH's view.
		if out.Topology.ConfiguredCluster != "" && len(out.Topology.Nodes) > 0 {
			out.Topology.Mode = "cluster"
		} else {
			out.Topology.Mode = "standalone"
		}

		// Engine breakdown — count Distributed, Replicated*, and
		// plain MergeTree tables in the database the running pod
		// is bound to. Drives the "is Coremetry using Distributed
		// tables" answer the operator literally asked for.
		if rows, err := s.store.Conn().Query(ctx, `
			SELECT engine, count() AS n
			FROM system.tables
			WHERE database = currentDatabase()
			GROUP BY engine
			SETTINGS max_execution_time = 3`); err == nil {
			for rows.Next() {
				var engine string
				var n uint64
				if err := rows.Scan(&engine, &n); err != nil {
					continue
				}
				switch {
				case engine == "Distributed":
					out.Topology.DistributedTables += int(n)
				case strings.HasPrefix(engine, "Replicated"):
					out.Topology.LocalReplicated += int(n)
				case strings.HasSuffix(engine, "MergeTree"):
					out.Topology.PlainMergeTree += int(n)
				}
			}
			rows.Close()
		}

		// ZooKeeper / Keeper probe — non-fatal if absent. A
		// cluster install missing this is misconfigured; standalone
		// installs don't need it. Single-row scan of system.zookeeper
		// root path with depth=0 — cheap.
		if rows, err := s.store.Conn().Query(ctx, `
			SELECT count() FROM system.zookeeper WHERE path = '/'
			SETTINGS max_execution_time = 2`); err == nil {
			for rows.Next() {
				var c uint64
				if err := rows.Scan(&c); err == nil && c > 0 {
					out.Topology.ZooKeeperConnected = true
				}
			}
			rows.Close()
		}

		// ── Slow queries (top 20 over the last 1h) ─────────────
		// Bounded by event_time + LIMIT; partition-pruned via the
		// query_log's own toYYYYMMDD(event_date) primary key.
		if rows, err := s.store.Conn().Query(ctx, `
			SELECT
			  query,
			  query_duration_ms                AS elapsed_ms,
			  memory_usage / 1048576.0          AS memory_mb,
			  read_rows,
			  result_rows,
			  toUnixTimestamp64Nano(toDateTime64(event_time, 9)) AS event_time_ns,
			  user
			FROM system.query_log
			WHERE event_time >= now() - INTERVAL 1 HOUR
			  AND type = 'QueryFinish'
			  AND query_duration_ms > 500
			ORDER BY query_duration_ms DESC
			LIMIT 20
			SETTINGS max_execution_time = 3`); err == nil {
			defer rows.Close()
			for rows.Next() {
				var q CHSlowQuery
				if err := rows.Scan(&q.Query, &q.ElapsedMs, &q.MemoryMB,
					&q.ReadRows, &q.ResultRows, &q.EventTimeNs, &q.User); err != nil {
					continue
				}
				// Truncate query body — UI shows full on hover.
				if len(q.Query) > 600 {
					q.Query = q.Query[:600] + "…"
				}
				out.SlowQueries = append(out.SlowQueries, q)
			}
		}

		// ── In-flight merges ───────────────────────────────────
		if rows, err := s.store.Conn().Query(ctx, `
			SELECT database, table,
			       elapsed,
			       progress * 100   AS progress_pct,
			       rows_read,
			       merged_rows_bytes
			FROM system.merges
			ORDER BY elapsed DESC
			LIMIT 20
			SETTINGS max_execution_time = 3`); err == nil {
			defer rows.Close()
			for rows.Next() {
				var m CHMerge
				if err := rows.Scan(&m.Database, &m.Table, &m.ElapsedSec,
					&m.ProgressPct, &m.RowsRead, &m.MergedSize); err != nil {
					continue
				}
				out.Merges = append(out.Merges, m)
			}
		}

		// ── Part hotspots ──────────────────────────────────────
		// Tables with the most active parts. CH merges parts in
		// the background; if the count grows >300 per partition
		// the table is falling behind on merges → reads slow,
		// SELECT plans fragment. Threshold at LIMIT level only
		// (top 15 by part count).
		if rows, err := s.store.Conn().Query(ctx, `
			SELECT database, table,
			       count()                  AS parts,
			       sum(rows)                AS rows_total,
			       sum(bytes_on_disk)       AS bytes_total
			FROM system.parts
			WHERE active = 1
			GROUP BY database, table
			ORDER BY parts DESC
			LIMIT 15
			SETTINGS max_execution_time = 3`); err == nil {
			defer rows.Close()
			for rows.Next() {
				var p CHPartHotspot
				if err := rows.Scan(&p.Database, &p.Table, &p.Parts,
					&p.RowsTotal, &p.BytesTotal); err != nil {
					continue
				}
				out.PartHotspots = append(out.PartHotspots, p)
			}
		}

		// ── Async insert buffer (v0.5.346) ─────────────────────
		// system.asynchronous_inserts shows currently-buffered
		// INSERTs awaiting the server-side flush. Tail-empty on
		// idle pods; non-zero during burst = coalescence
		// working. Available on CH 22.10+; silently empty on
		// older builds.
		if rows, err := s.store.Conn().Query(ctx, `
			SELECT database, table,
			       total_bytes, entries.bytes_count[1] AS entries_count,
			       dateDiff('millisecond', first_update, now64()) AS first_update_ms_ago
			FROM system.asynchronous_inserts
			ORDER BY total_bytes DESC
			LIMIT 20
			SETTINGS max_execution_time = 3`); err == nil {
			defer rows.Close()
			for rows.Next() {
				var a CHAsyncInsert
				var firstMs int64
				if err := rows.Scan(&a.Database, &a.Table,
					&a.TotalBytes, &a.EntriesCount, &firstMs); err != nil {
					continue
				}
				if firstMs > 0 {
					a.FirstUpdateMsAgo = uint64(firstMs)
				}
				out.AsyncInserts = append(out.AsyncInserts, a)
			}
		}

		// ── Replication queue lag (clustered installs) ─────────
		// system.replicas exists only on Replicated*MergeTree
		// engines; silently empty on a single-shard deployment.
		if rows, err := s.store.Conn().Query(ctx, `
			SELECT database, table,
			       queue_size,
			       absolute_delay
			FROM system.replicas
			WHERE queue_size > 0 OR absolute_delay > 0
			ORDER BY absolute_delay DESC
			LIMIT 20
			SETTINGS max_execution_time = 3`); err == nil {
			defer rows.Close()
			for rows.Next() {
				var l CHReplicationLag
				if err := rows.Scan(&l.Database, &l.Table, &l.QueueSize, &l.AbsoluteDelay); err != nil {
					continue
				}
				out.Replication = append(out.Replication, l)
			}
		}

		return out, nil
	})
}

// summary helpers — used by the panel header to surface single
// at-a-glance numbers without forcing the operator to read every
// row.
func (h CHHealth) maxMergeElapsed() float64 {
	max := 0.0
	for _, m := range h.Merges {
		if m.ElapsedSec > max {
			max = m.ElapsedSec
		}
	}
	return max
}
func (h CHHealth) maxPartCount() uint64 {
	var max uint64
	for _, p := range h.PartHotspots {
		if p.Parts > max {
			max = p.Parts
		}
	}
	return max
}

// String — short summary line used in /admin/stats footer link.
func (h CHHealth) String() string {
	return fmt.Sprintf("CH health: %d slow / %d merges / max %d parts",
		len(h.SlowQueries), len(h.Merges), h.maxPartCount())
}
