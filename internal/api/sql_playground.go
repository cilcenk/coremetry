package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"reflect"
	"regexp"
	"strings"
	"time"

	"github.com/ClickHouse/clickhouse-go/v2"
	"github.com/cilcenk/coremetry/internal/auth"
	"github.com/cilcenk/coremetry/internal/logstore"
)

// SQL playground — admin-only ad-hoc query interface against the
// same ClickHouse instance Coremetry runs against. Three layers
// of defence:
//
//  1. Server-side: only signed-in users with the admin role hit
//     this endpoint.
//  2. Application-side: a string-level allow-list rejects anything
//     that isn't a single SELECT / SHOW / DESCRIBE / EXPLAIN /
//     WITH statement.
//  3. Storage-side: every query runs with `readonly = 2` plus a
//     hard 60s execution-time + 10k row-result cap. Even if the
//     allow-list missed a vector, ClickHouse itself rejects DDL /
//     DML on this connection.
//
// All three are required — banking-grade ops won't accept any
// single layer as the boundary.

// commentRe strips single-line `--` and block /* … */ comments so
// the prefix check sees the actual leading keyword.
var (
	lineCommentRe  = regexp.MustCompile(`(?m)--[^\n]*`)
	blockCommentRe = regexp.MustCompile(`(?s)/\*.*?\*/`)
)

// allowedPrefixes are the only SQL verbs the playground accepts.
// Hard-coded — operator-supplied query goes through this gate
// before ever hitting the connection. Order doesn't matter; the
// check is exact-prefix on the cleaned, uppercased statement.
var allowedPrefixes = []string{
	"SELECT", "WITH", "SHOW", "DESCRIBE", "DESC", "EXPLAIN",
}

// isSafeReadSQL returns true iff `q` is a single read-only
// statement starting with one of allowedPrefixes. Comments are
// stripped first. Multi-statement queries (separated by ';') are
// rejected — operator can re-issue separately.
func isSafeReadSQL(q string) bool {
	clean := blockCommentRe.ReplaceAllString(q, "")
	clean = lineCommentRe.ReplaceAllString(clean, "")
	clean = strings.TrimSpace(clean)
	if clean == "" {
		return false
	}
	// Allow exactly one trailing semicolon — operators paste from
	// the docs all the time.
	clean = strings.TrimRight(clean, "; \t\r\n")
	if strings.Contains(clean, ";") {
		return false
	}
	// Get the first word. Letter-only — anything else means a
	// quoted / punctuation-prefixed payload we don't want to
	// touch.
	cu := strings.ToUpper(clean)
	for _, p := range allowedPrefixes {
		// `SELECT 1` or `SELECT(1)` or `SELECT\t1` should match;
		// `SELECTUS` (a hypothetical column name) should not.
		if strings.HasPrefix(cu, p) {
			next := byte(' ')
			if len(cu) > len(p) {
				next = cu[len(p)]
			}
			if next == ' ' || next == '\t' || next == '\n' || next == '(' || next == 0 {
				return true
			}
		}
	}
	return false
}

// execSQL runs a sanitised SELECT-style query and returns
// {columns, rows, tookMs}. Each row is []any with values
// stringified for safe JSON transport (CH driver returns
// concrete types, but mixing Int64/Float64/Time/Decimal
// payloads forces hand-marshalling per column).
func (s *Server) execSQL(w http.ResponseWriter, r *http.Request) {
	claims := auth.FromContext(r.Context())
	if claims == nil || claims.Role != auth.RoleAdmin {
		http.Error(w, "admin only", http.StatusForbidden)
		return
	}
	var body struct {
		Query string `json:"query"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "invalid body: "+err.Error(), http.StatusBadRequest)
		return
	}
	body.Query = strings.TrimSpace(body.Query)
	if !isSafeReadSQL(body.Query) {
		http.Error(w, "only single SELECT / WITH / SHOW / DESCRIBE / EXPLAIN statements allowed", http.StatusBadRequest)
		return
	}

	// Storage-side enforcement: readonly=2 (SELECT-only), 60s
	// time cap, 10k result rows, plus a memory cap so a runaway
	// CROSS JOIN doesn't drag the whole server down.
	ctx := clickhouse.Context(r.Context(), clickhouse.WithSettings(clickhouse.Settings{
		"readonly":           2,
		"max_execution_time": 60,
		"max_result_rows":    10_000,
		"max_memory_usage":   uint64(4_000_000_000),
		// Safety net: if the operator's query somehow tries
		// async insert (it can't past readonly=2, but defence
		// in depth), make sure it won't be silently coalesced.
		"async_insert": 0,
	}))

	start := time.Now()
	rows, err := s.store.Conn().Query(ctx, body.Query)
	if err != nil {
		writeJSON(w, map[string]any{"error": err.Error()})
		return
	}
	defer rows.Close()

	// The clickhouse-go driver rejects bare `*interface{}` scan
	// destinations on String / FixedString / typed columns —
	// "converting String to *interface {} is unsupported". We
	// have no schema knowledge here (the operator typed the
	// query), so we reflectively allocate a destination of the
	// column's ScanType() ahead of every row, scan into those
	// pointers, then dereference + JSON-friendly-ify (timestamps
	// → RFC3339, []byte → string, big.Int → string, etc.).
	cts := rows.ColumnTypes()
	cols := make([]string, len(cts))
	scanTypes := make([]reflect.Type, len(cts))
	for i, c := range cts {
		cols[i] = c.Name()
		scanTypes[i] = c.ScanType()
		if scanTypes[i] == nil {
			// Fallback: driver couldn't infer a Go type. `string`
			// is the safest universal target — CH will format
			// the value through its TextEncoder.
			scanTypes[i] = reflect.TypeOf("")
		}
	}

	out := make([][]any, 0, 64)
	for rows.Next() {
		// Per row: allocate concrete pointers per column's scan
		// type. reflect.New(T) returns *T as reflect.Value;
		// .Interface() gets the pointer back to driver-friendly
		// `any` form for Scan().
		ptrs := make([]any, len(cts))
		dests := make([]reflect.Value, len(cts))
		for i := range cts {
			v := reflect.New(scanTypes[i])
			ptrs[i] = v.Interface()
			dests[i] = v.Elem()
		}
		if err := rows.Scan(ptrs...); err != nil {
			writeJSON(w, map[string]any{"error": err.Error()})
			return
		}
		row := make([]any, len(cts))
		for i := range cts {
			row[i] = jsonifyValue(dests[i].Interface())
		}
		out = append(out, row)
	}
	if err := rows.Err(); err != nil {
		writeJSON(w, map[string]any{"error": err.Error()})
		return
	}

	took := time.Since(start)
	// Audit every query — query text + row count + duration go
	// into the append-only log so an admin can review later.
	s.audit(r, "sql.query", "sql_playground", "",
		fmt.Sprintf(`{"rows":%d,"tookMs":%d,"query":%q}`, len(out), took.Milliseconds(), body.Query))

	writeJSON(w, map[string]any{
		"columns":  cols,
		"rows":     out,
		"rowCount": len(out),
		"tookMs":   took.Milliseconds(),
	})
}

// jsonifyValue normalises a CH driver scan output into a value
// the default JSON encoder + the SPA's table renderer can both
// handle without custom marshalling. Most Go primitives pass
// through; the few CH-specific types (time.Time, big numbers,
// []byte, UUIDs as [16]byte) are stringified for transport.
//
// Pointers are flattened to their pointee — `*string` → `string`,
// nil → JSON null. Arrays / Maps fall through; the encoder
// renders them as arrays / objects natively.
func jsonifyValue(v any) any {
	if v == nil {
		return nil
	}
	rv := reflect.ValueOf(v)
	if rv.Kind() == reflect.Ptr {
		if rv.IsNil() {
			return nil
		}
		v = rv.Elem().Interface()
	}
	switch x := v.(type) {
	case time.Time:
		return x.Format(time.RFC3339Nano)
	case []byte:
		return string(x)
	case fmt.Stringer:
		// Covers big.Int / big.Float / decimal / netip.Addr,
		// any custom CH type that implements Stringer.
		return x.String()
	}
	return v
}

// SchemaTable + SchemaColumn drive the playground's left-side
// browser. Operator clicks a column → it pastes into the editor
// at cursor; click a table → SELECT * FROM <table> LIMIT 100
// gets generated. Both query system.tables / system.columns,
// pre-filtered to the current database. Results cached 60s.
type SchemaTable struct {
	Table   string         `json:"table"`
	Engine  string         `json:"engine"`
	Columns []SchemaColumn `json:"columns"`
}

type SchemaColumn struct {
	Name string `json:"name"`
	Type string `json:"type"`
}

// execElasticSQL is the Elasticsearch counterpart to execSQL.
// Read-only by definition — ES SQL plugin only supports SELECT
// / SHOW / DESCRIBE; we still keep a first-token safety net.
// Audited the same way as the CH path so the admin trail
// includes cross-backend queries uniformly.
func (s *Server) execElasticSQL(w http.ResponseWriter, r *http.Request) {
	claims := auth.FromContext(r.Context())
	if claims == nil || claims.Role != auth.RoleAdmin {
		http.Error(w, "admin only", http.StatusForbidden)
		return
	}
	// Type-assert the configured logs backend to the ES SQL
	// runner — the LogStore interface stays unchanged; only ES
	// implements ExecSQL. CH-backed deployments get a clean 400
	// instead of a misleading 500.
	type esSQLRunner interface {
		ExecSQL(ctx context.Context, query string, fetchSize int) (*logstore.SQLResult, error)
	}
	runner, ok := s.logs.(interface {
		ExecSQL(ctx context.Context, query string, fetchSize int) (*logstore.SQLResult, error)
	})
	_ = esSQLRunner(nil) // doc: which interface shape; suppresses unused name
	if !ok {
		http.Error(w, `{"error":"Elasticsearch SQL not available — current logs backend is `+s.logs.Backend()+`"}`, http.StatusBadRequest)
		return
	}
	var body struct {
		Query     string `json:"query"`
		FetchSize int    `json:"fetchSize"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "invalid body: "+err.Error(), http.StatusBadRequest)
		return
	}
	body.Query = strings.TrimSpace(body.Query)
	if !isSafeReadSQL(body.Query) {
		http.Error(w, "only single SELECT / WITH / SHOW / DESCRIBE statements allowed", http.StatusBadRequest)
		return
	}
	res, err := runner.ExecSQL(r.Context(), body.Query, body.FetchSize)
	if err != nil {
		writeJSON(w, map[string]any{"error": err.Error()})
		return
	}
	s.audit(r, "sql.query", "es_sql_playground", "",
		fmt.Sprintf(`{"backend":"elasticsearch","rows":%d,"tookMs":%d,"query":%q}`,
			len(res.Rows), res.TookMs, body.Query))
	writeJSON(w, map[string]any{
		"columns":  res.Columns,
		"rows":     res.Rows,
		"rowCount": len(res.Rows),
		"tookMs":   res.TookMs,
	})
}

func (s *Server) sqlSchema(w http.ResponseWriter, r *http.Request) {
	claims := auth.FromContext(r.Context())
	if claims == nil || claims.Role != auth.RoleAdmin {
		http.Error(w, "admin only", http.StatusForbidden)
		return
	}
	s.serveCached(w, r, "sql:schema", 60*time.Second, func() (any, error) {
		conn := s.store.Conn()
		// Pull tables + their engines.
		tableRows, err := conn.Query(r.Context(), `
			SELECT name, engine FROM system.tables
			WHERE database = currentDatabase()
			  AND name NOT LIKE '.inner%'
			ORDER BY name`)
		if err != nil {
			return nil, err
		}
		defer tableRows.Close()
		out := []SchemaTable{}
		byName := map[string]int{}
		for tableRows.Next() {
			var t SchemaTable
			if err := tableRows.Scan(&t.Table, &t.Engine); err != nil {
				return nil, err
			}
			byName[t.Table] = len(out)
			out = append(out, t)
		}
		// Pull every column in one shot, then bucket by table.
		colRows, err := conn.Query(r.Context(), `
			SELECT table, name, type
			FROM system.columns
			WHERE database = currentDatabase()
			  AND table NOT LIKE '.inner%'
			ORDER BY table, position`)
		if err != nil {
			return nil, err
		}
		defer colRows.Close()
		for colRows.Next() {
			var t, n, ty string
			if err := colRows.Scan(&t, &n, &ty); err != nil {
				return nil, err
			}
			if i, ok := byName[t]; ok {
				out[i].Columns = append(out[i].Columns, SchemaColumn{Name: n, Type: ty})
			}
		}
		return out, nil
	})
}
