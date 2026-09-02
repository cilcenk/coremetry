package api

// route_registry.go — v0.10.247 (DataTable/ContextBar audit §11, "api.go'ya
// satır EKLEMEDEN kayıt").
//
// Bugüne dek her domain dosyası registerRoutes'a bir satır ekliyordu
// (api.go:617+). Bu defter, bir domain dosyasının KENDİ init()'inde kayıt
// olmasını sağlar: api.go hiç büyümez. buildMux bu dosyaya taşındı
// (api.go 5 satır küçüldü) ve registerRoutes'tan sonra defteri ADLA
// sıralı, deterministik boşaltır — Go 1.22 ServeMux kalıp çakışması
// yine kayıt anında panic'ler ve TestMuxRoutePatterns buildMux'ı
// kurduğu için defter rotaları da çakışma testine girer (v0.9.465-470
// sınıfı). Aynı ad iki kez kaydolursa init anında panic: çift kayıt
// sessizce iki handler bağlamaz.
//
// Aşı (audit hakemi): api-route skill'inin iki serbest-form sapması
// (rollup_routes.go, annotation_routes.go) aynı commit'te deftere taşındı.

import (
	"fmt"
	"net/http"
	"sort"
)

type extraRouteRegistrar func(s *Server, mux *http.ServeMux)

var extraRouteRegistrars = map[string]extraRouteRegistrar{}

// registerRoutesExtra — init()'ten çağrılır; ad benzersiz olmalı.
func registerRoutesExtra(name string, fn extraRouteRegistrar) {
	if name == "" || fn == nil {
		panic("registerRoutesExtra: boş ad ya da nil registrar")
	}
	if _, dup := extraRouteRegistrars[name]; dup {
		panic(fmt.Sprintf("registerRoutesExtra: %q iki kez kaydedildi", name))
	}
	extraRouteRegistrars[name] = fn
}

// extraRouteNames — deterministik sıra (test + kayıt).
func extraRouteNames() []string {
	names := make([]string, 0, len(extraRouteRegistrars))
	for n := range extraRouteRegistrars {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}

// buildMux registers every route. Ayrı fonksiyon (v0.9.471): Go 1.22
// ServeMux kalıp çakışmaları KAYIT anında panic'ler — kayıt Start()
// içinde gömülüyken hiçbir test bunu göremiyordu ve v0.9.465-470
// tag'leri boot'ta patladı. TestMuxRoutePatterns bu fonksiyonu kurar.
func (s *Server) buildMux() *http.ServeMux {
	mux := http.NewServeMux()
	s.registerRoutes(mux)
	for _, name := range extraRouteNames() {
		extraRouteRegistrars[name](s, mux)
	}
	return mux
}
