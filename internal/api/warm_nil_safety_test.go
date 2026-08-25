package api

import (
	"net/http"
	"testing"
	"time"
)

// v0.10.28 — `go test -race ./internal/api/` KULLANILAMIYORDU.
//
// `registerRoutes` arka plan ısıtıcısını goroutine olarak başlatıyor —
// yani rota kaydının bir YAN ETKİSİ. Alanları sıfır bir Server ile
// çağrıldığında (insight_test.go:366 tam olarak bunu yapıyor) ilk SETNX
// nil'e dokunup panikliyordu ve Go'da bir goroutine'deki yakalanmamış
// panik TÜM SÜRECİ düşürür — test ikilisi dâhil.
//
// ── ZARAR NEREDEYDİ ─────────────────────────────────────────────────────
//
// PROD'DA DEĞİL: main.go cache'i `cache.NewNoop()` ile kuruyor ve Redis
// hatası dalında da Noop'ta KALIYOR (main.go:542-552), yani cacheImpl
// asla nil olmuyor. İlk teşhisimde "Redis yapılandırılmamış kurulumda
// aynı yol açık" demiştim; YANLIŞTI ve koda bakınca çürüdü.
//
// Zarar test tarafındaydı ve sinsiydi: panik `-race` koşusunu
// düşürüyordu, yani bu pakette YARIŞ DEDEKTÖRÜ KÖRDÜ. Paket ise sohbetin
// eşzamanlı SSE kodunu barındırıyor — v0.10.27'nin heartbeat'i (ping
// goroutine'i + paylaşılan yazım kilidi) tam da burada yaşıyor. Yani
// eşzamanlılık kodu, onu doğrulayacak aracın çalışmadığı bir pakete
// giriyordu.

// TestRegisterRoutesSurvivesZeroServer — asıl regresyon.
//
// Sıfır-değerli bir Server ile rota kaydı PANİKLEMEMELİ. Panik ederse
// tüm test paketi düşer ve -race bir daha koşamaz.
func TestRegisterRoutesSurvivesZeroServer(t *testing.T) {
	mux := http.NewServeMux()
	(&Server{}).registerRoutes(mux)
	// Arka plan goroutine'i ilk tick'ine kadar yaşasın; guard yoksa
	// panik ORADA çıkıyordu, registerRoutes döndükten sonra.
	time.Sleep(50 * time.Millisecond)
}

// TestWarmWorkerIsNilSafe — işçiyi DOĞRUDAN çağır.
//
// Yukarıdaki test zamanlamaya bağlı (goroutine tick'ini beklemek);
// bu test sözleşmeyi doğrudan ölçüyor ve zamanlamadan bağımsız.
// v0.10.27'de zamanlamaya dayanan bir testin sözleşmeyi ölçmeden yeşil
// durabildiğini görmüştüm — aynı hatayı tekrarlamamak için ikisi de var.
func TestWarmWorkerIsNilSafe(t *testing.T) {
	for _, tc := range []struct {
		name string
		s    *Server
	}{
		{"tamamen sıfır", &Server{}},
		{"cache yok", &Server{}},
		{"nil alıcı", nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			done := make(chan struct{})
			go func() {
				defer close(done)
				// Panik ederse goroutine ölür ve `done` yine kapanır
				// (defer), ama panik SÜRECİ düşürdüğü için test zaten
				// buraya kadar gelmez — asıl koruma budur.
				tc.s.warmDependenciesCache()
			}()
			select {
			case <-done:
			case <-time.After(2 * time.Second):
				t.Fatal("ısıtıcı nil alanlarla DÖNMEDİ — guard'dan geçip döngüye girmiş olmalı")
			}
		})
	}
}
