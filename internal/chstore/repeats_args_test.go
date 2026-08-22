package chstore

import (
	"strings"
	"testing"
	"time"
)

// v0.9.1286 — Explore "Repeats" returned HTTP 500 for every group-by
// except the default. Reproduced live on v0.9.1285:
// GET /api/spans/repeats?groupBy=resource.k8s.namespace.name answered
// 500 "code 386, There is no supertype for types String, DateTime ...
// indexOf(res_keys, toDateTime('2026-08-22 21:04:32'))" — the window's
// From timestamp had landed in the attribute-NAME slot.
//
// Root cause: ClickHouse binds `?` positionally, so the arg list must
// follow the order the placeholders appear in the SQL TEXT. The
// group-key projection sits in the inner SELECT, ABOVE the inner
// WHERE, but the args were assembled where-clause-first.
//
// The reason it survived: groupKeyExpr emits NO bind arg for a
// well-known key (db.statement resolves to the `db_statement`
// column), and db.statement is the default. With zero group args the
// misordering is a no-op — the defect was invisible on the one path
// anybody exercised and fatal on every other.
//
// So the test drives the arg builder and the SQL builder from the
// SAME inputs and checks they agree slot for slot. Asserting the
// concatenation order alone would just restate the implementation.
func TestRepeatedSpansArgsMatchPlaceholderOrder(t *testing.T) {
	from := time.Date(2026, 8, 22, 21, 4, 32, 0, time.UTC)
	to := from.Add(10 * time.Minute)

	cases := []struct {
		name string
		// keysArray / whereSQL as the real builders render them, with
		// the bind args that belong to each.
		keysArray  string
		keyArgs    []any
		whereSQL   string
		whereArgs  []any
		minRepeats int
		limit      int
		// wantHead is the expected prefix of the arg list — the slots
		// whose ordering is the bug.
		wantHead []any
	}{
		{
			name:       "well-known column emits no group arg (the default that hid the bug)",
			keysArray:  "[toString(db_statement)]",
			keyArgs:    nil,
			whereSQL:   "WHERE time >= ? AND time <= ?",
			whereArgs:  []any{from, to},
			minRepeats: 5,
			limit:      200,
			wantHead:   []any{from, to},
		},
		{
			name:       "resource key — the shape that 500'd with code 386",
			keysArray:  "[toString(res_values[indexOf(res_keys, ?)])]",
			keyArgs:    []any{"k8s.namespace.name"},
			whereSQL:   "WHERE time >= ? AND time <= ?",
			whereArgs:  []any{from, to},
			minRepeats: 5,
			limit:      200,
			wantHead:   []any{"k8s.namespace.name", from, to},
		},
		{
			name:       "arbitrary span attr key",
			keysArray:  "[toString(attr_values[indexOf(attr_keys, ?)])]",
			keyArgs:    []any{"messaging.destination.name"},
			whereSQL:   "WHERE time >= ? AND time <= ?",
			whereArgs:  []any{from, to},
			minRepeats: 3,
			limit:      50,
			wantHead:   []any{"messaging.destination.name", from, to},
		},
		{
			name:       "two group keys, both carrying args",
			keysArray:  "[toString(attr_values[indexOf(attr_keys, ?)]), toString(res_values[indexOf(res_keys, ?)])]",
			keyArgs:    []any{"http.route", "service.version"},
			whereSQL:   "WHERE time >= ? AND time <= ?",
			whereArgs:  []any{from, to},
			minRepeats: 5,
			limit:      200,
			wantHead:   []any{"http.route", "service.version", from, to},
		},
		{
			name:       "group key plus an operator filter in the WHERE",
			keysArray:  "[toString(attr_values[indexOf(attr_keys, ?)])]",
			keyArgs:    []any{"db.operation"},
			whereSQL:   "WHERE time >= ? AND time <= ? AND service_name = ?",
			whereArgs:  []any{from, to, "portfolio-service"},
			minRepeats: 5,
			limit:      200,
			wantHead:   []any{"db.operation", from, to, "portfolio-service"},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			sql := repeatedSpansSQL(tc.keysArray, tc.whereSQL)
			args := repeatedSpansArgs(tc.keyArgs, tc.whereArgs, tc.minRepeats, tc.limit, from, to)

			// Every placeholder must have exactly one arg. A mismatch
			// here means the driver errors or silently shifts.
			if n := strings.Count(sql, "?"); n != len(args) {
				t.Fatalf("SQL has %d placeholders but %d args were bound\n--- SQL ---\n%s",
					n, len(args), sql)
			}

			// The head slots are the ones that regressed.
			for i, want := range tc.wantHead {
				if args[i] != want {
					t.Errorf("arg[%d] = %#v, want %#v — positional binding follows "+
						"SQL TEXT order (group-key projection, then inner WHERE)",
						i, args[i], want)
				}
			}

			// The group-key slots must never receive a time. That is the
			// exact shape of the code 386 the operator hit.
			for i := range tc.keyArgs {
				if _, isTime := args[i].(time.Time); isTime {
					t.Errorf("arg[%d] is a time.Time in a group-key slot — this is the "+
						"v0.9.1286 defect: indexOf(keys, toDateTime(...)) → CH code 386",
						i)
				}
			}

			// Tail order is the other half of the contract.
			tail := args[len(args)-4:]
			if tail[0] != tc.minRepeats || tail[1] != tc.limit || tail[2] != from || tail[3] != to {
				t.Errorf("tail args = %#v, want [minRepeats limit from to] = %#v",
					tail, []any{tc.minRepeats, tc.limit, from, to})
			}
		})
	}
}
