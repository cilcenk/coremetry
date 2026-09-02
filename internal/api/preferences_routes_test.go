package api

// preferences_routes_test.go — v0.10.247 tercih ucu sözleşmesi: key regex,
// şekil doğrulaması (v, giriş sayısı, id regex), kanonik JSON (widths
// düşer, alan sırası sabit), deterministik id, claims yoksa 401.

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestPreferenceKeyAndID(t *testing.T) {
	for _, ok := range []string{"traces-list", "a", "x9-y", strings.Repeat("a", 64)} {
		if !validPreferenceKey(ok) {
			t.Errorf("%q geçerli olmalı", ok)
		}
	}
	for _, bad := range []string{"", "-a", "Traces", "a_b", "a/b", strings.Repeat("a", 65), "a b"} {
		if validPreferenceKey(bad) {
			t.Errorf("%q geçersiz olmalı", bad)
		}
	}
	if preferenceID("u1", "traces-list") != "pref:u1:traces-list" {
		t.Error("id deterministik değil")
	}
}

func TestValidateColumnModel(t *testing.T) {
	many := make([]string, 65)
	for i := range many {
		many[i] = "c"
	}
	cases := []struct {
		name string
		m    columnModelBody
		ok   bool
	}{
		{"geçerli", columnModelBody{V: 1, Order: []string{"time", "channel_code", "http.route"}, Hidden: []string{"status"}, Sig: "s"}, true},
		{"v≠1", columnModelBody{V: 2, Order: []string{"a"}}, false},
		{"boş order", columnModelBody{V: 1, Order: nil}, false},
		{"65 giriş", columnModelBody{V: 1, Order: many}, false},
		{"kötü id", columnModelBody{V: 1, Order: []string{"a b"}}, false},
		{"hidden kötü id", columnModelBody{V: 1, Order: []string{"a"}, Hidden: []string{"x/y"}}, false},
		{"sig uzun", columnModelBody{V: 1, Order: []string{"a"}, Sig: strings.Repeat("s", 129)}, false},
	}
	for _, c := range cases {
		if err := validateColumnModel(c.m); (err == nil) != c.ok {
			t.Errorf("%s: err=%v, ok istenen %v", c.name, err, c.ok)
		}
	}
}

func TestCanonicalColumnModelJSON(t *testing.T) {
	a := canonicalColumnModelJSON(columnModelBody{V: 1, Order: []string{"a", "b"}, Hidden: nil, Sig: "s", Widths: map[string]float64{"a": 120}})
	if a != `{"v":1,"order":["a","b"],"hidden":[],"sig":"s"}` {
		t.Errorf("kanonik JSON: %s", a)
	}
	if strings.Contains(a, "widths") {
		t.Error("genişlikler tarayıcı-yerel kalmalı")
	}
}

func TestPreferencesRequireClaims(t *testing.T) {
	s := &Server{}
	mux := s.buildMux()
	for _, m := range []string{"GET", "PUT", "DELETE"} {
		req := httptest.NewRequest(m, "/api/preferences/traces-list", strings.NewReader(`{"v":1,"order":["a"],"hidden":[]}`))
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		if rec.Code != http.StatusUnauthorized {
			t.Errorf("%s claims yokken %d, istenen 401", m, rec.Code)
		}
	}
}
