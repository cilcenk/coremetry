package chstore

import (
	"os"
	"strings"
	"testing"

	"github.com/cilcenk/coremetry/internal/logql"
)

// log_query_compile_test.go — v0.10.279 (log-search audit B1).
//
// Üç şey pinlenir: (1) alan çözümü — ES expandShorthand'ın kısa adları CH'de
// hangi ifadeye bağlanıyor; (2) sözdizimi hatasında eski alt-dize yoluna
// düşüş; (3) ÜÇ CH okuma yolu da (liste/histogram/fieldstats) aynı derlenmiş
// yüklemi çağırıyor — saf çekirdek yeşil ama çağrıldığı yer pinli değilse
// kusur yerinde kalır (v0.9.1334 dersi).

func TestLogQueryTargetResolve(t *testing.T) {
	for _, tc := range []struct {
		field, wantExpr string
		kind            logql.FieldKind
		nargs           int
	}{
		{"service.name", "service_name", logql.FieldString, 0},
		{"Service", "service_name", logql.FieldString, 0},
		{"level", "severity_text", logql.FieldFold, 0},
		{"log.level", "severity_text", logql.FieldFold, 0},
		{"severity_num", "severity_num", logql.FieldNumeric, 0},
		{"trace_id", "trace_id", logql.FieldID, 0},
		{"span.id", "span_id", logql.FieldID, 0},
		{"message", "body", logql.FieldBody, 0},
		{"host.name", "host_name", logql.FieldString, 0},
		{"pod", "k8s.pod.name", logql.FieldString, 0},
		{"kubernetes.namespace_name", "service.namespace", logql.FieldString, 0}, // identity.go sözlüğü
		{"cluster", "openshift.cluster.name", logql.FieldString, 0},
		{"deployment.environment", "deployment.environment.name", logql.FieldString, 0},
		{"http.status_code", "attr_values[indexOf(attr_keys, ?)]", logql.FieldString, 2},
	} {
		ref := LogQueryTarget.Resolve(tc.field)
		if !strings.Contains(ref.Expr, tc.wantExpr) {
			t.Errorf("Resolve(%q).Expr = %q; %q içermeliydi", tc.field, ref.Expr, tc.wantExpr)
		}
		if ref.Kind != tc.kind {
			t.Errorf("Resolve(%q).Kind = %v; want %v", tc.field, ref.Kind, tc.kind)
		}
		if len(ref.Args) != tc.nargs {
			t.Errorf("Resolve(%q) args %d; want %d", tc.field, len(ref.Args), tc.nargs)
		}
	}
	// Bilinmeyen alan: attr önce, sonra res; varlık iki dizide.
	ref := LogQueryTarget.Resolve("error.type")
	if !strings.Contains(ref.ExistsExpr, "has(attr_keys, ?)") || !strings.Contains(ref.ExistsExpr, "has(res_keys, ?)") {
		t.Errorf("varlık yüklemi iki diziye bakmalı: %q", ref.ExistsExpr)
	}
	if cols := LogQueryTarget.IDColumns("4bf92f3577b34da6a3ce929d0e0e4736"); len(cols) != 2 {
		t.Errorf("çıplak hex id kolon dalları: %v", cols)
	}
	if cols := LogQueryTarget.IDColumns("timeout"); cols != nil {
		t.Errorf("düz metin kolon dalı taşımamalı: %v", cols)
	}
}

func TestLogSearchConjunctCompiles(t *testing.T) {
	expr, args := LogSearchConjunct(`service.name:"checkout" AND NOT level:debug "disk full"`)
	for _, want := range []string{"service_name = ?", "NOT (lower(", "multiSearchAnyCaseInsensitive(body, [?])"} {
		if !strings.Contains(expr, want) {
			t.Errorf("expr = %q; %q içermeliydi", expr, want)
		}
	}
	if len(args) != 3 || args[0] != "checkout" || args[1] != "debug" || args[2] != "disk full" {
		t.Errorf("args = %#v", args)
	}
	if strings.Count(expr, "?") != len(args) {
		t.Errorf("`?` sayısı %d ≠ args %d", strings.Count(expr, "?"), len(args))
	}
	// Boş metin → yüklem yok.
	if e, a := LogSearchConjunct("   "); e != "" || a != nil {
		t.Errorf("boş metin: %q %v", e, a)
	}
}

func TestLogSearchConjunctFallsBackOnSyntaxError(t *testing.T) {
	// Kapanmamış tırnak: operatör yazarken yarım — eski alt-dize yolu.
	expr, args := LogSearchConjunct(`"disk full`)
	if expr != "multiSearchAnyCaseInsensitive(body, [?])" || len(args) != 1 || args[0] != `"disk full` {
		t.Errorf("düşüş yolu: %q %v", expr, args)
	}
	if LogQuerySyntaxError(`"disk full`) == nil {
		t.Error("sözdizimi hatası yüzeye çıkmalı")
	}
	if LogQuerySyntaxError(`service.name:"x"`) != nil {
		t.Error("geçerli sorgu hata vermemeli")
	}
}

// TestAllThreeCHLogPathsUseTheCompiledPredicate — liste (chstore.logsWhere),
// histogram ve FieldStats (logstore/clickhouse.go) — hepsi LogSearchConjunct.
func TestAllThreeCHLogPathsUseTheCompiledPredicate(t *testing.T) {
	repo, err := os.ReadFile("repo.go")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(repo), "LogSearchConjunct(f.Search)") {
		t.Error("logsWhere derlenmiş yüklemi çağırmıyor")
	}
	ls, err := os.ReadFile("../logstore/clickhouse.go")
	if err != nil {
		t.Fatal(err)
	}
	src := string(ls)
	if n := strings.Count(src, "chstore.LogSearchConjunct(f.Search)"); n != 2 {
		t.Errorf("logstore/clickhouse.go'da derlenmiş yüklem %d yerde; histogram + FieldStats = 2 bekleniyor", n)
	}
	if strings.Contains(src, `wc += " AND multiSearchAnyCaseInsensitive(body, [?])"`) {
		t.Error("logstore/clickhouse.go hâlâ el yazımı gövde yüklemi taşıyor — üç yol ıraksar")
	}
}
