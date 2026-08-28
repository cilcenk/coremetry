package api

import (
	"errors"
	"net/url"
	"strings"
	"testing"

	"github.com/cilcenk/coremetry/internal/chstore"
)

// filters_reject_test.go — v0.10.118. DERLENEMEYEN FİLTRE SINIRDA REDDEDİLİR.
//
// Ölçülen olay (perf keşfi 2026-08-28, docs/perf/perf-budget-2026-08-28.md
// §2.3): `filters=[{"k":"http.user_agent","op":"contains",…}]` — `contains`
// allowedOps'ta yok. Eski akış: parseFilters JSON'u kabul ediyor, yan
// tümce MV yolunu diskalifiye ediyor (len(Filters)>0), ApplyFilters
// derlenemeyen yan tümceyi loglayıp ATLIYOR → ham GROUP BY 24h penceresinin
// tamamını tarıyor (1.57 M satır, 6.2 s) ve FİLTRESİZ 51 satır 200 dönüyor.
// Doğruluk + maliyet, ikisi birden. Şimdi: 400, tarama yok.

func TestParseFiltersRejectsUncompilableClauses(t *testing.T) {
	cases := []struct {
		name    string
		raw     string
		wantErr string // "" = geçerli
	}{
		{"boş → filtre yok", "", ""},
		{"geçerli eşitlik", `[{"k":"http.method","op":"=","v":["GET"]}]`, ""},
		{"boş op = eşitlik", `[{"k":"http.method","v":["GET"]}]`, ""},
		{"LIKE küçük harf (upper'lanır)", `[{"k":"user_agent.original","op":"like","v":["%Mozilla%"]}]`, ""},
		{"EXISTS", `[{"k":"channel_code","op":"EXISTS"}]`, ""},
		{"bilinmeyen op contains", `[{"k":"http.user_agent","op":"contains","v":["zzz"]}]`, `invalid operator "contains"`},
		{"bilinmeyen op startsWith", `[{"k":"x","op":"startsWith","v":["a"]}]`, `invalid operator "startsWith"`},
		{"anahtar yok (v0.9.269 şekli)", `[{"key":"tablespace_name","op":"=","value":"SYSTEM"}]`, "has no key"},
		{"bozuk JSON", `[{"k":"x","op":"="`, "filters:"},
		{"ikinci yan tümce bozuk → indeksli hata", `[{"k":"a","op":"=","v":["1"]},{"k":"b","op":"between","v":["1","2"]}]`, "filters[1]"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			out, err := parseFilters(c.raw)
			if c.wantErr == "" {
				if err != nil {
					t.Fatalf("geçerli girdi reddedildi: %v", err)
				}
				return
			}
			if err == nil {
				t.Fatalf("reddedilmedi, %d filtre döndü", len(out))
			}
			if !errors.Is(err, errBadRequest) {
				t.Errorf("400 sınıfı değil: %v", err)
			}
			if !strings.Contains(err.Error(), c.wantErr) {
				t.Errorf("hata metni %q içermiyor: %v", c.wantErr, err)
			}
		})
	}
}

func TestParseFilterGroupRejectsBadLeaves(t *testing.T) {
	if g, err := parseFilterGroup(""); g != nil || err != nil {
		t.Fatalf("boş grup: %v %v", g, err)
	}
	if g, err := parseFilterGroup(`{"join":"OR","filters":[{"k":"a","op":"=","v":["1"]}],"groups":[{"join":"AND","filters":[{"k":"b","op":"!=","v":["2"]}]}]}`); err != nil || g == nil || len(g.Groups) != 1 {
		t.Fatalf("geçerli grup reddedildi: %v", err)
	}
	for name, raw := range map[string]string{
		"yaprakta bilinmeyen op":   `{"join":"AND","filters":[{"k":"a","op":"contains","v":["1"]}]}`,
		"alt grupta bilinmeyen op": `{"join":"OR","filters":[],"groups":[{"join":"AND","filters":[{"k":"a","op":"~","v":["1"]}]}]}`,
		"bozuk JSON":               `{"join":"AND","filters":[`,
		"anahtarsız yaprak":        `{"join":"AND","filters":[{"op":"=","v":["1"]}]}`,
	} {
		t.Run(name, func(t *testing.T) {
			g, err := parseFilterGroup(raw)
			if err == nil || !errors.Is(err, errBadRequest) {
				t.Fatalf("reddedilmedi: g=%v err=%v", g, err)
			}
		})
	}
}

// TestParseTraceFilterPropagatesFilterErrors — /api/traces, /count,
// /aggregate ve export aynı ayrıştırıcıdan geçer; hata 400 olarak
// handler'a çıkar (ferr != nil → http.Error 400).
func TestParseTraceFilterPropagatesFilterErrors(t *testing.T) {
	q := url.Values{}
	q.Set("filters", `[{"k":"http.user_agent","op":"contains","v":["zzz"]}]`)
	if _, err := parseTraceFilter(q); err == nil || !errors.Is(err, errBadRequest) {
		t.Fatalf("filters hatası yayılmadı: %v", err)
	}
	q = url.Values{}
	q.Set("filterGroup", `{"join":"AND","filters":[{"k":"a","op":"contains","v":["1"]}]}`)
	if _, err := parseTraceFilter(q); err == nil || !errors.Is(err, errBadRequest) {
		t.Fatalf("filterGroup hatası yayılmadı: %v", err)
	}
	q = url.Values{}
	q.Set("filters", `[{"k":"channel_code","op":"=","v":["010101"]}]`)
	f, err := parseTraceFilter(q)
	if err != nil || len(f.Filters) != 1 {
		t.Fatalf("geçerli filtre: %v %+v", err, f.Filters)
	}
	// Doğrulama chstore'un kendi allowedOps sözlüğüne bağlı — iki liste
	// ayrışamaz: parse'ın kabul ettiği her op SQL'e derlenir.
	for _, op := range []string{"=", "!=", "=~", "!~", "LIKE", "NOT LIKE", "IN", "NOT IN", ">", ">=", "<", "<=", "EXISTS", "NOT EXISTS"} {
		fe := chstore.FilterExpr{Key: "http.method", Op: op, Values: []string{"1"}}
		if err := fe.Validate(); err != nil {
			t.Errorf("op %q parse'ta reddedildi ama SQL'de geçerli", op)
		}
		if _, _, err := fe.SQL(); err != nil {
			t.Errorf("op %q parse'ta geçti ama SQL'e derlenmedi: %v", op, err)
		}
	}
}
