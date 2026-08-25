package api

import (
	"io"
	"net/http"
	"sync"
	"time"
)

// sse_heartbeat.go — sessiz SSE akışının ara katmanlarca koparılmaması
// (v0.10.27, Copilot denetimi bulgusu).
//
// ── KUSUR ───────────────────────────────────────────────────────────────
//
// Serbest tool döngüsü v0.8.404'ten beri BUFFERED: ilk LLM çağrısı
// bitene kadar (çağrı-başı tavan varsayılan 180s) tek bayt gitmeyebiliyor.
// `X-Accel-Buffering: no` var ama o yalnız nginx'in TAMPONLAMASINI kapatır;
// UZUN SESSİZLİĞİ engellemez.
//
// OpenShift Route / nginx / kurumsal proxy arkasında sessiz bir bağlantı
// kesildiğinde operatör HİÇBİR hata görmüyordu: balon "yazıyor…"
// imlecinde asılı kalıyor, `done` çerçevesi hiç gelmiyor ve `busy` true
// kaldığı için yeni soru da yazılamıyordu.
//
// ── EMSAL ───────────────────────────────────────────────────────────────
//
// `api_logs.go` canlı log akışında 15 saniyelik bir ticker ile `: ping`
// basıyor. Aynı aralık ve aynı çerçeve şekli burada da kullanılıyor —
// ikinci bir sözleşme icat etmenin sebebi yok.
//
// `: ping` bir SSE YORUMUDUR: istemcinin `readSSE` ayrıştırıcısında
// `data:` satırı olmayan çerçeve `if (!data) continue` ile atlanıyor,
// yani heartbeat sohbete hiç görünmüyor.
//
// ── İKİ İNCELİK ─────────────────────────────────────────────────────────
//
//  1. `http.ResponseWriter` EŞZAMANLI YAZIMA GÜVENLİ DEĞİL. Ping
//     goroutine'i ile `emit` aynı kilidi paylaşmak ZORUNDA; paylaşmazsa
//     yarış, bozuk çerçeve ve çözülemeyen JSON üretir.
//
//  2. `Stop()` SENKRON olmalı. Handler döndükten sonra yazılan bir ping,
//     ResponseWriter'ı ömrünün dışında kullanmak demek. Ticker'ı
//     durdurmak YETMEZ — goroutine o an bir yazımın ortasında olabilir.
//     Stop, goroutine'in gerçekten bittiğini BEKLİYOR.

// sseHeartbeatEvery — api_logs.go'daki canlı log akışıyla aynı aralık.
const sseHeartbeatEvery = 15 * time.Second

// sseHeartbeat — çalışan bir ping döngüsünün tutamacı.
type sseHeartbeat struct {
	stop     chan struct{}
	finished chan struct{}
	once     sync.Once
}

// startSSEHeartbeat — `every` aralığıyla `: ping` yorum çerçevesi basar.
//
// mu, çağıranın `emit`iyle PAYLAŞILAN kilit olmalı; ayrı kilitler yarışı
// engellemez.
func startSSEHeartbeat(mu *sync.Mutex, w io.Writer, f http.Flusher, every time.Duration) *sseHeartbeat {
	h := &sseHeartbeat{stop: make(chan struct{}), finished: make(chan struct{})}
	go func() {
		defer close(h.finished)
		t := time.NewTicker(every)
		defer t.Stop()
		for {
			select {
			case <-h.stop:
				return
			case <-t.C:
				mu.Lock()
				// SSE yorumu: istemci `data:` satırı olmadığı için atlar.
				_, _ = io.WriteString(w, ": ping\n\n")
				if f != nil {
					f.Flush()
				}
				mu.Unlock()
			}
		}
	}()
	return h
}

// Stop — ping'i durdurur ve goroutine'in BİTTİĞİNİ bekler.
//
// Beklemek şart: handler döndükten sonra yazılan bir ping,
// ResponseWriter'ı ömrünün dışında kullanmaktır. `once` sayesinde
// birden çok Stop çağrısı (defer + erken dönüş) güvenli.
func (h *sseHeartbeat) Stop() {
	if h == nil {
		return
	}
	h.once.Do(func() { close(h.stop) })
	<-h.finished
}
