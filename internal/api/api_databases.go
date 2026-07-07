package api

// Databases + messaging read handlers. Split out of api.go for
// code organisation (behaviour-preserving). All handlers hit the
// pre-aggregated db_*_5m / messaging MVs via chstore and front the
// read with s.serveCached. Shared helpers (parseFromTo, cacheBucket)
// stay in api.go because many other clusters use them too.

import (
	"context"
	"fmt"
	"net/http"
	"time"

	"github.com/cilcenk/coremetry/internal/chstore"
)

func (s *Server) getDatabases(w http.ResponseWriter, r *http.Request) {
	from, to := parseFromTo(r, time.Hour)
	key := "databases:" + cacheBucket(from, to)
	s.serveCached(w, r, key, 30*time.Second, func(ctx context.Context) (any, error) {
		return s.store.GetDatabases(ctx, from, to)
	})
}

// getDBTrends serves the per-row RED sparklines (#1) + latest-bucket
// health snapshot (#6) for the /databases + /messaging overview
// grid. One DBTrend per (db_system, instance, db_name) — keyed
// identically to the /api/databases rows so the frontend joins
// trends → rows by (system, instance, dbName). Read-only; no auth
// gate / audit. Cache key hashes the (minute-bucketed) window via
// the shared cacheBucket helper; 30s TTL matches the overview so a
// page load and its sparkline fetch share the same warm window.
func (s *Server) getDBTrends(w http.ResponseWriter, r *http.Request) {
	from, to := parseFromTo(r, time.Hour)
	key := "db-trends:" + cacheBucket(from, to)
	s.serveCached(w, r, key, 30*time.Second, func(ctx context.Context) (any, error) {
		return s.store.GetDBTrends(ctx, from, to)
	})
}

// getDatabaseDetail returns the drawer payload for one
// (db_system, instance) pair — per-(service, pod) caller
// breakdown plus the top db_statement prefixes. Cached 30s.
// Distinct cache keys per (system, instance, window) so the
// row click is sub-100ms warm cache.
func (s *Server) getDatabaseDetail(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	system := q.Get("system")
	instance := q.Get("instance")
	if system == "" {
		http.Error(w, `{"error":"system required"}`, http.StatusBadRequest)
		return
	}
	from, to := parseFromTo(r, time.Hour)
	key := fmt.Sprintf("db-detail:%s:%s:%s", system, instance, cacheBucket(from, to))
	s.serveCached(w, r, key, 30*time.Second, func(ctx context.Context) (any, error) {
		return s.store.GetDatabaseDetail(ctx, system, instance, from, to)
	})
}

// getOracleMetrics returns the OracleDB-receiver drill-down for
// one instance — sessions/processes utilisation, cumulative
// counter rates, tablespace usage. Falls back to deterministic
// synthetic data (flagged Synthetic=true in the payload) when
// no oracledb.* metric_points exist in the window so the panel
// still renders during integration setup.
func (s *Server) getOracleMetrics(w http.ResponseWriter, r *http.Request) {
	instance := r.URL.Query().Get("instance")
	from, to := parseFromTo(r, time.Hour)
	key := fmt.Sprintf("oracle:%s:%s", instance, cacheBucket(from, to))
	s.serveCached(w, r, key, 30*time.Second, func(ctx context.Context) (any, error) {
		return s.store.GetOracleMetrics(ctx, instance, from, to)
	})
}

// getPostgresMetrics serves the Postgres receiver drill-down
// for the row-click drawer on /databases. Mirrors getOracleMetrics:
// 30s cache TTL bucketed to a 30s grid so morning-triage hits
// share one query trip even with rolling time windows.
func (s *Server) getPostgresMetrics(w http.ResponseWriter, r *http.Request) {
	instance := r.URL.Query().Get("instance")
	from, to := parseFromTo(r, time.Hour)
	key := fmt.Sprintf("postgres:%s:%s", instance, cacheBucket(from, to))
	s.serveCached(w, r, key, 30*time.Second, func(ctx context.Context) (any, error) {
		return s.store.GetPostgresMetrics(ctx, instance, from, to)
	})
}

// getMySQLMetrics — MySQL receiver drill-down (buffer pool /
// threads / row-lock / slow queries / handlers / replica lag).
func (s *Server) getMySQLMetrics(w http.ResponseWriter, r *http.Request) {
	instance := r.URL.Query().Get("instance")
	from, to := parseFromTo(r, time.Hour)
	key := fmt.Sprintf("mysql:%s:%s", instance, cacheBucket(from, to))
	s.serveCached(w, r, key, 30*time.Second, func(ctx context.Context) (any, error) {
		return s.store.GetMySQLMetrics(ctx, instance, from, to)
	})
}

// getRedisMetrics — Redis receiver drill-down (clients / memory /
// commands / hit rate / per-keyspace / replication / role).
func (s *Server) getRedisMetrics(w http.ResponseWriter, r *http.Request) {
	instance := r.URL.Query().Get("instance")
	from, to := parseFromTo(r, time.Hour)
	key := fmt.Sprintf("redis:%s:%s", instance, cacheBucket(from, to))
	s.serveCached(w, r, key, 30*time.Second, func(ctx context.Context) (any, error) {
		return s.store.GetRedisMetrics(ctx, instance, from, to)
	})
}

// getMessaging is the parallel handler for queues / topics
// (Kafka / RabbitMQ / IBM MQ / etc.). Same caching semantics.
//
// v0.8.364 (Stage-2 M1) — optional ?compare=prior (the endpoints
// v0.5.404 pattern): a second scan of the SAME MVs over the
// immediately-preceding equal-length window, merged onto the
// current rows by (system, cluster, destination) identity. Opt-in
// because it doubles the CH cost.
func (s *Server) getMessaging(w http.ResponseWriter, r *http.Request) {
	from, to := parseFromTo(r, time.Hour)
	compare := r.URL.Query().Get("compare") == "prior"
	// Cache key hashes all inputs (window + compare). The default
	// read keeps the pre-M1 key byte-identical so the background
	// warm loop in api.go (warm("messaging", …)) still primes the
	// slot the page load hits; compare rides its own slot.
	key := "messaging:" + cacheBucket(from, to)
	if compare {
		key = "messaging:cmp:" + cacheBucket(from, to)
	}
	s.serveCached(w, r, key, 30*time.Second, func(ctx context.Context) (any, error) {
		rows, err := s.store.GetMessaging(ctx, from, to)
		if err != nil {
			return nil, err
		}
		if !compare || len(rows) == 0 {
			return rows, nil
		}
		// Prior window: same length, shifted back by exactly the
		// window width so the comparison stays apples-to-apples.
		// Rollup variant skips the top-callers pass — the delta
		// merge only reads counts + quantiles. Prior failure is
		// non-fatal: return current rows without trends rather
		// than 500'ing the page.
		dur := to.Sub(from)
		priorRows, err := s.store.GetMessagingRollup(ctx, from.Add(-dur), from)
		if err != nil {
			return rows, nil
		}
		mergeMessagingPrior(rows, priorRows)
		return rows, nil
	})
}

// mergeMessagingPrior copies the prior-window counters onto the
// current rows by (system, cluster, destination) identity — the
// same key GetMessaging groups by, so a destination that moved
// rank between windows still matches. Rows absent from the prior
// window keep zero Prior* fields (omitempty → absent in JSON →
// the frontend renders no delta badge). Pure — table-driven
// tested in messaging_prior_test.go (v0.8.364).
func mergeMessagingPrior(cur, prior []chstore.MessagingInstance) {
	type key struct{ system, cluster, dest string }
	idx := make(map[key]*chstore.MessagingInstance, len(prior))
	for i := range prior {
		idx[key{prior[i].System, prior[i].Cluster, prior[i].Destination}] = &prior[i]
	}
	for i := range cur {
		p, ok := idx[key{cur[i].System, cur[i].Cluster, cur[i].Destination}]
		if !ok {
			continue
		}
		cur[i].PriorSpanCount = p.SpanCount
		cur[i].PriorErrorCount = p.ErrorCount
		cur[i].PriorProduceCount = p.ProduceCount
		cur[i].PriorConsumeCount = p.ConsumeCount
		cur[i].PriorAvgMs = p.AvgMs
		cur[i].PriorP50Ms = p.P50Ms
		cur[i].PriorP99Ms = p.P99Ms
	}
}

// getMessagingDetail is the parallel handler for queues /
// topics. Takes ?system=&cluster=&destination=&from=&to=. The
// cluster query param defaults to "(default)" for single-
// cluster deployments where the SPA hasn't been updated yet —
// matches the clusterExpr fallback in the store.
func (s *Server) getMessagingDetail(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	system := q.Get("system")
	dest := q.Get("destination")
	cluster := q.Get("cluster")
	if cluster == "" {
		cluster = "(default)"
	}
	if system == "" {
		http.Error(w, `{"error":"system required"}`, http.StatusBadRequest)
		return
	}
	from, to := parseFromTo(r, time.Hour)
	key := fmt.Sprintf("msg-detail:%s:%s:%s:%s", system, cluster, dest, cacheBucket(from, to))
	s.serveCached(w, r, key, 30*time.Second, func(ctx context.Context) (any, error) {
		return s.store.GetMessagingDetail(ctx, system, cluster, dest, from, to)
	})
}
