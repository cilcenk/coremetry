package chstore

import (
	"strings"
	"testing"
	"unicode/utf8"
)

// v0.9.1115 regresyon — Operator-reported (prod, 2026-08-16):
// worker loglarında "traces export: rpc error: … string field
// contains invalid UTF-8". truncStmt'in bayt kesimi (s[:1024]) çok
// baytlı karakterin ortasına denk gelince db.statement span
// attribute'u geçersiz UTF-8 taşıyor ve protobuf marshal O TURUN
// TÜM self-telemetri batch'ini düşürüyordu. v0.9.586 yalnız
// hata-mesajı yolunu (SafeAttr) korumuştu; bu test statement
// yolunu kilitler.
func TestTruncStmtUTF8Safe(t *testing.T) {
	cases := []struct {
		name string
		in   string
	}{
		{
			// 'ı' (c4 b1) tam 1024. bayt sınırını ortalar: kesim c4'te.
			"çok baytlı karakter kesim sınırında",
			strings.Repeat("a", maxStmtBytes-1) + "ığüşöç -- rejim kayması",
		},
		{
			// Sınırın hemen içinde biten çok baytlı — kesim güvenli olmalı.
			"sınır içinde Türkçe",
			strings.Repeat("b", maxStmtBytes-2) + "şşşş",
		},
		{"uzun ASCII", strings.Repeat("SELECT 1;", 200)},
		{"kaynağı zaten bozuk kısa string", "SELECT '\xff\xfe' AS x"},
		{"kısa geçerli aynen döner", "  SELECT 1  "},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := truncStmt(c.in)
			if !utf8.ValidString(got) {
				t.Fatalf("truncStmt geçersiz UTF-8 döndürdü: %q", got)
			}
			if len(got) > maxStmtBytes+len("…") {
				t.Errorf("kesim tavanı aşıldı: %d bayt", len(got))
			}
		})
	}
	if got := truncStmt("  SELECT 1  "); got != "SELECT 1" {
		t.Errorf("kısa geçerli string birebir dönmeli, geldi: %q", got)
	}
}
