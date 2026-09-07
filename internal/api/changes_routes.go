package api

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/cilcenk/coremetry/internal/mcptools"
)

// changes_routes.go — v0.10.545: GET /api/changes — list_deployments tool'unun
// HTTP eşi (cluster/namespace/servis + from/to). Aynı gövde
// (mcptools.ListDeploymentsWindow); api.go büyümez (route_registry init).
// Viewer tabanı: REST eşleri (/api/rollouts, /api/services/{name}/deploys)
// da viewer. Kapsam (G13) geldiğinde Scope aynı gövdeye enjekte edilir.

func init() { registerRoutesExtra("changes", (*Server).registerChangesRoutes) }

func (s *Server) registerChangesRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/changes", s.listChanges)
}

const changesTTL = 30 * time.Second

func (s *Server) listChanges(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	from, to := parseFromTo(r, 6*time.Hour)
	a := struct {
		Cluster, Namespace, Service string
		Limit                       int
	}{strings.TrimSpace(q.Get("cluster")), strings.TrimSpace(q.Get("namespace")), strings.TrimSpace(q.Get("service")), parseInt(q.Get("limit"), 20)}
	if a.Cluster != "" {
		if c, ok := s.resolveCluster(a.Cluster); ok {
			a.Cluster = c.EffectiveID()
		}
	}
	// Anahtar TÜM girdileri taşır (cache_key_test.go sözleşmesi); pencere dakika kovası.
	key := fmt.Sprintf("changes:v1:c=%s:ns=%s:svc=%s:lim=%d:from=%d:to=%d", a.Cluster, a.Namespace, a.Service, a.Limit, from.Unix()/60, to.Unix()/60)
	s.serveCached(w, r, key, changesTTL, func(ctx context.Context) (any, error) {
		return mcptools.ListDeploymentsWindow(ctx, s.mcpDeps(), mcptools.ListDeploymentsArgs{
			Cluster: a.Cluster, Namespace: a.Namespace, Service: a.Service, Limit: a.Limit,
		}, from, to)
	})
}
