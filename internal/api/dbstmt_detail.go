package api

// dbstmt_detail.go — v0.8.378 (Stage-2 slice D2, docs/pages-enhancement-
// audit.md §2 Faz D): the /slow-queries statement drill-down. ONE read-only
// endpoint over the chstore readers in internal/chstore/dbstmt_detail.go:
//
//	GET /api/databases/statements/detail
//	    ?hash=<decimal uint64>&system=&db=&from=&to=&compare=prior
//
// One payload for the drawer: window summary (header strip), 5m-grain
// trend series, per-service caller breakdown, true exemplar trace pivots
// (slowest + worst-error trace off spans.db_stmt_hash). Sections are
// null-tolerant — a failed section renders as null, the rest survive; the
// drawer never blanks on a partial backend hiccup (the endpoints_detail
// posture, v0.8.360).
//
// hash is the D1 (v0.8.375) statement identity, riding the wire as a
// DECIMAL STRING because a uint64 in JSON silently loses precision past
// 2^53 — parsed server-side with strconv.ParseUint, 400 on garbage AND on
// 0 (the "no statement" sentinel never identifies a class).
//
// compare=prior re-runs the summary + caller reads against the
// immediately-preceding equal-length window and merges Prior* fields onto
// the current values (the Endpoints v0.5.404 / messaging M1 v0.8.364
// pattern) — opt-in because it doubles the MV cost.
//
// Bare route (viewer-visible — a read-only drill-down, same posture as the
// pivot/endpoints-detail endpoints), serveCached 30s with a hash-ALL-inputs
// key: (system, db) fold into one FNV digest with a NUL separator so field
// boundaries can't be forged (the v0.5.187 ambiguity class applied to
// strings), the window is minute-bucketed (pivotMinuteBucket) so concurrent
// drawer opens within the same minute share one upstream trip.

import (
	"context"
	"fmt"
	"hash/fnv"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/cilcenk/coremetry/internal/chstore"
)

// registerDBStmtDetailRoutes mounts the D2 drill-down endpoint. Called
// ONCE from api.go's Start block (its single new line for v0.8.378).
func (s *Server) registerDBStmtDetailRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/databases/statements/detail", s.getDBStmtDetail)
}

// dbStmtDetailKey builds the cache key from ALL inputs: the statement
// hash, the (system, db) filter digest (NUL-separated FNV-64a — two
// different tuples can never alias), the compare flag, and the
// minute-bucketed window. Pinned by dbstmt_detail_key_test.go.
func dbStmtDetailKey(hash uint64, system, db string, compare bool, from, to time.Time) string {
	h := fnv.New64a()
	h.Write([]byte(system))
	h.Write([]byte{0})
	h.Write([]byte(db))
	return fmt.Sprintf("dbstmt-detail:h=%d:sd=%x:cmp=%v:from=%d:to=%d",
		hash, h.Sum64(), compare, pivotMinuteBucket(from), pivotMinuteBucket(to))
}

// dbStmtExemplars carries the two representative trace_ids for the
// statement window — the TRUE pivot (spans.db_stmt_hash = ?) replacing
// the lossy `db.statement LIKE prefix%` deep-link. Empty fields mean "no
// exemplar" (all-healthy window for the error one).
type dbStmtExemplars struct {
	SlowTraceID  string `json:"slowTraceId,omitempty"`
	ErrorTraceID string `json:"errorTraceId,omitempty"`
}

// dbStmtDetailPayload is the one-response drawer contract. Every section
// is nil when its read failed — per-section tolerance, never a 500 for a
// partial miss. Statement is the '?'-normalized display form re-derived
// from the summary's bucket sample (hash-consistent with the class by the
// dbstmt.go parity contract); empty when the summary section missed —
// the frontend then falls back to the catalog row's statement.
type dbStmtDetailPayload struct {
	StmtHash  string                     `json:"stmtHash"`
	Statement string                     `json:"statement,omitempty"`
	FromNs    int64                      `json:"fromNs"`
	ToNs      int64                      `json:"toNs"`
	Summary   *chstore.DBStmtSummary     `json:"summary"`
	Trend     []chstore.DBStmtTrendPoint `json:"trend"`
	// TrendBucketSec — the trend series' bucket width (5m-grain multiple,
	// coarsened on wide windows). Lets the frontend densify the sparse
	// series against the window without re-deriving the coarsening rule.
	TrendBucketSec int64                  `json:"trendBucketSec,omitempty"`
	Callers        []chstore.DBStmtCaller `json:"callers"`
	Exemplars      *dbStmtExemplars       `json:"exemplars"`
}

// getDBStmtDetail serves GET /api/databases/statements/detail. Window
// defaults to the last hour (the catalog page's default range).
func (s *Server) getDBStmtDetail(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	hashStr := strings.TrimSpace(q.Get("hash"))
	hash, err := strconv.ParseUint(hashStr, 10, 64)
	if err != nil || hash == 0 {
		http.Error(w, "hash must be a non-zero decimal uint64 (SlowQueryRow.stmtHash)", http.StatusBadRequest)
		return
	}
	system := strings.TrimSpace(q.Get("system"))
	dbName := strings.TrimSpace(q.Get("db"))
	compare := q.Get("compare") == "prior"
	from, to := parseFromTo(r, time.Hour)

	key := dbStmtDetailKey(hash, system, dbName, compare, from, to)
	s.serveCached(w, r, key, 30*time.Second, func(ctx context.Context) (any, error) {
		dq := chstore.DBStmtDetailQuery{
			Hash: hash, DBSystem: system, DBName: dbName,
			From: from, To: to,
		}
		out := dbStmtDetailPayload{
			StmtHash: hashStr,
			FromNs:   from.UnixNano(), ToNs: to.UnixNano(),
		}
		// Sections run sequentially (each is an MV point-read for one
		// stmt_hash; the payload is cached 30s) with per-section error
		// tolerance. The ctx guard stops the chain when the client is
		// gone / the request deadline hit, so the reads can't outlive
		// one budget (the endpoints_detail shape).
		if sum, err := s.store.GetDBStmtSummary(ctx, dq); err == nil && sum != nil {
			out.Summary = sum
			out.Statement = chstore.NormalizeDBStatement(sum.SampleStatement)
		}
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		if tr, bucketSec, err := s.store.GetDBStmtTrend(ctx, dq); err == nil {
			out.Trend = tr
			out.TrendBucketSec = bucketSec
		}
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		if cs, err := s.store.GetDBStmtCallers(ctx, dq, 20); err == nil {
			out.Callers = cs
		}
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		if slow, errTid, err := s.store.DBStmtExemplars(ctx, dq); err == nil && (slow != "" || errTid != "") {
			out.Exemplars = &dbStmtExemplars{SlowTraceID: slow, ErrorTraceID: errTid}
		}
		if !compare || out.Summary == nil {
			return out, nil
		}
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		// Prior window: same length, shifted back by exactly the window
		// width (apples-to-apples). Prior failures are non-fatal — the
		// drawer renders current values without deltas rather than 500'ing.
		dur := to.Sub(from)
		pq := dq
		pq.From = from.Add(-dur)
		pq.To = from
		if psum, err := s.store.GetDBStmtSummary(ctx, pq); err == nil && psum != nil {
			out.Summary.PriorCalls = psum.Calls
			out.Summary.PriorErrors = psum.Errors
			out.Summary.PriorAvgMs = psum.AvgMs
			out.Summary.PriorP95Ms = psum.P95Ms
		}
		if len(out.Callers) > 0 {
			if pcs, err := s.store.GetDBStmtCallers(ctx, pq, 20); err == nil {
				idx := make(map[string]*chstore.DBStmtCaller, len(pcs))
				for i := range pcs {
					idx[pcs[i].Service] = &pcs[i]
				}
				for i := range out.Callers {
					if p, ok := idx[out.Callers[i].Service]; ok {
						out.Callers[i].PriorCalls = p.Calls
						out.Callers[i].PriorErrors = p.Errors
						out.Callers[i].PriorAvgMs = p.AvgMs
						out.Callers[i].PriorP95Ms = p.P95Ms
					}
				}
			}
		}
		return out, nil
	})
}
