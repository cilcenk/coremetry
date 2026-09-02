package api

import (
	"os"
	"strings"
	"testing"
)

// logs_query_syntax_test.go — v0.10.280: karar tablosu + iki handler'ın
// da kapıdan geçtiğinin kaynak pini (saf çekirdek yeşil ama çağrılmıyorsa
// kusur yerinde kalır — v0.9.1334 dersi).

func TestLogQuerySyntaxReject(t *testing.T) {
	for _, tc := range []struct {
		name, backend, search string
		reject                bool
	}{
		{"CH geçerli alan yazımı", "clickhouse", `service.name:"x" AND level:error`, false},
		{"CH düz metin", "clickhouse", `timeout`, false},
		{"CH boş", "clickhouse", `   `, false},
		{"CH kapanmamış tırnak", "clickhouse", `"disk`, true},
		{"CH dengesiz parantez", "clickhouse", `(a OR b`, true},
		{"CH sonda AND", "clickhouse", `a AND`, true},
		{"ES aynı hata REDDEDİLMEZ (query_string sahibi)", "elasticsearch", `(a OR b`, false},
		{"bilinmeyen arka uç", "unknown", `(a OR b`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			msg, got := logQuerySyntaxReject(tc.backend, tc.search)
			if got != tc.reject {
				t.Errorf("reject = %v; want %v (msg %q)", got, tc.reject, msg)
			}
			if got && !strings.Contains(msg, "konum") {
				t.Errorf("mesaj konum taşımalı: %q", msg)
			}
		})
	}
}

func TestBothLogHandlersRejectSyntax(t *testing.T) {
	b, err := os.ReadFile("api_logs.go")
	if err != nil {
		t.Fatal(err)
	}
	if n := strings.Count(string(b), "s.rejectLogQuerySyntax(w, f.Search)"); n != 2 {
		t.Errorf("rejectLogQuerySyntax %d yerde; serveLogsSearch + getLogsTimeseries = 2 bekleniyor", n)
	}
}
