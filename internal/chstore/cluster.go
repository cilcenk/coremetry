package chstore

import (
	"context"
	"fmt"
	"regexp"
	"strings"
)

// Distributed-ClickHouse helpers. The application code reads and
// writes to logical table names like `spans` or `logs`. In single-
// node mode those names are concrete MergeTree tables. In cluster
// mode they are `Distributed` tables that fan inserts and queries
// out to per-shard `<name>_local` `ReplicatedMergeTree` tables.
// Either way, the rest of the app uses the un-suffixed name.
//
// `clusterMode()` returns true when the operator has set
// clickhouse.cluster_name in config; everything else flows from
// that single switch.

// clusterMode reports whether Distributed-CH schema should be used.
func (s *Store) clusterMode() bool { return strings.TrimSpace(s.cfg.ClusterName) != "" }

// onCluster appends an `ON CLUSTER <name>` clause when in cluster
// mode, otherwise returns "". Insert it right after the table /
// view name in any DDL statement so the same definition flows to
// every node in the cluster atomically.
func (s *Store) onCluster() string {
	if !s.clusterMode() {
		return ""
	}
	return " ON CLUSTER `" + s.cfg.ClusterName + "`"
}

// engine returns the storage-engine clause for a high-volume table:
//
//	single-node: `<base>()`           (e.g. MergeTree())
//	cluster:     `Replicated<base>(zk_path, replica)`
//
// `name` is used as the trailing path component in ZK so each table
// gets its own coordination znode. The {shard} / {replica} macros
// are resolved by ClickHouse from the per-server `macros` config
// (operator must define those macros on each replica's <macros>
// section in config.xml).
func (s *Store) engine(base, name string) string {
	if !s.clusterMode() {
		return base + "()"
	}
	prefix := strings.TrimRight(s.cfg.ReplicaPath, "/")
	if prefix == "" {
		prefix = "/clickhouse/tables"
	}
	return fmt.Sprintf("Replicated%s('%s/{shard}/%s', '{replica}')",
		base, prefix, name)
}

// shardKey returns the SQL expression placed in `Distributed(...,
// shard_key)`. Default `rand()` distributes evenly with no
// locality; an operator can override to e.g. `cityHash64(trace_id)`
// so spans belonging to the same trace co-locate on one shard
// (faster `GROUP BY trace_id`, slightly less even row-per-shard).
func (s *Store) shardKey() string {
	if k := strings.TrimSpace(s.cfg.ShardKey); k != "" {
		return k
	}
	return "rand()"
}

// LocalTableName returns the `<name>_local` flavour when in
// cluster mode, or the bare name otherwise. Used inside
// Replicated*MergeTree CREATE statements; the `<name>` itself is
// reserved for the Distributed wrapper. Also exported so external
// callers (chmigrate, ad-hoc tools) can resolve the per-shard
// table name without re-implementing the suffix logic.
func (s *Store) LocalTableName(name string) string {
	if !s.clusterMode() {
		return name
	}
	return name + "_local"
}

// highVolumeTables are the tables sharded across the cluster — they
// get `_local` Replicated*MergeTree per node plus a Distributed
// wrapper at the unsuffixed name. Materialized-view bodies that
// SELECT from these names are rewritten to read the `_local`
// flavour on the same node, so each shard's MV stays local.
//
// Other tables (admin / config) are Replicated for consistency
// across replicas but not sharded; the application reads them by
// the bare name and CH ZK keeps the copies in sync.
var highVolumeTables = map[string]bool{
	// Base tables — sharded directly.
	"spans":         true,
	"logs":          true,
	"metric_points": true,
	"profiles":      true,
	// Materialized views feeding off the high-volume base tables —
	// each shard aggregates its own slice, the Distributed wrapper
	// fans queries out across shards and merges.
	"service_summary_5m": true,
	"trace_summary_1d":   true,
}

// adaptDDL rewrites a DDL statement for the current mode.
//
// Single-node mode: returns the input unchanged in a 1-element
// slice. Existing behaviour is preserved exactly.
//
// Cluster mode: returns 1 or 2 statements:
//   - For an admin table: the original CREATE/ALTER with
//     ON CLUSTER injected and the engine swapped to its
//     `Replicated*` counterpart (e.g. ReplacingMergeTree →
//     ReplicatedReplacingMergeTree).
//   - For a high-volume table: the same transform, but the
//     primary table is renamed to `<name>_local`; a second
//     statement is emitted creating `<name>` as a Distributed
//     wrapper over the per-shard locals.
//   - For a materialized view feeding a high-volume table:
//     the view itself is renamed to `<name>_local`, its FROM
//     clause is rewritten to the matching `_local`, the engine
//     becomes Replicated, and a Distributed wrapper is emitted
//     so callers can still read from the un-suffixed name.
//
// The transformer is regex-based against the SQL strings we
// control in store.go — every production CREATE TABLE in this
// codebase uses one of:
//
//	ENGINE = MergeTree()
//	ENGINE = ReplacingMergeTree(version)
//	ENGINE = AggregatingMergeTree           (no parens)
//
// so the regex never sees user input.
func (s *Store) adaptDDL(sql string) []string {
	if !s.clusterMode() {
		return []string{sql}
	}

	on := s.onCluster()
	zkPrefix := strings.TrimRight(s.cfg.ReplicaPath, "/")
	if zkPrefix == "" {
		zkPrefix = "/clickhouse/tables"
	}

	// Find the table or MV name being created / altered.
	name, kind := identifyDDLTarget(sql)
	if name == "" {
		// Unknown shape — pass through untouched. Safe fallback;
		// the worst case is the statement runs on the connected
		// node only, same as single-node behaviour.
		return []string{sql}
	}
	hv := highVolumeTables[name]
	target := name
	if hv {
		target = name + "_local"
	}

	rewritten := sql

	// 1. Inject ON CLUSTER + (for high-volume) rename to _local.
	switch kind {
	case "table":
		rewritten = reCreateTable.ReplaceAllString(rewritten,
			"${1}"+target+on+" ")
	case "altertable":
		rewritten = reAlterTable.ReplaceAllString(rewritten,
			"${1}"+target+on+" ")
	case "mv":
		// MV: rewrite the view name AND the FROM <high-vol>
		// references in the SELECT body so each shard's MV
		// reads from its own local Replicated source.
		rewritten = reCreateMV.ReplaceAllString(rewritten,
			"${1}"+target+on+" ")
		for src := range highVolumeTables {
			pat := regexp.MustCompile(`(?i)\bFROM\s+` + regexp.QuoteMeta(src) + `\b`)
			rewritten = pat.ReplaceAllString(rewritten, "FROM "+src+"_local")
		}
	}

	// 2. Swap MergeTree-family engines to their Replicated
	// counterparts. Only on CREATE statements (ALTER doesn't
	// have an ENGINE clause).
	if kind == "table" || kind == "mv" {
		rewritten = reEngine.ReplaceAllStringFunc(rewritten, func(m string) string {
			parts := reEngine.FindStringSubmatch(m)
			base := parts[1]
			argList := strings.TrimSpace(parts[2]) // includes parens or empty
			zkPath := fmt.Sprintf("'%s/{shard}/%s', '{replica}'", zkPrefix, name)
			// Splice the ZK args in front of any existing args.
			var newArgs string
			switch {
			case argList == "" || argList == "()":
				newArgs = "(" + zkPath + ")"
			default:
				inside := strings.TrimSuffix(strings.TrimPrefix(argList, "("), ")")
				inside = strings.TrimSpace(inside)
				if inside == "" {
					newArgs = "(" + zkPath + ")"
				} else {
					newArgs = "(" + zkPath + ", " + inside + ")"
				}
			}
			return "ENGINE = Replicated" + base + newArgs
		})
	}

	out := []string{rewritten}

	// 3. For high-volume CREATE TABLE / CREATE MATERIALIZED VIEW,
	// emit the Distributed wrapper at the un-suffixed name.
	if hv && (kind == "table" || kind == "mv") {
		wrapper := fmt.Sprintf(
			"CREATE TABLE IF NOT EXISTS %s%s AS %s ENGINE = Distributed(`%s`, currentDatabase(), %s, %s)",
			name, on, target, s.cfg.ClusterName, target, s.shardKey())
		out = append(out, wrapper)
	}

	// 4. v0.5.362 — for a high-volume ALTER that changes the
	// column list, the rewritten step 1 above only updates the
	// `_local` ReplicatedMergeTree. The Distributed wrapper
	// keeps its own column list and stays behind; INSERT/SELECT
	// against the bare name then errors with "no such column …".
	// ClickHouse's Distributed engine accepts ADD COLUMN /
	// DROP COLUMN / MODIFY COLUMN, so emit the same ALTER
	// against the un-suffixed name when one of those clauses
	// is present. Other ALTERs (ADD INDEX, MODIFY TTL,
	// MATERIALIZE) Distributed legitimately doesn't carry —
	// skip the wrapper for those.
	if hv && kind == "altertable" && reAlterColList.MatchString(sql) {
		bareAlter := reAlterTable.ReplaceAllString(sql, "${1}"+name+on+" ")
		out = append(out, bareAlter)
	}
	return out
}

// execDDL adapts a DDL statement for cluster mode and runs every
// resulting fragment in order. In single-node mode this is a thin
// wrapper around conn.Exec. Errors include the (truncated) SQL of
// the failing fragment so a regex-induced bug isn't a silent
// production-only failure.
func (s *Store) execDDL(ctx context.Context, sql string) error {
	for _, frag := range s.adaptDDL(sql) {
		if err := s.conn.Exec(ctx, frag); err != nil {
			return fmt.Errorf("ddl exec: %w\nSQL: %.200s", err, frag)
		}
	}
	return nil
}

// isClusterUnsupportedAlter recognises the ClickHouse error
// signature for an ALTER that the Distributed engine refuses to
// proxy. Callers softening this into a warning need a precise
// match so we don't accidentally swallow unrelated DDL failures
// (column type mismatch, permission denial, etc.).
//
// CH reports it as code 48 "NOT_IMPLEMENTED" with the literal
// "is not supported by storage Distributed" tail — that string
// is stable across at least 22.x → 24.x server versions.
func isClusterUnsupportedAlter(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	return strings.Contains(msg, "is not supported by storage Distributed")
}

// identifyDDLTarget pulls the target table/view name out of a DDL
// string. Returns (name, "table"|"altertable"|"mv") on a match,
// ("", "") otherwise so the caller can pass through untouched.
func identifyDDLTarget(sql string) (string, string) {
	if m := reCreateTable.FindStringSubmatch(sql); m != nil {
		return m[2], "table"
	}
	if m := reAlterTable.FindStringSubmatch(sql); m != nil {
		return m[2], "altertable"
	}
	if m := reCreateMV.FindStringSubmatch(sql); m != nil {
		return m[2], "mv"
	}
	return "", ""
}

var (
	reCreateTable = regexp.MustCompile(`(?is)^(\s*CREATE\s+TABLE\s+IF\s+NOT\s+EXISTS\s+)([A-Za-z_][A-Za-z0-9_]*)\b`)
	reAlterTable  = regexp.MustCompile(`(?is)^(\s*ALTER\s+TABLE\s+)([A-Za-z_][A-Za-z0-9_]*)\b`)
	reCreateMV    = regexp.MustCompile(`(?is)^(\s*CREATE\s+MATERIALIZED\s+VIEW\s+IF\s+NOT\s+EXISTS\s+)([A-Za-z_][A-Za-z0-9_]*)\b`)
	// Engine clause: ENGINE = <BaseEngine>(args?)  — args optional.
	// Captures the base engine name and the argument list including
	// surrounding parens (or empty when the engine is parenless).
	reEngine = regexp.MustCompile(`(?i)ENGINE\s*=\s*(MergeTree|ReplacingMergeTree|AggregatingMergeTree|SummingMergeTree)\s*(\([^)]*\))?`)

	// reAlterColList detects whether an ALTER touches the column
	// list (ADD/DROP/MODIFY/RENAME COLUMN). The Distributed engine
	// supports these — index / TTL / MATERIALIZE alterations are
	// out of scope and stay scoped to the _local table.
	reAlterColList = regexp.MustCompile(`(?i)\b(?:ADD|DROP|MODIFY|RENAME)\s+COLUMN\b`)
)
