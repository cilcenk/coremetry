package api

// influx_routes.go — v0.10.222 (Influx D1, docs/audit/influx-integration.md
// §10; operatör onayı 2026-09-01).
//
// api.go BÜYÜMEYECEK kuralı (registerVMetricsRoutes emsali): Influx kaynak
// yönetiminin rotaları + handler'ları burada, api.go tek satır register
// çağrısıyla büyür. Dosya adı spec'te böyle istendi (influx_routes.go);
// handler'lar ayrı dosyaya çıkacak kadar çok değil (3).
//
//	GET  /api/settings/influx       admin — snapshot (tokenRef bir REFERANS,
//	                                 görünür; tokenResolved rozeti)
//	PUT  /api/settings/influx       admin — tüm liste atomik (thanos deseni);
//	                                 audit settings.influx.update
//	POST /api/settings/influx/test  admin — formdaki TEK kaynağı KAYDETMEDEN
//	                                 dener; 200 + ok:false başarısızlıkta
//	GET  /api/influx/status         her rol — kaynak başına metric_points izi
//	                                 (son 1 saat, CH'den) + bu pod'daki işçinin
//	                                 bellek durumu (yalnız worker/all rolünde)
//
// Üçü de admin (vmetrics gerekçesi): kaydedilen tokenRef operatörün Influx
// org'unu okur; GET'te bile kaynak URL'leri ve sorgular var.
//
// Neden merge yok (vmetrics'in "boş token saklıyı korur" kuralı): burada
// secret saklanmıyor, tokenRef görünür bir referans — form onu geri alır
// ve aynen yollar. Sunucu sahipli tek şey ID; onu influx.Normalize önceki
// kayıttan taşır.

import (
	"context"
	"encoding/json"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/cilcenk/coremetry/internal/auth"
	"github.com/cilcenk/coremetry/internal/chstore"
	"github.com/cilcenk/coremetry/internal/influx"
)

func (s *Server) registerInfluxRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/settings/influx", auth.RequireRole(auth.RoleAdmin, s.getInfluxSettings))
	mux.HandleFunc("PUT /api/settings/influx", auth.RequireRole(auth.RoleAdmin, s.putInfluxSettings))
	mux.HandleFunc("POST /api/settings/influx/test", auth.RequireRole(auth.RoleAdmin, s.testInfluxSource))
	// v0.10.223 (D2) — durum salt-okunur; viewer da "veri geliyor mu"yu görsün.
	mux.HandleFunc("GET /api/influx/status", s.getInfluxStatus)
}

func (s *Server) getInfluxSettings(w http.ResponseWriter, r *http.Request) {
	if s.influx == nil {
		http.Error(w, "influx not available", http.StatusServiceUnavailable)
		return
	}
	writeJSON(w, s.influx.Snapshot())
}

func (s *Server) putInfluxSettings(w http.ResponseWriter, r *http.Request) {
	if s.influx == nil {
		http.Error(w, "influx not available", http.StatusServiceUnavailable)
		return
	}
	var in influx.Settings
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		writeJSONError(w, http.StatusBadRequest, "geçersiz JSON: "+err.Error())
		return
	}
	cfg, err := influx.Normalize(in, s.influx.CurrentSettings(), influx.NewSourceID)
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, err.Error())
		return
	}
	// WithoutCancel: istemci cevabı beklemeden kapatırsa yazım yarıda
	// kalmasın (thanos_handlers.go:787 dersi).
	if err := s.influx.SavePersisted(context.WithoutCancel(r.Context()), s.store, cfg); err != nil {
		writeErr(w, err)
		return
	}
	s.publishConfigReload(r.Context(), "influx")
	snap := s.influx.Snapshot()
	names := make([]string, 0, len(cfg.Sources))
	enabled := 0
	for _, src := range cfg.Sources {
		names = append(names, src.Name)
		if src.Enabled {
			enabled++
		}
	}
	details, _ := json.Marshal(map[string]any{
		"sources": len(cfg.Sources), "enabled": enabled, "names": names,
	})
	s.audit(r, "settings.influx.update", "settings", "influx_sources", string(details))
	writeJSON(w, snap)
}

func (s *Server) testInfluxSource(w http.ResponseWriter, r *http.Request) {
	if s.influx == nil {
		http.Error(w, "influx not available", http.StatusServiceUnavailable)
		return
	}
	var in influx.SourceConfig
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		writeJSONError(w, http.StatusBadRequest, "geçersiz JSON: "+err.Error())
		return
	}
	// Kapalı bir taslağı da denemek meşru: doğrulama etkin-kaynak kurallarıyla.
	in.Enabled = true
	cfg, err := influx.Normalize(influx.Settings{Sources: []influx.SourceConfig{in}}, s.influx.CurrentSettings(), influx.NewSourceID)
	if err != nil {
		writeJSON(w, influx.TestResult{OK: false, Error: err.Error(), Queries: []influx.QueryProbe{}})
		return
	}
	writeJSON(w, s.influx.Test(r.Context(), cfg.Sources[0]))
}

// influxStatusSource — durum satırı: ayar + CH izi + (varsa) işçi belleği.
type influxStatusSource struct {
	ID          string               `json:"id"`
	Name        string               `json:"name"`
	Enabled     bool                 `json:"enabled"`
	LastPointAt int64                `json:"lastPointAt,omitempty"`
	Points1h    uint64               `json:"points1h"`
	Series1h    uint64               `json:"series1h"`
	Worker      *influx.SourceStatus `json:"worker,omitempty"`
}

type influxStatusPayload struct {
	Sources         []influxStatusSource `json:"sources"`
	WorkerOnThisPod bool                 `json:"workerOnThisPod"`
	// v0.10.333 — işçi başka pod'daysa paylaşılan durumdan (system_settings).
	WorkerRemote    bool   `json:"workerRemote,omitempty"`
	WorkerPod       string `json:"workerPod,omitempty"`
	WorkerUpdatedAt int64  `json:"workerUpdatedAt,omitempty"`
	GeneratedAt     int64  `json:"generatedAt"`
}

// influxStatusKey — SAF: TÜM girdiler (kaynak adları, sıralı) anahtarda.
// Ad listesi ≤20 ve kısa; hash yerine düz katılım okunur ve kararlı.
func influxStatusKey(names []string) string {
	sorted := append([]string(nil), names...)
	sort.Strings(sorted)
	return "influx:status:" + strings.Join(sorted, ",")
}

func (s *Server) getInfluxStatus(w http.ResponseWriter, r *http.Request) {
	if s.influx == nil {
		http.Error(w, "influx not available", http.StatusServiceUnavailable)
		return
	}
	snap := s.influx.Snapshot()
	names := make([]string, 0, len(snap.Sources))
	for _, src := range snap.Sources {
		names = append(names, src.Name)
	}
	// İşçi belleği CACHE DIŞI: pod-yerel ve anlık; CH izi 15 s serveCached.
	s.serveCached(w, r, influxStatusKey(names), 15*time.Second, func(ctx context.Context) (any, error) {
		var rows []chstore.InfluxIngestRow
		if s.store != nil && len(names) > 0 {
			got, err := s.store.InfluxIngestStatus(ctx, names)
			if err != nil {
				return nil, err
			}
			rows = got
		}
		byName := map[string]chstore.InfluxIngestRow{}
		for _, row := range rows {
			byName[row.Source] = row
		}
		var wst map[string]influx.SourceStatus
		out := influxStatusPayload{Sources: make([]influxStatusSource, 0, len(snap.Sources)),
			WorkerOnThisPod: s.influxWorker != nil, GeneratedAt: time.Now().UnixMilli()}
		if s.influxWorker != nil {
			wst = map[string]influx.SourceStatus{}
			for _, st := range s.influxWorker.Status() {
				wst[st.SourceID] = st
			}
		} else if s.store != nil {
			// v0.10.333 — işçi bu pod'da değil: worker liderinin yayınladığı
			// paylaşılan durum (system_settings). Yayın hiç yoksa işçi hiç
			// koşmamış demektir; kart bunu söyler.
			if raw, err := s.store.GetSetting(ctx, influx.WorkerStatusKey); err == nil {
				if snapW, ok := influx.DecodeWorkerStatus(raw); ok {
					wst = map[string]influx.SourceStatus{}
					for _, st := range snapW.Sources {
						wst[st.SourceID] = st
					}
					out.WorkerRemote = true
					out.WorkerPod = snapW.Pod
					out.WorkerUpdatedAt = snapW.UpdatedAt
				}
			}
		}
		for _, src := range snap.Sources {
			row := influxStatusSource{ID: src.ID, Name: src.Name, Enabled: src.Enabled}
			if ir, ok := byName[src.Name]; ok {
				row.LastPointAt, row.Points1h, row.Series1h = ir.LastPointAt, ir.Points1h, ir.Series1h
			}
			if st, ok := wst[src.ID]; ok {
				stc := st
				row.Worker = &stc
			}
			out.Sources = append(out.Sources, row)
		}
		return out, nil
	})
}
