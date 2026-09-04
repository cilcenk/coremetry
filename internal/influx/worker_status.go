package influx

// worker_status.go — v0.10.333 (operatör, prod: Influx kartında "veri gelmedi"
// ama işçi satırı YOK). Poll işçisi yalnız worker rolündeki lider pod'da
// koşar; Settings sayfasını servis eden API pod'u o değilse durum
// pod-yerel kaldığı için kart hiçbir şey söyleyemiyordu. Şimdi işçi her
// poll sonrası durumunu system_settings['influx_worker_status'] blobuna
// yazar (invariant #6: runtime durumu için tek depo), API pod'u yerel işçi
// yoksa oradan okur ve "işçi başka pod'da" diye ilan eder.

import (
	"context"
	"encoding/json"
	"log"
	"os"
	"sort"
	"time"
)

const WorkerStatusKey = "influx_worker_status"

// StatusPublisher — chstore.Store'un PutSetting'i (arayüz: test çiftleri için).
type StatusPublisher interface {
	PutSetting(ctx context.Context, key string, value []byte) error
}

// WorkerStatusSnapshot — paylaşılan durum blobu.
type WorkerStatusSnapshot struct {
	Pod       string         `json:"pod"`
	UpdatedAt int64          `json:"updatedAt"` // unix ms
	Sources   []SourceStatus `json:"sources"`
}

func EncodeWorkerStatus(pod string, at time.Time, sources []SourceStatus) ([]byte, error) {
	out := append([]SourceStatus(nil), sources...)
	sort.Slice(out, func(i, j int) bool { return out[i].SourceID < out[j].SourceID })
	return json.Marshal(WorkerStatusSnapshot{Pod: pod, UpdatedAt: at.UnixMilli(), Sources: out})
}

func DecodeWorkerStatus(raw []byte) (*WorkerStatusSnapshot, bool) {
	if len(raw) == 0 {
		return nil, false
	}
	var s WorkerStatusSnapshot
	if err := json.Unmarshal(raw, &s); err != nil || s.UpdatedAt == 0 {
		return nil, false
	}
	return &s, true
}

// SetStatusStore — main.go bağlar; nil = yayın yok (test/tek pod).
func (w *Worker) SetStatusStore(p StatusPublisher) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.statusStore = p
}

func podName() string {
	if h, err := os.Hostname(); err == nil && h != "" {
		return h
	}
	return "unknown"
}

// publishStatus — Tick'te en az bir kaynak poll'landıysa çağrılır; hata
// yalnız loglanır (durum yayını poll'u asla bloklamaz).
func (w *Worker) publishStatus(ctx context.Context, at time.Time) {
	w.mu.Lock()
	p := w.statusStore
	w.mu.Unlock()
	if p == nil {
		return
	}
	raw, err := EncodeWorkerStatus(podName(), at, w.Status())
	if err != nil {
		return
	}
	if err := p.PutSetting(ctx, WorkerStatusKey, raw); err != nil {
		log.Printf("[influx] işçi durumu yayınlanamadı: %v", err)
	}
}
