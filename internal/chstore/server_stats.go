package chstore

import (
	"context"
	"fmt"
	"log"
	"sort"
	"strings"
)

// Live ClickHouse node utilisation (v0.9.290, operator ask: "can I see
// the ClickHouse server's memory and CPU utilisation too").
//
// /admin/stats could already say how much room the data OCCUPIES
// (system.parts) and, since v0.9.289, how much room is LEFT
// (system.disks). This is the third question and the one that explains
// a failing query rather than a filling disk: is the node under
// pressure right now, and what are the memory ceilings a code-241
// actually hit.
//
// Three system tables are folded into one (host, key, value) read.
// They are all in-memory counters, so the cost is independent of how
// much data the cluster holds — no scan, no aggregation.

// serverAsyncMetrics — from system.asynchronous_metrics. Sampled by
// ClickHouse on a timer; the *Normalized CPU counters are already
// divided by core count, so 1.0 means every core saturated.
var serverAsyncMetrics = []string{
	"OSMemoryTotal", "OSMemoryAvailable", "MemoryResident",
	"OSUserTimeNormalized", "OSSystemTimeNormalized", "OSIOWaitTimeNormalized",
	"LoadAverage1", "Uptime",
}

// serverLiveMetrics — from system.metrics. Instantaneous, not sampled.
var serverLiveMetrics = []string{"MemoryTracking", "Query", "Merge"}

// serverMemoryLimits — from system.server_settings. These are the
// ceilings named in a "Query memory limit exceeded" (code 241), which
// is why they belong on this panel: the error quotes a number the
// operator otherwise has to go find on the node.
var serverMemoryLimits = []string{"max_server_memory_usage"}

// collectServerStats reads per-node utilisation. Never returns an
// error: this is an optional panel and /admin/stats must not go blank
// because a credential lacks access to a system table (same contract as
// the storage and disk blocks).
func (s *Store) collectServerStats(ctx context.Context) []ServerStat {
	// system.* are LOCAL tables. On an external Distributed cluster a
	// plain read describes only the node we happen to be connected to.
	// v0.9.454 — clusterAllReplicas, cluster() DEĞİL: utilizasyon
	// (bellek/CPU sayaçları) node-düzeyi metadata'dır; cluster() her
	// shard'dan tek replika okuyup 4 node'lu (2×2) kümede 2 node
	// gösteriyordu — disks paneliyle aynı operatör bulgusu.
	host := "''"
	wrap := func(tbl string) string { return tbl }
	if cn := strings.TrimSpace(s.cfg.ClusterName); cn != "" {
		host = "hostName()"
		wrap = func(tbl string) string { return "clusterAllReplicas('" + cn + "', " + tbl + ")" }
	}

	quote := func(names []string) string {
		out := make([]string, 0, len(names))
		for _, n := range names {
			out = append(out, "'"+n+"'")
		}
		return strings.Join(out, ",")
	}

	// One round trip. toFloat64OrZero on the settings value keeps a
	// non-numeric setting from failing the whole read.
	q := fmt.Sprintf(`
		SELECT %[1]s AS host, metric AS k, toFloat64(value) AS v
		  FROM %[2]s WHERE metric IN (%[5]s)
		UNION ALL
		SELECT %[1]s, metric, toFloat64(value)
		  FROM %[3]s WHERE metric IN (%[6]s)
		UNION ALL
		SELECT %[1]s, name, toFloat64OrZero(value)
		  FROM %[4]s WHERE name IN (%[7]s)
		SETTINGS max_execution_time = 5`,
		host,
		wrap("system.asynchronous_metrics"),
		wrap("system.metrics"),
		wrap("system.server_settings"),
		quote(serverAsyncMetrics), quote(serverLiveMetrics), quote(serverMemoryLimits))

	rows, err := s.conn.Query(ctx, q)
	if err != nil {
		log.Printf("[sysstats] server utilisation query: %v — surfacing dashboard without it", err)
		return nil
	}
	defer rows.Close()

	byHost := map[string]*ServerStat{}
	for rows.Next() {
		var h, k string
		var v float64
		if err := rows.Scan(&h, &k, &v); err != nil {
			break
		}
		st, ok := byHost[h]
		if !ok {
			st = &ServerStat{Host: h}
			byHost[h] = st
		}
		applyServerMetric(st, k, v)
	}
	if err := rows.Err(); err != nil {
		log.Printf("[sysstats] server utilisation stream: %v — partial node list", err)
	}

	out := make([]ServerStat, 0, len(byHost))
	for _, st := range byHost {
		// v0.9.975 — the per-query ceiling is a CLIENT-side setting, not
		// a server one: system.server_settings only knows the profile
		// default, never what this driver actually sends. Carrying the
		// boot-resolved pair here is what lets /admin/stats show
		// "configured X, effective Y" side by side with the server
		// ceiling that forced it. (Pre-v0.9.975 MaxQueryMemory was
		// declared but nothing ever populated it — the field rendered as
		// absent on every install.)
		st.MaxQueryMemory = uint64(s.EffectiveQueryMemory())
		st.ConfiguredQueryMemory = uint64(s.ConfiguredQueryMemory())
		out = append(out, *st)
	}
	// Deterministic order so the panel doesn't reshuffle between polls.
	sort.Slice(out, func(i, j int) bool { return out[i].Host < out[j].Host })
	return out
}

// applyServerMetric folds one (key, value) row into a ServerStat. Split
// out from the query so the whole mapping is unit-testable without a
// cluster — an unknown key must be ignored, not panic, because the
// metric vocabulary shifts between ClickHouse versions.
func applyServerMetric(st *ServerStat, key string, v float64) {
	// Counters are non-negative by definition; a negative reading is a
	// sampling artefact and must not underflow the unsigned fields.
	u := func() uint64 {
		if v < 0 {
			return 0
		}
		return uint64(v)
	}
	switch key {
	case "OSMemoryTotal":
		st.OSMemoryTotal = u()
	case "OSMemoryAvailable":
		st.OSMemoryAvailable = u()
	case "MemoryResident":
		st.MemoryResident = u()
	case "MemoryTracking":
		st.MemoryTracking = u()
	case "max_server_memory_usage":
		st.MaxServerMemory = u()
	case "max_memory_usage":
		st.MaxQueryMemory = u()
	case "OSUserTimeNormalized":
		st.CPUUser = v
	case "OSSystemTimeNormalized":
		st.CPUSystem = v
	case "OSIOWaitTimeNormalized":
		st.CPUIOWait = v
	case "LoadAverage1":
		st.LoadAvg1 = v
	case "Uptime":
		st.UptimeSec = v
	case "Query":
		st.RunningQueries = u()
	case "Merge":
		st.RunningMerges = u()
	}
}
