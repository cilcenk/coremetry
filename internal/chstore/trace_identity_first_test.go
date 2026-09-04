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

func TestIdentityKeysIndexedOnlyAndDeduped(t *testing.T) {
	// v0.10.343 — Operator-reported (prod 342): OR içindeki dizi-yolu yazımları
	// indeksi öldürüp zaman aşımına düşürüyordu. Yalnız indeksli yüklemler
	// kullanılır; aynı kolona derlenen yazımlar tek sorgu.
	prevIdx := attrIndexReady.Load()
	attrIndexReady.Store(false)
	t.Cleanup(func() { attrIndexReady.Store(prevIdx) })
	withPromoted(t, "function_id", "attr_function_id")
	keys := identityKeys("060203AFD10049A1B2")
	var indexed, skipped []string
	for _, k := range keys {
		if k.indexed {
			indexed = append(indexed, k.key)
		} else {
			skipped = append(skipped, k.key)
		}
	}
	if len(indexed) != 1 || indexed[0] != "function_id" {
		t.Fatalf("kvh yokken yalnız terfi haritasındaki yazım indeksli: %v (atlanan %v)", indexed, skipped)
	}
	for _, k := range keys {
		if k.key == "function_id" && !strings.Contains(k.sql, "attr_function_id = ?") {
			t.Fatalf("terfi yüklemi: %s", k.sql)
		}
		if k.key == "FUNCTION_ID" && !strings.Contains(k.sql, "indexOf(attr_keys") {
			t.Fatalf("haritasız yazım dizi yolu: %s", k.sql)
		}
		if strings.HasPrefix(k.key, "k8s.") {
			t.Fatalf("resource kapsamı kimlik değil: %q", k.key)
		}
	}
	// Aynı kolona derlenen iki yazım tek yüklem: harita ikisini de kaydettiyse.
	registerTraceAttrMaterialized(map[string]string{"FUNCTION_ID": "attr_function_id"})
	n := 0
	for _, k := range identityKeys("x") {
		if strings.Contains(k.sql, "attr_function_id") {
			n++
		}
	}
	if n != 1 {
		t.Fatalf("aynı kolon tek sorgu olmalı, %d", n)
	}
	// kvh varken haritasız yazımlar da indeksli (bloom).
	attrIndexReady.Store(true)
	for _, k := range identityKeys("x") {
		if !k.indexed {
			t.Fatalf("kvh açıkken her yazım indeksli olmalı: %+v", k)
		}
	}
}

func TestIdentityFirstQueryShape(t *testing.T) {
	withPromoted(t, "function_id", "attr_function_id")
	to := time.Date(2026, 9, 4, 12, 0, 0, 0, time.UTC)
	f := TraceFilter{Service: "svc", Search: "060203AFD10049A1B2", From: to.Add(-time.Hour), To: to,
		Filters: []FilterExpr{{Key: "channel_code", Op: "=", Values: []string{"060203"}}}, HasError: true}
	var fk identityKey
	for _, k := range identityKeys("060203AFD10049A1B2") {
		if k.key == "function_id" {
			fk = k
		}
	}
	sql, args := identityFirstQuery(f, fk, "", 100, f.From, f.To)
	for _, w := range []string{"SELECT trace_id", "attr_function_id = ?", "service_name = ?", "ORDER BY time DESC", "LIMIT ?", "max_execution_time = 5"} {
		if !strings.Contains(sql, w) {
			t.Fatalf("%q yok: %s", w, sql)
		}
	}
	// Çip/arama/hata aday sorgusuna GİRMEZ: tek daraltma pencere+servis+kimlik.
	if strings.Contains(sql, "attr_channel_code") || strings.Contains(sql, "status_code") || strings.Contains(sql, "multiSearch") || strings.Contains(sql, " OR ") {
		t.Fatalf("yabancı yüklem sızmış: %s", sql)
	}
	if args[len(args)-1] != 100 || args[len(args)-2] != "060203AFD10049A1B2" {
		t.Fatalf("args: WHERE → kimlik değeri → bütçe: %v", args)
	}
}

// v0.10.344 — kimlik değerindeki zaman çapa (operatör: function_id
// "2026 09 04 153319" taşıyor); çapalı kimlik bulunamazsa tam tarama yok.
func TestIdentityAnchorAndWindow(t *testing.T) {
	a, ok := identityAnchor("060203hb9Z0000389965120260904153319993000")
	if !ok || a.UTC().Format("2006-01-02 15:04:05") != "2026-09-04 15:33:19" {
		t.Fatalf("çapa: %v %v", a, ok)
	}
	for _, no := range []string{"060203AFD10049A1B2", "20261304153319", "20260904253319", "0fcd70a94ba1f695ea079750e71a7c10", "2026090415331"} {
		if _, ok := identityAnchor(no); ok {
			t.Fatalf("%q çapa olmamalı", no)
		}
	}
	from, to := time.Date(2026, 9, 4, 12, 0, 0, 0, time.UTC), time.Date(2026, 9, 4, 13, 0, 0, 0, time.UTC)
	wf, wt, anchored := identityWindow("x20260904153319y", from, to)
	if !anchored || wf.UTC().Format("15:04") != "03:33" || wt.UTC().Format("2006-01-02 15:04") != "2026-09-05 03:33" {
		t.Fatalf("çapalı pencere ±12 s: %v %v", wf, wt)
	}
	if wf2, wt2, anchored2 := identityWindow("nodatehere123", from, to); anchored2 || !wf2.Equal(from) || !wt2.Equal(to) {
		t.Fatal("çapasız → seçili aralık")
	}
	if !identityStopsFallback(IdentityHit{AnchorMs: 1}, 0) || identityStopsFallback(IdentityHit{}, 0) ||
		identityStopsFallback(IdentityHit{AnchorMs: 1}, 3) || identityStopsFallback(IdentityHit{AnchorMs: 1, Error: "x"}, 0) {
		t.Fatal("düşüş kararı: yalnız çapalı + 0 hit + hatasız")
	}
}

func TestIdentityQueryUsesAnchoredWindowAndArrayKeysLast(t *testing.T) {
	prevIdx := attrIndexReady.Load()
	attrIndexReady.Store(false)
	t.Cleanup(func() { attrIndexReady.Store(prevIdx) })
	withPromoted(t, "function_id", "attr_function_id")
	token := "060203hb9Z0000389965120260904153319993000"
	keys := identityKeys(token)
	// dizi yolu anahtarlar artık atlanmıyor (indexed=false), indeksliler önce sıralanır (candidates'ta)
	var idx, arr int
	for _, k := range keys {
		if k.indexed {
			idx++
		} else {
			arr++
		}
	}
	if idx != 1 || arr == 0 {
		t.Fatalf("indeksli %d, dizi %d", idx, arr)
	}
	f := TraceFilter{Search: token, From: time.Date(2026, 9, 4, 12, 0, 0, 0, time.UTC), To: time.Date(2026, 9, 4, 13, 0, 0, 0, time.UTC)}
	from, to, _ := identityWindow(token, f.From, f.To)
	sql, args := identityFirstQuery(f, keys[0], "", 10, from, to)
	if !strings.Contains(sql, "time >= ?") {
		t.Fatalf("pencere WHERE'de: %s", sql)
	}
	// buildGetTracesWhere >5 dk pencerede başlangıcı 5 dk'ya hizalar (v0.5.356).
	if got, ok := args[0].(time.Time); !ok || !got.Equal(from.Truncate(5*time.Minute)) {
		t.Fatalf("ilk arg çapalı pencere başı (5 dk hizalı) olmalı: %v", args[0])
	}
}
