package mcpclient

// transport_stdio_test.go — stdio taşıması GERÇEK bir alt süreçle test
// edilir: test binary'si kendini yardımcı-süreç olarak yeniden çalıştırır
// (stdlib exec testlerinin GO_WANT_HELPER deseni). Sahte pipe yerine
// gerçek süreç: kapanış/EOF/kill yolları ancak böyle sürülür.

import (
	"bufio"
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"
)

const helperEnv = "MCPCLIENT_STDIO_HELPER"

// TestMain — helperEnv doluysa test süreci MCP sunucusu gibi davranır.
func TestMain(m *testing.M) {
	if os.Getenv(helperEnv) == "1" {
		runStdioHelper()
		os.Exit(0)
	}
	os.Exit(m.Run())
}

// runStdioHelper — satır-ayrımlı JSON-RPC konuşan minimal sunucu.
func runStdioHelper() {
	sc := bufio.NewScanner(os.Stdin)
	sc.Buffer(make([]byte, 64*1024), stdioLineCap)
	out := json.NewEncoder(os.Stdout)
	for sc.Scan() {
		var env struct {
			ID     *int64          `json:"id"`
			Method string          `json:"method"`
			Params json.RawMessage `json:"params"`
		}
		if json.Unmarshal(sc.Bytes(), &env) != nil || env.ID == nil {
			continue // bildirimler cevapsız
		}
		reply := func(result any) {
			_ = out.Encode(map[string]any{"jsonrpc": "2.0", "id": *env.ID, "result": result})
		}
		switch env.Method {
		case "initialize":
			// Yanıttan önce bir bildirim — okuyucu ayrıştırmalı.
			_ = out.Encode(map[string]any{"jsonrpc": "2.0", "method": notifyListChanged})
			reply(map[string]any{"protocolVersion": protocolVersion})
		case "tools/list":
			reply(map[string]any{"tools": []map[string]any{{
				"name": "echo", "description": "yansıt",
				"inputSchema": map[string]any{"type": "object"},
			}}})
		case "tools/call":
			var p struct {
				Arguments json.RawMessage `json:"arguments"`
			}
			_ = json.Unmarshal(env.Params, &p)
			reply(map[string]any{"content": []map[string]any{
				{"type": "text", "text": "yankı:" + string(p.Arguments)},
			}})
		case "slow":
			time.Sleep(5 * time.Second)
			reply(map[string]any{})
		default:
			_ = out.Encode(map[string]any{"jsonrpc": "2.0", "id": *env.ID,
				"error": map[string]any{"code": -32601, "message": "yok"}})
		}
	}
}

func helperConfig(t *testing.T) ServerConfig {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	return ServerConfig{Name: "helper", Transport: "stdio", Command: exe, Enabled: true}
}

func newHelperTransport(t *testing.T) *stdioTransport {
	t.Helper()
	cfg := helperConfig(t)
	t.Setenv(helperEnv, "1")
	tr, err := newStdioTransport(cfg)
	if err != nil {
		t.Fatalf("stdio başlatılamadı: %v", err)
	}
	return tr
}

func TestStdioEndToEnd(t *testing.T) {
	tr := newHelperTransport(t)
	cl := NewClient(tr)
	defer cl.Close()

	tools, trunc, err := cl.ListTools(context.Background())
	if err != nil || trunc || len(tools) != 1 || tools[0].Name != "echo" {
		t.Fatalf("ListTools: %v trunc=%v %+v", err, trunc, tools)
	}
	text, isErr, err := cl.CallTool(context.Background(), "echo", json.RawMessage(`{"x":1}`))
	if err != nil || isErr || !strings.Contains(text, `yankı:{"x":1}`) {
		t.Fatalf("CallTool: %q isErr=%v err=%v", text, isErr, err)
	}
	// initialize'ın yanıt ÖNCESİ bastığı bildirim kanalda olmalı.
	select {
	case m := <-tr.Notifications():
		if m != notifyListChanged {
			t.Errorf("bildirim=%q", m)
		}
	default:
		t.Error("stdio bildirimi kanala düşmedi")
	}
}

func TestStdioCallHonorsContext(t *testing.T) {
	tr := newHelperTransport(t)
	defer tr.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	var res struct{}
	if err := tr.Call(ctx, "slow", map[string]any{}, &res); err == nil {
		t.Fatal("ctx tavanı dolmasına rağmen Call döndü")
	}
	// Taşıma hâlâ sağlıklı: sıradaki çağrı çalışmalı (asılı istek
	// pending'de sızıntı bırakmamalı).
	var initRes struct{}
	if err := tr.Call(context.Background(), "initialize", map[string]any{}, &initRes); err != nil {
		t.Fatalf("timeout sonrası taşıma bozuldu: %v", err)
	}
	tr.mu.Lock()
	n := len(tr.pending)
	tr.mu.Unlock()
	if n != 0 {
		t.Errorf("pending sızıntısı: %d", n)
	}
}

func TestStdioCloseTerminatesProcess(t *testing.T) {
	tr := newHelperTransport(t)
	if err := tr.Close(); err != nil && !strings.Contains(err.Error(), "exit") {
		// Helper EOF'ta 0 ile çıkar; farklı bir hata kapanışın
		// takılmadığı sürece kabul.
		t.Logf("kapanış notu: %v", err)
	}
	select {
	case <-tr.done:
	case <-time.After(2 * time.Second):
		t.Fatal("okuyucu kapanmadı — süreç asılı olabilir")
	}
	// Kapalı taşımada çağrı DÜRÜST hata verir.
	if err := tr.Call(context.Background(), "initialize", nil, nil); err == nil {
		t.Error("kapalı taşımada Call hata vermedi")
	}
}

func TestDialTransportValidation(t *testing.T) {
	if _, err := DialTransport(ServerConfig{Name: "x", Transport: "http"}); err == nil {
		t.Error("url'siz http kabul edildi")
	}
	if _, err := DialTransport(ServerConfig{Name: "x", Transport: "stdio"}); err == nil {
		t.Error("komutsuz stdio kabul edildi")
	}
	if _, err := DialTransport(ServerConfig{Name: "x", Transport: "gopher"}); err == nil {
		t.Error("bilinmeyen taşıma kabul edildi")
	}
}
