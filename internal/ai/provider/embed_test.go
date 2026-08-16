// embed_test.go — FAZ 1.4 (AI Assistant tasarımı §4-Faz1, denetim D7).
//
// rag.embedOnce internal/ altındaki SON bant-dışı LLM istemcisiydi.
// Devralmanın kabul ölçütü diğer taşıyıcılarla aynı: TEL ÜSTÜNDEKİ
// ŞEKİL birebir korunmalı. Burada pinlenen üç şey, üçü de eski
// yazılışta bir bug'ın saklanabildiği yerler:
//
//  1. gövde + header (kimliksiz uç dahil — air-gapped vLLM),
//  2. SIRA: yanıt `index` alanına göre yerleşir, GELİŞ sırasına değil
//     (bu düşerse chunk metni yanlış vektörle eşleşir ve retrieval
//     sessizce çöp döndürür — hiçbir hata yükselmez),
//  3. usage.prompt_tokens — Faz 1.4'ün /ai maliyet görünürlüğü buna
//     dayanıyor.
package provider

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
)

// embedRT — giden isteğin HAM gövdesini saklar (map'e çözmek alan
// sırasını kaybettirirdi; bu dilimin sözü gövdenin AYNI kalması).
type embedRT struct {
	reqs   []*http.Request
	bodies []string
	status int
	body   string
	err    error
}

func (c *embedRT) RoundTrip(req *http.Request) (*http.Response, error) {
	if c.err != nil {
		return nil, c.err
	}
	c.reqs = append(c.reqs, req)
	if req.Body != nil {
		raw, _ := io.ReadAll(req.Body)
		c.bodies = append(c.bodies, string(raw))
	}
	st := c.status
	if st == 0 {
		st = 200
	}
	return &http.Response{
		StatusCode: st,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body:       io.NopCloser(strings.NewReader(c.body)),
		Request:    req,
	}, nil
}

// embedRespJSON — n vektörlük bir yanıt gövdesi. order verilirse data
// dizisi O SIRAYLA yazılır (ama her elemanın `index`i doğru kalır), yani
// "sunucu karışık döndü" hâli taklit edilir. Vektörün ilk bileşeni
// girdinin indeksidir — testler eşleşmeyi bununla doğrular.
func embedRespJSON(order []int, usage int) string {
	var sb strings.Builder
	sb.WriteString(`{"data":[`)
	for i, idx := range order {
		if i > 0 {
			sb.WriteString(",")
		}
		fmt.Fprintf(&sb, `{"index":%d,"embedding":[%d.0,0.5]}`, idx, idx)
	}
	sb.WriteString(`]`)
	if usage >= 0 {
		fmt.Fprintf(&sb, `,"usage":{"prompt_tokens":%d}`, usage)
	}
	sb.WriteString(`}`)
	return sb.String()
}

func seq(n int) []int {
	out := make([]int, n)
	for i := range out {
		out[i] = i
	}
	return out
}

// ─── golden request body ────────────────────────────────────────────

// TestDoEmbeddings_GoldenRequestBody, rag.embedOnce'ın v0.8.438'den
// beri gönderdiği şekli pinler: {"model":…,"input":[…]} (bu alan
// sırasıyla), operatörün ucu + "/embeddings", Content-Type ve —
// yalnız anahtar varsa — Bearer.
func TestDoEmbeddings_GoldenRequestBody(t *testing.T) {
	tests := []struct {
		name     string
		cfg      Config
		req      EmbedRequest
		wantURL  string
		wantBody string
		wantAuth string
	}{
		{
			name:     "tek girdi + anahtar",
			cfg:      Config{BaseURL: "https://llm.internal/v1", APIKey: "sk-test", Model: "BAAI/bge-m3"},
			req:      EmbedRequest{Model: "BAAI/bge-m3", Inputs: []string{"merhaba"}},
			wantURL:  "https://llm.internal/v1/embeddings",
			wantBody: `{"model":"BAAI/bge-m3","input":["merhaba"]}`,
			wantAuth: "Bearer sk-test",
		},
		{
			// Air-gapped hedefteki tipik hâl: küme-içi vLLM, kimlik yok.
			// Boş anahtarla Authorization header'ı HİÇ basılmamalı —
			// bazı geçitler boş Bearer'ı 401'liyor.
			name:     "anahtarsız uç",
			cfg:      Config{BaseURL: "http://bge-m3.ai.svc:8000/v1/", Model: "bge-m3"},
			req:      EmbedRequest{Inputs: []string{"a", "b"}},
			wantURL:  "http://bge-m3.ai.svc:8000/v1/embeddings",
			wantBody: `{"model":"bge-m3","input":["a","b"]}`,
			wantAuth: "",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			rt := &embedRT{body: embedRespJSON(seq(len(tc.req.Inputs)), 7)}
			cfg := tc.cfg
			cfg.HTTPClient = &http.Client{Transport: rt}
			if _, err := DoEmbeddings(context.Background(), cfg, tc.req); err != nil {
				t.Fatalf("DoEmbeddings: %v", err)
			}
			if len(rt.reqs) != 1 {
				t.Fatalf("istek sayısı %d, beklenen 1", len(rt.reqs))
			}
			if got := rt.reqs[0].URL.String(); got != tc.wantURL {
				t.Errorf("URL = %q, beklenen %q", got, tc.wantURL)
			}
			if got := rt.bodies[0]; got != tc.wantBody {
				t.Errorf("gövde = %s\nbeklenen = %s", got, tc.wantBody)
			}
			if got := rt.reqs[0].Header.Get("Content-Type"); got != "application/json" {
				t.Errorf("Content-Type = %q", got)
			}
			if got := rt.reqs[0].Header.Get("Authorization"); got != tc.wantAuth {
				t.Errorf("Authorization = %q, beklenen %q", got, tc.wantAuth)
			}
			// DoOpenAI'ın `api-key` ikizi embedding ucuna hiç gitmedi
			// (v0.8.384 yalnız chat geçidiydi); taşıma onu eklememeli.
			if got := rt.reqs[0].Header.Get("api-key"); got != "" {
				t.Errorf("embedding ucuna api-key header'ı gitti: %q — bu, taşınan davranışta olmayan bir ekleme", got)
			}
		})
	}
}

// TestDoEmbeddings_Batch64OrderPreserved — 64'lük batch, sunucu ters
// sırada yanıtlıyor. Vektörler GİRDİ indeksine yerleşmezse chunk ↔
// embedding eşleşmesi sessizce bozulur; mutasyon testi tam olarak bunu
// hedefliyor (sıra korumasını düşür → kırmızı).
func TestDoEmbeddings_Batch64OrderPreserved(t *testing.T) {
	const n = 64
	inputs := make([]string, n)
	for i := range inputs {
		inputs[i] = fmt.Sprintf("chunk-%02d", i)
	}
	// Sunucu sırası: tersten.
	order := make([]int, n)
	for i := range order {
		order[i] = n - 1 - i
	}
	rt := &embedRT{body: embedRespJSON(order, 512)}
	cfg := Config{BaseURL: "http://x/v1", Model: "bge-m3", HTTPClient: &http.Client{Transport: rt}}

	got, err := DoEmbeddings(context.Background(), cfg, EmbedRequest{Inputs: inputs})
	if err != nil {
		t.Fatalf("DoEmbeddings: %v", err)
	}
	if len(got.Vectors) != n {
		t.Fatalf("vektör sayısı %d, beklenen %d", len(got.Vectors), n)
	}
	for i, v := range got.Vectors {
		if len(v) == 0 {
			t.Fatalf("vektör[%d] boş — sıra ataması atladı", i)
		}
		if v[0] != float32(i) {
			t.Fatalf("vektör[%d][0] = %v, beklenen %d — SIRA KORUNMADI (geliş sırasına yerleşmiş olabilir)", i, v[0], i)
		}
	}
	// Gövdedeki girdi sırası da korunmalı.
	var sent embedRequestBody
	if err := json.Unmarshal([]byte(rt.bodies[0]), &sent); err != nil {
		t.Fatalf("gövde çözülemedi: %v", err)
	}
	for i, in := range sent.Input {
		if in != inputs[i] {
			t.Fatalf("gönderilen girdi[%d] = %q, beklenen %q", i, in, inputs[i])
		}
	}
	if got.InputTokens != 512 {
		t.Errorf("InputTokens = %d, beklenen 512", got.InputTokens)
	}
}

// ─── usage + çözümleme ──────────────────────────────────────────────

func TestParseEmbeddings(t *testing.T) {
	tests := []struct {
		name       string
		body       string
		n          int
		wantTokens int
		wantErr    string
		wantFirst  []float32
	}{
		{
			name:       "usage okunur",
			body:       embedRespJSON(seq(3), 42),
			n:          3,
			wantTokens: 42,
			wantFirst:  []float32{0, 0.5},
		},
		{
			// Eski Ollama / bazı vLLM derlemeleri usage yollamıyor.
			// Satır yine yazılmalı, token 0 kalmalı — "0 token" ≠ hata.
			name:       "usage yoksa 0",
			body:       embedRespJSON(seq(2), -1),
			n:          2,
			wantTokens: 0,
			wantFirst:  []float32{0, 0.5},
		},
		{
			name:       "sayı uyuşmazlığı",
			body:       embedRespJSON(seq(2), 9),
			n:          3,
			wantTokens: 9, // usage hata yolunda da taşınır
			wantErr:    "embedding sayısı uyuşmuyor: 3 girdi, 2 vektör",
		},
		{
			name:    "aralık dışı index",
			body:    `{"data":[{"index":5,"embedding":[1.0]}]}`,
			n:       1,
			wantErr: "embedding index 5 aralık dışı",
		},
		{
			name:    "bozuk JSON",
			body:    `{"data":`,
			n:       1,
			wantErr: "unexpected EOF",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ParseEmbeddings(strings.NewReader(tc.body), tc.n)
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("hata = %v, beklenen %q", err, tc.wantErr)
				}
				if got.InputTokens != tc.wantTokens {
					t.Errorf("hata yolunda InputTokens = %d, beklenen %d", got.InputTokens, tc.wantTokens)
				}
				return
			}
			if err != nil {
				t.Fatalf("beklenmeyen hata: %v", err)
			}
			if got.InputTokens != tc.wantTokens {
				t.Errorf("InputTokens = %d, beklenen %d", got.InputTokens, tc.wantTokens)
			}
			if len(got.Vectors) != tc.n {
				t.Fatalf("vektör sayısı %d, beklenen %d", len(got.Vectors), tc.n)
			}
			for i, v := range tc.wantFirst {
				if got.Vectors[0][i] != v {
					t.Errorf("vektör[0][%d] = %v, beklenen %v", i, got.Vectors[0][i], v)
				}
			}
		})
	}
}

// ─── hata sözleşmesi ────────────────────────────────────────────────

// TestDoEmbeddings_HTTPError — 200 dışı yanıt TİPLİ hata olmalı ve
// operatörün gördüğü cümle ("embedding endpoint %d: …", v0.8.438)
// değişmemeli.
func TestDoEmbeddings_HTTPError(t *testing.T) {
	rt := &embedRT{status: 503, body: "  model yükleniyor  "}
	cfg := Config{BaseURL: "http://x/v1", Model: "m", HTTPClient: &http.Client{Transport: rt}}

	_, err := DoEmbeddings(context.Background(), cfg, EmbedRequest{Inputs: []string{"a"}})
	if err == nil {
		t.Fatal("hata bekleniyordu")
	}
	var he *HTTPError
	if !errors.As(err, &he) {
		t.Fatalf("tipli HTTPError bekleniyordu, alınan %T: %v", err, err)
	}
	if he.Status != 503 {
		t.Errorf("Status = %d, beklenen 503", he.Status)
	}
	if want := "embedding endpoint 503: model yükleniyor"; err.Error() != want {
		t.Errorf("hata metni = %q, beklenen %q", err.Error(), want)
	}
}

func TestDoEmbeddings_TransportError(t *testing.T) {
	rt := &embedRT{err: errors.New("dial tcp: no route to host")}
	cfg := Config{BaseURL: "http://x/v1", Model: "m", HTTPClient: &http.Client{Transport: rt}}
	_, err := DoEmbeddings(context.Background(), cfg, EmbedRequest{Inputs: []string{"a"}})
	if err == nil || !strings.Contains(err.Error(), "embedding isteği:") {
		t.Fatalf("taşıma hatası cümlesi korunmadı: %v", err)
	}
}

// TestDoEmbeddings_GuardRails — iki hâl de sessizce yanlış bir uca
// gitmek yerine açık hata vermeli. Boş uçta chat yolunun
// api.openai.com varsayılanına DÜŞMEK, air-gapped kurulumda doküman
// metnini internete göndermeyi denemek olurdu.
func TestDoEmbeddings_GuardRails(t *testing.T) {
	t.Run("nil client", func(t *testing.T) {
		_, err := DoEmbeddings(context.Background(), Config{BaseURL: "http://x"}, EmbedRequest{Inputs: []string{"a"}})
		if err == nil || !strings.Contains(err.Error(), "nil HTTPClient") {
			t.Fatalf("hata = %v", err)
		}
	})
	t.Run("boş uç", func(t *testing.T) {
		rt := &embedRT{body: embedRespJSON(seq(1), 1)}
		_, err := DoEmbeddings(context.Background(),
			Config{Model: "m", HTTPClient: &http.Client{Transport: rt}},
			EmbedRequest{Inputs: []string{"a"}})
		if err == nil || !strings.Contains(err.Error(), "uç adresi boş") {
			t.Fatalf("hata = %v", err)
		}
		if len(rt.reqs) != 0 {
			t.Fatalf("boş uçla istek gönderildi: %v", rt.reqs[0].URL)
		}
	})
}

// TestDoEmbeddings_ModelFallback — Request.Model boşsa Config.Model
// kullanılır (kardeş taşıyıcılarla aynı öncelik), ama uydurulmuş bir
// VARSAYILAN model yoktur: boyut uyuşmazlığı sessiz retrieval bozulması
// demek olurdu.
func TestDoEmbeddings_ModelFallback(t *testing.T) {
	rt := &embedRT{body: embedRespJSON(seq(1), 1)}
	cfg := Config{BaseURL: "http://x/v1", Model: "cfg-model", HTTPClient: &http.Client{Transport: rt}}
	if _, err := DoEmbeddings(context.Background(), cfg, EmbedRequest{Inputs: []string{"a"}}); err != nil {
		t.Fatalf("DoEmbeddings: %v", err)
	}
	if !strings.Contains(rt.bodies[0], `"model":"cfg-model"`) {
		t.Errorf("Config.Model'e düşülmedi: %s", rt.bodies[0])
	}
}
