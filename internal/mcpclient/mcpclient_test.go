package mcpclient

// mcpclient_test.go — v0.10.86 dilim ①. Sahte MCP sunucusu httptest ile
// TEL ŞEKLİNİ taklit eder: initialize + Mcp-Session-Id, sayfalı
// tools/list, tools/call (isError dahil), SSE yanıtı ve binmiş bildirim.
// Adlar jenerik — gerçek bir sunucu/müşteri adı teste girmez.

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

// fakeMCPHTTP — streamable HTTP konuşan sahte sunucu.
type fakeMCPHTTP struct {
	mu       sync.Mutex
	calls    []string // gelen method'lar
	sessions []string // gelen Mcp-Session-Id başlıkları
	sse      bool     // tools/call yanıtını SSE olarak dön
	authTok  string   // doluysa Bearer eşleşmesi ister
	srv      *httptest.Server
}

func newFakeMCPHTTP(t *testing.T) *fakeMCPHTTP {
	t.Helper()
	f := &fakeMCPHTTP{}
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if f.authTok != "" && r.Header.Get("Authorization") != "Bearer "+f.authTok {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		var env rpcEnvelope
		if err := json.NewDecoder(r.Body).Decode(&env); err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		f.mu.Lock()
		f.calls = append(f.calls, env.Method)
		f.sessions = append(f.sessions, r.Header.Get("Mcp-Session-Id"))
		f.mu.Unlock()

		if env.ID == nil { // bildirim
			w.WriteHeader(http.StatusAccepted)
			return
		}
		reply := func(result any) {
			raw, _ := json.Marshal(result)
			out := map[string]any{"jsonrpc": "2.0", "id": *env.ID, "result": json.RawMessage(raw)}
			if f.sse && env.Method == "tools/call" {
				// Yanıttan ÖNCE binmiş bir bildirim — istemci onu kanala
				// akıtıp yanıtı yine bulmalı.
				w.Header().Set("Content-Type", "text/event-stream")
				notif, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "method": notifyListChanged})
				body, _ := json.Marshal(out)
				fmt.Fprintf(w, "data: %s\n\ndata: %s\n\n", notif, body)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(out)
		}
		switch env.Method {
		case "initialize":
			w.Header().Set("Mcp-Session-Id", "ses-42")
			reply(map[string]any{"protocolVersion": protocolVersion,
				"serverInfo": map[string]any{"name": "fake"}})
		case "tools/list":
			// İki sayfa: cursor'suz ilk sayfa + "p2" sayfası.
			var p struct {
				Cursor string `json:"cursor"`
			}
			raw, _ := json.Marshal(env.Params)
			_ = json.Unmarshal(raw, &p)
			if p.Cursor == "" {
				reply(map[string]any{
					"tools": []map[string]any{{
						"name": "search_kb", "description": "bilgi tabanında ara",
						"inputSchema": map[string]any{"type": "object"},
					}},
					"nextCursor": "p2",
				})
				return
			}
			reply(map[string]any{"tools": []map[string]any{{
				"name": "get_doc", "description": "doküman getir",
				"inputSchema": map[string]any{"type": "object"},
			}}})
		case "tools/call":
			var p struct {
				Name      string          `json:"name"`
				Arguments json.RawMessage `json:"arguments"`
			}
			raw, _ := json.Marshal(env.Params)
			_ = json.Unmarshal(raw, &p)
			if p.Name == "boom" {
				reply(map[string]any{"isError": true,
					"content": []map[string]any{{"type": "text", "text": "iç hata: kayıt yok"}}})
				return
			}
			reply(map[string]any{"content": []map[string]any{
				{"type": "text", "text": "sonuç:" + p.Name},
				{"type": "image", "data": "…"},
			}})
		default:
			out := map[string]any{"jsonrpc": "2.0", "id": *env.ID,
				"error": map[string]any{"code": -32601, "message": "method yok"}}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(out)
		}
	}))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeMCPHTTP) client() *Client {
	return NewClient(newHTTPTransport(ServerConfig{URL: f.srv.URL}, f.srv.Client()))
}

func TestHTTPEndToEnd(t *testing.T) {
	f := newFakeMCPHTTP(t)
	cl := f.client()
	ctx := context.Background()

	tools, trunc, err := cl.ListTools(ctx)
	if err != nil || trunc {
		t.Fatalf("ListTools err=%v trunc=%v", err, trunc)
	}
	if len(tools) != 2 || tools[0].Name != "search_kb" || tools[1].Name != "get_doc" {
		t.Fatalf("sayfalama katalogu toplamadı: %+v", tools)
	}
	// initialize BİR kez + initialized bildirimi gitmiş olmalı.
	f.mu.Lock()
	seq := strings.Join(f.calls, ",")
	f.mu.Unlock()
	if !strings.HasPrefix(seq, "initialize,notifications/initialized,tools/list") {
		t.Fatalf("el sıkışma sırası yanlış: %s", seq)
	}

	text, isErr, err := cl.CallTool(ctx, "search_kb", json.RawMessage(`{"q":"x"}`))
	if err != nil || isErr {
		t.Fatalf("CallTool err=%v isErr=%v", err, isErr)
	}
	if !strings.Contains(text, "sonuç:search_kb") {
		t.Errorf("metin içerik gelmedi: %q", text)
	}
	// Metin-dışı parça SESSİZCE kaybolmaz.
	if !strings.Contains(text, "metin-dışı içerik parçası atlandı") {
		t.Errorf("atlanan içerik ilan edilmedi: %q", text)
	}

	// Oturum kimliği initialize'dan sonraki İSTEKLERE geri yazılmalı.
	f.mu.Lock()
	defer f.mu.Unlock()
	for i, m := range f.calls {
		if i == 0 {
			continue // initialize'ın kendisi oturumsuz
		}
		if f.sessions[i] != "ses-42" {
			t.Errorf("%s isteği oturum başlığı taşımıyor (%q)", m, f.sessions[i])
		}
	}
}

func TestHTTPToolIsErrorFlag(t *testing.T) {
	f := newFakeMCPHTTP(t)
	cl := f.client()
	text, isErr, err := cl.CallTool(context.Background(), "boom", nil)
	if err != nil {
		t.Fatalf("taşıma hatası beklenmiyordu: %v", err)
	}
	// isError TAŞIMA hatası değildir: içerik modele "başarısız sonuç"
	// olarak gidecek (dilim ③ ToolErrorJSON köprüsü).
	if !isErr || !strings.Contains(text, "iç hata: kayıt yok") {
		t.Errorf("isErr=%v text=%q", isErr, text)
	}
}

func TestHTTPSSEResponseCarriesNotification(t *testing.T) {
	f := newFakeMCPHTTP(t)
	f.sse = true
	tr := newHTTPTransport(ServerConfig{URL: f.srv.URL}, f.srv.Client())
	cl := NewClient(tr)
	if _, _, err := cl.CallTool(context.Background(), "search_kb", nil); err != nil {
		t.Fatalf("SSE yanıtı çözülemedi: %v", err)
	}
	select {
	case m := <-tr.Notifications():
		if m != notifyListChanged {
			t.Errorf("bildirim=%q", m)
		}
	default:
		t.Error("akışa binmiş bildirim kanala düşmedi")
	}
}

func TestHTTPAuthRejectedIsUniform(t *testing.T) {
	f := newFakeMCPHTTP(t)
	f.authTok = "gizli"
	cl := NewClient(newHTTPTransport(ServerConfig{URL: f.srv.URL, Token: "yanlış"}, f.srv.Client()))
	_, _, err := cl.ListTools(context.Background())
	if err == nil || !strings.Contains(err.Error(), "kimliği reddetti") {
		t.Errorf("tek biçim kimlik hatası yok: %v", err)
	}
}

// ── saf tablolar ────────────────────────────────────────────────────────

func TestSplitPrefixed(t *testing.T) {
	for _, tc := range []struct {
		in, server, tool string
		ok               bool
	}{
		{"kb__search_kb", "kb", "search_kb", true},
		// Tool adının KENDİ "__"si bölünmez: ilk ayraç sınırdır.
		{"kb__weird__name", "kb", "weird__name", true},
		{"list_services", "", "", false}, // yerli ad — önek yok
		{"__x", "", "", false},
		{"kb__", "", "", false},
	} {
		s, tool, ok := SplitPrefixed(tc.in)
		if s != tc.server || tool != tc.tool || ok != tc.ok {
			t.Errorf("SplitPrefixed(%q) = (%q,%q,%v)", tc.in, s, tool, ok)
		}
	}
}

func TestSanitizeServerName(t *testing.T) {
	for _, tc := range [][2]string{
		{"Runbook KB", "runbook-kb"},
		// Alt çizgi '-' olur — önekteki "__" tekliği buna dayanıyor.
		{"my_server", "my-server"},
		{"  x  ", "x"},
		{"__", ""},
	} {
		if got := sanitizeServerName(tc[0]); got != tc[1] {
			t.Errorf("sanitize(%q)=%q, istenen %q", tc[0], got, tc[1])
		}
	}
	// Yapısal iddia: sanitize edilmiş HİÇBİR ad "__" içeremez, yani
	// SplitPrefixed'in ilk-ayraç kuralı her zaman sunucu sınırını bulur.
	if strings.Contains(sanitizeServerName("a__b"), "__") {
		t.Error("sanitize __ bırakıyor — önek ayrımı çöker")
	}
}
