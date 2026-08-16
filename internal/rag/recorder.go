package rag

import (
	"context"
	"time"
)

// recorder.go — embedding çağrılarının ai_calls görünürlüğü
// (v0.9.1126, Faz 1.4; denetim bulgusu D7).
//
// Embedding, /ai sayfasında bugüne dek TAMAMEN görünmezdi: doküman
// yükleme ve 30 dakikalık crawler tick'i binlerce chunk'ı embed
// ediyor, hiçbiri satır yazmıyordu. Operatörün "AI ne kadar iş
// yapıyor / hangi uç düşük" sorusu bu yüzden eksik cevaplanıyordu.
//
// Seam copilot.Recorder'ın BİREBİR ikizi ve bilinçli olarak AYRI:
// rag'ın copilot'u (ya da chstore'u) import etmesi gerekmiyor, main.go
// iki küçük adaptörü de chstore'a bağlıyor. Tasarım dokümanı §2.1 bu
// iki seam'in ileride provider/recorder.go'da birleşmesini öngörüyor;
// o taşıma copilot'un CallMeta'sıyla birlikte yapılacak iş, bu dilimin
// kapsamı değil.

// Recorder — ai_calls yazıcısı. main.go'daki adaptör uygular.
type Recorder interface {
	RecordCall(ctx context.Context, c CallRecord)
}

// CallRecord — bir embedding BATCH'inin (tek HTTP çağrısı) kaydı.
//
// Alan kümesi copilot.CallRecord'un ANLAMLI alt kümesidir. Eksik
// olanların hepsi bilinçli:
//
//   - OutputTokens: embedding üretmez, hep 0 olurdu.
//   - UserID/UserEmail: embed hem etkileşimli soru yolunda hem de
//     ingest/crawler arka planında koşuyor; kullanıcı kimliği yalnız
//     birinde var. Yarısı dolu bir sütun, /ai'da "bu embed'leri kim
//     tetikledi" sorusuna YANLIŞ cevap verirdi.
//   - PromptSample/ResponseSample: YOK ve olmayacak — doküman içeriği
//     ai_calls'a KOPYALANMAZ (maskeli-kod yolunun duruşunun aynısı,
//     v0.9.831). Vektörler de öyle: kaydın işi maliyet ve sağlık.
//
// PromptChars batch'teki metinlerin TOPLAM uzunluğudur; çağrının
// gerçekte ne kadar iş yaptığını taşıyan tek dürüst ölçü (usage
// yollamayan uçlarda InputTokens 0 kalır).
type CallRecord struct {
	CreatedAt   time.Time
	Surface     string
	Provider    string
	Model       string
	BaseURL     string
	DurationMs  uint32
	InputTokens uint32
	Status      string
	ErrorMsg    string
	PromptChars uint32
}

// SurfaceEmbedding — /ai'daki surface etiketi. Sayfa surface'leri
// serbest metin olarak gruplar, yani yeni etiket için frontend'de iş
// yok; sabit burada ki iki yazılışa bölünmesin.
const SurfaceEmbedding = "embedding"

// providerLabel — ai_calls.provider değeri. Uç OpenAI-uyumlu
// /embeddings konuşuyor (hedef: vLLM/KServe'deki bge-m3), yani
// copilot'un "openai" etiketiyle aynı aile; ayrı bir etiket uydurmak
// /ai'ın provider kırılımını gereksiz yere ikiye bölerdi.
const providerLabel = "openai"

// SetRecorder — boot'ta bir kez (main.go). Nil-güvenli.
func (s *Service) SetRecorder(r Recorder) {
	if s == nil {
		return
	}
	s.recorder = r
}

// recordEmbed — batch başına TEK satır. Ateşle-unut: CH ingest 5-20ms
// alabiliyor ve embed hem kullanıcının sorusunun önünde hem de
// upload'ın içinde duruyor (copilot.recordNarration'ın aynı gerekçesi).
func (s *Service) recordEmbed(started time.Time, c Config, texts []string, inputTokens int, err error) {
	if s.recorder == nil {
		return
	}
	chars := 0
	for _, t := range texts {
		chars += len(t)
	}
	rec := CallRecord{
		CreatedAt:   started,
		Surface:     SurfaceEmbedding,
		Provider:    providerLabel,
		Model:       c.Model,
		BaseURL:     c.Endpoint,
		DurationMs:  uint32(time.Since(started).Milliseconds()),
		InputTokens: uint32(inputTokens),
		Status:      "ok",
		PromptChars: uint32(chars),
	}
	if err != nil {
		rec.Status = "error"
		rec.ErrorMsg = truncErr(err.Error())
	}
	go func(r Recorder, rec CallRecord) {
		// Sınırlı ctx: takılmış bir CH ingest goroutine'i çivilemesin.
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		r.RecordCall(ctx, rec)
	}(s.recorder, rec)
}

// truncErr — copilot ile aynı 512 tavanı. Bir uç 4KB'lık HTML hata
// sayfası dönebiliyor ve o satır ai_calls'ta hiçbir işe yaramaz.
func truncErr(s string) string {
	if len(s) > 512 {
		return s[:512]
	}
	return s
}
