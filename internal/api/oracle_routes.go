package api

// oracle_routes.go — v0.10.580, Oracle hata tablosu AŞAMA 1 (audit:
// docs/audit/oracle-error-log-2026-09-09.md §7). Yalnız datasource
// tanımı, credential ve bağlantı testi; poller / metrik / Problem
// Aşama 2-3'te.
//
// api.go'ya SIFIR satır: kayıt route_registry.go defterine init()'ten
// düşüyor (preferences_routes.go emsali, v0.10.247). buildMux defteri ad
// sırasıyla boşaltır ve TestMuxRoutePatterns çakışmayı görür.
//
//	GET  /api/settings/oracle       admin — snapshot (şifre MASKELİ,
//	                                 passwordRef bir referans olduğu için görünür)
//	PUT  /api/settings/oracle       admin — tüm liste atomik; audit
//	                                 settings.oracle.update
//	POST /api/settings/oracle/test  admin — formdaki TEK kaynağı KAYDETMEDEN
//	                                 dener; başarısızlıkta 200 + ok:false
//	GET  /api/oracle/status         her rol — kaynak başına ad/etkin/son hata
//	                                 (şifresiz); viewer state'i GÖRMELİ
//
// İlk üçü admin (influx/vmetrics gerekçesi): kaydedilen credential
// operatörün üretim veritabanını okur, GET'te bile host/şema/tablo var.
//
// serveCached YOK: durum ucu bellek-içi ve pod-yerel (CH round-trip yok),
// influx durum ucundaki "işçi belleği CACHE DIŞI" duruşunun aynısı. Cache
// burada yalnız bayat bir rozet üretirdi.

import (
	"context"
	"encoding/json"
	"net/http"
	"time"

	"github.com/cilcenk/coremetry/internal/auth"
	"github.com/cilcenk/coremetry/internal/oracle"
)

func init() { registerRoutesExtra("oracle", (*Server).registerOracleRoutes) }

func (s *Server) registerOracleRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/settings/oracle", auth.RequireRole(auth.RoleAdmin, s.getOracleSettings))
	mux.HandleFunc("PUT /api/settings/oracle", auth.RequireRole(auth.RoleAdmin, s.putOracleSettings))
	mux.HandleFunc("POST /api/settings/oracle/test", auth.RequireRole(auth.RoleAdmin, s.testOracleSource))
	mux.HandleFunc("GET /api/oracle/status", s.getOracleStatus)
}

// SetOracle — Oracle kaynak servisi (v0.10.580; main.go her rolde çağırır,
// api pod'u Settings'i servis ediyor). nil-safe: handler'lar 503.
func (s *Server) SetOracle(o *oracle.Service) { s.oracle = o }

// oracleStore — s.store'u dar arayüze GÜVENLİ çevirir. Doğrudan geçirmek
// TİPLİ-NİL üretir: nil *chstore.Store bir arayüz değerine sarıldığında
// `store == nil` YANLIŞ döner ve paketin nil-guard'ı atlanıp ilk alan
// erişiminde panic'lenir.
func (s *Server) oracleStore() oracle.SettingsStore {
	if s.store == nil {
		return nil
	}
	return s.store
}

func (s *Server) getOracleSettings(w http.ResponseWriter, r *http.Request) {
	if s.oracle == nil {
		http.Error(w, "oracle not available", http.StatusServiceUnavailable)
		return
	}
	writeJSON(w, s.oracle.Snapshot())
}

func (s *Server) putOracleSettings(w http.ResponseWriter, r *http.Request) {
	if s.oracle == nil {
		http.Error(w, "oracle not available", http.StatusServiceUnavailable)
		return
	}
	var in oracle.Settings
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		writeJSONError(w, http.StatusBadRequest, "geçersiz JSON: "+err.Error())
		return
	}
	cfg, err := oracle.Normalize(in, s.oracle.CurrentSettings(), oracle.NewSourceID)
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, err.Error())
		return
	}
	// WithoutCancel: istemci cevabı beklemeden kapatırsa yazım yarıda
	// kalmasın (thanos_handlers.go dersi).
	if err := s.oracle.SavePersisted(context.WithoutCancel(r.Context()), s.oracleStore(), cfg); err != nil {
		writeErr(w, err)
		return
	}
	s.publishConfigReload(r.Context(), "oracle")
	names := make([]string, 0, len(cfg.Sources))
	enabled := 0
	for _, src := range cfg.Sources {
		names = append(names, src.Name)
		if src.Enabled {
			enabled++
		}
	}
	// details'a ŞİFRE/DSN girmez — yalnız sayılar ve adlar.
	details, _ := json.Marshal(map[string]any{
		"sources": len(cfg.Sources), "enabled": enabled, "names": names,
	})
	s.audit(r, "settings.oracle.update", "settings", "oracle_sources", string(details))
	writeJSON(w, s.oracle.Snapshot())
}

func (s *Server) testOracleSource(w http.ResponseWriter, r *http.Request) {
	if s.oracle == nil {
		http.Error(w, "oracle not available", http.StatusServiceUnavailable)
		return
	}
	var in oracle.SourceConfig
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		writeJSONError(w, http.StatusBadRequest, "geçersiz JSON: "+err.Error())
		return
	}
	// Kapalı bir taslağı da denemek meşru: doğrulama etkin-kaynak
	// kurallarıyla koşsun ki eksik alan testte değil formda görünsün.
	in.Enabled = true
	cfg, err := oracle.Normalize(oracle.Settings{Sources: []oracle.SourceConfig{in}}, s.oracle.CurrentSettings(), oracle.NewSourceID)
	if err != nil {
		// Doğrulama hatası da BAŞARILI bir cevaptır: 200 + ok:false.
		writeJSON(w, oracle.TestResult{OK: false, Error: err.Error(), Columns: []string{}})
		return
	}
	writeJSON(w, s.oracle.Test(r.Context(), cfg.Sources[0]))
}

// oracleStatusPayload — Aşama 1 dürüstlüğü: poller YOK, dolayısıyla
// "son hata" bu pod'da koşmuş bağlantı testinin izidir. Uydurma bir
// "sağlıklı" satırı basmıyoruz.
type oracleStatusPayload struct {
	// v0.10.601 — worker liderinin poll durumu (system_settings blobu; influx
	// v0.10.333 duruşu). nil = henüz yayın yok.
	Poll        *oracle.WorkerStatusSnapshot `json:"poll,omitempty"`
	Sources     []oracle.SourceStatus        `json:"sources"`
	GeneratedAt int64                        `json:"generatedAt"`
}

func (s *Server) getOracleStatus(w http.ResponseWriter, r *http.Request) {
	if s.oracle == nil {
		http.Error(w, "oracle not available", http.StatusServiceUnavailable)
		return
	}
	writeJSON(w, oracleStatusPayload{
		Sources:     s.oracle.Status(),
		Poll:        s.oraclePollStatus(r.Context()),
		GeneratedAt: time.Now().UnixMilli(),
	})
}

// oraclePollStatus — v0.10.601: poll işçisi yalnız worker liderinde koşar;
// Settings'i servis eden API pod'u durumu blobdan okur. Hata = nil (durum
// ucu poll blobu yüzünden 500 vermez).
func (s *Server) oraclePollStatus(ctx context.Context) *oracle.WorkerStatusSnapshot {
	if s.store == nil {
		return nil
	}
	raw, err := s.store.GetSetting(ctx, oracle.WorkerStatusKey)
	if err != nil {
		return nil
	}
	snap, ok := oracle.DecodeWorkerStatus(raw)
	if !ok {
		return nil
	}
	return snap
}
