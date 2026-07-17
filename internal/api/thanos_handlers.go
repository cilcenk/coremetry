package api

import (
	"context"
	"encoding/json"
	"fmt"
	"hash/fnv"
	"net/http"
	"strings"
	"time"

	"github.com/cilcenk/coremetry/internal/thanos"
)

// /clusters yüzeyinin ince handler'ları (v0.8.576, audit:
// docs/audit/thanos-multicluster-metrics-audit.md §5-6). Veri yolu
// internal/thanos'ta; burada yalnız cache anahtarı + rol kapısı +
// settings CRUD var (api.go-growth-minimal, hosts.go emsali).
//
// Fan-out İSTEMCİDE: /clusters sayfası cluster başına ayrı istek
// atar (audit §6) — her cluster kendi serveCached slotunda yaşar,
// yavaş/bozuk cluster diğerlerinin HIT'ini süründürmez ve backend'e
// errgroup girmez (v0.8.532 dersi).

// clusterCfgDigest hashes the config inputs that CHANGE the query
// result (URL + namespace filter) into the cache key, so an admin
// edit takes effect on the next request instead of hiding behind
// the 60s TTL. Token deliberately excluded: a rotated token yields
// the same data. (Hard constraint: cache key hashes ALL inputs.)
func clusterCfgDigest(c thanos.ClusterConfig) string {
	h := fnv.New64a()
	h.Write([]byte(c.URL))
	h.Write([]byte{0})
	h.Write([]byte(c.NamespaceFilter))
	return fmt.Sprintf("%x", h.Sum64())
}

// getClusterPods — GET /api/clusters/pods?cluster=<name>. Anlık
// (namespace, pod) CPU+memory; Thanos'a cluster başına 4 sabit
// sorgu (pod başına asla). TTL 60s (hosts konvansiyonu; tipik 30s
// scrape'in bir tur gecikmesi kabul edilir).
func (s *Server) getClusterPods(w http.ResponseWriter, r *http.Request) {
	if s.thanos == nil || !s.thanos.HasEnabledClusters() {
		http.Error(w, "no thanos clusters configured", http.StatusNotFound)
		return
	}
	name := strings.TrimSpace(r.URL.Query().Get("cluster"))
	if name == "" {
		http.Error(w, "cluster query param required", http.StatusBadRequest)
		return
	}
	cfg, ok := s.thanos.ClusterByName(name)
	if !ok {
		http.Error(w, "unknown or disabled cluster", http.StatusNotFound)
		return
	}
	key := fmt.Sprintf("cluster-pods:%s:%s", name, clusterCfgDigest(cfg))
	s.serveCached(w, r, key, 60*time.Second, func(ctx context.Context) (any, error) {
		// 10s deadline per cluster call (client hard cap 15s):
		// a wedged Querier must not pin the singleflight slot.
		qctx, cancel := context.WithTimeout(ctx, 10*time.Second)
		defer cancel()
		rows, err := s.thanos.PodMetrics(qctx, cfg)
		if err != nil {
			return nil, err
		}
		return map[string]any{"cluster": name, "pods": rows, "count": len(rows)}, nil
	})
}

// getClusterPodDetail — GET /api/clusters/pods/detail?cluster=&
// namespace=&pod=&from=&to=. Tek pod'un dakika-bucket'lı trendi
// (drawer yolu). Pencere hosts gibi ≤6h clamp'lenir.
func (s *Server) getClusterPodDetail(w http.ResponseWriter, r *http.Request) {
	if s.thanos == nil || !s.thanos.HasEnabledClusters() {
		http.Error(w, "no thanos clusters configured", http.StatusNotFound)
		return
	}
	q := r.URL.Query()
	name := strings.TrimSpace(q.Get("cluster"))
	namespace := strings.TrimSpace(q.Get("namespace"))
	pod := strings.TrimSpace(q.Get("pod"))
	if name == "" || namespace == "" || pod == "" {
		http.Error(w, "cluster, namespace, pod query params required", http.StatusBadRequest)
		return
	}
	cfg, ok := s.thanos.ClusterByName(name)
	if !ok {
		http.Error(w, "unknown or disabled cluster", http.StatusNotFound)
		return
	}
	from, to := parseFromTo(r, time.Hour)
	if to.Sub(from) > 6*time.Hour { // hosts clampHostWindow simetriği
		from = to.Add(-6 * time.Hour)
	}
	key := fmt.Sprintf("cluster-pod-detail:%s:%s:%s:%s:%s",
		name, namespace, pod, clusterCfgDigest(cfg), cacheBucket(from, to))
	s.serveCached(w, r, key, 60*time.Second, func(ctx context.Context) (any, error) {
		qctx, cancel := context.WithTimeout(ctx, 10*time.Second)
		defer cancel()
		trend, err := s.thanos.PodTrend(qctx, cfg, namespace, pod, from, to)
		if err != nil {
			return nil, err
		}
		return map[string]any{
			"cluster": name, "namespace": namespace, "pod": pod,
			"trend": trend,
		}, nil
	})
}

// getClusterSources — GET /api/clusters/sources. ENABLED cluster
// adları (viewer+): /clusters sayfasının fan-out listesi. Settings
// GET'i admin-only olduğundan bu dar, secret'sız uç ayrı; bellek-içi
// snapshot'tan okur, cache gerekmez.
func (s *Server) getClusterSources(w http.ResponseWriter, r *http.Request) {
	names := []string{}
	if s.thanos != nil {
		for _, c := range s.thanos.Snapshot().Clusters {
			if c.Enabled {
				names = append(names, c.Name)
			}
		}
	}
	writeJSON(w, map[string]any{"clusters": names})
}

// getThanosSettings returns the masked cluster list (per-cluster
// hasToken; tokens never round-trip — tempo contract).
func (s *Server) getThanosSettings(w http.ResponseWriter, r *http.Request) {
	if s.thanos == nil {
		writeJSON(w, thanos.Snapshot{})
		return
	}
	writeJSON(w, s.thanos.Snapshot())
}

// putThanosSettings replaces the WHOLE cluster list atomically
// (custom_roles whole-blob convention). Per-cluster empty token
// preserves the stored one, matched by cluster NAME — so editing a
// URL or the namespace filter doesn't force a token re-paste.
func (s *Server) putThanosSettings(w http.ResponseWriter, r *http.Request) {
	if s.thanos == nil {
		http.Error(w, "thanos service not available", http.StatusServiceUnavailable)
		return
	}
	var in thanos.Settings
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		http.Error(w, "invalid body", http.StatusBadRequest)
		return
	}
	cur := s.thanos.CurrentSettings()
	stored := map[string]string{}
	for _, c := range cur.Clusters {
		stored[c.Name] = c.Token
	}
	seen := map[string]bool{}
	for i := range in.Clusters {
		c := &in.Clusters[i]
		c.Name = strings.TrimSpace(c.Name)
		c.URL = strings.TrimSpace(c.URL)
		c.AuthType = strings.TrimSpace(c.AuthType)
		if c.Name == "" {
			http.Error(w, "cluster name required", http.StatusBadRequest)
			return
		}
		if seen[c.Name] {
			http.Error(w, "duplicate cluster name: "+c.Name, http.StatusBadRequest)
			return
		}
		seen[c.Name] = true
		if c.Enabled && c.URL == "" {
			http.Error(w, "url required for enabled cluster "+c.Name, http.StatusBadRequest)
			return
		}
		switch c.AuthType {
		case "", "none", "bearer":
			// ok
		default:
			http.Error(w, "authType must be one of: none, bearer", http.StatusBadRequest)
			return
		}
		if c.Token == "" {
			c.Token = stored[c.Name]
		}
	}
	if err := s.thanos.SavePersisted(r.Context(), s.store, in); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	s.publishConfigReload(r.Context(), "thanos")
	snap := s.thanos.Snapshot()
	// Token'lar audit_log'a girmez (tempo sözleşmesi) — adlar +
	// enabled bayrakları operatörün "kim ne zaman hangi cluster'ı
	// ekledi/kapattı" sorusuna yeter.
	names := make([]string, 0, len(snap.Clusters))
	for _, c := range snap.Clusters {
		state := "off"
		if c.Enabled {
			state = "on"
		}
		names = append(names, c.Name+"("+state+")")
	}
	details, _ := json.Marshal(map[string]any{"clusters": names, "count": len(names)})
	s.audit(r, "settings.thanos.update", "settings", "thanos_clusters", string(details))
	writeJSON(w, snap)
}
