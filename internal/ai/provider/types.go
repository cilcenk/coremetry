// Package provider is the single LLM transport for Coremetry.
//
// FAZ 1.1 (AI Assistant tasarımı, docs/plans/ai-assistant-design-2026-08-16.md
// §2.1 / §4-Faz1). Bugün 8 ayrı istek üreticisi var (copilot.go ×3,
// chat.go ×2, stream.go ×2, rag.go embed) ve her biri gövde şeklini,
// header'ları ve salvage zincirini KENDİ kopyasıyla taşıyor. Bunun
// bedeli ölçüldü: 1024 max_tokens paritesi ~1000 sürüm boyunca
// anthropic/github yollarında eksik kaldı (v0.9.1120). Tek transport
// o sınıfı kapatır.
//
// FAZ 1.2 — kapsam ÜÇ sağlayıcının BUFFERED explain yolunun tamamı:
// DoOpenAI (JSON merdiveni dahil) + DoAnthropic + DoGitHub. Eski
// buffered üreticiler (explain*WithUsage) silindi; artık tek yazılış
// var. Stream (stream:true) ve tool-calling (chat.go) üreticileri
// bilinçli olarak DIŞARIDA — onlar sonraki dilimde taşınır; bu dilimde
// yalnız salvage/parse zincirini buradan çağırırlar, yani kurtarma
// mantığı da tek yerde.
//
// Tasarım kuralı: provider DURUM TUTMAZ. Config bir SNAPSHOT olarak
// dışarıdan gelir; canlı yapılandırmanın (kilit, 30s refresh, TLS-skip,
// timeout, GitHub oturum jetonu, JSON-yetenek kararları) sahibi
// copilot.Service'tir ve öyle kalır. Merdiven de öyle: hangi basamağın
// DENENECEĞİ ve reddedilince ne yapılacağı Service'in kararı, gövdeye
// hangi response_format'ın basılacağı buranın işi.
package provider

import "net/http"

// JSON zorlama basamakları. DoOpenAI üçünü de basar; hangi basamağın
// deneneceğine ve reddedilince bir alta inileceğine copilot.Service
// karar verir (yetenek kararları uç-başına önbellekli, yani DURUM).
//
// Basamak sınırın DIŞINDAysa ya da JSONSchema şemasız gelirse DoOpenAI
// açık hata döndürür: sessizce kısıtsız çağrıya düşmek, isteyen yüzeyin
// garantisini haber vermeden kaybettirirdi (fail-open-silently-unapplies
// sınıfı).
const (
	JSONPlain  = 0 // response_format yok
	JSONObject = 1 // {"type":"json_object"}
	JSONSchema = 2 // {"type":"json_schema", …}
)

// Request — bir LLM çağrısının tel-üstü girdileri.
//
// Model burada da, Config'te de var: Config kimlik/uç bilgisidir
// (baseURL+key+varsayılan model), Request ise O çağrının modelidir.
// Boşsa Config.Model, o da boşsa sağlayıcı varsayılanı kullanılır.
type Request struct {
	Model     string
	MaxTokens int
	// Temperature nil = gövdeye HİÇ koyma. copilot.Service bugün
	// daima bir değer gönderiyor (tuneTemperature ok=true); nil hâli,
	// bazı reasoning uçlarının temperature'ı reddettiği gelecekteki
	// "hiç gönderme" durumu için ifade edilebilir tutuluyor.
	Temperature *float64
	System      string
	User        string
	// JSONLevel — JSONPlain | JSONObject | JSONSchema. Yalnız
	// openai-compat yolunda uygulanır: anthropic ve github uçlarına
	// response_format bugüne kadar hiç gönderilmedi ve bu dilim
	// DAVRANIŞ TAŞIYOR, değiştirmiyor. O iki taşıyıcı JSONPlain
	// dışındaki basamağı açık hatayla reddeder ki "uyguladım" sanılmasın.
	JSONLevel int
	// JSONSchemaName / JSONSchema — yalnız JSONLevel==JSONSchema'da
	// okunur ve İKİSİ de zorunludur. Boş şemayla json_schema göndermek
	// 400 alır ve o uç için GEREKSİZ bir yetenek kararı yazdırırdı
	// (v0.9.527); Service şemasız isteği zaten bir alt basamağa indirir,
	// buradaki denetim o sözleşmenin ikinci kilidi.
	JSONSchemaName string
	JSONSchema     map[string]any
}

// Response — çözümlenmiş yanıt. Token sayıları `usage` alanından
// gelir; bazı yerel uçlar (eski Ollama, vLLM) usage yollamaz ve
// 0 kalır — kaydedici satırı yine yazar (gecikme + statü değerli).
type Response struct {
	Text         string
	InputTokens  int
	OutputTokens int
}

// Config — çağrı anındaki yapılandırma SNAPSHOT'ı.
//
// HTTPClient zorunlu: timeout (operatör-ayarlı, v0.9.1120) ve
// TLS-skip transport'u onun içinde yaşıyor. Nil'e sessizce
// http.DefaultClient koymak, 180s local-LLM timeout'unu ve
// kurumsal-CA muafiyetini haber vermeden düşürürdü.
type Config struct {
	BaseURL    string
	APIKey     string
	Model      string
	HTTPClient *http.Client
}
