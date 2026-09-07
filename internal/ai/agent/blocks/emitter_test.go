package blocks

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// v0.10.535 — tek SSE emitter sözleşmesi: çerçeve şekli, tembel/eager
// başlık, Started, heartbeat kilit paylaşımı + senkron/idempotent Stop.

func TestEmitFrameShapeAndLazyHeaders(t *testing.T) {
	rec := httptest.NewRecorder()
	em, ok := NewEmitter(rec, Options{})
	if !ok {
		t.Fatal("recorder Flusher'dır")
	}
	if rec.Header().Get("Content-Type") != "" || em.Started() {
		t.Fatal("tembel başlık: ilk çerçeveden önce başlık ve Started olmamalı")
	}
	em.Emit("delta", map[string]string{"text": "a"})
	em.Emit("done", map[string]bool{"ok": true})
	want := "event: delta\ndata: {\"text\":\"a\"}\n\nevent: done\ndata: {\"ok\":true}\n\n"
	if rec.Body.String() != want {
		t.Fatalf("çerçeve:\n%q\nwant\n%q", rec.Body.String(), want)
	}
	for k, v := range map[string]string{"Content-Type": "text/event-stream", "Cache-Control": "no-cache", "Connection": "keep-alive", "X-Accel-Buffering": "no"} {
		if rec.Header().Get(k) != v {
			t.Errorf("başlık %s = %q, want %q", k, rec.Header().Get(k), v)
		}
	}
	if !em.Started() || rec.Flushed != true {
		t.Fatal("Started + flush")
	}
	em.Close()
	em.Close() // idempotent, heartbeat yokken de güvenli
}

func TestEagerHeadersWithoutFrames(t *testing.T) {
	rec := httptest.NewRecorder()
	em, _ := NewEmitter(rec, Options{EagerHeaders: true})
	if rec.Header().Get("Content-Type") != "text/event-stream" {
		t.Fatal("eager başlık hemen yazılmalı")
	}
	if em.Started() {
		t.Fatal("başlık ≠ çerçeve: Started yalnız çerçeveyle")
	}
}

type noFlush struct{ http.ResponseWriter }

func TestNoFlusherRefused(t *testing.T) {
	if _, ok := NewEmitter(noFlush{httptest.NewRecorder()}, Options{}); ok {
		t.Fatal("Flush edemeyen writer reddedilmeli (buffered yola düşüş)")
	}
}

type fakeWriter struct {
	mu      sync.Mutex
	buf     strings.Builder
	flushes int
}

func (f *fakeWriter) Write(p []byte) (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.buf.Write(p)
}
func (f *fakeWriter) Flush()       { f.mu.Lock(); defer f.mu.Unlock(); f.flushes++ }
func (f *fakeWriter) text() string { f.mu.Lock(); defer f.mu.Unlock(); return f.buf.String() }

func TestHeartbeatPingsAndStopsSynchronously(t *testing.T) {
	w := &fakeWriter{}
	var mu sync.Mutex
	h := StartHeartbeat(&mu, w, w, 2*time.Millisecond)
	time.Sleep(20 * time.Millisecond)
	h.Stop()
	h.Stop()
	var nilH *Heartbeat
	nilH.Stop()
	n := strings.Count(w.text(), ": ping\n\n")
	if n == 0 {
		t.Fatal("ping yok")
	}
	time.Sleep(10 * time.Millisecond)
	if strings.Count(w.text(), ": ping\n\n") != n {
		t.Fatal("Stop senkron değil: durduktan sonra ping yazıldı")
	}
}

func TestHeartbeatSharesEmitLock(t *testing.T) {
	rec := httptest.NewRecorder()
	em, _ := NewEmitter(rec, Options{Heartbeat: time.Millisecond})
	defer em.Close()
	// Kilidi tutarken ping YAZILAMAZ; bırakınca yazılır.
	em.mu.Lock()
	before := rec.Body.Len()
	time.Sleep(8 * time.Millisecond)
	if rec.Body.Len() != before {
		em.mu.Unlock()
		t.Fatal("heartbeat emit kilidini paylaşmıyor — yarış")
	}
	em.mu.Unlock()
	time.Sleep(8 * time.Millisecond)
	if !strings.Contains(rec.Body.String(), ": ping\n\n") {
		t.Fatal("kilit bırakılınca ping gelmeli")
	}
	// Eager başlık: heartbeat varken ping'den önce başlık yazılmış olmalı.
	if rec.Header().Get("Content-Type") != "text/event-stream" {
		t.Fatal("heartbeat'li emitter başlığı hemen yazmalı")
	}
}
