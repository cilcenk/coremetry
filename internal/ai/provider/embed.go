package provider

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
)

// embed.go — OpenAI uyumlu /embeddings gövdesi (FAZ 1.4).
//
// Denetimin D7 bulgusu: rag.embedOnce, internal/ altındaki SON
// bant-dışı LLM HTTP istemcisiydi — kendi client'ı, kendi header
// yazılışı, kendi hata cümleleri ve ai_calls'ta HİÇBİR satır. Yani
// operatör /ai'da "AI ne kadar iş yaptı" sorusunu embedding trafiği
// eksik cevaplıyordu. Bu dosya gövdeyi tek transport'a alır; kayıt
// tarafını rag.Service'in Recorder'ı yazar.
//
// Embedding UCU bilinçli olarak chat sağlayıcısından AYRI kalır
// (tasarım dokümanı K2): air-gapped hedefte embedding vLLM/KServe'deki
// bge-m3'ten gelir, sohbet başka bir uçtan. Yani buraya gelen Config
// rag_embedding blob'unun snapshot'ıdır, ai_copilot'unkinin değil.
//
// Gövde/başlık/çözümleme rag.embedOnce'ın BİREBİR aynısıdır — bu dilim
// davranış TAŞIYOR, değiştirmiyor. İki bilinçli fark ve gerekçeleri
// aşağıda işaretli: (1) boş uç artık açık hata, (2) usage okunuyor.

// EmbedRequest — bir /embeddings çağrısının tel-üstü girdileri.
//
// Toplu-iş (batch) sınırı BURADA DEĞİL, çağırandadır: rag kendi
// embedBatchMax=64 döngüsünü korur, çünkü sınırın sebebi uç-başına
// istek boyu ve o karar yapılandırmanın sahibinin işi. Burada
// dilimleme yapmak, ai_calls'ta "bir satır = bir HTTP çağrısı"
// sözleşmesini de sessizce bozardı.
type EmbedRequest struct {
	Model string
	// Inputs — sıra ANLAMLIDIR. Çağıran vektörleri girdi indeksiyle
	// eşleştirir (rag chunk_idx'e yazar); yanıt `index` alanına göre
	// yeniden sıralanır, geliş sırasına DEĞİL.
	Inputs []string
}

// EmbedResponse — çözümlenmiş yanıt.
//
// InputTokens `usage.prompt_tokens`tan gelir. OpenAI ve vLLM ikisi de
// embeddings yanıtında usage yollar; yollamayan uçlarda 0 kalır ve
// kaydedici satırı yine yazar (gecikme + statü tek başına değerli —
// Response'un usage sözleşmesiyle aynı duruş).
type EmbedResponse struct {
	Vectors     [][]float32
	InputTokens int
}

// embedRequestBody / embedResponseBody — OpenAI-uyumlu /embeddings
// sözleşmesi (vLLM aynı şemayı servis eder). Alan sırası rag'ın eski
// struct'ıyla aynı tutuldu: gövde bayt-bayt aynı çıksın.
type embedRequestBody struct {
	Model string   `json:"model"`
	Input []string `json:"input"`
}

type embedResponseBody struct {
	Data []struct {
		Index     int       `json:"index"`
		Embedding []float32 `json:"embedding"`
	} `json:"data"`
	Usage struct {
		PromptTokens int `json:"prompt_tokens"`
	} `json:"usage"`
}

// DoEmbeddings tek bir /embeddings çağrısı yapar ve vektörleri GİRDİ
// SIRASINDA döndürür.
//
// Hata semantiği eski ikiziyle aynı: taşıma hatası "embedding isteği: %w",
// 200 dışı yanıt "embedding endpoint %d: <gövde>" — artık HTTPError
// tipiyle, yani statü metinden ayıklanmadan okunabiliyor.
func DoEmbeddings(ctx context.Context, cfg Config, req EmbedRequest) (EmbedResponse, error) {
	if cfg.HTTPClient == nil {
		return EmbedResponse{}, errors.New("provider: nil HTTPClient — timeout ve TLS-skip ayarları onun içinde yaşıyor")
	}
	// (1) Bilinçli fark: chat yolundaki defaultBaseURL (api.openai.com)
	// BURADA YOK ve olmamalı. Boş uçla varsayılana düşmek, air-gapped
	// kurulumda doküman metnini internete göndermeyi denemek olurdu.
	// Eski kod da bu hâlde çalışmıyordu (url "/embeddings" → şemasız
	// istek hatası); tek fark hatanın artık okunabilir olması.
	base := strings.TrimSpace(cfg.BaseURL)
	if base == "" {
		return EmbedResponse{}, errors.New("provider: embeddings uç adresi boş (Settings → AI → RAG)")
	}
	model := req.Model
	if model == "" {
		model = cfg.Model
	}
	// Varsayılan embedding modeli YOK: uç-başına model adı (bge-m3,
	// text-embedding-3-small, …) tamamen operatörün ve tahmin etmek
	// 400'e ya da yanlış boyutlu vektöre yol açar.

	body, err := json.Marshal(embedRequestBody{Model: model, Input: req.Inputs})
	if err != nil {
		return EmbedResponse{}, err
	}
	url := strings.TrimRight(base, "/") + "/embeddings"
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return EmbedResponse{}, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	if cfg.APIKey != "" {
		httpReq.Header.Set("Authorization", "Bearer "+cfg.APIKey)
	}
	// DoOpenAI'daki `api-key` ikizi burada BİLİNÇLİ olarak yok: o
	// header'ı operatörün chat geçidi istemişti (v0.8.384), embedding
	// ucu bugüne dek yalnız Bearer ile doğruladı. Eklemek "gövde
	// taşındı" dilimini sessiz bir davranış değişikliğine çevirirdi.

	resp, err := cfg.HTTPClient.Do(httpReq)
	if err != nil {
		return EmbedResponse{}, fmt.Errorf("embedding isteği: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return EmbedResponse{}, &HTTPError{Provider: labelEmbedding, Status: resp.StatusCode, Body: strings.TrimSpace(string(b))}
	}
	return ParseEmbeddings(resp.Body, len(req.Inputs))
}

// ParseEmbeddings, /embeddings yanıtını çözer ve vektörleri GİRDİ
// indeksine göre yerleştirir.
//
// Kardeşlerinden farklı olarak []byte değil io.Reader alır ve okuma
// TAVANI YOKTUR — bilerek: 64 metinlik bir bge-m3 batch'i 64×1024
// float32'yi JSON olarak ~4-5MB'ta taşır, yani chat yolunun 1MB'lık
// maxRespBytes tavanı burada yanıtın ORTASINDAN keserdi. Kesik gövde
// "decode" hatası olarak görünür ve teşhisi çok pahalıya patlardı.
func ParseEmbeddings(r io.Reader, n int) (EmbedResponse, error) {
	var er embedResponseBody
	if err := json.NewDecoder(r).Decode(&er); err != nil {
		return EmbedResponse{}, err
	}
	// (2) Bilinçli fark: usage okunuyor. Hata yollarında bile döndürülür
	// ki kaydedici "ne kadar girdi işlendi" bilgisini kaybetmesin.
	out := EmbedResponse{InputTokens: er.Usage.PromptTokens}
	if len(er.Data) != n {
		return out, fmt.Errorf("embedding sayısı uyuşmuyor: %d girdi, %d vektör", n, len(er.Data))
	}
	vecs := make([][]float32, n)
	for _, d := range er.Data {
		if d.Index < 0 || d.Index >= n {
			return out, fmt.Errorf("embedding index %d aralık dışı", d.Index)
		}
		vecs[d.Index] = d.Embedding
	}
	out.Vectors = vecs
	return out, nil
}
