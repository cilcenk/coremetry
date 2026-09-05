package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"
)

// v0.10.427 — M4: notifications/cancelled. Kanonik id, SSE yolunda iptal
// handler'a ulaşır ve yanıt bastırılır; bilinmeyen/tamamlanmış id no-op;
// initialize kaydedilmez; streamable yok sayılır (oturumsuz).

func TestCanonicalID(t *testing.T) {
	cases := map[string]string{`1`: `1`, ` 1 `: `1`, `1.0`: `1`, `"1"`: `"1"`, `"a b"`: `"a b"`, `{`: `{`,
		// v0.10.430 — 2^53 üstü tam sayılar ayrı kalır (float64'e çökmez).
		`9007199254740993`: `9007199254740993`, `1735689600123456789`: `1735689600123456789`, `1e2`: `100`}
	if canonicalID(json.RawMessage(`9007199254740992`)) == canonicalID(json.RawMessage(`9007199254740993`)) {
		t.Fatal("komşu 64-bit id'ler aynı anahtara çökmemeli")
	}
	for in, want := range cases {
		if got := canonicalID(json.RawMessage(in)); got != want {
			t.Errorf("%q → %q, want %q", in, got, want)
		}
	}
	if cancellable("initialize") || cancellable("ping") || cancellable("tools/list") || !cancellable("tools/call") || !cancellable("resources/read") || !cancellable("prompts/get") {
		t.Fatal("iptal edilebilir yöntem kümesi")
	}
}

func TestCancelledStopsToolAndSuppressesResponse(t *testing.T) {
	srv, ts := testServer(t)
	started := make(chan struct{}, 1)
	sawCancel := make(chan bool, 1)
	srv.RegisterTool(Tool{
		Name: "slow_tool", Description: "t", InputSchema: map[string]any{"type": "object"},
		Handler: func(ctx context.Context, _ json.RawMessage) (any, error) {
			started <- struct{}{}
			select {
			case <-ctx.Done():
				sawCancel <- true
				return nil, ctx.Err()
			case <-time.After(5 * time.Second):
				sawCancel <- false
				return map[string]any{"late": true}, nil
			}
		},
	})
	sess := srv.newSession() // SSE okuyucusu yok; sess.out tampondan okunur
	t.Cleanup(func() { srv.removeSession(sess.id) })
	post := func(body []byte) *http.Response {
		resp, err := http.Post(ts.URL+"/api/mcp/messages?sessionId="+sess.id, "application/json", bytes.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		return resp
	}
	done := make(chan int, 1)
	go func() {
		resp := post(rpc("tools/call", 7, `{"name":"slow_tool","arguments":{}}`))
		done <- resp.StatusCode
	}()
	<-started
	cancelBody := []byte(`{"jsonrpc":"2.0","method":"notifications/cancelled","params":{"requestId":7.0,"reason":"user aborted"}}`)
	if resp := post(cancelBody); resp.StatusCode != http.StatusAccepted {
		t.Fatalf("bildirim 202 olmalı, %d", resp.StatusCode)
	}
	select {
	case ok := <-sawCancel:
		if !ok {
			t.Fatal("handler iptali görmedi (5 sn bekledi)")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("handler dönmedi")
	}
	if code := <-done; code != http.StatusAccepted {
		t.Fatalf("iptal edilen istek POST'u 202 olmalı, %d", code)
	}
	select {
	case raw := <-sess.out:
		t.Fatalf("iptal edilen isteğe yanıt gönderilmemeli: %s", raw)
	case <-time.After(150 * time.Millisecond):
	}
	// Tamamlanmış/bilinmeyen id: sessiz no-op, defter temiz.
	post([]byte(`{"jsonrpc":"2.0","method":"notifications/cancelled","params":{"requestId":7}}`))
	post([]byte(`{"jsonrpc":"2.0","method":"notifications/cancelled","params":{"requestId":999}}`))
	time.Sleep(50 * time.Millisecond)
	sess.mu.Lock()
	n, m := len(sess.inflight), len(sess.cancelled)
	sess.mu.Unlock()
	if n != 0 || m != 0 {
		t.Fatalf("defter sızdırıyor: inflight=%d cancelled=%d", n, m)
	}
	// İptalsiz normal çağrı yanıtı hâlâ gelir.
	post(rpc("tools/call", 8, `{"name":"echo_tool","arguments":{"x":1}}`))
	select {
	case raw := <-sess.out:
		if !strings.Contains(string(raw), `"id":8`) {
			t.Fatalf("beklenmeyen yanıt: %s", raw)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("normal yanıt gelmedi")
	}
}

// Streamable (oturumsuz): iptal bildirimi 202 ile yutulur, hata yok.
func TestCancelledIgnoredOnStreamable(t *testing.T) {
	_, ts := testServer(t)
	resp, _ := postStreamable(t, ts, []byte(`{"jsonrpc":"2.0","method":"notifications/cancelled","params":{"requestId":1}}`))
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("202 bekleniyor, %d", resp.StatusCode)
	}
}

// Kaynak pini: SSE yolu track/finish ile sarar; bildirim initialize'dan
// önce ele alınır; bu paket coremetry import'suz kalır (toolerr_test).
func TestCancelWiring(t *testing.T) {
	b, err := os.ReadFile("mcp.go")
	if err != nil {
		t.Fatal(err)
	}
	src := string(b)
	if !strings.Contains(src, "sess.track(r.Context(), &req)") || !strings.Contains(src, "if finish() {") {
		t.Fatal("HandleMessage isteği track/finish ile sarmıyor")
	}
	if !strings.Contains(src, `case "notifications/cancelled":`) {
		t.Fatal("dispatchNotification cancelled'ı ele almıyor")
	}
	if strings.Index(src, `case "notifications/cancelled":`) > strings.Index(src, "if sess == nil {\n\t\treturn\n\t}\n\tswitch req.Method {") && strings.Contains(src, "if sess == nil {\n\t\treturn\n\t}\n\tswitch req.Method {") {
		t.Fatal("cancelled dalı sess==nil erken dönüşünden ÖNCE gelmeli (oturumsuz yolda da yutulmalı, panik değil)")
	}
}
