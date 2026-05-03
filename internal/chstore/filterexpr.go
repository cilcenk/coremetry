package chstore

import (
	"fmt"
	"strings"
)

// FilterExpr is a single advanced filter clause: `key op value(s)`.
// `Key` may be a well-known column ("service.name", "http.method", …) or any
// custom attribute name — looked up in the attr_keys/attr_values arrays.
type FilterExpr struct {
	Key    string   `json:"k"`
	Op     string   `json:"op"`
	Values []string `json:"v"`
}

// Allowed operator set. Anything else is rejected at parse time.
var allowedOps = map[string]bool{
	"=": true, "!=": true,
	"LIKE": true, "NOT LIKE": true,
	"IN": true, "NOT IN": true,
	">": true, ">=": true, "<": true, "<=": true,
	"EXISTS": true, "NOT EXISTS": true,
}

// Map of well-known span attribute names to dedicated ClickHouse columns,
// so common queries skip the array lookup and stay index-friendly.
var wellKnown = map[string]string{
	"service.name":      "service_name",
	"service_name":      "service_name",
	"name":              "name",
	"operation":         "name",
	"kind":              "kind",
	"status_code":       "status_code",
	"status":            "status_code",
	"host.name":         "host_name",
	"deployment.environment": "deploy_env",
	"http.method":       "http_method",
	"http.route":        "http_route",
	"http.status_code":  "http_status",
	"db.system":         "db_system",
	"db.statement":      "db_statement",
	"rpc.system":        "rpc_system",
	"rpc.method":        "rpc_method",
	"peer.service":      "peer_service",
	"messaging.system":  "msg_system",
	"duration_ms":       "(duration / 1e6)",
}

// SQL builds the WHERE fragment + arg list for this filter, or returns
// (empty, nil, error) if the expression is malformed.
//
// For unknown attribute keys, the lookup expression `attr_values[indexOf(attr_keys, ?)]`
// pushes the key as a parameter. For numeric ops we cast to Float64 so that
// even string-stored attributes compare correctly when they parse.
func (f FilterExpr) SQL() (string, []any, error) {
	op := strings.ToUpper(strings.TrimSpace(f.Op))
	if op == "" {
		op = "="
	}
	if !allowedOps[op] {
		return "", nil, fmt.Errorf("invalid operator %q", op)
	}
	if f.Key == "" {
		return "", nil, fmt.Errorf("missing key")
	}

	// Resolve the LHS expression. Tempo-style prefixes pick the array:
	//   resource.X  →  res_values[indexOf(res_keys, 'X')]
	//   span.X      →  attr_values[indexOf(attr_keys, 'X')]
	//   X           →  well-known dedicated column if any, else span attr
	var lhs string
	var args []any
	switch {
	case strings.HasPrefix(f.Key, "resource."):
		name := strings.TrimPrefix(f.Key, "resource.")
		lhs = "res_values[indexOf(res_keys, ?)]"
		args = append(args, name)
	case strings.HasPrefix(f.Key, "span."):
		name := strings.TrimPrefix(f.Key, "span.")
		// Allow span.<known> to fall back to the dedicated column for indexing
		if col, ok := wellKnown[name]; ok {
			lhs = col
		} else {
			lhs = "attr_values[indexOf(attr_keys, ?)]"
			args = append(args, name)
		}
	default:
		if col, ok := wellKnown[f.Key]; ok {
			lhs = col
		} else {
			lhs = "attr_values[indexOf(attr_keys, ?)]"
			args = append(args, f.Key)
		}
	}

	// Numeric ops get a Float64 cast — `attr_values` is String, so this
	// turns "200" into 200.0 for comparison without breaking text columns.
	numericOp := op == ">" || op == ">=" || op == "<" || op == "<="

	switch op {
	case "EXISTS", "NOT EXISTS":
		neg := op == "NOT EXISTS"
		// Pick the right array (or use the dedicated column if well-known).
		var hasExpr string
		var hArgs []any
		switch {
		case strings.HasPrefix(f.Key, "resource."):
			name := strings.TrimPrefix(f.Key, "resource.")
			hasExpr = "has(res_keys, ?)"; hArgs = []any{name}
		case strings.HasPrefix(f.Key, "span."):
			name := strings.TrimPrefix(f.Key, "span.")
			if _, ok := wellKnown[name]; ok {
				hasExpr = "(" + wellKnown[name] + " != '')"
			} else {
				hasExpr = "has(attr_keys, ?)"; hArgs = []any{name}
			}
		default:
			if _, ok := wellKnown[f.Key]; ok {
				hasExpr = "(" + wellKnown[f.Key] + " != '')"
			} else {
				hasExpr = "has(attr_keys, ?)"; hArgs = []any{f.Key}
			}
		}
		if neg {
			hasExpr = "NOT " + hasExpr
		}
		return hasExpr, hArgs, nil

	case "IN", "NOT IN":
		if len(f.Values) == 0 {
			return "", nil, fmt.Errorf("op %s needs at least one value", op)
		}
		placeholders := strings.TrimRight(strings.Repeat("?,", len(f.Values)), ",")
		for _, v := range f.Values {
			args = append(args, v)
		}
		return lhs + " " + op + " (" + placeholders + ")", args, nil

	case "LIKE", "NOT LIKE":
		if len(f.Values) != 1 {
			return "", nil, fmt.Errorf("op %s needs exactly one value", op)
		}
		args = append(args, "%"+f.Values[0]+"%")
		return lhs + " " + op + " ?", args, nil

	default: // =, !=, comparison ops
		if len(f.Values) != 1 {
			return "", nil, fmt.Errorf("op %s needs exactly one value", op)
		}
		if numericOp {
			// Cast to Float64. accurateCastOrNull is type-tolerant and handles
			// both String (attribute lookups) and numeric (well-known) columns.
			args = append(args, f.Values[0])
			return "accurateCastOrNull(" + lhs + ", 'Float64') " + op +
				" accurateCastOrNull(?, 'Float64')", args, nil
		}
		args = append(args, f.Values[0])
		return lhs + " " + op + " ?", args, nil
	}
}

// ApplyFilters appends each filter as a separate WHERE conjunct, skipping
// (and logging) any that fail to compile.
func ApplyFilters(wc *whereClause, filters []FilterExpr) {
	for _, f := range filters {
		sql, args, err := f.SQL()
		if err != nil || sql == "" {
			continue // silently skip — UI validates first
		}
		wc.add(sql, args...)
	}
}
