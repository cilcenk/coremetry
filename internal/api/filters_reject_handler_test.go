package api

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// filters_reject_handler_test.go — v0.10.118 çalışma-zamanı kapısı
// (handler yolu). Canlı doğrulama mümkün olmadı: minikube pod'u eski
// imajda (registry çekimi askıda), lokal binary'nin ON CLUSTER boot DDL'i
// Keeper'da zaman aşımı verdi. Bu test GERÇEK handler'ı httptest ile
// çağırır: 400 yolu store'a dokunmadan döner, yani &Server{} yeter.
// Kanıt: gövde JSON `{"error": …}` ve metin op'u adlandırır.

func TestGetTracesRejectsInvalidFilterOpAtTheBoundary(t *testing.T) {
	s := &Server{}
	bad := `[{"k":"http.user_agent","op":"contains","v":["zzz"]}]`
	for _, tc := range []struct {
		name string
		h    http.HandlerFunc
		url  string
	}{
		{"traces list · filters", s.getTraces, "/api/traces?limit=50&from=1&to=2&filters=" + bad},
		{"traces list · filterGroup", s.getTraces, "/api/traces?limit=50&from=1&to=2&filterGroup=" + `{"join":"AND","filters":[{"k":"a","op":"contains","v":["1"]}]}`},
		{"traces count", s.getTracesCount, "/api/traces/count?from=1&to=2&filters=" + bad},
		{"traces aggregate", s.getTraceAggregate, "/api/traces/aggregate?from=1&to=2&filters=" + bad},
		{"traces export", s.exportTracesCSV, "/api/traces/export.csv?from=1&to=2&filters=" + bad},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rr := httptest.NewRecorder()
			tc.h(rr, httptest.NewRequest(http.MethodGet, tc.url, nil))
			if rr.Code != http.StatusBadRequest {
				t.Fatalf("code=%d body=%s", rr.Code, rr.Body.String())
			}
			if !strings.Contains(rr.Body.String(), "contains") {
				t.Fatalf("gövde op'u adlandırmıyor: %s", rr.Body.String())
			}
		})
	}
}
