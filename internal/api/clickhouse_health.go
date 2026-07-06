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
	// v0.6.22 — in-flight ALTER TABLE … DELETE / UPDATE
	// mutations. Healthy queue is empty; growing queue is an
	// operator-visible signal that a state-table mutation
	// pattern needs rethinking (tombstone, ReplacingMergeTree,
	// etc.). Top 20 most-recent unfinished, sorted by parts-
	// remaining desc so the worst offender lands on top.
	Mutations      []CHMutation     `json:"mutations,omitempty"`
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
	// v0.5.419 — resolved per-table shard expression map. Lets
	// the operator audit "which shard key did each table actually
	// get?" without `SHOW CREATE TABLE` round-trips. Empty in
	// standalone mode.
	ShardPolicy map[string]string `json:"shardPolicy,omitempty"`
	// v0.5.428 — bug-fix: distinguish "system.clusters probe failed"
	// from "probe succeeded but returned no rows". Without this the
	// frontend's misconfig banner false-positives when CH is busy
	// enough to time out the probe (v0.5.424 raised the timeout
	// from 3s to 8s, but operators still hit it intermittently).
	// Empty when the probe completed cleanly (regardless of result
	// count); populated with the error string when it failed. The
	// frontend treats a populated error as "soft warning, can't
	// confirm cluster" rather than the hard "misconfig" banner.
	ClusterProbeError string `json:"clusterProbeError,omitempty"`
	// v0.5.439 — when the live probe fails but a recent successful
	// probe is cached, Nodes is filled from the cache and these
	// two flags surface the staleness so the frontend renders a
	// small "last refreshed N min ago" pill instead of the warn
	// banner. ClusterProbeError stays empty in this path — the
	// banner only fires when there's no cache to fall back on.
	ClusterNodesStale     bool  `json:"clusterNodesStale,omitempty"`
	ClusterNodesAgeMs     int64 `json:"clusterNodesAgeMs,omitempty"`
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

// CHMutation — v0.6.22. One row per in-flight or recently-stuck
// ALTER TABLE … DELETE / UPDATE mutation. ALTER mutations rewrite
// matching parts in the background; healthy systems clear them
// within seconds. A growing queue is the operator-visible
// signature that a state table is being mutated faster than CH
// can rebuild parts — at which point the right fix is a tombstone
// or ReplacingMergeTree pattern, not "wait longer".
//
// Powered by system.mutations. Filter is_done = 0 so only the
// queue depth shows up; finished mutations age out of the table
// after ~7 days and don't matter for live ops.
type CHMutation struct {
	Database  string `json:"database"`
	Table     string `json:"table"`
	Command   string `json:"command"`     // trimmed; full text would be unbounded
	Parts     uint64 `json:"parts"`        // parts left to mutate
	ElapsedMs int64  `json:"elapsedMs"`    // since the mutation was submitted
	LatestFail string `json:"latestFail,omitempty"`
}

// getClickHouseHealth — admin-only. Cached 5s (CH self-stats are
// cheap; 5s amortises across a tab full of operators).
func (s *Server) getClickHouseHealth(w http.ResponseWriter, r *http.Request) {
	s.serveCached(w, r, "ch-health", 5*time.Second, func(ctx context.Context) (any, error) {
		ctx, cancel := context.WithTimeout(ctx, 8*time.Second)
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
		// v0.5.424 — operator-reported: misconfig banner fired
		// on a healthy cluster where CREATE TABLE ON CLUSTER
		// clearly worked. Root cause: 3s timeout was too tight
		// on a busy production CH; the system.clusters query
		// silently failed (err != nil branch) and the empty
		// Nodes result tripped the misconfig path. Bumped to
		// 8s so transient load can't false-positive the banner.
		if name := out.Topology.ConfiguredCluster; name != "" {
			probeErr := ""
			var probedNodes []CHClusterNode
			rows, err := s.store.Conn().Query(ctx, `
				SELECT cluster, shard_num, replica_num,
				       host_name, host_address, port, is_local
				FROM system.clusters
				WHERE cluster = ?
				ORDER BY shard_num, replica_num
				SETTINGS max_execution_time = 8`, name)
			if err != nil {
				probeErr = err.Error()
			} else {
				for rows.Next() {
					var n CHClusterNode
					var isLocal uint8
					if err := rows.Scan(&n.Cluster, &n.ShardNum, &n.ReplicaNum,
						&n.HostName, &n.HostAddress, &n.Port, &isLocal); err != nil {
						continue
					}
					n.IsLocal = isLocal == 1
					probedNodes = append(probedNodes, n)
				}
				if rerr := rows.Err(); rerr != nil {
					probeErr = rerr.Error()
				}
				rows.Close()
			}

			// v0.5.439 — cache + stale-fallback. On success, refresh
			// the cache. On failure with a recent cached snapshot,
			// fill Nodes from cache + flag staleness so the
			// frontend shows a small "last refreshed N min ago"
			// pill rather than the warn banner. The cache TTL is
			// generous (15 min) — cluster topology rarely changes,
			// and the alternative (warn banner flashing on every
			// transient timeout) is worse UX than briefly-stale
			// nodes. Beyond 15 min OR no cache at all → fall back
			// to the v0.5.428 ClusterProbeError banner.
			const staleHorizon = 15 * time.Minute
			switch {
			case probeErr == "":
				// Live probe succeeded — use it, refresh cache.
				out.Topology.Nodes = probedNodes
				s.clusterNodesMu.Lock()
				s.lastClusterNodes = append(s.lastClusterNodes[:0], probedNodes...)
				s.lastClusterAt = time.Now()
				s.clusterNodesMu.Unlock()
			default:
				// Probe failed. Try cache.
				s.clusterNodesMu.RLock()
				cached := append([]CHClusterNode(nil), s.lastClusterNodes...)
				cachedAt := s.lastClusterAt
				s.clusterNodesMu.RUnlock()
				if len(cached) > 0 && !cachedAt.IsZero() && time.Since(cachedAt) < staleHorizon {
					out.Topology.Nodes = cached
					out.Topology.ClusterNodesStale = true
					out.Topology.ClusterNodesAgeMs = time.Since(cachedAt).Milliseconds()
					// Don't set ClusterProbeError — the frontend's
					// stale pill carries the "couldn't refresh"
					// signal at a softer volume.
				} else {
					// No usable cache → keep current v0.5.428
					// behaviour: surface the probe error for the
					// warn banner.
					out.Topology.ClusterProbeError = probeErr
				}
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

		// v0.5.419 — resolved shard policy per table. Only
		// populated in cluster mode (standalone has no shards).
		if out.Topology.Mode == "cluster" {
			out.Topology.ShardPolicy = s.store.ShardPolicy()
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

		// ── In-flight mutations (v0.6.22) ─────────────────────
		// system.mutations rows where is_done = 0. Each row =
		// one ALTER … DELETE / UPDATE still rewriting parts.
		// Healthy = 0 rows. Steady non-zero = mutation pattern
		// faster than merges can keep up — recommend a switch
		// to ReplacingMergeTree(version) or a tombstone column.
		// command text is truncated to 200 chars so a giant
		// IN(…) literal doesn't blow the panel layout.
		if rows, err := s.store.Conn().Query(ctx, `
			SELECT database, table,
			       substring(command, 1, 200) AS command,
			       parts_to_do                AS parts,
			       dateDiff('millisecond', create_time, now64()) AS elapsed_ms,
			       latest_fail_reason
			FROM system.mutations
			WHERE is_done = 0
			ORDER BY parts DESC
			LIMIT 20
			SETTINGS max_execution_time = 3`); err == nil {
			defer rows.Close()
			for rows.Next() {
				var m CHMutation
				if err := rows.Scan(&m.Database, &m.Table,
					&m.Command, &m.Parts, &m.ElapsedMs, &m.LatestFail); err != nil {
					continue
				}
				out.Mutations = append(out.Mutations, m)
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
