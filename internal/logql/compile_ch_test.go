package logql

import (
	"reflect"
	"strings"
	"testing"
)

// compile_ch_test.go — v0.10.279. Sahte Target ile saf derleme testleri:
// her operatör her alan türünde (CLAUDE.md "her birimi test et" kuralının
// sözdizimi karşılığı). SQL şekli + bağlama sırası birlikte pinlenir —
// yanlış sıradaki bir `?` hata vermez, YANLIŞ SATIR döndürür.

type fakeTarget struct{}

func (fakeTarget) Resolve(f string) FieldRef {
	switch f {
	case "service":
		return FieldRef{Kind: FieldString, Expr: "service_name"}
	case "level":
		return FieldRef{Kind: FieldFold, Expr: "sev"}
	case "trace_id":
		return FieldRef{Kind: FieldID, Expr: "trace_id"}
	case "message":
		return FieldRef{Kind: FieldBody, Expr: "body"}
	case "severity_num":
		return FieldRef{Kind: FieldNumeric, Expr: "severity_num"}
	}
	return FieldRef{Kind: FieldString,
		Expr: "attr(?, ?)", Args: []any{f, f},
		ExistsExpr: "(has(attr_keys, ?) OR has(res_keys, ?))", ExistsArgs: []any{f, f}}
}

func (fakeTarget) IDColumns(term string) []string {
	t := strings.TrimSpace(term)
	if len(t) == 32 || len(t) == 16 {
		return []string{"trace_id", "span_id"}
	}
	return nil
}

func compile(t *testing.T, q string) (string, []any) {
	t.Helper()
	e, err := Parse(q)
	if err != nil {
		t.Fatalf("Parse(%q): %v", q, err)
	}
	return Compile(e, fakeTarget{})
}

func TestCompileCH(t *testing.T) {
	for _, tc := range []struct {
		name, q, sql string
		args         []any
	}{
		{"serbest metin", `timeout`, `multiSearchAnyCaseInsensitive(body, [?])`, []any{"timeout"}},
		{"iki terim örtük AND", `disk 90%`,
			`(multiSearchAnyCaseInsensitive(body, [?]) AND multiSearchAnyCaseInsensitive(body, [?]))`, []any{"disk", "90%"}},
		{"tırnaklı ifade tek iğne", `"connection refused: timeout"`,
			`multiSearchAnyCaseInsensitive(body, [?])`, []any{"connection refused: timeout"}},
		{"çıplak hex id kolon dalı", `4BF92F3577B34DA6A3CE929D0E0E4736`,
			`(multiSearchAnyCaseInsensitive(body, [?]) OR trace_id = ? OR span_id = ?)`,
			[]any{"4BF92F3577B34DA6A3CE929D0E0E4736", "4bf92f3577b34da6a3ce929d0e0e4736", "4bf92f3577b34da6a3ce929d0e0e4736"}},
		{"tırnaklı hex id kolon dalı YOK (literal)", `"4bf92f3577b34da6a3ce929d0e0e4736"`,
			`multiSearchAnyCaseInsensitive(body, [?])`, []any{"4bf92f3577b34da6a3ce929d0e0e4736"}},
		{"serbest joker", `Order*`, `ilike(body, ?)`, []any{"Order%"}},
		{"String alan eşitlik", `service:"checkout"`, `(service_name = ?)`, []any{"checkout"}},
		{"String alan joker", `service:check*`, `like(service_name, ?)`, []any{"check%"}},
		{"Fold alan harf-duyarsız", `level:error`, `(lower(sev) = lower(?))`, []any{"error"}},
		{"ID alan küçük harf", `trace_id:ABCDEF`, `(trace_id = ?)`, []any{"abcdef"}},
		{"Body alan", `message:"disk full"`, `multiSearchAnyCaseInsensitive(body, [?])`, []any{"disk full"}},
		{"Numeric eşitlik", `severity_num:17`, `(severity_num = ?)`, []any{float64(17)}},
		{"Numeric gte", `severity_num:>=17`, `(severity_num >= ?)`, []any{float64(17)}},
		{"attr lookup eşitlik — alan adı iki kez ÖNCE", `http.status:"500"`, `(attr(?, ?) = ?)`, []any{"http.status", "http.status", "500"}},
		{"attr sayısal karşılaştırma", `http.status:>=500`, `(toFloat64OrNull(attr(?, ?)) >= ?)`, []any{"http.status", "http.status", float64(500)}},
		{"attr sözlüksel karşılaştırma", `region:<"eu"`, `(attr(?, ?) < ?)`, []any{"region", "region", "eu"}},
		{"attr varlık", `_exists_:error.type`, `(has(attr_keys, ?) OR has(res_keys, ?))`, []any{"error.type", "error.type"}},
		{"kolon varlık", `_exists_:service`, `(service_name != '')`, nil},
		{"is-one-of", `channel:("MOBILE" OR "WEB")`, `((attr(?, ?) = ?) OR (attr(?, ?) = ?))`,
			[]any{"channel", "channel", "MOBILE", "channel", "channel", "WEB"}},
		{"NOT", `NOT level:debug`, `NOT (lower(sev) = lower(?))`, []any{"debug"}},
		{"aralık kapalı sayısal", `severity_num:[9 TO 12]`, `((severity_num >= ?) AND (severity_num <= ?))`, []any{float64(9), float64(12)}},
		{"aralık açık üst", `severity_num:{5 TO *]`, `((severity_num > ?))`, []any{float64(5)}},
		{"compileSearch tam ifade", `service:"db" AND NOT _exists_:trace_id AND (disk OR memory)`,
			`((service_name = ?) AND NOT (trace_id != '') AND (multiSearchAnyCaseInsensitive(body, [?]) OR multiSearchAnyCaseInsensitive(body, [?])))`,
			[]any{"db", "disk", "memory"}},
		{"joker deseninde gerçek % ve _ kaçışlı", `service:a_b%*`, `like(service_name, ?)`, []any{`a\_b\%%`}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			sql, args := compile(t, tc.q)
			if sql != tc.sql {
				t.Errorf("sql:\n got %s\nwant %s", sql, tc.sql)
			}
			if !reflect.DeepEqual(args, tc.args) && !(len(args) == 0 && len(tc.args) == 0) {
				t.Errorf("args: got %#v want %#v", args, tc.args)
			}
			if strings.Count(sql, "?") != len(args) {
				t.Errorf("`?` sayısı %d ≠ args %d — bağlama kayar", strings.Count(sql, "?"), len(args))
			}
		})
	}
	if sql, args := Compile(nil, fakeTarget{}); sql != "" || args != nil {
		t.Errorf("nil ifade → boş; got %q %v", sql, args)
	}
}
