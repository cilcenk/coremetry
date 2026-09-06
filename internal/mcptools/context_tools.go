package mcptools

// context_tools.go — v0.10.478 (CoSRE Telemetry Agent Faz 4, F4-1; audit G9):
// set_context / get_context / clear_context. Bağlam SUNUCUDA (api/
// chat_context.go, konuşma başına Redis); tool'lar Deps kapanışlarıyla
// okur/yazar. Dış MCP istemcisinde konuşma yoktur → dürüst hata.

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/cilcenk/coremetry/internal/mcp"
)

var contextFieldProps = map[string]any{
	"cluster":     map[string]any{"type": "string"},
	"namespace":   map[string]any{"type": "string"},
	"workload":    map[string]any{"type": "string"},
	"service":     map[string]any{"type": "string"},
	"pod":         map[string]any{"type": "string"},
	"range_s":     map[string]any{"type": "integer", "minimum": 60, "maximum": 2592000},
	"errors_only": map[string]any{"type": "boolean"},
	"filters":     map[string]any{"type": "array", "items": map[string]any{"type": "object"}, "description": "chstore FilterExpr[] ({k,op,v}) — search_traces'in filters_applied çıktısını aynen geç."},
	"search_text": map[string]any{"type": "string"},
}

func noContextErr() error {
	return fmt.Errorf("sohbet bağlamı yalnız in-app CoSRE sohbetinde tutulur (bu çağrıda konuşma yok)")
}

func setContextTool(d Deps) mcp.Tool {
	return mcp.Tool{
		Name:             "set_context",
		ShortDescription: "Sohbetin AKTİF çalışma kümesini günceller (cluster, namespace, workload, servis, pod, pencere, yalnız-hata, süzgeçler). Her çözüm/aramadan sonra değişeni yaz; hafızana güvenme.",
		Description: "Update the conversation's active context stored on the server — the working set later turns resolve against (\"onun içinde\", \"aynı filtreyle\", \"son 1 saate genişlet\"). " +
			"Pass ONLY the fields that changed; unknown fields are rejected. Call it after every successful entity resolution or search. range_s marks the window as explicit " +
			"(it then beats the screen range). Returns the full context. Not available to external MCP clients (no conversation).",
		InputSchema: map[string]any{"type": "object", "properties": contextFieldProps},
		MinRole:     "",
		Handler: func(ctx context.Context, raw json.RawMessage) (any, error) {
			if d.CtxSet == nil {
				return nil, noContextErr()
			}
			patch := map[string]any{}
			if len(raw) > 0 {
				if err := json.Unmarshal(raw, &patch); err != nil {
					return nil, fmt.Errorf("decode args: %w", err)
				}
			}
			if len(patch) == 0 {
				return nil, fmt.Errorf("set_context: en az bir alan ver")
			}
			out, err := d.CtxSet(ctx, patch)
			if err != nil {
				return nil, err
			}
			return map[string]any{"context": out}, nil
		},
	}
}

func getContextTool(d Deps) mcp.Tool {
	return mcp.Tool{
		Name:             "get_context",
		ShortDescription: "Sohbetin aktif çalışma kümesini okur (sunucuda tutulur). Bir tool argümanı boşsa önce buraya bak; önceki turları hatırlamaya çalışma.",
		Description: "Read the conversation's active context (cluster, namespace, workload, service, pod, range_s, errors_only, filters, search_text, last_intent) stored on the server. " +
			"Use it to fill empty tool arguments instead of relying on your memory of earlier turns. Empty object = nothing set yet.",
		InputSchema: map[string]any{"type": "object", "properties": map[string]any{}},
		MinRole:     "",
		Handler: func(ctx context.Context, raw json.RawMessage) (any, error) {
			if d.CtxGet == nil {
				return nil, noContextErr()
			}
			out, err := d.CtxGet(ctx)
			if err != nil {
				return nil, err
			}
			return map[string]any{"context": out}, nil
		},
	}
}

func clearContextTool(d Deps) mcp.Tool {
	return mcp.Tool{
		Name:             "clear_context",
		ShortDescription: "Aktif çalışma kümesinden alan(lar)ı siler; alan verilmezse hepsini. Operatör konu değiştirdiğinde varlık alanlarını sil, pencereyi koru.",
		Description: "Clear fields of the conversation's active context (fields[]: cluster, namespace, workload, service, pod, range_s, errors_only, filters, search_text). " +
			"No fields = clear everything. When the operator switches subject, clear the entity fields but keep range_s unless they changed it.",
		InputSchema: map[string]any{"type": "object", "properties": map[string]any{"fields": map[string]any{"type": "array", "items": map[string]any{"type": "string"}}}},
		MinRole:     "",
		Handler: func(ctx context.Context, raw json.RawMessage) (any, error) {
			if d.CtxClear == nil {
				return nil, noContextErr()
			}
			var a struct {
				Fields []string `json:"fields,omitempty"`
			}
			if len(raw) > 0 {
				if err := json.Unmarshal(raw, &a); err != nil {
					return nil, fmt.Errorf("decode args: %w", err)
				}
			}
			out, err := d.CtxClear(ctx, a.Fields)
			if err != nil {
				return nil, err
			}
			return map[string]any{"context": out}, nil
		},
	}
}
