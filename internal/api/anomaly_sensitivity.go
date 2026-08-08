// v0.9.826 — anomali dedektörünün EŞİKLERİ operatörün eline geçti.
//
// anomaly_tracked (v0.9.800) hangi metriğin ölçüleceğini verdi; bu blob
// ölçülenin ne zaman OLAY sayılacağını veriyor.
//
// NEDEN VİDA: bu eşikler bugüne kadar altı kez KODDA değişti (v0.8.220
// dwell, v0.9.48 düz-MAD tabanı, v0.9.180 mutlak taban, v0.9.193
// criticalZ 5→6 + P1-only kapısı…) ve her seferinde operatör bir çentik
// ötede aynı duvara çarptı. "Ne kadar sapma olay sayılır" filoya, servis
// hacmine ve gecikme profiline bağlı — kodda tahmin edilecek bir sayı
// değil ve geri dönüş tek tık olmalı.
//
// KABLO: anomaly_tracked emsalinin aynısı — system_settings blob'u +
// boot hidrasyonu + 30 sn yenileme + admin PUT + audit; derlenmiş ayar
// Store'da atomic pointer'da yayınlanır. Dedektör her tarama tikinde
// oradan okur, yani ayar tarayıcıya bir CH okuması EKLEMEDEN canlı kalır.
//
// EVALUATOR'A DOKUNULMADI: operatörün elle kurduğu alarm kuralları
// operatörün malı ve bu vidalar onlara karışmaz.
package api

import (
	"context"
	"encoding/json"
	"log"
	"net/http"
	"time"

	"github.com/cilcenk/coremetry/internal/chstore"
)

// LoadAnomalySensitivity hydrates the detector thresholds from
// system_settings into the Store. Get zaten soft-fail'dir (CH hatası →
// varsayılanlar), yani boot bir ayar okumasına bağlanmıyor.
func (s *Server) LoadAnomalySensitivity(ctx context.Context) {
	s.store.LoadAnomalySensitivity(ctx)
}

// StartAnomalySensitivityRefresh — çok-pod yakınsaması. PUT'u alan pod
// anında takas eder; diğer podlar (dedektörün lideri başka bir pod
// olabilir) bir sonraki tikte görür. Leader gerekmiyor: bu bir OKUMA.
func (s *Server) StartAnomalySensitivityRefresh(ctx context.Context, interval time.Duration) {
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			s.LoadAnomalySensitivity(ctx)
		}
	}
}

// getAnomalySensitivity — GET /api/settings/anomaly-sensitivity.
//
// CANLI yayınlanmış ayardan değil KAYITLI blob'dan okunur: ikisi
// ayrışmış olabilir (bu pod henüz yenilemedi) ve ayar sayfası
// KAYDEDİLENİ göstermeli — operatör ne yazdığını görsün, isteğinin
// hangi pod'a düştüğünü değil (v0.9.797/800 ile aynı gerekçe).
func (s *Server) getAnomalySensitivity(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, s.store.GetAnomalySensitivity(r.Context()))
}

// putAnomalySensitivity validates + persists the thresholds and swaps the
// published pointer on THIS pod in the same request, so the next anomaly
// tick here already uses it (restart yok).
//
// DOĞRULAMA KELEPÇEDE, REDDETMEDE DEĞİL — bilinçli fark. Bir eşik
// alanının "yanlış" değeri yok: 0 meşru (vidayı kapat), büyük değer
// meşru (sertleştir). Yalnız ANLAMSIZ olanlar (negatif, NaN, aralık
// dışı dwell/criticalZ) varsayılanına döner ve operatör kaydettiğini
// yanıtta GÖRÜR. Reddetseydik, tek bir kötü alan yüzünden iyi olan
// diğer dördü de kaydedilmezdi.
func (s *Server) putAnomalySensitivity(w http.ResponseWriter, r *http.Request) {
	var in chstore.AnomalySensitivityConfig
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		http.Error(w, "invalid JSON: "+err.Error(), http.StatusBadRequest)
		return
	}
	cfg := chstore.NormalizeAnomalySensitivity(in)
	if err := s.store.SaveAnomalySensitivity(r.Context(), cfg); err != nil {
		writeErr(w, err)
		return
	}
	s.store.SetAnomalySensitivity(cfg)
	details, _ := json.Marshal(cfg)
	s.audit(r, "settings.update", "anomaly_sensitivity", "anomaly_sensitivity", string(details))
	log.Printf("[settings] anomaly_sensitivity: dwell=%d criticalZ=%.1f", cfg.DwellBuckets, cfg.CriticalZ)
	writeJSON(w, cfg)
}
