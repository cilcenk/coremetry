package chstore

import (
	"context"
	"fmt"
	"log"
	"strings"
)

// PurgeResult reports the outcome of a telemetry purge.
type PurgeResult struct {
	TablesPurged []string `json:"tablesPurged"`        // truncated successfully
	Skipped      []string `json:"skipped,omitempty"`   // absent on this install (e.g. op_group MV)
	Errors       []string `json:"errors,omitempty"`    // per-table failures (best-effort: purge continues)
}

// telemetryPurgeTables is the EXPLICIT allowlist of observability-DATA tables
// the "factory reset" empties. Fail-safe by construction: ONLY these are
// touched, so any table NOT listed — config, users, audit_log, alert rules,
// saved views, monitors, status page, LDAP/system_settings, … — is preserved.
// A future config table can never be wiped by accident. Keep in sync with the
// CREATE statements in store.go / chmigrate; TestPurgeAllowlistExcludesConfig
// pins that no config table leaks in.
var telemetryPurgeTables = []string{
	// raw signals
	"spans", "logs", "metric_points", "profiles",
	// RED / aggregation MVs
	"service_summary_5m", "operation_summary_5m", "operation_group_summary_5m",
	"db_summary_5m", "db_caller_summary_5m",
	"messaging_summary_5m", "messaging_caller_summary_5m",
	"spanmetrics_1s", "spanmetrics_10s", "spanmetrics_1m",
	"spanmetrics_calls_5m", "spanmetrics_duration_5m", "spanmetrics_hist_5m",
	"service_callers_5m",
	// topology
	"topology_edges_5m", "topology_op_edges_5m", "topology_root_flows_5m",
	// trace rollups
	"trace_summary_5m", "trace_summary_1d", "trace_service_index_5m", "trace_snapshots",
	// PURELY-mechanical generated analysis — written only by ingest / detector /
	// worker loops, no operator content, regenerates from new telemetry.
	// NOTE: problems, incidents, incident_events, incident_problems,
	// exception_groups, events and runbook_executions are DELIBERATELY excluded —
	// they carry operator-authored content (manual incidents, post-mortem notes,
	// acknowledgements, triage/assignment, deploy annotations, remediation
	// history). A telemetry purge must not erase operator work, so they are
	// preserved (see configPreserveTables) even though they reference telemetry
	// that is gone. Clear them from their own surfaces if a fuller reset is
	// wanted.
	"anomaly_events", "root_cause_hypotheses", "ai_calls", "monitor_results",
}

// configPreserveTables is NOT consulted at runtime. It exists so the regression
// test can assert telemetryPurgeTables never intersects the operator-owned /
// config / operator-authored set — the safety contract of the feature.
var configPreserveTables = []string{
	// config / settings
	"system_settings", "alert_rules", "saved_views", "users", "service_metadata",
	"dashboards", "audit_log", "notification_channels", "anomaly_silences",
	"maintenance_windows", "monitors", "runbooks", "slos", "service_contracts",
	"status_page_components", "status_page_config", "status_page_published",
	"status_page_subscribers", "log_templates", "feedbacks",
	// operator-authored analysis content (manual records / notes / acks / triage)
	"problems", "incidents", "incident_events", "incident_problems",
	"exception_groups", "events", "runbook_executions",
}

// PurgeTelemetry empties every observability-DATA table (telemetryPurgeTables),
// preserving all configuration. Best-effort: a per-table failure is recorded
// and the purge continues. Each table's storage is resolved by engine so it
// works across deployment modes:
//   - plain MergeTree/Replicated → TRUNCATE the table directly
//   - combined MaterializedView  → TRUNCATE its hidden `.inner_id.<uuid>`
//   - Distributed wrapper        → TRUNCATE the `<name>_local` shard table
//     ON CLUSTER (cluster derived from the engine def, or cfg.ClusterName);
//     if the local is itself an MV, its inner is truncated.
// Carries the volume-guard overrides (max_table_size_to_drop /
// max_partition_size_to_drop = 0) so a huge spans table truncates regardless of
// accumulated size — same guard dropCombinedMV uses.
func (s *Store) PurgeTelemetry(ctx context.Context) (PurgeResult, error) {
	res := PurgeResult{}
	for _, t := range telemetryPurgeTables {
		// op_group MV is disabled when the spans table lacks op_group (external
		// Distributed, cluster_name unset) — it doesn't exist, nothing to purge.
		if t == "operation_group_summary_5m" && !s.hasOpGroupCol {
			res.Skipped = append(res.Skipped, t)
			continue
		}
		stmt, skip, err := s.truncateStmt(ctx, t)
		if skip {
			res.Skipped = append(res.Skipped, t)
			continue
		}
		if err != nil {
			res.Errors = append(res.Errors, fmt.Sprintf("%s: %v", t, err))
			continue
		}
		if e := s.conn.Exec(ctx, stmt); e != nil {
			log.Printf("[chstore] purge: truncate %s failed: %v", t, e)
			res.Errors = append(res.Errors, fmt.Sprintf("%s: %v", t, e))
			continue
		}
		res.TablesPurged = append(res.TablesPurged, t)
	}
	if len(res.Errors) > 0 {
		return res, fmt.Errorf("telemetry purge completed with %d error(s)", len(res.Errors))
	}
	return res, nil
}

const purgeGuard = " SETTINGS max_table_size_to_drop = 0, max_partition_size_to_drop = 0"

// truncateStmt resolves the correct TRUNCATE for one telemetry table by
// inspecting its engine. skip=true when the table is absent on this install.
func (s *Store) truncateStmt(ctx context.Context, name string) (stmt string, skip bool, err error) {
	engine, engineFull, uuid, found, qerr := s.lookupTable(ctx, name)
	if qerr != nil {
		return "", false, qerr
	}
	if !found {
		return "", true, nil
	}
	switch engine {
	case "Distributed":
		// Data is in <name>_local on the shards; truncate that ON CLUSTER.
		cluster := parseDistributedCluster(engineFull)
		if cluster == "" {
			cluster = strings.TrimSpace(s.cfg.ClusterName)
		}
		if cluster == "" {
			return "", false, fmt.Errorf("Distributed table: no cluster to truncate shards (set clickhouse.cluster_name)")
		}
		onCluster := " ON CLUSTER `" + cluster + "`"
		local := name + "_local"
		lEngine, _, lUUID, lFound, lerr := s.lookupTable(ctx, local)
		if lerr != nil {
			return "", false, lerr
		}
		if !lFound {
			// Wrapper without a discoverable local on this node — best-effort
			// truncate the local name ON CLUSTER (exists on the shards).
			return "TRUNCATE TABLE IF EXISTS " + local + onCluster + purgeGuard, false, nil
		}
		if lEngine == "MaterializedView" {
			return "TRUNCATE TABLE IF EXISTS " + innerName(lUUID) + onCluster + purgeGuard, false, nil
		}
		return "TRUNCATE TABLE IF EXISTS " + local + onCluster + purgeGuard, false, nil
	case "MaterializedView":
		if !validUUID(uuid) {
			return "", false, fmt.Errorf("MaterializedView %s has no inner storage uuid", name)
		}
		return "TRUNCATE TABLE IF EXISTS " + innerName(uuid) + s.onCluster() + purgeGuard, false, nil
	default:
		return "TRUNCATE TABLE IF EXISTS " + name + s.onCluster() + purgeGuard, false, nil
	}
}

func (s *Store) lookupTable(ctx context.Context, name string) (engine, engineFull, uuid string, found bool, err error) {
	// count() makes this an aggregate so it ALWAYS returns exactly one row —
	// cnt=0 means absent (skip), a Scan error means a real query/connection
	// problem (surface it), and the two are never conflated.
	var cnt uint64
	e := s.conn.QueryRow(ctx,
		"SELECT any(engine), any(engine_full), any(toString(uuid)), count() FROM system.tables "+
			"WHERE database = currentDatabase() AND name = ?", name).Scan(&engine, &engineFull, &uuid, &cnt)
	if e != nil {
		return "", "", "", false, e
	}
	return engine, engineFull, uuid, cnt > 0, nil
}

func innerName(uuid string) string { return "`.inner_id." + uuid + "`" }

func validUUID(u string) bool {
	return u != "" && u != "00000000-0000-0000-0000-000000000000"
}
