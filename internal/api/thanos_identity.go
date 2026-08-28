package api

// thanos_identity.go — v0.10.128 (K8s entity katmanı adım 2, Remote Cluster
// kimlik eşlemesi rozeti; docs/plans/entity-layer-design-2026-08-28.md §1.1).
//
// api.go BÜYÜMEYECEK kuralı (registerVMetricsRoutes emsali): yüzeyin
// rotası kendi dosyasında, api.go tek satır register çağrısıyla büyür.
//
//	GET /api/clusters/sources/probe?cluster=<id|name>
//
// Rol kapısı: admin — Settings → Clusters formunun "Test label" düğmesi;
// sonucu operatör görür. Probe ucu sözleşmesi (vmetrics_handlers.go
// 161-164 emsali): bağlantı/sorgu başarısızlığı operatörün sorusuna
// BAŞARILI bir cevaptır → 200 + {ok:false, error}, 4xx değil. Cache YOK:
// tıklama başına bir sorgu, 10 s deadline (thanos_handlers.go ile aynı).
//
// Neden ayrı uç, `getClusterSources` genişletilmedi: sources listesi
// viewer-güvenli ve her sayfa açılışında çağrılıyor; N cluster'a Thanos
// sorgusu atmak onu yavaşlatır ve cache slotunu tek Thanos'a bağlardı.

import (
	"context"
	"net/http"
	"strings"
	"time"

	"github.com/cilcenk/coremetry/internal/auth"
)

func (s *Server) registerThanosIdentityRoutes(mux *http.ServeMux) {
	mux.Handle("GET /api/clusters/sources/probe",
		auth.RequireRole(auth.RoleAdmin, http.HandlerFunc(s.probeClusterSource)))
}

func (s *Server) probeClusterSource(w http.ResponseWriter, r *http.Request) {
	if s.thanos == nil {
		writeJSONError(w, http.StatusServiceUnavailable, "thanos service not available")
		return
	}
	ref := strings.TrimSpace(r.URL.Query().Get("cluster"))
	c, ok := s.thanos.ClusterByRef(ref)
	if !ok {
		writeJSONError(w, http.StatusNotFound, "cluster not configured or disabled")
		return
	}
	label, value := c.EffectiveThanosLabel()
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	series, err := s.thanos.ProbeCluster(ctx, c)
	out := map[string]any{
		"cluster": c.EffectiveID(), "name": c.Name,
		"label": label, "value": value, "series": series,
		"ok": err == nil && series > 0,
	}
	if err != nil {
		out["error"] = err.Error()
	} else if series == 0 && label != "" {
		out["error"] = "matcher eşleşmedi: " + label + `="` + value + `" ile kube_node_info serisi yok`
	}
	writeJSON(w, out)
}
