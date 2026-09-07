// Package blocks — v0.10.535 (CoSRE v2 Faz 2.2): AI yüzeylerinin SSE çıkışı
// TEK yazımda. Bugüne kadar iki gövde vardı: sohbet handler'ı (eager
// başlık + yazım kilidi + heartbeat + adım kimliği) ve explain/insight
// `sseEmitter` (tembel başlık, heartbeat yok). Çerçeve şekli aynıydı ama
// yalnız yorum + testle tutuluyordu. Faz 3'te blok tipleri (text/table/
// chart/…) bu paketin aynı Emitter'ından akacak.
//
// İki incelik (v0.10.27 dersleri) burada korunur:
//   - http.ResponseWriter eşzamanlı yazıma güvenli değil: heartbeat ile
//     Emit AYNI kilidi paylaşır.
//   - Heartbeat Stop'u SENKRON: handler döndükten sonra yazılan bir ping,
//     ResponseWriter'ı ömrünün dışında kullanmak olurdu.
//
// Başlıklar tembel (varsayılan): ilk çerçeve düşene kadar gövdeye bayt
// gitmez, üretim ilk bayttan ÖNCE patlarsa çağıran gerçek HTTP statüsü
// yazabilir (explain sözleşmesi). EagerHeaders ya da Heartbeat>0 başlığı
// hemen yazar (sohbet: ilk LLM çağrısı 180 s sürebilir, ping bekleyemez).
package blocks

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sync"
	"time"
)

// HeartbeatDefault — api_logs canlı akışıyla aynı aralık (`: ping`).
const HeartbeatDefault = 15 * time.Second

type Options struct {
	EagerHeaders bool
	Heartbeat    time.Duration // 0 = yok
}

type Emitter struct {
	w           http.ResponseWriter
	f           http.Flusher
	mu          sync.Mutex
	headersSent bool
	frames      int
	hb          *Heartbeat
}

// NewEmitter — ok=false: writer Flush edemiyor (ara katman sarımı);
// çağıran buffered yola düşmek ZORUNDA — flush edilmeyen yarım SSE
// gövdesi istemciyi asar.
func NewEmitter(w http.ResponseWriter, o Options) (*Emitter, bool) {
	f, ok := w.(http.Flusher)
	if !ok {
		return nil, false
	}
	e := &Emitter{w: w, f: f}
	if o.EagerHeaders || o.Heartbeat > 0 {
		e.mu.Lock()
		e.writeHeadersLocked()
		e.mu.Unlock()
	}
	if o.Heartbeat > 0 {
		e.hb = StartHeartbeat(&e.mu, w, f, o.Heartbeat)
	}
	return e, true
}

func (e *Emitter) writeHeadersLocked() {
	if e.headersSent {
		return
	}
	h := e.w.Header()
	h.Set("Content-Type", "text/event-stream")
	h.Set("Cache-Control", "no-cache")
	h.Set("Connection", "keep-alive")
	h.Set("X-Accel-Buffering", "no")
	e.headersSent = true
}

// Emit — `event: <ad>\ndata: <json>\n\n` + flush; kilit altında.
func (e *Emitter) Emit(event string, payload any) {
	b, _ := json.Marshal(payload)
	e.mu.Lock()
	defer e.mu.Unlock()
	e.writeHeadersLocked()
	fmt.Fprintf(e.w, "event: %s\ndata: %s\n\n", event, b)
	e.f.Flush()
	e.frames++
}

// Started — en az bir çerçeve düştü mü. Hata yolunun "gerçek HTTP hatası
// mı, error çerçevesi mi" kararı buna bakar.
func (e *Emitter) Started() bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.frames > 0
}

// Close — heartbeat'i durdurur ve goroutine'in bittiğini BEKLER;
// idempotent, nil-güvenli.
func (e *Emitter) Close() {
	if e == nil {
		return
	}
	e.hb.Stop()
}

// ── heartbeat ───────────────────────────────────────────────────────────

// Heartbeat — çalışan bir `: ping` döngüsünün tutamacı (SSE yorumu;
// istemcinin readSSE'si data satırı olmayan çerçeveyi atlar).
type Heartbeat struct {
	stop     chan struct{}
	finished chan struct{}
	once     sync.Once
}

// StartHeartbeat — mu, çağıranın yazımıyla PAYLAŞILAN kilit olmalı.
func StartHeartbeat(mu *sync.Mutex, w io.Writer, f http.Flusher, every time.Duration) *Heartbeat {
	h := &Heartbeat{stop: make(chan struct{}), finished: make(chan struct{})}
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

// Stop — durdurur ve goroutine'in BİTTİĞİNİ bekler; birden çok çağrı
// (defer + erken dönüş) ve nil güvenli.
func (h *Heartbeat) Stop() {
	if h == nil {
		return
	}
	h.once.Do(func() { close(h.stop) })
	<-h.finished
}
