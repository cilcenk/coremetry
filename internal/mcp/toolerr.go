package mcp

// v0.9.1234 — tool hatalarının MODELE giden sözleşmesi.
//
// Olay: bir tool handler'ı hata döndürdüğünde model HAM Go hatasını
// görüyordu. İki yerde birden: MCP teli (handleToolsCall, isError
// content'i `err.Error()`) ve uygulama içi sohbet döngüsü
// (copilot_chat.go, `"error: " + herr.Error()`). Pratikte bu şu
// demekti:
//
//	code: 241, DB::Exception: Memory limit (total) exceeded: would
//	use 9.31 GiB (attempt to allocate chunk of 4194304 bytes),
//	maximum: 9.31 GiB: While executing AggregatingTransform. (…)
//
// Üç ayrı biçimde kötü:
//
//   - UZUN. Bir sürücü dökümü tek başına modelin bağlam bütçesinden
//     kilobaytlar yiyor (chat_tool_budget.go'nun BAŞARILI sonuçlar
//     için savunduğu bütçeyi hata yolu tamamen delip geçiyordu).
//   - İNGİLİZCE ve sürücü diliyle. Hava-boşluklu küçük model (gemma4,
//     Türkçe) bu metinden "şimdi ne yapmalıyım"ı çıkaramaz.
//   - SIZDIRAN. Sürücü metni sorgu içini (tablo/transform adları,
//     bazen SQL parçası) modele ve oradan operatörün ekranına taşıyor.
//
// Ve en önemlisi: EYLEME DÖNÜK DEĞİL. Modelin cevaplaması gereken tek
// soru "bu çağrı olmadı, şimdi ne yapayım?" — pencereyi daralt mı,
// yeniden dene mi, adı doğrula mı, vazgeç mi. Ham metin bunu
// söylemiyor, model de tahmin ediyor: aynı çağrıyı aynı argümanlarla
// tekrarlıyor, tur bütçesini yakıyor.
//
// Bu dosya beş sınıflık küçük bir sözleşme tanımlar. İki tüketici de
// (MCP teli + sohbet döngüsü) AYNI nesneyi görür; operatörün ⚙ çipi
// de aynısını gösterir — modelin gördüğünü operatör de görür, kanıt
// doktrini (chat_step_preview.go) hata yoluna da uzanır.
//
// NEDEN BU PAKET: `internal/mcp` bilerek SIFIR coremetry bağımlılığı
// taşır (protokol katmanı depolamadan bağımsız — tools.go'nun paket
// yorumu bunu "chstore↔mcp import dansı" diye anar; toolerr_test.go
// bunu kapı olarak çiviler). Sınıflandırıcı yalnız error DEĞERLERİ
// gördüğü için buraya sığar ve mcptools ile api'nin İKİSİ de import
// edebilir — mcptools zaten mcp'yi import ettiğinden ters yön (bu
// kodun mcptools'ta yaşaması) MCP telini sözleşmenin dışında
// bırakırdı.
//
// Bunun bedeli: `logstore.ErrBackendSlow` gibi coremetry sentinel'leri
// buradan errors.Is ile görülemez, metinle eşlenir. O yüzden gerçek
// sentinel'i bu sınıflandırıcıya sokan pin testi, İKİ paketi de gören
// yerde yaşıyor: internal/api/tool_error_pin_test.go. Sentinel'in adı
// ya da metni değişirse test kırılır — sessiz kayma yok.

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"strings"
)

// Tool hata sınıfları. BEŞ tane, bilerek: her biri modelin
// yapabileceği FARKLI bir eyleme karşılık gelir. Altıncı bir sınıf
// ancak altıncı bir eylem varsa eklenmeli.
const (
	// ToolErrTimeout — okuma bütçeyi aştı (ctx deadline, CH 159,
	// max_execution_time). Eylem: pencereyi daralt, tekrar dene.
	ToolErrTimeout = "timeout"
	// ToolErrBackendUnavailable — arka uç cevap veremiyor: bağlantı
	// reddi, bellek sınırı, yapılandırılmamış log backend'i. Eylem:
	// kısa bekle + bir kez daha, yoksa başka kanıt yolu.
	ToolErrBackendUnavailable = "backend_unavailable"
	// ToolErrBadArgs — çağıran hatası: eksik zorunlu alan, bozulmuş
	// JSON, yanlış biçim. Eylem: argümanı düzelt; AYNI argümanla
	// tekrar denemenin faydası yok.
	ToolErrBadArgs = "bad_args"
	// ToolErrNotFound — ad/kimlik bu pencerede yok. Eylem: keşif
	// tool'u ile doğrula ya da pencereyi büyüt.
	ToolErrNotFound = "not_found"
	// ToolErrInternal — sınıflandırılamayan. Eylem: tekrarlama,
	// eksikliği cevapta söyle.
	ToolErrInternal = "internal"
)

// toolErrDetailMaxRunes — ham hata metninin modele giden tavanı.
//
// 300 rune: bir CH istisnasının "code: NNN, DB::Exception: <cümle>"
// başı buraya rahat sığar (teşhis için gereken tek parça o), gerisi —
// allocation dökümü, transform zinciri, stack — sığmaz. Bilerek
// KÜÇÜK: hata yolu bir bütçe kalemi değil, bir tabela. chat_tool
// _budget.go'nun 6000 rune'u BAŞARILI kanıt içindir; başarısız bir
// çağrının modele borcu tek cümledir.
//
// Rune, bayt değil: sürücü metinleri Türkçe servis/operasyon adları
// taşıyor ve bayttan kesmek çok baytlı runeyi ikiye bölüp JSON
// kodlayıcıda U+FFFD üretirdi (clipStepPreview'ün aynı dersi).
const toolErrDetailMaxRunes = 300

// ToolError — başarısız bir tool çağrısının modele giden hâli.
//
// Alan sırası bilinçli: model önce NE olduğunu (error), sonra tekrar
// denemenin işe yarayıp yaramayacağını (retryable), sonra NE YAPACAĞINI
// (hint) okur; ham metin (detail) en sonda, çünkü en az eyleme dönük
// olan o.
type ToolError struct {
	Error     string `json:"error"`
	Retryable bool   `json:"retryable"`
	Hint      string `json:"hint"`
	Detail    string `json:"detail,omitempty"`
}

// toolErrPolicy — sınıf → {tekrar denenebilir mi, ne yapmalı}.
//
// İpuçları TÜRKÇE ve EMİR kipinde: hava-boşluklu küçük model Türkçe
// konuşuyor (prompts.go doktrini) ve "şunu yap" cümlesi "şu oldu"
// cümlesinden çok daha yüksek oranda uygulanıyor. Her ipucu somut bir
// tool ADI ya da somut bir vida (range_s) anar — soyut öğüt ("girdiyi
// kontrol edin") modelin bir sonraki turunu değiştirmiyor.
//
// retryable SINIF BAŞINA sabittir, hata başına değil: model için
// öngörülebilir olması, tek tek hataları ince ayarlamaktan değerli.
var toolErrPolicy = map[string]struct {
	retryable bool
	hint      string
}{
	ToolErrTimeout: {true,
		"okuma bütçeyi aştı — range_s'i küçült (yarıya indir) ve bir kez daha dene; " +
			"dar pencere çoğu soruyu aynı doğrulukla cevaplar"},
	ToolErrBackendUnavailable: {true,
		"arka uç şu an cevap veremiyor (bağlantı, bellek sınırı ya da yapılandırılmamış backend) — " +
			"birkaç saniye sonra BİR kez daha dene; yine olmazsa aynı kanıta başka bir tool ile ulaş"},
	ToolErrBadArgs: {false,
		"argümanlar hatalı — zorunlu alanları doldur ve adları list_services / list_operations ile " +
			"doğrula; AYNI argümanlarla tekrar deneme"},
	ToolErrNotFound: {false,
		"aradığın kayıt bu pencerede yok — adı/kimliği list_services, list_operations ya da " +
			"list_exception_groups ile doğrula, gerekirse range_s'i büyüt"},
	ToolErrInternal: {false,
		"beklenmeyen hata — aynı çağrıyı tekrarlama; aynı kanıta başka bir tool ile ulaşmayı dene " +
			"ve cevabında bu adımın eksik kaldığını SÖYLE"},
}

// ClassifyToolError — bir handler hatasını sözleşmeye çevirir. SAF:
// yalnız error değerine bakar, hiçbir şey yazmaz. Tablo testli.
//
// nil hata çağıran hatasıdır (başarı yolunda çağrılmaz); yine de
// internal döner, panik değil — hata yolunda panik atmak, hata
// yolunun kendisini bozar.
func ClassifyToolError(err error) ToolError {
	class := classifyToolErrorClass(err)
	p := toolErrPolicy[class]
	te := ToolError{Error: class, Retryable: p.retryable, Hint: p.hint}
	if err != nil {
		te.Detail = capRunes(err.Error(), toolErrDetailMaxRunes)
	}
	return te
}

// ToolErrorJSON — sözleşmenin tel/model üzerindeki hâli: kompakt JSON.
//
// Neden JSON: model BAŞARILI tool sonuçlarını zaten JSON okuyor
// (handleToolsCall out'u json.Marshal'lar, sohbet döngüsü aynısını
// besler). Hata yolunun düz metin olması, modelin iki ayrı biçim
// öğrenmesini gerektiriyordu — küçük modelde bu bedava değil.
func ToolErrorJSON(err error) string {
	b, merr := json.Marshal(ClassifyToolError(err))
	if merr != nil {
		// Marshal yalnız düz string alanlar üzerinde çalışıyor, yani
		// buraya düşmek imkânsıza yakın. Yine de sabit bir sözleşme
		// nesnesi dönüyoruz: hata yolunda BOŞ metin dönmek modele
		// "çağrı sessizce boş döndü" dedirtirdi.
		return `{"error":"internal","retryable":false,"hint":"beklenmeyen hata — aynı çağrıyı tekrarlama"}`
	}
	return string(b)
}

// classifyToolErrorClass — sınıf seçimi. SIRA ÖNEMLİ, en spesifikten
// en genele:
//
//  1. stdlib sentinel'leri (errors.Is/As) — metne bağlı olmayan tek
//     sinyal; sürücü sürümü değişse de kaymazlar.
//  2. "decode args:" öneki — mcptools'un HER tool'da kullandığı
//     sarmalama; io.EOF gibi bir alt-hatayı taşısa bile bu bir
//     çağıran hatasıdır, taşıma hatası değil.
//  3. metin sinyalleri: taşıma/sürücü → argüman → bulunamama.
//
// Metinle eşleme kırılgan olduğu için burada TUTUYORUZ: handler'ları
// tek tek sarmalamak 50+ dosyaya dokunmak demekti ve her yeni tool
// aynı sarmalamayı unutabilirdi. Merkezî tahmin + testli sinyal
// listesi, dağıtılmış disiplinden daha dayanıklı.
func classifyToolErrorClass(err error) string {
	if err == nil {
		return ToolErrInternal
	}
	// context.Canceled da timeout sınıfına giriyor: modelin yapacağı
	// şey aynı (pencereyi daralt, tekrar dene) ve iptal pratikte hep
	// üst bütçenin dolmasından geliyor.
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
		return ToolErrTimeout
	}
	var nerr net.Error
	if errors.As(err, &nerr) && nerr.Timeout() {
		return ToolErrTimeout
	}
	var operr *net.OpError
	if errors.As(err, &operr) {
		return ToolErrBackendUnavailable
	}

	s := strings.ToLower(err.Error())
	if strings.Contains(s, "decode args") {
		return ToolErrBadArgs
	}
	for _, sig := range toolErrTimeoutSignals {
		if strings.Contains(s, sig) {
			return ToolErrTimeout
		}
	}
	for _, sig := range toolErrUnavailableSignals {
		if strings.Contains(s, sig) {
			return ToolErrBackendUnavailable
		}
	}
	for _, sig := range toolErrBadArgsSignals {
		if strings.Contains(s, sig) {
			return ToolErrBadArgs
		}
	}
	for _, sig := range toolErrNotFoundSignals {
		if strings.Contains(s, sig) {
			return ToolErrNotFound
		}
	}
	return ToolErrInternal
}

// Sinyal listeleri — hepsi KÜÇÜK harf (karşılaştırma ToLower'lı).
//
// Kaynakları: ClickHouse sürücü metinleri (code: 159 TIMEOUT_EXCEEDED,
// code: 241 MEMORY_LIMIT_EXCEEDED, code: 202 TOO_MANY_SIMULTANEOUS_
// QUERIES), ES/HTTP taşıma metinleri, logstore.ErrBackendSlow'un
// sentinel metni ve mcptools handler'larının doğrulama cümleleri
// (hem "is required" hem "zorunlu" — katalog iki dilli).
var (
	toolErrTimeoutSignals = []string{
		"timeout exceeded", "timeout_exceeded", "code: 159",
		"max_execution_time", "deadline exceeded", "context deadline",
		"query was cancelled", "socket timeout",
	}
	toolErrUnavailableSignals = []string{
		"slow/unreachable", "not configured", "yapılandırılmamış",
		"memory limit", "memory_limit_exceeded", "code: 241",
		"too many simultaneous queries", "code: 202",
		"connection refused", "connection reset", "broken pipe",
		"dial tcp", "no such host", "no route to host",
		"network is unreachable", "unexpected eof", "server misbehaving",
		"service unavailable", "circuit", "attempt to read after eof",
	}
	toolErrBadArgsSignals = []string{
		"is required", "zorunlu", "must be one of", "must be ",
		"olmalı", "değil:", "geçersiz", "invalid ",
		"cannot unmarshal", "unexpected end of json", "rfc3339",
		"uymuyor", "hex",
	}
	toolErrNotFoundSignals = []string{
		"not found", "bulunamadı", "has no data", "does not exist",
		"unknown ", "no data in last",
	}
)

// capRunes — metni rune sınırında keser ve kesildiğini "…" ile SÖYLER.
// Sessiz kesme yasak sınıf (clipStepPreview / assemble.HistoryTrimNote
// aynı doktrin): kırpıldığını bilmeyen okuyucu eksik metni tam sanar.
func capRunes(s string, max int) string {
	if max <= 0 {
		return ""
	}
	n := 0
	for i := range s {
		n++
		if n > max {
			return s[:i] + "…"
		}
	}
	return s
}
