package api

// exception_pods.go — v0.10.138 (DETAY SAYFALARI adım 4: exceptions dağılımı).
// api.go BÜYÜMEZ kuralı: rota burada, api.go tek satır register.
//
//   GET /api/exception-groups/{fp}/pods
//
// Hata grubunun oluşumlarının pod/node dağılımı (grubun kendi penceresi,
// samples ile aynı yüklem). Entity katmanı kapalıysa 404 {disabled:true}
// (k8s_* kolonları 0011 ile gelir; panel gizlenir) — entityEnabled emsali.
// Rol kapısı yok: salt-okunur drill-down, viewer görmeli. serveCached 30 s;
// anahtar fp (pencere grubun kendisinden — occurrences ucuyla aynı duruş).
// Satırlara Remote Cluster eşlemesi (clusterId/clusterName) eklenir;
// eşlenmeyen cluster değerleri ilan edilir (link yok — brief kuralı 4).

import (
	"context"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/cilcenk/coremetry/internal/chstore"
	"github.com/cilcenk/coremetry/internal/thanos"
)

func (s *Server) registerExceptionPodRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/exception-groups/{fp}/pods", s.getExceptionGroupPods)
}

func (s *Server) getExceptionGroupPods(w http.ResponseWriter, r *http.Request) {
	if !s.entityEnabled(w) {
		return
	}
	fp := strings.TrimSpace(r.PathValue("fp"))
	if fp == "" {
		writeJSONError(w, http.StatusBadRequest, "fingerprint gerekli")
		return
	}
	s.serveCached(w, r, "exc-pods:"+fp, 30*time.Second, func(ctx context.Context) (any, error) {
		res, err := s.store.GetExceptionGroupPods(ctx, fp)
		if err != nil {
			return nil, err
		}
		if res.From.IsZero() {
			return nil, errNotFound // bilinmeyen grup: boş 200 değil 404 (inceleme)
		}
		byValue := map[string]thanos.ClusterConfig{}
		for _, c := range s.thanos.CurrentSettings().Clusters {
			if c.Enabled {
				for _, k := range c.SpanClusterKeys() { // v0.10.139 — bir kayıt birden çok değer
					byValue[k] = c
				}
			}
		}
		type row struct {
			chstore.ExceptionPodRow
			ClusterID   string `json:"clusterId,omitempty"`
			ClusterName string `json:"clusterName,omitempty"`
		}
		rows := make([]row, 0, len(res.Rows))
		unmapped := map[string]bool{}
		type key struct{ cid, ns, pod string }
		idx := map[key]int{}
		for _, p := range res.Rows {
			x := row{ExceptionPodRow: p}
			if c, ok := byValue[p.Cluster]; ok {
				x.ClusterID, x.ClusterName = c.EffectiveID(), c.Name
				// v0.10.139 — çoklu değerli kayıtta aynı pod tek satır.
				k := key{x.ClusterID, p.Namespace, p.Pod}
				if j, ok := idx[k]; ok {
					rows[j].Occurrences += p.Occurrences
					if p.LastSeen.After(rows[j].LastSeen) {
						rows[j].LastSeen = p.LastSeen
					}
					continue
				}
				idx[k] = len(rows)
			} else {
				unmapped[p.Cluster] = true
			}
			rows = append(rows, x)
		}
		out := map[string]any{"fingerprint": fp, "rows": rows, "noContext": res.NoContext, "total": res.Total, "scanned": res.Scanned,
			"sampled": res.Sampled, "truncated": res.Truncated, "from": res.From, "to": res.To}
		if res.SchemaMissing {
			out["schemaMissing"] = true
		}
		if len(unmapped) > 0 {
			vals := make([]string, 0, len(unmapped))
			for v := range unmapped {
				vals = append(vals, v)
			}
			sort.Strings(vals)
			out["unmappedClusters"] = vals
		}
		return out, nil
	})
}
