package api

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/cilcenk/coremetry/internal/mcp"
)

// v0.10.480 (Faz 4, G12) — in-app tool çağrısı audit ayrıntısı: tel ile aynı
// alanlar, transport farkı, gövde YOK, arg önizlemesi kırpık.

func TestChatToolAuditDetails(t *testing.T) {
	long := json.RawMessage(`{"service":"` + strings.Repeat("x", 400) + `"}`)
	s := chatToolAuditDetails("search_traces", long, 1500*time.Millisecond, errors.New("code: 159, Timeout exceeded"), 0)
	var m map[string]any
	if err := json.Unmarshal([]byte(s), &m); err != nil {
		t.Fatal(err)
	}
	if m["tool"] != "search_traces" || m["transport"] != "chat-inapp" || m["ok"] != false || m["errorClass"] != "timeout" || m["durationMs"] != float64(1500) {
		t.Errorf("alanlar: %v", m)
	}
	if p := m["argsPreview"].(string); len([]rune(p)) > 257 || !strings.HasSuffix(p, "…") {
		t.Errorf("önizleme kırpılmalı: %d", len([]rune(p)))
	}
	ok := chatToolAuditDetails("get_trace", json.RawMessage(`{"trace_id":"a"}`), time.Millisecond, nil, 42)
	if !strings.Contains(ok, `"ok":true`) || !strings.Contains(ok, `"resultBytes":42`) || strings.Contains(ok, `"errorClass":"t`) {
		t.Errorf("başarılı çağrı: %s", ok)
	}
	// Tel yolu ile aynı anahtar kümesi (transport hariç): audit okuyucusu tek şema görür.
	var w map[string]any
	_ = json.Unmarshal([]byte(mcpToolAuditDetails("x", nil, time.Second, mcpCallOutcomeForTest())), &w)
	for k := range w {
		if _, ok := m[k]; !ok {
			t.Errorf("tel anahtarı %q in-app ayrıntısında yok", k)
		}
	}
}

func mcpCallOutcomeForTest() mcp.CallOutcome { return mcp.CallOutcome{ResultBytes: 1} }
