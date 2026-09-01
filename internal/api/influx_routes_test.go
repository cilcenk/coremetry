package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/cilcenk/coremetry/internal/influx"
)

// influx_routes_test.go — v0.10.222 (Influx D1). Handler sözleşmeleri
// CH'siz: (1) servis yokken 503; (2) bozuk JSON / geçersiz ayar 400 — store'a
// HİÇ ulaşılmaz (Normalize önce); (3) test ucu başarısızlığı 200 + ok:false
// (api-route kuralı: bağlantı denemesinin başarısızlığı operatörün
// sorusuna başarılı bir cevaptır); (4) düz-metin token 400 (K5 kapısı
// handler'da da tutuyor). Rota kaydı TestMuxRoutePatterns'ta (registerRoutes
// içinden) kurulur.

func TestInfluxRoutes_NilService503(t *testing.T) {
	s := &Server{}
	for _, c := range []struct {
		method, path string
		h            http.HandlerFunc
	}{
		{"GET", "/api/settings/influx", s.getInfluxSettings},
		{"PUT", "/api/settings/influx", s.putInfluxSettings},
		{"POST", "/api/settings/influx/test", s.testInfluxSource},
	} {
		rec := httptest.NewRecorder()
		c.h(rec, httptest.NewRequest(c.method, c.path, strings.NewReader(`{}`)))
		if rec.Code != http.StatusServiceUnavailable {
			t.Errorf("%s %s: want 503, got %d", c.method, c.path, rec.Code)
		}
	}
}

func TestInfluxRoutes_PutValidation(t *testing.T) {
	s := &Server{influx: influx.New()}
	cases := []struct {
		name, body, wantMsg string
	}{
		{"broken json", `{"sources": [`, "JSON"},
		{"enabled without url", `{"sources":[{"name":"gg","enabled":true,"org":"o","tokenRef":"env:X","queries":[{"name":"q","flux":"x","groupBy":["A"]}]}]}`, "url"},
		{"plain token", `{"sources":[{"name":"gg","enabled":false,"tokenRef":"my-plain-token"}]}`, "tokenRef"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			s.putInfluxSettings(rec, httptest.NewRequest("PUT", "/api/settings/influx", strings.NewReader(c.body)))
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("want 400, got %d: %s", rec.Code, rec.Body.String())
			}
			if !strings.Contains(rec.Body.String(), c.wantMsg) {
				t.Fatalf("body should mention %q: %s", c.wantMsg, rec.Body.String())
			}
			if ct := rec.Header().Get("Content-Type"); !strings.Contains(ct, "json") {
				t.Fatalf("errors are JSON envelopes (writeJSONError), got content-type %q", ct)
			}
		})
	}
}

func TestInfluxRoutes_TestUnresolvedTokenIs200NotOK(t *testing.T) {
	s := &Server{influx: influx.New()}
	body := `{"name":"gg","url":"http://influx.local:8086","org":"o","tokenRef":"env:COREMETRY_INFLUX_TOKEN_DOES_NOT_EXIST","queries":[{"name":"q","flux":"from(bucket:\"b\")","groupBy":["A"]}]}`
	rec := httptest.NewRecorder()
	s.testInfluxSource(rec, httptest.NewRequest("POST", "/api/settings/influx/test", strings.NewReader(body)))
	if rec.Code != http.StatusOK {
		t.Fatalf("want 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var res influx.TestResult
	if err := json.Unmarshal(rec.Body.Bytes(), &res); err != nil {
		t.Fatal(err)
	}
	if res.OK || res.TokenResolved || !strings.Contains(res.Error, "DOES_NOT_EXIST") {
		t.Fatalf("unresolved token → ok:false with the ref in the error: %+v", res)
	}
	// Doğrulama hatası da 200 + ok:false (form deneme düğmesi 4xx görmez).
	rec = httptest.NewRecorder()
	s.testInfluxSource(rec, httptest.NewRequest("POST", "/api/settings/influx/test", strings.NewReader(`{"name":"gg"}`)))
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"ok":false`) {
		t.Fatalf("validation failure on test → 200 ok:false; got %d %s", rec.Code, rec.Body.String())
	}
}
