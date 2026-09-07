// v0.9.613 — /admin/clickhouse DDL kuyruğu sağlık ucu.
//
// Üç gecedir süren prod vakasının ürünleşmiş teşhisi: operatör artık
// elle clusterAllReplicas sorgusu yazmıyor, panel verdict + eylem
// cümlesini veriyor. Sorgu ayrıntıları ve ayırıcının kalibrasyonu
// chstore/ddl_queue_health.go başlığında.
package api

import (
	"context"
	"net/http"
	"time"

	"github.com/cilcenk/coremetry/internal/auth"
)

// registerCHDDLQueueRoutes — api.go'daki admin/clickhouse bloğundan
// çağrılır (registerXxxRoutes deseni).
func (s *Server) registerCHDDLQueueRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/admin/clickhouse/ddl-queue",
		auth.RequireRole(auth.RoleAdmin, s.getCHDDLQueueHealth))
}

// getCHDDLQueueHealth — teşhis okuma.
//
// 15s cache: teşhis paneli açıkken tazelenebilir olmalı ama
// clusterAllReplicas fan-out'u her F5'te koşmamalı. Anahtar sabit —
// girdisi yok (cluster adı süreç ömrü boyunca sabit).
func (s *Server) getCHDDLQueueHealth(w http.ResponseWriter, r *http.Request) {
	// v0.10.525 — prod'da kuyruk okuması 13,6 s (Keeper, 10k girdi): 12 s bütçe
	// her seferinde "PROBE DÜŞTÜ" üretiyordu. Bütçe 30 s (sorgu tavanları 20 s),
	// cache 60 s — teşhis paneli, canlı sayaç değil.
	s.serveCached(w, r, "ch-ddl-queue", 60*time.Second, func(ctx context.Context) (any, error) {
		ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
		defer cancel()
		return s.store.GetDDLQueueHealth(ctx)
	})
}
