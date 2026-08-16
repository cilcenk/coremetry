// embed_test.go — FAZ 1.4 (denetim bulgusu D7).
//
// İki söz test ediliyor:
//
//  1. GÖRÜNÜRLÜK — her embedding BATCH'i /ai'da bir satır. Batch
//     başına, chunk başına DEĞİL: 130 chunk'lık bir yükleme 3 HTTP
//     çağrısı yapıyor ve ai_calls "bir satır = bir çağrı" sözleşmesini
//     taşıyor (aksi hâlde gecikme ve token sayıları anlamsızlaşırdı).
//  2. SIZDIRMAZLIK — doküman içeriği ai_calls'a KOPYALANMAZ. Kayıt
//     maliyet ve sağlık taşır; metnin kendisi rag_chunks'ta zaten var
//     ve /ai'ın sample sütunları operatörün gözü önünde duruyor
//     (maskeli-kod yolunun duruşu, v0.9.831).
package rag

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"
)

// ─── yardımcılar ────────────────────────────────────────────────────

type fakeRecorder struct {
	mu   sync.Mutex
	recs []CallRecord
	ch   chan struct{}
}

func newFakeRecorder() *fakeRecorder {
	return &fakeRecorder{ch: make(chan struct{}, 64)}
}

func (f *fakeRecorder) RecordCall(_ context.Context, c CallRecord) {
	f.mu.Lock()
	f.recs = append(f.recs, c)
	f.mu.Unlock()
	f.ch <- struct{}{}
}

// wait — kayıt ateşle-unut olduğu için (copilot deseninin aynısı)
// testin beklemesi gerekiyor. Zaman aşımı kırmızı: "kayıt hiç gelmedi"
// sessizce geçmemeli.
func (f *fakeRecorder) wait(t *testing.T, n int) []CallRecord {
	t.Helper()
	for i := 0; i < n; i++ {
		select {
		case <-f.ch:
		case <-time.After(3 * time.Second):
			f.mu.Lock()
			got := len(f.recs)
			f.mu.Unlock()
			t.Fatalf("%d kayıt bekleniyordu, %d geldi", n, got)
		}
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]CallRecord(nil), f.recs...)
}

// embedServer — girdi sayısı kadar vektör döndüren sahte /embeddings
// ucu. Her isteğin girdi sayısını kaydeder (batch'leme testi bunu okur).
type embedServer struct {
	mu     sync.Mutex
	sizes  []int
	status int
	body   string
	usage  int
}

func newEmbedServer(t *testing.T, s *embedServer) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/embeddings") {
			t.Errorf("beklenmeyen yol: %s", r.URL.Path)
		}
		var body struct {
			Model string   `json:"model"`
			Input []string `json:"input"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		s.mu.Lock()
		s.sizes = append(s.sizes, len(body.Input))
		status, custom, usage := s.status, s.body, s.usage
		s.mu.Unlock()
		if status != 0 && status != 200 {
			w.WriteHeader(status)
			fmt.Fprint(w, custom)
			return
		}
		var sb strings.Builder
		sb.WriteString(`{"data":[`)
		for i := range body.Input {
			if i > 0 {
				sb.WriteString(",")
			}
			fmt.Fprintf(&sb, `{"index":%d,"embedding":[%d.0]}`, i, i)
		}
		fmt.Fprintf(&sb, `],"usage":{"prompt_tokens":%d}}`, usage)
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, sb.String())
	}))
	t.Cleanup(srv.Close)
	return srv
}

func newTestService(endpoint string) *Service {
	s := New()
	s.Configure(Config{Endpoint: endpoint, Model: "BAAI/bge-m3", Enabled: true})
	return s
}

// ─── 1. batch başına tek satır ──────────────────────────────────────

// TestEmbed_RecordsOneCallPerBatch — 130 metin = 64+64+2 = 3 HTTP
// çağrısı = 3 ai_calls satırı. PromptChars her satırda O BATCH'in
// toplam metin uzunluğu; chunk başına ya da tüm işin toplamı DEĞİL.
func TestEmbed_RecordsOneCallPerBatch(t *testing.T) {
	es := &embedServer{usage: 11}
	srv := newEmbedServer(t, es)
	svc := newTestService(srv.URL + "/v1")
	rec := newFakeRecorder()
	svc.SetRecorder(rec)

	const n = 130
	texts := make([]string, n)
	for i := range texts {
		// Uzunluk indeksle değişsin ki batch toplamları farklı çıksın
		// ve "hepsi aynı sayı" hatası testi kandıramasın.
		texts[i] = strings.Repeat("x", 10+i)
	}
	got, err := svc.Embed(context.Background(), texts)
	if err != nil {
		t.Fatalf("Embed: %v", err)
	}
	if len(got) != n {
		t.Fatalf("vektör sayısı %d, beklenen %d", len(got), n)
	}

	es.mu.Lock()
	sizes := append([]int(nil), es.sizes...)
	es.mu.Unlock()
	if want := []int{64, 64, 2}; !reflect.DeepEqual(sizes, want) {
		t.Fatalf("batch boyutları %v, beklenen %v", sizes, want)
	}

	recs := rec.wait(t, 3)
	if len(recs) != 3 {
		t.Fatalf("kayıt sayısı %d, beklenen 3 (batch başına bir satır)", len(recs))
	}
	sum := func(from, to int) uint32 {
		var s int
		for i := from; i < to; i++ {
			s += len(texts[i])
		}
		return uint32(s)
	}
	wantChars := []uint32{sum(0, 64), sum(64, 128), sum(128, 130)}
	for i, r := range recs {
		if r.PromptChars != wantChars[i] {
			t.Errorf("kayıt[%d].PromptChars = %d, beklenen %d (batch toplamı)", i, r.PromptChars, wantChars[i])
		}
		if r.Surface != SurfaceEmbedding {
			t.Errorf("kayıt[%d].Surface = %q, beklenen %q", i, r.Surface, SurfaceEmbedding)
		}
		if r.Provider != "openai" {
			t.Errorf("kayıt[%d].Provider = %q", i, r.Provider)
		}
		if r.Model != "BAAI/bge-m3" {
			t.Errorf("kayıt[%d].Model = %q", i, r.Model)
		}
		if r.BaseURL != srv.URL+"/v1" {
			t.Errorf("kayıt[%d].BaseURL = %q", i, r.BaseURL)
		}
		if r.Status != "ok" {
			t.Errorf("kayıt[%d].Status = %q", i, r.Status)
		}
		if r.InputTokens != 11 {
			t.Errorf("kayıt[%d].InputTokens = %d, beklenen 11 (usage.prompt_tokens)", i, r.InputTokens)
		}
		if r.CreatedAt.IsZero() {
			t.Errorf("kayıt[%d].CreatedAt boş", i)
		}
	}
}

// ─── 2. hata yolu ───────────────────────────────────────────────────

// TestEmbed_RecordsErrorStatus — uç düşünce satır YİNE yazılır. /ai'da
// "embedding ucu %100 hata veriyor" görünmezse operatör sessizce
// metin-only indekslemeye düşer ve bunu aylarca fark etmez (embed
// hatası v0.9.173'ten beri upload'u bloklamıyor).
func TestEmbed_RecordsErrorStatus(t *testing.T) {
	es := &embedServer{status: 500, body: strings.Repeat("hata gövdesi ", 200)}
	srv := newEmbedServer(t, es)
	svc := newTestService(srv.URL + "/v1")
	rec := newFakeRecorder()
	svc.SetRecorder(rec)

	if _, err := svc.Embed(context.Background(), []string{"abc", "de"}); err == nil {
		t.Fatal("hata bekleniyordu")
	}
	recs := rec.wait(t, 1)
	r := recs[0]
	if r.Status != "error" {
		t.Errorf("Status = %q, beklenen error", r.Status)
	}
	if !strings.Contains(r.ErrorMsg, "embedding endpoint 500") {
		t.Errorf("ErrorMsg = %q", r.ErrorMsg)
	}
	if len(r.ErrorMsg) != 512 {
		t.Errorf("ErrorMsg uzunluğu %d — 512 tavanı uygulanmamış", len(r.ErrorMsg))
	}
	if r.PromptChars != 5 {
		t.Errorf("PromptChars = %d, beklenen 5 — hata yolunda da işin boyutu kaydedilmeli", r.PromptChars)
	}
}

// ─── 3. kaydedici yokken ────────────────────────────────────────────

// TestEmbed_NilRecorderNoPanic — kayıt OPSİYONEL (testler, minimal
// binary, kaydediciyi kurmayan gelecekteki bir çağıran). copilot'un
// nil-güvenli duruşunun aynısı.
func TestEmbed_NilRecorderNoPanic(t *testing.T) {
	es := &embedServer{usage: 3}
	srv := newEmbedServer(t, es)
	svc := newTestService(srv.URL + "/v1")
	// SetRecorder hiç çağrılmadı.
	got, err := svc.Embed(context.Background(), []string{"a", "b"})
	if err != nil {
		t.Fatalf("Embed: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("vektör sayısı %d", len(got))
	}
	// Nil *Service üzerinde de patlamamalı (copilot.SetRecorder emsali).
	var nilSvc *Service
	nilSvc.SetRecorder(newFakeRecorder())
}

// ─── 4. içerik sızdırmazlığı ────────────────────────────────────────

// TestEmbed_NeverRecordsDocumentContent — kaydın HİÇBİR alanı doküman
// metnini taşımamalı. Alan adına göre değil, REFLEKSİYONLA tüm string
// alanlar taranıyor: yarın "PromptSample" adında bir alan eklenirse de
// bu test kırmızı yanar (kapı adı değil, DAVRANIŞI ölçüyor).
func TestEmbed_NeverRecordsDocumentContent(t *testing.T) {
	const marker = "GIZLI-MUSTERI-SOZLESMESI-4711"
	es := &embedServer{usage: 5}
	srv := newEmbedServer(t, es)
	svc := newTestService(srv.URL + "/v1")
	rec := newFakeRecorder()
	svc.SetRecorder(rec)

	if _, err := svc.Embed(context.Background(), []string{"başlangıç " + marker + " son"}); err != nil {
		t.Fatalf("Embed: %v", err)
	}
	recs := rec.wait(t, 1)
	v := reflect.ValueOf(recs[0])
	for i := 0; i < v.NumField(); i++ {
		f := v.Field(i)
		if f.Kind() != reflect.String {
			continue
		}
		if strings.Contains(f.String(), marker) {
			t.Fatalf("doküman içeriği ai_calls kaydına sızdı: %s = %q",
				v.Type().Field(i).Name, f.String())
		}
	}
	// Yapısal ikinci kilit: içerik örneği taşıyacak bir alan HİÇ
	// olmamalı — boş bırakılan bir alan, sonraki bir "iyileştirme"de
	// doldurulmaya davetiyedir.
	rt := reflect.TypeOf(CallRecord{})
	for i := 0; i < rt.NumField(); i++ {
		if n := rt.Field(i).Name; strings.Contains(n, "Sample") || strings.Contains(n, "Content") {
			t.Errorf("CallRecord içerik alanı taşıyor: %s — embedding kaydı doküman metni kopyalamaz", n)
		}
	}
}
