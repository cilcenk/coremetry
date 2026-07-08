package chstore

import (
	"context"
	"errors"
	"sort"
	"strings"
	"time"
)

// v0.8.387 — env-separation Phase 3: /problems consumes the global
// `?env=` picker.
//
// Problems rows live in the `problems` state table keyed by
// (rule, service) — they carry NO env dimension, and a problem the
// evaluator computed over all-env metrics cannot be attributed to one
// env post-hoc. So the env filter on /problems honestly means:
// "show problems whose SERVICE ran in the selected env" — resolved
// through the service→env map below, the exact env twin of
// GetServiceClusterMap / clusterMemberServices (v0.8.386).

// GetServiceEnvMap returns one entry per service with the distinct
// deployment environments (spans.deploy_env) it emitted from during
// the last `since` window. Backs the /problems + /inbox env filter
// (service-scoped semantics, see EnvMemberServices).
//
// Single batched query — N+1-free regardless of problem count.
// deploy_env is a typed LowCardinality column, so unlike the cluster
// map's res/attr derive this GROUP BY is a cheap dict pass even at
// billion-span scale. Capped at 50000 rows (1000 services × 50 envs
// class of bound — far above any realistic install).
//
// Cached 60s per `since` (the v0.8.359 P2-C discipline, mirrored from
// GetServiceClusterMap): env membership is deploy-stable, so a minute
// of staleness is invisible, and the /problems + sidebar 30s polls
// never pay more than one map refresh per minute. The cached map is
// returned SHARED: callers must treat it as read-only.
func (s *Store) GetServiceEnvMap(ctx context.Context, since time.Duration) (map[string][]string, error) {
	if since == 0 {
		since = 1 * time.Hour
	}
	s.envMapMu.RLock()
	if s.envMapVal != nil && s.envMapFor == since &&
		time.Since(s.envMapAt) < envMapCacheTTL {
		v := s.envMapVal
		s.envMapMu.RUnlock()
		return v, nil
	}
	s.envMapMu.RUnlock()
	from := time.Now().Add(-since)
	rows, err := s.conn.Query(ctx, `
		SELECT service_name, deploy_env
		FROM spans
		WHERE time >= ? AND service_name != '' AND deploy_env != ''
		GROUP BY service_name, deploy_env
		ORDER BY service_name, deploy_env
		LIMIT 50000
		SETTINGS max_execution_time = 8`, from)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string][]string{}
	for rows.Next() {
		var svc, env string
		if err := rows.Scan(&svc, &env); err != nil {
			continue
		}
		out[svc] = append(out[svc], env)
	}
	if err := rows.Err(); err != nil {
		return out, err
	}
	// Replace, never mutate — a reader holding the old snapshot stays
	// consistent (same discipline as the clusterMap / alertRules caches).
	s.envMapMu.Lock()
	s.envMapAt = time.Now()
	s.envMapFor = since
	s.envMapVal = out
	s.envMapMu.Unlock()
	return out, nil
}

// errEnvMapUnavailable — EnvMemberServices' cold conn-less path.
// Callers soft-fail to UNFILTERED on any error (never blank the
// triage page on a map blip); pinned in env_members_test.go.
var errEnvMapUnavailable = errors.New("service→env map unavailable")

// EnvMemberServices resolves an environment name to the sorted set
// of services that ran in it, from the 60s-cached 1h-clamped
// service→env map (v0.8.387 — the env twin of clusterMemberServices,
// v0.8.386). Exported because the /inbox handler applies the same
// service-scoped env semantics to its merged item list.
//
// Return contract (load-bearing, unlike clusterMemberServices' nil):
//   - (members, nil)  — authoritative; an EMPTY slice means the env
//     genuinely has no services in the last hour, and callers filter
//     to zero service-scoped rows (honest empty, not "show all").
//   - (nil, err)      — the map could not be resolved (cold conn-less
//     store, CH blip); callers MUST soft-fail to unfiltered so a
//     transient error never hides a firing P1.
func (s *Store) EnvMemberServices(ctx context.Context, env string) ([]string, error) {
	// Conn-less Stores (pure SQL-shape tests) may still carry a
	// SEEDED map cache; only a real cache miss needs the conn.
	s.envMapMu.RLock()
	fresh := s.envMapVal != nil && s.envMapFor == time.Hour &&
		time.Since(s.envMapAt) < envMapCacheTTL
	s.envMapMu.RUnlock()
	if !fresh && s.conn == nil {
		return nil, errEnvMapUnavailable
	}
	m, err := s.GetServiceEnvMap(ctx, time.Hour)
	if err != nil {
		return nil, err
	}
	out := []string{} // non-nil: empty is an authoritative "no members"
	for svc, envs := range m {
		for _, e := range envs {
			if e == env {
				out = append(out, svc)
				break
			}
		}
	}
	sort.Strings(out)
	return out, nil
}

// applyEnvServiceScope adds the service-scoped env conjunct for the
// problems state table to wc. Pure — table-tested (v0.8.387).
//
// Semantics pinned here:
//   - service = '' rows ALWAYS survive: log-query rules open problems
//     with an empty service (evaluator.go log_query path) — global
//     monitors are env-unattributable, and hiding a firing global
//     alert because the operator narrowed to uat is a triage hazard.
//   - a service in the member set survives — a multi-env service is a
//     member of every env it runs in, so its problems show under each
//     (the audit's same-name-multi-env case).
//   - a service ABSENT from the map (env-less infra, e.g. the demo's
//     cross-env Oracle RAC) is hidden — consistent with what /services
//     and /traces show under the same env pick (strict deploy_env
//     filter hides env-less rows there too).
//   - empty member set → only the global (service='') rows remain:
//     the env honestly has no services, so zero service-scoped
//     problems is the correct answer, not "show all".
func applyEnvServiceScope(wc *whereClause, members []string) {
	if len(members) == 0 {
		wc.add("service = ''")
		return
	}
	holders := make([]string, len(members))
	args := make([]any, len(members))
	for i, m := range members {
		holders[i] = "?"
		args[i] = m
	}
	wc.add("(service = '' OR service IN ("+strings.Join(holders, ",")+"))", args...)
}

// envScopeProblems resolves ProblemFilter.Env and applies the
// service-scoped conjunct. Shared by ListProblems AND CountProblems
// so the /problems list, the sidebar badge, and the buckets endpoint
// agree by construction. Soft-fails to unfiltered on a map error —
// list and count then agree on the UNfiltered numbers too.
func (s *Store) envScopeProblems(ctx context.Context, wc *whereClause, env string) {
	if env == "" {
		return
	}
	members, err := s.EnvMemberServices(ctx, env)
	if err != nil {
		return
	}
	applyEnvServiceScope(wc, members)
}
