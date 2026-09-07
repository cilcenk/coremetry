package api

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/cilcenk/coremetry/internal/copilot"
)

// ai_call_matrix_test.go — v0.10.533 (CoSRE v2 Faz 2.1): yedi sarmalayıcı
// tek kurucudan (aiCall) geçer. Bu test matrisin HER hücresinde sözleşmeyi
// gerçek copilot.Service + sahte sağlayıcı üzerinden doğrular: ExchangeID
// ctx'ten taşınır (v0.9.593 sınıfı), surface doğru, JSON kipi gövdeye iner,
// maskeli örnek ai_calls'a yazılır ama gerçek prompt modele gider, akan yol
// delta üretir. Kaynak-pin testleri (ai_shield/chat_span) şeklini, bu test
// davranışını çiviler.

type matrixProvider struct {
	mu     sync.Mutex
	bodies []map[string]any
	srv    *httptest.Server
}

func newMatrixProvider(t *testing.T) *matrixProvider {
	t.Helper()
	p := &matrixProvider{}
	p.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		var m map[string]any
		_ = json.Unmarshal(raw, &m)
		p.mu.Lock()
		p.bodies = append(p.bodies, m)
		p.mu.Unlock()
		if ws, _ := m["stream"].(bool); ws {
			w.Header().Set("Content-Type", "text/event-stream")
			for _, d := range []string{"{\"ok\":", "true}"} {
				chunk, _ := json.Marshal(map[string]any{"choices": []any{map[string]any{"delta": map[string]any{"content": d}}}})
				_, _ = w.Write([]byte("data: " + string(chunk) + "\n\n"))
			}
			_, _ = w.Write([]byte("data: {\"choices\":[],\"usage\":{\"prompt_tokens\":5,\"completion_tokens\":2}}\n\ndata: [DONE]\n\n"))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		out, _ := json.Marshal(map[string]any{
			"choices": []any{map[string]any{"message": map[string]any{"content": `{"ok":true}`}, "finish_reason": "stop"}},
			"usage":   map[string]any{"prompt_tokens": 5, "completion_tokens": 2},
		})
		_, _ = w.Write(out)
	}))
	t.Cleanup(p.srv.Close)
	return p
}

func (p *matrixProvider) last() map[string]any {
	p.mu.Lock()
	defer p.mu.Unlock()
	if len(p.bodies) == 0 {
		return nil
	}
	return p.bodies[len(p.bodies)-1]
}

type chanRecorder struct{ ch chan copilot.CallRecord }

func (c *chanRecorder) RecordCall(_ context.Context, r copilot.CallRecord) { c.ch <- r }

func (c *chanRecorder) next(t *testing.T) copilot.CallRecord {
	t.Helper()
	select {
	case r := <-c.ch:
		return r
	case <-time.After(3 * time.Second):
		t.Fatal("ai_calls kaydı gelmedi")
		return copilot.CallRecord{}
	}
}

func TestAICallMatrix(t *testing.T) {
	prov := newMatrixProvider(t)
	cop := copilot.New(copilot.ProviderOpenAI, "test-key", "gemma4")
	cop.Configure(copilot.ProviderOpenAI, "test-key", "gemma4", prov.srv.URL, false, true)
	rec := &chanRecorder{ch: make(chan copilot.CallRecord, 8)}
	cop.SetRecorder(rec)
	s := &Server{copilot: cop}

	reqWith := func(xid string) *http.Request {
		r := httptest.NewRequest(http.MethodPost, "/api/copilot/explain-problem/p1", nil)
		return r.WithContext(copilot.WithMeta(r.Context(), copilot.CallMeta{ExchangeID: xid}))
	}
	ctxWith := func(xid string) context.Context {
		return copilot.WithMeta(context.Background(), copilot.CallMeta{ExchangeID: xid, UserID: "u1", UserEmail: "u1@x"})
	}
	schema := map[string]any{"type": "object", "properties": map[string]any{"ok": map[string]any{"type": "boolean"}}}

	cases := []struct {
		name        string
		call        func() (string, error)
		surface     string
		xid         string
		wantJSON    bool
		wantSample  string // prompt_sample bu metni İÇERMELİ
		wantNoCode  bool   // maskeli: gerçek kod örnekte OLMAMALI
		wantDeltas  bool
		wantUser    string
	}{
		{"copilotExplain", func() (string, error) { return s.copilotExplain(reqWith("x1"), "SYS", "USER") }, "explain-problem", "x1", false, "USER", false, false, ""},
		{"copilotExplainStream", func() (string, error) {
			var got []string
			out, err := s.copilotExplainStream(reqWith("x2"), "SYS", "USER", func(d string) { got = append(got, d) })
			if len(got) == 0 {
				t.Errorf("akan sarmalayıcı delta üretmedi")
			}
			return out, err
		}, "explain-problem", "x2", false, "USER", false, true, ""},
		{"copilotExplainMasked", func() (string, error) {
			return s.copilotExplainMasked(reqWith("x3"), "SYS", "USER SECRETCODE", "USER [kod: repo/f.go:1-2]")
		}, "explain-problem", "x3", false, "[kod: repo/f.go:1-2]", true, false, ""},
		{"copilotExplainJSON", func() (string, error) { return s.copilotExplainJSON(reqWith("x4"), "SYS", "USER", schema) }, "explain-problem", "x4", true, "USER", false, false, ""},
		{"copilotExplainSurface", func() (string, error) { return s.copilotExplainSurface(ctxWith("x5"), "chat-guided", "SYS", "USER") }, "chat-guided", "x5", false, "USER", false, false, "u1"},
		{"copilotStreamSurface", func() (string, error) {
			return s.copilotStreamSurface(ctxWith("x6"), "chat-drawer", "SYS", "USER", func(string) {})
		}, "chat-drawer", "x6", false, "USER", false, true, "u1"},
		{"copilotExplainJSONSurface", func() (string, error) {
			return s.copilotExplainJSONSurface(ctxWith("x7"), "rootcause-verdict", "SYS", "USER", schema)
		}, "rootcause-verdict", "x7", true, "USER", false, false, "u1"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			out, err := c.call()
			if err != nil || !strings.Contains(out, "ok") {
				t.Fatalf("çağrı: %q %v", out, err)
			}
			r := rec.next(t)
			if r.ExchangeID != c.xid {
				t.Errorf("ExchangeID taşınmadı: %q != %q", r.ExchangeID, c.xid)
			}
			if r.Surface != c.surface {
				t.Errorf("surface %q, want %q", r.Surface, c.surface)
			}
			if c.wantUser != "" && r.UserID != c.wantUser {
				t.Errorf("ctx kullanıcı meta'sı korunmalı: %q", r.UserID)
			}
			if !strings.Contains(r.PromptSample, c.wantSample) {
				t.Errorf("prompt_sample %q, want ⊇ %q", r.PromptSample, c.wantSample)
			}
			if c.wantNoCode && strings.Contains(r.PromptSample, "SECRETCODE") {
				t.Errorf("maskeli yolda gerçek kod ai_calls'a sızdı")
			}
			body := prov.last()
			if body == nil {
				t.Fatal("sağlayıcı gövdesi yok")
			}
			if c.wantNoCode {
				msgs, _ := json.Marshal(body["messages"])
				if !strings.Contains(string(msgs), "SECRETCODE") {
					t.Errorf("maskeli yolda GERÇEK prompt modele gitmeli")
				}
			}
			_, hasRF := body["response_format"]
			if hasRF != c.wantJSON {
				t.Errorf("response_format var=%v, want %v", hasRF, c.wantJSON)
			}
			if ws, _ := body["stream"].(bool); ws != c.wantDeltas {
				t.Errorf("stream=%v, want %v", ws, c.wantDeltas)
			}
		})
	}
}
