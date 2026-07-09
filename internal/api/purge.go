package api

import (
	"context"
	"fmt"
	"net/http"
	"time"
)

// purgeTelemetry is the "factory reset" admin action: it TRUNCATEs every
// observability-DATA table in ClickHouse (spans / logs / metrics + all
// aggregation, topology, trace and derived-analysis tables) while preserving
// ALL configuration (system_settings/LDAP, alert rules, saved views, users,
// dashboards, monitors, status page, notification channels — and audit_log
// itself). Admin-only (gated on the route); audited. Best-effort: the result
// reports per-table successes / skips / errors so the UI can show exactly what
// happened. POST /api/admin/purge-telemetry.
func (s *Server) purgeTelemetry(w http.ResponseWriter, r *http.Request) {
	// v0.8.413 — operator-reported: the purge timed out at the client's
	// 60s fetch abort, and because it ran on r.Context() the abort
	// CANCELED the purge MID-WAY (partial truncate; a big test env
	// needs well over a minute of ON CLUSTER TRUNCATEs). Detach: the
	// purge is a deliberate, audited factory reset — once started it
	// runs to completion even if the tab closes. 10-minute umbrella so
	// a wedged keeper can't pin the goroutine forever.
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	res, err := s.store.PurgeTelemetry(ctx)
	// Audit unconditionally — a partial purge is still a state mutation the
	// operator must see in the (preserved) audit trail.
	s.audit(r, "telemetry.purge", "clickhouse", s.store.DatabaseName(),
		fmt.Sprintf("purged=%d skipped=%d errors=%d", len(res.TablesPurged), len(res.Skipped), len(res.Errors)))
	// Deliberately NOT calling cacheInvalidatePrefix("") here: the cache keys are
	// not namespaced and share the Redis keyspace with the leader locks (and
	// possibly a co-tenant app), so a MATCH * UNLINK would release every leader
	// lock cluster-wide and could wipe unrelated keys. The hot reads carry short
	// TTLs (≤30s), so the UI converges to the emptied tables within one cache
	// window — an acceptable wait for a rare, explicit factory reset.
	_ = err // err is reflected in res.Errors; return the full result either way.
	writeJSON(w, res)
}
