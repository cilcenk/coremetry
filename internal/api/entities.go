package api

// entities.go — v0.10.130 (K8s entity katmanı adım 6: sorgu katmanı ve
// pivot uçları; docs/plans/entity-layer-design-2026-08-28.md §4-§5).
//
// api.go BÜYÜMEYECEK kuralı: rotalar burada, api.go tek satır.
//
//	GET /api/entities/clusters                       viewer — Remote Cluster listesi (id/name) + son koşu
//	GET /api/entities?cluster=&type=&namespace=&q=&at=&limit=   viewer — sunucu-taraflı arama (picker kuralı)
//	GET /api/entity?id=&at=                          viewer — varlık + ebeveyn zinciri + çocuk sayıları + ömürler
//	GET /api/entity/services?id=&from&to             viewer — pod/node/ns/wl → servisler + sağlık (entity_seen_5m)
//	GET /api/entity/metrics?id=&from&to              viewer — pod → Thanos CPU/bellek trendi (delegasyon)
//	GET /api/services/{name}/pods?cluster=&from&to   viewer — servisi taşıyan pod'lar + sağlık
//
// Rol kapısı YOK — salt-okunur drill-down; küresel middleware kimliksiz
// isteği 401 yapar ve viewer bu veriyi GÖRMELİ. Bayrak kapalıyken uçlar
// 404 {disabled:true} — mevcut sayfalar etkilenmez. Cluster ZORUNLU
// (design §4 kuralı): cluster'sız liste/servis-pod sorgusu tüm cluster'lara
// yayılmaz; servis→pod'lar cluster'sız verilirse yanıt clusterAmbiguous ilan
// eder. Her cevap serveCached; anahtarlar entity_keys.go (tüm girdiler).

import (
	"context"
	"log"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/cilcenk/coremetry/internal/chstore"
	"github.com/cilcenk/coremetry/internal/entity"
	"github.com/cilcenk/coremetry/internal/thanos"
)

func (s *Server) registerEntityQueryRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/entities/clusters", s.getEntityClusters)
	mux.HandleFunc("GET /api/entities", s.listEntities)
	// entity_id '/' taşır ("pod:<cid>/<ns>/<pod>") — yol segmenti olamaz
	// (Go mux çok-segmentli {id}'yi yalnız sonda kabul eder); ?id= ile.
	mux.HandleFunc("GET /api/entity", s.getEntity)
	mux.HandleFunc("GET /api/entity/services", s.getEntityServices)
	mux.HandleFunc("GET /api/entity/metrics", s.getEntityMetrics)
	mux.HandleFunc("GET /api/entity/containers", s.getEntityContainers) // v0.10.135 — pod konteyner durumları (Thanos KSM)
	mux.HandleFunc("GET /api/services/{name}/pods", s.getServicePods)
}

// entityEnabled — bayrak kapısı: kapalıysa 404 {disabled:true} yazar, false döner.
func (s *Server) entityEnabled(w http.ResponseWriter) bool {
	if s.entitySettings == nil || !s.entitySettings.Resolved().Enabled {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"disabled":true}`))
		return false
	}
	return true
}

// resolveCluster — ?cluster= id ya da ad → (cluster id, span cluster değeri).
func (s *Server) resolveCluster(ref string) (thanos.ClusterConfig, bool) {
	if s.thanos == nil || ref == "" {
		return thanos.ClusterConfig{}, false
	}
	return s.thanos.ClusterByRef(ref)
}

func parseAt(q string) time.Time {
	if q == "" {
		return time.Time{}
	}
	if n, err := strconv.ParseInt(q, 10, 64); err == nil {
		// v0.10.135 — üç birim (ns / ms / s): FE linkleri ms taşır (Date.now
		// ölçeği); ms'yi saniye sanmak 50k yıl ileri bir "an" üretirdi.
		switch {
		case n > 1e15: // ns
			return time.Unix(0, n).UTC()
		case n > 1e11: // ms
			return time.UnixMilli(n).UTC()
		}
		return time.Unix(n, 0).UTC()
	}
	if t, err := time.Parse(time.RFC3339, q); err == nil {
		return t.UTC()
	}
	return time.Time{}
}

func (s *Server) getEntityClusters(w http.ResponseWriter, r *http.Request) {
	if !s.entityEnabled(w) {
		return
	}
	s.serveCached(w, r, "entities:clusters", 15*time.Second, func(ctx context.Context) (any, error) {
		out := []map[string]any{}
		if s.thanos == nil {
			return map[string]any{"clusters": out}, nil
		}
		runs, _ := s.store.EntitySyncRuns(ctx, time.Now().Add(-24*time.Hour), 500)
		last := map[string]entity.Run{}
		for _, ru := range runs { // DESC sıralı: ilk görülen en yeni
			if _, ok := last[ru.ClusterID]; !ok {
				last[ru.ClusterID] = ru
			}
		}
		cfg := s.thanos.CurrentSettings()
		for _, c := range cfg.Clusters {
			if !c.Enabled {
				continue
			}
			m := map[string]any{"id": c.EffectiveID(), "name": c.Name, "spanClusterValue": c.SpanClusterKey()}
			if ru, ok := last[c.EffectiveID()]; ok {
				m["lastRun"] = ru
			}
			out = append(out, m)
		}
		if ru, ok := last[entity.UnmappedClusterID]; ok {
			return map[string]any{"clusters": out, "unmapped": ru}, nil
		}
		return map[string]any{"clusters": out}, nil
	})
}

func (s *Server) listEntities(w http.ResponseWriter, r *http.Request) {
	if !s.entityEnabled(w) {
		return
	}
	q := r.URL.Query()
	c, ok := s.resolveCluster(strings.TrimSpace(q.Get("cluster")))
	if !ok {
		writeJSONError(w, http.StatusBadRequest, "cluster parametresi zorunlu (Remote Cluster id ya da adı)")
		return
	}
	typ, ns, search := strings.TrimSpace(q.Get("type")), strings.TrimSpace(q.Get("namespace")), strings.TrimSpace(q.Get("q"))
	limit := parseInt(q.Get("limit"), 100)
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	at := parseAt(q.Get("at"))
	key := entityListKey(c.EffectiveID(), typ, ns, search, limit, at)
	s.serveCached(w, r, key, 15*time.Second, func(ctx context.Context) (any, error) {
		rows, err := s.store.EntityList(ctx, chstore.EntityListQuery{ClusterID: c.EffectiveID(), Type: typ, Namespace: ns, Search: search, At: at, Limit: limit})
		if err != nil {
			return nil, err
		}
		return map[string]any{"cluster": c.EffectiveID(), "items": rows}, nil
	})
}

func (s *Server) getEntity(w http.ResponseWriter, r *http.Request) {
	if !s.entityEnabled(w) {
		return
	}
	id := strings.TrimSpace(r.URL.Query().Get("id"))
	ref, ok := entity.ParseID(id)
	if !ok {
		writeJSONError(w, http.StatusBadRequest, "geçersiz entity id")
		return
	}
	at := parseAt(r.URL.Query().Get("at"))
	key := entityPivotKey("get", id+"@"+atBucket(at), time.Time{}, time.Time{})
	s.serveCached(w, r, key, 15*time.Second, func(ctx context.Context) (any, error) {
		cur, match, all, err := s.store.EntityLifetimesAt(ctx, id, at)
		if err != nil {
			return nil, err
		}
		if cur == nil {
			return nil, errNotFound
		}
		// v0.10.135 — ölü / o-an-geçersiz kayıt 404 DEĞİL: en yeni ömür +
		// atMatch=false döner; sayfa "artık mevcut değil, son görülme X" +
		// tarihçe gösterir. Zincir ve çocuklar kaydın KENDİ zamanında çözülür
		// (ölü pod'un konteynerleri bugün değil, son görüldüğü anda geçerliydi).
		eff := at
		if eff.IsZero() && cur.ValidTo != nil {
			eff = cur.LastSeen
		}
		parents := s.store.EntityParents(ctx, id, eff)
		children, _ := s.store.EntityChildrenCounts(ctx, ref.ClusterID, id, eff)
		out := map[string]any{"entity": cur, "parents": parents, "children": children, "lifetimes": all, "atMatch": match}
		if ref.Type == entity.TypePod {
			// node: runs_on — kaydın zamanına DEĞEN ilişki (canlı pod: son 1 saat)
			wFrom, wTo := time.Now().Add(-time.Hour), time.Now()
			if !eff.IsZero() {
				wFrom, wTo = eff.Add(-time.Hour), eff.Add(time.Hour)
			}
			if rels, err := s.store.EntityRelations(ctx, ref.ClusterID, entity.RelRunsOn, id, false, wFrom, wTo); err == nil && len(rels) > 0 {
				out["node"] = rels[0].ChildID
			}
			// Kardeş pod'lar: aynı workload, kendisi hariç. Konteynerler: çocuklar.
			if cur.ParentID != "" {
				if sib, err := s.store.EntityList(ctx, chstore.EntityListQuery{ClusterID: ref.ClusterID, Type: entity.TypePod, ParentID: cur.ParentID, ExcludeID: id, At: eff, Limit: 50}); err == nil {
					out["siblings"] = sib
				}
			}
			if ctrs, err := s.store.EntityList(ctx, chstore.EntityListQuery{ClusterID: ref.ClusterID, Type: entity.TypeContainer, ParentID: id, At: eff, Limit: 50}); err == nil {
				out["containers"] = ctrs
			}
		}
		if c, ok := s.thanos.ClusterByID(ref.ClusterID); ok {
			out["cluster"] = map[string]any{"id": c.EffectiveID(), "name": c.Name}
		}
		return out, nil
	})
}

// getEntityServices — pod → servisler; node/namespace/workload → altındaki
// pod'lar → servisler; hepsi entity_seen_5m sağlığıyla.
func (s *Server) getEntityServices(w http.ResponseWriter, r *http.Request) {
	if !s.entityEnabled(w) {
		return
	}
	id := strings.TrimSpace(r.URL.Query().Get("id"))
	ref, ok := entity.ParseID(id)
	if !ok {
		writeJSONError(w, http.StatusBadRequest, "geçersiz entity id")
		return
	}
	from, to := parseFromTo(r, time.Hour)
	c, ok := s.thanos.ClusterByID(ref.ClusterID)
	if !ok {
		writeJSONError(w, http.StatusNotFound, "cluster not configured")
		return
	}
	key := entityPivotKey("services", id, from, to)
	s.serveCached(w, r, key, 30*time.Second, func(ctx context.Context) (any, error) {
		var pods []chstore.EntityRecord
		switch ref.Type {
		case entity.TypePod:
			if cur, _, err := s.store.EntityLifetimes(ctx, id, to); err == nil && cur != nil {
				pods = []chstore.EntityRecord{*cur}
			} else {
				pods = []chstore.EntityRecord{{Type: entity.TypePod, ClusterID: ref.ClusterID, ID: id, Namespace: ref.Namespace, Name: ref.Name}}
			}
		case entity.TypeNode:
			rels, err := s.store.EntityRelations(ctx, ref.ClusterID, entity.RelRunsOn, id, true, from, to)
			if err != nil {
				return nil, err
			}
			for _, rl := range rels {
				if pr, ok := entity.ParseID(rl.ParentID); ok {
					pods = append(pods, chstore.EntityRecord{Type: entity.TypePod, ClusterID: ref.ClusterID, ID: rl.ParentID, Namespace: pr.Namespace, Name: pr.Name})
				}
			}
		case entity.TypeNamespace, entity.TypeWorkload:
			// İnceleme (v0.10.135): `ns:` id'de ParseID namespace'i Name'e
			// koyar — ref.Namespace BOŞ; süzgeç SQL'den düşüyor, LIMIT 500
			// alfabetik ilk namespace'lerde kesiyor, Go süzgeci sıfır
			// bırakıyordu. Süzgeç SQL'de: namespace → namespace = ?,
			// workload → parent_id = ?. 500 tavanı namespace İÇİ.
			q := chstore.EntityListQuery{ClusterID: ref.ClusterID, Type: entity.TypePod, At: to, Limit: 500}
			if ref.Type == entity.TypeNamespace {
				q.Namespace = ref.Name
			} else {
				q.Namespace, q.ParentID = ref.Namespace, id
			}
			list, err := s.store.EntityList(ctx, q)
			if err != nil {
				return nil, err
			}
			pods = append(pods, list...)
		default:
			return nil, errBadRequest
		}
		if len(pods) == 0 {
			return map[string]any{"entity": id, "pods": []any{}, "services": []any{}}, nil
		}
		// Sağlık: pod'ların (ns, pod) çiftleri → entity_seen_5m.
		byNS := map[string][]string{}
		for _, p := range pods {
			byNS[p.Namespace] = append(byNS[p.Namespace], p.Name)
		}
		var aggs []chstore.EntitySeenAgg
		for ns, names := range byNS {
			a, err := s.store.EntitySeenForPods(ctx, c.SpanClusterKey(), ns, names, from, to)
			if err != nil {
				return nil, err
			}
			aggs = append(aggs, a...)
		}
		type svcRow struct {
			Service string  `json:"service"`
			Pods    int     `json:"pods"`
			Spans   int64   `json:"spans"`
			Errors  int64   `json:"errors"`
			AvgMs   float64 `json:"avgMs"`
		}
		svc := map[string]*svcRow{}
		podSet := map[string]map[string]bool{}
		for _, a := range aggs {
			row, ok := svc[a.Service]
			if !ok {
				row = &svcRow{Service: a.Service}
				svc[a.Service] = row
				podSet[a.Service] = map[string]bool{}
			}
			if !podSet[a.Service][a.Namespace+"/"+a.Pod] {
				podSet[a.Service][a.Namespace+"/"+a.Pod] = true
				row.Pods++
			}
			row.AvgMs = (row.AvgMs*float64(row.Spans) + a.AvgMs*float64(a.Spans)) / float64(max64(row.Spans+a.Spans, 1))
			row.Spans += a.Spans
			row.Errors += a.Errors
		}
		services := make([]svcRow, 0, len(svc))
		for _, v := range svc {
			services = append(services, *v)
		}
		sort.Slice(services, func(i, j int) bool { return services[i].Spans > services[j].Spans })
		return map[string]any{"entity": id, "cluster": c.EffectiveID(), "pods": pods, "services": services, "rows": aggs}, nil
	})
}

func max64(a, b int64) int64 {
	if a > b {
		return a
	}
	return b
}

// getEntityMetrics — pod → Thanos CPU/bellek trendi (cluster matcher'lı).
func (s *Server) getEntityMetrics(w http.ResponseWriter, r *http.Request) {
	if !s.entityEnabled(w) {
		return
	}
	id := strings.TrimSpace(r.URL.Query().Get("id"))
	ref, ok := entity.ParseID(id)
	if !ok || ref.Type != entity.TypePod {
		writeJSONError(w, http.StatusBadRequest, "yalnız pod varlıkları için")
		return
	}
	c, ok := s.thanos.ClusterByID(ref.ClusterID)
	if !ok {
		writeJSONError(w, http.StatusNotFound, "cluster not configured")
		return
	}
	from, to := parseFromTo(r, time.Hour)
	from, to, _ = clampThanosWindow(from, to)
	key := entityPivotKey("metrics", id, from, to)
	s.serveCached(w, r, key, 60*time.Second, func(ctx context.Context) (any, error) {
		qctx, cancel := context.WithTimeout(ctx, 10*time.Second)
		defer cancel()
		pts, err := s.thanos.PodTrend(qctx, c, ref.Namespace, ref.Name, from, to)
		if err != nil {
			return nil, err
		}
		if pts == nil {
			pts = []thanos.TrendPoint{}
		}
		return map[string]any{"entity": id, "cluster": c.EffectiveID(), "points": pts}, nil
	})
}

// getServicePods — servisi taşıyan pod'lar + sağlık. cluster verilmezse
// tüm cluster'lar okunur ve yanıt clusterAmbiguous ile ilan eder.
func (s *Server) getServicePods(w http.ResponseWriter, r *http.Request) {
	if !s.entityEnabled(w) {
		return
	}
	name := r.PathValue("name")
	if name == "" {
		writeJSONError(w, http.StatusBadRequest, "service name required")
		return
	}
	from, to := parseFromTo(r, time.Hour)
	clusterRef := strings.TrimSpace(r.URL.Query().Get("cluster"))
	var clusterValue, clusterID string
	if clusterRef != "" {
		c, ok := s.resolveCluster(clusterRef)
		if !ok {
			writeJSONError(w, http.StatusNotFound, "cluster not configured")
			return
		}
		clusterValue, clusterID = c.SpanClusterKey(), c.EffectiveID()
	}
	key := servicePodsKey(name, clusterID, from, to)
	s.serveCached(w, r, key, 30*time.Second, func(ctx context.Context) (any, error) {
		aggs, err := s.store.EntitySeenForService(ctx, name, clusterValue, from, to)
		if err != nil {
			return nil, err
		}
		// span cluster değeri → Remote Cluster id (eşlenemeyen ilan edilir)
		byValue := map[string]thanos.ClusterConfig{}
		for _, c := range s.thanos.CurrentSettings().Clusters {
			if c.Enabled {
				byValue[c.SpanClusterKey()] = c
			}
		}
		type podRow struct {
			chstore.EntitySeenAgg
			ClusterID string                `json:"clusterId,omitempty"`
			EntityID  string                `json:"entityId,omitempty"`
			Entity    *chstore.EntityRecord `json:"entity,omitempty"`
			// v0.10.136 — adım 2: durum (Thanos KSM anlık) + giriş-span latency (ham spans).
			Phase           string  `json:"phase,omitempty"`
			Restarts        int     `json:"restarts"`
			RestartsUnknown bool    `json:"restartsUnknown,omitempty"`
			LastTermReason  string  `json:"lastTermReason,omitempty"`
			CPUCores        float64 `json:"cpuCores,omitempty"`
			MemBytes        float64 `json:"memBytes,omitempty"`
			StatusKnown     bool    `json:"statusKnown,omitempty"`
			EntrySpans      int64   `json:"entrySpans,omitempty"`
			P50Ms           float64 `json:"p50Ms,omitempty"`
			P95Ms           float64 `json:"p95Ms,omitempty"`
			P99Ms           float64 `json:"p99Ms,omitempty"`
		}
		out := make([]podRow, 0, len(aggs))
		clusters := map[string]bool{}
		unmapped := map[string]bool{}
		for _, a := range aggs {
			row := podRow{EntitySeenAgg: a}
			if c, ok := byValue[a.Cluster]; ok {
				row.ClusterID = c.EffectiveID()
				row.EntityID = entity.PodID(c.EffectiveID(), a.Namespace, a.Pod)
				clusters[c.EffectiveID()] = true
				if cur, _, err := s.store.EntityLifetimes(ctx, row.EntityID, to); err == nil && cur != nil {
					row.Entity = cur
				}
			} else {
				unmapped[a.Cluster] = true
			}
			out = append(out, row)
		}
		// v0.10.136 — adım 2 zenginleştirme. (a) Latency: ham spans, giriş-span,
		// pod başına p50/p95/p99 (entity_seen_5m yalnız ortalama taşır); hata
		// best-effort — 0011 kolonu olmayan CH'de yüzdelikler boş kalır, tablo
		// yaşar. (b) Durum: cluster başına TEK Thanos envanter sorgusu, hedefli
		// pod=~ regex (≤200 ad; fazlası statusNotes ile ilan). (c) Zincir:
		// satırların entity ebeveynlerinden workload/namespace özeti.
		statusNotes := []string{}
		var notesMu sync.Mutex
		note := func(n string) { notesMu.Lock(); statusNotes = append(statusNotes, n); notesMu.Unlock() }
		if len(out) > 0 {
			if lat, err := s.store.PodLatencyForService(ctx, name, clusterValue, from, to); err == nil {
				byKey := make(map[[3]string]chstore.PodLatencyRow, len(lat))
				for _, l := range lat {
					byKey[[3]string{l.Cluster, l.Namespace, l.Pod}] = l
				}
				for i := range out {
					if l, ok := byKey[[3]string{out[i].Cluster, out[i].Namespace, out[i].Pod}]; ok {
						out[i].EntrySpans, out[i].P50Ms, out[i].P95Ms, out[i].P99Ms = l.EntrySpans, l.P50Ms, l.P95Ms, l.P99Ms
					}
				}
				if len(lat) >= chstore.PodLatencyLimit {
					note("latency yalnız en yoğun " + strconv.Itoa(chstore.PodLatencyLimit) + " pod için")
				}
			} else {
				log.Printf("[entities] pod latency for %s: %v", name, err)
				note("latency alınamadı (sunucu günlüğü)")
			}
		}
		byCluster := map[string][]int{}
		for i := range out {
			if out[i].ClusterID != "" {
				byCluster[out[i].ClusterID] = append(byCluster[out[i].ClusterID], i)
			}
		}
		// Cluster'lar PARALEL, tek 10 s bütçe (inceleme: ardışık 10 s × N cluster
		// serveCached 30 s TTL'ini aşıyordu). Her goroutine yalnız kendi
		// cluster'ının satır indekslerine yazar — örtüşme yok.
		sctx, scancel := context.WithTimeout(ctx, 10*time.Second)
		var wg sync.WaitGroup
		for cid, idxs := range byCluster {
			c, ok := s.thanos.ClusterByID(cid)
			if !ok {
				continue
			}
			names := make([]string, 0, len(idxs))
			for _, i := range idxs {
				names = append(names, out[i].Pod)
			}
			re, truncated := thanos.PodNamesRegex(names)
			if truncated {
				note(c.Name + ": durum yalnız ilk pod'lar için (seçici tavanı)")
			}
			if re == "" {
				continue
			}
			wg.Add(1)
			go func(c thanos.ClusterConfig, idxs []int, re string) {
				defer wg.Done()
				prows, _, err := s.thanos.PodMetrics(sctx, c, re)
				if err != nil {
					note(c.Name + ": durum alınamadı")
					return
				}
				byPod := make(map[string]thanos.PodRow, len(prows))
				for _, p := range prows {
					byPod[p.Namespace+"/"+p.Pod] = p
				}
				for _, i := range idxs {
					if p, ok := byPod[out[i].Namespace+"/"+out[i].Pod]; ok {
						out[i].Phase, out[i].Restarts, out[i].RestartsUnknown, out[i].LastTermReason = p.Phase, p.Restarts, p.RestartsUnknown, p.LastTermReason
						out[i].CPUCores, out[i].MemBytes, out[i].StatusKnown = p.CPUCores, p.MemBytes, true
					}
				}
			}(c, idxs, re)
		}
		wg.Wait()
		scancel()
		type chainItem struct {
			ID        string `json:"id"`
			Type      string `json:"type"`
			Name      string `json:"name"`
			Kind      string `json:"kind,omitempty"`
			Namespace string `json:"namespace,omitempty"`
			ClusterID string `json:"clusterId"`
			Pods      int    `json:"pods"`
		}
		chainIdx := map[string]int{}
		chain := []chainItem{}
		for _, row := range out {
			if row.Entity == nil || row.Entity.ParentID == "" {
				continue
			}
			pid := row.Entity.ParentID
			if j, ok := chainIdx[pid]; ok {
				chain[j].Pods++
				continue
			}
			pr, ok := entity.ParseID(pid)
			if !ok {
				continue
			}
			ci := chainItem{ID: pid, Type: pr.Type, Name: pr.Name, Kind: pr.Kind, Namespace: pr.Namespace, ClusterID: pr.ClusterID, Pods: 1}
			if pr.Type == entity.TypeNamespace {
				ci.Namespace = pr.Name
			}
			chainIdx[pid] = len(chain)
			chain = append(chain, ci)
		}
		sort.SliceStable(chain, func(a, b int) bool { return chain[a].Pods > chain[b].Pods })
		resp := map[string]any{"service": name, "pods": out, "chain": chain}
		if len(statusNotes) > 0 {
			sort.Strings(statusNotes)
			resp["statusNotes"] = statusNotes
		}
		if clusterID == "" && len(clusters) > 1 {
			ids := make([]string, 0, len(clusters))
			for id := range clusters {
				ids = append(ids, id)
			}
			sort.Strings(ids)
			resp["clusterAmbiguous"] = ids
		}
		if len(unmapped) > 0 {
			vals := make([]string, 0, len(unmapped))
			for v := range unmapped {
				vals = append(vals, v)
			}
			sort.Strings(vals)
			resp["unmappedClusters"] = vals
		}
		return resp, nil
	})
}

// getEntityContainers — v0.10.135 (DETAY SAYFALARI adım 1). Pod'un konteyner
// durumları Thanos/KSM'den anlık (ready / restart / bekleme sebebi / son
// sonlanma sebebi). Thanos hatası 5xx DEĞİL: 200 + error alanı — panel
// "bilinmiyor" der, sayfanın geri kalanı yaşar (vmetrics probe duruşu).
func (s *Server) getEntityContainers(w http.ResponseWriter, r *http.Request) {
	if !s.entityEnabled(w) {
		return
	}
	id := strings.TrimSpace(r.URL.Query().Get("id"))
	ref, ok := entity.ParseID(id)
	if !ok || ref.Type != entity.TypePod {
		writeJSONError(w, http.StatusBadRequest, "pod entity id gerekli")
		return
	}
	c, ok := s.thanos.ClusterByID(ref.ClusterID)
	if !ok {
		writeJSONError(w, http.StatusNotFound, "cluster kaydı yok")
		return
	}
	key := entityPivotKey("containers", id, time.Time{}, time.Time{})
	s.serveCached(w, r, key, 30*time.Second, func(ctx context.Context) (any, error) {
		// Dört ardışık Thanos sorgusu; getEntityMetrics gibi 10 s üst sınır —
		// takılan Querier isteği 60 s'e kadar asmasın (inceleme, v0.10.135).
		qctx, cancel := context.WithTimeout(ctx, 10*time.Second)
		defer cancel()
		rows, err := s.thanos.PodContainers(qctx, c, ref.Namespace, ref.Name)
		if err != nil {
			return map[string]any{"entity": id, "containers": []any{}, "error": err.Error()}, nil
		}
		if rows == nil {
			rows = []thanos.ContainerStatus{}
		}
		return map[string]any{"entity": id, "containers": rows}, nil
	})
}
