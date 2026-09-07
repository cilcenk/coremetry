package api

import (
	"io"
	"net/http"
	"sync"
	"time"

	"github.com/cilcenk/coremetry/internal/ai/agent/blocks"
)

// sse_heartbeat.go — v0.10.27'nin `: ping` döngüsü v0.10.535'te
// ai/agent/blocks'a taşındı (sohbet artık blocks.Emitter'ın kendi
// heartbeat'ini kullanır). Bu dosya yalnız adları korur: api_logs canlı
// akışı ve testler startSSEHeartbeat/sseHeartbeat adıyla çağırır.
//
// Dersler (blocks/emitter.go başlığında da yazılı): ping ile emit AYNI
// kilidi paylaşır; Stop SENKRON — handler döndükten sonra ping yazılmaz.

// sseHeartbeatEvery — api_logs.go'daki canlı log akışıyla aynı aralık.
const sseHeartbeatEvery = blocks.HeartbeatDefault

type sseHeartbeat = blocks.Heartbeat

func startSSEHeartbeat(mu *sync.Mutex, w io.Writer, f http.Flusher, every time.Duration) *sseHeartbeat {
	return blocks.StartHeartbeat(mu, w, f, every)
}
