package api

// rollouts.go — v0.10.200 (ROLLOUTS Faz 3; docs/audits/rollouts-audit.md §9).
//
// api.go BÜYÜMEYECEK kuralı: rota ailesi burada, api.go tek satır.
//
//	GET /api/rollouts?from&to&cluster&namespace&workload&status&kind&limit   viewer — olay listesi (FINAL, started_at DESC)
//	GET /api/rollout?cluster=&namespace=&workload=&revision=&startedAt=       viewer — tekil (bileşik kimlik → ?id= yerine açık anahtar; entities.go:40 emsali)
//	GET /api/rollouts/stats?from&to&cluster&namespace&topN                    viewer — agregat sekmesi (deploy sıklığı, rollback oranı, ort. süre, en çok rollback)
//	GET /api/rollouts/runs                                                     viewer — son reconciler koşuları
//	GET /api/settings/rollouts   PUT /api/settings/rollouts                    admin — bayrak + vidalar (audit)
//
// Bayrak kapalıyken uçlar 404 {disabled:true} (entities.go:52-58 duruşu):
// sayfa "kapalı" der, boş liste sanmaz. serveCached 15/60 s, anahtar TÜM
// girdileri taşır (rollout_keys.go, saf + testli); limit/topN anahtardan
// ÖNCE kelepçelenir. Tail (SSE) bu dosyada: StartRolloutTail yalnız api
// rolünde, kursör updated_at, watermark now64(3) − 3 s, FINAL + LIMIT,
// PublishLocal (köprüsüz — her pod kendi tail'iyle üretir; audit §3 T).

import (
	"context"
	"encoding/json"
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/cilcenk/coremetry/internal/auth"
	"github.com/cilcenk/coremetry/internal/chstore"
	"github.com/cilcenk/coremetry/internal/rollout"
	"github.com/cilcenk/coremetry/internal/sse"
)

func (s *Server) registerRolloutRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/rollouts", s.listRollouts)
	mux.HandleFunc("GET /api/rollout", s.getRollout)
	mux.HandleFunc("GET /api/rollout/detail", s.getRolloutDetail) // v0.10.203 — çekmece (rollout_detail.go)
	mux.HandleFunc("GET /api/rollouts/stats", s.getRolloutStats)
	// runs: koşu hataları ham CH dizeleri taşıyabilir → admin (viewer liste/stats görür)
	mux.Handle("GET /api/rollouts/runs",
		auth.RequireRole(auth.RoleAdmin, http.HandlerFunc(s.getRolloutRuns)))
	mux.Handle("GET /api/settings/rollouts",
		auth.RequireRole(auth.RoleAdmin, http.HandlerFunc(s.getRolloutSettings)))
	mux.Handle("PUT /api/settings/rollouts",
		auth.RequireRole(auth.RoleAdmin, http.HandlerFunc(s.putRolloutSettings)))
}

// SetRollout — main.go bağlar (Set* kablolama deseni).
func (s *Server) SetRollout(cfg *rollout.SettingsService) { s.rolloutCfg = cfg }

// rolloutEnabled — bayrak kapalıysa 404 {disabled:true} yazar.
func (s *Server) rolloutEnabled(w http.ResponseWriter) bool {
	if s.rolloutCfg != nil && s.rolloutCfg.Resolved().Enabled {
		return true
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusNotFound)
	_ = json.NewEncoder(w).Encode(map[string]any{"disabled": true, "error": "rollouts kapalı — Settings → Rollouts → Enable"})
	return false
}

const (
	rolloutListLimitDefault = 100
	rolloutListLimitMax     = 500
	rolloutStatsTopNMax     = 50
	rolloutListTTL          = 15 * time.Second
	rolloutStatsTTL         = 60 * time.Second
)

func (s *Server) listRollouts(w http.ResponseWriter, r *http.Request) {
	if !s.rolloutEnabled(w) {
		return
	}
	q := r.URL.Query()
	from, to := parseFromTo(r, 24*time.Hour)
	f := chstore.RolloutFilter{
		ClusterID: strings.TrimSpace(q.Get("cluster")), Namespace: strings.TrimSpace(q.Get("namespace")),
		Workload: strings.TrimSpace(q.Get("workload")), Status: strings.TrimSpace(q.Get("status")), Kind: strings.TrimSpace(q.Get("kind")),
	}
	// Remote Cluster adı da kabul (id'ye çevir) — FE id gönderir, elle URL ad yazabilir.
	if f.ClusterID != "" {
		if c, ok := s.resolveCluster(f.ClusterID); ok {
			f.ClusterID = c.EffectiveID()
		}
	}
	limit := parseInt(q.Get("limit"), rolloutListLimitDefault)
	if limit <= 0 {
		limit = rolloutListLimitDefault
	}
	if limit > rolloutListLimitMax {
		limit = rolloutListLimitMax // kelepçe: sessiz varsayılana düşürme değil (store emsali)
	}
	key := rolloutsListKey(f, limit, from, to)
	s.serveCached(w, r, key, rolloutListTTL, func(ctx context.Context) (any, error) {
		rows, err := s.store.RolloutList(ctx, f, from, to, limit)
		if err != nil {
			return nil, err
		}
		if rows == nil {
			rows = []chstore.RolloutRow{}
		}
		s.attachProblemsCaused(ctx, rows) // v0.10.244 — D4 feed rozeti (hata = rozet yok)
		resp := map[string]any{"rollouts": rows, "from": from.UnixMilli(), "to": to.UnixMilli(), "limit": limit}
		if len(rows) >= limit {
			resp["capped"] = true
		}
		// MV/kolon yoksa (dış Distributed'da 0012 uygulanmadan) liste boş — ilan.
		// Yalnız boş listede: sağlıklı yolda ekstra CH sorgusu atılmaz.
		if len(rows) == 0 {
			if st := s.rolloutLayerNote(ctx); st != "" {
				resp["note"] = st
			}
		}
		return resp, nil
	})
}

// rolloutLayerNote — degrade kip ilanı (audit §9): son koşu yoksa/hatalıysa
// söyle. run.Error viewer'a AKMAZ (ham CH/driver dizesi host/tablo taşıyabilir);
// ayrıntı /api/rollouts/runs (admin).
func (s *Server) rolloutLayerNote(ctx context.Context) string {
	run, err := s.store.RolloutLastRun(ctx)
	if err != nil {
		return "koşu kaydı okunamadı (geçici CH hatası olabilir; ayrıntı: sunucu logları)"
	}
	if run == nil {
		return "reconciler henüz koşmadı (lider worker + Settings → Rollouts → Enable; dış Distributed'da Admin → ClickHouse → 0012)"
	}
	if run.Status == rollout.RunFailed {
		return "son reconciler koşusu hata verdi — ayrıntı: /api/rollouts/runs (admin)"
	}
	if run.Status == rollout.RunPartial {
		return "son koşu kısmi (eşlenmemiş cluster / tavan) — ayrıntı: /api/rollouts/runs (admin)"
	}
	return ""
}

func (s *Server) getRollout(w http.ResponseWriter, r *http.Request) {
	if !s.rolloutEnabled(w) {
		return
	}
	q := r.URL.Query()
	id := chstore.RolloutID{ClusterID: strings.TrimSpace(q.Get("cluster")), Namespace: strings.TrimSpace(q.Get("namespace")),
		Workload: strings.TrimSpace(q.Get("workload")), Revision: strings.TrimSpace(q.Get("revision"))}
	ms, _ := strconv.ParseInt(q.Get("startedAt"), 10, 64)
	if id.ClusterID == "" || id.Workload == "" || id.Revision == "" || ms <= 0 {
		writeJSONError(w, http.StatusBadRequest, "cluster, workload, revision, startedAt (ms) zorunlu")
		return
	}
	id.StartedAt = time.UnixMilli(ms)
	if c, ok := s.resolveCluster(id.ClusterID); ok {
		id.ClusterID = c.EffectiveID()
	}
	key := rolloutKey(id)
	s.serveCached(w, r, key, rolloutListTTL, func(ctx context.Context) (any, error) {
		row, err := s.store.RolloutByID(ctx, id)
		if err != nil {
			return nil, err
		}
		if row == nil {
			return nil, errNotFound
		}
		return map[string]any{"rollout": row}, nil
	})
}

func (s *Server) getRolloutStats(w http.ResponseWriter, r *http.Request) {
	if !s.rolloutEnabled(w) {
		return
	}
	q := r.URL.Query()
	from, to := parseFromTo(r, 7*24*time.Hour)
	cluster := strings.TrimSpace(q.Get("cluster"))
	if cluster != "" {
		if c, ok := s.resolveCluster(cluster); ok {
			cluster = c.EffectiveID()
		}
	}
	ns := strings.TrimSpace(q.Get("namespace"))
	topN := parseInt(q.Get("topN"), 10)
	if topN <= 0 {
		topN = 10
	}
	if topN > rolloutStatsTopNMax {
		topN = rolloutStatsTopNMax
	}
	key := rolloutStatsKey(cluster, ns, topN, from, to)
	s.serveCached(w, r, key, rolloutStatsTTL, func(ctx context.Context) (any, error) {
		st, err := s.store.RolloutStats(ctx, cluster, ns, from, to, topN)
		if err != nil {
			return nil, err
		}
		return st, nil
	})
}

func (s *Server) getRolloutRuns(w http.ResponseWriter, r *http.Request) {
	if !s.rolloutEnabled(w) {
		return
	}
	s.serveCached(w, r, "rollouts:runs", 10*time.Second, func(ctx context.Context) (any, error) {
		runs, err := s.store.RolloutRuns(ctx, 20)
		if err != nil {
			return nil, err
		}
		if runs == nil {
			runs = []rollout.Run{}
		}
		return map[string]any{"runs": runs}, nil
	})
}

func (s *Server) getRolloutSettings(w http.ResponseWriter, r *http.Request) {
	if s.rolloutCfg == nil {
		writeJSONError(w, http.StatusServiceUnavailable, "rollout settings not wired")
		return
	}
	cfg := s.rolloutCfg.Current()
	res := s.rolloutCfg.Resolved()
	writeJSON(w, map[string]any{"settings": cfg, "resolved": map[string]any{
		"enabled": res.Enabled, "intervalSec": int(res.Interval / time.Second), "bucketSec": int(res.Bucket / time.Second),
		"threshold": res.Threshold, "hysteresis": res.Hysteresis, "exitHysteresis": res.ExitHysteresis,
		"overlapMaxSec": int(res.OverlapMax / time.Second), "lookbackSec": int(res.Lookback / time.Second),
		"weakSignal": res.WeakSignal, "stalledMinSec": int(res.StalledMin / time.Second),
	}, "defaults": rollout.DefaultSettings()})
}

func (s *Server) putRolloutSettings(w http.ResponseWriter, r *http.Request) {
	if s.rolloutCfg == nil {
		writeJSONError(w, http.StatusServiceUnavailable, "rollout settings not wired")
		return
	}
	var in rollout.Settings
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		writeJSONError(w, http.StatusBadRequest, "geçersiz JSON: "+err.Error())
		return
	}
	// Kelepçe okumada (Resolved, entity emsali) ama ANLAŞILMAZ girdi 400:
	// "banana" sessizce 5m olup 200 dönmesin.
	if err := rollout.ValidateSettings(in); err != nil {
		writeJSONError(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := s.rolloutCfg.SavePersisted(r.Context(), s.store, in); err != nil {
		writeErr(w, err)
		return
	}
	s.cacheInvalidatePrefix(r.Context(), "rollouts:")
	s.publishConfigReload(r.Context(), "rollouts") // öteki pod'lar 30 s poll'ü beklemesin (v0.9.237 sınıfı)
	details, _ := json.Marshal(in)
	s.audit(r, "rollouts.settings.update", "settings", rollout.SettingsKey, string(details))
	s.getRolloutSettings(w, r)
}

// ── SSE tail (audit §3 Seçenek T) ─────────────────────────────────────────
//
// Her api pod'u workload_rollouts'u tail'ler: keyset kursör + watermark
// (replika gecikmesi payı), FINAL + LIMIT. Yayın PublishLocal ve tik başına
// TEK olay ({"n": satır} — Plan A: FE invalidation olarak kullanır; satır
// gövdesi yayınlanmaz, ilk toplu reconcile 10k olay basardı). Köprüye
// basılmaz (N pod × N tail = N× teslim olurdu). Bayrak kapalıyken VE kimse
// dinlemiyorken (Subscribers()==0) sorgu atılmaz — updated_at ORDER BY'da
// değil, tail tam-tablo FINAL taramasıdır; 15 s'den sık koşmaz. Tablo yoksa
// (0012 uygulanmadı) hata loglanır ve sonraki tikte yeniden denenir.

const (
	rolloutTailEvery = 15 * time.Second
	// rolloutTailWatermark — updated_at'i worker yazar, api başka replikadan
	// okuyabilir; watermark'tan yeni satırlar sonraki tike bırakılır. Keyset
	// kursör geri dönmez: dar watermark geciken replikada satır kaçırır.
	rolloutTailWatermark = 15 * time.Second
	rolloutTailLimit     = 500
	rolloutTailMaxPages  = 20
)

// StartRolloutTail — yalnız api rolünde çağrılır (main.go).
func (s *Server) StartRolloutTail(ctx context.Context) {
	if s.bus == nil {
		return
	}
	go func() {
		t := time.NewTicker(rolloutTailEvery)
		defer t.Stop()
		// Keyset kursör (chstore.RolloutCursor): bir tikin upsert'i tüm satırlara
		// aynı updated_at'i basar; salt zaman kursörü bir batch'i geçemezdi.
		cursor := chstore.RolloutCursor{UpdatedAt: time.Now().Add(-rolloutTailWatermark)}
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
			}
			if s.rolloutCfg == nil || !s.rolloutCfg.Resolved().Enabled || s.bus.Subscribers() == 0 {
				cursor = chstore.RolloutCursor{UpdatedAt: time.Now().Add(-rolloutTailWatermark)}
				continue
			}
			// Dolu batch → aynı tikte bir sonraki sayfa (kuyruk birikmesin).
			changed := 0
			for page := 0; page < rolloutTailMaxPages; page++ {
				tctx, cancel := context.WithTimeout(ctx, 5*time.Second)
				rows, next, err := s.store.RolloutTail(tctx, cursor, rolloutTailWatermark, rolloutTailLimit)
				cancel()
				if err != nil {
					if ctx.Err() == nil { // kapanış iptali arıza değil
						log.Printf("[rollout] tail: %v", err)
					}
					break
				}
				changed += len(rows)
				cursor = next
				if len(rows) < rolloutTailLimit {
					break
				}
			}
			if changed > 0 {
				// Tik başına TEK olay + TEK önbellek süpürmesi (deploy anları
				// dışında hiç; Plan A: FE olayı görünce yeniden çeker).
				s.bus.PublishLocal(sse.KindRollout, map[string]any{"n": changed})
				s.cacheInvalidatePrefix(ctx, "rollouts:")
			}
		}
	}()
}

// sseDropped — /api/health görünürlüğü (bus nil-güvenli).
func (s *Server) sseDropped() uint64 {
	if s.bus == nil {
		return 0
	}
	return s.bus.Dropped()
}
