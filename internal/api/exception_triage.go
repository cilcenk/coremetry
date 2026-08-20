// v0.9.775 — exception triyaj pencereleri operatörün eline geçti.
//
// Operator-reported, BU SINIFTA ÜÇÜNCÜ vaka:
//
//	v0.9.627  12 dakikada 11.260 olay P2 göründü  → P1 tazeliği 5dk → 1sa
//	v0.9.699  101.132 olay / 2dk58sn P3'e düştü   → uçurum yerine basamak
//	2026-08-08  1.575'lik patlama 22:37'de bitti, operatör 00:22'de baktı
//	            (1sa 45dk) → P2 göründü; beklenti P1.
//	            191'lik SQLTimeout 1sa sınırını aşınca P2 → P3 oldu;
//	            beklenti P2.
//
// Her seferinde aynı düzeltmeyi yaptım: sabiti bir çentik öteledim. Her
// seferinde operatör bir çentik ötede aynı duvara çarptı. Basamak
// felsefesi (v0.9.699) doğru — taze P1, aynı gün P2, sonrası P3 — kusur
// basamağın YERİNİN kodda gömülü olmasıydı. Bu sürüm iki şey yapıyor:
// varsayılan P1 penceresini 4 saate çıkarıyor (problem tarafındaki
// "critical open ≥4h → P1" ile simetrik) ve pencereleri
// system_settings'e taşıyor ki dördüncü vaka bir sürüm değil bir ayar
// değişikliği olsun.
package api

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"sync/atomic"
	"time"

	"github.com/cilcenk/coremetry/internal/chstore"
)

// exceptionTriageCfg — süreç-genelinde tek kopya, atomik.
//
// exceptionPriority satır BAŞINA çağrılıyor (inbox listesi 200 satır
// olabilir); oradan bir CH okuması yapmak ayarı hot-path'e sokardı.
// Bunun yerine blob boot'ta hidrate edilir, PUT'ta güncellenir ve
// çok-pod kurulumlarda 30 sn'lik yenileme döngüsüyle yakınsar — tempo /
// copilot / ldap servislerinin StartConfigRefresh deseniyle aynı.
var exceptionTriageCfg atomic.Pointer[chstore.ExceptionTriageConfig]

// currentExceptionTriage — hiç hidrate edilmemişse varsayılanlar.
// Test ikili (binary) hiçbir zaman Store'a bağlanmaz, yani saf öncelik
// testleri bu daldan geçer.
func currentExceptionTriage() chstore.ExceptionTriageConfig {
	if c := exceptionTriageCfg.Load(); c != nil {
		return *c
	}
	return chstore.DefaultExceptionTriage()
}

// setExceptionTriage — normalize EDİLMİŞ hâlini yayınlar, böylece
// okuyucular hiçbir zaman ters basamaklı bir config görmez.
func setExceptionTriage(c chstore.ExceptionTriageConfig) {
	n := chstore.NormalizeExceptionTriage(c)
	exceptionTriageCfg.Store(&n)
}

// LoadExceptionTriage hydrates the package-global from system_settings.
// Hata hâlinde sessizce varsayılanlarda kalır (Get zaten soft-fail'dir)
// — boot'u bir ayar okumasına bağlamıyoruz.
func (s *Server) LoadExceptionTriage(ctx context.Context) {
	setExceptionTriage(s.store.GetExceptionTriage(ctx))
}

// StartExceptionTriageRefresh — çok-pod yakınsaması. Pod A'daki PUT
// kendi global'ini anında günceller; diğer podlar bir sonraki tikte
// görür. Leader gerektirmiyor: bu bir OKUMA, yan etkisi yok.
func (s *Server) StartExceptionTriageRefresh(ctx context.Context, interval time.Duration) {
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			s.LoadExceptionTriage(ctx)
		}
	}
}

// getExceptionTriage returns the live triage windows. Hiç kaydedilmemiş
// bir kurulum v0.9.775 varsayılanlarını görür.
func (s *Server) getExceptionTriage(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, s.store.GetExceptionTriage(r.Context()))
}

// putExceptionTriage validates + persists the windows and swaps the
// package-global in the same request, so the next /api/inbox read on
// THIS pod already uses them (restart yok).
//
// Sınırlar üst uçta bilerek geniş: "bende bir patlama 12 saat boyunca
// P1 kalsın" gerçek bir politika, yazım hatası değil. Alt uç 1 saat,
// çünkü daha kısası v0.9.627'nin düzelttiği kusuru geri getirir.
func (s *Server) putExceptionTriage(w http.ResponseWriter, r *http.Request) {
	var c chstore.ExceptionTriageConfig
	if err := json.NewDecoder(r.Body).Decode(&c); err != nil {
		http.Error(w, "invalid JSON: "+err.Error(), http.StatusBadRequest)
		return
	}
	if c.P1FreshHours < 1 || c.P1FreshHours > 168 {
		http.Error(w, "p1FreshHours must be between 1 and 168", http.StatusBadRequest)
		return
	}
	if c.P2SameDayHours < 1 || c.P2SameDayHours > 720 {
		http.Error(w, "p2SameDayHours must be between 1 and 720", http.StatusBadRequest)
		return
	}
	// P2 penceresi P1'den dar olursa taze bir patlama P1 basamağını
	// atlayıp doğrudan P3'e düşerdi — v0.9.699'un tam olarak
	// düzelttiği uçurum. Okuma yolundaki kelepçe eski satırlar için
	// var, operatörün yazım hatasını gizlemek için değil.
	if c.P2SameDayHours < c.P1FreshHours {
		http.Error(w, "p2SameDayHours must be >= p1FreshHours", http.StatusBadRequest)
		return
	}
	if c.StaleResolveHours < 1 || c.StaleResolveHours > 720 {
		http.Error(w, "staleResolveHours must be between 1 and 720", http.StatusBadRequest)
		return
	}
	// v0.9.1188 — patlama kapıları. Üst sınırlar cömert ama SONSUZ DEĞİL:
	// 100.000/dk'lık bir eşik kapıyı fiilen kapatır ve merdivenin üst ucunu
	// sessizce yok ederdi; reddetmek, operatöre ne yaptığını söylemenin yolu.
	if c.BurstMinRate < 1 || c.BurstMinRate > 100000 {
		http.Error(w, "burstMinRate must be between 1 and 100000", http.StatusBadRequest)
		return
	}
	if c.BurstMinTotal < 1 || c.BurstMinTotal > 10000000 {
		http.Error(w, "burstMinTotal must be between 1 and 10000000", http.StatusBadRequest)
		return
	}
	if err := s.store.SaveExceptionTriage(r.Context(), c); err != nil {
		writeErr(w, err)
		return
	}
	setExceptionTriage(c)
	s.audit(r, "settings.update", "exception_triage", "exception_triage",
		fmt.Sprintf(`{"p1FreshHours":%d,"p2SameDayHours":%d,"staleResolveHours":%d,"burstMinRate":%.0f,"burstMinTotal":%d}`,
			c.P1FreshHours, c.P2SameDayHours, c.StaleResolveHours,
			c.BurstMinRate, c.BurstMinTotal))
	log.Printf("[settings] exception_triage: P1 ≤%dsa · P2 ≤%dsa · stale %dsa · patlama ≥%.0f/dk & ≥%d",
		c.P1FreshHours, c.P2SameDayHours, c.StaleResolveHours,
		c.BurstMinRate, c.BurstMinTotal)
	writeJSON(w, chstore.NormalizeExceptionTriage(c))
}
