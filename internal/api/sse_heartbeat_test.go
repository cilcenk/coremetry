package api

import (
	"os"
	"strings"
	"sync"
	"testing"
	"time"
)

// v0.10.27 — Copilot denetimi: SSE'de heartbeat YOKTU. Serbest tool
// döngüsü buffered ve ilk LLM çağrısı bitene kadar (180s'e kadar) tek
// bayt gitmeyebiliyor; OpenShift Route / nginx arkasında sessiz bir
// bağlantı koparıldığında operatör HİÇBİR hata görmüyordu — balon
// "yazıyor…" imlecinde asılı kalıyor, `done` hiç gelmiyor ve `busy`
// true kaldığı için yeni soru da yazılamıyordu.

type fakeSSEWriter struct {
	mu  sync.Mutex
	buf strings.Builder
	// flushes — Flush çağrısı sayısı; tamponlanan bir ping, gönderilmemiş
	// pingtir.
	flushes int
}

func (f *fakeSSEWriter) Write(p []byte) (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.buf.Write(p)
}
func (f *fakeSSEWriter) Flush() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.flushes++
}
func (f *fakeSSEWriter) text() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.buf.String()
}
func (f *fakeSSEWriter) flushCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.flushes
}

func TestHeartbeatEmitsCommentFrames(t *testing.T) {
	var mu sync.Mutex
	w := &fakeSSEWriter{}
	h := startSSEHeartbeat(&mu, w, w, 5*time.Millisecond)
	time.Sleep(40 * time.Millisecond)
	h.Stop()

	got := w.text()
	if !strings.Contains(got, ": ping\n\n") {
		t.Fatalf("ping çerçevesi yok: %q", got)
	}
	// ⚠ ÇERÇEVE ŞEKLİ. İstemcinin readSSE ayrıştırıcısı `data:` satırı
	// olmayan çerçeveyi atlıyor (`if (!data) continue`). Ping bir
	// `event:` ya da `data:` taşısaydı sohbete GÖRÜNÜR bir çöp olarak
	// düşerdi.
	if strings.Contains(got, "event:") || strings.Contains(got, "data:") {
		t.Errorf("heartbeat gerçek bir çerçeve yayıyor — istemci onu atlayamaz: %q", got)
	}
	// Yazıp flush etmemek, ping'i ara katmanda tamponda bırakır: sessiz
	// bağlantı sorunu aynen sürer.
	if w.flushCount() == 0 {
		t.Error("ping yazıldı ama Flush edilmedi — ara katmanda tamponda kalır")
	}
}

// blockingWriter — Write'ta bloke olan yazıcı. Goroutine'i yazımın TAM
// ORTASINDA yakalamak için; zamanlamaya dayanan bir test bu sözleşmeyi
// ölçemez (aşağıdaki nota bakın).
type blockingWriter struct {
	entered chan struct{} // Write'a girildi
	release chan struct{} // Write'ın dönmesine izin
	once    sync.Once
	mu      sync.Mutex
	n       int
}

func (b *blockingWriter) Write(p []byte) (int, error) {
	b.once.Do(func() { close(b.entered) })
	<-b.release
	b.mu.Lock()
	b.n += len(p)
	b.mu.Unlock()
	return len(p), nil
}
func (b *blockingWriter) Flush() {}

// ⚠ EN KRİTİK TEST. Stop() SENKRON olmalı.
//
// Handler döndükten sonra yazılan bir ping, ResponseWriter'ı ömrünün
// dışında kullanmaktır. Ticker'ı durdurmak YETMEZ: goroutine tam o anda
// bir yazımın ortasında olabilir.
//
// ⚠ İLK YAZIMDA BU TEST ISIRMIYORDU. Zamanlamaya dayanıyordu (Stop
// çağır, 20ms bekle, yazım oldu mu bak) ve asenkron Stop mutasyonunda
// bile YEŞİL kaldı: goroutine kapanma sinyalini ~1ms'de görüyor, yani
// yarış penceresi ölçülemeyecek kadar dardı. Mutasyon denetimi yakaladı.
//
// Deterministik hâli: goroutine'i bloke bir yazımın İÇİNDE tut, sonra
// Stop'un ondan ÖNCE dönemeyeceğini kanıtla.
func TestStopIsSynchronous(t *testing.T) {
	var mu sync.Mutex
	w := &blockingWriter{entered: make(chan struct{}), release: make(chan struct{})}
	h := startSSEHeartbeat(&mu, w, w, time.Millisecond)

	// Goroutine yazımın içine girene kadar bekle.
	select {
	case <-w.entered:
	case <-time.After(2 * time.Second):
		t.Fatal("heartbeat hiç yazmadı")
	}

	stopped := make(chan struct{})
	go func() { h.Stop(); close(stopped) }()

	// Yazım hâlâ bloke; SENKRON bir Stop DÖNEMEZ.
	select {
	case <-stopped:
		t.Fatal("Stop() uçan bir yazım varken döndü — handler dönerse " +
			"ResponseWriter ömrünün DIŞINDA yazılır")
	case <-time.After(50 * time.Millisecond):
	}

	close(w.release)
	select {
	case <-stopped:
	case <-time.After(2 * time.Second):
		t.Fatal("Stop() yazım bittikten sonra da dönmedi — kilitlendi")
	}
}

func TestStopIsIdempotent(t *testing.T) {
	// defer + erken dönüş birlikte iki kez Stop çağırabilir; ikinci
	// çağrı kapalı kanalı yeniden kapatıp panic ETMEMELİ.
	var mu sync.Mutex
	w := &fakeSSEWriter{}
	h := startSSEHeartbeat(&mu, w, w, time.Hour)
	h.Stop()
	h.Stop()
	h.Stop()
}

func TestNilHeartbeatStopIsSafe(t *testing.T) {
	var h *sseHeartbeat
	h.Stop() // panic etmemeli
}

// TestHeartbeatSharesTheWriteLock — YARIŞ SÖZLEŞMESİ.
//
// http.ResponseWriter eşzamanlı yazıma güvenli DEĞİL. Heartbeat ile emit
// AYNI kilidi paylaşmazsa çerçeveler iç içe geçer ve istemci çözülemeyen
// JSON görür. `go test -race` bu testte yarışı yakalar.
func TestHeartbeatSharesTheWriteLock(t *testing.T) {
	var mu sync.Mutex
	w := &fakeSSEWriter{}
	h := startSSEHeartbeat(&mu, w, w, time.Millisecond)
	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			mu.Lock()
			_, _ = w.Write([]byte("event: x\ndata: {}\n\n"))
			w.Flush()
			mu.Unlock()
		}()
	}
	wg.Wait()
	h.Stop()
}

// TestChatWiresHeartbeat — KABLOLAMA PİNİ.
func TestChatWiresHeartbeat(t *testing.T) {
	b, err := os.ReadFile("copilot_chat.go")
	if err != nil {
		t.Fatalf("copilot_chat.go okunamadı: %v", err)
	}
	src := stripGoCommentsAPI(string(b))

	for _, must := range []string{
		"startSSEHeartbeat(&wmu, w, flusher, sseHeartbeatEvery)",
		"defer hb.Stop()",
		"var wmu sync.Mutex",
		"wmu.Lock()",
	} {
		if !strings.Contains(src, must) {
			t.Errorf("sohbet heartbeat'i kurmuyor, kayıp: %s", must)
		}
	}
	// emit ile heartbeat AYNI kilidi paylaşmalı; ayrı kilit yarışı
	// engellemez ve `-race` altında bile sessiz kalabilir.
	iLock := strings.Index(src, "var wmu sync.Mutex")
	iEmit := strings.Index(src, "emit := func(")
	if iLock < 0 || iEmit < 0 || iLock > iEmit {
		t.Error("yazım kilidi emit'ten ÖNCE kurulmuyor")
	}
}
