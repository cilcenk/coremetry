package logstore

// Trace-id alan VARLIĞI keşfi (v0.9.1084 — operator-reported:
// "Logs sayfasında with trace dediğimde hiç log gelmiyor prod'ta").
//
// Kök neden bir CH-vs-ES ayrışması: HasTrace filtresi ES'te yapısal
// trace alanları üzerinde `exists` koşar; trace→log pivotları ise
// traceTermsAny'nin GÖVDE-eşleşme dalıyla da çalışır ("[trace_id=abc]
// msg" — pipeline id'yi hiç alana ayırmamış). Yani pivotların çalıştığı
// bir prod ES'te yapısal alan hiç yoksa hasTrace SESSİZCE boş döner ve
// operatör bunu bug sanır (haklı olarak — sessiz boş, bug'ın ta kendisi;
// EnvUnapplied/v0.8.398 dürüstlük sınıfı).
//
// Bu dosya es_env_field.go deseninin birebir aynası: bir field_caps ile
// aday trace alanlarının HERHANGİ birinin mapping'de var olup olmadığına
// bakılır; olumlu/olumsuz karar TTL'le önbelleklenir; yokluk hâlinde
// Search, Page.HasTraceUnapplied ile dürüstçe "filtre bu kaynakta
// uygulanamıyor" der. Filtrenin kendisi DEĞİŞMEZ — exists yok-alanla
// zaten hiç eşleşmiyordu; değişen tek şey sessizliğin itirafa dönmesi.
//
// Env çözümünden TEK fark karar kuralı: env bir TERM hedefi arar
// (keyword-capable şart); hasTrace yalnız EXISTS koşar ve exists her
// indekslenmiş tipte çalışır — text-only bir trace alanı da VAR sayılır.

import (
	"context"
	"log"
	"sync"
	"time"
)

// resolveTracePresenceFromCaps — KARAR kuralı (saf, tablo testli):
// adaylardan herhangi biri mapping'de HERHANGİ bir tiple mevcutsa
// exists filtresi işe yarayabilir → true. Env kuralının aksine
// keyword şartı YOK (exists analize bakmaz).
func resolveTracePresenceFromCaps(candidates []string, caps map[string]traceFieldCap) (string, bool) {
	for _, name := range candidates {
		if len(caps[name].Types) > 0 {
			return name, true
		}
	}
	return "", false
}

// esTracePresenceCache — es_env_field deseni: olumlu VE olumsuz karar
// aynı TTL'le saklanır (istek başına field_caps, yasaklı ES-maliyet
// şekli).
type esTracePresenceCache struct {
	mu      sync.Mutex
	present bool
	expires time.Time
}

// traceFieldsPresent — hasTrace filtresinin bu backend'de karşılığı
// var mı? false ⇒ çağıran Page.HasTraceUnapplied raporlar.
func (s *ESStore) traceFieldsPresent(ctx context.Context) bool {
	// Operatör fields.TraceID yapılandırdıysa söz onun (ESFieldMap
	// sözleşmesi) — keşifsiz güvenilir.
	if s.fields.TraceID != "" {
		return true
	}
	now := time.Now()
	s.tracePresence.mu.Lock()
	if now.Before(s.tracePresence.expires) {
		p := s.tracePresence.present
		s.tracePresence.mu.Unlock()
		return p
	}
	s.tracePresence.mu.Unlock()

	candidates := traceFieldCandidates(s.fields.TraceID)
	fcCtx, cancel := context.WithTimeout(ctx, envFieldCapsTimeout)
	defer cancel()
	idx := s.queryIndices(fcCtx, Filter{From: now.Add(-24 * time.Hour), To: now})
	caps, err := s.fieldCaps(fcCtx, idx, candidates)
	present := false
	if err != nil {
		// Probe düştü — YOKLUK İDDİA EDİLMEZ (yanlış bir "çalışamaz"
		// rozeti, yanlış bir sessiz boştan daha az zararlı değil).
		// Bir TTL boyunca mevcut varsayılır; filtre bugünkü gibi davranır.
		present = true
		log.Printf("[logstore-es] trace alanı keşfi düştü (hasTrace bu TTL boyunca uygulanabilir varsayıldı): %v", err)
	} else if fld, ok := resolveTracePresenceFromCaps(candidates, caps); ok {
		present = true
		log.Printf("[logstore-es] hasTrace exists hedefi mevcut: %q (types=%v)", fld, caps[fld].Types)
	} else {
		log.Printf("[logstore-es] mapping'de yapısal trace-id alanı YOK (%v arandı) — hasTrace filtresi HasTraceUnapplied raporlar (trace→log pivotları gövde eşleşmesiyle çalışmaya devam eder; Settings → Elasticsearch → field map ile alan tanımlanabilir)", candidates)
	}
	s.tracePresence.mu.Lock()
	s.tracePresence.present, s.tracePresence.expires = present, now.Add(envFieldTTL)
	s.tracePresence.mu.Unlock()
	return present
}
