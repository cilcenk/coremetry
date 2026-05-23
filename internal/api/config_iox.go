package api

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"reflect"
	"strconv"
	"strings"
	"time"

	"github.com/cilcenk/coremetry/internal/auth"
)

// configTables is the ordered catalogue of operator-set state we
// support exporting/importing.
//
// Excluded by design:
//   - users (carries password hashes; needs a dedicated migration
//     path that the admin can review per-row, not a bulk JSON dump)
//   - spans / logs / metric_points / problems / incidents /
//     anomaly_events / monitor_results / trace_snapshots /
//     ai_calls / audit_log / log_templates / *_5m — these are
//     observability data, not config; they're recomputed from
//     ingest and don't survive a clean install anyway
//   - exception_groups — has runtime triage state (occurrences,
//     last_seen) interleaved with operator state; a fresh install
//     rebuilds these from spans, not from an import file
var configTables = []string{
	"system_settings",
	"service_metadata",
	"notification_channels",
	"alert_rules",
	"dashboards",
	"saved_views",
	"slos",
	"maintenance_windows",
	"anomaly_silences",
	"monitors",
	"service_contracts",
	"status_page_config",
	"status_page_components",
	"status_page_subscribers",
}

type configExportPayload struct {
	Format           string                 `json:"format"`
	Version          int                    `json:"version"`
	ExportedAt       string                 `json:"exportedAt"`
	CoremetryVersion string                 `json:"coremetryVersion,omitempty"`
	Tables           map[string]tableExport `json:"tables"`
}

type tableExport struct {
	Columns []string                 `json:"columns"`
	Rows    []map[string]interface{} `json:"rows"`
}

// exportConfig dumps every configTables row as JSON. The payload is
// streamed as a download (Content-Disposition: attachment) so the
// operator's browser saves it as a file rather than rendering it.
// FINAL on every SELECT — these are all ReplacingMergeTree(version);
// we want the latest visible state, not the merge history.
func (s *Server) exportConfig(w http.ResponseWriter, r *http.Request) {
	claims := auth.FromContext(r.Context())
	if claims == nil || claims.Role != auth.RoleAdmin {
		http.Error(w, "admin only", http.StatusForbidden)
		return
	}

	out := configExportPayload{
		Format:           "coremetry.config",
		Version:          1,
		ExportedAt:       time.Now().UTC().Format(time.RFC3339),
		CoremetryVersion: strings.TrimSpace(os.Getenv("COREMETRY_VERSION")),
		Tables:           make(map[string]tableExport, len(configTables)),
	}

	totalRows := 0
	for _, t := range configTables {
		cols, rows, err := s.dumpConfigTable(r.Context(), t)
		if err != nil {
			http.Error(w, fmt.Sprintf("dump %s: %v", t, err), http.StatusInternalServerError)
			return
		}
		out.Tables[t] = tableExport{Columns: cols, Rows: rows}
		totalRows += len(rows)
	}

	fname := fmt.Sprintf("coremetry-config-%s.json", time.Now().UTC().Format("20060102-150405"))
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Content-Disposition", `attachment; filename="`+fname+`"`)

	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	if err := enc.Encode(out); err != nil {
		s.audit(r, "config.export.failed", "config", "", fmt.Sprintf(`{"error":%q}`, err.Error()))
		return
	}
	s.audit(r, "config.export", "config", "",
		fmt.Sprintf(`{"tables":%d,"rows":%d}`, len(out.Tables), totalRows))
}

// importConfig parses a JSON payload (produced by exportConfig) and
// replays each table's rows back into ClickHouse.
//
// Semantics:
//   - Each row is inserted with its ORIGINAL version column intact.
//     ReplacingMergeTree(version) then picks the highest version per
//     ORDER BY key:
//       • fresh install   → all imported rows appear
//       • local edit LATER than imported  → local wins (merge)
//       • local edit EARLIER than imported → imported wins
//   - ?mode=replace bumps every imported row's version to now() so
//     the imported state always wins regardless of local edits.
//     Opt-in confirms the operator wants to overwrite drift.
//   - Unknown tables in the payload are skipped (forward-compat).
//   - Empty tables are skipped silently (no-op).
//
// We do NOT truncate target tables — operators who really want a
// blank slate should COREMETRY_CH_RESET_SCHEMA=1 and reboot.
func (s *Server) importConfig(w http.ResponseWriter, r *http.Request) {
	claims := auth.FromContext(r.Context())
	if claims == nil || claims.Role != auth.RoleAdmin {
		http.Error(w, "admin only", http.StatusForbidden)
		return
	}

	mode := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("mode")))
	if mode == "" {
		mode = "merge"
	}
	if mode != "merge" && mode != "replace" {
		http.Error(w, "mode must be merge or replace", http.StatusBadRequest)
		return
	}

	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 64<<20))
	if err != nil {
		http.Error(w, "read body: "+err.Error(), http.StatusBadRequest)
		return
	}
	dec := json.NewDecoder(strings.NewReader(string(body)))
	// UseNumber preserves UInt64 version columns (>2^53 nanos) across
	// the round-trip without lossy float coercion.
	dec.UseNumber()
	var payload configExportPayload
	if err := dec.Decode(&payload); err != nil {
		http.Error(w, "invalid JSON: "+err.Error(), http.StatusBadRequest)
		return
	}
	if payload.Format != "coremetry.config" {
		http.Error(w, "not a coremetry.config export", http.StatusBadRequest)
		return
	}
	if payload.Version < 1 || payload.Version > 1 {
		http.Error(w, fmt.Sprintf("unsupported export version %d", payload.Version), http.StatusBadRequest)
		return
	}

	result := map[string]any{
		"mode":   mode,
		"tables": map[string]int{},
	}
	totalRows := 0
	skipped := []string{}

	for _, t := range configTables {
		te, ok := payload.Tables[t]
		if !ok || len(te.Rows) == 0 {
			continue
		}
		n, err := s.loadConfigTable(r.Context(), t, te, mode == "replace")
		if err != nil {
			http.Error(w, fmt.Sprintf("import %s: %v", t, err), http.StatusInternalServerError)
			return
		}
		result["tables"].(map[string]int)[t] = n
		totalRows += n
	}

	for k := range payload.Tables {
		known := false
		for _, t := range configTables {
			if t == k {
				known = true
				break
			}
		}
		if !known {
			skipped = append(skipped, k)
		}
	}
	if len(skipped) > 0 {
		result["skippedUnknown"] = skipped
	}

	// Settings hot-reload: every config service whose backing
	// store lives in system_settings hydrates on boot via
	// LoadPersisted. After an import we have to fan that out so
	// the live process picks up the new values without a restart.
	// publishConfigReload covers multi-pod via Pub/Sub; the
	// in-process subscriber wired by reloadConfigOnSignal picks
	// up the local-pod broadcast too.
	for _, svc := range []string{"copilot", "ldap", "tempo", "sampling", "pipeline", "kibana"} {
		s.publishConfigReload(r.Context(), svc)
	}

	s.audit(r, "config.import", "config", "",
		fmt.Sprintf(`{"mode":%q,"tables":%d,"rows":%d}`, mode, len(result["tables"].(map[string]int)), totalRows))
	result["rows"] = totalRows
	writeJSON(w, result)
}

// dumpConfigTable runs `SELECT * FROM <t> FINAL` and returns
// (column names, rows-as-maps). Column types come from the driver's
// ScanType() so we don't need a hand-coded struct per table.
//
// Big-int safety: uint64/int64 are JSON-encoded as numeric tokens
// (no precision loss in the wire format itself). On import we
// re-parse via json.Number, so version columns (~10^18 nanos)
// round-trip past JavaScript's 2^53 safe-integer ceiling — matters
// because the operator may inspect the JSON in a browser between
// export and import.
func (s *Server) dumpConfigTable(ctx context.Context, table string) ([]string, []map[string]interface{}, error) {
	conn := s.store.Conn()
	q := fmt.Sprintf("SELECT * FROM `%s` FINAL", table)
	rows, err := conn.Query(ctx, q)
	if err != nil {
		return nil, nil, err
	}
	defer rows.Close()

	cts := rows.ColumnTypes()
	cols := make([]string, len(cts))
	scanTypes := make([]reflect.Type, len(cts))
	for i, c := range cts {
		cols[i] = c.Name()
		scanTypes[i] = c.ScanType()
		if scanTypes[i] == nil {
			scanTypes[i] = reflect.TypeOf("")
		}
	}

	out := make([]map[string]interface{}, 0, 64)
	for rows.Next() {
		ptrs := make([]any, len(cts))
		dests := make([]reflect.Value, len(cts))
		for i := range cts {
			v := reflect.New(scanTypes[i])
			ptrs[i] = v.Interface()
			dests[i] = v.Elem()
		}
		if err := rows.Scan(ptrs...); err != nil {
			return nil, nil, err
		}
		row := make(map[string]interface{}, len(cts))
		for i := range cts {
			row[cols[i]] = jsonifyConfigValue(dests[i].Interface())
		}
		out = append(out, row)
	}
	if err := rows.Err(); err != nil {
		return nil, nil, err
	}
	return cols, out, nil
}

// jsonifyConfigValue normalises a CH driver scan output into a value
// the default JSON encoder can serialise losslessly. Unlike
// sql_playground's helper, this one keeps numeric types as their
// native Go ints / uints — encoding/json emits them as digit
// tokens (no float coercion), which preserves UInt64 precision.
func jsonifyConfigValue(v any) any {
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
	}
	return v
}

// loadConfigTable inserts `te.Rows` into `table` using a positional
// batch. Column types come from system.columns so we can coerce
// JSON's untyped scalars back to driver-expected Go types per
// column.
//
// bumpVersion=true (mode=replace) rewrites every row's `version`
// column to now() in nanos — imported rows always win the
// ReplacingMergeTree dedup against any locally edited rows.
func (s *Server) loadConfigTable(ctx context.Context, table string, te tableExport, bumpVersion bool) (int, error) {
	if len(te.Rows) == 0 {
		return 0, nil
	}
	conn := s.store.Conn()

	// Look up the canonical column ordering + types via
	// system.columns so we never depend on a hand-coded per-table
	// spec that would drift the moment someone adds an ALTER ADD
	// COLUMN to store.go.
	colQuery := `
		SELECT name, type
		FROM system.columns
		WHERE database = currentDatabase() AND table = ?
		ORDER BY position`
	crows, err := conn.Query(ctx, colQuery, table)
	if err != nil {
		return 0, fmt.Errorf("describe %s: %w", table, err)
	}
	type colInfo struct {
		name string
		typ  string
	}
	colInfos := []colInfo{}
	for crows.Next() {
		var ci colInfo
		if err := crows.Scan(&ci.name, &ci.typ); err != nil {
			crows.Close()
			return 0, err
		}
		colInfos = append(colInfos, ci)
	}
	crows.Close()
	if len(colInfos) == 0 {
		return 0, fmt.Errorf("table %s has no columns (does it exist?)", table)
	}

	colNames := make([]string, len(colInfos))
	for i, ci := range colInfos {
		colNames[i] = "`" + ci.name + "`"
	}
	insertSQL := fmt.Sprintf("INSERT INTO `%s` (%s)", table, strings.Join(colNames, ", "))
	batch, err := conn.PrepareBatch(ctx, insertSQL)
	if err != nil {
		return 0, fmt.Errorf("prepare %s: %w", table, err)
	}

	now := uint64(time.Now().UnixNano())
	for ri, row := range te.Rows {
		vals := make([]any, len(colInfos))
		for i, ci := range colInfos {
			raw, present := row[ci.name]
			if !present {
				// New column the export file pre-dates — fill with
				// the column's typed zero so the batch row stays
				// positionally aligned.
				vals[i] = zeroForCHType(ci.typ)
				continue
			}
			if bumpVersion && ci.name == "version" {
				vals[i] = now
				continue
			}
			cv, err := coerceForCHType(raw, ci.typ)
			if err != nil {
				return 0, fmt.Errorf("row %d col %s: %w", ri, ci.name, err)
			}
			vals[i] = cv
		}
		if err := batch.Append(vals...); err != nil {
			return 0, fmt.Errorf("append row %d: %w", ri, err)
		}
	}
	if err := batch.Send(); err != nil {
		return 0, fmt.Errorf("send %s: %w", table, err)
	}
	return len(te.Rows), nil
}

// coerceForCHType converts a JSON-decoded value into the Go type
// clickhouse-go expects for a column declared as `chType`.
// LowCardinality(...) and Nullable(...) wrappers are stripped — the
// underlying scan type matches the inner type for both.
func coerceForCHType(v any, chType string) (any, error) {
	t := strings.TrimSpace(chType)
	nullable := false
	for {
		switch {
		case strings.HasPrefix(t, "LowCardinality(") && strings.HasSuffix(t, ")"):
			t = t[len("LowCardinality(") : len(t)-1]
		case strings.HasPrefix(t, "Nullable(") && strings.HasSuffix(t, ")"):
			t = t[len("Nullable(") : len(t)-1]
			nullable = true
		default:
			goto done
		}
	}
done:
	t = strings.TrimSpace(t)

	if v == nil {
		if nullable {
			return nil, nil
		}
		return zeroForCHType(chType), nil
	}

	// Array(<inner>) — JSON []any → concrete []T.
	if strings.HasPrefix(t, "Array(") && strings.HasSuffix(t, ")") {
		inner := strings.TrimSpace(t[len("Array(") : len(t)-1])
		arr, ok := v.([]any)
		if !ok {
			return nil, fmt.Errorf("expected array for %s, got %T", chType, v)
		}
		switch inner {
		case "String", "LowCardinality(String)":
			out := make([]string, len(arr))
			for i, e := range arr {
				if s, ok := e.(string); ok {
					out[i] = s
				} else {
					out[i] = fmt.Sprintf("%v", e)
				}
			}
			return out, nil
		case "UInt64":
			out := make([]uint64, len(arr))
			for i, e := range arr {
				out[i] = toUint64(e)
			}
			return out, nil
		case "Float64":
			out := make([]float64, len(arr))
			for i, e := range arr {
				out[i] = toFloat64(e)
			}
			return out, nil
		}
		return arr, nil
	}

	if t == "String" {
		if s, ok := v.(string); ok {
			return s, nil
		}
		return fmt.Sprintf("%v", v), nil
	}
	if strings.HasPrefix(t, "FixedString") || strings.HasPrefix(t, "Enum") || t == "UUID" {
		if s, ok := v.(string); ok {
			return s, nil
		}
		return fmt.Sprintf("%v", v), nil
	}

	if strings.HasPrefix(t, "DateTime") || strings.HasPrefix(t, "Date") {
		s, ok := v.(string)
		if !ok {
			return nil, fmt.Errorf("expected RFC3339 string for %s, got %T", chType, v)
		}
		if tt, err := time.Parse(time.RFC3339Nano, s); err == nil {
			return tt, nil
		}
		if tt, err := time.Parse(time.RFC3339, s); err == nil {
			return tt, nil
		}
		return nil, fmt.Errorf("parse time %q: bad layout", s)
	}

	switch t {
	case "UInt8":
		return uint8(toUint64(v)), nil
	case "UInt16":
		return uint16(toUint64(v)), nil
	case "UInt32":
		return uint32(toUint64(v)), nil
	case "UInt64":
		return toUint64(v), nil
	case "Int8":
		return int8(toInt64(v)), nil
	case "Int16":
		return int16(toInt64(v)), nil
	case "Int32":
		return int32(toInt64(v)), nil
	case "Int64":
		return toInt64(v), nil
	case "Float32":
		return float32(toFloat64(v)), nil
	case "Float64":
		return toFloat64(v), nil
	case "Bool":
		if b, ok := v.(bool); ok {
			return b, nil
		}
		return toUint64(v) != 0, nil
	}

	if s, ok := v.(string); ok {
		return s, nil
	}
	return v, nil
}

// zeroForCHType returns a typed zero matching the column's CH scan
// type — used when an imported file is missing a column the target
// schema added later.
func zeroForCHType(chType string) any {
	t := strings.TrimSpace(chType)
	for {
		switch {
		case strings.HasPrefix(t, "LowCardinality(") && strings.HasSuffix(t, ")"):
			t = t[len("LowCardinality(") : len(t)-1]
		case strings.HasPrefix(t, "Nullable(") && strings.HasSuffix(t, ")"):
			t = t[len("Nullable(") : len(t)-1]
		default:
			goto done
		}
	}
done:
	if strings.HasPrefix(t, "Array(") {
		return []string{}
	}
	switch {
	case t == "String", strings.HasPrefix(t, "Enum"), strings.HasPrefix(t, "FixedString"), t == "UUID":
		return ""
	case t == "UInt8":
		return uint8(0)
	case t == "UInt16":
		return uint16(0)
	case t == "UInt32":
		return uint32(0)
	case t == "UInt64":
		return uint64(0)
	case t == "Int8":
		return int8(0)
	case t == "Int16":
		return int16(0)
	case t == "Int32":
		return int32(0)
	case t == "Int64":
		return int64(0)
	case t == "Float32":
		return float32(0)
	case t == "Float64":
		return float64(0)
	case t == "Bool":
		return false
	case strings.HasPrefix(t, "DateTime"), strings.HasPrefix(t, "Date"):
		return time.Time{}
	}
	return ""
}

func toUint64(v any) uint64 {
	switch x := v.(type) {
	case uint64:
		return x
	case int64:
		if x < 0 {
			return 0
		}
		return uint64(x)
	case float64:
		return uint64(x)
	case json.Number:
		if u, err := strconv.ParseUint(x.String(), 10, 64); err == nil {
			return u
		}
		if i, err := x.Int64(); err == nil && i >= 0 {
			return uint64(i)
		}
	case string:
		if u, err := strconv.ParseUint(x, 10, 64); err == nil {
			return u
		}
	case bool:
		if x {
			return 1
		}
	}
	return 0
}

func toInt64(v any) int64 {
	switch x := v.(type) {
	case int64:
		return x
	case uint64:
		return int64(x)
	case float64:
		return int64(x)
	case json.Number:
		if i, err := x.Int64(); err == nil {
			return i
		}
	case string:
		if i, err := strconv.ParseInt(x, 10, 64); err == nil {
			return i
		}
	case bool:
		if x {
			return 1
		}
	}
	return 0
}

func toFloat64(v any) float64 {
	switch x := v.(type) {
	case float64:
		return x
	case int64:
		return float64(x)
	case uint64:
		return float64(x)
	case json.Number:
		if f, err := x.Float64(); err == nil {
			return f
		}
	case string:
		if f, err := strconv.ParseFloat(x, 64); err == nil {
			return f
		}
	}
	return 0
}
