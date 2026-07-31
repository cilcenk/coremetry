package api

import (
	"net/http"
	"testing"
)

// v0.9.471 (olay: v0.9.465-470 tag'leri boot'ta panic) — Go 1.22 ServeMux
// kalıp çakışmaları KAYIT anında panic'ler; kayıt Start() içinde
// gömülüyken hiçbir test mux'u kurmuyordu. "GET /api/anomalies/events/{id}"
// (v465) mevcut "GET /api/anomalies/{id}/rootcause" ile çakıştı ve altı
// sürüm açılamaz çıktı — go test yeşil, prod boot kırmızıydı.
//
// Bu test route kayıtlarını GERÇEKTEN kurar: bir sonraki çakışma prod
// yerine ilk test koşusunda patlar. Handler'lar çağrılmaz — yalnız kayıt;
// zero-value Server yeterlidir (kayıt aşaması alan dereference etmez;
// ederse o da yakalanmaya değer bir boot bağımlılığıdır).
func TestMuxRoutePatterns(t *testing.T) {
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("route kaydı panic'ledi (ServeMux kalıp çakışması ya da kayıt-anı bağımlılığı): %v", r)
		}
	}()
	s := &Server{}
	mux := http.NewServeMux()
	s.registerRoutes(mux)
}
