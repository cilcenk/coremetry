// v0.9.800 — anomali motorunun İZLEDİĞİ metrik seti operatörün eline
// geçti; request_rate varsayılan olarak KAPALI.
//
// Operator-reported (2026-08-09): request_rate anomalileri false-pozitif.
// Bu filoda istek hacmi kampanya, batch penceresi, nöbet devri gibi
// tamamen normal nedenlerle sıçrıyor ve düşüyor — "istek hızı
// beklenenden farklı" cümlesi operatör için bir OLAY değil. error_rate
// ve p99_ms ise gerçek olay sinyalleri ve olduğu gibi kalıyor.
//
// KARAR: metriği koddan SÖKMEK yerine vida. Hangi sinyalin olay
// sayıldığı filoya bağlı; kodda tahmin edilecek bir şey değil ve geri
// dönüş tek tık olmalı (anomaly_promotion v0.5.70, exception_triage
// v0.9.775 aynı gerekçe).
//
// KABLO: metric_exclusions (v0.9.797) emsali — system_settings blob'u +
// boot hidrasyonu + 30 sn yenileme + admin PUT + audit; derlenmiş set
// Store'da atomic pointer'da yayınlanır. Dedektör (internal/anomaly)
// her tarama tikinde oradan okur, yani ayar tarayıcıya bir CH okuması
// daha EKLEMEDEN canlı kalır.
//
// EVALUATOR'A DOKUNULMADI: orada yerleşik bir request_rate kuralı yok;
// operatörün elle kurduğu alarm kuralları operatörün malı ve bu vida
// onları kapatmaz.
package api

import (
	"context"
	"encoding/json"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/cilcenk/coremetry/internal/chstore"
)

// LoadAnomalyTracked hydrates the tracked-metric set from
// system_settings into the Store. Get zaten soft-fail'dir (CH hatası →
// varsayılanlar), yani boot bir ayar okumasına bağlanmıyor.
func (s *Server) LoadAnomalyTracked(ctx context.Context) {
	s.store.LoadAnomalyTracked(ctx)
}

// StartAnomalyTrackedRefresh — çok-pod yakınsaması. PUT'u alan pod
// anında takas eder; diğer podlar (dedektörün lideri başka bir pod
// olabilir) bir sonraki tikte görür. Leader gerekmiyor: bu bir OKUMA.
func (s *Server) StartAnomalyTrackedRefresh(ctx context.Context, interval time.Duration) {
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			s.LoadAnomalyTracked(ctx)
		}
	}
}

// getAnomalyTracked — GET /api/settings/anomaly-tracked.
//
// CANLI yayınlanmış setten değil KAYITLI blob'dan okunur: ikisi
// ayrışmış olabilir (bu pod henüz yenilemedi) ve ayar sayfası
// KAYDEDİLENİ göstermeli — operatör ne yazdığını görsün, isteğinin
// hangi pod'a düştüğünü değil (v0.9.797 ile aynı gerekçe).
func (s *Server) getAnomalyTracked(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, s.store.GetAnomalyTracked(r.Context()))
}

// putAnomalyTracked validates + persists the set and swaps the
// published pointer on THIS pod in the same request, so the next
// anomaly tick here already uses it (restart yok).
//
// Tek doğrulama kuralı: en az bir metrik açık kalmalı. Anomali
// motorunu tümden kapatmanın yolu bu vida DEĞİL (o Background
// ayarıdır); hepsi kapalı bir set motoru sessizce sıfır metrikle
// çalıştırırdı ve operatör bunu ekranda "anomali yok" diye görürdü —
// sessiz kapanma bu depoda tekrarlayan hata sınıfı.
func (s *Server) putAnomalyTracked(w http.ResponseWriter, r *http.Request) {
	var in chstore.AnomalyTrackedConfig
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		http.Error(w, "invalid JSON: "+err.Error(), http.StatusBadRequest)
		return
	}
	// Fill (kelepçesiz): kanonik olmayan anahtarlar düşer, eksikler
	// varsayılanını alır. "Hepsi kapalı"yı Normalize'dan ÖNCE görmek
	// zorundayız — Normalize onu sessizce varsayılana çevirirdi ve
	// operatör kaydettiğinden başka bir şey görürdü.
	cfg := chstore.FillAnomalyTracked(in)
	if !cfg.Any() {
		http.Error(w, "en az bir metrik izlenmeli — anomali motorunu tümden kapatmak için Background ayarlarını kullanın",
			http.StatusBadRequest)
		return
	}
	if err := s.store.SaveAnomalyTracked(r.Context(), cfg); err != nil {
		writeErr(w, err)
		return
	}
	s.store.SetAnomalyTracked(cfg)
	enabled := strings.Join(cfg.Enabled(), ",")
	details, _ := json.Marshal(cfg)
	s.audit(r, "settings.update", "anomaly_tracked", "anomaly_tracked", string(details))
	log.Printf("[settings] anomaly_tracked: izlenen metrikler = %s", enabled)
	writeJSON(w, cfg)
}
