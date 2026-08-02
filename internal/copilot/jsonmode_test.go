package copilot

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

// v0.9.526 — JSON modu yetenek kararı artık kanıta dayalı.
//
// v0.9.517 katı-JSON yüzeyleri için `response_format` gönderiyor ve
// desteklemeyen uçta sessizce geri düşüyordu. Geri düşme mantığı doğruydu
// ama TETİKLEYİCİSİ çok genişti: `resp.StatusCode >= 300`. Yani geçici bir
// 500, bir 429 rate-limit ya da rolling-restart penceresinde gelen 503,
// `jsonModeUnsupported` haritasına kalıcı bir kayıt yazıyordu — süreç
// yeniden başlayana kadar o uçta JSON modu kapalı kalıyordu.
//
// Neden sessiz: operatör hiçbir hata görmez. Çağrı ikinci denemede başarılı
// döner, yüzey çalışır. Kaybolan tek şey, `json_object` kısıtının kapattığı
// JSON-kaçağı sınıfının geri gelmesidir — ve o sınıf zaten nadir + sessiz
// olduğu için kimse bağlantıyı kurmaz.
//
// Bu testler iki sözleşmeyi pinler:
//  1. Yalnız "isteği anlamadım" ailesi (400/422/501) karar verebilir.
//  2. O ailede bile karar, kısıtsız denemenin GERÇEKTEN başarmasına bağlı.

func TestJSONModeVerdictStatus(t *testing.T) {
	for code, want := range map[int]bool{
		// "Bu parametreyi anlamadım" ailesi — karar verebilir.
		http.StatusBadRequest:          true,
		http.StatusUnprocessableEntity: true,
		http.StatusNotImplemented:      true,

		// Geçici / ilgisiz — karar VEREMEZ. Her biri v0.9.517'de
		// yeteneği kalıcı kapatıyordu.
		http.StatusTooManyRequests:     false,
		http.StatusInternalServerError: false,
		http.StatusBadGateway:          false,
		http.StatusServiceUnavailable:  false,
		http.StatusGatewayTimeout:      false,

		// 404 kasıtlı olarak dışarıda: rota/model yapılandırma hatası,
		// yetenek kararına çevrilirse teşhis saklanır.
		http.StatusNotFound: false,

		// Kimlik/yetki de yetenek değil.
		http.StatusUnauthorized: false,
		http.StatusForbidden:    false,
	} {
		if got := jsonModeVerdictStatus(code); got != want {
			t.Errorf("jsonModeVerdictStatus(%d) = %v, beklenen %v", code, got, want)
		}
	}
}

const okChatBody = `{"choices":[{"message":{"content":"cevap"},"finish_reason":"stop"}],"usage":{"prompt_tokens":3,"completion_tokens":4}}`

// jsonModeProbeServer — `response_format` taşıyan istekleri rejectStatus ile
// reddeder, taşımayanlara okChatBody döner (ya da plainStatus verilmişse onu).
// Gerçek bir vLLM/gateway'in "bu parametreyi bilmiyorum" davranışının modeli.
func jsonModeProbeServer(t *testing.T, rejectStatus, plainStatus int) (*Service, *int32, func()) {
	t.Helper()
	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
		raw, _ := io.ReadAll(io.LimitReader(r.Body, 1<<20))
		var body map[string]any
		_ = json.Unmarshal(raw, &body)
		_, hasFormat := body["response_format"]

		if hasFormat {
			w.WriteHeader(rejectStatus)
			_, _ = w.Write([]byte(`{"error":"unknown field response_format"}`))
			return
		}
		if plainStatus != http.StatusOK {
			w.WriteHeader(plainStatus)
			_, _ = w.Write([]byte(`{"error":"context length exceeded"}`))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(okChatBody))
	}))
	s := New("openai", "", "test-model")
	s.Configure("openai", "", "test-model", srv.URL, false, true)
	return s, &calls, srv.Close
}

// 400 + kısıtsız deneme başarılı → karar YAZILIR, cevap kurtarılır.
// Asıl özelliğin var oluş sebebi: desteklemeyen uçta Explain kırılmasın.
func TestJSONModeDemotesOnBadRequestWhenPlainRetrySucceeds(t *testing.T) {
	s, calls, done := jsonModeProbeServer(t, http.StatusBadRequest, http.StatusOK)
	defer done()

	out, _, _, err := s.explainOpenAIWithUsage(WithJSONMode(context.Background()), "sys", "user")
	if err != nil {
		t.Fatalf("kısıtsız deneme başarılıyken hata dönmemeli: %v", err)
	}
	if out != "cevap" {
		t.Errorf("kısıtsız denemenin cevabı kullanılmalı, got %q", out)
	}
	if n := atomic.LoadInt32(calls); n != 2 {
		t.Errorf("tam iki çağrı beklenir (kısıtlı + kısıtsız), got %d", n)
	}
	if !s.jsonModeBlocked("openai", s.baseURL, "test-model") {
		t.Error("kanıtlanmış reddediş karar olarak kaydedilmeli")
	}

	// İkinci çağrı artık hiç response_format göndermemeli → tek istek.
	atomic.StoreInt32(calls, 0)
	if _, _, _, err := s.explainOpenAIWithUsage(WithJSONMode(context.Background()), "sys", "user"); err != nil {
		t.Fatalf("karar önbelleklendikten sonra hata olmamalı: %v", err)
	}
	if n := atomic.LoadInt32(calls); n != 1 {
		t.Errorf("karar önbellekli olmalı — tek istek beklenir, got %d", n)
	}
}

// GEÇİCİ hatalar yeteneği kapatmamalı. v0.9.526'nın asıl düzelttiği sınıf:
// her biri v0.9.517'de kalıcı karar yazıyordu.
func TestJSONModeSurvivesTransientErrors(t *testing.T) {
	for _, code := range []int{
		http.StatusInternalServerError,
		http.StatusServiceUnavailable,
		http.StatusTooManyRequests,
		http.StatusBadGateway,
		http.StatusGatewayTimeout,
		http.StatusNotFound,
	} {
		s, calls, done := jsonModeProbeServer(t, code, http.StatusOK)

		_, _, _, err := s.explainOpenAIWithUsage(WithJSONMode(context.Background()), "sys", "user")
		if err == nil {
			t.Errorf("%d: hata dönmeli — geçici hata sessizce yutulmamalı", code)
		}
		if s.jsonModeBlocked("openai", s.baseURL, "test-model") {
			t.Errorf("%d: geçici hata JSON modunu KAPATMAMALI (v0.9.517 gerilemesi)", code)
		}
		if n := atomic.LoadInt32(calls); n != 1 {
			t.Errorf("%d: karar-dışı statüde yeniden deneme olmamalı, got %d çağrı", code, n)
		}
		done()
	}
}

// 400 ama kısıtsız deneme de patlıyor → 400 JSON moduyla ilgili DEĞİLDİ
// (bağlam taşması, bozuk model adı…). Karar yazılmamalı, ve çağırana
// kendi isteğinin hatası dönmeli.
func TestJSONModeKeepsCapabilityWhenPlainRetryAlsoFails(t *testing.T) {
	s, calls, done := jsonModeProbeServer(t, http.StatusBadRequest, http.StatusBadRequest)
	defer done()

	_, _, _, err := s.explainOpenAIWithUsage(WithJSONMode(context.Background()), "sys", "user")
	if err == nil {
		t.Fatal("iki deneme de patlarken hata dönmeli")
	}
	if s.jsonModeBlocked("openai", s.baseURL, "test-model") {
		t.Error("kanıtlanmamış reddediş karar olarak yazılmamalı — 400 başka sebepten")
	}
	if n := atomic.LoadInt32(calls); n != 2 {
		t.Errorf("bir kez kısıtsız denenmeli, got %d çağrı", n)
	}
	// Dönen hata çağıranın GERÇEKTEN yaptığı isteğin hatası olmalı.
	if !strings.Contains(err.Error(), "response_format") {
		t.Errorf("özgün isteğin hatası dönmeli, got %v", err)
	}
}

// ─── v0.9.527 — merdiven: json_schema → json_object → kısıtsız ────────

var testSchema = map[string]any{
	"type":                 "object",
	"properties":           map[string]any{"ozet": map[string]any{"type": "string"}},
	"required":             []string{"ozet"},
	"additionalProperties": false,
}

// ladderServer — hangi basamakların reddedileceğini alır, her isteğin
// hangi basamakta geldiğini kaydeder.
func ladderServer(t *testing.T, reject map[jsonLevel]bool) (*Service, *[]jsonLevel, func()) {
	t.Helper()
	var seen []jsonLevel
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(io.LimitReader(r.Body, 1<<20))
		var body map[string]any
		_ = json.Unmarshal(raw, &body)

		lvl := jsonNone
		if rf, ok := body["response_format"].(map[string]any); ok {
			if rf["type"] == "json_schema" {
				lvl = jsonSchema
			} else {
				lvl = jsonObject
			}
		}
		seen = append(seen, lvl)

		if reject[lvl] {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"error":"unsupported"}`))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(okChatBody))
	}))
	s := New("openai", "", "test-model")
	s.Configure("openai", "", "test-model", srv.URL, false, true)
	return s, &seen, srv.Close
}

// Şemayı reddeden ama json_object'i kabul eden uç: BİR basamak inilir.
// json_object kaybedilmemeli — merdivenin iki ayrı karar tutmasının
// bütün sebebi bu.
func TestJSONSchemaDemotesOneRungOnly(t *testing.T) {
	s, seen, done := ladderServer(t, map[jsonLevel]bool{jsonSchema: true})
	defer done()

	ctx := WithJSONSchema(context.Background(), "test", testSchema)
	out, _, _, err := s.explainOpenAIWithUsage(ctx, "sys", "user")
	if err != nil {
		t.Fatalf("json_object çalışırken hata dönmemeli: %v", err)
	}
	if out != "cevap" {
		t.Errorf("alt basamağın cevabı kullanılmalı, got %q", out)
	}
	if want := []jsonLevel{jsonSchema, jsonObject}; !sameLevels(*seen, want) {
		t.Errorf("iniş sırası %v, beklenen %v", *seen, want)
	}
	if !s.jsonSchemaBlocked("openai", s.baseURL, "test-model") {
		t.Error("şema basamağı kapatılmalıydı")
	}
	if s.jsonModeBlocked("openai", s.baseURL, "test-model") {
		t.Error("json_object KAPATILMAMALIYDI — yalnız bir basamak inildi")
	}

	// Sonraki çağrı doğrudan json_object ile gitmeli.
	*seen = nil
	if _, _, _, err := s.explainOpenAIWithUsage(ctx, "sys", "user"); err != nil {
		t.Fatalf("beklenmeyen hata: %v", err)
	}
	if want := []jsonLevel{jsonObject}; !sameLevels(*seen, want) {
		t.Errorf("karar önbelleklenmeli — %v, beklenen %v", *seen, want)
	}
}

// İki basamağı da reddeden uç: kısıtsıza kadar iner ve İKİ karar da
// yazılır (her çerçeve kendi basamağını, alttaki başardığı için).
func TestJSONLadderFallsAllTheWayDown(t *testing.T) {
	s, seen, done := ladderServer(t, map[jsonLevel]bool{jsonSchema: true, jsonObject: true})
	defer done()

	ctx := WithJSONSchema(context.Background(), "test", testSchema)
	if _, _, _, err := s.explainOpenAIWithUsage(ctx, "sys", "user"); err != nil {
		t.Fatalf("kısıtsız çalışırken hata dönmemeli: %v", err)
	}
	if want := []jsonLevel{jsonSchema, jsonObject, jsonNone}; !sameLevels(*seen, want) {
		t.Errorf("iniş sırası %v, beklenen %v", *seen, want)
	}
	if !s.jsonSchemaBlocked("openai", s.baseURL, "test-model") {
		t.Error("şema basamağı kapatılmalıydı")
	}
	if !s.jsonModeBlocked("openai", s.baseURL, "test-model") {
		t.Error("json_object basamağı da kapatılmalıydı")
	}

	// Artık tek istek, kısıtsız.
	*seen = nil
	_, _, _, _ = s.explainOpenAIWithUsage(ctx, "sys", "user")
	if want := []jsonLevel{jsonNone}; !sameLevels(*seen, want) {
		t.Errorf("iki karar da önbellekli olmalı — %v, beklenen %v", *seen, want)
	}
}

// Şema istendi ama bağlamda şema yok (çağıran nil geçti) → sessizce
// json_object'e düş. Boş bir `json_schema` gövdesi göndermek 400 alır
// ve GEREKSİZ yere yetenek kararı yazdırırdı.
func TestJSONSchemaWithoutSpecFallsBackToObject(t *testing.T) {
	s, seen, done := ladderServer(t, nil)
	defer done()

	ctx := context.WithValue(context.Background(), jsonModeKey{}, jsonSchema)
	if _, _, _, err := s.explainOpenAIWithUsage(ctx, "sys", "user"); err != nil {
		t.Fatalf("beklenmeyen hata: %v", err)
	}
	if want := []jsonLevel{jsonObject}; !sameLevels(*seen, want) {
		t.Errorf("şemasız istek json_object olmalı — %v, beklenen %v", *seen, want)
	}
}

// Gönderilen gövde gerçekten OpenAI json_schema şeklinde olmalı:
// {"type":"json_schema","json_schema":{"name","schema","strict"}}.
func TestJSONSchemaBodyShape(t *testing.T) {
	var got map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(io.LimitReader(r.Body, 1<<20))
		var body map[string]any
		_ = json.Unmarshal(raw, &body)
		got, _ = body["response_format"].(map[string]any)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(okChatBody))
	}))
	defer srv.Close()
	s := New("openai", "", "test-model")
	s.Configure("openai", "", "test-model", srv.URL, false, true)

	ctx := WithJSONSchema(context.Background(), "nl-to-query", testSchema)
	if _, _, _, err := s.explainOpenAIWithUsage(ctx, "sys", "user"); err != nil {
		t.Fatalf("beklenmeyen hata: %v", err)
	}
	if got["type"] != "json_schema" {
		t.Fatalf("type json_schema olmalı, got %v", got["type"])
	}
	js, ok := got["json_schema"].(map[string]any)
	if !ok {
		t.Fatal("json_schema gövdesi yok")
	}
	if js["name"] != "nl-to-query" {
		t.Errorf("name yüzey adını taşımalı, got %v", js["name"])
	}
	if js["strict"] != true {
		t.Errorf("strict:true gönderilmeli, got %v", js["strict"])
	}
	if _, ok := js["schema"].(map[string]any); !ok {
		t.Error("schema gövdesi taşınmamış")
	}
}

func sameLevels(a, b []jsonLevel) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// JSON modu istenmediğinde gövdeye response_format GİRMEMELİ — kısıt
// istemeyen yüzeyler (serbest anlatım) modeli JSON'a hapsetmemeli.
func TestNoJSONModeSendsNoResponseFormat(t *testing.T) {
	var sawFormat bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(io.LimitReader(r.Body, 1<<20))
		var body map[string]any
		_ = json.Unmarshal(raw, &body)
		_, sawFormat = body["response_format"]
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(okChatBody))
	}))
	defer srv.Close()
	s := New("openai", "", "test-model")
	s.Configure("openai", "", "test-model", srv.URL, false, true)

	if _, _, _, err := s.explainOpenAIWithUsage(context.Background(), "sys", "user"); err != nil {
		t.Fatalf("beklenmeyen hata: %v", err)
	}
	if sawFormat {
		t.Error("JSON modu istenmediği halde response_format gönderildi")
	}
}
