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
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/cilcenk/coremetry/internal/auth"
	"github.com/cilcenk/coremetry/internal/chstore"
	"github.com/cilcenk/coremetry/internal/thanos"
)

func (s *Server) registerThanosIdentityRoutes(mux *http.ServeMux) {
	mux.Handle("GET /api/clusters/sources/probe",
		auth.RequireRole(auth.RoleAdmin, http.HandlerFunc(s.probeClusterSource)))
	// v0.10.140 — etiket otomatik algılama (admin; apply=1 kaydeder + audit).
	mux.Handle("POST /api/settings/thanos/detect", auth.RequireRole(auth.RoleAdmin, http.HandlerFunc(s.detectClusterLabel)))
	// v0.10.141 — span cluster değerleri: liste (sayaç + ilk/son görülme + sahip) ve atama.
	mux.Handle("GET /api/settings/thanos/span-clusters", auth.RequireRole(auth.RoleAdmin, http.HandlerFunc(s.listSpanClusterValues)))
	mux.Handle("POST /api/settings/thanos/assign-span-cluster", auth.RequireRole(auth.RoleAdmin, http.HandlerFunc(s.assignSpanClusterValue)))
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
		"ok":          err == nil && series > 0,
		"labelSource": c.ThanosLabelSource,
	}
	// v0.10.140 — etiket boşsa test anında da algıla (yazmaz; öneri döner).
	if label == "" && err == nil {
		if d, derr := s.thanos.DetectClusterLabel(ctx, c); derr == nil {
			out["detected"] = d
		}
	}
	if err != nil {
		out["error"] = err.Error()
	} else if series == 0 && label != "" {
		out["error"] = "matcher eşleşmedi: " + label + `="` + value + `" ile kube_node_info serisi yok`
	}
	writeJSON(w, out)
}

// detectClusterLabel — v0.10.140. POST /api/settings/thanos/detect?cluster=<id|ad>&apply=1
// Kayıt için Thanos etiketini ENJEKSİYONSUZ sorguyla algılar; apply=1 ve
// sonuç belirsiz değilse blob'a yazar (auto + zaman damgası; Reconcile
// teklik kapısından geçer), reload yayınlar, audit yazar. Belirsizlikte
// adaylar döner, hiçbir şey yazılmaz.
func (s *Server) detectClusterLabel(w http.ResponseWriter, r *http.Request) {
	if s.thanos == nil {
		writeJSONError(w, http.StatusServiceUnavailable, "thanos service not available")
		return
	}
	ref := strings.TrimSpace(r.URL.Query().Get("cluster"))
	cur := s.thanos.CurrentSettings()
	idx := -1
	for i, c := range cur.Clusters {
		if c.EffectiveID() == ref || c.Name == ref {
			idx = i
			break
		}
	}
	if idx < 0 {
		writeJSONError(w, http.StatusNotFound, "cluster kaydı yok")
		return
	}
	c := cur.Clusters[idx]
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	d, err := s.thanos.DetectClusterLabel(ctx, c)
	out := map[string]any{"cluster": c.EffectiveID(), "name": c.Name, "applied": false}
	if err != nil {
		out["error"] = err.Error()
		writeJSON(w, out)
		return
	}
	out["detection"] = d
	if r.URL.Query().Get("apply") != "1" {
		writeJSON(w, out)
		return
	}
	// Ağ çağrısı sonrası blob'u TAZE oku (inceleme: 10 s'lik algılama sırasında
	// başka bir PUT'un yazdığı değişiklik ezilmesin — kayıp güncelleme).
	cur = s.thanos.CurrentSettings()
	idx = -1
	for i, cc := range cur.Clusters {
		if cc.EffectiveID() == c.EffectiveID() {
			idx = i
			break
		}
	}
	if idx < 0 {
		out["error"] = "kayıt algılama sırasında silindi"
		writeJSON(w, out)
		return
	}
	next, err := thanos.ApplyDetection(cur.Clusters[idx], d, time.Now())
	if err != nil {
		out["error"] = err.Error()
		writeJSON(w, out)
		return
	}
	in := cur
	in.Clusters = append([]thanos.ClusterConfig(nil), cur.Clusters...)
	in.Clusters[idx] = next
	merged, err := thanos.ReconcileClusterSettings(in, cur)
	if err != nil {
		out["error"] = err.Error() // teklik: aynı etiket çifti başka kayıtta
		writeJSON(w, out)
		return
	}
	if err := s.thanos.SavePersisted(context.WithoutCancel(r.Context()), s.store, merged); err != nil {
		writeErr(w, err)
		return
	}
	s.publishConfigReload(r.Context(), "thanos")
	s.thanos.ResetLabelChecks(context.WithoutCancel(r.Context()), s.store)
	go s.thanos.LabelCheckTickPersist(context.WithoutCancel(r.Context()), s.store) // taze denetim (bayat uyarı kalmasın)
	s.cacheInvalidate(context.WithoutCancel(r.Context()), "thanos:span-clusters")
	s.audit(r, "settings.thanos.detect", "settings", c.EffectiveID(),
		fmt.Sprintf("cluster=%s label=%s value=%q series=%d", c.Name, d.Label, d.Value, d.Series))
	out["applied"] = true
	writeJSON(w, out)
}

// autoDetectNewClusterLabels — PUT kancası (saf değil: Thanos'a gider).
// Yalnız cur'da OLMAYAN (yeni) ve etiketi boş, kaynağı manual olmayan
// etkin kayıtlar; teklik ihlalinde algılama atlanır (Reconcile hatası
// yutulmaz, sadece o kayıt elle kalır).
func (s *Server) autoDetectNewClusterLabels(ctx context.Context, in, cur thanos.Settings) thanos.Settings {
	known := map[string]bool{}
	for _, c := range cur.Clusters {
		known[c.EffectiveID()] = true
		known["name:"+c.Name] = true // id'siz yeniden adlandırma "yeni" sayılmasın (inceleme)
	}
	// Toplam bütçe 6 s (kayıt başına değil): PUT'u Thanos gecikmesine rehin
	// vermez; süre dolunca kalan kayıtlar elle kalır (UI "Detect label").
	bctx, bcancel := context.WithTimeout(ctx, 6*time.Second)
	defer bcancel()
	changed := false
	for i, c := range in.Clusters {
		if known[c.EffectiveID()] || known["name:"+c.Name] || !c.Enabled || c.URL == "" || c.ThanosLabelName != "" || c.ThanosLabelSource == "manual" {
			continue
		}
		if bctx.Err() != nil {
			break
		}
		d, err := s.thanos.DetectClusterLabel(bctx, c)
		if err != nil || d.Ambiguous || d.Label == "" {
			continue
		}
		next, err := thanos.ApplyDetection(c, d, time.Now())
		if err != nil {
			continue
		}
		trial := in
		trial.Clusters = append([]thanos.ClusterConfig(nil), in.Clusters...)
		trial.Clusters[i] = next
		if _, err := thanos.ReconcileClusterSettings(trial, cur); err != nil {
			continue
		}
		in.Clusters[i] = next
		changed = true
	}
	if changed {
		if merged, err := thanos.ReconcileClusterSettings(in, cur); err == nil {
			return merged
		}
	}
	return in
}

// listSpanClusterValues — v0.10.141. Son 7 günün span cluster değerleri
// (entity_seen_5m; yoksa spans 1 saat) + her değerin sahibi (Remote Cluster)
// ya da EŞLEŞMEMİŞ. 5 dk cache (admin yüzeyi; sorgu MV üzerinden ucuz).
func (s *Server) listSpanClusterValues(w http.ResponseWriter, r *http.Request) {
	if s.thanos == nil {
		writeJSONError(w, http.StatusServiceUnavailable, "thanos service not available")
		return
	}
	s.serveCached(w, r, "thanos:span-clusters", 5*time.Minute, func(ctx context.Context) (any, error) {
		res, err := s.store.EntitySeenClusterValues(ctx, time.Now().Add(-7*24*time.Hour))
		if err != nil {
			return nil, err
		}
		cfg := s.thanos.CurrentSettings()
		type row struct {
			chstore.SeenClusterValue
			OwnerID   string `json:"ownerId,omitempty"`
			OwnerName string `json:"ownerName,omitempty"`
		}
		rows := make([]row, 0, len(res.Rows))
		unmapped := 0
		for _, v := range res.Rows {
			x := row{SeenClusterValue: v}
			if o, ok := thanos.SpanClusterOwner(cfg, v.Value); ok {
				x.OwnerID, x.OwnerName = o.EffectiveID(), o.Name
			} else {
				unmapped++
			}
			rows = append(rows, x)
		}
		return map[string]any{"rows": rows, "unmapped": unmapped, "source": res.Source, "since": res.Since}, nil
	})
}

// assignSpanClusterValue — v0.10.141. Bir span cluster değerini bir Remote
// Cluster kaydına bağlar (kalıcı; teklik Reconcile'da — çakışma 200 +
// conflict + sahip, probe duruşu). backfill=true: entity ayarına
// BackfillUntil (şimdi+10 dk) yazılır → lider Tick'i 24 saatlik span
// geçişiyle koşar (rol-güvenli); bu pod'da syncer varsa hemen bir tick.
func (s *Server) assignSpanClusterValue(w http.ResponseWriter, r *http.Request) {
	if s.thanos == nil {
		writeJSONError(w, http.StatusServiceUnavailable, "thanos service not available")
		return
	}
	var in struct {
		Value     string `json:"value"`
		ClusterID string `json:"clusterId"`
		Backfill  bool   `json:"backfill"`
	}
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		writeJSONError(w, http.StatusBadRequest, "geçersiz JSON: "+err.Error())
		return
	}
	in.Value, in.ClusterID = strings.TrimSpace(in.Value), strings.TrimSpace(in.ClusterID)
	if in.Value == "" || in.ClusterID == "" {
		writeJSONError(w, http.StatusBadRequest, "value ve clusterId zorunlu")
		return
	}
	cur := s.thanos.CurrentSettings()
	if o, ok := thanos.SpanClusterOwner(cur, in.Value); ok && o.EffectiveID() != in.ClusterID {
		writeJSON(w, map[string]any{"ok": false, "conflict": true, "ownerId": o.EffectiveID(), "ownerName": o.Name,
			"error": fmt.Sprintf("span cluster değeri %q zaten %q kaydına bağlı; bir değer aynı anda tek kayda bağlanabilir", in.Value, o.Name)})
		return
	}
	idx := -1
	for i, c := range cur.Clusters {
		if c.EffectiveID() == in.ClusterID {
			idx = i
			break
		}
	}
	if idx < 0 {
		writeJSONError(w, http.StatusNotFound, "cluster kaydı yok")
		return
	}
	next := cur
	next.Clusters = append([]thanos.ClusterConfig(nil), cur.Clusters...)
	c := next.Clusters[idx]
	// AÇIK değerlere ekle (SpanClusterKeys değil: Name yedeğini listeye
	// yazmak yeniden adlandırmayı kırar — v0.10.139 dersi). Aynı değer zaten
	// bu kayıttaysa idempotent (Reconcile tekilleştirir).
	// Kaydın hiç açık değeri yoksa örtük Name anahtarı da AÇIKÇA korunur —
	// aksi halde ilk atama Name eşleşmesini sessizce koparırdı (inceleme).
	// Bilinçli operatör eylemi: yeniden adlandırmada eski ad tarihsel
	// span'ler için açık değer olarak kalır (istenen).
	vals := c.ExplicitSpanClusterValues()
	if len(vals) == 0 && c.Name != "" {
		vals = []string{c.Name}
	}
	c.SpanClusterValues = append(vals, in.Value)
	next.Clusters[idx] = c
	merged, err := thanos.ReconcileClusterSettings(next, cur)
	if err != nil {
		writeJSONError(w, http.StatusConflict, err.Error())
		return
	}
	if err := s.thanos.SavePersisted(r.Context(), s.store, merged); err != nil {
		writeErr(w, err)
		return
	}
	s.publishConfigReload(r.Context(), "thanos")
	s.cacheInvalidate(context.WithoutCancel(r.Context()), "thanos:span-clusters") // sahip değişti; 5 dk cache bayat kalmasın
	backfill := "none"
	switch {
	case !in.Backfill:
	case s.entitySettings == nil || !s.entitySettings.Resolved().Enabled:
		backfill = "skipped: entity katmanı kapalı" // inceleme: kapalıyken "running" yalan olurdu
	default:
		es := s.entitySettings.Current()
		es.BackfillUntil = time.Now().Add(10 * time.Minute).UnixMilli()
		es.BackfillValue = in.Value // yalnız bu değer; küresel 24 s pencere yok (inceleme)
		if err := s.entitySettings.SavePersisted(context.WithoutCancel(r.Context()), s.store, es); err == nil {
			s.publishConfigReload(r.Context(), "entities")
			backfill = "scheduled-24h"
			if s.entitySync != nil {
				go func(ctx context.Context) {
					if ran, why := s.entitySync.TryTick(ctx); !ran {
						log.Printf("[entities] backfill anlık tick atlandı (%s); lider tick'i alacak", why)
					}
				}(context.WithoutCancel(r.Context()))
				backfill = "scheduled-24h (tick tetiklendi)"
			}
		} else {
			backfill = "failed: " + err.Error()
		}
	}
	s.audit(r, "settings.thanos.assign_span_cluster", "settings", in.ClusterID,
		fmt.Sprintf("value=%q cluster=%s backfill=%s", in.Value, c.Name, backfill))
	writeJSON(w, map[string]any{"ok": true, "clusterId": c.EffectiveID(), "clusterName": c.Name, "values": merged.Clusters[idx].SpanClusterKeys(), "backfill": backfill})
}
