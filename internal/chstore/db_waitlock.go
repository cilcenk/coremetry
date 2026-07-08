package chstore

// db_waitlock.go — cross-engine waits & locks strip for the
// /databases detail drawer (v0.8.391, Stage-2 D3,
// docs/pages-enhancement-audit.md §2 Faz D3).
//
// The audit found wait/lock visibility engine-lopsided: Oracle FULL
// (wait classes + row-lock waits), MySQL PARTIAL (row-lock waits/
// time), Postgres NONE on the drawer's common surface. This file
// normalizes whatever each OTel DB receiver ACTUALLY emits into one
// small model the drawer renders identically for every engine:
//
//   oracle      → oracledb.wait_time.<class> (10 wait classes,
//                 cumulative s), oracledb.row_lock_waits (+ Prom /
//                 enq.tx spellings), oracledb.enqueue_deadlocks +
//                 oracledb.exchange_deadlocks
//   postgresql  → postgresql.deadlocks, postgresql.database.locks /
//                 postgresql.locks dimensioned by lock_type (NO
//                 wait-class family exists in the receiver)
//   mysql       → mysql.row_locks / mysql.innodb.row_lock.waits,
//                 mysql.innodb.row_lock.time (ms) /
//                 mysql.row_locks_time, mysql.locks dimensioned by
//                 kind (table locks immediate/waited)
//
// HONESTY CONTRACT: a family the receiver never emits (or emitted
// no points in the window) stays ABSENT (nil pointer / empty slice)
// — never a fake zero. The frontend renders "no lock telemetry from
// this receiver" per engine off that absence. Present-but-flat
// counters (max==min) DO report an honest 0.

import (
	"context"
	"sort"
	"strings"
	"time"
)

// DBWaitLock is the payload of GET /api/databases/waitlock — the
// common wait/lock model for one (system, instance).
type DBWaitLock struct {
	// System is the NORMALIZED engine key: "oracle" | "postgresql" |
	// "mysql" (mariadb folds into mysql, postgres into postgresql).
	System        string  `json:"system"`
	Instance      string  `json:"instance"`
	WindowSeconds float64 `json:"windowSeconds"`
	// Supported=false → the engine has no receiver wait/lock story
	// at all (redis, sqlserver, unknown). The strip renders nothing.
	Supported bool `json:"supported"`
	// WaitClasses — per-class wait pressure in waiting-seconds per
	// elapsed second (1.0 = one client fully blocked), descending.
	// Only Oracle's receiver emits this family; empty otherwise.
	WaitClasses []DBWaitClass   `json:"waitClasses"`
	Locks       DBWaitLockLocks `json:"locks"`
}

// DBWaitClass is one wait-class row. Same derivation as the Oracle
// panel: (max-min of the cumulative counter over window) / windowSec.
type DBWaitClass struct {
	Name   string  `json:"name"`
	PerSec float64 `json:"perSec"`
}

// DBWaitLockLocks — nil pointer = family not seen in the window
// (either the receiver doesn't emit it or it isn't wired), which the
// frontend renders as an honest per-engine empty, never a zero.
type DBWaitLockLocks struct {
	// WaitsPerSec — row-lock wait events per second (oracle, mysql).
	WaitsPerSec *float64 `json:"waitsPerSec,omitempty"`
	// TimeSec — total seconds spent in row-lock waits over the
	// window (mysql; receiver counts milliseconds).
	TimeSec *float64 `json:"timeSec,omitempty"`
	// DeadlocksPerSec — deadlocks per second (oracle sums enqueue +
	// exchange counters; postgresql has a single counter).
	DeadlocksPerSec *float64 `json:"deadlocksPerSec,omitempty"`
	// ByMode — dimensioned lock breakdown: PG current lock counts
	// per lock_type (gauge), MySQL table-lock rates per kind (/s).
	ByMode []DBLockMode `json:"byMode,omitempty"`
}

// DBLockMode is one dimensioned lock entry. Unit disambiguates the
// gauge-vs-rate semantics per engine ("count" | "/s").
type DBLockMode struct {
	Mode  string  `json:"mode"`
	Value float64 `json:"value"`
	Unit  string  `json:"unit"`
}

// dbWaitLockSpec declares, per engine, exactly which metric families
// feed each section of the common model. Pure data — pinned by the
// table test in db_waitlock_test.go so a receiver-convention rename
// breaks loudly.
type dbWaitLockSpec struct {
	Engine            string   // normalized key
	WaitClassPrefixes []string // cumulative seconds; class name = metric suffix
	LockWaitCounters  []string // cumulative wait-event counts → /s (first present wins)
	LockTimeCounters  []string // cumulative lock time → window total (first present wins)
	LockTimeDivisor   float64  // receiver unit → seconds (mysql: ms → 1000)
	DeadlockCounters  []string // cumulative deadlocks → /s (present ones SUM)
	LockModeMetrics   []string // dimensioned lock metrics
	LockModeAttr      string   // the dimension attr key
	LockModeIsRate    bool     // true → (max-min)/window per mode; false → argMax gauge
	instanceClause    func(withInstance bool) string
}

// dbWaitLockSpecFor maps a /databases row's db_system to its engine
// spec. ok=false → no receiver wait/lock family exists for that
// engine (redis has no lock concept; unknown engines unsupported).
func dbWaitLockSpecFor(system string) (dbWaitLockSpec, bool) {
	switch strings.ToLower(strings.TrimSpace(system)) {
	case "oracle", "oracledb":
		return dbWaitLockSpec{
			Engine:            "oracle",
			WaitClassPrefixes: []string{"oracledb.wait_time.", "oracledb_wait_time_"},
			LockWaitCounters:  []string{"oracledb.row_lock_waits", "oracledb_row_lock_waits", "oracledb.enq.tx.row_lock_contention"},
			DeadlockCounters:  []string{"oracledb.enqueue_deadlocks", "oracledb.exchange_deadlocks"},
			instanceClause:    oracleInstanceClause,
		}, true
	case "postgresql", "postgres":
		return dbWaitLockSpec{
			Engine:           "postgresql",
			DeadlockCounters: []string{"postgresql.deadlocks"},
			LockModeMetrics:  []string{"postgresql.database.locks", "postgresql.locks"},
			LockModeAttr:     "lock_type",
			instanceClause:   pgInstanceClause,
		}, true
	case "mysql", "mariadb":
		return dbWaitLockSpec{
			Engine:           "mysql",
			LockWaitCounters: []string{"mysql.row_locks", "mysql.innodb.row_lock.waits"},
			LockTimeCounters: []string{"mysql.innodb.row_lock.time", "mysql.row_locks_time"},
			LockTimeDivisor:  1000, // receiver counts milliseconds
			LockModeMetrics:  []string{"mysql.locks"},
			LockModeAttr:     "kind",
			LockModeIsRate:   true,
			instanceClause:   mysqlInstanceClause,
		}, true
	}
	return dbWaitLockSpec{}, false
}

// counterMetrics is the flat exact-name list for the single-trip
// counter query (wait-class families match by prefix instead).
func (sp dbWaitLockSpec) counterMetrics() []string {
	out := make([]string, 0, len(sp.LockWaitCounters)+len(sp.LockTimeCounters)+len(sp.DeadlockCounters))
	out = append(out, sp.LockWaitCounters...)
	out = append(out, sp.LockTimeCounters...)
	out = append(out, sp.DeadlockCounters...)
	return out
}

// dbWaitLockCounterSQL builds the one bounded counter query per
// drawer open: every cumulative family in a single metric_points
// trip, (max-min)/window per metric. Pure so the bounds (time WHERE
// + LIMIT + max_execution_time ≤ 10) are pinned by table test.
// Prefixes come from our own spec constants, never user input.
func dbWaitLockCounterSQL(sp dbWaitLockSpec, withInstance bool) string {
	conds := make([]string, 0, 1+len(sp.WaitClassPrefixes))
	if n := len(sp.counterMetrics()); n > 0 {
		conds = append(conds, "metric IN ("+chPlaceholders(n)+")")
	}
	for _, p := range sp.WaitClassPrefixes {
		conds = append(conds, "startsWith(metric, '"+p+"')")
	}
	return `
		SELECT metric, (max(value) - min(value)) / ? AS rate
		FROM metric_points
		WHERE time >= ? AND time <= ?
		  AND (` + strings.Join(conds, " OR ") + `)
		` + sp.instanceClause(withInstance) + `
		GROUP BY metric
		LIMIT 64
		SETTINGS max_execution_time = 10`
}

// dbWaitLockModeSQL builds the dimensioned lock-mode query (PG lock
// counts by lock_type; MySQL table-lock rates by kind). The attr key
// is a spec constant. Pure for the same bounds-pinning reason.
func dbWaitLockModeSQL(sp dbWaitLockSpec, withInstance bool) string {
	agg := "argMax(value, time)"
	if sp.LockModeIsRate {
		agg = "(max(value) - min(value)) / ?"
	}
	return `
		SELECT attr_values[indexOf(attr_keys, '` + sp.LockModeAttr + `')] AS mode,
		       ` + agg + ` AS v
		FROM metric_points
		WHERE time >= ? AND time <= ?
		  AND metric IN (` + chPlaceholders(len(sp.LockModeMetrics)) + `)
		  AND has(attr_keys, '` + sp.LockModeAttr + `')
		` + sp.instanceClause(withInstance) + `
		GROUP BY mode
		HAVING mode != ''
		ORDER BY v DESC
		LIMIT 20
		SETTINGS max_execution_time = 10`
}

// chPlaceholders renders n comma-separated bind markers for an IN
// list. n is always a small spec-derived constant here.
func chPlaceholders(n int) string {
	return strings.TrimRight(strings.Repeat("?,", n), ",")
}

// assembleDBWaitLock folds the raw per-metric rates + mode rows into
// the common model. rates carries ONLY metrics that had points in
// the window (absent key = family absent → nil pointer, the honesty
// contract). Pure — table-tested in db_waitlock_test.go (v0.8.391):
// oracle deadlock summing, mysql ms→s conversion, first-present
// precedence, present-but-flat honest zeros, negative-rate clamps.
func assembleDBWaitLock(sp dbWaitLockSpec, rates map[string]float64, modes []DBLockMode, windowSec float64, out *DBWaitLock) {
	clamp := func(v float64) float64 {
		if v < 0 {
			return 0 // counter reset mid-window → safer underestimate
		}
		return v
	}

	// Wait classes: prefix-matched metrics, class name = suffix.
	for metric, rate := range rates {
		for _, p := range sp.WaitClassPrefixes {
			if strings.HasPrefix(metric, p) {
				out.WaitClasses = append(out.WaitClasses, DBWaitClass{
					Name:   strings.TrimPrefix(metric, p),
					PerSec: clamp(rate),
				})
				break
			}
		}
	}
	sort.Slice(out.WaitClasses, func(i, j int) bool {
		if out.WaitClasses[i].PerSec != out.WaitClasses[j].PerSec {
			return out.WaitClasses[i].PerSec > out.WaitClasses[j].PerSec
		}
		return out.WaitClasses[i].Name < out.WaitClasses[j].Name
	})

	// Lock waits: first present spelling wins (declared precedence
	// mirrors mysql.go / oracle.go fallback order).
	for _, m := range sp.LockWaitCounters {
		if v, ok := rates[m]; ok {
			v = clamp(v)
			out.Locks.WaitsPerSec = &v
			break
		}
	}

	// Lock time: rate × window = total over window, then receiver
	// unit → seconds.
	for _, m := range sp.LockTimeCounters {
		if v, ok := rates[m]; ok {
			div := sp.LockTimeDivisor
			if div <= 0 {
				div = 1
			}
			t := clamp(v) * windowSec / div
			out.Locks.TimeSec = &t
			break
		}
	}

	// Deadlocks: SUM every present counter (oracle splits enqueue vs
	// exchange; both are deadlocks to the operator).
	var dl float64
	seenDL := false
	for _, m := range sp.DeadlockCounters {
		if v, ok := rates[m]; ok {
			dl += clamp(v)
			seenDL = true
		}
	}
	if seenDL {
		out.Locks.DeadlocksPerSec = &dl
	}

	if len(modes) > 0 {
		out.Locks.ByMode = modes
	}
}

// GetDBWaitLock reads the per-engine wait/lock families for one
// (system, instance) and normalizes them into the common strip
// model. At most two bounded metric_points trips (counters + the
// optional dimensioned lock-mode read). Unsupported engines return
// Supported=false with zero CH trips.
func (s *Store) GetDBWaitLock(ctx context.Context, system, instance string, from, to time.Time) (*DBWaitLock, error) {
	out := &DBWaitLock{
		System:      strings.ToLower(strings.TrimSpace(system)),
		Instance:    instance,
		WaitClasses: []DBWaitClass{},
	}
	sp, ok := dbWaitLockSpecFor(system)
	if !ok {
		return out, nil
	}
	out.Supported = true
	out.System = sp.Engine

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
	out.WindowSeconds = windowSec

	// Same fallback as the engine panels: an empty/unknown instance
	// drops the filter (single-DB deployments whose receiver doesn't
	// tag points per-instance).
	withInstance := instance != "" && instance != "unknown"

	// Trip 1 — every cumulative family at once.
	q := dbWaitLockCounterSQL(sp, withInstance)
	args := []any{windowSec, from, to}
	for _, m := range sp.counterMetrics() {
		args = append(args, m)
	}
	if withInstance {
		args = append(args, instance, instance)
	}
	rates, err := scanMetricMap(ctx, s, q, args)
	if err != nil {
		return nil, err
	}

	// Trip 2 — dimensioned lock modes, only where the engine has a
	// dimensioned family. Non-fatal: the strip renders the counter
	// families even if this read fails.
	var modes []DBLockMode
	if len(sp.LockModeMetrics) > 0 {
		if m, err := s.queryDBWaitLockModes(ctx, sp, instance, withInstance, from, to, windowSec); err == nil {
			modes = m
		}
	}

	assembleDBWaitLock(sp, rates, modes, windowSec, out)
	return out, nil
}

func (s *Store) queryDBWaitLockModes(
	ctx context.Context, sp dbWaitLockSpec, instance string, withInstance bool,
	from, to time.Time, windowSec float64,
) ([]DBLockMode, error) {
	q := dbWaitLockModeSQL(sp, withInstance)
	args := []any{}
	unit := "count"
	if sp.LockModeIsRate {
		args = append(args, windowSec) // the SELECT-clause divisor binds first
		unit = "/s"
	}
	args = append(args, from, to)
	for _, m := range sp.LockModeMetrics {
		args = append(args, m)
	}
	if withInstance {
		args = append(args, instance, instance)
	}
	rows, err := s.conn.Query(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []DBLockMode{}
	for rows.Next() {
		var e DBLockMode
		if err := rows.Scan(&e.Mode, &e.Value); err != nil {
			continue
		}
		if e.Value < 0 {
			e.Value = 0 // counter reset → suppress
		}
		e.Unit = unit
		out = append(out, e)
	}
	return out, nil
}
