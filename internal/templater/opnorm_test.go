package templater

// op_group normalizer (group_id release A). Table-driven coverage
// of NormalizeOperation across every source-priority branch:
// http.route, raw "METHOD path", DB-statement shaping, generic name
// fallback, and the empty case. The over-normalization guards
// ("/api/v2" keeps v2, "health" stays) pin the boundary so a future
// tweak to LooksLikeOpaqueID can't silently start eating version
// tokens or plain words.

import "testing"

func TestNormalizeOperation(t *testing.T) {
	cases := []struct {
		name string // test label
		// inputs
		op, kind, method, route, dbSys, dbStmt string
		want                                   string
	}{
		// ── (a) http.route present — instrumentation already templated ──
		{
			name:   "route + method",
			op:     "HTTP GET", method: "GET", route: "/users/{id}",
			want: "GET /users/{id}",
		},
		{
			name:  "route, client span no method",
			op:    "GET", route: "/orders/{orderId}/items",
			want: "/orders/{orderId}/items",
		},
		{
			name:   "route wins over db",
			method: "POST", route: "/charge", dbSys: "postgresql", dbStmt: "INSERT INTO x VALUES (1)",
			want: "POST /charge",
		},

		// ── (a') raw "METHOD path" — we template the segments ──
		{
			name: "numeric id segment",
			op:   "GET /users/12345",
			want: "GET /users/:id",
		},
		{
			name: "two numeric id segments",
			op:   "GET /users/12345/orders/678",
			want: "GET /users/:id/orders/:id",
		},
		{
			name: "single short numeric id (URL number is always id)",
			op:   "GET /users/5",
			want: "GET /users/:id",
		},
		{
			name: "uuid segment",
			op:   "DELETE /sessions/550e8400-e29b-41d4-a716-446655440000",
			want: "DELETE /sessions/:id",
		},
		{
			name: "hex digest segment",
			op:   "GET /blob/9f86d081884c7d659a2feaa0c55ad015a3bf4f1b2b0b822cd15d6c15b0f00a08",
			want: "GET /blob/:id",
		},
		{
			name: "trailing query string collapsed",
			op:   "GET /search?q=hello&page=2",
			want: "GET /search",
		},
		{
			name: "non-id segments unchanged (version + word)",
			op:   "GET /api/v1/health",
			want: "GET /api/v1/health",
		},
		{
			name: "POST with trailing slash + id",
			op:   "POST /accounts/99887766/transfer",
			want: "POST /accounts/:id/transfer",
		},
		{
			name: "root path",
			op:   "GET /",
			want: "GET /",
		},
		{
			name: "method-shaped name with db attrs present still goes http path",
			op:   "PUT /items/42", dbSys: "mysql", dbStmt: "UPDATE items SET x=1",
			want: "PUT /items/:id",
		},

		// ── (b) DB statement shaping ──
		{
			name:  "sql number + string literal",
			dbSys: "postgresql", dbStmt: "SELECT * FROM t WHERE id=678 AND s='x'",
			want: "postgresql: SELECT * FROM t WHERE id=? AND s=?",
		},
		{
			name:  "IN list collapses",
			dbSys: "postgresql", dbStmt: "SELECT * FROM t WHERE id IN (1,2,3)",
			want: "postgresql: SELECT * FROM t WHERE id IN (?)",
		},
		{
			name:  "IN list with spaces collapses",
			dbSys: "mysql", dbStmt: "DELETE FROM q WHERE k IN ( 10, 20 , 30 )",
			want: "mysql: DELETE FROM q WHERE k IN (?)",
		},
		{
			name:  "multiline + tabs whitespace collapsed",
			dbSys: "oracle", dbStmt: "SELECT a,\n\tb\nFROM   tbl\nWHERE  z = 9",
			want: "oracle: SELECT a, b FROM tbl WHERE z = ?",
		},
		{
			name:  "double-quoted literal",
			dbSys: "postgresql", dbStmt: `SELECT * FROM t WHERE name = "bob"`,
			want: `postgresql: SELECT * FROM t WHERE name = ?`,
		},
		{
			name:  "float literal",
			dbSys: "mysql", dbStmt: "UPDATE acct SET bal = 3.14 WHERE id = 7",
			want: "mysql: UPDATE acct SET bal = ? WHERE id = ?",
		},
		{
			name:  "db system set but empty statement falls back to name",
			op:    "oracle.execute", dbSys: "oracle", dbStmt: "",
			want: "oracle.execute",
		},
		{
			name:  "db system + empty statement + empty name",
			dbSys: "oracle", dbStmt: "",
			want: "",
		},

		// ── (c) generic name fallback ──
		{
			name: "generic name with id segment",
			op:   "process/order/12345",
			want: "process/order/:id",
		},
		{
			name: "generic name with uuid token, space separated",
			op:   "handle job 550e8400-e29b-41d4-a716-446655440000",
			want: "handle job :id",
		},
		{
			name: "generic plain operation unchanged",
			op:   "Kafka publish payments.events",
			want: "Kafka publish payments.events",
		},
		{
			name: "generic name with numeric token >=4 digits",
			op:   "worker.task.88273",
			want: "worker.task.88273", // '.'-joined token, not a path/space sep → whole token survives
		},

		// ── over-normalization guards ──
		{
			name: "v2 version token kept",
			op:   "GET /api/v2/users",
			want: "GET /api/v2/users",
		},
		{
			name: "plain word health stays",
			op:   "health",
			want: "health",
		},
		{
			name: "short word not eaten",
			op:   "GET /ping",
			want: "GET /ping",
		},

		// ── (d) empty ──
		{
			name: "empty name nothing applies",
			want: "",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := NormalizeOperation(tc.op, tc.kind, tc.method, tc.route, tc.dbSys, tc.dbStmt)
			if got != tc.want {
				t.Errorf("NormalizeOperation(op=%q method=%q route=%q dbSys=%q dbStmt=%q)\n got = %q\nwant = %q",
					tc.op, tc.method, tc.route, tc.dbSys, tc.dbStmt, got, tc.want)
			}
		})
	}
}

// TestNormalizeOperationCap200 proves the 200-char cap holds on a
// pathologically long DB statement (LowCardinality dict hygiene).
func TestNormalizeOperationCap200(t *testing.T) {
	long := "SELECT "
	for i := 0; i < 500; i++ {
		long += "col_a, "
	}
	long += "FROM t"
	got := NormalizeOperation("", "", "", "", "postgresql", long)
	if len(got) > opGroupMaxLen {
		t.Fatalf("result not capped: len=%d > %d", len(got), opGroupMaxLen)
	}
}
