package api

// registerVMetricsRoutes — v0.9.1150 (VictoriaMetrics read backend Faz 1).
//
// api.go BÜYÜMEYECEK kuralı (registerAIRoutes / registerRAGRoutes /
// registerRollupRoutes emsali): yeni ayar yüzeyinin rotaları kendi
// dosyasında. Route desenleri ve gate'ler tempo/logstore ikizleriyle
// BİREBİR aynı şekilde; mux çakışmaları TestMuxRoutePatterns'ta korunur.
//
// Üçü de admin: okuma backend'i değişimi kurulumdaki HER operatörün
// Metrics sayfasında gördüğü sayıların KAYNAĞINI değiştiriyor, ve kayıtlı
// token operatörün tüm VM'ini okuyor. GET'i de admin tutuyoruz (kıyas:
// /api/settings/failure-slo'nun GET'i rolsüzdür çünkü o bir grafik
// referans ÇİZGİSİ; bu ise yapılandırma + hasToken).
//
// Kaynak ROZETİNİN bu uçlara ihtiyacı YOK: /api/metrics/names cevabı
// `source` alanını kendisi taşıyor. Aksi halde viewer rozeti hiç
// göremezdi (bu uç 403) ve iki istek arasında ayar değişirse rozet
// gövdeye yalan söylerdi.

import (
	"net/http"

	"github.com/cilcenk/coremetry/internal/auth"
)

func (s *Server) registerVMetricsRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET  /api/settings/victoria-metrics", auth.RequireRole(auth.RoleAdmin, s.getVMSettings))
	mux.HandleFunc("PUT  /api/settings/victoria-metrics", auth.RequireRole(auth.RoleAdmin, s.putVMSettings))
	mux.HandleFunc("POST /api/settings/victoria-metrics/test", auth.RequireRole(auth.RoleAdmin, s.testVMSettings))
}
