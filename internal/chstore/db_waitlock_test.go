package chstore

// db_waitlock_test.go — pins the cross-engine waits & locks strip
// (v0.8.391, Stage-2 D3). Two invariant families:
//
//  1. Metric-family mapping: which receiver metric names feed each
//     engine's section of the common model. A receiver-convention
//     rename (or an accidental family swap between engines) breaks
//     here before it ships a silently-empty strip.
//  2. SQL bounds: every metric_points query carries the time WHERE,
//     a LIMIT, and max_execution_time ≤ 10 — the CLAUDE.md hard
//     constraint for raw metric_points reads.
//
// Plus the pure assembler: absence vs honest-zero semantics (the
// strip's honesty contract), oracle deadlock summing, mysql ms→s.

import (
	"strings"
	"testing"
)

func TestDBWaitLockSpecFor(t *testing.T) {
	cases := []struct {
		system     string
		ok         bool
		engine     string
		waitPrefix string // one prefix that must be present ("" = none)
		lockWait   string // one lock-wait counter that must be present
		lockTime   string
		deadlock   string
		modeMetric string
		modeAttr   string
		modeIsRate bool
	}{
		{"oracle", true, "oracle", "oracledb.wait_time.", "oracledb.row_lock_waits", "", "oracledb.enqueue_deadlocks", "", "", false},
		{"Oracle", true, "oracle", "oracledb.wait_time.", "oracledb.row_lock_waits", "", "oracledb.exchange_deadlocks", "", "", false},
		{"oracledb", true, "oracle", "oracledb_wait_time_", "oracledb.enq.tx.row_lock_contention", "", "oracledb.enqueue_deadlocks", "", "", false},
		{"postgresql", true, "postgresql", "", "", "", "postgresql.deadlocks", "postgresql.database.locks", "lock_type", false},
		{"postgres", true, "postgresql", "", "", "", "postgresql.deadlocks", "postgresql.locks", "lock_type", false},
		{"mysql", true, "mysql", "", "mysql.row_locks", "mysql.innodb.row_lock.time", "", "mysql.locks", "kind", true},
		{"mariadb", true, "mysql", "", "mysql.innodb.row_lock.waits", "mysql.row_locks_time", "", "mysql.locks", "kind", true},
		{"MySQL ", true, "mysql", "", "mysql.row_locks", "mysql.innodb.row_lock.time", "", "mysql.locks", "kind", true},
		// No receiver wait/lock story → unsupported, zero CH trips.
		{"redis", false, "", "", "", "", "", "", "", false},
		{"mssql", false, "", "", "", "", "", "", "", false},
		{"mongodb", false, "", "", "", "", "", "", "", false},
		{"", false, "", "", "", "", "", "", "", false},
	}
	for _, c := range cases {
		t.Run(c.system, func(t *testing.T) {
			sp, ok := dbWaitLockSpecFor(c.system)
			if ok != c.ok {
				t.Fatalf("dbWaitLockSpecFor(%q) ok=%v want %v", c.system, ok, c.ok)
			}
			if !ok {
				return
			}
			if sp.Engine != c.engine {
				t.Errorf("engine=%q want %q", sp.Engine, c.engine)
			}
			has := func(list []string, want string) bool {
				for _, m := range list {
					if m == want {
						return true
					}
				}
				return false
			}
			if c.waitPrefix != "" && !has(sp.WaitClassPrefixes, c.waitPrefix) {
				t.Errorf("wait-class prefixes %v missing %q", sp.WaitClassPrefixes, c.waitPrefix)
			}
			if c.waitPrefix == "" && len(sp.WaitClassPrefixes) != 0 {
				t.Errorf("engine %s must not claim a wait-class family (honesty contract), got %v", sp.Engine, sp.WaitClassPrefixes)
			}
			if c.lockWait != "" && !has(sp.LockWaitCounters, c.lockWait) {
				t.Errorf("lock-wait counters %v missing %q", sp.LockWaitCounters, c.lockWait)
			}
			if c.lockTime != "" && !has(sp.LockTimeCounters, c.lockTime) {
				t.Errorf("lock-time counters %v missing %q", sp.LockTimeCounters, c.lockTime)
			}
			if c.deadlock != "" && !has(sp.DeadlockCounters, c.deadlock) {
				t.Errorf("deadlock counters %v missing %q", sp.DeadlockCounters, c.deadlock)
			}
			if c.modeMetric != "" {
				if !has(sp.LockModeMetrics, c.modeMetric) {
					t.Errorf("lock-mode metrics %v missing %q", sp.LockModeMetrics, c.modeMetric)
				}
				if sp.LockModeAttr != c.modeAttr {
					t.Errorf("lock-mode attr %q want %q", sp.LockModeAttr, c.modeAttr)
				}
				if sp.LockModeIsRate != c.modeIsRate {
					t.Errorf("lock-mode isRate=%v want %v", sp.LockModeIsRate, c.modeIsRate)
				}
			}
			// mysql lock time arrives in milliseconds — the divisor is
			// the every-unit-tested branch (unit-mixing pitfall class).
			if sp.Engine == "mysql" && sp.LockTimeDivisor != 1000 {
				t.Errorf("mysql LockTimeDivisor=%v want 1000 (ms→s)", sp.LockTimeDivisor)
			}
			if sp.instanceClause == nil {
				t.Errorf("engine %s has no instance clause", sp.Engine)
			}
		})
	}
}

// Every engine × withInstance combination: the counter query must be
// time-bounded, LIMITed, and capped at max_execution_time = 10.
func TestDBWaitLockCounterSQLBounds(t *testing.T) {
	for _, system := range []string{"oracle", "postgresql", "mysql"} {
		sp, ok := dbWaitLockSpecFor(system)
		if !ok {
			t.Fatalf("spec for %s missing", system)
		}
		for _, withInstance := range []bool{true, false} {
			sql := dbWaitLockCounterSQL(sp, withInstance)
			for _, want := range []string{"time >= ?", "time <= ?", "LIMIT 64", "max_execution_time = 10", "FROM metric_points", "GROUP BY metric"} {
				if !strings.Contains(sql, want) {
					t.Errorf("%s counter SQL (withInstance=%v) missing %q\n--- SQL ---\n%s", system, withInstance, want, sql)
				}
			}
			// The IN list binds exactly the spec's exact-name counters.
			if n := len(sp.counterMetrics()); n > 0 {
				if got := strings.Count(sql, "?") - 3 - boolToInt(withInstance)*2; got != n {
					t.Errorf("%s counter SQL binds %d metric placeholders, want %d\n--- SQL ---\n%s", system, got, n, sql)
				}
			}
			// Wait-class prefixes appear for oracle ONLY — a prefix
			// bleeding into pg/mysql would fabricate a wait-class family
			// the receiver never emits.
			hasPrefix := strings.Contains(sql, "startsWith(metric,")
			if (system == "oracle") != hasPrefix {
				t.Errorf("%s counter SQL wait-class prefix presence=%v, want %v\n--- SQL ---\n%s", system, hasPrefix, system == "oracle", sql)
			}
			if withInstance && !strings.Contains(sql, "indexOf(") {
				t.Errorf("%s counter SQL (withInstance) missing instance clause\n--- SQL ---\n%s", system, sql)
			}
		}
	}
}

func TestDBWaitLockModeSQLBounds(t *testing.T) {
	// postgresql: gauge semantics (argMax latest per mode).
	pg, _ := dbWaitLockSpecFor("postgresql")
	pgSQL := dbWaitLockModeSQL(pg, false)
	for _, want := range []string{"argMax(value, time)", "lock_type", "time >= ?", "time <= ?", "LIMIT 20", "max_execution_time = 10", "HAVING mode != ''"} {
		if !strings.Contains(pgSQL, want) {
			t.Errorf("pg mode SQL missing %q\n--- SQL ---\n%s", want, pgSQL)
		}
	}
	if strings.Contains(pgSQL, "max(value) - min(value)") {
		t.Errorf("pg lock modes are a gauge — rate derivation is wrong\n--- SQL ---\n%s", pgSQL)
	}

	// mysql: cumulative table-lock counters → rate per mode.
	my, _ := dbWaitLockSpecFor("mysql")
	mySQL := dbWaitLockModeSQL(my, true)
	for _, want := range []string{"(max(value) - min(value)) / ?", "'kind'", "LIMIT 20", "max_execution_time = 10", "indexOf("} {
		if !strings.Contains(mySQL, want) {
			t.Errorf("mysql mode SQL missing %q\n--- SQL ---\n%s", want, mySQL)
		}
	}
}

// The strip's honesty contract lives in the assembler: a family with
// no points stays nil (→ "no lock telemetry from this receiver"),
// while present-but-flat counters report an honest 0.
func TestAssembleDBWaitLock(t *testing.T) {
	t.Run("oracle full", func(t *testing.T) {
		sp, _ := dbWaitLockSpecFor("oracle")
		out := &DBWaitLock{WaitClasses: []DBWaitClass{}}
		assembleDBWaitLock(sp, map[string]float64{
			"oracledb.wait_time.user_io":     0.8,
			"oracledb.wait_time.concurrency": 1.2,
			"oracledb.wait_time.commit":      -0.5, // reset → clamp 0
			"oracledb.enqueue_deadlocks":     0.002,
			"oracledb.exchange_deadlocks":    0.001,
			"oracledb.row_lock_waits":        0.4,
		}, nil, 3600, out)
		if len(out.WaitClasses) != 3 {
			t.Fatalf("waitClasses=%d want 3", len(out.WaitClasses))
		}
		// Descending by perSec; the clamped reset lands last at 0.
		if out.WaitClasses[0].Name != "concurrency" || out.WaitClasses[1].Name != "user_io" {
			t.Errorf("wait-class order wrong: %+v", out.WaitClasses)
		}
		if out.WaitClasses[2].Name != "commit" || out.WaitClasses[2].PerSec != 0 {
			t.Errorf("counter reset must clamp to 0: %+v", out.WaitClasses[2])
		}
		if out.Locks.WaitsPerSec == nil || *out.Locks.WaitsPerSec != 0.4 {
			t.Errorf("waitsPerSec=%v want 0.4", out.Locks.WaitsPerSec)
		}
		// enqueue + exchange SUM.
		if out.Locks.DeadlocksPerSec == nil || *out.Locks.DeadlocksPerSec != 0.003 {
			t.Errorf("deadlocksPerSec=%v want 0.003 (enqueue+exchange sum)", out.Locks.DeadlocksPerSec)
		}
		if out.Locks.TimeSec != nil {
			t.Errorf("oracle has no lock-time family; got %v", *out.Locks.TimeSec)
		}
	})

	t.Run("absence stays nil — never fake zeros", func(t *testing.T) {
		sp, _ := dbWaitLockSpecFor("oracle")
		out := &DBWaitLock{WaitClasses: []DBWaitClass{}}
		assembleDBWaitLock(sp, map[string]float64{}, nil, 3600, out)
		if out.Locks.WaitsPerSec != nil || out.Locks.DeadlocksPerSec != nil || out.Locks.TimeSec != nil {
			t.Errorf("no points in window must keep every pointer nil: %+v", out.Locks)
		}
		if len(out.WaitClasses) != 0 {
			t.Errorf("no points must keep waitClasses empty: %+v", out.WaitClasses)
		}
	})

	t.Run("present-but-flat counter is an honest zero", func(t *testing.T) {
		sp, _ := dbWaitLockSpecFor("postgresql")
		out := &DBWaitLock{WaitClasses: []DBWaitClass{}}
		assembleDBWaitLock(sp, map[string]float64{"postgresql.deadlocks": 0}, nil, 3600, out)
		if out.Locks.DeadlocksPerSec == nil || *out.Locks.DeadlocksPerSec != 0 {
			t.Errorf("flat counter must report 0, not absence: %+v", out.Locks.DeadlocksPerSec)
		}
	})

	t.Run("mysql ms→s over the window + spelling precedence", func(t *testing.T) {
		sp, _ := dbWaitLockSpecFor("mysql")
		out := &DBWaitLock{WaitClasses: []DBWaitClass{}}
		// 2 ms of lock time accrued per second over a 600 s window
		// → 1200 ms total → 1.2 s. Both time spellings present: the
		// canonical OTel name (declared first) must win.
		assembleDBWaitLock(sp, map[string]float64{
			"mysql.innodb.row_lock.time": 2,
			"mysql.row_locks_time":       9999,
			"mysql.innodb.row_lock.waits": 0.7, // fallback spelling
		}, []DBLockMode{{Mode: "waited", Value: 0.1, Unit: "/s"}}, 600, out)
		if out.Locks.TimeSec == nil || *out.Locks.TimeSec != 1.2 {
			t.Errorf("timeSec=%v want 1.2 (2 ms/s × 600 s ÷ 1000)", out.Locks.TimeSec)
		}
		if out.Locks.WaitsPerSec == nil || *out.Locks.WaitsPerSec != 0.7 {
			t.Errorf("waitsPerSec=%v want 0.7 via fallback spelling", out.Locks.WaitsPerSec)
		}
		if len(out.Locks.ByMode) != 1 || out.Locks.ByMode[0].Mode != "waited" {
			t.Errorf("byMode passthrough broken: %+v", out.Locks.ByMode)
		}
	})
}

func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}
