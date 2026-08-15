// v0.9.1036 — failure-rate (%) SLO eşiğinin okuma/yazma ucu.
//
// Şablon: problem_priority (v0.9.838). Fark TEK ve bilinçli — GET rol
// KAPISIZDIR (herhangi bir kimlikli kullanıcı). Gerekçe: bu blob bir
// yönetim vidası değil, bir GRAFİK ÇİZGİSİ; viewer rolü CLAUDE.md
// invariant 7 gereği durumu GÖRÜR ("viewer SEES state read-only, never
// blank"). Admin'e kapatmak, viewer'ın hata-oranı grafiğini eşiksiz
// bırakırdı — yani rol, okunan GRAFİĞİ değiştirirdi.
//
// PUT admin + audit'li, her ayar yazımı gibi.
package api

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"strings"

	"github.com/cilcenk/coremetry/internal/chstore"
)

// getFailureSLO — GET /api/settings/failure-slo.
//
// KAYITLI blob'dan okunuyor: ayar sayfası ve grafik, operatörün NE
// YAZDIĞINI göstermeli. problem_priority / metric_exclusions duruşuyla
// aynı.
func (s *Server) getFailureSLO(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, s.store.GetFailureSLO(r.Context()))
}

// putFailureSLO validates + persists the threshold config.
//
// Sunucu tarafı doğrulama, istemci tarafı normalize'in KOPYASI DEĞİL:
// burada saçma girdi 400 ile GERİ ÇEVRİLİR (operatör yazdığının
// kırpıldığını görmeli), okuma yolundaki NormalizeFailureSLO ise elle
// düzenlenmiş bir satırı sessizce güvenli şekle çeker. İkisi ayrı iş.
func (s *Server) putFailureSLO(w http.ResponseWriter, r *http.Request) {
	// Varsayılanlarla ÖNCEDEN DOLDURULMUŞ struct'a decode: gövdede
	// olmayan bir alan varsayılanında kalır (problem_priority dersi).
	c := chstore.DefaultFailureSLO()
	if err := json.NewDecoder(r.Body).Decode(&c); err != nil {
		http.Error(w, "invalid JSON: "+err.Error(), http.StatusBadRequest)
		return
	}
	// 0 GEÇERLİ: "varsayılan çizgi çizme".
	if c.DefaultPct < 0 || c.DefaultPct > chstore.MaxFailureSLOPct {
		http.Error(w, fmt.Sprintf("defaultPct must be between 0 and %d (0 = no default line)",
			chstore.MaxFailureSLOPct), http.StatusBadRequest)
		return
	}
	if len(c.Overrides) > chstore.MaxFailureSLOOverrides {
		http.Error(w, fmt.Sprintf("at most %d service overrides", chstore.MaxFailureSLOOverrides),
			http.StatusBadRequest)
		return
	}
	for svc, pct := range c.Overrides {
		if strings.TrimSpace(svc) == "" {
			http.Error(w, "override service name cannot be empty", http.StatusBadRequest)
			return
		}
		if pct < 0 || pct > chstore.MaxFailureSLOPct {
			http.Error(w, fmt.Sprintf("override %q: pct must be between 0 and %d",
				svc, chstore.MaxFailureSLOPct), http.StatusBadRequest)
			return
		}
	}
	c = chstore.NormalizeFailureSLO(c)
	if err := s.store.SaveFailureSLO(r.Context(), c); err != nil {
		writeErr(w, err)
		return
	}
	// Override adları SIRALI: rastgele harita iterasyonu iki özdeş
	// kaydı audit'te farklı gösterirdi.
	s.audit(r, "settings.update", "failure_slo", "failure_slo",
		fmt.Sprintf(`{"defaultPct":%g,"overrides":%d,"services":%q}`,
			c.DefaultPct, len(c.Overrides),
			strings.Join(chstore.FailureSLOOverrideServices(c), ",")))
	log.Printf("[settings] failure_slo: varsayılan %%%g · %d servis override",
		c.DefaultPct, len(c.Overrides))
	writeJSON(w, c)
}
