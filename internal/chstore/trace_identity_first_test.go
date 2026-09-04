package chstore

import (
	"strings"
	"testing"
	"time"
)

// trace_identity_first_test.go — v0.10.342 (operatör: "function_id benim için
// trace id gibi; arama kutusuna yazınca trace'i getirsin").

func TestIdentityToken(t *testing.T) {
	cases := map[string]string{
		"060203AFD10049A1B2":               "060203AFD10049A1B2",
		"  060203AFD10049A1B2  ":           "060203AFD10049A1B2",
		"POST /BSAWEB/mobile/login":        "", // boşluk: operasyon araması
		"short":                            "", // < 8
		"abc*defgh":                        "", // joker
		"ord-2026.09.04:17":                "ord-2026.09.04:17",
		"0fcd70a94ba1f695ea079750e71a7c10": "0fcd70a94ba1f695ea079750e71a7c10",
		"çağrı-kimliği-1":                  "", // ASCII dışı
	}
	for in, want := range cases {
		if got := identityToken(in); got != want {
			t.Errorf("identityToken(%q) = %q want %q", in, got, want)
		}
	}
	if !isHex32("0fcd70a94ba1f695ea079750e71a7c10") || isHex32("060203AFD10049A1B2") || isHex32(strings.Repeat("g", 32)) {
		t.Fatal("isHex32")
	}
}

func TestIdentityFirstEligible(t *testing.T) {
	if !identityFirstEligible(TraceFilter{Search: "060203AFD10049A1B2"}) {
		t.Fatal("kimlik terimi → uygun")
	}
	if identityFirstEligible(TraceFilter{Search: "POST /x"}) || identityFirstEligible(TraceFilter{}) {
		t.Fatal("operasyon araması / boş → uygun değil")
	}
	if identityFirstEligible(TraceFilter{Search: "060203AFD10049A1B2", TraceID: "x"}) ||
		identityFirstEligible(TraceFilter{Search: "060203AFD10049A1B2", CandidateIDs: []string{"a"}}) {
		t.Fatal("başka id daraltması varken uygun değil")
	}
}

func TestIdentityKeysAreSpanScopedPromotedAndFacets(t *testing.T) {
	keys := identityKeys()
	has := func(k string) bool {
		for _, x := range keys {
			if x == k {
				return true
			}
		}
		return false
	}
	for _, want := range []string{"function_id", "FUNCTION_ID", "channel_code", "function_code"} {
		if !has(want) {
			t.Fatalf("%q kimlik anahtarı olmalı: %v", want, keys)
		}
	}
	for _, no := range []string{"k8s.pod.name", "k8s.namespace.name", "container.image.name"} {
		if has(no) {
			t.Fatalf("resource kapsamı kimlik değil: %q", no)
		}
	}
	seen := map[string]bool{}
	for _, k := range keys {
		if seen[k] {
			t.Fatalf("tekrar: %q", k)
		}
		seen[k] = true
	}
}

func TestIdentityFirstQueryShape(t *testing.T) {
	withPromoted(t, "function_id", "attr_function_id")
	to := time.Date(2026, 9, 4, 12, 0, 0, 0, time.UTC)
	f := TraceFilter{Service: "svc", Search: "060203AFD10049A1B2", From: to.Add(-time.Hour), To: to,
		Filters: []FilterExpr{{Key: "channel_code", Op: "=", Values: []string{"060203"}}}, HasError: true}
	sql, args, ok := identityFirstQuery(f, []string{"function_id", "channel_code"}, "060203AFD10049A1B2", "", 100)
	if !ok {
		t.Fatal("sorgu üretilmeli")
	}
	for _, w := range []string{"SELECT trace_id, multiIf(", "attr_function_id = ?", "'function_id'", "indexOf(attr_keys, ?)", "'channel_code'",
		"service_name = ?", " OR ", "ORDER BY time DESC", "LIMIT ?", "max_execution_time = 10"} {
		if !strings.Contains(sql, w) {
			t.Fatalf("%q yok: %s", w, sql)
		}
	}
	// Çipler/arama/hata aday sorgusuna GİRMEZ (errorFirstFilter): tek daraltma pencere+servis+kimlik.
	if strings.Contains(sql, "attr_channel_code = ?") && strings.Count(sql, "channel_code") > 2 {
		t.Fatalf("çip aday sorgusuna sızmış: %s", sql)
	}
	if strings.Contains(sql, "status_code") || strings.Contains(sql, "multiSearch") {
		t.Fatalf("hata/arama yüklemi aday sorgusuna sızmış: %s", sql)
	}
	// args: SELECT (multiIf) → WHERE → LIMIT; son arg bütçe.
	if args[len(args)-1] != 100 {
		t.Fatalf("son arg bütçe olmalı: %v", args)
	}
	n := 0
	for _, a := range args {
		if a == "060203AFD10049A1B2" {
			n++
		}
	}
	if n < 4 { // 2 anahtar × (SELECT + WHERE)
		t.Fatalf("kimlik değeri her anahtar için SELECT ve WHERE'de bağlanmalı: %v", args)
	}
	if _, _, ok := identityFirstQuery(f, nil, "x", "", 1); ok {
		t.Fatal("anahtar yoksa sorgu yok")
	}
}
