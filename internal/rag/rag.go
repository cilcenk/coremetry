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
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/cilcenk/coremetry/internal/ai/provider"
)

const settingsKey = "rag_embedding"

// Config — rag_embedding system_settings blob'u (invariant #6).
type Config struct {
	Endpoint string `json:"endpoint"`         // ör. http://bge-m3.ai.svc:8000/v1
	Model    string `json:"model"`            // ör. BAAI/bge-m3
	APIKey   string `json:"apiKey,omitempty"` // opsiyonel — asla geri echo edilmez
	Enabled  bool   `json:"enabled"`
	TopK     int    `json:"topK,omitempty"` // 0 → 5
	// InsecureSkipVerify (v0.9.23) — operatör-raporlu prod bug'ı:
	// air-gapped kurulumda embedding endpoint'i (vLLM/KServe) ve iç
	// wiki'ler self-signed sertifikayla koşuyor; upload chunk+embed
	// adımında TLS doğrulamasına takılıyordu. Tempo/Zoom deseninin
	// aynısı; varsayılan kapalı.
	InsecureSkipVerify bool `json:"insecureSkipVerify,omitempty"`
	// Sources (v0.8.442) — wiki/URL kaynakları; 30 dk'lık senkron
	// tick'i her kaynağı sınırlı crawl edip hash-diff'le indeksler.
	Sources []CrawlSource `json:"sources,omitempty"`
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
	// recorder — ai_calls yazıcısı (v0.9.1126, Faz 1.4). Boot'ta bir kez
	// SetRecorder ile kurulur; nil = kayıt kapalı (testler, minimal
	// binary). copilot.Service'in aynı seam'i.
	recorder Recorder
}

func New() *Service {
	return &Service{httpc: NewHTTPClient(30*time.Second, false)}
}

// NewHTTPClient — RAG dış çağrılarının (embed + crawler) ortak
// client fabrikası; skipVerify self-signed iç servisler için
// (tempo newTempoHTTPClient emsali).
func NewHTTPClient(timeout time.Duration, insecureSkipVerify bool) *http.Client {
	tr := &http.Transport{}
	if insecureSkipVerify {
		tr.TLSClientConfig = &tls.Config{InsecureSkipVerify: true}
	}
	return &http.Client{Timeout: timeout, Transport: tr}
}

func (s *Service) Snapshot() Config {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.cfg
}

func (s *Service) Configure(c Config) {
	s.mu.Lock()
	defer s.mu.Unlock()
	prevInsecure := s.cfg.InsecureSkipVerify
	s.cfg = c
	// TLS bayrağı değişince client yeniden kurulur (tempo deseni) —
	// havuzdaki eski-politika bağlantıları toggle'ı aşamaz.
	if s.httpc == nil || prevInsecure != c.InsecureSkipVerify {
		s.httpc = NewHTTPClient(30*time.Second, c.InsecureSkipVerify)
	}
}

// client — canlı http.Client'ın KİLİTLİ okuyucusu. Configure onu
// TLS bayrağı değişince yeniden kurabiliyor ve StartConfigRefresh o
// yolu 30 saniyede bir kendi goroutine'inden çağırıyor; alanı çıplak
// okumak yarış demekti (embedOnce v0.8.438'den beri öyle okuyordu).
func (s *Service) client() *http.Client {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.httpc
}

// Ready — SEMANTİK yol (embed): endpoint + model girilmiş ve etkin.
func (s *Service) Ready() bool {
	c := s.Snapshot()
	return c.Enabled && strings.TrimSpace(c.Endpoint) != "" && strings.TrimSpace(c.Model) != ""
}

// Enabled — RAG etkin mi (embedding endpoint'i ŞART DEĞİL). v0.9.162:
// ingest/crawl bununla kapılanır; endpoint yoksa doküman METİN-ONLY girer
// (BM25 keyword retrieval çalışır, Ready ise ayrıca embed'lenir).
func (s *Service) Enabled() bool {
	return s.Snapshot().Enabled
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

// embedOnce — TEK HTTP çağrısı = TEK batch = TEK ai_calls satırı.
//
// Gövde/başlık/çözümleme v0.9.1126 (Faz 1.4) ile internal/ai/provider'a
// taşındı; burada kalan yarı yapılandırmanın sahipliği (rag_embedding
// blob'u, kendi 30s client'ı, kendi TLS-skip'i) ve KAYIT.
func (s *Service) embedOnce(ctx context.Context, c Config, texts []string) ([][]float32, error) {
	started := time.Now()
	resp, err := provider.DoEmbeddings(ctx, provider.Config{
		BaseURL:    c.Endpoint,
		APIKey:     c.APIKey,
		Model:      c.Model,
		HTTPClient: s.client(),
	}, provider.EmbedRequest{Model: c.Model, Inputs: texts})
	// Hata yolunda da kaydedilir: /ai'da "embedding ucu düştü" hâli
	// yalnız pod log'unda kalmasın (copilot'un status="error" duruşu).
	s.recordEmbed(started, c, texts, resp.InputTokens, err)
	if err != nil {
		return nil, err
	}
	return resp.Vectors, nil
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
