// Package rag — v0.8.438 doküman soru-cevap (RAG) servisi.
//
// Deterministik "prefetch + narrate" deseninin retrieval'lı hali: bu
// paket soruyu embed eder, ClickHouse'tan en yakın chunk'ları çeker ve
// Copilot chat'ine hazır bir bağlam bloğu üretir; 2B model yalnız
// anlatır (tool-loop yok). Embedding, OpenAI-uyumlu /v1/embeddings
// sunan herhangi bir endpoint'ten gelir (vLLM/KServe'deki bge-m3 gibi)
// ve TAMAMEN config-gated'dir: endpoint girilmemişse Ready()=false ve
// chat bugünkü gibi RAG'siz çalışır — sessiz, dürüst degrade.
package rag

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"
	"sync"
	"time"
)

const settingsKey = "rag_embedding"

// Config — rag_embedding system_settings blob'u (invariant #6).
type Config struct {
	Endpoint string `json:"endpoint"`          // ör. http://bge-m3.ai.svc:8000/v1
	Model    string `json:"model"`             // ör. BAAI/bge-m3
	APIKey   string `json:"apiKey,omitempty"`  // opsiyonel — asla geri echo edilmez
	Enabled  bool   `json:"enabled"`
	TopK     int    `json:"topK,omitempty"`    // 0 → 5
}

// SettingsStore — chstore'un ihtiyaç duyulan dilimi (copilot.SettingsStore
// ile aynı desen; import döngüsü yok).
type SettingsStore interface {
	GetSetting(ctx context.Context, key string) ([]byte, error)
	PutSetting(ctx context.Context, key string, value []byte) error
}

type Service struct {
	mu    sync.RWMutex
	cfg   Config
	httpc *http.Client
}

func New() *Service {
	return &Service{httpc: &http.Client{Timeout: 30 * time.Second}}
}

func (s *Service) Snapshot() Config {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.cfg
}

func (s *Service) Configure(c Config) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.cfg = c
}

// Ready — RAG yolu ancak endpoint + model girilmiş ve etkinken açılır.
func (s *Service) Ready() bool {
	c := s.Snapshot()
	return c.Enabled && strings.TrimSpace(c.Endpoint) != "" && strings.TrimSpace(c.Model) != ""
}

func (s *Service) EffectiveTopK() int {
	c := s.Snapshot()
	if c.TopK <= 0 {
		return 5
	}
	if c.TopK > 20 {
		return 20
	}
	return c.TopK
}

// LoadPersisted / SavePersisted — copilot.Service'in birebir deseni.
func (s *Service) LoadPersisted(ctx context.Context, store SettingsStore) error {
	raw, err := store.GetSetting(ctx, settingsKey)
	if err != nil || len(raw) == 0 {
		return err
	}
	var c Config
	if err := json.Unmarshal(raw, &c); err != nil {
		return err
	}
	s.Configure(c)
	return nil
}

func (s *Service) SavePersisted(ctx context.Context, store SettingsStore, c Config) error {
	raw, err := json.Marshal(c)
	if err != nil {
		return err
	}
	if err := store.PutSetting(ctx, settingsKey, raw); err != nil {
		return err
	}
	s.Configure(c)
	return nil
}

// embedRequest/-Response — OpenAI-uyumlu /v1/embeddings sözleşmesi
// (vLLM aynı şemayı servis eder).
type embedRequest struct {
	Model string   `json:"model"`
	Input []string `json:"input"`
}
type embedResponse struct {
	Data []struct {
		Index     int       `json:"index"`
		Embedding []float32 `json:"embedding"`
	} `json:"data"`
}

// embedBatchMax — tek istekte gönderilecek maksimum metin; büyük
// dokümanlar dilimlenerek gider (endpoint tarafında istek boyu sınırı).
const embedBatchMax = 64

// Embed — metinleri embedding vektörlerine çevirir (sıra korunur).
func (s *Service) Embed(ctx context.Context, texts []string) ([][]float32, error) {
	c := s.Snapshot()
	if !s.Ready() {
		return nil, fmt.Errorf("embedding endpoint yapılandırılmamış (Settings → AI)")
	}
	out := make([][]float32, 0, len(texts))
	for start := 0; start < len(texts); start += embedBatchMax {
		end := start + embedBatchMax
		if end > len(texts) {
			end = len(texts)
		}
		batch, err := s.embedOnce(ctx, c, texts[start:end])
		if err != nil {
			return nil, err
		}
		out = append(out, batch...)
	}
	return out, nil
}

func (s *Service) embedOnce(ctx context.Context, c Config, texts []string) ([][]float32, error) {
	body, err := json.Marshal(embedRequest{Model: c.Model, Input: texts})
	if err != nil {
		return nil, err
	}
	url := strings.TrimRight(c.Endpoint, "/") + "/embeddings"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	if c.APIKey != "" {
		req.Header.Set("Authorization", "Bearer "+c.APIKey)
	}
	resp, err := s.httpc.Do(req)
	if err != nil {
		return nil, fmt.Errorf("embedding isteği: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return nil, fmt.Errorf("embedding endpoint %d: %s", resp.StatusCode, strings.TrimSpace(string(b)))
	}
	var er embedResponse
	if err := json.NewDecoder(resp.Body).Decode(&er); err != nil {
		return nil, err
	}
	if len(er.Data) != len(texts) {
		return nil, fmt.Errorf("embedding sayısı uyuşmuyor: %d girdi, %d vektör", len(texts), len(er.Data))
	}
	out := make([][]float32, len(texts))
	for _, d := range er.Data {
		if d.Index < 0 || d.Index >= len(out) {
			return nil, fmt.Errorf("embedding index %d aralık dışı", d.Index)
		}
		out[d.Index] = d.Embedding
	}
	return out, nil
}

// StartConfigRefresh — multi-pod senkronu (copilot deseninin aynısı).
func (s *Service) StartConfigRefresh(ctx context.Context, store SettingsStore, interval time.Duration) {
	if s == nil || store == nil {
		return
	}
	if interval <= 0 {
		interval = 30 * time.Second
	}
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			if err := s.LoadPersisted(ctx, store); err != nil {
				log.Printf("[rag] config refresh: %v", err)
			}
		}
	}
}
