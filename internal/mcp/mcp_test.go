package mcp

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// v0.9.14 — transport/oturum katmanının İLK testleri (audit: docs/
// audit/mcp-claude-code-production-audit.md §4). SSE lifecycle,
// JSON-RPC framing, Streamable-HTTP round-trip, rate-limit kapısı,
// wedged-consumer düşürmesi.

func testServer(t *testing.T) (*Server, *httptest.Server) {
	t.Helper()
	srv := New("coremetry-test", "v0.0.0-test")
	srv.RegisterTool(Tool{
		Name:        "echo_tool",
		Description: "test aracı",
		InputSchema: map[string]any{"type": "object"},
		Handler: func(_ context.Context, raw json.RawMessage) (any, error) {
			return map[string]any{"echo": string(raw)}, nil
		},
	})
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/mcp/sse", srv.HandleSSE)
	mux.HandleFunc("POST /api/mcp/messages", srv.HandleMessage)
	mux.HandleFunc("POST /api/mcp", srv.HandleStreamable)
	ts := httptest.NewServer(mux)
	t.Cleanup(ts.Close)
	return srv, ts
}

func rpc(method string, id int, params string) []byte {
	if params == "" {
		return []byte(fmt.Sprintf(`{"jsonrpc":"2.0","id":%d,"method":%q}`, id, method))
	}
	return []byte(fmt.Sprintf(`{"jsonrpc":"2.0","id":%d,"method":%q,"params":%s}`, id, method, params))
}

func postStreamable(t *testing.T, ts *httptest.Server, body []byte) (*http.Response, map[string]any) {
	t.Helper()
	resp, err := http.Post(ts.URL+"/api/mcp", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("post: %v", err)
	}
	t.Cleanup(func() { resp.Body.Close() })
	if resp.StatusCode == http.StatusAccepted {
		return resp, nil
	}
	var out map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	return resp, out
}

func TestStreamableInitializeVersion(t *testing.T) {
	_, ts := testServer(t)
	_, out := postStreamable(t, ts, rpc("initialize", 1,
		`{"protocolVersion":"2025-03-26","clientInfo":{"name":"claude-code","version":"1.0"}}`))
	result, _ := out["result"].(map[string]any)
	if result == nil || result["protocolVersion"] != ProtocolVersionStreamable {
		t.Fatalf("want protocolVersion %s, got %v", ProtocolVersionStreamable, out)
	}
	// Stateless sözleşme: session başlığı DÖNÜLMEZ.
	// (İstemci sessionless kipe düşer — çok-pod güvenliğinin özü.)
}

func TestStreamableUnknownMethod(t *testing.T) {
	_, ts := testServer(t)
	_, out := postStreamable(t, ts, rpc("no/such", 2, ""))
	errObj, _ := out["error"].(map[string]any)
	if errObj == nil || errObj["code"] != float64(ErrMethodNotFound) {
		t.Fatalf("want -32601, got %v", out)
	}
}

func TestStreamableNotificationIs202(t *testing.T) {
	_, ts := testServer(t)
	resp, _ := postStreamable(t, ts,
		[]byte(`{"jsonrpc":"2.0","method":"notifications/initialized"}`))
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("want 202, got %d", resp.StatusCode)
	}
}

func TestStreamableBadJSON(t *testing.T) {
	_, ts := testServer(t)
	resp, out := postStreamable(t, ts, []byte(`{not json`))
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("want 400, got %d", resp.StatusCode)
	}
	errObj, _ := out["error"].(map[string]any)
	if errObj == nil || errObj["code"] != float64(ErrParse) {
		t.Fatalf("want -32700, got %v", out)
	}
}

func TestStreamableToolCallAndGate(t *testing.T) {
	srv, ts := testServer(t)
	// Kapısız: çağrı geçer.
	_, out := postStreamable(t, ts, rpc("tools/call", 3,
		`{"name":"echo_tool","arguments":{"x":1}}`))
	if out["error"] != nil {
		t.Fatalf("kapısız çağrı geçmeliydi: %v", out)
	}
	// Kapı RED: -32000, tool handler HİÇ koşmaz.
	srv.SetToolCallGate(func(_ context.Context, tool string) error {
		return errors.New("rate limited: test")
	})
	_, out = postStreamable(t, ts, rpc("tools/call", 4,
		`{"name":"echo_tool","arguments":{}}`))
	errObj, _ := out["error"].(map[string]any)
	if errObj == nil || errObj["code"] != float64(ErrRateLimited) {
		t.Fatalf("want -32000, got %v", out)
	}
	// Bilinmeyen tool kapıdan ÖNCE -32601 (gate gerçek çağrıları sayar).
	_, out = postStreamable(t, ts, rpc("tools/call", 5, `{"name":"ghost"}`))
	errObj, _ = out["error"].(map[string]any)
	if errObj == nil || errObj["code"] != float64(ErrMethodNotFound) {
		t.Fatalf("bilinmeyen tool -32601 olmalı, got %v", out)
	}
}

// v0.9.20 — batch kabulü (2025-03-26 spec zorunluluğu) + parse
// hatasında id:null.
func TestStreamableBatch(t *testing.T) {
	_, ts := testServer(t)
	body := []byte(`[
		{"jsonrpc":"2.0","id":1,"method":"ping"},
		{"jsonrpc":"2.0","method":"notifications/initialized"},
		{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"echo_tool","arguments":{}}}
	]`)
	resp, err := http.Post(ts.URL+"/api/mcp", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var out []map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatalf("batch yanıtı dizi olmalı: %v", err)
	}
	// Bildirim yanıt girdisi ÜRETMEZ → 3 istekten 2 yanıt.
	if len(out) != 2 {
		t.Fatalf("want 2 responses, got %d: %v", len(out), out)
	}
	if out[0]["id"] != float64(1) || out[1]["id"] != float64(2) {
		t.Fatalf("id eşleşmesi bozuk: %v", out)
	}
}

func TestStreamableAllNotificationBatch202(t *testing.T) {
	_, ts := testServer(t)
	resp, err := http.Post(ts.URL+"/api/mcp", "application/json",
		bytes.NewReader([]byte(`[{"jsonrpc":"2.0","method":"notifications/initialized"}]`)))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("tamamı-bildirim batch 202 olmalı, got %d", resp.StatusCode)
	}
}

func TestStreamableParseErrorHasNullID(t *testing.T) {
	_, ts := testServer(t)
	resp, err := http.Post(ts.URL+"/api/mcp", "application/json",
		bytes.NewReader([]byte(`{bozuk`)))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	if !bytes.Contains(raw, []byte(`"id":null`)) {
		t.Fatalf("parse hatasında id:null spec gereği gövdede olmalı: %s", raw)
	}
}

func TestUnknownSession404(t *testing.T) {
	_, ts := testServer(t)
	resp, err := http.Post(ts.URL+"/api/mcp/messages?sessionId=yok",
		"application/json", bytes.NewReader(rpc("ping", 1, "")))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("want 404, got %d", resp.StatusCode)
	}
}

// TestSSELifecycle — GET → endpoint event'i; POST initialize →
// yanıt SSE kanalından 2024-11-05 ile; bağlantı kapanınca session
// silinir.
func TestSSELifecycle(t *testing.T) {
	srv, ts := testServer(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, ts.URL+"/api/mcp/sse", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("sse get: %v", err)
	}
	defer resp.Body.Close()

	scanner := bufio.NewScanner(resp.Body)
	var endpoint string
	lines := make(chan string, 16)
	go func() {
		for scanner.Scan() {
			lines <- scanner.Text()
		}
		close(lines)
	}()
	readLine := func() string {
		select {
		case l := <-lines:
			return l
		case <-time.After(3 * time.Second):
			t.Fatal("SSE satırı zaman aşımı")
			return ""
		}
	}
	if l := readLine(); l != "event: endpoint" {
		t.Fatalf("ilk event endpoint olmalı, got %q", l)
	}
	endpoint = strings.TrimPrefix(readLine(), "data: ")
	if !strings.HasPrefix(endpoint, "/api/mcp/messages?sessionId=") {
		t.Fatalf("endpoint şekli yanlış: %q", endpoint)
	}
	sessID := strings.TrimPrefix(endpoint, "/api/mcp/messages?sessionId=")

	// initialize POST → 202; yanıt SSE'den gelir.
	presp, err := http.Post(ts.URL+endpoint, "application/json",
		bytes.NewReader(rpc("initialize", 1, `{"protocolVersion":"2024-11-05"}`)))
	if err != nil {
		t.Fatal(err)
	}
	presp.Body.Close()
	if presp.StatusCode != http.StatusAccepted {
		t.Fatalf("POST want 202, got %d", presp.StatusCode)
	}
	// message event + data satırını bekle.
	var data string
	for {
		l := readLine()
		if strings.HasPrefix(l, "data: ") {
			data = strings.TrimPrefix(l, "data: ")
			break
		}
	}
	if !strings.Contains(data, ProtocolVersion) || strings.Contains(data, ProtocolVersionStreamable) {
		t.Fatalf("SSE initialize %s dönmeli, got %s", ProtocolVersion, data)
	}

	// Bağlantı kapanınca session silinir.
	cancel()
	deadline := time.Now().Add(2 * time.Second)
	for srv.lookupSession(sessID) != nil {
		if time.Now().After(deadline) {
			t.Fatal("session bağlantı kapanınca silinmedi")
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// TestWedgedConsumerDrop — SSE tüketicisi tıkalıyken HandleMessage
// sendTimeout sonunda yanıtı düşürür ama 202 döner (istemci asılı
// kalmaz). sendTimeout var'ı testte kısaltılır.
func TestWedgedConsumerDrop(t *testing.T) {
	srv, ts := testServer(t)
	old := sendTimeout
	sendTimeout = 50 * time.Millisecond
	t.Cleanup(func() { sendTimeout = old })

	sess := srv.newSession() // SSE okuyucusu YOK — tıkalı tüketici
	t.Cleanup(func() { srv.removeSession(sess.id) })
	for i := 0; i < cap(sess.out); i++ {
		sess.out <- json.RawMessage(`{}`) // buffer'ı doldur
	}
	start := time.Now()
	resp, err := http.Post(ts.URL+"/api/mcp/messages?sessionId="+sess.id,
		"application/json", bytes.NewReader(rpc("ping", 9, "")))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	elapsed := time.Since(start)
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("want 202, got %d", resp.StatusCode)
	}
	if elapsed < 40*time.Millisecond || elapsed > 2*time.Second {
		t.Fatalf("sendTimeout davranışı beklenen aralıkta değil: %v", elapsed)
	}
}
