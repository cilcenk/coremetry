package chstore

import (
	"strings"
	"testing"
)

// filterexpr_kvh_test.go — v0.10.300 (trace arama Dilim 1b): dizi yoluna
// düşen `=` / `IN` yüklemleri bloom'un kullanabildiği hash yüklemini ÖNE
// alır, kesin eşitlik KALIR; bağlama sırası SQL'deki `?` sırasıyla birebir
// (yanlış sıra hata vermez, YANLIŞ SATIR döndürür). Terfi/bilinen kolon,
// negasyon, regex, aralık, metric_points ve indeks-yok hâli aynen.

func withAttrIndex(t *testing.T, on bool) {
	t.Helper()
	prev := AttrIndexAvailable()
	registerAttrIndex(on)
	t.Cleanup(func() { registerAttrIndex(prev) })
}

func TestKVHEqualityPredicate(t *testing.T) {
	withAttrIndex(t, true)
	f := FilterExpr{Key: "banking.txn_ref", Op: "=", Values: []string{"TXN-1"}}
	sql, args, err := f.SQL()
	if err != nil {
		t.Fatal(err)
	}
	want := `(has(attr_kvh, cityHash64(concat(?, '\x1F', ?))) AND attr_values[indexOf(attr_keys, ?)] = ?)`
	if sql != want {
		t.Errorf("sql:\n got %s\nwant %s", sql, want)
	}
	if len(args) != 4 || args[0] != "banking.txn_ref" || args[1] != "TXN-1" || args[2] != "banking.txn_ref" || args[3] != "TXN-1" {
		t.Errorf("args %v", args)
	}
	if strings.Count(sql, "?") != len(args) {
		t.Errorf("`?` %d ≠ args %d", strings.Count(sql, "?"), len(args))
	}
	// resource.* → res_kvh + res dizileri
	r := FilterExpr{Key: "resource.k8s.pod.uid", Op: "=", Values: []string{"u1"}}
	sql, args, _ = r.SQL()
	if !strings.HasPrefix(sql, "(has(res_kvh, ") || !strings.Contains(sql, "res_values[indexOf(res_keys, ?)] = ?") {
		t.Errorf("resource yolu: %s", sql)
	}
	if args[0] != "k8s.pod.uid" || args[2] != "k8s.pod.uid" {
		t.Errorf("resource anahtarı öneksiz bağlanmalı: %v", args)
	}
	// span.* öneki (bilinen kolon DEĞİL — http.route → http_route kolonuna gider, kvh yok)
	sp := FilterExpr{Key: "span.custom.flag", Op: "=", Values: []string{"/x"}}
	sql, _, _ = sp.SQL()
	if !strings.HasPrefix(sql, "(has(attr_kvh, ") {
		t.Errorf("span. öneki: %s", sql)
	}
}

func TestKVHInPredicate(t *testing.T) {
	withAttrIndex(t, true)
	f := FilterExpr{Key: "channel", Op: "IN", Values: []string{"MOBILE", "WEB"}}
	sql, args, err := f.SQL()
	if err != nil {
		t.Fatal(err)
	}
	want := `(hasAny(attr_kvh, [cityHash64(concat(?, '\x1F', ?)), cityHash64(concat(?, '\x1F', ?))]) AND attr_values[indexOf(attr_keys, ?)] IN (?,?))`
	if sql != want {
		t.Errorf("sql:\n got %s\nwant %s", sql, want)
	}
	exp := []any{"channel", "MOBILE", "channel", "WEB", "channel", "MOBILE", "WEB"}
	if len(args) != len(exp) {
		t.Fatalf("args %v", args)
	}
	for i := range exp {
		if args[i] != exp[i] {
			t.Errorf("args[%d] = %v; want %v", i, args[i], exp[i])
		}
	}
	if strings.Count(sql, "?") != len(args) {
		t.Errorf("`?` %d ≠ args %d", strings.Count(sql, "?"), len(args))
	}
}

func TestKVHNotAppliedElsewhere(t *testing.T) {
	withAttrIndex(t, true)
	for _, f := range []FilterExpr{
		{Key: "channel", Op: "!=", Values: []string{"x"}},
		{Key: "channel", Op: "NOT IN", Values: []string{"x"}},
		{Key: "channel", Op: "=~", Values: []string{"a.*"}},
		{Key: "channel", Op: "LIKE", Values: []string{"a"}},
		{Key: "http.status_code", Op: ">=", Values: []string{"500"}},
		{Key: "channel", Op: "EXISTS"},
		{Key: "service.name", Op: "=", Values: []string{"api"}}, // bilinen kolon
	} {
		sql, _, err := f.SQL()
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(sql, "kvh") {
			t.Errorf("%s %s: bloom yolu YALNIZ =/IN ve dizi yolunda: %s", f.Key, f.Op, sql)
		}
	}
	// Terfi kolonu: kolona yönlenir, kvh yok.
	withPromoted(t, "channel_code", "attr_channel_code")
	sql, _, _ := FilterExpr{Key: "channel_code", Op: "=", Values: []string{"030101"}}.SQL()
	if strings.Contains(sql, "kvh") || !strings.Contains(sql, "attr_channel_code") {
		t.Errorf("terfi kolonu: %s", sql)
	}
	// metric_points: kvh kolonu yok → asla.
	msql, _, _ := FilterExpr{Key: "pod", Op: "=", Values: []string{"p"}}.SQLForMetricPoints()
	if strings.Contains(msql, "kvh") {
		t.Errorf("metric_points yolu kvh üretmemeli: %s", msql)
	}
}

func TestKVHOffKeepsLegacyShape(t *testing.T) {
	withAttrIndex(t, false)
	sql, args, _ := FilterExpr{Key: "banking.txn_ref", Op: "=", Values: []string{"TXN-1"}}.SQL()
	if sql != "attr_values[indexOf(attr_keys, ?)] = ?" || len(args) != 2 {
		t.Errorf("indeks yokken eski şekil: %s %v", sql, args)
	}
}

func TestKVHAliasQualified(t *testing.T) {
	withAttrIndex(t, true)
	sql, _, _ := FilterExpr{Key: "k", Op: "=", Values: []string{"v"}}.SQLAliased("s")
	if !strings.Contains(sql, "has(s.attr_kvh, ") || !strings.Contains(sql, "s.attr_values[indexOf(s.attr_keys, ?)]") {
		t.Errorf("alias: %s", sql)
	}
	used, ready := AttrIndexStats()
	if !ready || used == 0 {
		t.Errorf("stats used=%d ready=%v", used, ready)
	}
}
